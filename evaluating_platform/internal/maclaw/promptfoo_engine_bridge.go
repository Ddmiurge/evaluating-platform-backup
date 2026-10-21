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
	// promptfooEngineDefaultTests only applies when the confirmed plan carried no
	// count at all. It stays lower than the wizard default (20) on purpose: a
	// chat-initiated run has less explicit user intent behind its number, so the
	// conservative default belongs here.
	promptfooEngineDefaultTests = 5
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

	// Source selects the engine_runs.source value (U5). Empty keeps the
	// historical EngineRunSourceChat behavior; the confirm fast path passes
	// EngineRunSourceChatConfirm so its pfj- jobs are distinguishable in
	// engine_runs. Ignored (falls back to chat) for unknown values.
	Source string `json:"-"`

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

// SetCustomPluginResolver wires the U10 published-package resolver into the
// bridge. Optional: without it, `pf_plugin:` references are not resolvable and
// runs naming them are rejected rather than silently dropped.
func (b *RedteamToolBridge) SetCustomPluginResolver(resolver *CustomPluginResolver) {
	if b == nil {
		return
	}
	b.customPlugins = resolver
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
		Description: "内置引擎评测插件与策略目录，两条轴都在这里：插件卡（promptfoo_plugin:<id>）负责生成攻击用例，策略卡（promptfoo_strategy:<id>）负责包装载荷。用户点名某个手法或包装（例如 base64）时，应选中对应的策略卡，而不是不带策略出计划。选中项请以 source_ref 原样回传进计划卡的 selected_capability_refs；执行时插件 id 传 plugins、策略 id 传 strategies。",
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
	// U6 pre-flight guard: same rule as the wizard path. The confirm fast path
	// builds plugin ids from a chat plan card, which an LLM can name freely —
	// without this check a card proposing e.g. `financial:hallucination` would
	// produce a successful run with 0 coverage for that plugin. Placed before
	// credential materialization and persistence so rejection is free of side
	// effects.
	if err := ValidateEngineRunRemoteGeneration(plugins, strategies); err != nil {
		return nil, err
	}
	// P2-2 pre-flight guard: same rule as the wizard path, and for the same
	// reason plus one more. The confirm fast path builds plugin ids from a chat
	// plan card, which an LLM names freely — so `prompt-injection` (a plausible
	// thing for a planner to write, and a real platform id until P2-1) can
	// arrive here even though the selector no longer offers it. Refusing now
	// costs nothing; refusing later means a persisted run that fails after the
	// user waited.
	if err := ValidateEngineRunPluginLegality(plugins); err != nil {
		return nil, err
	}
	// P2-2 (D-3): same rule as the wizard path — a plugin the platform cannot
	// feed fails the run with 0 cases, so refuse it before the run record exists.
	if err := ValidateEngineRunPluginConfig(plugins); err != nil {
		return nil, err
	}
	judgeMode := engineJudgeModeFromString(in.JudgeMode)
	// P2-4: resolved against the selection, because the default plugin is
	// `harmful` (26 sub-plugins) and a flat default of 5 could not be submitted.
	numTests := ResolveEngineRunNumTests(in.NumTests, promptfooEngineDefaultTests, plugins)
	// P2-2 (S-4): refuse rather than clamp. A confirmed count is the number the
	// user approved on the plan card, so quietly running fewer rounds would make
	// the report and the approval disagree with nothing in the response saying
	// so. The cap is EngineMaxNumTests — the same one the wizard enforces and the
	// same one the engine clamps at, so the three cannot drift apart again.
	if numTests > EngineMaxNumTests {
		return nil, &EngineNumTestsTooLargeError{Requested: numTests, Max: EngineMaxNumTests}
	}
	// P3-1: same retired-strategy gate as the wizard path — a chat plan card can
	// name one of the withdrawn platform dimensions just as easily.
	if err := ValidateEngineRunStrategyRetirement(strategies); err != nil {
		return nil, err
	}
	// P2-4: same coverage floor as the wizard path, checked once the effective
	// count is known. A chat plan can name a collection id (`harmful`) together
	// with a small round count, and that combination has no legal split.
	if err := ValidateEngineRunNumTestsCoverage(plugins, numTests); err != nil {
		return nil, err
	}
	// U10: resolve expert-published packages and apply admin governance.
	//
	// Placement is the point. This sits in PreparePromptfooEngineRun — the one
	// function every confirmed engine run passes through — rather than in a
	// helper the callers happen to call. U8's counter-proof showed what happens
	// when a guard lives at a call site: deleting the wiring left 43 tests
	// green. A disabled package that reached the engine would be exactly the
	// silent-failure shape this project keeps refusing, so the check is here.
	//
	// Rejection happens before credential materialization and before the run
	// record is persisted, so a refused package leaves no half-created run and
	// no credential touched.
	resolved, err := b.resolveCustomPlugins(ctx, userID, plugins)
	if err != nil {
		return nil, err
	}
	plugins = resolved.engineRefs
	customPlugins := resolved.packages
	// Phase 3 · 任务 2: the same treatment for expert-published strategy
	// packages. Placed at the same seam for the same reason — this is the one
	// function every confirmed engine run passes through, so a strategy package
	// an admin disabled cannot reach the engine by any other route, and the
	// refusal happens before credentials are materialized and before the run
	// record is persisted.
	strategyRes, err := b.resolveCustomStrategies(ctx, userID, strategies)
	if err != nil {
		return nil, err
	}
	strategies = strategyRes.engineRefs
	customStrategies := strategyRes.packages
	sessionID := firstNonEmptyString(in.ExecutionSessionID, in.SessionID, in.Metadata["session_id"])
	normalized := PromptfooEngineRunInput{
		RunID:              strings.TrimSpace(in.RunID),
		Purpose:            purpose,
		NumTests:           numTests,
		Plugins:            resolved.recordIDs,
		Strategies:         strategyRes.recordIDs,
		JudgeMode:          string(judgeMode),
		ExecutionUserID:    userID,
		ExecutionSessionID: sessionID,
	}

	// The persisted run record must name the resolved ids, never a transport
	// marker. Two markers exist and both are internal: `promptfoo-custom:` for
	// plugin packages and `pf_strategy:` for strategy packages. Using either
	// here would put a transport detail into the record the report and the
	// browser are built from.
	recordRefs := make([]EngineCapabilityRef, 0, len(resolved.recordIDs))
	for _, id := range resolved.recordIDs {
		recordRefs = append(recordRefs, EngineCapabilityRef{ID: id})
	}
	recordStrategyRefs := make([]EngineCapabilityRef, 0, len(strategyRes.recordIDs))
	for _, id := range strategyRes.recordIDs {
		recordStrategyRefs = append(recordStrategyRefs, EngineCapabilityRef{ID: id})
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

	// U5: the confirm fast path tags its run as chat_confirm so the pfj- ↔ run
	// binding written afterwards is distinguishable from ordinary chat runs.
	runSource := EngineRunSourceChat
	if isKnownEngineRunSource(in.Source) {
		runSource = in.Source
	}
	// The record keeps the EVALUATION purpose — it is what the run detail shows
	// the user ("why did I run this"). The engine gets a different string: the
	// TARGET's own purpose, because promptfoo renders it into every generation
	// template and every grader rubric. Conflating the two made every generated
	// payload a meta-request; see EngineRunPurposeForTarget.
	record := b.engineRuns.NewRun(userID, instanceID, sessionID, runSource, purpose, judgeMode, numTests, strings.TrimSpace(target.ID), recordRefs, recordStrategyRefs)
	saved, err := b.engineRuns.Save(ctx, record)
	if err != nil {
		return nil, fmt.Errorf("persist engine run: %w", err)
	}

	submitStarted := time.Now()
	engineReq := &EngineRunRequest{
		RunID:          saved.ID,
		PlatformUserID: userID.String(),
		Purpose:        EngineRunPurposeForTarget(target),
		NumTests:       numTests,
		Plugins:        plugins,
		Strategies:     strategies,
		JudgeMode:      judgeMode,
		Credentials:    derefOrEmptyEngineCreds(MaterializeEngineCredentialsWithGeneration(target, generation)),
		Catalog:        PromptfooEngineCatalog(),
		// U10: the resolved packages. Governance has already been applied; the
		// engine's job is to write each definition into its per-run workdir and
		// point promptfoo at it.
		CustomPlugins: customPlugins,
		// Phase 3 · 任务 2: the resolved strategy packages. No workdir entry —
		// a strategy is inlined into `redteam.strategies` as
		// `custom:<variant>` plus its `strategyText`, which is the only form
		// promptfoo accepts without executing code.
		CustomStrategies: customStrategies,
	}
	// U7: enrich the request with the application description promptfoo's own
	// generation reads. Ignored while Plan A is selected, so this is safe to
	// send unconditionally and keeps one request shape for both paths.
	ResolveEngineNativeGenerationInputs(engineReq, target.Metadata)
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

// customPluginResolution is the outcome of resolving a run's plugin refs.
//
// Two id lists, because the two consumers need different things and conflating
// them would leak:
//
//   - `engineRefs` carries the `promptfoo-custom:` marker the engine swaps for a
//     `file://` path inside its own workdir. It never leaves the process.
//   - `recordIDs` carries the package's own plugin id, which is what the run
//     record, the report and the browser see. Using the marker there would put
//     an internal transport detail into user-facing output, and using the
//     engine's `file://` path would put a **local filesystem path** into a
//     report — explicitly forbidden by AGENTS.md ("must never include … local
//     paths").
type customPluginResolution struct {
	engineRefs []EngineCapabilityRef
	recordIDs  []string
	packages   []EngineCustomPlugin
}

// resolveCustomPlugins applies U10 governance to a run's plugin refs.
//
// Pure pass-through when no ref names a published package, so a deployment that
// never uses the feature behaves exactly as before — that is the rollback
// property, and it is also why the resolver does not require wiring to be
// present in order to run.
func (b *RedteamToolBridge) resolveCustomPlugins(
	ctx context.Context,
	userID uuid.UUID,
	refs []EngineCapabilityRef,
) (*customPluginResolution, error) {
	recordIDs := make([]string, 0, len(refs))
	engineRefs := make([]EngineCapabilityRef, 0, len(refs))
	var packages []EngineCustomPlugin
	for _, ref := range refs {
		if !IsCustomPluginRef(ref.ID) {
			recordIDs = append(recordIDs, ref.ID)
			engineRefs = append(engineRefs, ref)
			continue
		}
		if b == nil || b.customPlugins == nil || !b.customPlugins.Enabled() {
			return nil, &CustomPluginRefError{
				Code: CustomPluginErrRefUnavailable,
				Ref:  ref.ID,
				Message: "自定义插件 " + CustomPluginRefTarget(ref.ID) + " 无法加载：平台未启用自定义插件发布链路。" +
					"请联系管理员确认加密资源存储（maclaw_resources KeyStore）与发布表已配置。",
			}
		}
		pkg, err := b.customPlugins.resolveOne(ctx, userID, ref.ID)
		if err != nil {
			return nil, err
		}
		packages = append(packages, *pkg)
		recordIDs = append(recordIDs, pkg.PluginID)
		engineRefs = append(engineRefs, EngineCapabilityRef{
			ID:     CustomPluginEngineMarker + ref.ID,
			Config: ref.Config,
		})
	}
	return &customPluginResolution{engineRefs: engineRefs, recordIDs: recordIDs, packages: packages}, nil
}

// resolveCustomStrategies applies Phase 3 governance to a run's strategy refs.
//
// Structurally identical to resolveCustomPlugins — same resolver, same
// publication table, same `enabled = true AND status = 'published'` filter —
// because both features answer the same three questions (is it available, get
// the payload, hand it over in an engine-loadable form). The differences are in
// the answers: a strategy carries no workdir path and no `file://` reference,
// only the `pf_strategy:` marker the engine swaps for `custom:<variant>` plus
// the inline config.
func (b *RedteamToolBridge) resolveCustomStrategies(
	ctx context.Context,
	userID uuid.UUID,
	refs []EngineCapabilityRef,
) (*customStrategyResolution, error) {
	recordIDs := make([]string, 0, len(refs))
	engineRefs := make([]EngineCapabilityRef, 0, len(refs))
	var packages []EngineCustomStrategy
	for _, ref := range refs {
		if !IsCustomStrategyRef(ref.ID) {
			recordIDs = append(recordIDs, ref.ID)
			engineRefs = append(engineRefs, ref)
			continue
		}
		if b == nil || b.customPlugins == nil || !b.customPlugins.Enabled() {
			return nil, &CustomPluginRefError{
				Code: CustomStrategyErrRefUnavailable,
				Ref:  ref.ID,
				Message: "自定义策略 " + CustomStrategyRefTarget(ref.ID) + " 无法加载：平台未启用自定义策略发布链路。" +
					"请联系管理员确认加密资源存储（maclaw_resources KeyStore）与发布表已配置。",
			}
		}
		pkg, err := b.customPlugins.resolveOneStrategy(ctx, userID, ref.ID)
		if err != nil {
			return nil, err
		}
		packages = append(packages, *pkg)
		recordIDs = append(recordIDs, pkg.StrategyID)
		engineRefs = append(engineRefs, EngineCapabilityRef{ID: CustomStrategyRef(pkg.Handle)})
	}
	return &customStrategyResolution{engineRefs: engineRefs, recordIDs: recordIDs, packages: packages}, nil
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

// EngineSafeResultFindings converts the engine's result into report findings.
//
// # It now emits one finding per PROBE, not per plugin (2026-10-16)
//
// The old shape was one finding per plugin, whose whole content was
// 「插件 harmful 共执行 26 条探针，攻击成功 3 条」. A user asking "did it leak the
// system prompt?" had no way to find out from their own report — the payloads
// were described but never shown, which is what made the report unusable as a
// deliverable (see audit-2026-10-09/23-聊天链路报告原文与promptfoo对齐.md §1).
//
// The per-plugin rollup is still produced — it is the useful shape for a
// 50-probe run — but it now lives in the report's metrics section, not in the
// place where the reader expects to see the attacks. Two different questions,
// two different sections.
//
// # Fallback
//
// An engine that predates `case_results` reports none. Falling back to the
// per-plugin rollup is the honest thing there: it is what we actually know. It
// is deliberately NOT an empty findings list, which would read as "no
// vulnerabilities found" — a claim we would have no basis for.
func EngineSafeResultFindings(result *EngineSafeResult) []EvaluationReportFinding {
	if result == nil {
		return nil
	}
	if len(result.CaseResults) > 0 {
		return engineCaseFindings(result)
	}
	return enginePluginRollupFindings(result)
}

// engineCaseFindings renders the engine's per-probe detail as report findings.
//
// The field mapping deliberately matches the batch path
// (`execute_redteam_evaluation_batch`, redteam_bridge.go), which has carried raw
// text for longer: Title = the attack, Description = the target's answer,
// Suggestion = the verdict's reasoning. Keeping the two paths on the same fields
// means one finding renderer (and one PDF card) serves both, instead of the
// report having to know which engine produced it.
func engineCaseFindings(result *EngineSafeResult) []EvaluationReportFinding {
	judgeSourceByPlugin := make(map[string]string, len(result.PluginStats))
	probesByPlugin := make(map[string]int, len(result.PluginStats))
	for _, stat := range result.PluginStats {
		judgeSourceByPlugin[stat.PluginID] = stat.JudgeSource
		probesByPlugin[stat.PluginID] = stat.Probes
	}

	findings := make([]EvaluationReportFinding, 0, len(result.CaseResults))
	for _, item := range result.CaseResults {
		index := item.Index
		if index <= 0 {
			index = len(findings) + 1
		}
		severity := strings.TrimSpace(item.Severity)
		if severity == "" || severity == "none" {
			// An attack that worked but carries no grader severity still needs a
			// display value; "none" next to 攻击成功 true would read as contradictory.
			if item.AttackSuccess {
				severity = "medium"
			} else {
				severity = "low"
			}
		}
		pluginID := strings.TrimSpace(item.PluginID)
		if pluginID == "" {
			pluginID = "engine_plugin"
		}
		title, promptRedacted := engineCaseTitle(item)
		metadata := map[string]string{
			"engine":         "promptfoo",
			"case_index":     redteam.IntString(index),
			"plugin_id":      pluginID,
			"strategy_id":    engineStrategyMetadataValue(item.StrategyID),
			"attack_success": redteam.BoolString(item.AttackSuccess),
		}
		if source := strings.TrimSpace(judgeSourceByPlugin[pluginID]); source != "" {
			metadata[RedteamJudgeSourceMetadataKey] = source
		}
		if probes := probesByPlugin[pluginID]; probes > 0 {
			metadata["plugin_probes"] = redteam.IntString(probes)
		}
		if promptRedacted {
			// The renderer turns this into an explicit note: the title above is the
			// payload BEFORE the strategy encoded it, not the bytes that were sent.
			metadata["prompt_redacted_by_engine"] = "true"
		}
		if item.Score != nil {
			// promptfoo's own grading score. Rendered only for engine-judged
			// runs — see EngineCaseResult.Score.
			metadata["grading_score"] = fmt.Sprintf("%.4g", *item.Score)
		}
		findings = append(findings, EvaluationReportFinding{
			ID:          "case-" + redteam.IntString(index),
			Title:       title,
			Severity:    severity,
			Category:    pluginID,
			Description: strings.TrimSpace(item.Response),
			Suggestion:  strings.TrimSpace(item.JudgeReason),
			Metadata:    metadata,
			Turns:       engineCaseTurns(item.Turns),
		})
	}
	return findings
}

// engineStrategyMetadataValue normalizes the "no strategy" sentinel.
//
// The engine reports `none` for a probe no strategy wrapped — the same sentinel
// `strategy_stats` uses. It is carried into metadata as-is (so a reader can
// filter on it), but every renderer must translate it: a report that prints
// 「攻击策略：none」 invents a technique called none, and one that prints
// 「攻击策略：自定义策略」 for it would claim a wrapper that never ran.
func engineStrategyMetadataValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.EqualFold(trimmed, "none") {
		return "none"
	}
	return trimmed
}

func engineCaseTurns(turns []EngineCaseTurn) []EvaluationReportFindingTurn {
	if len(turns) == 0 {
		return nil
	}
	out := make([]EvaluationReportFindingTurn, 0, len(turns))
	for _, turn := range turns {
		out = append(out, EvaluationReportFindingTurn{
			Role:    strings.TrimSpace(turn.Role),
			Content: turn.Content,
		})
	}
	return out
}

// enginePluginRollupFindings is the pre-2026-10 aggregate shape: one finding per
// plugin. Used as the fallback when the engine reported no per-probe detail, and
// as the source of the report's metrics section.
func enginePluginRollupFindings(result *EngineSafeResult) []EvaluationReportFinding {
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
				// U8：这条 finding 的判定标准。逐插件带上，才看得出同一份报告里
				// 不同插件是按不同尺度判的（U4 的 judge_track 回答不了这个问题）。
				RedteamJudgeSourceMetadataKey: stat.JudgeSource,
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
	// U8：判定**标准**的溯源，与上面的 judge_track（判定链路）正交、互不覆盖。
	// 单值键给「主要按哪档判」，计数键给「有没有哪部分其实没标准」——
	// 只有单值键时，99 条按族判 + 1 条落兜底会显示成「按族判」，退化被抹掉了。
	summary := SummarizeEngineJudgeSources(result)
	if summary.Distinct > 0 {
		if histogram := formatJudgeSourceHistogram(summary); histogram != "" {
			metadata[RedteamJudgeSourcesMetadataKey] = histogram
		}
		metadata[RedteamJudgeSourceMetadataKey] = dominantJudgeSource(summary)
		if summary.HasFallback() {
			metadata[redteamJudgeSourceFallbackMetadataKey] = RedteamJudgeSourceFallback
		}
	}
	if result != nil {
		metadata["probes"] = redteam.IntString(result.Totals.Probes)
		metadata["attack_success"] = redteam.IntString(result.Totals.AttackSuccess)
		metadata["pass_rate"] = fmt.Sprintf("%.2f", result.Totals.PassRate)
	}
	// P2-6: which generation path produced this report, and how many probes it
	// configured. Plan A and Plan C are not comparable (different probe counts for
	// the same input) and before this key existed two such reports looked identical
	// — U7 item 14. Both keys are added only when the engine supplied them: an
	// absent value is honest ("not recorded"), while a guessed one would be
	// published under a heading that claims to explain the numbers.
	if path := NormalizedGenerationPathOrEmpty(result); path != "" {
		metadata[RedteamGenerationPathMetadataKey] = path
		if effective := EffectiveNumTestsMetadata(result); effective != "" {
			metadata[RedteamNumTestsEffectiveMetadataKey] = effective
		}
	}
	// 2026-10-16：findings 现在是**逐条探针**一条，所以「某插件跑了 N 条、成功 M 条」
	// 这种汇总在原处放不下了。它不能被丢掉——一份 50 条探针的报告，读者既需要逐条
	// 原文，也需要按插件/策略的稠密汇总才能判断该先修哪里。
	//
	// 存成 JSON 字符串而不是加数据库列：`stage_durations_json` 已是同一形状的先例，
	// 且这两个键已在 redteam 包的白名单里显式登记（否则会被 SanitizeMetadata 按
	// 子串黑名单丢掉——那正是 generation_path 的 P0 教训）。
	if result != nil {
		// Omitted when empty, not written as "[]". Go cannot tell an absent
		// `plugin_stats` from an empty one after decoding, and a run with plugins
		// always has stats — so an empty list means the engine did not tell us.
		// Recording "[]" would render as a metrics table asserting that no plugin
		// ran, which is a claim we have no basis for; the renderer says
		// 「未记录」 for an absent key instead.
		if lines := enginePluginStatsMetadata(result.PluginStats); len(lines) > 0 {
			metadata[RedteamPluginStatsMetadataKey] = redteam.MustJSONString(lines)
		}
		if lines := engineStrategyStatsMetadata(result.StrategyStats); len(lines) > 0 {
			metadata[RedteamStrategyStatsMetadataKey] = redteam.MustJSONString(lines)
		}
	}
	return metadata
}

// Report-metadata keys carrying the per-plugin and per-strategy rollups.
//
// They moved into metadata on 2026-10-16, when findings became one entry per
// probe: a rollup line cannot live inside a per-probe finding without appearing
// once per probe.
//
// **Both names must stay in the redteam package's safe-key list**
// (`isSafePayloadMetadataKey`). `SanitizeMetadata` drops any key whose name
// contains `prompt` / `response` / `path` / `content` / … by substring, and the
// report is compiled from sanitized metadata — the P2-6 P0 was exactly a
// platform key (`generation_path`) being eaten that way. Here the names happen to
// be free of those substrings, which is precisely why they are listed
// explicitly: "it happens to be safe today" is the assumption that failed once.
const (
	RedteamPluginStatsMetadataKey   = "plugin_stats_json"
	RedteamStrategyStatsMetadataKey = "strategy_stats_json"
)

// EnginePluginStatLine / EngineStrategyStatLine are the shapes persisted under
// RedteamPluginStatsMetadataKey / RedteamStrategyStatsMetadataKey.
//
// A struct rather than map[string]any so the writer and the reader (the report's
// metrics section) cannot disagree about field names — the two live in different
// files and a typo would show up as a blank column, not a compile error.
type EnginePluginStatLine struct {
	PluginID      string  `json:"plugin_id"`
	Label         string  `json:"label"`
	Probes        int     `json:"probes"`
	AttackSuccess int     `json:"attack_success"`
	SuccessRate   float64 `json:"success_rate"`
	MaxSeverity   string  `json:"max_severity"`
	JudgeSource   string  `json:"judge_source,omitempty"`
}

type EngineStrategyStatLine struct {
	ID            string  `json:"id"`
	Label         string  `json:"label"`
	Probes        int     `json:"probes"`
	AttackSuccess int     `json:"attack_success"`
	SuccessRate   float64 `json:"success_rate"`
	MaxSeverity   string  `json:"max_severity"`
}

func enginePluginStatsMetadata(stats []EnginePluginStat) []EnginePluginStatLine {
	out := make([]EnginePluginStatLine, 0, len(stats))
	for _, stat := range stats {
		out = append(out, EnginePluginStatLine{
			PluginID:      stat.PluginID,
			Label:         stat.Label,
			Probes:        stat.Probes,
			AttackSuccess: stat.AttackSuccess,
			SuccessRate:   stat.SuccessRate,
			MaxSeverity:   stat.MaxSeverity,
			JudgeSource:   stat.JudgeSource,
		})
	}
	return out
}

func engineStrategyStatsMetadata(stats []EngineStrategyStat) []EngineStrategyStatLine {
	out := make([]EngineStrategyStatLine, 0, len(stats))
	for _, stat := range stats {
		out = append(out, EngineStrategyStatLine{
			ID:            stat.ID,
			Label:         stat.Label,
			Probes:        stat.Probes,
			AttackSuccess: stat.AttackSuccess,
			SuccessRate:   stat.SuccessRate,
			MaxSeverity:   stat.MaxSeverity,
		})
	}
	return out
}

// redteamJudgeSourceFallbackMetadataKey 标记本次运行存在落兜底档的探测。
// 单值键/计数键已经能推出这个结论，单列一个键是为了让渲染层不必解析
// histogram 就能决定是否显示警告。
const redteamJudgeSourceFallbackMetadataKey = "judge_source_fallback"

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

// NormalizedGenerationPathOrEmpty returns the generation path to store, or "" when
// the engine did not supply one (or supplied an uninterpretable one — stored
// absence is honest; a guess would be published under a heading that claims to
// explain the numbers). `ValidateReportGenerationMetadata` re-checks the stored
// value, so an illegal path cannot reach a report either way.
func NormalizedGenerationPathOrEmpty(result *EngineSafeResult) string {
	if result == nil {
		return ""
	}
	path, err := NormalizeRedteamGenerationPath(result.GenerationPath)
	if err != nil {
		return ""
	}
	return path
}

// EffectiveNumTestsMetadata returns the configured probe count to store, or ""
// when the engine did not report one.
func EffectiveNumTestsMetadata(result *EngineSafeResult) string {
	if result == nil || result.NumTestsEffective <= 0 {
		return ""
	}
	return redteam.IntString(result.NumTestsEffective)
}

// engineRedactedPromptMarker is what promptfoo writes where its secret heuristic
// rejected a value. Spelled out here rather than imported: it is promptfoo's
// output format, observed at the DB boundary, and the only place this platform
// needs to recognise it.
const engineRedactedPromptMarker = "[REDACTED]"

// engineCaseTitle picks the attack text to render for one probe.
//
// It prefers the prompt that was actually sent. When promptfoo redacted that
// prompt on the way into its database — which it does for every base64-shaped
// payload, i.e. every probe of the base64 strategy — the pre-strategy original is
// used instead and the caller marks the finding.
//
// The second return value is "this title is a fallback", which is what the
// report's note line is keyed on. Returning the original WITHOUT that flag would
// present encoded-then-redacted as if it were the sent payload.
func engineCaseTitle(item EngineCaseResult) (string, bool) {
	prompt := strings.TrimSpace(item.Prompt)
	if prompt != "" && prompt != engineRedactedPromptMarker {
		return prompt, false
	}
	if original := strings.TrimSpace(item.OriginalPrompt); original != "" {
		return original, true
	}
	// Nothing to fall back to: render what the engine gave us, even if that is the
	// redaction marker. An empty title would look like a missing probe.
	return prompt, false
}
