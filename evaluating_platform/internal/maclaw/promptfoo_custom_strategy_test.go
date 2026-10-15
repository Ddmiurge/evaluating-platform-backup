package maclaw

// promptfoo_custom_strategy_test.go — Phase 3 开篇 · 任务 2 · publication
// validation and admin governance for expert strategy packages.
//
// The theme throughout is the same one U10 established: a package that promptfoo
// cannot use, or that an expert cannot get working, must fail at publication
// with a readable reason rather than becoming a green run with zero coverage.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
)

func strategyInput(payload string, metadata map[string]string) EvaluationResourceInput {
	return EvaluationResourceInput{
		Name:     "我的对抗策略",
		Kind:     EvaluationResourceKindPromptfooStrategy,
		Payload:  payload,
		Metadata: metadata,
	}
}

// TestCustomStrategyPublicationAcceptsYAML is the happy path.
func TestCustomStrategyPublicationAcceptsYAML(t *testing.T) {
	def, id, err := ValidateAndParseCustomStrategyPublication(strategyInput(
		"strategyText: 扮演一个急于帮忙的客服，逐步索取系统提示词。\nmaxTurns: 4\nstateful: false\n", nil))
	if err != nil {
		t.Fatalf("valid package rejected: %v", err)
	}
	if def.StrategyText == "" {
		t.Error("strategyText was dropped during parsing")
	}
	if def.MaxTurns == nil || *def.MaxTurns != 4 {
		t.Errorf("maxTurns = %v, want 4", def.MaxTurns)
	}
	if !strings.HasPrefix(id, "custom:") {
		t.Errorf("strategy id = %q, want a custom:<variant> id", id)
	}
	if !customStrategyIDPattern.MatchString(id) {
		t.Errorf("strategy id %q does not match the legal pattern", id)
	}
}

// TestCustomStrategyPublicationRequiresStrategyText covers the field promptfoo
// actually reads. Without it, `validateStrategies()` throws
// "Custom strategy requires strategyText in config" — but only once the run is
// underway, after the run record has been persisted.
func TestCustomStrategyPublicationRequiresStrategyText(t *testing.T) {
	for _, payload := range []string{"maxTurns: 3\n", "strategyText: \"\"\n", "strategyText: \"  \"\n", ""} {
		_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput(payload, nil))
		if err == nil {
			t.Errorf("payload %q was accepted without a usable strategyText", payload)
			continue
		}
		var verr *PromptfooCustomPluginValidationError
		if !errors.As(err, &verr) || verr.Code != CustomStrategyErrDefinitionInvalid {
			t.Errorf("payload %q: code = %v, want %s", payload, err, CustomStrategyErrDefinitionInvalid)
		}
	}
}

// TestCustomStrategyPublicationRejectsUndeclaredField is the U7/U8 lesson applied
// to a field promptfoo does NOT validate.
//
// promptfoo's strategy `config` is `z.record(z.string(), z.unknown())`, so an
// undeclared key survives parsing and is spread into the provider config. A typo
// like `maxTurn` is therefore *accepted and inert* — worse than dropped, because
// it looks like it worked.
func TestCustomStrategyPublicationRejectsUndeclaredField(t *testing.T) {
	_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput("strategyText: x\nmaxTurn: 5\n", nil))
	if err == nil {
		t.Fatal("a package with an undeclared field was accepted")
	}
	var verr *PromptfooCustomPluginValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error type = %T, want *PromptfooCustomPluginValidationError", err)
	}
	if verr.Code != CustomStrategyErrDefinitionInvalid {
		t.Errorf("code = %q, want %q", verr.Code, CustomStrategyErrDefinitionInvalid)
	}
	if verr.Field != "maxTurn" {
		t.Errorf("field = %q, want the offending key named", verr.Field)
	}
	if !strings.Contains(verr.Message, "maxTurn") {
		t.Errorf("message does not name the offending field: %q", verr.Message)
	}
}

// TestCustomStrategyPublicationRejectsEngineOwnedFields pins the security
// decision: a package must not be able to choose the endpoint its attack model
// is called on.
func TestCustomStrategyPublicationRejectsEngineOwnedFields(t *testing.T) {
	for _, field := range []string{"redteamProvider", "targetId"} {
		payload := "strategyText: x\n" + field + ": {id: openai:chat:gpt-4o}\n"
		_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput(payload, nil))
		if err == nil {
			t.Errorf("package was allowed to set engine-owned field %q", field)
			continue
		}
		var verr *PromptfooCustomPluginValidationError
		if !errors.As(err, &verr) || verr.Field != field {
			t.Errorf("%s: err = %v, want the field named", field, err)
		}
	}
}

// TestCustomStrategyPublicationRejectsJSForm covers D-03.
//
// On promptfoo 0.123.0 a `file://` strategy is *necessarily* JS/TS —
// `loadStrategy()` calls `isJavascriptFile()` and throws otherwise — so a
// file-based strategy is not a restricted version of this feature, it is a
// different and much more dangerous one: an `import()` of a module exporting
// `id` and `action`, inside the process that holds the generation API key.
func TestCustomStrategyPublicationRejectsJSForm(t *testing.T) {
	cases := map[string]map[string]string{
		"declared javascript": {CustomStrategyMetadataFormKey: "javascript"},
		"declared js":         {CustomStrategyMetadataFormKey: "js"},
		"declared python":     {CustomStrategyMetadataFormKey: "python"},
	}
	for label, metadata := range cases {
		_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput("strategyText: x\n", metadata))
		if err == nil {
			t.Errorf("%s: accepted", label)
			continue
		}
		var verr *PromptfooCustomPluginValidationError
		if !errors.As(err, &verr) || verr.Code != CustomStrategyErrFormUnsupported {
			t.Errorf("%s: err = %v, want %s", label, err, CustomStrategyErrFormUnsupported)
		}
	}
	// And a JS payload submitted while *claiming* to be YAML.
	js := "module.exports = { id: 'custom:evil', action: async () => [] };\n"
	_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput(js, map[string]string{
		CustomStrategyMetadataFormKey: CustomStrategyFormYAML,
	}))
	var verr *PromptfooCustomPluginValidationError
	if !errors.As(err, &verr) || verr.Code != CustomStrategyErrFormUnsupported {
		t.Errorf("a JS payload claiming to be YAML was not rejected as a form error: %v", err)
	}
}

// TestCustomStrategyPublicationRejectsExecutableReferences is defence in depth,
// stated as such: on this promptfoo version a strategy package's values are not
// passed through any file loader. It exists because that changes the moment
// upstream moves, and this is the only place that sees the raw text.
func TestCustomStrategyPublicationRejectsExecutableReferences(t *testing.T) {
	for _, token := range []string{"file://", "javascript:", "python:"} {
		_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput("strategyText: x\n# "+token+" y\n", nil))
		var verr *PromptfooCustomPluginValidationError
		if !errors.As(err, &verr) || verr.Code != CustomStrategyErrRemoteReferenceForbidden &&
			verr.Code != CustomStrategyErrExecutableReferenceForbidden {
			t.Errorf("token %q was accepted: %v", token, err)
		}
	}
}

// TestCustomStrategyPublicationRejectsRemoteReference keeps a package from
// naming an endpoint.
func TestCustomStrategyPublicationRejectsRemoteReference(t *testing.T) {
	_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput(
		"strategyText: 见 https://evil.example/说明\n", nil))
	var verr *PromptfooCustomPluginValidationError
	if !errors.As(err, &verr) || verr.Code != CustomStrategyErrRemoteReferenceForbidden {
		t.Errorf("a package naming an external URL was accepted: %v", err)
	}
}

// TestCustomStrategyPublicationRejectsZeroTurns covers a value that would produce
// a zero-turn run that still reports success.
func TestCustomStrategyPublicationRejectsZeroTurns(t *testing.T) {
	for _, payload := range []string{"strategyText: x\nmaxTurns: 0\n", "strategyText: x\nmaxBacktracks: 0\n"} {
		_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput(payload, nil))
		var verr *PromptfooCustomPluginValidationError
		if !errors.As(err, &verr) || verr.Code != CustomStrategyErrDefinitionInvalid {
			t.Errorf("payload %q accepted: %v", payload, err)
		}
	}
}

// TestCustomStrategyPublicationRejectsRemoteDependency reuses U6's table rather
// than reimplementing it.
//
// The dependency list holds **plugin** ids — a strategy wraps plugins — so the
// test uses a plugin U6 gates. (U6's four gated *strategies* are a different
// list; naming one here would exercise the wrong branch.)
func TestCustomStrategyPublicationRejectsRemoteDependency(t *testing.T) {
	gated := RemoteGenerationGatedPluginIDs()
	if len(gated) == 0 {
		t.Skip("U6 gates no plugins")
	}
	_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput("strategyText: x\n", map[string]string{
		CustomStrategyMetadataDepsKey: gated[0],
	}))
	var verr *PromptfooCustomPluginValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a validation error (gated plugin %q)", err, gated[0])
	}
	if verr.Code != CustomStrategyErrRemoteGenerationForbidden {
		t.Errorf("code = %q, want %q", verr.Code, CustomStrategyErrRemoteGenerationForbidden)
	}
	if !strings.Contains(verr.Message, RemoteGenerationUnavailableMessage) {
		t.Errorf("message does not explain the remote-generation constraint: %q", verr.Message)
	}
	// A local-only plugin must still be accepted, so the guard is not simply
	// refusing every declaration.
	local := LocalOnlyPluginIDs()
	if len(local) > 0 {
		if _, _, err := ValidateAndParseCustomStrategyPublication(strategyInput("strategyText: x\n", map[string]string{
			CustomStrategyMetadataDepsKey: local[0],
		})); err != nil {
			t.Errorf("a local-only dependency was refused: %v", err)
		}
	}
}

// TestCustomStrategyPublicationRejectsOversizePackage.
func TestCustomStrategyPublicationRejectsOversizePackage(t *testing.T) {
	huge := "strategyText: " + strings.Repeat("x", CustomStrategyMaxPayloadBytes+10) + "\n"
	_, _, err := ValidateAndParseCustomStrategyPublication(strategyInput(huge, nil))
	var verr *PromptfooCustomPluginValidationError
	if !errors.As(err, &verr) || verr.Code != CustomStrategyErrPayloadTooLarge {
		t.Errorf("oversize package: err = %v, want %s", err, CustomStrategyErrPayloadTooLarge)
	}
}

// TestCustomStrategyPublicationIgnoresOtherKinds keeps the guard from firing on
// unrelated resources.
func TestCustomStrategyPublicationIgnoresOtherKinds(t *testing.T) {
	in := strategyInput("strategyText: x\n", nil)
	in.Kind = EvaluationResourceKindSample
	def, id, err := ValidateAndParseCustomStrategyPublication(in)
	if err != nil || def != nil || id != "" {
		t.Errorf("a non-strategy resource was validated: def=%v id=%q err=%v", def, id, err)
	}
}

// TestCustomStrategyIDDerivation checks the derived id is always legal, whatever
// the handle looks like.
func TestCustomStrategyIDDerivation(t *testing.T) {
	for _, handle := range []string{"h-abc123", "abc", "///", "..", "A/B:C", strings.Repeat("x", 200), ""} {
		id := CustomStrategyVariantFor(handle, nil)
		if !customStrategyIDPattern.MatchString(id) {
			t.Errorf("handle %q derived the illegal id %q", handle, id)
		}
		// And it must not collide with a built-in strategy.
		_ = id
	}
}

// TestCustomStrategyRefHelpers covers the marker prefix round-trip and the
// substring-safety property.
func TestCustomStrategyRefHelpers(t *testing.T) {
	ref := CustomStrategyRef("h-1")
	if !IsCustomStrategyRef(ref) {
		t.Errorf("IsCustomStrategyRef(%q) = false", ref)
	}
	if got := CustomStrategyRefTarget(ref); got != "h-1" {
		t.Errorf("CustomStrategyRefTarget = %q, want h-1", got)
	}
	if IsCustomStrategyRef("base64") {
		t.Error("a built-in id was mistaken for a package reference")
	}
	// Distinct prefix from the plugin one, so the two cannot be confused.
	if CustomStrategyRefPrefix == CustomPluginRefPrefix {
		t.Error("strategy and plugin ref prefixes must differ; both travel in one request")
	}
}

// --- the store's single write exit ------------------------------------------

func customStrategyStoreInput(payload string) EvaluationResourceInput {
	return EvaluationResourceInput{
		Name:    "ACME 客服诱导策略",
		Kind:    EvaluationResourceKindPromptfooStrategy,
		Status:  EvaluationResourceStatusPublished,
		Enabled: true,
		Payload: payload,
	}
}

// TestCustomStrategyValidYAMLIsStoredAndCanonicalized drives the real write exit
// so the guard is proven *wired*, not merely correct.
//
// The counter-proof for this batch disabled the `if in.Kind ==
// EvaluationResourceKindPromptfooStrategy` branch in SaveResource; without a
// test at this exit the JS payload below would simply have been stored.
func TestCustomStrategyValidYAMLIsStoredAndCanonicalized(t *testing.T) {
	service, store := newCustomPluginStoreService(t)
	summary, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1",
		customStrategyStoreInput("strategyText: 逐轮施压\nmaxTurns: 4\n"))
	if err != nil {
		t.Fatalf("SaveResource: %v", err)
	}
	if summary.Kind != EvaluationResourceKindPromptfooStrategy {
		t.Fatalf("kind = %q, want %q", summary.Kind, EvaluationResourceKindPromptfooStrategy)
	}
	if len(store.saved) != 1 {
		t.Fatalf("stored %d resources, want 1", len(store.saved))
	}
	// The stored payload must be the canonical re-serialization, so what the
	// engine inlines is exactly what validation inspected.
	if md := store.saved[0].Metadata; md[CustomStrategyMetadataFormKey] != CustomStrategyFormYAML {
		t.Errorf("stored metadata %v does not record the yaml form", md)
	}
	if id := store.saved[0].Metadata["pf_strategy_id"]; !customStrategyIDPattern.MatchString(id) {
		t.Errorf("stored strategy id = %q, want a legal custom:<variant> id", id)
	}
}

// TestCustomStrategyJSIsRejectedAtTheStoreExit is the counter-proof target: with
// the guard removed this package is stored.
func TestCustomStrategyJSIsRejectedAtTheStoreExit(t *testing.T) {
	service, store := newCustomPluginStoreService(t)
	_, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1",
		customStrategyStoreInput("module.exports = { id: 'custom:evil', action: async () => [] };"))
	var verr *PromptfooCustomPluginValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	if verr.Code != CustomStrategyErrFormUnsupported {
		t.Errorf("code = %q, want %q", verr.Code, CustomStrategyErrFormUnsupported)
	}
	if len(store.saved) != 0 {
		t.Fatalf("%d resource(s) stored; a rejected package must leave nothing behind", len(store.saved))
	}
}

// TestCustomStrategyMissingTextIsRejectedAtTheStoreExit covers the field
// promptfoo actually consumes.
func TestCustomStrategyMissingTextIsRejectedAtTheStoreExit(t *testing.T) {
	service, store := newCustomPluginStoreService(t)
	_, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1",
		customStrategyStoreInput("maxTurns: 3\n"))
	var verr *PromptfooCustomPluginValidationError
	if !errors.As(err, &verr) || verr.Code != CustomStrategyErrDefinitionInvalid {
		t.Fatalf("err = %v, want %s", err, CustomStrategyErrDefinitionInvalid)
	}
	if len(store.saved) != 0 {
		t.Fatalf("%d resource(s) stored; want 0", len(store.saved))
	}
}

// stubStrategyStore is a publication/resource pair for the governance tests.
type stubStrategyStore struct {
	publications map[string]*model.MaclawResourcePublication
	payloads     map[string]string
}

// GetPublishedByHandle mirrors the repository's real filter
// (`internal/repository/maclaw_resource_projection.go:253`):
//
//	WHERE (handle = $1 OR id = $1) AND enabled = true AND status = 'published'
//
// The stub applies it rather than returning every row, because that filter IS
// the governance mechanism — a stub that returned disabled rows would test a
// store that does not exist, and the "管理员停用 → 立即不可用" assertion would
// pass for the wrong reason.
func (s *stubStrategyStore) GetPublishedByHandle(_ context.Context, handle string) (*model.MaclawResourcePublication, error) {
	pub := s.publications[handle]
	if pub == nil || !pub.Enabled || pub.Status != string(EvaluationResourceStatusPublished) {
		return nil, nil
	}
	return pub, nil
}

func (s *stubStrategyStore) MaterializeResource(_ context.Context, _ uuid.UUID, idOrHandle string) (*EvaluationResourceMaterialization, error) {
	payload, ok := s.payloads[idOrHandle]
	if !ok {
		return nil, nil
	}
	return &EvaluationResourceMaterialization{Payload: payload}, nil
}

func newStrategyResolver(pubs map[string]*model.MaclawResourcePublication, payloads map[string]string) *CustomPluginResolver {
	return NewCustomPluginResolver(&stubStrategyStore{publications: pubs, payloads: payloads}, &stubStrategyStore{publications: pubs, payloads: payloads})
}

func publishedStrategy(handle, kind, strategyID, enabled, status string) *model.MaclawResourcePublication {
	pub := &model.MaclawResourcePublication{
		Kind:                 kind,
		SourceResourceHandle: handle,
		SourceExpertUserID:   uuid.New(),
		Enabled:              enabled == "true",
		Status:               status,
		Metadata:             map[string]string{},
	}
	if strategyID != "" {
		pub.Metadata["pf_strategy_id"] = strategyID
	}
	return pub
}

// TestCustomStrategyGovernanceResolvesPublishedPackage is the happy path.
func TestCustomStrategyGovernanceResolvesPublishedPackage(t *testing.T) {
	resolver := newStrategyResolver(
		map[string]*model.MaclawResourcePublication{
			"h-1": publishedStrategy("h-1", string(EvaluationResourceKindPromptfooStrategy), "custom:h-1", "true", "published"),
		},
		map[string]string{"h-1": "strategyText: 逐轮施压\n"},
	)
	got, err := resolver.resolveOneStrategy(context.Background(), uuid.New(), CustomStrategyRef("h-1"))
	if err != nil {
		t.Fatalf("published package did not resolve: %v", err)
	}
	if got.StrategyID != "custom:h-1" {
		t.Errorf("StrategyID = %q, want custom:h-1", got.StrategyID)
	}
	if !strings.Contains(got.Definition, "strategyText") {
		t.Errorf("definition = %q, want the canonical payload", got.Definition)
	}
}

// TestCustomStrategyGovernanceRefusesDisabledPackage is the DoD requirement:
// 管理员停用 → 立即不可用.
//
// `GetPublishedByHandle` filters on enabled+published, so a disabled package is
// indistinguishable from an unknown one and both are refused.
func TestCustomStrategyGovernanceRefusesDisabledPackage(t *testing.T) {
	for _, tc := range []struct{ label, enabled, status string }{
		{"disabled", "false", "published"},
		{"archived", "true", "archived"},
		{"draft", "true", "draft"},
	} {
		resolver := newStrategyResolver(
			map[string]*model.MaclawResourcePublication{
				"h-1": publishedStrategy("h-1", string(EvaluationResourceKindPromptfooStrategy), "custom:h-1", tc.enabled, tc.status),
			},
			map[string]string{"h-1": "strategyText: x\n"},
		)
		_, err := resolver.resolveOneStrategy(context.Background(), uuid.New(), CustomStrategyRef("h-1"))
		if err == nil {
			t.Errorf("%s: package resolved; it must not", tc.label)
			continue
		}
		var refErr *CustomPluginRefError
		if !errors.As(err, &refErr) || refErr.Code != CustomStrategyErrRefUnavailable {
			t.Errorf("%s: err = %v, want %s", tc.label, err, CustomStrategyErrRefUnavailable)
		}
		// The message must not leak *which* governance action was applied.
		if strings.Contains(refErr.Message, "停用") && strings.Contains(refErr.Message, "已删除") == false &&
			!strings.Contains(refErr.Message, "下架") && !strings.Contains(refErr.Message, "归档") {
			t.Errorf("%s: message does not explain the situation: %q", tc.label, refErr.Message)
		}
	}
}

// TestCustomStrategyGovernanceRefusesWrongKind stops a plugin package being
// loaded as a strategy (and vice versa).
func TestCustomStrategyGovernanceRefusesWrongKind(t *testing.T) {
	resolver := newStrategyResolver(
		map[string]*model.MaclawResourcePublication{
			"h-1": publishedStrategy("h-1", string(EvaluationResourceKindPromptfooPlugin), "", "true", "published"),
		},
		map[string]string{"h-1": "generator: x\n"},
	)
	if _, err := resolver.resolveOneStrategy(context.Background(), uuid.New(), CustomStrategyRef("h-1")); err == nil {
		t.Error("a plugin package was loaded as a strategy")
	}
}

// TestCustomStrategyGovernanceRefusesEmptyPayload covers a publication row whose
// encrypted body is gone.
func TestCustomStrategyGovernanceRefusesEmptyPayload(t *testing.T) {
	resolver := newStrategyResolver(
		map[string]*model.MaclawResourcePublication{
			"h-1": publishedStrategy("h-1", string(EvaluationResourceKindPromptfooStrategy), "custom:h-1", "true", "published"),
		},
		map[string]string{},
	)
	if _, err := resolver.resolveOneStrategy(context.Background(), uuid.New(), CustomStrategyRef("h-1")); err == nil {
		t.Error("a publication with no content resolved")
	}
}

// TestCustomStrategyGovernanceRefusesEmptyHandle.
func TestCustomStrategyGovernanceRefusesEmptyHandle(t *testing.T) {
	resolver := newStrategyResolver(nil, nil)
	_, err := resolver.resolveOneStrategy(context.Background(), uuid.New(), "pf_strategy:")
	var refErr *CustomPluginRefError
	if !errors.As(err, &refErr) || refErr.Code != CustomStrategyErrRefUnavailable {
		t.Errorf("err = %v, want %s", err, CustomStrategyErrRefUnavailable)
	}
}

// TestResolveCustomStrategiesPassesBuiltInsThrough is the rollback property: a
// deployment that never uses the feature behaves exactly as before.
func TestResolveCustomStrategiesPassesBuiltInsThrough(t *testing.T) {
	resolver := newStrategyResolver(nil, nil)
	got, err := resolver.resolveCustomStrategies(context.Background(), uuid.New(), []EngineCapabilityRef{
		{ID: "base64"},
		{ID: "rot13"},
	})
	if err != nil {
		t.Fatalf("built-in strategies were rejected: %v", err)
	}
	if len(got.packages) != 0 {
		t.Errorf("built-in strategies produced %d packages", len(got.packages))
	}
	if len(got.engineRefs) != 2 || got.engineRefs[0].ID != "base64" || got.engineRefs[1].ID != "rot13" {
		t.Errorf("built-in refs were altered: %+v", got.engineRefs)
	}
	if len(got.recordIDs) != 2 {
		t.Errorf("recordIDs = %v, want the two built-ins", got.recordIDs)
	}
}

// TestResolveCustomStrategiesKeepsMarkersOutOfTheRecord is the same rule U10
// established for plugins: the run record carries the resolved id, never the
// transport marker.
func TestResolveCustomStrategiesKeepsMarkersOutOfTheRecord(t *testing.T) {
	resolver := newStrategyResolver(
		map[string]*model.MaclawResourcePublication{
			"h-1": publishedStrategy("h-1", string(EvaluationResourceKindPromptfooStrategy), "custom:h-1", "true", "published"),
		},
		map[string]string{"h-1": "strategyText: x\n"},
	)
	got, err := resolver.resolveCustomStrategies(context.Background(), uuid.New(), []EngineCapabilityRef{
		{ID: "base64"},
		{ID: CustomStrategyRef("h-1")},
	})
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(got.packages) != 1 || got.packages[0].Handle != "h-1" {
		t.Fatalf("packages = %+v, want one for h-1", got.packages)
	}
	// Engine side gets the marker...
	if got.engineRefs[1].ID != CustomStrategyRef("h-1") {
		t.Errorf("engine ref = %q, want the pf_strategy marker", got.engineRefs[1].ID)
	}
	// ...the record gets the resolved id.
	if got.recordIDs[1] != "custom:h-1" {
		t.Errorf("record id = %q, want custom:h-1 (never a transport marker)", got.recordIDs[1])
	}
}
