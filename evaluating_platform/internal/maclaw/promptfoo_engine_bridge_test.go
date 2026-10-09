package maclaw

// promptfoo_engine_bridge_test.go — Phase 1 bridge tool coverage: plugin
// catalog search, result mappers (polarity semantics), and the confirmed-run
// orchestration against fakes.

import (
	"context"
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
		{"chinese injection", "提示注入", []string{"promptfoo_plugin:prompt-injection"}},
		{"chinese jailbreak", "越狱", []string{"promptfoo_plugin:jailbreak"}},
		{"english id", "jailbreak", []string{"promptfoo_plugin:jailbreak"}},
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
	enabled   bool
	createErr error
	statuses  map[string]*EngineRunStatus
	statusHook func(runID string, call int) *EngineRunStatus
	created   []*EngineRunRequest
	getCalls  int
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
		RunID:     "maclaw-run-1",
		Purpose:   "客服助手安全评测",
		NumTests:  2,
		Plugins:   []string{"harmful"},
		Strategies: []string{"direct"},
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
	if runner.created[0].NumTests != promptfooEngineDefaultTests {
		t.Fatalf("default num_tests = %d", runner.created[0].NumTests)
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
