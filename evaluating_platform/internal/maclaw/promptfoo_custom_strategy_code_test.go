package maclaw

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
)

// CODE strategy packages (`pf_strategy_form=js`).
//
// Design: docs/architecture/strategy-code-packages.md. The engine side is tested
// in `promptfoo_engine/tests/customStrategyCode.test.ts`; this file pins the BFF
// half — what gets stored, and what gets refused before it is stored.

func codeStrategyInput(source string, metadata map[string]string) EvaluationResourceInput {
	meta := map[string]string{
		CustomStrategyMetadataFormKey:    CustomStrategyFormJS,
		CustomStrategyMetadataVariantKey: "code-probe",
	}
	for k, v := range metadata {
		meta[k] = v
	}
	return EvaluationResourceInput{
		Name:     "代码策略",
		Kind:     EvaluationResourceKindPromptfooStrategy,
		Status:   EvaluationResourceStatusPublished,
		Enabled:  true,
		Payload:  source,
		Metadata: meta,
	}
}

const validCodeStrategy = `export default {
  id: 'custom:code-probe',
  action(testCases, injectVar) {
    return testCases.map((tc) => ({ ...tc, vars: { ...tc.vars, [injectVar]: 'x ' + String(tc.vars[injectVar]) } }));
  },
};
`

func TestValidateCustomStrategyCodeAcceptsAPureModule(t *testing.T) {
	source, strategyID, err := ValidateAndParseCustomStrategyCodePublication(codeStrategyInput(validCodeStrategy, nil))
	if err != nil {
		t.Fatalf("valid code package refused: %v", err)
	}
	if source != validCodeStrategy {
		t.Error("validated source differs from the submitted module")
	}
	if !strings.HasPrefix(strategyID, "custom:") {
		t.Errorf("strategy id = %q, want a custom:<variant>", strategyID)
	}
	// The id the report shows is namespaced away from promptfoo's own `custom`
	// slot (which would demand strategyText).
	if public := CustomStrategyCodePublicID(strategyID); !strings.HasPrefix(public, "custom:code:") {
		t.Errorf("public id = %q, want custom:code:<variant>", public)
	}
}

// The capability tokens are a threshold, not a boundary — but they must actually
// fire, and each refusal must name the token so the submitter can act.
func TestValidateCustomStrategyCodeRefusesCapabilityTokens(t *testing.T) {
	cases := map[string]string{
		"child_process": `import cp from 'child_process'; export default { id: 'x', action() {} };`,
		"process.env":   `export default { id: 'x', action() { return void process.env.SECRET; } };`,
		"require(":      `const fs = require('fs'); export default { id: 'x', action() {} };`,
		"fetch(":        `export default { id: 'x', action() { return fetch('https://x'); } };`,
		"new Function":  `export default { id: 'x', action() { return new Function('1'); } };`,
		"eval(":         `export default { id: 'x', action() { return eval('1'); } };`,
		"globalThis":    `export default { id: 'x', action() { return globalThis; } };`,
		"file://":       `export default { id: 'x', action() { return 'file:///etc/passwd'; } };`,
	}
	for label, source := range cases {
		_, _, err := ValidateAndParseCustomStrategyCodePublication(codeStrategyInput(source, nil))
		if err == nil {
			t.Errorf("%s: accepted", label)
			continue
		}
		var verr *PromptfooCustomPluginValidationError
		if !errors.As(err, &verr) || verr.Code != CustomStrategyErrCodeUnsafe {
			t.Errorf("%s: err = %v, want %s", label, err, CustomStrategyErrCodeUnsafe)
		}
		if !strings.Contains(verr.Error(), label) {
			t.Errorf("%s: message does not name the token: %s", label, verr.Error())
		}
	}
}

func TestValidateCustomStrategyCodeRefusesBadShape(t *testing.T) {
	cases := map[string]struct {
		source string
		code   string
	}{
		"empty":             {"   ", CustomStrategyErrCodeInvalid},
		"no action":         {`export default { id: 'x' };`, CustomStrategyErrCodeInvalid},
		"not an ESM export": {`module.exports = { id: 'x', action() {} };`, CustomStrategyErrCodeInvalid},
		"too large": {
			"export default { id: 'x', action() { return '" + strings.Repeat("a", CustomStrategyCodeMaxBytes) + "'; } };",
			CustomStrategyErrCodeInvalid,
		},
	}
	for label, tc := range cases {
		_, _, err := ValidateAndParseCustomStrategyCodePublication(codeStrategyInput(tc.source, nil))
		if err == nil {
			t.Errorf("%s: accepted", label)
			continue
		}
		var verr *PromptfooCustomPluginValidationError
		if !errors.As(err, &verr) || verr.Code != tc.code {
			t.Errorf("%s: err = %v, want code %s", label, err, tc.code)
		}
	}
}

// A YAML package and a code package must not be able to take each other's path:
// the store dispatches on the declared form, and each guard refuses the other's
// payload instead of parsing it into nonsense.
func TestCustomStrategyFormsStayOnTheirOwnPath(t *testing.T) {
	// Text package declaring js → the YAML parser refuses (routing backstop).
	if _, _, err := ValidateAndParseCustomStrategyPublication(codeStrategyInput(validCodeStrategy, nil)); err == nil {
		t.Error("a code package was accepted by the YAML parser")
	}
	// Code package declaring yaml shape → the code guard is not invoked, but the
	// YAML parser must refuse the JS payload rather than store it.
	_, _, err := ValidateAndParseCustomStrategyPublication(EvaluationResourceInput{
		Name:     "伪装",
		Kind:     EvaluationResourceKindPromptfooStrategy,
		Payload:  validCodeStrategy,
		Metadata: map[string]string{CustomStrategyMetadataFormKey: CustomStrategyFormYAML},
	})
	if err == nil {
		t.Error("a JS payload claiming to be YAML was accepted")
	}
}

// Storing a code package records the form, so the resolver can dispatch it to the
// file materializer instead of the custom:<variant> inliner.
func TestSaveResourceStoresTheCodeForm(t *testing.T) {
	store := newFakeResourceStore()
	service := NewResourceStoreService(store, agentTestKeyStore(t))
	saved, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1", codeStrategyInput(validCodeStrategy, nil))
	if err != nil {
		t.Fatalf("SaveResource: %v", err)
	}
	if saved.Metadata[CustomStrategyMetadataFormKey] != CustomStrategyFormJS {
		t.Fatalf("stored form = %q, want js", saved.Metadata[CustomStrategyMetadataFormKey])
	}
	if strings.TrimSpace(saved.Metadata["pf_strategy_id"]) == "" {
		t.Error("stored package has no strategy id — the resolver would have to derive one")
	}
}

// The resolver must split the two kinds: a code package goes to the file
// materializer's channel, a text package to the custom:<variant> inliner. If both
// landed in `packages`, the engine would try to inline an ESM module as
// strategyText — and promptfoo would reject it mid-run.
func TestResolveEngineCapabilitiesSplitsCodeFromTextStrategies(t *testing.T) {
	keyStore := agentTestKeyStore(t)
	service := NewResourceStoreService(newFakeResourceStore(), keyStore)
	expert := uuid.New()
	// Serve both packages from one publication lookup keyed by handle.
	lookup := &codeStrategyLookup{}

	// Deterministic order: `text` is inserted first, so when both packages
	// resolve to the same derived `custom:strategy` id the TEXT one claims it.
	// That is what Step 0 exposed in the engine (the id promptfoo records is the
	// ref, not the module's own id), and the platform does not yet de-collide
	// them — see the note below.
	order := []string{"expert_text_1", "expert_code_1"}

	for _, handle := range order {
		input := map[string]EvaluationResourceInput{
			"expert_code_1": codeStrategyInput(validCodeStrategy, map[string]string{
				"handle":                         "expert_code_1",
				CustomStrategyMetadataVariantKey: "expert_code_1_probe",
			}),
			"expert_text_1": {
				Name:    "文本策略",
				Kind:    EvaluationResourceKindPromptfooStrategy,
				Status:  EvaluationResourceStatusPublished,
				Enabled: true,
				Payload: "strategyText: |-\n  分多轮逐步试探。\n",
				Metadata: map[string]string{
					"handle":                      "expert_text_1",
					CustomStrategyMetadataFormKey: CustomStrategyFormYAML,
				},
			},
		}[handle]
		saved, err := service.SaveResource(context.Background(), expert, "tenant-1", input)
		if err != nil {
			t.Fatalf("save %s: %v", handle, err)
		}
		lookup.items = append(lookup.items, &model.MaclawResourcePublication{
			SourceExpertUserID:   expert,
			SourceResourceHandle: handle,
			SourceVersion:        "1",
			Name:                 saved.Name,
			Kind:                 string(EvaluationResourceKindPromptfooStrategy),
			Status:               string(EvaluationResourceStatusPublished),
			Enabled:              true,
			Metadata:             saved.Metadata,
		})
	}

	bridge := NewRedteamToolBridge(nil, nil, nil)
	bridge.SetCustomPluginResolver(NewCustomPluginResolver(lookup, service))

	resolved, err := bridge.ResolveEngineCapabilities(context.Background(), uuid.New(), nil, []EngineCapabilityRef{
		{ID: "pf_strategy:expert_code_1"},
		{ID: "pf_strategy:expert_text_1"},
	})
	if err != nil {
		t.Fatalf("ResolveEngineCapabilities: %v", err)
	}
	if len(resolved.CustomStrategyCode) != 1 || resolved.CustomStrategyCode[0].Handle != "expert_code_1" {
		t.Errorf("code packages = %+v, want exactly the js one", resolved.CustomStrategyCode)
	}
	if len(resolved.CustomStrategies) != 1 || resolved.CustomStrategies[0].Handle != "expert_text_1" {
		t.Errorf("text packages = %+v, want exactly the yaml one", resolved.CustomStrategies)
	}
	if resolved.CustomStrategyCode[0].Form != CustomStrategyFormJS {
		t.Errorf("code package form = %q, want js", resolved.CustomStrategyCode[0].Form)
	}
	// Both are recorded by their resolved strategy id — never a transport marker.
	if len(resolved.RecordStrategies) != 2 {
		t.Errorf("record strategies = %+v, want both resolved ids", resolved.RecordStrategies)
	}
	for _, ref := range resolved.RecordStrategies {
		if strings.Contains(ref.ID, "pf_strategy:") || strings.Contains(ref.ID, "file://") {
			t.Errorf("record strategy id carries a transport marker or path: %q", ref.ID)
		}
	}
}

// codeStrategyLookup serves a fixed publication list by handle; GetPublishedByHandle
// returns nil for an unknown or unavailable package, mirroring the repository.
type codeStrategyLookup struct {
	items []*model.MaclawResourcePublication
}

func (l *codeStrategyLookup) GetPublishedByHandle(_ context.Context, handle string) (*model.MaclawResourcePublication, error) {
	for _, item := range l.items {
		if item != nil && item.SourceResourceHandle == handle {
			return item, nil
		}
	}
	return nil, nil
}

// A code package without an explicit variant is refused at publication: the
// platform cannot distinguish two of them in a report otherwise (promptfoo
// records the path, not the module's id — Step 0).
func TestValidateCustomStrategyCodeRequiresAnExplicitVariant(t *testing.T) {
	input := codeStrategyInput(validCodeStrategy, nil)
	delete(input.Metadata, CustomStrategyMetadataVariantKey)
	_, _, err := ValidateAndParseCustomStrategyCodePublication(input)
	if err == nil {
		t.Fatal("a code package without a variant was accepted")
	}
	var verr *PromptfooCustomPluginValidationError
	if !errors.As(err, &verr) || verr.Code != CustomStrategyErrCodeInvalid {
		t.Fatalf("err = %v, want %s", err, CustomStrategyErrCodeInvalid)
	}
	if !strings.Contains(verr.Error(), "pf_strategy_variant") {
		t.Errorf("message does not name the missing key: %s", verr.Error())
	}
}
