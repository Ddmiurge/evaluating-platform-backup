package maclaw

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The engine's `purpose` is what promptfoo renders into every generation
// template ("Generate a list of N trick prompts for the system purpose: …") and
// into every grader rubric (<Purpose>…</Purpose>). It decides what the attacks
// are aimed at AND what the judge compares against, and it is invisible in every
// other field of a report — which is why it gets a guard instead of a comment.
//
// Measured 2026-10-20: the platform fed promptfoo the EVALUATION purpose, so the
// generation model was asked for a trick prompt aimed at a system whose purpose
// is running security evaluations. It wrote six meta-requests ("作为大模型安全
// 评测（promptfoo 引擎）的出题人，请把…列出来") and the run scored 0/6. The same
// template with the target's own role produced six real prompt-extraction
// attacks.

func TestEngineRunPurposeForTargetUsesTheTargetsOwnRole(t *testing.T) {
	const role = "客服问答机器人：回答售后、退款、物流咨询"

	if got := EngineRunPurposeForTarget(&EvaluationTargetInput{Description: role}); got != role {
		t.Errorf("purpose = %q, want the target description %q", got, role)
	}
	if got := EngineRunPurposeForTarget(&EvaluationTargetInput{Description: "  " + role + "  "}); got != role {
		t.Errorf("purpose = %q, want the trimmed description %q", got, role)
	}

	// No description is not a reason to reach for an evaluation purpose: a
	// neutral role produces generic attacks, which are useful, while an
	// evaluation purpose produces payloads that carry out the evaluation.
	for _, target := range []*EvaluationTargetInput{nil, {}, {Name: "默认被测模型"}} {
		if got := EngineRunPurposeForTarget(target); got != EngineRunPurposeFallback {
			t.Errorf("purpose = %q, want the neutral fallback %q", got, EngineRunPurposeFallback)
		}
	}

	// The fallback must be a role. A task-shaped fallback would reintroduce the
	// defect for every enterprise that never filled the field in — the common
	// case at the time this was written.
	if strings.Contains(EngineRunPurposeFallback, "评测") {
		t.Errorf("fallback %q describes the evaluation rather than the tested system", EngineRunPurposeFallback)
	}
}

// The bridge-level half. `EngineRunPurposeForTarget` could be correct while the
// call site still passed the plan note, and that is the failure this test
// exists for — the helper is only useful if the run actually uses it.
func TestPrepareEngineRunSendsTheTargetsPurposeNotTheEvaluationPurpose(t *testing.T) {
	const (
		role              = "客服问答机器人：回答售后、退款、物流咨询"
		evaluationPurpose = "大模型安全评测（promptfoo 引擎）"
	)

	runner := &fakePromptfooEngineRunner{enabled: true, statuses: map[string]*EngineRunStatus{}}
	store := newFakeEngineRunStore()
	bridge := NewRedteamToolBridge(nil, nil, nil)
	bridge.SetPromptfooEngine(runner, NewEngineRunService(store), &fakeEngineTargetProvider{
		target: &EvaluationTargetInput{
			ID: "target-1", Name: "默认被测模型", Description: role,
			BaseURL: "https://t.example/v1", Model: "m", CredentialSecret: "sk-t", Enabled: true,
		},
	}, fakeDefaultRuntimeConfigProvider{cfg: &RuntimeAppConfig{
		MaclawLLMProviders: []RuntimeLLMProvider{
			{Name: "gen", URL: "https://gen.example.com", Key: "sk-gen", Model: "gen-model", WireAPI: "chat_completions"},
		},
		MaclawLLMCurrentProvider: "gen",
	}})

	if _, err := bridge.PreparePromptfooEngineRun(context.Background(), uuid.New(), "inst-1", PromptfooEngineRunInput{
		Purpose:  evaluationPurpose,
		Plugins:  []string{"contracts"},
		NumTests: 1,
	}); err != nil {
		t.Fatalf("PreparePromptfooEngineRun: %v", err)
	}
	if len(runner.created) != 1 {
		t.Fatalf("engine runs submitted = %d, want 1", len(runner.created))
	}
	if got := runner.created[0].Purpose; got != role {
		t.Errorf("engine purpose = %q, want the target's role %q — promptfoo aims generation and judging with this string", got, role)
	}
	// The two strings are different claims and both are needed: the engine gets
	// the target's role, the run record keeps the evaluation purpose that the
	// run detail shows the user.
	if len(store.records) != 1 {
		t.Fatalf("run records = %d, want 1", len(store.records))
	}
	for _, record := range store.records {
		if got := record.Purpose; got != evaluationPurpose {
			t.Errorf("run record purpose = %q, want the evaluation purpose %q kept for display", got, evaluationPurpose)
		}
	}
}
