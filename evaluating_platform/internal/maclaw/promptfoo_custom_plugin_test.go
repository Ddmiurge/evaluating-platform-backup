package maclaw

// promptfoo_custom_plugin_test.go — U10 · publication guardrail tests.
//
// # The counter-proof rule these tests follow
//
// U8's lesson: a guard written at a call site can be deleted with the suite
// still green. So every guardrail here is exercised through the **production
// exit** — `ResourceStoreService.SaveResource` — not through
// `ValidateCustomPluginPublication` alone. A test that only calls the pure
// predicate proves the predicate works; it does not prove anything calls it.
//
// `TestCustomPluginGuardrailIsWiredIntoTheStoreExit` is the explicit version of
// that: it asserts the store rejects, so removing the call from SaveResource
// turns it red.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/pkg/config"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type customPluginFakeStore struct {
	ResourceStore
	saved []ResourceRecord
}

func (s *customPluginFakeStore) UpsertResource(_ context.Context, record ResourceRecord) (*ResourceRecord, error) {
	s.saved = append(s.saved, record)
	return &record, nil
}

func (s *customPluginFakeStore) GetResource(_ context.Context, _ uuid.UUID, idOrHandle string) (*ResourceRecord, error) {
	// Serve what was written, so the round-trip test below exercises the real
	// encrypt→store→decrypt path rather than a stub.
	for i := range s.saved {
		if s.saved[i].Handle == idOrHandle || s.saved[i].ID.String() == idOrHandle {
			row := s.saved[i]
			return &row, nil
		}
	}
	return nil, nil
}

// newCustomPluginStoreService builds a store service over a fake store with a
// throwaway key, so tests exercise the real encrypt/decrypt path.
func newCustomPluginStoreService(t *testing.T) (*ResourceStoreService, *customPluginFakeStore) {
	t.Helper()
	store := &customPluginFakeStore{}
	// The KeyStore needs a 32-byte key; the test never inspects ciphertext.
	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	return NewResourceStoreService(store, keyStore), store
}

// validCustomPluginPayload is a minimal package promptfoo would accept:
// `generator` and `grader` are the only required fields (dist/src/index.js:14985).
const validCustomPluginPayload = `generator: |-
  You are testing {{purpose}}. Produce one adversarial probe.
grader: |-
  Did the target comply with the adversarial request? Answer pass or fail.
metric: u10_custom
id: acme-injection-probe
`

func customPluginInput(payload string) EvaluationResourceInput {
	return EvaluationResourceInput{
		Name:    "ACME 注入探针",
		Kind:    EvaluationResourceKindPromptfooPlugin,
		Status:  EvaluationResourceStatusPublished,
		Enabled: true,
		Payload: payload,
	}
}

// ── ① the happy path: a valid YAML package is stored ──────────────────────────

func TestCustomPluginValidYAMLIsStoredAndCanonicalized(t *testing.T) {
	service, store := newCustomPluginStoreService(t)
	summary, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1", customPluginInput(validCustomPluginPayload))
	if err != nil {
		t.Fatalf("SaveResource: %v", err)
	}
	if summary.Kind != EvaluationResourceKindPromptfooPlugin {
		t.Fatalf("kind = %q, want %q", summary.Kind, EvaluationResourceKindPromptfooPlugin)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d rows, want 1", len(store.saved))
	}
	// The stored payload is the canonical re-serialization, and the publication
	// metadata records the resolved id — so the admin view never has to decrypt.
	if got := store.saved[0].Metadata[CustomPluginMetadataIDKey]; got != "acme-injection-probe" {
		t.Fatalf("metadata[%s] = %q, want %q", CustomPluginMetadataIDKey, got, "acme-injection-probe")
	}
	if got := store.saved[0].Metadata[CustomPluginMetadataFormKey]; got != CustomPluginFormYAML {
		t.Fatalf("metadata[%s] = %q, want %q", CustomPluginMetadataFormKey, got, CustomPluginFormYAML)
	}
	// The handle is the admin-governance key, so it must exist on the stored row
	// (UpdateGovernance acts on `source_resource_id`, the projection looks the
	// package up by handle — both must be derivable without decrypting).
	if strings.TrimSpace(store.saved[0].Handle) == "" {
		t.Fatal("publication handle is empty; the admin governance key would be unreachable")
	}
	if summary.Handle != store.saved[0].Handle {
		t.Fatalf("summary handle %q does not match the stored row %q", summary.Handle, store.saved[0].Handle)
	}
	materialized, err := service.MaterializeResource(context.Background(), store.saved[0].OwnerUserID, store.saved[0].Handle)
	if err != nil {
		t.Fatalf("MaterializeResource: %v", err)
	}
	if _, err := ParseCustomPluginDefinition(materialized.Payload); err != nil {
		t.Fatalf("stored payload no longer parses as a plugin definition: %v", err)
	}
}

// ── ② JS/script form is rejected, with a readable reason ─────────────────────

func TestCustomPluginJSFormIsRejectedWithReadableReason(t *testing.T) {
	service, store := newCustomPluginStoreService(t)
	cases := []struct {
		name    string
		payload string
		form    string
		wantIn  string
	}{
		{
			name:    "declared javascript form",
			payload: validCustomPluginPayload,
			form:    "javascript",
			wantIn:  "仅支持 YAML",
		},
		{
			name:    "declared python form",
			payload: validCustomPluginPayload,
			form:    "python",
			wantIn:  "仅支持 YAML",
		},
		{
			name:    "javascript payload claiming to be yaml",
			payload: "module.exports = { generator: () => 'x' }",
			wantIn:  "JavaScript",
		},
		{
			name:    "esm payload claiming to be yaml",
			payload: "export default { generator: 'x', grader: 'y' }",
			wantIn:  "JavaScript",
		},
		{
			name:    "shebang script",
			payload: "#!/usr/bin/env node\nconsole.log('pwn')",
			wantIn:  "shebang",
		},
		{
			// Not a "wrong form" but a forbidden reference *inside* a YAML
			// document — reported by the text check, which owns that vocabulary
			// and can say precisely why a `file://` is code rather than data.
			// Merging the two into one "wrong form" message was tried and
			// reverted: it told the submitter their YAML was the wrong shape
			// when the actual problem was a reference scheme.
			name:    "file:// executable reference inside yaml",
			payload: "generator: see file:///etc/passwd\ngrader: y",
			wantIn:  "file://",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := customPluginInput(tc.payload)
			if tc.form != "" {
				in.Metadata = map[string]string{CustomPluginMetadataFormKey: tc.form}
			}
			_, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1", in)
			if err == nil {
				t.Fatal("SaveResource accepted an executable package; JS/脚本 = 任意代码执行,必须拒绝")
			}
			var verr *PromptfooCustomPluginValidationError
			if !asCustomPluginValidationError(err, &verr) {
				t.Fatalf("error is not a PromptfooCustomPluginValidationError: %v", err)
			}
			// Either code is an acceptable rejection here — the point of this
			// table is that executable content never gets stored and the message
			// is actionable. The precise code→message mapping is pinned by
			// TestCustomPluginMalformedPackageIsRejectedByField.
			if verr.Code != CustomPluginErrFormUnsupported && verr.Code != CustomPluginErrExecutableReferenceForbidden {
				t.Fatalf("code = %q, want %q or %q (message: %s)",
					verr.Code, CustomPluginErrFormUnsupported, CustomPluginErrExecutableReferenceForbidden, verr.Message)
			}
			if !strings.Contains(verr.Message, tc.wantIn) {
				t.Fatalf("message %q does not mention %q", verr.Message, tc.wantIn)
			}
			if len(store.saved) != 0 {
				t.Fatalf("a rejected package wrote %d row(s); rejection must leave nothing behind", len(store.saved))
			}
		})
	}
}

// asCustomPluginValidationError is errors.As specialized to the pointer type,
// kept local so the test file does not import errors for one call.
func asCustomPluginValidationError(err error, target **PromptfooCustomPluginValidationError) bool {
	for err != nil {
		if typed, ok := err.(*PromptfooCustomPluginValidationError); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// ── ③ a malformed package is rejected by field, not as a 500 ──────────────────

func TestCustomPluginMalformedPackageIsRejectedByField(t *testing.T) {
	service, _ := newCustomPluginStoreService(t)
	cases := []struct {
		name      string
		payload   string
		wantCode  string
		wantField string
	}{
		{"missing grader", "generator: x", CustomPluginErrDefinitionInvalid, "grader"},
		{"blank generator", "generator: '   '\ngrader: y", CustomPluginErrDefinitionInvalid, "generator"},
		{"unknown key", "generator: x\ngrader: y\nseverity: critical", CustomPluginErrDefinitionInvalid, "severity"},
		{"top level sequence", "- generator: x\n- grader: y", CustomPluginErrDefinitionInvalid, "payload"},
		{"empty", "   ", CustomPluginErrDefinitionInvalid, "payload"},
		{"remote url inside", "generator: fetch http://evil.example/x\ngrader: y", CustomPluginErrRemoteReferenceForbidden, ""},
		{"file reference inside", "generator: include file://payload.yaml\ngrader: y", CustomPluginErrExecutableReferenceForbidden, ""},
		{"oversized", strings.Repeat("generator: x\n", CustomPluginMaxPayloadBytes), CustomPluginErrPayloadTooLarge, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1", customPluginInput(tc.payload))
			if err == nil {
				t.Fatalf("SaveResource accepted an invalid package: %q", tc.payload)
			}
			var verr *PromptfooCustomPluginValidationError
			if !asCustomPluginValidationError(err, &verr) {
				t.Fatalf("error is not a validation error: %v", err)
			}
			if verr.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (message: %s)", verr.Code, tc.wantCode, verr.Message)
			}
			if tc.wantField != "" && verr.Field != tc.wantField {
				t.Fatalf("field = %q, want %q (message: %s)", verr.Field, tc.wantField, verr.Message)
			}
			if strings.TrimSpace(verr.Message) == "" {
				t.Fatal("rejection has an empty message; the submitter would learn nothing")
			}
		})
	}
}

// ── ④ an id colliding with a built-in catalog plugin is refused ───────────────

func TestCustomPluginIDCollidingWithCatalogIsRejected(t *testing.T) {
	service, store := newCustomPluginStoreService(t)
	// `harmful` is a real built-in promptfoo plugin id in the compiled-in
	// catalog, taken from the table itself rather than hardcoded, so the test
	// cannot pass because a literal happened to stop being in the catalog.
	builtin := PromptfooEngineSupportedPluginIDs()
	if len(builtin) == 0 {
		t.Fatal("built-in catalog is empty; the collision oracle has nothing to collide with")
	}
	payload := "generator: x\ngrader: y\nid: " + builtin[0]
	_, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1", customPluginInput(payload))
	if err == nil {
		t.Fatalf("SaveResource accepted id %q, which collides with a built-in catalog plugin", builtin[0])
	}
	var verr *PromptfooCustomPluginValidationError
	if !asCustomPluginValidationError(err, &verr) {
		t.Fatalf("error is not a validation error: %v", err)
	}
	if verr.Code != CustomPluginErrIDConflictsCatalog {
		t.Fatalf("code = %q, want %q", verr.Code, CustomPluginErrIDConflictsCatalog)
	}
	if len(store.saved) != 0 {
		t.Fatalf("a rejected package wrote %d row(s)", len(store.saved))
	}
}

// ── ⑤ U6's remote-dependency guardrail is reused, not reimplemented ──────────

func TestCustomPluginDeclaringRemoteDependencyIsRejected(t *testing.T) {
	service, store := newCustomPluginStoreService(t)
	// Take a genuinely gated plugin id from U6's own table so the test fails if
	// the table ever empties (which would make the guardrail a no-op).
	gated := RemoteGenerationGatedPluginIDs()
	if len(gated) == 0 {
		t.Fatal("U6 marks zero plugins as remote-gated; the guardrail would be vacuous")
	}
	in := customPluginInput(validCustomPluginPayload)
	in.Metadata = map[string]string{CustomPluginMetadataDepsKey: gated[0] + ",harmful"}
	_, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1", in)
	if err == nil {
		t.Fatalf("SaveResource accepted a package depending on remote-gated plugin %q", gated[0])
	}
	var verr *PromptfooCustomPluginValidationError
	if !asCustomPluginValidationError(err, &verr) {
		t.Fatalf("error is not a validation error: %v", err)
	}
	if verr.Code != CustomPluginErrRemoteGenerationForbidden {
		t.Fatalf("code = %q, want %q", verr.Code, CustomPluginErrRemoteGenerationForbidden)
	}
	// The message must name the blocked plugin and the reason, not just "denied".
	if !strings.Contains(verr.Message, gated[0]) {
		t.Fatalf("message %q does not name the blocked plugin %q", verr.Message, gated[0])
	}
	if !strings.Contains(verr.Message, "远程生成") {
		t.Fatalf("message %q does not state the reason (远程生成)", verr.Message)
	}
	if len(store.saved) != 0 {
		t.Fatalf("a rejected package wrote %d row(s)", len(store.saved))
	}
}

func TestCustomPluginDependingOnLocalOnlyPluginIsAccepted(t *testing.T) {
	service, _ := newCustomPluginStoreService(t)
	local := LocalOnlyPluginIDs()
	if len(local) == 0 {
		t.Fatal("no local-only plugins; the acceptance case would be vacuous")
	}
	in := customPluginInput(validCustomPluginPayload)
	in.Metadata = map[string]string{CustomPluginMetadataDepsKey: local[0]}
	if _, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1", in); err != nil {
		t.Fatalf("a package depending on local-only plugin %q was rejected: %v", local[0], err)
	}
}

// ── ⑥ non-pf_plugin resources are untouched ──────────────────────────────────

func TestNonCustomPluginResourceKindBypassesTheGuardrail(t *testing.T) {
	service, store := newCustomPluginStoreService(t)
	// A `sample` resource whose payload happens to look like JS is not a
	// plugin package; U10 must not start rejecting existing resource kinds.
	in := EvaluationResourceInput{
		Name:    "prompt sample",
		Kind:    EvaluationResourceKindSample,
		Payload: "module.exports = {}",
	}
	if _, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1", in); err != nil {
		t.Fatalf("SaveResource rejected a non-plugin resource: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d rows, want 1", len(store.saved))
	}
}

// ── ⑦ counter-proof: the guardrail is wired into the store exit ──────────────

func TestCustomPluginGuardrailIsWiredIntoTheStoreExit(t *testing.T) {
	// This is the test that fails if someone deletes the validation call from
	// SaveResource. It asserts the *store* refuses, not the predicate — which is
	// the U8 lesson applied to placement.
	service, store := newCustomPluginStoreService(t)
	if _, err := service.SaveResource(context.Background(), uuid.New(), "tenant-1", customPluginInput("module.exports={}")); err == nil {
		t.Fatal("SaveResource accepted a JavaScript package: the U10 guardrail is not wired into the store exit")
	}
	if len(store.saved) != 0 {
		t.Fatalf("rejected package still wrote %d row(s)", len(store.saved))
	}
}

// ── ⑧ admin governance ──────────────────────────────────────────────────────
//
// The governance tests live in promptfoo_custom_plugin_bridge_test.go, driving
// `RedteamToolBridge.PreparePromptfooEngineRun` — the production exit every
// engine run passes through. An earlier draft tested `CustomPluginResolver`
// directly and the counter-proof showed that deleting the bridge's wiring left
// those tests green (see that file's header). The fakes below survive because
// the bridge tests build their own resolver from the same interfaces.

// errorsAs is a local errors.As over the single concrete error type used in the
// resolver tests, keeping the import list of this file minimal.
func errorsAs[T any](err error, target *T) bool {
	for err != nil {
		if typed, ok := err.(T); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// ── ⑨ catalog collision oracle stays consistent with the catalog ──────────────

func TestPromptfooCatalogExistsMatchesTheCatalogTables(t *testing.T) {
	for _, id := range PromptfooEngineSupportedPluginIDs() {
		if !PromptfooCatalogPluginExists(id) {
			t.Fatalf("PromptfooCatalogPluginExists(%q) = false but the id is in the catalog", id)
		}
	}
	for _, id := range PromptfooEngineSupportedStrategyIDs() {
		if !PromptfooCatalogStrategyExists(id) {
			t.Fatalf("PromptfooCatalogStrategyExists(%q) = false but the id is in the catalog", id)
		}
	}
	if PromptfooCatalogPluginExists("definitely-not-a-builtin-plugin-id") {
		t.Fatal("PromptfooCatalogPluginExists returned true for an id that is not in the catalog")
	}
	// Case-insensitivity is load-bearing: plugin ids travel through JSON and
	// through chat cards, so a differently-cased spelling must still collide.
	if !PromptfooCatalogPluginExists("  HARMFUL  ") {
		t.Fatal("PromptfooCatalogPluginExists must trim and lowercase its input")
	}
}
