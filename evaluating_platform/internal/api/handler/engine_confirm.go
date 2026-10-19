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
	"sort"
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

// promptfooEnginePlanSelection is the promptfoo capabilities a plan card
// selected, split by axis.
//
// # Why strategies are in here (2026-10-16)
//
// This extractor used to read only `promptfoo_plugin:` refs. The plan card has
// carried `promptfoo_strategy:<id>` refs for as long as the capability catalog
// has emitted them — the platform builds those card refs itself
// (`CapabilitySourcePromptfooStrategy`) and maclaw relays them verbatim. So a
// plan that had selected `base64` arrived at the confirm path with the strategy
// named, and left it with `strategies = []`.
//
// Measured on ai02 before the fix (阶段三服务器验证记录 §3): the plan card read
// `["promptfoo_plugin:prompt-extraction", "promptfoo_strategy:base64"]`, the
// engine run came back with `strategies = []` and a `strategy_stats` holding
// only `none` (6 probes). The wizard path, selecting the same `base64`, applied
// it correctly — the loss was specific to this path.
//
// That is the "selected but silently dropped" shape P3-1 removed from the
// wizard, one step earlier in the chain (plan -> execution rather than
// form -> submit). It is worse here: the user never picked the strategy by hand,
// so nothing on screen gave them a reason to check whether it ran.
type promptfooEnginePlanSelection struct {
	// Plugins are the promptfoo plugin ids the plan selected.
	Plugins []string
	// Strategies are the promptfoo strategy ids the plan selected.
	Strategies []string
	// DeclaredStrategies counts `promptfoo_strategy:` refs seen BEFORE the id is
	// extracted, malformed ones included. The confirm path refuses when this is
	// non-zero but Strategies is empty: running without the strategy the plan
	// declared is precisely the silent narrowing this change exists to stop.
	DeclaredStrategies int
}

// promptfooEnginePlanSelectionFromContent reads the selection out of a plan
// card's content. ok=false means the plan declares no promptfoo capability at
// all, which is how ConfirmPlan recognises a plan that belongs to the MaClaw
// agent loop rather than the engine fast path.
func promptfooEnginePlanSelectionFromContent(content string) (promptfooEnginePlanSelection, bool) {
	body, ok := runtimePlanPayloadJSON(content)
	if !ok {
		return promptfooEnginePlanSelection{}, false
	}
	plugins := map[string]bool{}
	strategies := map[string]bool{}
	declaredStrategies := 0

	pluginPrefix := maclaw.CapabilitySourcePromptfooPlugin + ":"
	strategyPrefix := maclaw.CapabilitySourcePromptfooStrategy + ":"

	// addRef splits one capability ref by axis. A ref whose id is empty is
	// counted but not collected — the caller needs to know the plan *meant* to
	// select something even when what it wrote cannot be used.
	addRef := func(value any) {
		ref := strings.TrimSpace(fmtAnyString(value))
		if ref == "" {
			return
		}
		lower := strings.ToLower(ref)
		switch {
		case strings.HasPrefix(lower, pluginPrefix):
			if id := strings.TrimSpace(ref[len(pluginPrefix):]); id != "" {
				plugins[id] = true
			}
		case strings.HasPrefix(lower, strategyPrefix):
			declaredStrategies++
			if id := strings.TrimSpace(ref[len(strategyPrefix):]); id != "" {
				strategies[id] = true
			}
		}
	}
	addID := func(set map[string]bool, value any) {
		if id := strings.TrimSpace(fmtAnyString(value)); id != "" {
			set[id] = true
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
			if sourceType != maclaw.CapabilitySourcePromptfooPlugin && sourceType != maclaw.CapabilitySourcePromptfooStrategy {
				continue
			}
			addRef(firstAny(m["source_ref"], m["ref"], m["capability_ref"], m["id"]))
		}
	}
	// Top-level plugins/strategies arrays written by MaClaw's plan cards.
	if raw, ok := body["plugins"].([]any); ok {
		for _, item := range raw {
			addID(plugins, item)
		}
	}
	if raw, ok := body["strategies"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				declaredStrategies++
				addID(strategies, s)
				continue
			}
			if m, ok := item.(map[string]any); ok {
				declaredStrategies++
				addID(strategies, firstAny(m["id"], m["strategy_id"], m["source_ref"]))
			}
		}
	}

	selection := promptfooEnginePlanSelection{
		Plugins:            sortedIDSet(plugins),
		Strategies:         sortedIDSet(strategies),
		DeclaredStrategies: declaredStrategies,
	}
	if len(selection.Plugins) == 0 && len(selection.Strategies) == 0 && selection.DeclaredStrategies == 0 {
		return promptfooEnginePlanSelection{}, false
	}
	return selection, true
}

// sortedIDSet renders a deduplicating id set as a sorted slice, so a plan's
// selection reaches the engine in a stable order (and the run record reads the
// same on every confirm).
func sortedIDSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		if id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Error codes for a plan that cannot be executed as confirmed.
const (
	// EnginePlanErrNoPlugins — the plan selected no attack plugin, so there is
	// nothing to generate cases from. Strategies only wrap; they produce none.
	EnginePlanErrNoPlugins = "promptfoo_plan_has_no_plugins"
	// EnginePlanErrStrategyRefsUnresolved — the plan declared at least one
	// `promptfoo_strategy:` ref but none yielded a usable id. Executing anyway
	// would silently downgrade the evaluation to "ask the target directly", which
	// is the exact failure this path was fixed for.
	EnginePlanErrStrategyRefsUnresolved = "promptfoo_strategy_refs_unresolved"
)

// promptfooEnginePlanRefusal reports why a confirmed plan cannot be executed as
// declared, or refused=false when it can.
//
// Both refusals exist because the alternative is a run *narrower* than the card
// the user just approved, with nothing on screen saying so — measured on ai02
// before this change (阶段三服务器验证记录 §3): the card named a strategy, the
// run had none. A refusal costs one round trip; the silent version cost an
// evaluation the user believed had exercised a technique it never sent.
//
// A pure predicate on the selection, so it holds no matter which caller
// assembled the selection, and so the rule is testable without an HTTP harness.
func promptfooEnginePlanRefusal(selection promptfooEnginePlanSelection) (code, message string, refused bool) {
	if len(selection.Plugins) == 0 {
		return EnginePlanErrNoPlugins,
			"计划卡没有选择任何 promptfoo 攻击插件，没有插件就无法生成攻击用例。请在计划卡上重新选择后再确认。",
			true
	}
	if selection.DeclaredStrategies > 0 && len(selection.Strategies) == 0 {
		return EnginePlanErrStrategyRefsUnresolved,
			"计划卡声明了攻击策略，但没有任何一个能解析出可用 id（ref 形如 promptfoo_strategy:<id>）。按计划执行会把这次评测降级成「无策略直问」，所以这里拒绝而不是继续。",
			true
	}
	return "", "", false
}

// confirmPromptfooEnginePlan runs the platform-side engine fast path:
// prepare synchronously (fast), then a background goroutine waits for the
// terminal state while a ticker mirrors engine progress into the job store.
func (h *MaclawRuntimeHandler) confirmPromptfooEnginePlan(c *gin.Context, sessionID, instanceID string, planMessage *maclaw.RuntimeMessage, in maclawRuntimeConfirmInput, testCount int, selection promptfooEnginePlanSelection) {
	if h.engineBridge == nil || h.engineJobs == nil || h.engineRunService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "promptfoo engine is not configured", "code": "engine_not_configured"})
		return
	}
	userID, err := uuid.Parse(strings.TrimSpace(c.GetString("user_id")))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid user id", "code": "invalid_user"})
		return
	}
	// Fail closed on a selection we cannot execute exactly as planned. The
	// predicate is a pure function so the rule can be tested without an HTTP
	// harness — see promptfooEnginePlanRefusal.
	if code, message, refused := promptfooEnginePlanRefusal(selection); refused {
		c.JSON(http.StatusConflict, gin.H{"error": message, "code": code})
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
		RunID:      planMessage.ID,
		SessionID:  sessionID,
		Purpose:    purpose,
		NumTests:   testCount,
		Plugins:    selection.Plugins,
		Strategies: selection.Strategies,
		JudgeMode:  judgeMode,
		Source:     maclaw.EngineRunSourceChatConfirm,
		Metadata:   map[string]string{"session_id": sessionID},
	})
	if err != nil {
		// Pre-flight refusals are request problems, not upstream failures. Mapping
		// them to 502 would tell the user the engine broke when the truth is that
		// the confirmed plan needs a different capability — and the plan card is
		// the only place it can be changed.
		if writeEnginePreflightRejection(c, err) {
			return
		}
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
		// The card the user confirmed. The session snapshot uses it to place the
		// report card next to its plan — see appendCompletedEngineReportMessages.
		PlanMessageID: planMessage.ID,
		UserID:        userID.String(),
		InstanceID:    instanceID,
		SessionID:     sessionID,
		Status:        maclaw.EvaluationJobStatusRunning,
		StatusText:    "promptfoo 引擎评测执行中",
		StartedAt:     now,
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
