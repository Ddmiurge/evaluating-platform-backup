package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
)

// sameIDs compares two id lists treating nil and empty as equal. The extractor
// builds its lists with make(), so "no strategies" arrives as a non-nil empty
// slice; a test that insisted on nil would be pinning an implementation detail
// rather than the selection.
func sameIDs(got, want []string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

func TestPromptfooEnginePlanSelectionFromContent(t *testing.T) {
	cases := []struct {
		name           string
		content        string
		wantOK         bool
		wantPlugins    []string
		wantStrategies []string
	}{
		{
			name:        "selected_capability_refs with promptfoo plugins",
			content:     `{"selected_capability_refs":["promptfoo_plugin:harmful","promptfoo_plugin:pii"],"test_count":3}`,
			wantOK:      true,
			wantPlugins: []string{"harmful", "pii"},
		},
		{
			name:           "selected_capability_refs with promptfoo strategies",
			content:        `{"selected_capability_refs":["promptfoo_strategy:base64","promptfoo_strategy:crescendo"]}`,
			wantOK:         true,
			wantStrategies: []string{"base64", "crescendo"},
		},
		{
			name:        "plugins array only",
			content:     `{"plugins":["harmful","jailbreak"],"note":"x"}`,
			wantOK:      true,
			wantPlugins: []string{"harmful", "jailbreak"},
		},
		{
			name:           "strategies array (bare ids)",
			content:        `{"plugins":["harmful"],"strategies":["base64"]}`,
			wantOK:         true,
			wantPlugins:    []string{"harmful"},
			wantStrategies: []string{"base64"},
		},
		{
			name:           "strategies array of objects",
			content:        `{"plugins":["harmful"],"strategies":[{"id":"base64"},{"id":"rot13"}]}`,
			wantOK:         true,
			wantPlugins:    []string{"harmful"},
			wantStrategies: []string{"base64", "rot13"},
		},
		{
			name:        "selected_capabilities with source_type",
			content:     `{"selected_capabilities":[{"source_type":"promptfoo_plugin","source_ref":"promptfoo_plugin:pii"}]}`,
			wantOK:      true,
			wantPlugins: []string{"pii"},
		},
		{
			name:           "selected_capabilities with strategy source_type",
			content:        `{"selected_capabilities":[{"source_type":"promptfoo_strategy","source_ref":"promptfoo_strategy:goat"}]}`,
			wantOK:         true,
			wantStrategies: []string{"goat"},
		},
		{
			name:        "no engine refs",
			content:     `{"selected_capability_refs":["sample:123"],"plugins":[]}`,
			wantOK:      false,
		},
		{
			name:    "not json",
			content: `plain text plan`,
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selection, ok := promptfooEnginePlanSelectionFromContent(tc.content)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (selection=%+v)", ok, tc.wantOK, selection)
			}
			if !tc.wantOK {
				return
			}
			if !sameIDs(selection.Plugins, tc.wantPlugins) {
				t.Errorf("plugins = %v, want %v", selection.Plugins, tc.wantPlugins)
			}
			if !sameIDs(selection.Strategies, tc.wantStrategies) {
				t.Errorf("strategies = %v, want %v", selection.Strategies, tc.wantStrategies)
			}
		})
	}
}

// TestPromptfooEnginePlanSelectionKeepsStrategyRefs is the regression test for
// the P1 measured on ai02 (阶段三服务器验证记录 §3).
//
// Before the fix the extractor looked only for `promptfoo_plugin:`, so this
// exact plan payload — taken verbatim from the server run — produced
// strategies=[] and the engine executed "ask the target directly" while the card
// had shown `base64`. The wizard path applied the same `base64` correctly, so
// the loss was specific to this path.
//
// Counter-proof: delete the `promptfoo_strategy:` branch in addRef → red.
func TestPromptfooEnginePlanSelectionKeepsStrategyRefs(t *testing.T) {
	const measuredPlan = `{"selected_capability_refs":["promptfoo_plugin:prompt-extraction","promptfoo_strategy:base64"]}`

	selection, ok := promptfooEnginePlanSelectionFromContent(measuredPlan)
	if !ok {
		t.Fatal("expected ok")
	}
	if !reflect.DeepEqual(selection.Plugins, []string{"prompt-extraction"}) {
		t.Errorf("plugins = %v, want [prompt-extraction]", selection.Plugins)
	}
	if !reflect.DeepEqual(selection.Strategies, []string{"base64"}) {
		t.Fatalf("strategies = %v, want [base64] — the strategy the plan card named "+
			"would be dropped and the run would silently become a direct ask", selection.Strategies)
	}
	if selection.DeclaredStrategies != 1 {
		t.Errorf("declared strategies = %d, want 1", selection.DeclaredStrategies)
	}
}

// TestPromptfooEnginePlanSelectionCountsMalformedStrategyRefs pins the signal the
// fail-closed guard keys on: a ref that cannot yield an id is still *declared*.
// Without the count, `promptfoo_strategy:` (empty id) would look identical to a
// plan that never mentioned a strategy.
func TestPromptfooEnginePlanSelectionCountsMalformedStrategyRefs(t *testing.T) {
	selection, ok := promptfooEnginePlanSelectionFromContent(
		`{"selected_capability_refs":["promptfoo_plugin:harmful","promptfoo_strategy:"]}`)
	if !ok {
		t.Fatal("expected ok")
	}
	if selection.DeclaredStrategies != 1 {
		t.Errorf("declared strategies = %d, want 1", selection.DeclaredStrategies)
	}
	if len(selection.Strategies) != 0 {
		t.Errorf("strategies = %v, want empty (the id is blank)", selection.Strategies)
	}
}

func TestPromptfooEnginePlanSelectionDedupesAndSorts(t *testing.T) {
	selection, ok := promptfooEnginePlanSelectionFromContent(
		`{"selected_capability_refs":["promptfoo_plugin:pii","promptfoo_plugin:harmful","promptfoo_plugin:pii"],` +
			`"plugins":["harmful"],"strategies":["rot13","base64","rot13"]}`)
	if !ok {
		t.Fatal("expected ok")
	}
	if !reflect.DeepEqual(selection.Plugins, []string{"harmful", "pii"}) {
		t.Errorf("plugins = %v, want sorted+deduped [harmful pii]", selection.Plugins)
	}
	if !reflect.DeepEqual(selection.Strategies, []string{"base64", "rot13"}) {
		t.Errorf("strategies = %v, want sorted+deduped [base64 rot13]", selection.Strategies)
	}
}

func TestPromptfooEnginePlanSelectionMixedPlanStillDetected(t *testing.T) {
	// A plan mixing engine refs with expert sample refs is NOT a pure engine
	// plan — the caller (ConfirmPlan) checks resources separately; the
	// extractor itself should still report the engine refs.
	selection, ok := promptfooEnginePlanSelectionFromContent(
		`{"selected_capability_refs":["promptfoo_plugin:harmful","sample:abc"]}`)
	if !ok {
		t.Fatal("expected ok")
	}
	if !reflect.DeepEqual(selection.Plugins, []string{"harmful"}) {
		t.Fatalf("plugins = %v", selection.Plugins)
	}
	if len(selection.Strategies) != 0 {
		t.Errorf("strategies = %v, want empty", selection.Strategies)
	}
}

func TestPromptfooEnginePlanRefusal(t *testing.T) {
	cases := []struct {
		name        string
		selection   promptfooEnginePlanSelection
		wantCode    string
		wantRefused bool
	}{
		{
			name:      "plugins and strategies — executable",
			selection: promptfooEnginePlanSelection{Plugins: []string{"harmful"}, Strategies: []string{"base64"}, DeclaredStrategies: 1},
		},
		{
			name:      "plugins only — executable (no strategy was declared)",
			selection: promptfooEnginePlanSelection{Plugins: []string{"harmful"}},
		},
		{
			name:        "no plugins — nothing to generate cases from",
			selection:   promptfooEnginePlanSelection{Strategies: []string{"base64"}, DeclaredStrategies: 1},
			wantCode:    EnginePlanErrNoPlugins,
			wantRefused: true,
		},
		{
			name:        "strategy declared but unresolvable — would silently run without it",
			selection:   promptfooEnginePlanSelection{Plugins: []string{"harmful"}, DeclaredStrategies: 1},
			wantCode:    EnginePlanErrStrategyRefsUnresolved,
			wantRefused: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, message, refused := promptfooEnginePlanRefusal(tc.selection)
			if refused != tc.wantRefused {
				t.Fatalf("refused = %v, want %v (code=%q)", refused, tc.wantRefused, code)
			}
			if tc.wantRefused {
				if code != tc.wantCode {
					t.Errorf("code = %q, want %q", code, tc.wantCode)
				}
				if message == "" {
					t.Error("refusal carries no message; the user cannot act on it")
				}
			}
		})
	}
}

func TestPromptfooEnginePlanSelectionWithoutRemovedRefs(t *testing.T) {
	base := promptfooEnginePlanSelection{
		Plugins:            []string{"harmful", "pii"},
		Strategies:         []string{"base64", "rot13"},
		DeclaredStrategies: 2,
	}

	t.Run("removes the named plugin", func(t *testing.T) {
		out := base.withoutRemovedRefs(normalizedRefSet([]string{"promptfoo_plugin:pii"}))
		if !sameIDs(out.Plugins, []string{"harmful"}) {
			t.Errorf("plugins = %v, want [harmful]", out.Plugins)
		}
		if !sameIDs(out.Strategies, []string{"base64", "rot13"}) {
			t.Errorf("strategies = %v, want both", out.Strategies)
		}
	})

	t.Run("removes the named strategy and keeps the fail-closed count honest", func(t *testing.T) {
		out := base.withoutRemovedRefs(normalizedRefSet([]string{"promptfoo_strategy:rot13"}))
		if !sameIDs(out.Strategies, []string{"base64"}) {
			t.Errorf("strategies = %v, want [base64]", out.Strategies)
		}
		// DeclaredStrategies is the "plan named a strategy but none survived"
		// signal. A deliberate removal must not trip it — otherwise the user takes
		// the last strategy off and the confirm is refused as an unresolved ref.
		if out.DeclaredStrategies != 1 {
			t.Errorf("declared strategies = %d, want 1", out.DeclaredStrategies)
		}
		if _, _, refused := promptfooEnginePlanRefusal(out); refused {
			t.Error("removing one strategy must not be refused")
		}
	})

	t.Run("removing every strategy leaves nothing to refuse on", func(t *testing.T) {
		out := base.withoutRemovedRefs(normalizedRefSet([]string{
			"promptfoo_strategy:base64", "promptfoo_strategy:rot13",
		}))
		if len(out.Strategies) != 0 || out.DeclaredStrategies != 0 {
			t.Errorf("out = %+v, want no strategies declared", out)
		}
		if _, _, refused := promptfooEnginePlanRefusal(out); refused {
			t.Error("a plan with strategies removed deliberately must run without them")
		}
	})

	t.Run("removing the only plugin is refused, not silently run empty", func(t *testing.T) {
		out := base.withoutRemovedRefs(normalizedRefSet([]string{
			"promptfoo_plugin:harmful", "promptfoo_plugin:pii",
		}))
		code, _, refused := promptfooEnginePlanRefusal(out)
		if !refused || code != EnginePlanErrNoPlugins {
			t.Fatalf("refused = %v code = %q, want %q", refused, code, EnginePlanErrNoPlugins)
		}
	})

	t.Run("a removal the plan never offered changes nothing", func(t *testing.T) {
		// REMOVAL ONLY: this is the trust boundary. A request may not add a
		// capability the plan did not select, and naming one that is absent is a
		// no-op rather than an insertion.
		out := base.withoutRemovedRefs(normalizedRefSet([]string{
			"promptfoo_plugin:cyberseceval", "skillhub:some-skill",
		}))
		if !sameIDs(out.Plugins, base.Plugins) || !sameIDs(out.Strategies, base.Strategies) {
			t.Fatalf("out = %+v, want the plan's selection unchanged", out)
		}
	})

	t.Run("blank refs are ignored", func(t *testing.T) {
		out := base.withoutRemovedRefs(normalizedRefSet([]string{"", "   "}))
		if !sameIDs(out.Plugins, base.Plugins) {
			t.Fatalf("plugins = %v, want unchanged", out.Plugins)
		}
	})
}

// TestConfirmPlanRefusesRemovalsItCannotHonour is the regression test for the
// 2026-10-19 P1.
//
// Maclaw does not always fill `selected_capability_refs` (it sometimes writes the
// plugins only into `risk_types`). Such a plan falls through to the runtime path,
// which has no consumer for a removal list — measured on ai02: "remove every
// plugin" was **accepted and re-ran the whole plan**, burning quota while the card
// claimed the user had narrowed it.
//
// The card only shows × for refs it can render, so a normal user cannot reach
// this; an API caller can. Refusing is the only honest answer — executing the
// plan unchanged is the silent downgrade the whole mechanism exists to prevent.
func TestConfirmPlanRefusesRemovalsItCannotHonour(t *testing.T) {
	gateway := &fakeMaclawRuntimeGateway{
		enabled: true,
		messages: []maclaw.RuntimeMessage{{
			ID:       "msg_plan_without_refs",
			Role:     "assistant",
			Metadata: map[string]string{"response_source": "plan_confirm"},
			Content: `{"response_source":"plan_confirm","target_summary":{"target_type":"llm"},` +
				`"risk_types":["jailbreak"],"test_count":5,"selection_strategy":"random","requires_confirmation":true}`,
		}},
	}
	handler := NewMaclawRuntimeHandler(gateway, "inst_1")
	router := newEnterpriseRoleRouter()
	router.POST("/maclaw/evaluation/sessions/:id/confirm", func(c *gin.Context) {
		c.Set("user_id", uuid.NewString())
		handler.ConfirmPlan(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/sessions/sess_1/confirm",
		strings.NewReader(`{"plan_message_id":"msg_plan_without_refs","test_count":5,"removed_capability_refs":["promptfoo_plugin:contracts"]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want 409", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "plan_capability_removal_unsupported") {
		t.Fatalf("body = %s, want the refusal code", w.Body.String())
	}
	// And nothing was executed: the whole point is not re-running the plan.
	if gateway.confirmCalls != 0 {
		t.Fatalf("confirmCalls = %d, want 0 — the plan must not be executed", gateway.confirmCalls)
	}
}
