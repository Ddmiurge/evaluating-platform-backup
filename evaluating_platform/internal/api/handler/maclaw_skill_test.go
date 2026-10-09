package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/model"
)

type fakeMaclawSkillGateway struct {
	enabled       bool
	lastLimit     int
	lastSearch    maclaw.SkillSearchInput
	lastImported  maclaw.SkillImportInput
	lastInstalled maclaw.SkillInstallInput
}

func (f *fakeMaclawSkillGateway) Enabled() bool { return f.enabled }

func (f *fakeMaclawSkillGateway) ListSkills(ctx context.Context, limit int) ([]maclaw.SkillSummary, error) {
	_ = ctx
	f.lastLimit = limit
	return []maclaw.SkillSummary{
		{Name: "classical-rewriter", Description: "Rewrite prompts", Status: "active", Source: "zip_import", Type: "executable", Mode: "sequential", RequiredEnv: []string{"OPENAI_API_KEY"}},
	}, nil
}

func (f *fakeMaclawSkillGateway) SearchSkills(ctx context.Context, in maclaw.SkillSearchInput) ([]maclaw.SkillSearchResult, error) {
	_ = ctx
	f.lastSearch = in
	return []maclaw.SkillSearchResult{
		{Source: "github", Name: "rewrite-skill", Description: "Generic maclaw skill", DefinitionType: "skill.yaml"},
	}, nil
}

func (f *fakeMaclawSkillGateway) InstallSkill(ctx context.Context, in maclaw.SkillInstallInput) ([]maclaw.SkillSummary, error) {
	_ = ctx
	f.lastInstalled = in
	return []maclaw.SkillSummary{
		{Name: "ccbos-classical-chinese-skill", Description: "Classical Chinese jailbreak", Status: "active", Source: "skillhub", Version: "1.0.0", HubSkillID: "ccbos-classical-chinese-skill"},
	}, nil
}

func TestMaclawSkillHandlerListsSafeSummaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawSkillGateway{enabled: true}
	handler := NewMaclawSkillHandler(gateway)
	router := gin.New()
	router.GET("/maclaw/skills", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/skills?limit=5", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastLimit != 5 {
		t.Fatalf("limit = %d", gateway.lastLimit)
	}
	if strings.Contains(w.Body.String(), "skill_dir") || strings.Contains(w.Body.String(), "steps") || strings.Contains(w.Body.String(), "content") {
		t.Fatalf("list response leaked runtime internals: %s", w.Body.String())
	}
	var resp struct {
		Items []maclaw.SkillSummary `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name != "classical-rewriter" {
		t.Fatalf("items = %#v", resp.Items)
	}
}

func TestMaclawSkillHandlerSearchesMaclawSkills(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawSkillGateway{enabled: true}
	handler := NewMaclawSkillHandler(gateway)
	router := gin.New()
	router.POST("/maclaw/skills/search", handler.Search)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/search", strings.NewReader(`{"query":"rewrite","sources":["github"],"top_n":3}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastSearch.Query != "rewrite" || gateway.lastSearch.TopN != 3 || len(gateway.lastSearch.Sources) != 1 {
		t.Fatalf("search input = %#v", gateway.lastSearch)
	}
	var resp struct {
		Items []maclaw.SkillSearchResult `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name != "rewrite-skill" {
		t.Fatalf("items = %#v", resp.Items)
	}
}

func TestMaclawSkillHandlerSearchUsesConfiguredHubWhenURLIsOmitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{
		Client:     mustMaclawClientForSkillTest(t, gateway),
		InstanceID: "inst_expert",
		Mapping:    &model.MaclawAccountMapping{PlatformUserID: expertID, MaclawTenantID: "tenant_expert"},
	}}
	handler := NewMaclawSkillHandlerWithProviderProjectionAndHub(provider, nil, fakeSkillHubConfigProvider{
		cfg: &maclaw.MaclawHubConfig{Enabled: true, HubURL: "https://hub.internal", AllowedSources: []string{"skillhub"}},
	})
	router := gin.New()
	router.POST("/maclaw/skills/search", func(c *gin.Context) {
		c.Set("user_id", expertID.String())
		c.Set("user_role", "expert")
		handler.Search(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/search", strings.NewReader(`{"query":"ccbos","sources":["skillhub"],"top_n":5}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastSearch.SkillHubURL != "https://hub.internal" {
		t.Fatalf("skill_hub_url = %q", gateway.lastSearch.SkillHubURL)
	}
	if len(gateway.lastSearch.Sources) != 1 || gateway.lastSearch.Sources[0] != "skillhub" {
		t.Fatalf("sources = %#v, want configured skillhub-only default", gateway.lastSearch.Sources)
	}
}

func TestMaclawSkillHandlerSearchRejectsDisallowedSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{
		Client:     mustMaclawClientForSkillTest(t, gateway),
		InstanceID: "inst_expert",
		Mapping:    &model.MaclawAccountMapping{PlatformUserID: expertID, MaclawTenantID: "tenant_expert"},
	}}
	handler := NewMaclawSkillHandlerWithProviderProjectionAndHub(provider, nil, fakeSkillHubConfigProvider{
		cfg: &maclaw.MaclawHubConfig{Enabled: true, HubURL: "https://hub.internal", AllowedSources: []string{"skillhub"}},
	})
	router := gin.New()
	router.POST("/maclaw/skills/search", func(c *gin.Context) {
		c.Set("user_id", expertID.String())
		c.Set("user_role", "expert")
		handler.Search(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/search", strings.NewReader(`{"query":"ccbos","sources":["github"],"top_n":5}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if len(gateway.lastSearch.Sources) != 0 {
		t.Fatalf("gateway search was called: %#v", gateway.lastSearch)
	}
}

func TestMaclawSkillHandlerSearchRequiresEnabledHubConfigForSourcePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{
		Client:     mustMaclawClientForSkillTest(t, gateway),
		InstanceID: "inst_expert",
		Mapping:    &model.MaclawAccountMapping{PlatformUserID: expertID, MaclawTenantID: "tenant_expert"},
	}}
	handler := NewMaclawSkillHandlerWithProviderProjectionAndHub(provider, nil, fakeSkillHubConfigProvider{})
	router := gin.New()
	router.POST("/maclaw/skills/search", func(c *gin.Context) {
		c.Set("user_id", expertID.String())
		c.Set("user_role", "expert")
		handler.Search(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/search", strings.NewReader(`{"query":"ccbos","sources":["github"],"top_n":5}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if len(gateway.lastSearch.Sources) != 0 {
		t.Fatalf("gateway search was called: %#v", gateway.lastSearch)
	}
}

func TestMaclawSkillHandlerInstallRejectsDisallowedSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{
		Client:     mustMaclawClientForSkillTest(t, gateway),
		InstanceID: "inst_expert",
		Mapping:    &model.MaclawAccountMapping{PlatformUserID: expertID, MaclawTenantID: "tenant_expert"},
	}}
	handler := NewMaclawSkillHandlerWithProviderProjectionAndHub(provider, nil, fakeSkillHubConfigProvider{
		cfg: &maclaw.MaclawHubConfig{Enabled: true, HubURL: "https://hub.internal", AllowedSources: []string{"skillhub"}},
	})
	router := gin.New()
	router.POST("/maclaw/skills/install", func(c *gin.Context) {
		c.Set("user_id", expertID.String())
		c.Set("user_role", "expert")
		handler.Install(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/install", strings.NewReader(`{"source":"github","skill_id":"owner/repo","overwrite":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastInstalled.Source != "" {
		t.Fatalf("gateway install was called: %#v", gateway.lastInstalled)
	}
}

func TestMaclawSkillHandlerInstallRequiresEnabledHubConfigForSourcePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{
		Client:     mustMaclawClientForSkillTest(t, gateway),
		InstanceID: "inst_expert",
		Mapping:    &model.MaclawAccountMapping{PlatformUserID: expertID, MaclawTenantID: "tenant_expert"},
	}}
	handler := NewMaclawSkillHandlerWithProviderProjectionAndHub(provider, nil, fakeSkillHubConfigProvider{})
	router := gin.New()
	router.POST("/maclaw/skills/install", func(c *gin.Context) {
		c.Set("user_id", expertID.String())
		c.Set("user_role", "expert")
		handler.Install(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/install", strings.NewReader(`{"source":"github","skill_id":"owner/repo","overwrite":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastInstalled.Source != "" {
		t.Fatalf("gateway install was called: %#v", gateway.lastInstalled)
	}
}

func TestMaclawSkillHandlerImportRequiresHubDistribution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawSkillGateway{enabled: true}
	handler := NewMaclawSkillHandler(gateway)
	router := gin.New()
	router.POST("/maclaw/skills/import", handler.Import)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/import", strings.NewReader(`{"zip_base64":"UEsDBAo=","overwrite":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastImported.ZipBase64 != "" {
		t.Fatalf("direct runtime import was called: %#v", gateway.lastImported)
	}
}

func TestMaclawSkillHandlerImportUsesHubAsOnlyDistributionPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{
		Client: mustMaclawClientForSkillTest(t, gateway),
		Mapping: &model.MaclawAccountMapping{
			PlatformUserID: expertID,
			PlatformEmail:  "expert@example.test",
		},
	}}
	store := newHandlerSkillPublicationStore()
	projection := maclaw.NewSkillProjectionService(provider, store, newHandlerSkillShadowStore())
	var submitted bool
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills/submit":
			submitted = true
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			if got := r.FormValue("email"); got != "expert@example.test" {
				t.Fatalf("email = %q", got)
			}
			file, _, err := r.FormFile("zip")
			if err != nil {
				t.Fatalf("zip form file missing: %v", err)
			}
			archiveBytes, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("read zip form file: %v", err)
			}
			if !zipArchiveContains(t, archiveBytes, "runtime/data/cases.json") || !zipArchiveContains(t, archiveBytes, "runtime/data/images/sample.png") {
				t.Fatalf("submitted zip should duplicate root data assets under runtime/data")
			}
			embedded := zipArchiveFileContent(t, archiveBytes, "runtime/embedded_data.py")
			if !strings.Contains(embedded, `"data/cases.json"`) || !strings.Contains(embedded, `"data/images/sample.png"`) {
				t.Fatalf("submitted zip should embed root data assets for MaClaw installers that drop subdirectories")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "sub_1", "status": "pending"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skill-submissions/sub_1":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "skill_id": "hub-generated-ccbos-id"})
		default:
			t.Fatalf("unexpected hub request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer hub.Close()
	handler := NewMaclawSkillHandlerWithProviderProjectionAndHub(provider, projection, fakeSkillHubConfigProvider{
		cfg: &maclaw.MaclawHubConfig{HubURL: hub.URL, Enabled: true, AllowedSources: []string{"skillhub"}},
	})
	router := gin.New()
	router.POST("/maclaw/skills/import", func(c *gin.Context) {
		c.Set("user_id", expertID.String())
		c.Set("user_role", "expert")
		handler.Import(c)
	})

	archive := skillArchiveForHubNormalizationTest(t)
	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/import", strings.NewReader(`{"zip_base64":"`+base64.StdEncoding.EncodeToString(archive)+`","overwrite":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !submitted {
		t.Fatalf("expected import to submit package to Hub")
	}
	if gateway.lastImported.ZipBase64 != "" {
		t.Fatalf("direct runtime import was called: %#v", gateway.lastImported)
	}
	if gateway.lastInstalled.Source != "skillhub" || gateway.lastInstalled.SkillHubURL != hub.URL || gateway.lastInstalled.SkillID != "hub-generated-ccbos-id" {
		t.Fatalf("install input = %#v", gateway.lastInstalled)
	}
	if len(store.items) != 1 || store.items[0].SourceSkillName != "ccbos-classical-chinese-skill" {
		t.Fatalf("publication items = %#v", store.items)
	}
	if store.items[0].Metadata["hub_skill_id"] != "hub-generated-ccbos-id" {
		t.Fatalf("hub_skill_id metadata = %#v", store.items[0].Metadata)
	}
}

func skillArchiveForHubNormalizationTest(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"skill.yaml":             "name: data-skill\n",
		"README.md":              "data skill\n",
		"runtime/main.py":        "print('ok')\n",
		"data/cases.json":        `{"cases":[]}`,
		"data/images/sample.png": "png",
		"runtime/data/keep.txt":  "existing",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func zipArchiveContains(t *testing.T, data []byte, name string) bool {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read submitted zip: %v", err)
	}
	for _, file := range zr.File {
		if file.Name == name {
			return true
		}
	}
	return false
}

func zipArchiveFileContent(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read submitted zip: %v", err)
	}
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open submitted zip file %s: %v", name, err)
		}
		defer rc.Close()
		content, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read submitted zip file %s: %v", name, err)
		}
		return string(content)
	}
	t.Fatalf("submitted zip missing %s", name)
	return ""
}

func TestMaclawSkillHandlerReturnsUnavailableWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewMaclawSkillHandler(&fakeMaclawSkillGateway{})
	router := gin.New()
	router.GET("/maclaw/skills", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/skills", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func mustMaclawClientForSkillTest(t *testing.T, gateway *fakeMaclawSkillGateway) *maclaw.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills":
			items, err := gateway.ListSkills(r.Context(), 0)
			if err != nil {
				t.Fatalf("ListSkills: %v", err)
			}
			_ = json.NewEncoder(w).Encode(gin.H{"items": items})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills/search":
			var in maclaw.SkillSearchInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatalf("decode search: %v", err)
			}
			items, err := gateway.SearchSkills(r.Context(), in)
			if err != nil {
				t.Fatalf("SearchSkills: %v", err)
			}
			_ = json.NewEncoder(w).Encode(gin.H{"items": items})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills/install":
			var in maclaw.SkillInstallInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatalf("decode install: %v", err)
			}
			items, err := gateway.InstallSkill(r.Context(), in)
			if err != nil {
				t.Fatalf("InstallSkill: %v", err)
			}
			_ = json.NewEncoder(w).Encode(gin.H{"items": items})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := maclaw.NewClient(maclaw.Config{BaseURL: server.URL, APIToken: "test-token", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

type handlerSkillPublicationStore struct {
	items []model.MaclawSkillPublication
}

func newHandlerSkillPublicationStore() *handlerSkillPublicationStore {
	return &handlerSkillPublicationStore{}
}

func (s *handlerSkillPublicationStore) UpsertSkillPublication(_ context.Context, item *model.MaclawSkillPublication) error {
	copied := *item
	s.items = append(s.items, copied)
	return nil
}

func (s *handlerSkillPublicationStore) MarkSkillPublicationUnavailable(_ context.Context, sourceExpertUserID uuid.UUID, sourceSkillName string) error {
	for i := range s.items {
		if s.items[i].SourceExpertUserID == sourceExpertUserID && s.items[i].SourceSkillName == sourceSkillName {
			s.items[i].Enabled = false
			s.items[i].Status = "disabled"
		}
	}
	return nil
}

func (s *handlerSkillPublicationStore) ListPublishedSkills(_ context.Context, q maclaw.SkillSearchInput) ([]model.MaclawSkillPublication, error) {
	out := []model.MaclawSkillPublication{}
	for _, item := range s.items {
		if item.Enabled && item.Status == "active" {
			out = append(out, item)
		}
	}
	_ = q
	return out, nil
}

type handlerSkillShadowStore struct{}

func newHandlerSkillShadowStore() *handlerSkillShadowStore { return &handlerSkillShadowStore{} }

func (s *handlerSkillShadowStore) GetSkillShadow(context.Context, uuid.UUID, uuid.UUID, string, string) (*model.MaclawSkillShadow, error) {
	return nil, nil
}

func (s *handlerSkillShadowStore) UpsertSkillShadow(context.Context, *model.MaclawSkillShadow) error {
	return nil
}

type fakeSkillHubConfigProvider struct {
	cfg *maclaw.MaclawHubConfig
}

func (p fakeSkillHubConfigProvider) GetHubConfig(context.Context) (*maclaw.MaclawHubConfig, error) {
	return p.cfg, nil
}

func TestMaclawSkillHandlerInstallsFromHubAndRecordsPublication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{
		Client:     mustMaclawClientForSkillTest(t, gateway),
		InstanceID: "inst_expert",
		Mapping:    &model.MaclawAccountMapping{PlatformUserID: expertID, MaclawTenantID: "tenant_expert"},
	}}
	store := newHandlerSkillPublicationStore()
	projection := maclaw.NewSkillProjectionService(provider, store, newHandlerSkillShadowStore())
	handler := NewMaclawSkillHandlerWithProviderAndProjection(provider, projection)
	router := gin.New()
	router.POST("/maclaw/skills/install", func(c *gin.Context) {
		c.Set("user_id", expertID.String())
		c.Set("user_role", "expert")
		handler.Install(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/install", strings.NewReader(`{"source":"skillhub","skill_hub_url":"https://hub.internal","skill_id":"ccbos-classical-chinese-skill","overwrite":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastInstalled.Source != "skillhub" || gateway.lastInstalled.SkillHubURL != "https://hub.internal" || gateway.lastInstalled.SkillID != "ccbos-classical-chinese-skill" || !gateway.lastInstalled.Overwrite {
		t.Fatalf("install input = %#v", gateway.lastInstalled)
	}
	if len(store.items) != 1 || store.items[0].SourceExpertUserID != expertID || store.items[0].Name != "ccbos-classical-chinese-skill" || store.items[0].SourceSkillName != "ccbos-classical-chinese-skill" {
		t.Fatalf("publication items = %#v", store.items)
	}
	if strings.Contains(w.Body.String(), "skill_dir") || strings.Contains(w.Body.String(), "steps") || strings.Contains(w.Body.String(), "content") || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatalf("install response leaked runtime internals: %s", w.Body.String())
	}
}

func TestMaclawSkillHandlerInstallsFromConfiguredHubWhenURLIsOmitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{
		Client:     mustMaclawClientForSkillTest(t, gateway),
		InstanceID: "inst_expert",
		Mapping:    &model.MaclawAccountMapping{PlatformUserID: expertID, MaclawTenantID: "tenant_expert"},
	}}
	store := newHandlerSkillPublicationStore()
	projection := maclaw.NewSkillProjectionService(provider, store, newHandlerSkillShadowStore())
	handler := NewMaclawSkillHandlerWithProviderProjectionAndHub(provider, projection, fakeSkillHubConfigProvider{
		cfg: &maclaw.MaclawHubConfig{Enabled: true, HubURL: "https://hub.internal", AllowedSources: []string{"skillhub"}},
	})
	router := gin.New()
	router.POST("/maclaw/skills/install", func(c *gin.Context) {
		c.Set("user_id", expertID.String())
		c.Set("user_role", "expert")
		handler.Install(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/maclaw/skills/install", strings.NewReader(`{"source":"skillhub","skill_id":"ccbos-classical-chinese-skill","overwrite":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastInstalled.SkillHubURL != "https://hub.internal" {
		t.Fatalf("skill_hub_url = %q", gateway.lastInstalled.SkillHubURL)
	}
}

func TestMaclawSkillHandlerListsExpertPublicationsForEnterprise(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	enterpriseID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{Client: mustMaclawClientForSkillTest(t, gateway), InstanceID: "inst_enterprise"}}
	store := newHandlerSkillPublicationStore()
	store.items = append(store.items, model.MaclawSkillPublication{
		SourceExpertUserID: expertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "1.0.0",
		Name:               "ccbos-classical-chinese-skill",
		Description:        "Generate classical Chinese jailbreak payloads",
		Status:             "active",
		Enabled:            true,
		Triggers:           []string{"文言文", "越狱", "CCBOS"},
	})
	projection := maclaw.NewSkillProjectionService(provider, store, newHandlerSkillShadowStore())
	handler := NewMaclawSkillHandlerWithProviderAndProjection(provider, projection)
	router := gin.New()
	router.GET("/maclaw/skills", func(c *gin.Context) {
		c.Set("user_id", enterpriseID.String())
		c.Set("user_role", "enterprise")
		handler.List(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/maclaw/skills", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []maclaw.SkillSummary `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, item := range resp.Items {
		if item.Name == "ccbos-classical-chinese-skill" {
			found = true
			if item.Metadata["source_expert_user_id"] != expertID.String() || item.Metadata["requires_shadow_copy"] != "true" {
				t.Fatalf("metadata = %#v", item.Metadata)
			}
		}
	}
	if !found {
		t.Fatalf("enterprise list did not include expert skill: %#v", resp.Items)
	}
}

func TestMaclawSkillHandlerListBackfillsPublishedHubSkillsForEnterprise(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expertID := uuid.New()
	enterpriseID := uuid.New()
	gateway := &fakeMaclawSkillGateway{enabled: true}
	provider := &fakeGatewayProvider{session: &maclaw.GatewaySession{
		Client: mustMaclawClientForSkillTest(t, gateway),
		Mapping: &model.MaclawAccountMapping{
			PlatformUserID:   enterpriseID,
			PlatformRole:     model.RoleEnterprise,
			MaclawTenantID:   "tenant_enterprise",
			MaclawInstanceID: "inst_enterprise",
		},
	}}
	store := newHandlerSkillPublicationStore()
	store.items = append(store.items, model.MaclawSkillPublication{
		SourceExpertUserID: expertID,
		SourceSkillName:    "ccbos-classical-chinese-skill",
		SourceVersion:      "1",
		Name:               "ccbos-classical-chinese-skill",
		Status:             "active",
		Enabled:            true,
		Metadata:           map[string]string{"hub_skill_id": "hub-ccbos"},
	})
	projection := maclaw.NewSkillProjectionService(provider, store, newHandlerSkillShadowStore())
	projection.SetHubConfigProvider(fakeSkillHubConfigProvider{
		cfg: &maclaw.MaclawHubConfig{Enabled: true, HubURL: "https://hub.internal", AllowedSources: []string{"skillhub"}},
	})
	handler := NewMaclawSkillHandlerWithProviderProjectionAndHub(provider, projection, fakeSkillHubConfigProvider{
		cfg: &maclaw.MaclawHubConfig{Enabled: true, HubURL: "https://hub.internal", AllowedSources: []string{"skillhub"}},
	})
	router := gin.New()
	router.GET("/maclaw/skills", func(c *gin.Context) {
		c.Set("user_id", enterpriseID.String())
		c.Set("user_role", "enterprise")
		handler.List(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/maclaw/skills", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastInstalled.Source != "skillhub" || gateway.lastInstalled.SkillID != "hub-ccbos" || gateway.lastInstalled.SkillHubURL != "https://hub.internal" {
		t.Fatalf("install input = %#v", gateway.lastInstalled)
	}
}
