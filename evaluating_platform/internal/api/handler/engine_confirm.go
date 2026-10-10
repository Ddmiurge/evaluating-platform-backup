package handler

// engine_confirm.go — Phase-1 BFF confirm fast path for promptfoo engine
// plan cards (see docs/architecture/promptfoo-generation-options.md).
//
// When a confirmed plan selects only promptfoo engine capabilities, the BFF
// orchestrates the engine run itself (prepare → background wait → evidence +
// report) instead of re-entering the MaClaw agent loop, whose LLM
// tool-calling cannot reliably execute the engine tool. Discovery and
// planning stay with MaClaw; execution stays platform-controlled. Safe
// progress metadata only in every response.

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
)

const (
	promptfooEngineJobIDPrefix = maclaw.EngineJobIDPrefix
	promptfooEngineWaitTimeout = 15 * time.Minute
	promptfooEngineTickRefresh = 2 * time.Second
)

// promptfooEngineRefsFromPlan extracts the promptfoo plugin ids selected by a
// plan card. Returns ok=false when the plan selects no engine capability.
func promptfooEngineRefsFromPlan(content string) ([]string, bool) {
	body, ok := runtimePlanPayloadJSON(content)
	if !ok {
		return nil, false
	}
	plugins := map[string]bool{}
	addRef := func(ref string) {
		ref = strings.TrimSpace(ref)
		lower := strings.ToLower(ref)
		if strings.HasPrefix(lower, "promptfoo_plugin:") {
			plugins[strings.TrimSpace(ref[len("promptfoo_plugin:"):])] = true
		}
	}
	if raw, ok := body["selected_capability_refs"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				addRef(s)
			}
		}
	}
	if raw, ok := body["selected_capabilities"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			sourceType := strings.ToLower(strings.TrimSpace(fmtAnyString(firstAny(m["source_type"], m["type"]))))
			if sourceType != maclaw.CapabilitySourcePromptfooPlugin {
				continue
			}
			addRef(fmtAnyString(firstAny(m["source_ref"], m["ref"], m["capability_ref"], m["id"])))
		}
	}
	// Top-level plugins array written by MaClaw's plan cards.
	if raw, ok := body["plugins"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				if id := strings.TrimSpace(s); id != "" {
					plugins[id] = true
				}
			}
		}
	}
	out := make([]string, 0, len(plugins))
	for id := range plugins {
		if id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// confirmPromptfooEnginePlan runs the platform-side engine fast path:
// prepare synchronously (fast), then a background goroutine waits for the
// terminal state while a ticker mirrors engine progress into the job store.
func (h *MaclawRuntimeHandler) confirmPromptfooEnginePlan(c *gin.Context, sessionID, instanceID string, planMessage *maclaw.RuntimeMessage, in maclawRuntimeConfirmInput, testCount int, plugins []string) {
	if h.engineBridge == nil || h.engineJobs == nil || h.engineRunService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "promptfoo engine is not configured", "code": "engine_not_configured"})
		return
	}
	userID, err := uuid.Parse(strings.TrimSpace(c.GetString("user_id")))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid user id", "code": "invalid_user"})
		return
	}
	planBody, _ := runtimePlanPayloadJSON(planMessage.Content)
	purpose := strings.TrimSpace(fmtAnyString(planBody["note"]))
	if purpose == "" {
		purpose = strings.TrimSpace(fmtAnyString(planBody["purpose"]))
	}
	if purpose == "" {
		purpose = "大模型安全评测（promptfoo 引擎）"
	}
	judgeMode := strings.TrimSpace(fmtAnyString(planBody["judge_mode"]))
	if testCount <= 0 {
		testCount = 3
	}

	prepared, err := h.engineBridge.PreparePromptfooEngineRun(c.Request.Context(), userID, instanceID, maclaw.PromptfooEngineRunInput{
		RunID:     planMessage.ID,
		SessionID: sessionID,
		Purpose:   purpose,
		NumTests:  testCount,
		Plugins:   plugins,
		JudgeMode: judgeMode,
		Source:    maclaw.EngineRunSourceChatConfirm,
		Metadata:  map[string]string{"session_id": sessionID},
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to submit engine evaluation", "code": "engine_submit_failed", "detail": err.Error()})
		return
	}

	jobID := promptfooEngineJobIDPrefix + uuid.NewString()
	now := time.Now().UTC()
	// Write order (U5): register the in-memory job first, then bind it to the
	// already-persisted engine run. If the bind fails we mark the job failed
	// below rather than leaving a "running" job nobody is recording — a
	// disconnected PostgreSQL must be visible, never silent.
	h.engineJobs.Create(&maclaw.PlatformEngineJob{
		ID:          jobID,
		EngineRunID: prepared.Record.ID,
		UserID:      userID.String(),
		InstanceID:  instanceID,
		SessionID:   sessionID,
		Status:      maclaw.EvaluationJobStatusRunning,
		StatusText:  "promptfoo 引擎评测执行中",
		StartedAt:   now,
	})

	// U5: persist the pfj- ↔ engine_run link so a restart can recover.
	if _, linkErr := h.engineRunService.LinkJob(c.Request.Context(), userID, prepared.Record.ID, jobID); linkErr != nil {
		log.Printf("[ERROR] promptfoo engine job link failed (run=%s job=%s user=%s): %v",
			prepared.Record.ID, jobID, userID, linkErr)
		h.engineJobs.MarkPersistFailed(jobID, "engine_job_persist_failed")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to persist engine job",
			"code":  "engine_job_persist_failed",
		})
		return
	}

	// Background wait + progress mirror.
	go h.waitPromptfooEngineJob(userID, instanceID, jobID, prepared)

	job := h.engineJobs.Get(jobID)
	c.JSON(http.StatusAccepted, maclaw.EvaluationJobFromPlatformEngine(job))
}

func (h *MaclawRuntimeHandler) waitPromptfooEngineJob(userID uuid.UUID, instanceID, jobID string, prepared *maclaw.PreparedEngineRun) {
	ctx, cancel := context.WithTimeout(context.Background(), promptfooEngineWaitTimeout)
	defer cancel()

	// Progress mirror: poll the persisted engine run record until terminal.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-time.After(promptfooEngineTickRefresh):
				record, err := h.engineRunService.Get(context.Background(), userID, prepared.Record.ID)
				if err != nil || record == nil {
					continue
				}
				h.engineJobs.UpdateProgress(jobID, engineJobStatusFromPhase(record.Status), record.PlannedCount, record.ExecutedCount, record.CurrentStage, engineStatusText(record.Status), record.ErrorCode, record.DurationMs)
				if maclaw.IsEngineRunTerminal(record.Status) {
					return
				}
			}
		}
	}()

	out, err := h.engineBridge.WaitPromptfooRedteamEvaluation(ctx, userID, instanceID, prepared)
	<-done
	if err != nil {
		h.engineJobs.Complete(jobID, maclaw.EvaluationJobStatusFailed, "engine_execution_failed", "")
		return
	}
	if out.Status != "completed" {
		h.engineJobs.Complete(jobID, maclaw.EvaluationJobStatusFailed, out.ErrorCode, "")
		return
	}
	h.engineJobs.UpdateProgress(jobID, maclaw.EvaluationJobStatusRunning, out.PlannedCount, out.ExecutedCount, "compile_report", "生成报告", "", 0)
	h.engineJobs.Complete(jobID, maclaw.EvaluationJobStatusSucceeded, "", out.ReportID)
}

func engineJobStatusFromPhase(phase maclaw.EngineRunPhase) maclaw.EvaluationJobStatus {
	switch phase {
	case maclaw.EnginePhaseSucceeded:
		return maclaw.EvaluationJobStatusSucceeded
	case maclaw.EnginePhaseFailed:
		return maclaw.EvaluationJobStatusFailed
	case maclaw.EnginePhaseCanceled:
		return maclaw.EvaluationJobStatusCanceled
	case maclaw.EnginePhaseQueued:
		return maclaw.EvaluationJobStatusPending
	default:
		return maclaw.EvaluationJobStatusRunning
	}
}

func engineStatusText(phase maclaw.EngineRunPhase) string {
	switch phase {
	case maclaw.EnginePhaseQueued:
		return "排队中"
	case maclaw.EnginePhaseGeneratingTests:
		return "生成攻击用例"
	case maclaw.EnginePhaseExecutingProbes:
		return "执行探针"
	case maclaw.EnginePhaseEngineJudging:
		return "引擎判定"
	case maclaw.EnginePhaseCompilingReport:
		return "生成报告"
	case maclaw.EnginePhaseSucceeded:
		return "评测完成"
	case maclaw.EnginePhaseFailed:
		return "评测失败"
	case maclaw.EnginePhaseCanceled:
		return "已取消"
	default:
		return "执行中"
	}
}
