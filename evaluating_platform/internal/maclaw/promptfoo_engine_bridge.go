package maclaw

// promptfoo_engine_bridge.go — Phase 1 of the merge plan: coarse bridge tools
// that let MaClaw discover and run promptfoo-engine evaluations.
//
// Three tools are layered on the existing bridge:
//   SearchRedteamPluginCatalog      — built-in plugin/strategy catalog search
//   RunPromptfooRedteamEvaluation   — confirmed engine run orchestration
//   GetPromptfooEvaluationResult    — safe status/result for one engine run
//
// Security contract mirrors execute_redteam_evaluation_batch: raw attack
// prompts, target responses, and credentials never cross this boundary — the
// engine reduces everything to SafeRunResult (counts/severity/categories)
// before it reaches here, and only sanitized findings/evidence handles are
// persisted and returned.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"evaluating_platform/internal/maclaw/redteam"
	"github.com/google/uuid"
)

// PromptfooEngineRunner abstracts the engine REST client for bridge tests.
// *PromptfooEngineClient satisfies it.
type PromptfooEngineRunner interface {
	Enabled() bool
	CreateRun(ctx context.Context, run *EngineRunRequest) error
	GetRun(ctx context.Context, runID string) (*EngineRunStatus, error)
}

const (
	promptfooEnginePollInterval = 2 * time.Second
	promptfooEnginePollTimeout  = 12 * time.Minute
	promptfooEngineDefaultTests = 5
	promptfooEngineMaxTests     = 50
)

// SearchRedteamPluginCatalogInput is the search tool input.
type SearchRedteamPluginCatalogInput struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// SearchRedteamPluginCatalogOutput is the search tool output (safe cards only).
type SearchRedteamPluginCatalogOutput struct {
	Cards       []CapabilityCard `json:"cards,omitempty"`
	Total       int              `json:"total"`
	Engine      string           `json:"engine"`
	Description string           `json:"description,omitempty"`
}

// PromptfooEngineRunInput is the confirmed-run tool input from MaClaw.
type PromptfooEngineRunInput struct {
	RunID      string            `json:"run_id,omitempty"`
	SessionID  string            `json:"session_id,omitempty"`
	Purpose    string            `json:"purpose,omitempty"`
	NumTests   int               `json:"num_tests,omitempty"`
	Plugins    []string          `json:"plugins,omitempty"`
	Strategies []string          `json:"strategies,omitempty"`
	JudgeMode  string            `json:"judge_mode,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`

	ExecutionUserID    uuid.UUID `json:"-"`
	ExecutionSessionID string    `json:"-"`
}

// PromptfooEngineRunOutput mirrors the batch tool output shape so MaClaw and
// the report pipeline treat both engines uniformly.
type PromptfooEngineRunOutput struct {
	RunID           string            `json:"run_id"`
	EngineRunID     string            `json:"engine_run_id"`
	Status          string            `json:"status"`
	ErrorCode       string            `json:"error_code,omitempty"`
	PlannedCount    int               `json:"planned_count"`
	ExecutedCount   int               `json:"executed_count"`
	Counts          map[string]int    `json:"counts"`
	ReportID        string            `json:"report_id,omitempty"`
	EvidenceHandles []string          `json:"evidence_handles,omitempty"`
	StageDurations  map[string]int64  `json:"stage_durations,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// PromptfooEngineResultOutput is the get-result tool output (safe fields only).
type PromptfooEngineResultOutput struct {
	EngineRunID   string            `json:"engine_run_id"`
	Phase         string            `json:"phase"`
	ErrorCode     string            `json:"error_code,omitempty"`
	PlannedCount  int               `json:"planned_count"`
	ExecutedCount int               `json:"executed_count"`
	DurationMs    int64             `json:"duration_ms"`
	Result        *EngineSafeResult `json:"result,omitempty"`
}

// SetPromptfooEngine wires the engine dependencies into the bridge. All parts
// are optional: the engine tools fail with a clear error when absent.
func (b *RedteamToolBridge) SetPromptfooEngine(runner PromptfooEngineRunner, runs *EngineRunService, targets EngineTargetProvider, generation EngineGenerationProvider) {
	if b == nil {
		return
	}
	b.engineRunner = runner
	b.engineRuns = runs
	b.engineTargets = targets
	b.engineGeneration = generation
}

func (b *RedteamToolBridge) promptfooEngineReady() error {
	if b == nil || b.engineRunner == nil || !b.engineRunner.Enabled() || b.engineRuns == nil || b.engineTargets == nil {
		return errors.New("promptfoo engine is not configured")
	}
	return nil
}

// SearchRedteamPluginCatalog searches the built-in promptfoo plugin/strategy
// catalog. Pure static data — safe metadata only.
func (b *RedteamToolBridge) SearchRedteamPluginCatalog(in SearchRedteamPluginCatalogInput) (*SearchRedteamPluginCatalogOutput, error) {
	cards := SearchPromptfooPluginCatalog(in.Query, in.Limit)
	return &SearchRedteamPluginCatalogOutput{
		Cards:       cards,
		Total:       len(cards),
		Engine:      "promptfoo",
		Description: "内置引擎评测插件与策略目录。确认执行时选择 plugin_id 传入 run_promptfoo_redteam_evaluation。",
	}, nil
}

// PreparedEngineRun is the synchronous part of a confirmed engine run: the
// engine run record plus the normalized input, before polling starts.
type PreparedEngineRun struct {
	Record         *EngineRunRecord
	NormalizedIn   PromptfooEngineRunInput
	StageDurations map[string]int64
}

// PreparePromptfooEngineRun validates, materializes credentials, persists the
// engine run record, and submits it to the engine. Returns immediately with
// the engine run id (the engine executes in its own background worker).
func (b *RedteamToolBridge) PreparePromptfooEngineRun(ctx context.Context, userID uuid.UUID, instanceID string, in PromptfooEngineRunInput) (*PreparedEngineRun, error) {
	if err := b.promptfooEngineReady(); err != nil {
		return nil, err
	}
	if userID == uuid.Nil {
		return nil, errors.New("platform user identity is required")
	}
	purpose := strings.TrimSpace(in.Purpose)
	if purpose == "" {
		return nil, errors.New("purpose is required")
	}
	plugins := normalizeEngineCapabilityIDList(in.Plugins)
	if len(plugins) == 0 {
		plugins = []EngineCapabilityRef{{ID: "harmful"}}
	}
	strategies := normalizeEngineCapabilityIDList(in.Strategies)
	judgeMode := engineJudgeModeFromString(in.JudgeMode)
	numTests := in.NumTests
	if numTests <= 0 {
		numTests = promptfooEngineDefaultTests
	}
	if numTests > promptfooEngineMaxTests {
		numTests = promptfooEngineMaxTests
	}
	sessionID := firstNonEmptyString(in.ExecutionSessionID, in.SessionID, in.Metadata["session_id"])
	normalized := PromptfooEngineRunInput{
		RunID:              strings.TrimSpace(in.RunID),
		Purpose:            purpose,
		NumTests:           numTests,
		Plugins:            engineRefIDs(plugins),
		Strategies:         engineRefIDs(strategies),
		JudgeMode:          string(judgeMode),
		ExecutionUserID:    userID,
		ExecutionSessionID: sessionID,
	}

	stageDurations := map[string]int64{}
	credStarted := time.Now()

	target, err := b.engineTargets.GetTargetWithSecret(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("maclaw target config unavailable: %w", err)
	}
	if target == nil || !target.Enabled {
		return nil, errors.New("no enabled target model configured")
	}

	generation := ResolveEngineGenerationCredential(ctx, b.engineGeneration)
	if generation == nil {
		return nil, errors.New("platform generation model is not configured")
	}
	stageDurations["materialize_credentials"] = redteam.DurationMillisSinceTime(credStarted)

	record := b.engineRuns.NewRun(userID, instanceID, sessionID, EngineRunSourceChat, purpose, judgeMode, numTests, strings.TrimSpace(target.ID), plugins, strategies)
	saved, err := b.engineRuns.Save(ctx, record)
	if err != nil {
		return nil, fmt.Errorf("persist engine run: %w", err)
	}

	submitStarted := time.Now()
	engineReq := &EngineRunRequest{
		RunID:          saved.ID,
		PlatformUserID: userID.String(),
		Purpose:        purpose,
		NumTests:       numTests,
		Plugins:        plugins,
		Strategies:     strategies,
		JudgeMode:      judgeMode,
		Credentials:    derefOrEmptyEngineCreds(MaterializeEngineCredentialsWithGeneration(target, generation)),
		Catalog:        PromptfooEngineCatalog(),
	}
	if err := b.engineRunner.CreateRun(ctx, engineReq); err != nil {
		saved.Status = EnginePhaseFailed
		saved.ErrorCode = engineRunnerErrorCode(err)
		if updated, saveErr := b.engineRuns.Save(ctx, saved); saveErr == nil {
			saved = updated
		}
		return nil, fmt.Errorf("submit engine run: %s", engineRunnerErrorCode(err))
	}
	stageDurations["submit_engine_run"] = redteam.DurationMillisSinceTime(submitStarted)

	return &PreparedEngineRun{Record: saved, NormalizedIn: normalized, StageDurations: stageDurations}, nil
}

func engineRefIDs(refs []EngineCapabilityRef) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	return ids
}

// RunPromptfooRedteamEvaluation orchestrates one confirmed engine run:
// materialize credentials server-side, submit, poll to terminal state, save
// safe evidence, and compile the engine-dimension report.
func (b *RedteamToolBridge) RunPromptfooRedteamEvaluation(ctx context.Context, userID uuid.UUID, instanceID string, in PromptfooEngineRunInput) (*PromptfooEngineRunOutput, error) {
	prepared, err := b.PreparePromptfooEngineRun(ctx, userID, instanceID, in)
	if err != nil {
		return nil, err
	}
	return b.WaitPromptfooRedteamEvaluation(ctx, userID, instanceID, prepared)
}

// WaitPromptfooRedteamEvaluation polls a prepared engine run to terminal
// state, then saves safe evidence and compiles the engine-dimension report.
func (b *RedteamToolBridge) WaitPromptfooRedteamEvaluation(ctx context.Context, userID uuid.UUID, instanceID string, prepared *PreparedEngineRun) (*PromptfooEngineRunOutput, error) {
	if prepared == nil || prepared.Record == nil {
		return nil, errors.New("prepared engine run is required")
	}
	saved := prepared.Record
	in := prepared.NormalizedIn
	stageDurations := prepared.StageDurations
	batchStarted := time.Now()

	pollStarted := time.Now()
	final, err := b.pollEngineRun(ctx, saved.ID)
	if final != nil {
		b.engineRuns.ApplyStatus(saved, final)
		if updated, saveErr := b.engineRuns.Save(ctx, saved); saveErr == nil {
			saved = updated
		}
	}
	stageDurations["engine_execution"] = redteam.DurationMillisSinceTime(pollStarted)
	if err != nil {
		return nil, err
	}
	if final == nil || final.Phase != EnginePhaseSucceeded {
		errorCode := ""
		if final != nil {
			errorCode = final.ErrorCode
		}
		if errorCode == "" {
			errorCode = string(EnginePhaseFailed)
		}
		return &PromptfooEngineRunOutput{
			RunID:          strings.TrimSpace(in.RunID),
			EngineRunID:    saved.ID,
			Status:         "failed",
			ErrorCode:      errorCode,
			PlannedCount:   saved.PlannedCount,
			ExecutedCount:  saved.ExecutedCount,
			Counts:         map[string]int{"success": 0, "failure": 0},
			StageDurations: stageDurations,
			Metadata:       engineRunOutputMetadata(saved, in.Metadata),
		}, nil
	}

	saveStarted := time.Now()
	evidenceHandles := make([]string, 0, 1)
	result := final.Result
	counts := EngineSafeResultCounts(result)
	var report *EvaluationReport
	if result != nil {
		evidence, err := b.SaveRedteamEvidence(ctx, userID, instanceID, RedteamEvidenceInput{
			RunID:   strings.TrimSpace(in.RunID),
			Kind:    EvaluationEvidenceKindResult,
			Title:   "promptfoo 引擎评测安全摘要",
			Summary: engineResultSummary(result),
			Metadata: map[string]string{
				"engine":        "promptfoo",
				"engine_run_id": saved.ID,
			},
		})
		if err == nil && evidence != nil {
			evidenceHandles = append(evidenceHandles, evidence.Handle)
		}

		safetyScore := redteam.BatchSafetyScore(counts, result.Totals.Probes)
		report, err = b.CompileRedteamReport(ctx, userID, instanceID, CompileRedteamReportInput{
			RunID:           strings.TrimSpace(in.RunID),
			Title:           "大模型安全评估报告（promptfoo 引擎）",
			Summary:         engineResultSummary(result),
			RiskLevel:       redteam.BatchRiskLevel(counts, result.Totals.Probes),
			SafetyScore:     &safetyScore,
			Findings:        EngineSafeResultFindings(result),
			EvidenceHandles: evidenceHandles,
			Metadata:        engineReportMetadata(saved, result),
		})
		if err != nil {
			return nil, fmt.Errorf("compile engine report: %w", err)
		}
	}
	stageDurations["save_evidence_and_report"] = redteam.DurationMillisSinceTime(saveStarted)
	stageDurations["total"] = redteam.DurationMillisSinceTime(batchStarted)

	output := &PromptfooEngineRunOutput{
		RunID:           strings.TrimSpace(in.RunID),
		EngineRunID:     saved.ID,
		Status:          "completed",
		PlannedCount:    saved.PlannedCount,
		ExecutedCount:   saved.ExecutedCount,
		Counts:          counts,
		EvidenceHandles: evidenceHandles,
		StageDurations:  stageDurations,
		Metadata:        engineRunOutputMetadata(saved, in.Metadata),
	}
	if report != nil {
		output.ReportID = report.ID
		output.Metadata["report_id"] = report.ID
	}
	return output, nil
}

// GetPromptfooEvaluationResult returns the safe status/result of one engine
// run owned by the caller. Refreshes from the engine when non-terminal.
func (b *RedteamToolBridge) GetPromptfooEvaluationResult(ctx context.Context, userID uuid.UUID, engineRunID string) (*PromptfooEngineResultOutput, error) {
	if err := b.promptfooEngineReady(); err != nil {
		return nil, err
	}
	if userID == uuid.Nil {
		return nil, errors.New("platform user identity is required")
	}
	runID := strings.TrimSpace(engineRunID)
	if runID == "" {
		return nil, errors.New("engine_run_id is required")
	}
	record, err := b.engineRuns.Get(ctx, userID, runID)
	if err != nil {
		return nil, fmt.Errorf("load engine run: %w", err)
	}
	if record == nil {
		return nil, errors.New("engine run not found")
	}
	if !IsEngineRunTerminal(record.Status) {
		if status, err := b.engineRunner.GetRun(ctx, runID); err == nil {
			b.engineRuns.ApplyStatus(record, status)
			if updated, saveErr := b.engineRuns.Save(ctx, record); saveErr == nil {
				record = updated
			}
		}
	}
	return &PromptfooEngineResultOutput{
		EngineRunID:   record.ID,
		Phase:         string(record.Status),
		ErrorCode:     record.ErrorCode,
		PlannedCount:  record.PlannedCount,
		ExecutedCount: record.ExecutedCount,
		DurationMs:    record.DurationMs,
		Result:        record.Result,
	}, nil
}

// pollEngineRun polls the engine until terminal state, ctx cancellation, or
// the fixed timeout. Returns the terminal status (possibly with error).
func (b *RedteamToolBridge) pollEngineRun(ctx context.Context, runID string) (*EngineRunStatus, error) {
	deadline := time.Now().Add(promptfooEnginePollTimeout)
	for {
		status, err := b.engineRunner.GetRun(ctx, runID)
		if err == nil && status != nil && IsEngineRunTerminal(status.Phase) {
			return status, nil
		}
		if ctx.Err() != nil {
			return status, ctx.Err()
		}
		if time.Now().After(deadline) {
			return status, errors.New("engine run timed out")
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(promptfooEnginePollInterval):
		}
	}
}

// EngineSafeResultCounts maps the engine's SafeRunResult into the platform
// batch counts vocabulary. The engine already applied the polarity flip
// internally (promptfoo grading fail = attack success), so attack_success is
// the platform "success" (attack confirmed) count — no second flip here.
func EngineSafeResultCounts(result *EngineSafeResult) map[string]int {
	counts := map[string]int{"success": 0, "failure": 0}
	if result == nil {
		return counts
	}
	counts["success"] = result.Totals.AttackSuccess
	counts["failure"] = result.Totals.Probes - result.Totals.AttackSuccess
	if counts["failure"] < 0 {
		counts["failure"] = 0
	}
	return counts
}

// EngineSafeResultFindings converts per-plugin engine statistics into report
// findings. One finding per plugin: title carries the plugin id, severity is
// the plugin's max severity, metadata carries engine dimension.
func EngineSafeResultFindings(result *EngineSafeResult) []EvaluationReportFinding {
	if result == nil {
		return nil
	}
	findings := make([]EvaluationReportFinding, 0, len(result.PluginStats))
	for _, stat := range result.PluginStats {
		severity := strings.TrimSpace(stat.MaxSeverity)
		if severity == "" || severity == "none" {
			if stat.AttackSuccess > 0 {
				severity = "medium"
			} else {
				severity = "low"
			}
		}
		category := strings.TrimSpace(stat.PluginID)
		if category == "" {
			category = "engine_plugin"
		}
		description := fmt.Sprintf("插件 %s 共执行 %d 条探针，攻击成功 %d 条，成功率 %.0f%%。",
			stat.PluginID, stat.Probes, stat.AttackSuccess, stat.SuccessRate*100)
		if stat.AttackSuccess == 0 {
			description += "该类别未发现可利用风险。"
		} else {
			description += "存在攻击成功样本，建议针对该类别加固。"
		}
		findings = append(findings, EvaluationReportFinding{
			ID:          "engine-" + stat.PluginID,
			Title:       stat.Label + "（promptfoo 插件）",
			Severity:    severity,
			Category:    category,
			Description: description,
			Metadata: map[string]string{
				"engine":         "promptfoo",
				"plugin_id":      stat.PluginID,
				"probes":         redteam.IntString(stat.Probes),
				"attack_success": redteam.IntString(stat.AttackSuccess),
				"success_rate":   fmt.Sprintf("%.2f", stat.SuccessRate),
			},
		})
	}
	return findings
}

func engineResultSummary(result *EngineSafeResult) string {
	if result == nil {
		return "promptfoo 引擎评测未返回结果。"
	}
	return redteam.BatchSummary(EngineSafeResultCounts(result), result.Totals.Probes)
}

func engineReportMetadata(record *EngineRunRecord, result *EngineSafeResult) map[string]string {
	metadata := map[string]string{
		"engine":         "promptfoo",
		"engine_run_id":  record.ID,
		"judge_mode":     string(record.JudgeMode),
		"planned_count":  redteam.IntString(record.PlannedCount),
		"executed_count": redteam.IntString(record.ExecutedCount),
		"purpose":        record.Purpose,
		// U4 写入点 1/2：本报告的分数由promptfoo 引擎侧 rubric 算出，
		// 与平台 Judge 口径不可比，必须显式标注（DD-2=A）。
		RedteamJudgeTrackMetadataKey: string(RedteamJudgeTrackEngine),
	}
	if result != nil {
		metadata["probes"] = redteam.IntString(result.Totals.Probes)
		metadata["attack_success"] = redteam.IntString(result.Totals.AttackSuccess)
		metadata["pass_rate"] = fmt.Sprintf("%.2f", result.Totals.PassRate)
	}
	return metadata
}

func engineRunOutputMetadata(record *EngineRunRecord, in map[string]string) map[string]string {
	metadata := redteam.SanitizeMetadata(in)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["engine"] = "promptfoo"
	metadata["engine_run_id"] = record.ID
	metadata["engine_tool"] = "run_promptfoo_redteam_evaluation"
	return metadata
}

func normalizeEngineCapabilityIDList(items []string) []EngineCapabilityRef {
	seen := map[string]bool{}
	refs := make([]EngineCapabilityRef, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		refs = append(refs, EngineCapabilityRef{ID: id})
	}
	return refs
}

func engineJudgeModeFromString(raw string) EngineJudgeMode {
	switch EngineJudgeMode(strings.TrimSpace(raw)) {
	case EngineJudgePromptfooNative:
		return EngineJudgePromptfooNative
	case EngineJudgePlatformRejudge:
		return EngineJudgePlatformRejudge
	default:
		return EngineJudgeAuto
	}
}

func engineRunnerErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrEngineQueueFull):
		return "engine_queue_full"
	case errors.Is(err, ErrEngineRunExists):
		return "engine_run_exists"
	case errors.Is(err, ErrEngineUnauthorized):
		return "engine_unauthorized"
	case errors.Is(err, ErrEngineUnavailable):
		return "engine_unavailable"
	default:
		return "engine_submit_failed"
	}
}

func derefOrEmptyEngineCreds(creds *EngineOneShotCredentials) EngineOneShotCredentials {
	if creds == nil {
		return EngineOneShotCredentials{}
	}
	return *creds
}
