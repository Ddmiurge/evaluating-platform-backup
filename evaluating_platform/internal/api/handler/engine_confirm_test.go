package handler

import (
	"testing"
)

func TestPromptfooEngineRefsFromPlan(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantOK     bool
		wantPlugins []string
	}{
		{
			name:   "selected_capability_refs with promptfoo plugins",
			content: `{"selected_capability_refs":["promptfoo_plugin:harmful","promptfoo_plugin:pii"],"test_count":3}`,
			wantOK:  true,
		},
		{
			name:   "plugins array only",
			content: `{"plugins":["harmful","jailbreak"],"note":"x"}`,
			wantOK:  true,
		},
		{
			name:   "selected_capabilities with source_type",
			content: `{"selected_capabilities":[{"source_type":"promptfoo_plugin","source_ref":"promptfoo_plugin:pii"}]}`,
			wantOK:  true,
		},
		{
			name:   "no engine refs",
			content: `{"selected_capability_refs":["sample:123"],"plugins":[]}`,
			wantOK:  false,
		},
		{
			name:   "not json",
			content: `plain text plan`,
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs, ok := promptfooEngineRefsFromPlan(tc.content)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (refs=%v)", ok, tc.wantOK, refs)
			}
			if tc.wantOK && len(refs) == 0 {
				t.Fatal("ok but no refs")
			}
		})
	}
}

func TestPromptfooEngineRefsFromPlanExtractsPluginIDs(t *testing.T) {
	refs, ok := promptfooEngineRefsFromPlan(`{"selected_capability_refs":["promptfoo_plugin:harmful","promptfoo_plugin:pii"]}`)
	if !ok {
		t.Fatal("expected ok")
	}
	set := map[string]bool{}
	for _, r := range refs {
		set[r] = true
	}
	if !set["harmful"] || !set["pii"] {
		t.Fatalf("refs = %v", refs)
	}
}

func TestPromptfooEngineRefsMixedPlanStillDetected(t *testing.T) {
	// A plan mixing engine refs with expert sample refs is NOT a pure engine
	// plan — the caller (ConfirmPlan) checks resources separately; the
	// extractor itself should still report the engine refs.
	refs, ok := promptfooEngineRefsFromPlan(`{"selected_capability_refs":["promptfoo_plugin:harmful","sample:abc"]}`)
	if !ok || len(refs) != 1 || refs[0] != "harmful" {
		t.Fatalf("refs = %v ok = %v", refs, ok)
	}
}
