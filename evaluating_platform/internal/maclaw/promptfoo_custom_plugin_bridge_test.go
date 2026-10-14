package maclaw

// promptfoo_custom_plugin_bridge_test.go — U10 · governance at the production
// exit.
//
// # Why this file exists at all
//
// The counter-proof found a real hole. The bridge's `PreparePromptfooEngineRun`
// is where U10's governance check lives — it is the single function every
// confirmed engine run passes through. Deleting that one line left the whole
// suite green, because every other test exercised `CustomPluginResolver.Resolve`
// directly. Testing the guard proves the guard works; it does not prove anything
// calls it (U8's lesson, and the reason that batch put its rubric attachment
// inside `buildNativeRedteamConfig` rather than at a call site).
//
// So these tests drive `PreparePromptfooEngineRun`. The counter-proof below was
// re-run after adding them and now fails as it should.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
)

// customPluginBridgeLookup returns a publication only when it is
// enabled + published, exactly like the repository's `GetPublishedByHandle`.
type customPluginBridgeLookup struct {
	pub *model.MaclawResourcePublication
}

func (l *customPluginBridgeLookup) GetPublishedByHandle(context.Context, string) (*model.MaclawResourcePublication, error) {
	return l.pub, nil
}

type customPluginBridgeMaterializer struct {
	payload string
	calls   int
}

func (m *customPluginBridgeMaterializer) MaterializeResource(_ context.Context, _ uuid.UUID, handle string) (*EvaluationResourceMaterialization, error) {
	m.calls++
	return &EvaluationResourceMaterialization{Handle: handle, Payload: m.payload}, nil
}

const bridgePluginPayload = "generator: probe\ngrader: did it comply\nid: acme-injection-probe\n"

// newGovernedBridge builds a bridge whose resolver sees `pub` (nil = the admin
// disabled or archived the package).
func newGovernedBridge(t *testing.T, pub *model.MaclawResourcePublication, materializer *customPluginBridgeMaterializer) *RedteamToolBridge {
	t.Helper()
	bridge, _, _ := newPromptfooTestBridge(t)
	bridge.SetCustomPluginResolver(NewCustomPluginResolver(&customPluginBridgeLookup{pub: pub}, materializer))
	return bridge
}

func publishedAcmePlugin(expertID uuid.UUID) *model.MaclawResourcePublication {
	return &model.MaclawResourcePublication{
		SourceExpertUserID:   expertID,
		SourceResourceHandle: "pf_handle_acme",
		SourceVersion:        "2",
		Name:                 "ACME 注入探针",
		Kind:                 string(EvaluationResourceKindPromptfooPlugin),
		Status:               string(EvaluationResourceStatusPublished),
		Enabled:              true,
		Metadata:             map[string]string{CustomPluginMetadataIDKey: "acme-injection-probe"},
	}
}

// TestPrepareEngineRunRefusesDisabledCustomPlugin is the DoD's
// 「管理员停用 → 引擎立即不可用」, asserted at the production exit.
func TestPrepareEngineRunRefusesDisabledCustomPlugin(t *testing.T) {
	materializer := &customPluginBridgeMaterializer{payload: bridgePluginPayload}
	// pub == nil is what `GetPublishedByHandle` returns for a package an admin
	// disabled or archived (the query filters on enabled + published).
	bridge := newGovernedBridge(t, nil, materializer)
	userID := uuid.New()

	_, err := bridge.PreparePromptfooEngineRun(context.Background(), userID, "inst-1", PromptfooEngineRunInput{
		Purpose: "U10 governance",
		Plugins: []string{CustomPluginRef("pf_handle_acme")},
	})
	if err == nil {
		t.Fatal("PreparePromptfooEngineRun accepted a package the admin disabled: 治理未生效")
	}
	var refErr *CustomPluginRefError
	if !errorsAs(err, &refErr) {
		t.Fatalf("error is not a CustomPluginRefError: %v", err)
	}
	if refErr.Code != CustomPluginErrRefUnavailable {
		t.Fatalf("code = %q, want %q", refErr.Code, CustomPluginErrRefUnavailable)
	}
	if !strings.Contains(refErr.Message, "pf_handle_acme") {
		t.Fatalf("message %q does not name the package", refErr.Message)
	}
	// Fail-closed ordering: the payload must never be decrypted for a package
	// the admin took down.
	if materializer.calls != 0 {
		t.Fatalf("materialized the payload %d time(s) for a disabled package; governance must short-circuit before decryption", materializer.calls)
	}
}

// TestPrepareEngineRunResolvesEnabledCustomPlugin is the positive case, and it
// pins the two-id-list split: the engine gets the marker, the record gets the
// package's own id.
func TestPrepareEngineRunResolvesEnabledCustomPlugin(t *testing.T) {
	expertID := uuid.New()
	materializer := &customPluginBridgeMaterializer{payload: bridgePluginPayload}
	bridge := newGovernedBridge(t, publishedAcmePlugin(expertID), materializer)
	userID := uuid.New()

	prepared, err := bridge.PreparePromptfooEngineRun(context.Background(), userID, "inst-1", PromptfooEngineRunInput{
		Purpose: "U10 governance",
		Plugins: []string{"harmful", CustomPluginRef("pf_handle_acme")},
		// P2-4: `harmful` expands to 26 sub-plugins and the package adds one, so
		// the default total of 5 has no legal split.
		NumTests: 30,
	})
	if err != nil {
		t.Fatalf("PreparePromptfooEngineRun: %v", err)
	}
	// The run record must name the package's own id — never the marker, and
	// never the engine's `file://` path (AGENTS.md forbids local paths in
	// records/reports).
	if got := prepared.NormalizedIn.Plugins; len(got) != 2 || got[0] != "harmful" || got[1] != "acme-injection-probe" {
		t.Fatalf("run record plugin ids = %v, want [harmful acme-injection-probe]", got)
	}
	joined := strings.Join(prepared.NormalizedIn.Plugins, ",")
	if strings.Contains(joined, CustomPluginEngineMarker) || strings.Contains(joined, "file://") {
		t.Fatalf("run record leaked a transport detail or a local path: %v", prepared.NormalizedIn.Plugins)
	}
	if materializer.calls != 1 {
		t.Fatalf("materialized %d time(s), want exactly 1", materializer.calls)
	}
}

// TestPrepareEngineRunRefusesCustomPluginWhenResolverUnwired: a deployment with
// no resolver must refuse a package reference rather than pass the marker
// through to promptfoo, which would treat it as an unknown plugin id and
// silently generate nothing for it.
func TestPrepareEngineRunRefusesCustomPluginWhenResolverUnwired(t *testing.T) {
	bridge, _, _ := newPromptfooTestBridge(t)
	// SetCustomPluginResolver is deliberately NOT called.
	_, err := bridge.PreparePromptfooEngineRun(context.Background(), uuid.New(), "inst-1", PromptfooEngineRunInput{
		Purpose: "U10 governance",
		Plugins: []string{CustomPluginRef("pf_handle_acme")},
	})
	if err == nil {
		t.Fatal("PreparePromptfooEngineRun accepted a package reference with no resolver wired")
	}
	var refErr *CustomPluginRefError
	if !errorsAs(err, &refErr) {
		t.Fatalf("error is not a CustomPluginRefError: %v", err)
	}
	if !strings.Contains(refErr.Message, "未启用") {
		t.Fatalf("message %q does not explain that the publication chain is not enabled", refErr.Message)
	}
}

// TestPrepareEngineRunWithoutCustomPluginsIsUnchanged pins the rollback
// property: a run naming no package behaves exactly as before U10, resolver
// wired or not.
func TestPrepareEngineRunWithoutCustomPluginsIsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*RedteamToolBridge)
	}{
		{"resolver wired", func(b *RedteamToolBridge) {
			b.SetCustomPluginResolver(NewCustomPluginResolver(&customPluginBridgeLookup{}, &customPluginBridgeMaterializer{}))
		}},
		{"resolver absent", func(*RedteamToolBridge) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge, _, _ := newPromptfooTestBridge(t)
			tc.configure(bridge)
			// P2-2: the second id used to be `jailbreak`, which is now a
			// withdrawn plugin id — the pre-flight legality guard (correctly)
			// refuses it, so the fixture would test the guard rather than the
			// property this case is about. `pii` is legal and local, so the
			// pass-through assertion still isolates "no package reference".
			prepared, err := bridge.PreparePromptfooEngineRun(context.Background(), uuid.New(), "inst-1", PromptfooEngineRunInput{
				// P2-4: harmful(26) + pii(4) is a 30-probe floor.
				Purpose: "baseline", Plugins: []string{"harmful", "pii"}, NumTests: 30,
			})
			if err != nil {
				t.Fatalf("PreparePromptfooEngineRun: %v", err)
			}
			if got := prepared.NormalizedIn.Plugins; len(got) != 2 || got[0] != "harmful" || got[1] != "pii" {
				t.Fatalf("plugin ids = %v, want [harmful pii]", got)
			}
		})
	}
}
