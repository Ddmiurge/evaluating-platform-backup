package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/model"
)

// Expert published-catalog browsing (2026-10-30): the expert's resource list
// may additionally merge cross-expert PUBLICATIONS — but only for the
// promptfoo package kinds, only on explicit request, with the caller's own
// packages deduped to their own rows and tagged, and never revealing which
// expert published a foreign package.

func newExpertCatalogFixture(t *testing.T) (expertA, expertB, enterpriseID uuid.UUID, publications *projectionHandlerPublicationStore, h *MaclawEvaluationHandler) {
	t.Helper()
	expertA = uuid.New()
	expertB = uuid.New()
	enterpriseID = uuid.New()

	own := map[string][]maclaw.EvaluationResourceSummary{
		string(maclaw.EvaluationResourceKindPromptfooStrategy): {
			{
				ID:      "res_a",
				Handle:  "handle_a",
				Name:    "Caller package",
				Kind:    maclaw.EvaluationResourceKindPromptfooStrategy,
				Version: "v2",
				Status:  maclaw.EvaluationResourceStatusPublished,
				Enabled: true,
				Metadata: map[string]string{
					"pf_strategy_form": "js",
				},
			},
			{
				ID:      "res_a_archived",
				Handle:  "handle_a_old",
				Name:    "Caller archived package",
				Kind:    maclaw.EvaluationResourceKindPromptfooStrategy,
				Version: "v1",
				Status:  maclaw.EvaluationResourceStatusArchived,
				Enabled: false,
			},
		},
		string(maclaw.EvaluationResourceKindSample): {
			{ID: "own_sample", Handle: "sample_handle", Name: "Caller sample", Kind: maclaw.EvaluationResourceKindSample, Status: maclaw.EvaluationResourceStatusPublished, Enabled: true},
		},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/evaluation/resources" {
			t.Fatalf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		kind := maclaw.EvaluationResourceKind(r.URL.Query().Get("kind"))
		items, ok := own[string(kind)]
		if !ok {
			items = nil
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	}))
	t.Cleanup(upstream.Close)

	client, err := maclaw.NewClient(maclaw.Config{BaseURL: upstream.URL, APIToken: "upstream-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("upstream client: %v", err)
	}
	// Enterprise runs against an EMPTY own-list upstream: its catalog assertions
	// must see the publication rows only, never the expert's own rows.
	enterpriseUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []maclaw.EvaluationResourceSummary{}})
	}))
	t.Cleanup(enterpriseUpstream.Close)
	enterpriseClient, err := maclaw.NewClient(maclaw.Config{BaseURL: enterpriseUpstream.URL, APIToken: "enterprise-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("enterprise client: %v", err)
	}
	provider := &projectionHandlerProvider{sessions: map[string]*maclaw.GatewaySession{
		expertA.String():        {Client: client, InstanceID: "inst_expert_a", Mapping: &model.MaclawAccountMapping{PlatformUserID: expertA, PlatformRole: model.RoleExpert, MaclawTenantID: "tenant_expert_a"}},
		enterpriseID.String():   {Client: enterpriseClient, InstanceID: "inst_enterprise", Mapping: &model.MaclawAccountMapping{PlatformUserID: enterpriseID, PlatformRole: model.RoleEnterprise, MaclawTenantID: "tenant_enterprise"}},
	}}
	publications = newProjectionHandlerPublicationStore()
	seed := func(owner uuid.UUID, resourceID, handle string, kind maclaw.EvaluationResourceKind, metadata map[string]string) {
		if err := publications.UpsertPublication(context.Background(), &model.MaclawResourcePublication{
			SourceExpertUserID:   owner,
			SourceResourceID:     resourceID,
			SourceResourceHandle: handle,
			SourceVersion:        "v1",
			Name:                 "package " + resourceID,
			Kind:                 string(kind),
			Status:               string(maclaw.EvaluationResourceStatusPublished),
			Enabled:              true,
			Metadata:             metadata,
		}); err != nil {
			t.Fatalf("seed publication %s: %v", resourceID, err)
		}
	}
	// The caller's own published package (dedupe target) and a foreign one.
	seed(expertA, "res_a", "handle_a", maclaw.EvaluationResourceKindPromptfooStrategy, map[string]string{"pf_strategy_form": "js"})
	seed(expertB, "res_b", "handle_b", maclaw.EvaluationResourceKindPromptfooStrategy, map[string]string{"pf_strategy_form": "yaml"})
	// A foreign SAMPLE publication: must never reach an expert, whatever params arrive.
	seed(expertB, "res_b_sample", "handle_b_sample", maclaw.EvaluationResourceKindSample, nil)

	projection := maclaw.NewResourceProjectionService(provider, publications, newProjectionHandlerShadowStore())
	h = NewMaclawEvaluationHandlerWithProviderAndProjection(provider, projection)
	return expertA, expertB, enterpriseID, publications, h
}

func doListResources(t *testing.T, h *MaclawEvaluationHandler, userID uuid.UUID, role string, query string) (int, []maclaw.EvaluationResourceSummary, string) {
	t.Helper()
	router := gin.New()
	router.GET("/maclaw/evaluation/resources", func(c *gin.Context) {
		c.Set("user_id", userID.String())
		c.Set("user_role", role)
		h.ListResources(c)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/resources?"+query, nil)
	router.ServeHTTP(rec, req)
	body := rec.Body.String()
	var out struct {
		Items []maclaw.EvaluationResourceSummary `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	return rec.Code, out.Items, body
}

func findItem(items []maclaw.EvaluationResourceSummary, id string) (maclaw.EvaluationResourceSummary, int) {
	found := maclaw.EvaluationResourceSummary{}
	count := 0
	for _, item := range items {
		if item.ID == id {
			found = item
			count++
		}
	}
	return found, count
}

func TestMaclawEvaluationHandlerExpertPublishedCatalogMergesOwnTaggedAndForeignHidden(t *testing.T) {
	expertA, _, _, _, h := newExpertCatalogFixture(t)
	status, items, body := doListResources(t, h, expertA, "expert", "kind=pf_strategy&include_published_catalog=1&include_inactive=true&limit=200")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}

	ownRow, ownCount := findItem(items, "res_a")
	if ownCount != 1 {
		t.Fatalf("res_a appeared %d times; own row and its publication must merge into exactly one", ownCount)
	}
	if ownRow.Version != "v2" || ownRow.Status != maclaw.EvaluationResourceStatusPublished {
		t.Fatalf("res_a must be the caller's own row (v2), not the publication copy (v1): %#v", ownRow)
	}
	if ownRow.Metadata["is_own"] != "true" {
		t.Fatalf("own row metadata = %#v, want is_own=true", ownRow.Metadata)
	}
	if ownRow.Metadata["pf_strategy_form"] != "js" {
		t.Fatalf("own row must keep its package form for the 代码策略 tag: %#v", ownRow.Metadata)
	}
	archived, archivedCount := findItem(items, "res_a_archived")
	if archivedCount != 1 || archived.Metadata["is_own"] != "true" {
		t.Fatalf("caller's archived row must stay visible and tagged: %#v", archived)
	}

	foreign, foreignCount := findItem(items, "res_b")
	if foreignCount != 1 {
		t.Fatalf("foreign published package missing or duplicated: %d", foreignCount)
	}
	if _, leaked := foreign.Metadata["source_expert_user_id"]; leaked {
		t.Fatalf("expert catalog must not reveal who published a foreign package: %#v", foreign.Metadata)
	}
	if _, tagged := foreign.Metadata["is_own"]; tagged {
		t.Fatalf("foreign package must not be tagged is_own: %#v", foreign.Metadata)
	}
	if !strings.Contains(body, `"is_own"`) {
		t.Fatalf("response must carry the is_own literal (frontend key contract): %s", body)
	}
}

func TestMaclawEvaluationHandlerExpertCatalogParamNeverLeaksForeignSamples(t *testing.T) {
	expertA, _, _, _, h := newExpertCatalogFixture(t)
	status, items, body := doListResources(t, h, expertA, "expert", "kind=sample&include_published_catalog=1&include_inactive=true&limit=200")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if _, count := findItem(items, "res_b_sample"); count != 0 {
		t.Fatalf("another expert's SAMPLE reached the expert list — the engine-kind whitelist is the boundary")
	}
	if _, count := findItem(items, "own_sample"); count != 1 {
		t.Fatalf("own rows must still be served: %#v", items)
	}
	if strings.Contains(body, "is_own") {
		t.Fatalf("non-engine kinds must stay on the untouched own-only path: %s", body)
	}
}

func TestMaclawEvaluationHandlerExpertCatalogOffWithoutParam(t *testing.T) {
	expertA, _, _, _, h := newExpertCatalogFixture(t)
	status, items, body := doListResources(t, h, expertA, "expert", "kind=pf_strategy&include_inactive=true&limit=200")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if _, count := findItem(items, "res_b"); count != 0 {
		t.Fatalf("without include_published_catalog the expert list must stay exactly as before")
	}
	if _, count := findItem(items, "res_a"); count != 1 {
		t.Fatalf("own row missing: %#v", items)
	}
	if strings.Contains(body, "is_own") {
		t.Fatalf("unrequested list must not carry catalog tags: %s", body)
	}
}

func TestMaclawEvaluationHandlerEnterpriseCatalogUnchangedByExpertFeature(t *testing.T) {
	_, expertB, enterpriseID, _, h := newExpertCatalogFixture(t)
	// Enterprise sees its own upstream resources (empty for these kinds here)
	// plus the full publication catalog — owner metadata intact, no is_own.
	status, items, body := doListResources(t, h, enterpriseID, "enterprise", "kind=pf_strategy&include_inactive=false&limit=200")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	foreign, count := findItem(items, "res_b")
	if count != 1 {
		t.Fatalf("enterprise must see published packages without any parameter: %#v", items)
	}
	if foreign.Metadata["source_expert_user_id"] != expertB.String() {
		t.Fatalf("enterprise projection metadata must stay byte-for-byte unchanged: %#v", foreign.Metadata)
	}
	if strings.Contains(body, "is_own") {
		t.Fatalf("enterprise catalog must not gain ownership tags: %s", body)
	}
}

// TestFrontendExpertCatalogKeepsTheCatalogContractLiterals is the source-level
// mirror guard this repo uses wherever a backend literal and a frontend read
// of that literal must move together. The behavioral tests above pin the
// BACKEND side; without this one, renaming `is_own` in Go would leave the
// published-catalog feature half-alive: the pages would silently show every
// package as foreign and the own-row actions would vanish.
func TestFrontendExpertCatalogKeepsTheCatalogContractLiterals(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	paths := []string{
		filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "frontend", "src", "services", "expert.ts"),
		filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "frontend", "src", "services", "engineEval.ts"),
	}
	literals := []string{"is_own", "include_published_catalog", "pf_strategy_form"}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read frontend catalog consumer %s: %v", path, err)
		}
		source := string(raw)
		for _, literal := range literals {
			if !strings.Contains(source, literal) {
				t.Fatalf("%s must keep reading the backend contract literal %q (renamed on one side only?)", filepath.Base(path), literal)
			}
		}
	}
}
