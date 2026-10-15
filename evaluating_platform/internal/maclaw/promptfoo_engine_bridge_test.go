package maclaw

// promptfoo_engine_bridge_test.go — Phase 1 bridge tool coverage: plugin
// catalog search, result mappers (polarity semantics), and the confirmed-run
// orchestration against fakes.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestSearchPromptfooPluginCatalog(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		wantHit []string
	}{
		{"chinese harmful", "有害内容", []string{"promptfoo_plugin:harmful"}},
		{"chinese pii", "隐私泄露", []string{"promptfoo_plugin:pii"}},
		// P2-1：这三个期望值原本指向 `promptfoo_plugin:prompt-injection` /
		// `promptfoo_plugin:jailbreak` —— 两个 promptfoo 根本不接受的 plugin id。
		// 它们已移出目录（见 retiredPluginCatalog），所以「搜索能命中它们」不再是
		// 一个可达的期望，而「搜索仍能命中所要的**能力**」才是。注入面留在
		// indirect-prompt-injection（插件位）与策略位；越狱面留在策略位
		// （jailbreak / jailbreak-templates）与基准插件（harmbench / pliny）。
		{"chinese injection", "提示注入", []string{"promptfoo_plugin:indirect-prompt-injection"}},
		{"chinese jailbreak", "越狱", []string{"promptfoo_plugin:harmbench", "promptfoo_strategy:goat"}},
		// P2-2: `promptfoo_strategy:jailbreak` no longer appears — that strategy is
		// now measured as remote-required (its action delegates to the
		// iterative:meta provider, which refuses to construct under fail-closed)
		// and is withheld. The query must still reach a jailbreak capability that
		// does run locally.
		{"english id", "jailbreak", []string{"promptfoo_strategy:jailbreak-templates"}},
		{"generic engine", "promptfoo 引擎评测", []string{"promptfoo_plugin:harmful"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cards := SearchPromptfooPluginCatalog(tc.query, 5)
			if len(cards) == 0 {
				t.Fatalf("no cards for query %q", tc.query)
			}
			refs := map[string]bool{}
			for _, card := range cards {
				refs[card.SourceRef] = true
			}
			for _, want := range tc.wantHit {
				if !refs[want] {
					t.Fatalf("query %q: expected %s in results, got %v", tc.query, want, refs)
				}
			}
		})
	}

	// The withdrawn ids must not be offered by search either. The catalog filter
	// (L1) is what removes them, but search is the surface a MaClaw planner
	// actually reads — an id reachable there would be planned and then fail
	// mid-run, which is exactly the failure P0-2 exists to close.
	for _, gone := range IllegalPluginIDs() {
		for _, card := range SearchPromptfooPluginCatalog(gone, 20) {
			if card.SourceRef == "promptfoo_plugin:"+gone {
				t.Errorf("已下线的 plugin id %q 仍能被目录搜索命中（%s）", gone, card.SourceRef)
			}
		}
	}
}

func TestSearchPromptfooPluginCatalogLimit(t *testing.T) {
	cards := SearchPromptfooPluginCatalog("", 3)
	if len(cards) != 3 {
		t.Fatalf("limit 3 returned %d cards", len(cards))
	}
}

func TestCapabilityCatalogSearchIncludesPromptfooPlugins(t *testing.T) {
	service := NewCapabilityCatalogService(nil, nil)
	cards, err := service.Search(context.Background(), CapabilityCatalogQuery{Query: "promptfoo 引擎评测 有害内容", Limit: 8})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, card := range cards {
		if card.SourceType == CapabilitySourcePromptfooPlugin && card.SourceRef == "promptfoo_plugin:harmful" {
			found = true
		}
	}
	if !found {
		t.Fatalf("promptfoo_plugin:harmful not in results: %+v", cards)
	}
}

func TestEngineSafeResultCountsPolarity(t *testing.T) {
	// The engine already flipped the polarity internally (pf grading fail =
	// attack success). The mapper must NOT flip again: attack_success maps
	// directly to the platform "success" (attack confirmed) count.
	result := &EngineSafeResult{
		Totals: EngineRunTotals{Probes: 5, AttackSuccess: 2, PassRate: 0.6},
	}
	counts := EngineSafeResultCounts(result)
	if counts["success"] != 2 || counts["failure"] != 3 {
		t.Fatalf("counts = %v, want success=2 failure=3", counts)
	}
	if got := EngineSafeResultCounts(nil); got["success"] != 0 || got["failure"] != 0 {
		t.Fatalf("nil result counts = %v", got)
	}
}

func TestEngineSafeResultFindings(t *testing.T) {
	result := &EngineSafeResult{
		Totals: EngineRunTotals{Probes: 3, AttackSuccess: 1, PassRate: 0.67},
		PluginStats: []EnginePluginStat{
			{PluginID: "harmful", Label: "harmful", Probes: 2, AttackSuccess: 1, SuccessRate: 0.5, MaxSeverity: "high"},
			{PluginID: "pii", Label: "pii", Probes: 1, AttackSuccess: 0, SuccessRate: 0, MaxSeverity: "none"},
		},
	}
	findings := EngineSafeResultFindings(result)
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	if findings[0].Severity != "high" || findings[0].Metadata["engine"] != "promptfoo" {
		t.Fatalf("finding[0] = %+v", findings[0])
	}
	// none-severity with no success falls back to low
	if findings[1].Severity != "low" {
		t.Fatalf("finding[1].severity = %s, want low", findings[1].Severity)
	}
	if EngineSafeResultFindings(nil) != nil {
		t.Fatal("nil result should produce nil findings")
	}
}

type fakePromptfooEngineRunner struct {
	enabled    bool
	createErr  error
	statuses   map[string]*EngineRunStatus
	statusHook func(runID string, call int) *EngineRunStatus
	created    []*EngineRunRequest
	getCalls   int
}

func (f *fakePromptfooEngineRunner) Enabled() bool { return f.enabled }

func (f *fakePromptfooEngineRunner) CreateRun(_ context.Context, run *EngineRunRequest) error {
	f.created = append(f.created, run)
	return f.createErr
}

func (f *fakePromptfooEngineRunner) GetRun(_ context.Context, runID string) (*EngineRunStatus, error) {
	f.getCalls++
	if f.statusHook != nil {
		return f.statusHook(runID, f.getCalls), nil
	}
	if status, ok := f.statuses[runID]; ok {
		return status, nil
	}
	return &EngineRunStatus{RunID: runID, Phase: EnginePhaseSucceeded}, nil
}

type fakeEngineTargetProvider struct {
	target *EvaluationTargetInput
	err    error
}

func (p *fakeEngineTargetProvider) GetTargetWithSecret(context.Context, uuid.UUID) (*EvaluationTargetInput, error) {
	return p.target, p.err
}

func newPromptfooTestBridge(t *testing.T) (*RedteamToolBridge, *fakePromptfooEngineRunner, *fakeEngineRunStore) {
	t.Helper()
	runner := &fakePromptfooEngineRunner{enabled: true, statuses: map[string]*EngineRunStatus{}}
	store := newFakeEngineRunStore()
	bridge := NewRedteamToolBridge(nil, nil, nil)
	bridge.SetPromptfooEngine(runner, NewEngineRunService(store), &fakeEngineTargetProvider{
		target: &EvaluationTargetInput{
			ID: "target-1", BaseURL: "https://t.example/v1", Model: "m", CredentialSecret: "sk-t", Enabled: true,
		},
	}, fakeDefaultRuntimeConfigProvider{cfg: &RuntimeAppConfig{
		MaclawLLMProviders: []RuntimeLLMProvider{
			{Name: "gen", URL: "https://gen.example.com", Key: "sk-gen", Model: "gen-model", WireAPI: "chat_completions"},
		},
		MaclawLLMCurrentProvider: "gen",
	}})
	return bridge, runner, store
}

func TestRunPromptfooRedteamEvaluationOrchestration(t *testing.T) {
	bridge, runner, store := newPromptfooTestBridge(t)
	userID := uuid.New()
	// Engine reports a succeeded run with one attack success on harmful.
	runner.statusHook = func(runID string, call int) *EngineRunStatus {
		return &EngineRunStatus{
			RunID: runID, Phase: EnginePhaseSucceeded,
			PlannedCount: 2, ExecutedCount: 2, DurationMs: 1200,
			Result: &EngineSafeResult{
				Totals:         EngineRunTotals{Probes: 2, AttackSuccess: 1, PassRate: 0.5},
				SeverityCounts: map[string]int{"high": 1},
				PluginStats: []EnginePluginStat{
					{PluginID: "harmful", Label: "harmful", Probes: 2, AttackSuccess: 1, SuccessRate: 0.5, MaxSeverity: "high"},
				},
			},
		}
	}

	out, err := bridge.RunPromptfooRedteamEvaluation(context.Background(), userID, "inst-1", PromptfooEngineRunInput{
		RunID:   "maclaw-run-1",
		Purpose: "客服助手安全评测",
		// P2-4: `harmful` expands to 26 sub-plugins, so 2 has no legal split.
		NumTests: 30,
		Plugins:  []string{"harmful"},
		// P3-1: `direct` 是平台自造维度、已下线；用一个 promptfoo 原生策略替代，
		// 这条测试关心的是编排，不是「direct 可用」。
		Strategies: []string{"base64"},
	})
	if err != nil {
		t.Fatalf("RunPromptfooRedteamEvaluation: %v", err)
	}
	if out.Status != "completed" || out.EngineRunID == "" {
		t.Fatalf("out = %+v", out)
	}
	if out.Counts["success"] != 1 || out.Counts["failure"] != 1 {
		t.Fatalf("counts = %v", out.Counts)
	}
	if out.ReportID == "" || len(out.EvidenceHandles) != 1 {
		t.Fatalf("report/evidence missing: %+v", out)
	}
	if out.Metadata["engine"] != "promptfoo" {
		t.Fatalf("metadata = %v", out.Metadata)
	}
	// Submitted engine request carries generation creds + plugins.
	if len(runner.created) != 1 {
		t.Fatalf("engine create calls = %d", len(runner.created))
	}
	req := runner.created[0]
	if req.Credentials.Generation == nil || req.Credentials.Generation.APIKey != "sk-gen" {
		t.Fatalf("generation creds missing: %+v", req.Credentials)
	}
	if req.Credentials.Target.APIKey != "sk-t" || len(req.Plugins) != 1 || req.Plugins[0].ID != "harmful" {
		t.Fatalf("engine request = %+v", req)
	}
	// Record is persisted as succeeded with result.
	record := store.records[out.EngineRunID]
	if record == nil {
		t.Fatalf("record not found: %v", out.EngineRunID)
	}
	if record.Status != EnginePhaseSucceeded || record.Result == nil {
		t.Fatalf("record = %+v", record)
	}
}

func TestRunPromptfooRedteamEvaluationDefaultsAndValidation(t *testing.T) {
	bridge, runner, store := newPromptfooTestBridge(t)
	userID := uuid.New()

	// No purpose → error.
	if _, err := bridge.RunPromptfooRedteamEvaluation(context.Background(), userID, "i", PromptfooEngineRunInput{}); err == nil {
		t.Fatal("expected purpose validation error")
	}
	// No plugins → defaults to harmful.
	out, err := bridge.RunPromptfooRedteamEvaluation(context.Background(), userID, "i", PromptfooEngineRunInput{Purpose: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(runner.created[0].Plugins) != 1 || runner.created[0].Plugins[0].ID != "harmful" {
		t.Fatalf("default plugins = %+v", runner.created[0].Plugins)
	}
	// P2-4: an absent count resolves to the selection's floor when that is higher
	// than the flat default. The default plugin is `harmful` (26 sub-plugins), so
	// the flat default of 5 is not submittable — without raising it, the platform's
	// own default request would be refused by its own coverage check.
	wantFloor := NumTestsFloorForTest([]string{"harmful"})
	if runner.created[0].NumTests != wantFloor || wantFloor <= promptfooEngineDefaultTests {
		t.Fatalf("default num_tests = %d, want the selection floor %d (flat default %d)",
			runner.created[0].NumTests, wantFloor, promptfooEngineDefaultTests)
	}
	_ = store
	_ = out
}

func TestRunPromptfooRedteamEvaluationFailsWithoutGeneration(t *testing.T) {
	bridge := NewRedteamToolBridge(nil, nil, nil)
	bridge.SetPromptfooEngine(&fakePromptfooEngineRunner{enabled: true}, NewEngineRunService(newFakeEngineRunStore()), &fakeEngineTargetProvider{
		target: &EvaluationTargetInput{ID: "t", BaseURL: "u", Model: "m", CredentialSecret: "k", Enabled: true},
	}, fakeDefaultRuntimeConfigProvider{cfg: nil})
	if _, err := bridge.RunPromptfooRedteamEvaluation(context.Background(), uuid.New(), "i", PromptfooEngineRunInput{Purpose: "p"}); err == nil {
		t.Fatal("expected generation_not_configured error")
	}
}

func TestRunPromptfooRedteamEvaluationEngineFailure(t *testing.T) {
	bridge, runner, _ := newPromptfooTestBridge(t)
	runner.statusHook = func(runID string, call int) *EngineRunStatus {
		return &EngineRunStatus{RunID: runID, Phase: EnginePhaseFailed, ErrorCode: "generation_failed"}
	}
	out, err := bridge.RunPromptfooRedteamEvaluation(context.Background(), uuid.New(), "i", PromptfooEngineRunInput{Purpose: "p"})
	if err != nil {
		t.Fatalf("failed run should not return go error: %v", err)
	}
	if out.Status != "failed" || out.ErrorCode != "generation_failed" {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetPromptfooEvaluationResult(t *testing.T) {
	bridge, runner, _ := newPromptfooTestBridge(t)
	userID := uuid.New()
	runner.statusHook = func(runID string, call int) *EngineRunStatus {
		return &EngineRunStatus{
			RunID: runID, Phase: EnginePhaseSucceeded, PlannedCount: 1, ExecutedCount: 1, DurationMs: 500,
			Result: &EngineSafeResult{Totals: EngineRunTotals{Probes: 1, AttackSuccess: 0, PassRate: 1}},
		}
	}
	created, err := bridge.RunPromptfooRedteamEvaluation(context.Background(), userID, "i", PromptfooEngineRunInput{Purpose: "p"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := bridge.GetPromptfooEvaluationResult(context.Background(), userID, created.EngineRunID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EngineRunID != created.EngineRunID || got.Phase != string(EnginePhaseSucceeded) || got.Result == nil {
		t.Fatalf("got = %+v", got)
	}
	// Wrong owner → not found.
	if _, err := bridge.GetPromptfooEvaluationResult(context.Background(), uuid.New(), created.EngineRunID); err == nil {
		t.Fatal("expected not-found for other owner")
	}
	_ = runner
}

func TestPromptfooEngineNotConfigured(t *testing.T) {
	bridge := NewRedteamToolBridge(nil, nil, nil)
	if _, err := bridge.SearchRedteamPluginCatalog(SearchRedteamPluginCatalogInput{Query: "x"}); err != nil {
		t.Fatalf("catalog search should not require engine wiring: %v", err)
	}
	if _, err := bridge.GetPromptfooEvaluationResult(context.Background(), uuid.New(), "er-1"); err == nil {
		t.Fatal("expected not-configured error")
	}
}

// ---------------------------------------------------------------------------
// U9 · tool_call_signals 契约往返
// ---------------------------------------------------------------------------

// TestEngineSafeResultCarriesToolCallSignals pins the U9 wire contract: the
// engine's `tool_call_signals` must survive the JSON hop into the struct the
// BFF persists into `engine_runs.result`.
//
// This is a **struct allowlist** on the Go side, which is exactly the U7/U8
// silent-strip failure mode in a different guise: an engine field that nobody
// declares here is not "not persisted yet", it is *silently dropped*, and the
// report then claims a run had no tool-call activity when it did.
func TestEngineSafeResultCarriesToolCallSignals(t *testing.T) {
	payload := `{
      "totals": {"probes": 4, "attack_success": 1, "pass_rate": 0.75},
      "severity_counts": {"critical": 0, "high": 1, "medium": 0, "low": 0},
      "risk_categories": [],
      "plugin_stats": [],
      "strategy_stats": [],
      "token_usage": {"generation": 0, "judging": 0, "target": 0},
      "tool_call_signals": {"probes_with_tool_calls": 2, "total_tool_call_markers": 5}
    }`
	var result EngineSafeResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.ToolCallSignals == nil {
		t.Fatal("tool_call_signals was silently dropped by the Go struct")
	}
	if result.ToolCallSignals.ProbesWithToolCalls != 2 {
		t.Fatalf("probes_with_tool_calls = %d, want 2", result.ToolCallSignals.ProbesWithToolCalls)
	}
	if result.ToolCallSignals.TotalToolCallMarkers != 5 {
		t.Fatalf("total_tool_call_markers = %d, want 5", result.ToolCallSignals.TotalToolCallMarkers)
	}
}

// TestEngineSafeResultToolCallSignalsOmittedWhenAbsent pins the backward
// compatibility direction: a pre-U9 engine payload (or any run without the
// field) must not fail to decode, and must not fabricate a zero.
//
// Fabricating zeros would be the more dangerous half: "0 probes called tools"
// reads as a measurement, while absent honestly reads as "this engine did not
// report it".
func TestEngineSafeResultToolCallSignalsOmittedWhenAbsent(t *testing.T) {
	payload := `{
      "totals": {"probes": 1, "attack_success": 0, "pass_rate": 1},
      "severity_counts": {},
      "risk_categories": [],
      "plugin_stats": [],
      "strategy_stats": [],
      "token_usage": {"generation": 0, "judging": 0, "target": 0}
    }`
	var result EngineSafeResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatalf("pre-U9 payload must still decode: %v", err)
	}
	if result.ToolCallSignals != nil {
		t.Fatalf("absent field must stay nil, got %+v", result.ToolCallSignals)
	}
	// And a present-but-zero report must round-trip as a real measurement.
	zero := `{"totals":{"probes":1,"attack_success":0,"pass_rate":1},"severity_counts":{},
      "risk_categories":[],"plugin_stats":[],"strategy_stats":[],
      "token_usage":{"generation":0,"judging":0,"target":0},
      "tool_call_signals":{"probes_with_tool_calls":0,"total_tool_call_markers":0}}`
	var measured EngineSafeResult
	if err := json.Unmarshal([]byte(zero), &measured); err != nil {
		t.Fatalf("zero-valued report must decode: %v", err)
	}
	if measured.ToolCallSignals == nil {
		t.Fatal("a present zero must stay distinguishable from an absent field")
	}
	if measured.ToolCallSignals.ProbesWithToolCalls != 0 {
		t.Fatalf("probes_with_tool_calls = %d, want 0", measured.ToolCallSignals.ProbesWithToolCalls)
	}
}

// TestPrepareEngineRunRefusesNumTestsAboveCap pins P2-2 (S-4) on the chat
// confirm path: a confirmed count above the cap is REFUSED, not quietly
// reduced.
//
// The confirmed count is the number the user approved on the plan card. Clamping
// it would make the report and the approval disagree, and nothing in the
// response would say so — the user would only notice by counting the probes. The
// refusal must also leave no run record, so a rejected confirm is free.
func TestPrepareEngineRunRefusesNumTestsAboveCap(t *testing.T) {
	bridge, _, store := newPromptfooTestBridge(t)

	_, err := bridge.PreparePromptfooEngineRun(context.Background(), uuid.New(), "inst-1", PromptfooEngineRunInput{
		Purpose:  "over the cap",
		Plugins:  []string{"harmful"},
		NumTests: EngineMaxNumTests + 1,
	})
	if err == nil {
		t.Fatal("PreparePromptfooEngineRun silently accepted a count above the cap")
	}
	var tooLarge *EngineNumTestsTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error type = %T (%v), want *EngineNumTestsTooLargeError", err, err)
	}
	if tooLarge.Requested != EngineMaxNumTests+1 || tooLarge.Max != EngineMaxNumTests {
		t.Errorf("error carries requested=%d max=%d, want %d / %d",
			tooLarge.Requested, tooLarge.Max, EngineMaxNumTests+1, EngineMaxNumTests)
	}
	if len(store.records) != 0 {
		t.Fatalf("run record created for a rejected count: %+v", store.records)
	}

	// At the cap the path must still work — the pair matters, because a guard
	// that also rejected the boundary would pass the test above while breaking
	// every maximum-size run.
	prepared, err := bridge.PreparePromptfooEngineRun(context.Background(), uuid.New(), "inst-1", PromptfooEngineRunInput{
		Purpose:  "at the cap",
		Plugins:  []string{"harmful"},
		NumTests: EngineMaxNumTests,
	})
	if err != nil {
		t.Fatalf("count at the cap (%d) was refused: %v", EngineMaxNumTests, err)
	}
	if prepared.NormalizedIn.NumTests != EngineMaxNumTests {
		t.Errorf("num_tests = %d, want %d unchanged", prepared.NormalizedIn.NumTests, EngineMaxNumTests)
	}
}
