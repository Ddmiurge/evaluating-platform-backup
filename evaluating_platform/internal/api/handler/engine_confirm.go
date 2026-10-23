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
	// Expert-published packages arrive as their own ref form, and the resolver
	// switches on that prefix — so for these the id collected here IS the whole
	// ref, not the part after a prefix (2026-10-23).
	customPluginPrefix := maclaw.CustomPluginRefPrefix
	customStrategyPrefix := maclaw.CustomStrategyRefPrefix

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
		case strings.HasPrefix(lower, customPluginPrefix):
			// An expert package. Before this branch existed the ref was simply
			// dropped, so a plan that selected only a published plugin reached
			// the confirm gate as 「计划里没有插件」 — the package was usable by
			// the engine and unreachable from the product.
			plugins[ref] = true
		case strings.HasPrefix(lower, customStrategyPrefix):
			declaredStrategies++
			strategies[ref] = true
		}
	}
	addID := func(set map[string]bool, value any) {
		if id := strings.TrimSpace(fmtAnyString(value)); id != "" {
			set[id] = true
		}
	}

	if raw, ok := body["selected_capability_refs"].([]any); ok {
		for _, item := range raw {
			switch typed := item.(type) {
			case string:
				addRef(typed)
			case map[string]any:
				// The frontend has always rendered object entries from this array
				// (`source_ref` / `ref` / `id`), and maclaw writes them — but the
				// confirm path only read strings, so an object-form selection
				// showed on the card and vanished at confirm. Same class as the
				// strategy refs dropped in §12: selected, displayed, not executed.
				addRef(firstAny(typed["source_ref"], typed["ref"], typed["capability_ref"], typed["id"]))
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
			ref := firstAny(m["source_ref"], m["ref"], m["capability_ref"], m["id"])
			lowerRef := strings.ToLower(strings.TrimSpace(fmtAnyString(ref)))
			// Either the source_type says it is an engine capability, or the ref
			// itself carries an engine prefix. The second half matters because a
			// published package may arrive typed as a platform resource while its
			// ref is `pf_plugin:<handle>` — the ref, not the label, is what the
			// engine switches on.
			engineRef := strings.HasPrefix(lowerRef, pluginPrefix) ||
				strings.HasPrefix(lowerRef, strategyPrefix) ||
				strings.HasPrefix(lowerRef, customPluginPrefix) ||
				strings.HasPrefix(lowerRef, customStrategyPrefix)
			if sourceType != maclaw.CapabilitySourcePromptfooPlugin && sourceType != maclaw.CapabilitySourcePromptfooStrategy && !engineRef {
				continue
			}
			addRef(ref)
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

// withoutRemovedRefs drops the refs the user took off the plan card before
// confirming.
//
// # Only subtraction, ever
//
// The plan card is the authorisation for what this run may use — the execution
// grant is bound to it. Accepting a selection from the browser would let a
// crafted request run a capability the plan never offered, so the request can
// only name things to REMOVE, and a removal that names something the plan did not
// select is simply not found. Removals naming skills or expert samples are
// ignored here on purpose: those axes drive a different execution path and are
// out of scope for this change.
//
// `DeclaredStrategies` is decremented alongside, because it is the fail-closed
// signal "the plan named a strategy but none survived". Removing the only
// strategy is a deliberate choice, not an unresolved ref, and must not be refused
// as one.
func (s promptfooEnginePlanSelection) withoutRemovedRefs(removed map[string]bool) promptfooEnginePlanSelection {
	if len(removed) == 0 {
		return s
	}
	out := s
	out.Plugins = nil
	out.Strategies = nil
	pluginPrefix := maclaw.CapabilitySourcePromptfooPlugin + ":"
	strategyPrefix := maclaw.CapabilitySourcePromptfooStrategy + ":"
	// `removed[id]` covers expert packages, whose id is already the ref
	// (`pf_plugin:<handle>`); `removed[prefix+id]` covers built-ins, whose id is
	// the bare catalog id. One rule for the card, two shapes underneath.
	for _, id := range s.Plugins {
		if removed[id] || removed[pluginPrefix+id] {
			continue
		}
		out.Plugins = append(out.Plugins, id)
	}
	for _, id := range s.Strategies {
		if removed[id] || removed[strategyPrefix+id] {
			continue
		}
		out.Strategies = append(out.Strategies, id)
	}
	if out.DeclaredStrategies > len(out.Strategies) {
		out.DeclaredStrategies = len(out.Strategies)
	}
	return out
}

// addedStrategyRefusal reports why the strategies the user added on the plan
// card cannot be honoured, or refused=false when they can.
//
// # Why addition exists at all, and why it is not removal's mirror
//
// Removal is safe by construction: it can only take away what the plan already
// offered, so a crafted request cannot widen anything. Addition is the client
// naming a capability the plan never selected, which is exactly what the
// authorisation rule in withoutRemovedRefs exists to prevent — so the server
// decides what may be added and proves each id is runnable before it is.
//
// Two limits, both deliberate:
//
//   - STRATEGIES ONLY. A plugin decides what gets generated and tested; the
//     plan's plugin list is the authorisation for that. A strategy only wraps
//     the cases those plugins produce, so adding one cannot widen what the run
//     is allowed to attack. Adding a plugin is refused, not ignored: silently
//     dropping it would look like the card's selection had been honoured.
//   - Every added id must be in this deployment's catalog, not withdrawn, and
//     not remote-gated — the same three gates the wizard applies at submit.
//     Without them a hand-written request could name a strategy the engine
//     throws on (`native_generation_strategy_unknown`) or silently drops, and
//     the card would have promised a technique that never ran.
//
// Why this is worth the extra surface: maclaw's planner does not reliably carry
// a strategy the user asked for in the chat (measured 2026-10-20 — "攻击策略要
// 选择base64" produced a plan with no strategy at all). The card is where the
// user can see and correct that, and it only works if the correction reaches
// the engine.
func addedStrategyRefusal(refs []string) (code, message string, refused bool) {
	strategyPrefix := maclaw.CapabilitySourcePromptfooStrategy + ":"
	for _, raw := range refs {
		ref := strings.TrimSpace(raw)
		if ref == "" {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(ref), strategyPrefix) {
			return EnginePlanErrAddedRefNotStrategy,
				"计划卡只允许追加攻击策略（promptfoo_strategy:<id>）：" + ref + " 不是策略，插件与专家资源必须由计划本身选中。",
				true
		}
		id := strings.TrimSpace(ref[len(strategyPrefix):])
		if id == "" || !maclaw.PromptfooCatalogStrategyExists(id) {
			return EnginePlanErrAddedStrategyUnknown,
				"攻击策略 " + id + " 不在本环境的策略目录里，无法追加；请从计划卡的下拉里选择。",
				true
		}
	}
	// The same gates the wizard runs at submit, over just the added ids.
	added := addedStrategyRefs(refs, nil)
	engineRefs := make([]maclaw.EngineCapabilityRef, 0, len(added))
	for _, id := range added {
		engineRefs = append(engineRefs, maclaw.EngineCapabilityRef{ID: id})
	}
	if err := maclaw.ValidateEngineRunStrategyRetirement(engineRefs); err != nil {
		return EnginePlanErrAddedStrategyUnknown, err.Error(), true
	}
	if err := maclaw.ValidateEngineRunRemoteGeneration(nil, engineRefs); err != nil {
		return EnginePlanErrAddedStrategyUnknown, err.Error(), true
	}
	return "", "", false
}

// withAddedStrategies returns the selection with the added strategies appended.
//
// Deduplicated against what is already selected, so a card that adds a strategy
// the plan happened to pick does not run it twice. Callers must run
// addedStrategyRefusal first — this function assumes every ref is a usable
// strategy ref and does not re-check.
func (s promptfooEnginePlanSelection) withAddedStrategies(refs []string) promptfooEnginePlanSelection {
	added := addedStrategyRefs(refs, s.Strategies)
	if len(added) == 0 {
		return s
	}
	out := s
	out.Strategies = append(append([]string{}, s.Strategies...), added...)
	return out
}

// addedStrategyRefs extracts the ids from `promptfoo_strategy:<id>` refs,
// dropping blanks and anything already in `existing`.
func addedStrategyRefs(refs []string, existing []string) []string {
	strategyPrefix := maclaw.CapabilitySourcePromptfooStrategy + ":"
	seen := make(map[string]bool, len(existing)+len(refs))
	for _, id := range existing {
		seen[strings.ToLower(strings.TrimSpace(id))] = true
	}
	out := make([]string, 0, len(refs))
	for _, raw := range refs {
		ref := strings.TrimSpace(raw)
		if ref == "" || !strings.HasPrefix(strings.ToLower(ref), strategyPrefix) {
			continue
		}
		id := strings.TrimSpace(ref[len(strategyPrefix):])
		if id == "" || seen[strings.ToLower(id)] {
			continue
		}
		seen[strings.ToLower(id)] = true
		out = append(out, id)
	}
	return out
}

// normalizedRefSet trims a removal list into a lookup set. Empty entries are
// dropped: a blank ref names nothing, and treating it as "remove everything"
// would be the opposite of what an empty entry means.
func normalizedRefSet(refs []string) map[string]bool {
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if trimmed := strings.TrimSpace(ref); trimmed != "" {
			out[trimmed] = true
		}
	}
	return out
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
	// EnginePlanErrAddedRefNotStrategy — the card tried to ADD something on the
	// plugin or resource axis. The plan's plugins are the authorisation for what
	// gets generated, so only strategies may be added; see
	// addedStrategyRefusal.
	EnginePlanErrAddedRefNotStrategy = "promptfoo_added_ref_not_a_strategy"
	// EnginePlanErrAddedStrategyUnknown — the card added a strategy id this
	// deployment cannot run: not in the catalog, withdrawn in P3-1, or
	// remote-gated. Refused rather than dropped, because the card would
	// otherwise show a technique that never ran.
	EnginePlanErrAddedStrategyUnknown = "promptfoo_added_strategy_unknown"
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
