package handler

// engine_run_test.go — BFF endpoint tests for promptfoo-engine runs.
//
// Focus: role gating, fail-closed when engine is not configured, credential
// materialization flow (target secret never leaks into responses), and
// create/list/get/cancel lifecycle against fake collaborators.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
)

// ---- fakes ----

type fakeEngineClient struct {
	maclaw.PromptfooEngineClient // embed for Enabled(); zero value is disabled
	healthErr                    error
	createErr                    error
	status                       *maclaw.EngineRunStatus
	cancelErr                    error
	createReq                    *maclaw.EngineRunRequest
	cancelCalled                 bool
}

func newEnabledFakeEngine() *fakeEngineClient {
	c := &fakeEngineClient{}
	// Enable by constructing a real client against a dummy URL (never dialed
	// because all methods are overridden by the fake).
	c.PromptfooEngineClient = *maclaw.NewPromptfooEngineClient("http://engine.test", "t", 5)
	return c
}

func (f *fakeEngineClient) Health(_ context.Context) error { return f.healthErr }

func (f *fakeEngineClient) CreateRun(_ context.Context, run *maclaw.EngineRunRequest) error {
	f.createReq = run
	return f.createErr
}

func (f *fakeEngineClient) GetRun(_ context.Context, runID string) (*maclaw.EngineRunStatus, error) {
	if f.status != nil {
		s := *f.status
		s.RunID = runID
		return &s, nil
	}
	return &maclaw.EngineRunStatus{RunID: runID, Phase: maclaw.EnginePhaseExecutingProbes}, nil
}

func (f *fakeEngineClient) CancelRun(_ context.Context, _ string) error {
	f.cancelCalled = true
	return f.cancelErr
}

func (f *fakeEngineClient) StreamRunEvents(_ context.Context, _ string, onEvent func(*maclaw.EngineRunStatus)) error {
	if onEvent != nil {
		onEvent(&maclaw.EngineRunStatus{RunID: "r", Phase: maclaw.EnginePhaseSucceeded})
	}
	return nil
}

type fakeTargetConfigService struct {
	target *maclaw.EvaluationTargetInput
	err    error
}

func (f *fakeTargetConfigService) GetTargetWithSecret(_ context.Context, _ uuid.UUID) (*maclaw.EvaluationTargetInput, error) {
	return f.target, f.err
}

// fakeGenerationProvider satisfies maclaw.EngineGenerationProvider with the
// platform default LLM config (first-provider fallback path of
// selectJudgeProvider, same as production demo provider).
type fakeGenerationProvider struct {
	cfg  *maclaw.RuntimeAppConfig
	err  error
}

func (f *fakeGenerationProvider) GetDefaultRuntimeConfig(_ context.Context) (*maclaw.RuntimeAppConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.cfg, nil
}

func newFakeGenerationProvider() *fakeGenerationProvider {
	return &fakeGenerationProvider{
		cfg: &maclaw.RuntimeAppConfig{
			MaclawLLMProviders: []maclaw.RuntimeLLMProvider{
				{Name: "demo", URL: "https://gen.example.com", Key: "sk-gen-secret-value", Model: "deepseek-v4-flash", WireAPI: "chat_completions"},
			},
			MaclawLLMCurrentProvider: "demo",
		},
	}
}

type fakeRunStore struct {
	records map[string]*maclaw.EngineRunRecord
}

func newFakeRunStore() *fakeRunStore {
	return &fakeRunStore{records: map[string]*maclaw.EngineRunRecord{}}
}

func (s *fakeRunStore) Create(_ context.Context, r maclaw.EngineRunRecord) (*maclaw.EngineRunRecord, error) {
	r.CreatedAt = time.Now()
	r.UpdatedAt = time.Now()
	c := r
	s.records[r.ID] = &c
	return &c, nil
}

func (s *fakeRunStore) Update(_ context.Context, r maclaw.EngineRunRecord) (*maclaw.EngineRunRecord, error) {
	if _, ok := s.records[r.ID]; !ok {
		return nil, context.Canceled
	}
	r.UpdatedAt = time.Now()
	c := r
	s.records[r.ID] = &c
	return &c, nil
}

func (s *fakeRunStore) Get(_ context.Context, _ uuid.UUID, id string) (*maclaw.EngineRunRecord, error) {
	if rec, ok := s.records[id]; ok {
		c := *rec
		return &c, nil
	}
	return nil, nil
}

func (s *fakeRunStore) List(_ context.Context, _ uuid.UUID, _ string, _ int) ([]maclaw.EngineRunRecord, error) {
	out := []maclaw.EngineRunRecord{}
	for _, rec := range s.records {
		out = append(out, *rec)
	}
	return out, nil
}

// ---- helpers ----

func newEngineRunRouter(handler *EngineRunHandler, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", "11111111-1111-1111-1111-111111111111")
		c.Set("user_role", role)
		c.Next()
	})
	r.GET("/maclaw/engine/health", handler.Health)
	r.POST("/maclaw/engine/runs", handler.CreateRun)
	r.GET("/maclaw/engine/runs", handler.ListRuns)
	r.GET("/maclaw/engine/runs/:id", handler.GetRun)
	r.POST("/maclaw/engine/runs/:id/cancel", handler.CancelRun)
	return r
}

func doJSON(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---- tests ----

func TestEngineRunHandlerDisabledFailClosed(t *testing.T) {
	// Zero-value handler: engine not configured → all endpoints 503.
	h := NewEngineRunHandler(maclaw.NewPromptfooEngineClient("", "", 0), nil, nil, nil)
	r := newEngineRunRouter(h, "enterprise")

	if w := doJSON(r, http.MethodGet, "/maclaw/engine/health", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d body = %s", w.Code, w.Body.String())
	}
	if w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", `{"purpose":"p","plugins":[{"id":"harmful"}]}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("create status = %d", w.Code)
	}
}

func TestEngineRunHandlerRoleGating(t *testing.T) {
	h := newEngineRunHandler(newEnabledFakeEngine(), maclaw.NewEngineRunService(newFakeRunStore()), nil, nil)
	r := newEngineRunRouter(h, "expert")

	if w := doJSON(r, http.MethodGet, "/maclaw/engine/health", ""); w.Code != http.StatusForbidden {
		t.Fatalf("expert should be forbidden, got %d", w.Code)
	}
}

func TestEngineRunHandlerCreateRunValidation(t *testing.T) {
	store := newFakeRunStore()
	h := newEngineRunHandler(newEnabledFakeEngine(), maclaw.NewEngineRunService(store), nil, nil)
	r := newEngineRunRouter(h, "enterprise")

	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing purpose", `{"plugins":[{"id":"harmful"}]}`, http.StatusBadRequest},
		{"missing plugins", `{"purpose":"合规"}`, http.StatusBadRequest},
		{"bad judge mode", `{"purpose":"p","plugins":[{"id":"x"}],"judge_mode":"nope"}`, http.StatusBadRequest},
		{"num_tests too large", `{"purpose":"p","plugins":[{"id":"x"}],"num_tests":9999}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", tc.body); w.Code != tc.want {
				t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestEngineRunHandlerCreateRunRequiresTarget(t *testing.T) {
	store := newFakeRunStore()
	targets := &fakeTargetConfigService{target: nil}
	h := newEngineRunHandler(newEnabledFakeEngine(), maclaw.NewEngineRunService(store), targets, nil)
	r := newEngineRunRouter(h, "enterprise")

	body := `{"purpose":"合规安全评测","plugins":[{"id":"harmful"}],"num_tests":5}`
	if w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", body); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestEngineRunHandlerCreateRunRequiresGeneration(t *testing.T) {
	store := newFakeRunStore()
	targets := &fakeTargetConfigService{target: &maclaw.EvaluationTargetInput{
		BaseURL: "https://t.example/v1", Model: "m", CredentialSecret: "sk-x", Enabled: true,
	}}
	// nil generation provider (nothing configured) must fail fast: 503, no record.
	h := newEngineRunHandler(newEnabledFakeEngine(), maclaw.NewEngineRunService(store), targets, nil)
	r := newEngineRunRouter(h, "enterprise")

	w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", `{"purpose":"p","plugins":[{"id":"x"}]}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "generation_not_configured") {
		t.Fatalf("missing error code: %s", w.Body.String())
	}
	if len(store.records) != 0 {
		t.Fatalf("run record created despite missing generation model: %+v", store.records)
	}
}

func TestEngineRunHandlerCreateRunSuccessNoSecretLeak(t *testing.T) {
	store := newFakeRunStore()
	engine := newEnabledFakeEngine()
	targets := &fakeTargetConfigService{target: &maclaw.EvaluationTargetInput{
		ID:               "t1",
		BaseURL:          "https://target.example/v1",
		Model:            "gpt-test",
		CredentialSecret: "sk-super-secret-value",
		Enabled:          true,
	}}
	h := newEngineRunHandler(engine, maclaw.NewEngineRunService(store), targets, newFakeGenerationProvider())
	r := newEngineRunRouter(h, "enterprise")

	body := `{"purpose":"合规安全评测","plugins":[{"id":"harmful"},{"id":"pii"}],"strategies":[{"id":"jailbreak"}],"num_tests":5,"judge_mode":"auto","session_id":"sess-1"}`
	w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}

	// Engine request must carry the materialized credential (server-side only).
	if engine.createReq == nil {
		t.Fatal("engine request missing")
	}
	if engine.createReq.Credentials.Target.APIKey != "sk-super-secret-value" {
		t.Fatalf("engine credential not materialized: %+v", engine.createReq.Credentials)
	}
	if engine.createReq.Credentials.Target.BaseURL != "https://target.example/v1" || engine.createReq.Credentials.Target.Model != "gpt-test" {
		t.Fatalf("unexpected target credential: %+v", engine.createReq.Credentials.Target)
	}
	// Generation credential (platform default MaClaw LLM) must be materialized
	// with the /v1-normalized base URL for the engine's Plan-A generation.
	if gen := engine.createReq.Credentials.Generation; gen == nil {
		t.Fatal("generation credential missing from engine request")
	} else if gen.BaseURL != "https://gen.example.com/v1" || gen.Model != "deepseek-v4-flash" || gen.APIKey != "sk-gen-secret-value" {
		t.Fatalf("unexpected generation credential: %+v", gen)
	}
	if engine.createReq.NumTests != 5 || engine.createReq.JudgeMode != maclaw.EngineJudgeAuto {
		t.Fatalf("unexpected request: %+v", engine.createReq)
	}
	if len(engine.createReq.Plugins) != 2 || len(engine.createReq.Strategies) != 1 {
		t.Fatalf("unexpected caps: %+v", engine.createReq)
	}

	// Response must NEVER contain the credentials.
	if strings.Contains(w.Body.String(), "sk-super-secret-value") || strings.Contains(w.Body.String(), "sk-gen-secret-value") {
		t.Fatalf("credential leaked into response: %s", w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp["phase"] != "queued" || resp["purpose"] != "合规安全评测" {
		t.Fatalf("unexpected response: %v", resp)
	}

	// Persisted record must not contain the secrets either.
	for _, rec := range store.records {
		blob, _ := json.Marshal(rec)
		if bytes.Contains(blob, []byte("sk-super-secret-value")) || bytes.Contains(blob, []byte("sk-gen-secret-value")) {
			t.Fatalf("credential leaked into persisted record: %s", blob)
		}
	}
}

func TestEngineRunHandlerCreateRunEngineErrorMarksFailed(t *testing.T) {
	store := newFakeRunStore()
	engine := newEnabledFakeEngine()
	engine.createErr = maclaw.ErrEngineQueueFull
	targets := &fakeTargetConfigService{target: &maclaw.EvaluationTargetInput{
		BaseURL: "https://t.example/v1", Model: "m", CredentialSecret: "sk-x", Enabled: true,
	}}
	h := newEngineRunHandler(engine, maclaw.NewEngineRunService(store), targets, newFakeGenerationProvider())
	r := newEngineRunRouter(h, "enterprise")

	w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", `{"purpose":"p","plugins":[{"id":"x"}]}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	for _, rec := range store.records {
		if rec.Status != maclaw.EnginePhaseFailed || rec.ErrorCode != "engine_queue_full" {
			t.Fatalf("record not marked failed: %+v", rec)
		}
	}
}

func TestEngineRunHandlerGetRunPollsEngineAndPersists(t *testing.T) {
	store := newFakeRunStore()
	engine := newEnabledFakeEngine()
	engine.status = &maclaw.EngineRunStatus{Phase: maclaw.EnginePhaseSucceeded, PlannedCount: 5, ExecutedCount: 5}
	h := newEngineRunHandler(engine, maclaw.NewEngineRunService(store), nil, nil)
	r := newEngineRunRouter(h, "enterprise")

	// Seed a stored run directly.
	svc := maclaw.NewEngineRunService(store)
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	rec := svc.NewRun(userID, "", "sess-1", "chat", "p", maclaw.EngineJudgeAuto, 5, "t1", []maclaw.EngineCapabilityRef{{ID: "harmful"}}, nil)
	saved, err := svc.Save(context.Background(), rec)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := doJSON(r, http.MethodGet, "/maclaw/engine/runs/"+saved.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["phase"] != "succeeded" {
		t.Fatalf("phase = %v", resp["phase"])
	}
	// Terminal snapshot persisted
	stored, _ := store.Get(context.Background(), userID, saved.ID)
	if stored == nil || stored.Status != maclaw.EnginePhaseSucceeded {
		t.Fatalf("terminal state not persisted: %+v", stored)
	}
}

func TestEngineRunHandlerCancelRun(t *testing.T) {
	store := newFakeRunStore()
	engine := newEnabledFakeEngine()
	h := newEngineRunHandler(engine, maclaw.NewEngineRunService(store), nil, nil)
	r := newEngineRunRouter(h, "enterprise")

	svc := maclaw.NewEngineRunService(store)
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	rec := svc.NewRun(userID, "", "", "wizard", "p", maclaw.EngineJudgeAuto, 5, "t1", []maclaw.EngineCapabilityRef{{ID: "x"}}, nil)
	saved, err := svc.Save(context.Background(), rec)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := doJSON(r, http.MethodPost, "/maclaw/engine/runs/"+saved.ID+"/cancel", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !engine.cancelCalled {
		t.Fatal("engine cancel not called")
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["phase"] != "canceled" {
		t.Fatalf("phase = %v", resp["phase"])
	}
}

func TestEngineRunHandlerListRuns(t *testing.T) {
	store := newFakeRunStore()
	h := newEngineRunHandler(newEnabledFakeEngine(), maclaw.NewEngineRunService(store), nil, nil)
	r := newEngineRunRouter(h, "enterprise")

	svc := maclaw.NewEngineRunService(store)
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	for i := 0; i < 3; i++ {
		rec := svc.NewRun(userID, "", "", "wizard", "p", maclaw.EngineJudgeAuto, 5, "t1", []maclaw.EngineCapabilityRef{{ID: "x"}}, nil)
		if _, err := svc.Save(context.Background(), rec); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	w := doJSON(r, http.MethodGet, "/maclaw/engine/runs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("items = %d", len(resp.Items))
	}
}

func TestEngineRunHandlerGetRunNotFound(t *testing.T) {
	h := newEngineRunHandler(newEnabledFakeEngine(), maclaw.NewEngineRunService(newFakeRunStore()), nil, nil)
	r := newEngineRunRouter(h, "enterprise")

	if w := doJSON(r, http.MethodGet, "/maclaw/engine/runs/er-nonexistent", ""); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestEngineRunHandlerInvalidUser(t *testing.T) {
	store := newFakeRunStore()
	h := newEngineRunHandler(newEnabledFakeEngine(), maclaw.NewEngineRunService(store), nil, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", "not-a-uuid")
		c.Set("user_role", "enterprise")
		c.Next()
	})
	r.POST("/maclaw/engine/runs", h.CreateRun)

	w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", `{"purpose":"p","plugins":[{"id":"x"}]}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
}