package handler

// maclaw_custom_plugin_test.go — U10 · the expert-facing publish surface.
//
// # What is being tested, and what is not
//
// These tests drive the **real** `ResourceStoreService` behind the HTTP route,
// so what they assert is what a browser would receive. That matters for the
// DoD's "JS 形态被拒绝并有清晰提示（**不是 500 错误**）": a guard that works but
// is classified as `503 maclaw runtime is not configured` has not satisfied it,
// so the status code and the message body are both part of the assertion.
//
// The unit-level guardrail tests live in
// `internal/maclaw/promptfoo_custom_plugin_test.go`. These are deliberately a
// thin second layer over the same production exit — a second, parallel test
// suite that re-implements the rules would be a third source of truth, which is
// the drift risk U6's shared-mapping-table design exists to prevent.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/maclaw"
	"evaluating_platform/pkg/config"
)

// customPluginTestGateway rejects anything the platform store would have
// rejected, by actually running it — so the test exercises the production path
// rather than a stub that could drift from it.
//
// It embeds `fakeMaclawGateway` (the package's existing full-interface fake) and
// overrides only the two resource methods. Embedding rather than re-declaring
// every method means a new method on `maclaw.EvaluationGateway` is a compile
// error here, instead of a runtime surprise.
type customPluginTestGateway struct {
	*fakeMaclawGateway
	service *maclaw.ResourceStoreService
}

func (g *customPluginTestGateway) Enabled() bool { return g.service != nil && g.service.Enabled() }

func (g *customPluginTestGateway) SaveEvaluationResource(ctx context.Context, in maclaw.EvaluationResourceInput) (*maclaw.EvaluationResourceSummary, error) {
	return g.service.SaveResource(ctx, uuid.New(), "tenant-expert", in)
}

func (g *customPluginTestGateway) SearchEvaluationResources(context.Context, maclaw.EvaluationResourceQuery) ([]maclaw.EvaluationResourceSummary, error) {
	return nil, nil
}

func newCustomPluginTestGateway(t *testing.T) *customPluginTestGateway {
	t.Helper()
	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	return &customPluginTestGateway{
		fakeMaclawGateway: &fakeMaclawGateway{enabled: true},
		service:           maclaw.NewResourceStoreService(&customPluginHandlerFakeStore{}, keyStore),
	}
}

// customPluginHandlerFakeStore is a minimal in-memory ResourceStore.
type customPluginHandlerFakeStore struct {
	maclaw.ResourceStore
	rows map[string]maclaw.ResourceRecord
}

func (s *customPluginHandlerFakeStore) UpsertResource(_ context.Context, record maclaw.ResourceRecord) (*maclaw.ResourceRecord, error) {
	if s.rows == nil {
		s.rows = map[string]maclaw.ResourceRecord{}
	}
	s.rows[record.ID.String()] = record
	return &record, nil
}

func (s *customPluginHandlerFakeStore) GetResource(_ context.Context, _ uuid.UUID, idOrHandle string) (*maclaw.ResourceRecord, error) {
	for _, row := range s.rows {
		if row.Handle == idOrHandle || row.ID.String() == idOrHandle {
			found := row
			return &found, nil
		}
	}
	return nil, nil
}

// postCustomPlugin drives the real expert publish route.
func postCustomPlugin(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := NewMaclawEvaluationHandler(newCustomPluginTestGateway(t))
	router := newEnterpriseRoleRouter()
	router.POST("/maclaw/evaluation/resources", handler.CreateResource)
	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/resources", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// TestCustomPluginJSFormIsRejectedAsReadableBadRequest is the DoD's headline
// requirement: JS/script forms must be refused with a message an expert can act
// on, surfaced as 400 — not 500, not 503, and not a silent success.
func TestCustomPluginJSFormIsRejectedAsReadableBadRequest(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "declared javascript form",
			body: `{"name":"x","kind":"pf_plugin","enabled":true,` +
				`"payload":"generator: x\ngrader: y\n","metadata":{"pf_plugin_form":"javascript"}}`,
		},
		{
			name: "javascript payload claiming yaml",
			body: `{"name":"x","kind":"pf_plugin","enabled":true,"payload":"module.exports = {}"}`,
		},
		{
			name: "esm payload claiming yaml",
			body: `{"name":"x","kind":"pf_plugin","enabled":true,"payload":"export default {}"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postCustomPlugin(t, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
			}
			got := w.Body.String()
			if strings.Contains(got, "\"code\":\"maclaw_") {
				t.Fatalf("rejection was classified as an upstream failure rather than a validation error: %s", got)
			}
			if !strings.Contains(got, "pf_plugin_") {
				t.Fatalf("response carries no machine-readable pf_plugin code: %s", got)
			}
			if !strings.Contains(got, "YAML") {
				t.Fatalf("response does not tell the expert that only YAML is accepted: %s", got)
			}
		})
	}
}

// TestCustomPluginRemoteDependencyIsRejectedAsConflict covers the U6 guardrail
// surfacing through the publish route.
func TestCustomPluginRemoteDependencyIsRejectedAsConflict(t *testing.T) {
	gated := maclaw.RemoteGenerationGatedPluginIDs()
	if len(gated) == 0 {
		t.Fatal("U6 marks zero plugins as remote-gated; the guardrail would be vacuous")
	}
	body := `{"name":"x","kind":"pf_plugin","enabled":true,` +
		`"payload":"generator: x\ngrader: y\n","metadata":{"pf_plugin_depends_on":"` + gated[0] + `"}}`
	w := postCustomPlugin(t, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, maclaw.CustomPluginErrRemoteGenerationForbidden) {
		t.Fatalf("response does not carry the remote-generation code: %s", got)
	}
	// The message must name both the blocked capability and the reason, so an
	// expert can tell "this is blocked by policy" from "this is broken".
	if !strings.Contains(got, gated[0]) || !strings.Contains(got, "远程生成") {
		t.Fatalf("message is not actionable: %s", got)
	}
}

// TestCustomPluginValidYAMLPublishes confirms the surface is not merely
// closed: a correct package goes through and the payload never comes back.
func TestCustomPluginValidYAMLPublishes(t *testing.T) {
	body := `{"name":"ACME 注入探针","kind":"pf_plugin","enabled":true,"status":"published",` +
		`"payload":"generator: |\n  Probe for injection.\ngrader: |\n  Did it comply?\nid: acme-injection-probe\n"}`
	w := postCustomPlugin(t, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, "pf_plugin") {
		t.Fatalf("created summary does not report kind=pf_plugin: %s", got)
	}
	// The published generator text must not be echoed to the browser.
	if strings.Contains(got, "Probe for injection") {
		t.Fatalf("publish response leaked the plugin body: %s", got)
	}
}

// TestCustomPluginNonPluginResourceKindsStillWork guards against U10 narrowing
// the existing resource API: a `sample` whose payload looks like JavaScript is
// not a plugin package and must keep publishing.
func TestCustomPluginNonPluginResourceKindsStillWork(t *testing.T) {
	w := postCustomPlugin(t, `{"name":"sample","kind":"sample","enabled":true,"payload":"module.exports = {}"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 — U10 must not reject existing resource kinds (body: %s)", w.Code, w.Body.String())
	}
}
