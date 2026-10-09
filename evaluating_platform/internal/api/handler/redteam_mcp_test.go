package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/model"
	"evaluating_platform/pkg/config"
)

type handlerRedteamCapabilitySearcher struct {
	cards []maclaw.CapabilityCard
	query maclaw.CapabilityCatalogQuery
}

func (s *handlerRedteamCapabilitySearcher) Search(_ context.Context, q maclaw.CapabilityCatalogQuery) ([]maclaw.CapabilityCard, error) {
	s.query = q
	return append([]maclaw.CapabilityCard(nil), s.cards...), nil
}

type handlerBatchPayloadProvider struct{}

func (p handlerBatchPayloadProvider) LoadSamplePayloads(_ context.Context, ref string, limit int) ([]model.AttackPayload, error) {
	out := make([]model.AttackPayload, 0, limit)
	for i := 1; i <= limit; i++ {
		out = append(out, model.AttackPayload{Index: i, Data: "handler payload"})
	}
	return out, nil
}

func (p handlerBatchPayloadProvider) LoadComposedPayloads(ctx context.Context, ref string, limit int) ([]model.AttackPayload, error) {
	return p.LoadSamplePayloads(ctx, ref, limit)
}

func (p handlerBatchPayloadProvider) GetTemplate(_ context.Context, ref string) (*model.Template, error) {
	return &model.Template{ID: uuid.New(), Name: "Template", Content: "{{sample}}"}, nil
}

func TestRedteamMCPHandlerRequiresBearerSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bridge := maclaw.NewRedteamToolBridge(&handlerRedteamCapabilitySearcher{}, nil, nil)
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/maclaw/redteam-mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	handler.Handle(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRedteamMCPHandlerListsAndCallsSafePlatformTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searcher := &handlerRedteamCapabilitySearcher{cards: []maclaw.CapabilityCard{{
		SourceType:   maclaw.CapabilitySourceResource,
		SourceRef:    "resource_classical_chinese_samples",
		Name:         "Classical Chinese jailbreak samples",
		Summary:      "Expert sample resource for classical Chinese jailbreak probes.",
		RiskTypes:    []string{"jailbreak"},
		TargetTypes:  []string{"llm"},
		Languages:    []string{"classical_chinese"},
		Enabled:      true,
		Status:       "published",
		SafeMetadata: map[string]string{"payload": "must-not-leak", "owner": "expert"},
	}}}
	bridge := maclaw.NewRedteamToolBridge(searcher, nil, nil)
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")

	list := performRedteamMCPRequest(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if !strings.Contains(list, "search_platform_redteam_capabilities") || !strings.Contains(list, "search_redteam_capabilities") || !strings.Contains(list, "compose_redteam_payloads") || !strings.Contains(list, "register_skill_payload_dataset") || !strings.Contains(list, "judge_attack_result") || !strings.Contains(list, "compile_redteam_report") || !strings.Contains(list, "execute_redteam_evaluation_batch") {
		t.Fatalf("tools/list response missing expected tools: %s", list)
	}
	for _, want := range []string{"query", "risk_types", "target_types", "languages", "limit", "capability_ref", "capability_refs", "sample_refs", "template_refs", "composed_attack_refs", "payload_handle", "response_summary", "attack_type", "judge_mode", "run_id", "evidence_handles"} {
		if !strings.Contains(list, want) {
			t.Fatalf("tools/list schema missing %q: %s", want, list)
		}
	}

	search := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_platform_redteam_capabilities","arguments":{"query":"classical chinese jailbreak","limit":5}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": uuid.NewString(),
		"X-Evaluating-Platform-Role":    "enterprise",
	})
	if !strings.Contains(search, "resource_classical_chinese_samples") {
		t.Fatalf("search response missing resource card: %s", search)
	}
	if strings.Contains(search, "must-not-leak") {
		t.Fatalf("search response leaked unsafe metadata: %s", search)
	}
	if searcher.query.Query != "classical chinese jailbreak" {
		t.Fatalf("query = %q", searcher.query.Query)
	}

	prepare := performRedteamMCPRequest(t, handler, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"prepare_redteam_capability","arguments":{"capability_refs":[" resource_classical_chinese_samples ","resource_classical_chinese_samples"]}}}`)
	if !strings.Contains(prepare, "resource_classical_chinese_samples") || !strings.Contains(prepare, "mcp_context_only") {
		t.Fatalf("prepare response = %s", prepare)
	}

	userID := uuid.NewString()
	grant := mintRedteamExecutionGrant("mcp-secret", userID, "sess_1", time.Now().Add(time.Hour))
	judge := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"judge_attack_result","arguments":{"run_id":"run_1","attack_type":"jailbreak","response_summary":"模型提供了绕过安全限制的步骤摘要","metadata":{"session_id":"sess_1","credential_secret":"must-strip","attack_success":"true","evaluation_execution_grant":"`+grant+`"}}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": userID,
	})
	if !strings.Contains(judge, `\"result\":\"success\"`) || !strings.Contains(judge, `\"judge_method\":\"rules\"`) {
		t.Fatalf("judge response = %s", judge)
	}
	if strings.Contains(judge, "credential_secret") || strings.Contains(judge, "must-strip") {
		t.Fatalf("judge response leaked sensitive metadata: %s", judge)
	}
}

func TestRedteamMCPHandlerRegistersSkillPayloadDatasetWithGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bridge := maclaw.NewRedteamToolBridge(nil, nil, nil)
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")
	userID := uuid.NewString()
	grant := mintRedteamExecutionGrant("mcp-secret", userID, "sess_skill", time.Now().Add(time.Hour), 10)

	body := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"register_skill_payload_dataset","arguments":{"run_id":"run_skill","session_id":"sess_skill","skill_name":"ccbos-classical-chinese-skill","payload_dataset":{"payloads":[{"source_sample_id":"sample:abc","original_question":"original sample question","payload_text":"rewritten classical Chinese payload"}]},"metadata":{"evaluation_execution_grant":"`+grant+`","payload":"must-strip"}}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": userID,
		"X-Evaluating-Platform-Role":    "enterprise",
	})
	if !strings.Contains(body, `\"payload_count\":1`) || !strings.Contains(body, "redteam_payload_") {
		t.Fatalf("register skill payload response = %s", body)
	}
	if strings.Contains(body, "rewritten classical Chinese payload") || strings.Contains(body, "must-strip") || strings.Contains(body, grant) {
		t.Fatalf("register skill payload response leaked sensitive data: %s", body)
	}
}

func TestRedteamMCPHandlerPreparesSkillInputFromGrantSelectedRefs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bridge := maclaw.NewRedteamToolBridge(nil, nil, nil)
	bridge.SetPayloadProvider(handlerBatchPayloadProvider{})
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")
	userID := uuid.NewString()
	sampleRef := "sample:" + uuid.NewString()
	grant := mintRedteamExecutionGrantWithContext("mcp-secret", userID, "sess_skill", time.Now().Add(time.Hour), redteamExecutionGrantContext{
		TestCount:              7,
		SelectedCapabilityRefs: []string{"skillhub:ccbos-classical-chinese-skill", sampleRef},
		SelectedSkillNames:     []string{"ccbos-classical-chinese-skill"},
	})

	body := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"prepare_skill_input_data","arguments":{"run_id":"run_skill","session_id":"sess_skill","metadata":{"evaluation_execution_grant":"`+grant+`"}}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": userID,
		"X-Evaluating-Platform-Role":    "enterprise",
	})
	if !strings.Contains(body, `\"count\":7`) || !strings.Contains(body, sampleRef+"#1") {
		t.Fatalf("prepare skill input response = %s", body)
	}
	if strings.Contains(body, grant) || strings.Contains(body, "ccbos-classical-chinese-skill") {
		t.Fatalf("prepare skill input response leaked grant context: %s", body)
	}
}

func TestRedteamMCPHandlerExecutesBatchToolWithGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"I cannot help with that request."}}]}`))
	}))
	defer targetServer.Close()

	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	store := &handlerTargetConfigStore{}
	targets := maclaw.NewTargetConfigService(store, keyStore)
	userID := uuid.New()
	if _, err := targets.SaveTarget(context.Background(), userID, maclaw.EvaluationTargetInput{
		Name:             "Target",
		Kind:             maclaw.EvaluationTargetKindLLM,
		BaseURL:          targetServer.URL + "/v1",
		Model:            "gpt-test",
		AuthType:         maclaw.EvaluationTargetAuthTypeBearer,
		CredentialSecret: "sk-handler",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}

	bridge := maclaw.NewRedteamToolBridge(nil, nil, nil)
	bridge.SetTargetConfigService(targets)
	bridge.SetPayloadProvider(handlerBatchPayloadProvider{})
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")
	grant := mintRedteamExecutionGrant("mcp-secret", userID.String(), "sess_batch", time.Now().Add(time.Hour), 10)
	body := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"execute_redteam_evaluation_batch","arguments":{"run_id":"run_batch","session_id":"sess_batch","test_count":3,"sample_refs":["sample:abc"],"metadata":{"evaluation_execution_grant":"`+grant+`","payload":"must-strip"}}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": userID.String(),
		"X-Evaluating-Platform-Role":    "enterprise",
	})
	if !strings.Contains(body, `\"executed_count\":10`) || !strings.Contains(body, `\"planned_count\":10`) || !strings.Contains(body, `\"report_id\"`) || !strings.Contains(body, `\"failure\":10`) {
		t.Fatalf("batch response = %s", body)
	}
	if strings.Contains(body, "handler payload") || strings.Contains(body, "sk-handler") || strings.Contains(body, "must-strip") || strings.Contains(body, grant) {
		t.Fatalf("batch response leaked sensitive data: %s", body)
	}
}

func TestRedteamMCPHandlerRejectsExpertCatalogSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searcher := &handlerRedteamCapabilitySearcher{cards: []maclaw.CapabilityCard{{
		SourceType: maclaw.CapabilitySourceSample,
		SourceRef:  "sample:other-expert",
		Name:       "Other expert sample",
		Enabled:    true,
		Status:     "published",
	}}}
	bridge := maclaw.NewRedteamToolBridge(searcher, nil, nil)
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")

	body := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"search_platform_redteam_capabilities","arguments":{"query":"jailbreak","limit":5}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": uuid.NewString(),
		"X-Evaluating-Platform-Role":    "expert",
	})
	if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "enterprise") {
		t.Fatalf("expert catalog search should be rejected, body = %s", body)
	}
	if searcher.query.Query != "" {
		t.Fatalf("catalog search was executed for expert role: %#v", searcher.query)
	}
}

func TestRedteamMCPHandlerPassesPlatformUserHeaderToTargetTool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var auth string
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer targetServer.Close()

	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	store := &handlerTargetConfigStore{}
	targets := maclaw.NewTargetConfigService(store, keyStore)
	userID := uuid.New()
	if _, err := targets.SaveTarget(context.Background(), userID, maclaw.EvaluationTargetInput{
		Name:             "Target",
		Kind:             maclaw.EvaluationTargetKindLLM,
		BaseURL:          targetServer.URL + "/v1",
		Model:            "gpt-test",
		AuthType:         maclaw.EvaluationTargetAuthTypeBearer,
		CredentialSecret: "sk-handler",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	bridge := maclaw.NewRedteamToolBridge(nil, nil, nil)
	bridge.SetTargetConfigService(targets)
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")

	grant := mintRedteamExecutionGrant("mcp-secret", userID.String(), "sess_1", time.Now().Add(time.Hour))
	body := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"call_evaluation_target","arguments":{"run_id":"run_1","prompt":"hello target","metadata":{"session_id":"sess_1","credential_secret":"must-strip","case":"smoke","evaluation_execution_grant":"`+grant+`"}}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": userID.String(),
	})
	if auth != "Bearer sk-handler" {
		t.Fatalf("auth = %q", auth)
	}
	if strings.Contains(body, "sk-handler") || strings.Contains(body, "credential_secret") || strings.Contains(body, "hello target") || strings.Contains(body, grant) || strings.Contains(body, "evaluation_execution_grant") {
		t.Fatalf("target call response leaked sensitive data: %s", body)
	}
	if !strings.Contains(body, `\"status\":\"called\"`) || !strings.Contains(body, "response_sha256") || !strings.Contains(body, `\"case\":\"smoke\"`) {
		t.Fatalf("target call response = %s", body)
	}
}

func TestRedteamMCPHandlerRejectsExecutionToolsWithoutGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bridge := maclaw.NewRedteamToolBridge(nil, nil, nil)
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")

	body := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"call_evaluation_target","arguments":{"run_id":"run_1","prompt":"hello target"}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": uuid.NewString(),
	})
	if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "execution grant") {
		t.Fatalf("execution tool should require grant, body = %s", body)
	}
}

func TestRedteamMCPHandlerUsesSignedGrantSessionWhenToolSessionDrifts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bridge := maclaw.NewRedteamToolBridge(nil, nil, nil)
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")
	userID := uuid.NewString()
	grant := mintRedteamExecutionGrant("mcp-secret", userID, "sess_1", time.Now().Add(time.Hour))

	body := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"call_evaluation_target","arguments":{"run_id":"run_1","prompt":"hello target","metadata":{"session_id":"sess_other","evaluation_execution_grant":"`+grant+`"}}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": userID,
	})
	if strings.Contains(body, `"isError":true`) || strings.Contains(body, "execution grant session mismatch") {
		t.Fatalf("execution tool should trust signed grant session instead of model-supplied session, body = %s", body)
	}
	if !strings.Contains(body, `\"status\":\"prepared\"`) || !strings.Contains(body, `\"session_id\":\"sess_1\"`) {
		t.Fatalf("execution tool should continue with the signed grant session, body = %s", body)
	}
}

type handlerTargetConfigStore struct {
	record *maclaw.TargetConfigRecord
}

func (s *handlerTargetConfigStore) GetByUserID(_ context.Context, userID uuid.UUID) (*maclaw.TargetConfigRecord, error) {
	if s.record == nil || s.record.UserID != userID {
		return nil, nil
	}
	copy := *s.record
	return &copy, nil
}

func (s *handlerTargetConfigStore) Upsert(_ context.Context, record maclaw.TargetConfigRecord) (*maclaw.TargetConfigRecord, error) {
	if record.ID == uuid.Nil {
		record.ID = uuid.New()
	}
	s.record = &record
	copy := record
	return &copy, nil
}

func performRedteamMCPRequest(t *testing.T, handler *RedteamMCPHandler, body string) string {
	return performRedteamMCPRequestWithHeaders(t, handler, body, nil)
}

func performRedteamMCPRequestWithHeaders(t *testing.T, handler *RedteamMCPHandler, body string, headers map[string]string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/maclaw/redteam-mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer mcp-secret")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	handler.Handle(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json response: %v body=%s", err, w.Body.String())
	}
	return w.Body.String()
}

func TestRedteamMCPHandlerRegistersPromptfooEngineTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bridge := maclaw.NewRedteamToolBridge(&handlerRedteamCapabilitySearcher{}, nil, nil)
	handler := NewRedteamMCPHandler(bridge, "mcp-secret")

	// tools/list must advertise the three Phase-1 engine tools.
	list := performRedteamMCPRequest(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, want := range []string{"search_redteam_plugin_catalog", "run_promptfoo_redteam_evaluation", "get_promptfoo_evaluation_result"} {
		if !strings.Contains(list, want) {
			t.Fatalf("tools/list missing %q: %s", want, list)
		}
	}

	// Catalog search works without engine wiring and returns safe cards.
	search := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_redteam_plugin_catalog","arguments":{"query":"有害内容","limit":5}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": uuid.NewString(),
		"X-Evaluating-Platform-Role":    "enterprise",
	})
	if !strings.Contains(search, "promptfoo_plugin:harmful") || !strings.Contains(search, `\"engine\":\"promptfoo\"`) {
		t.Fatalf("catalog search response = %s", search)
	}

	// Catalog search is rejected for expert roles.
	rejected := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_redteam_plugin_catalog","arguments":{"query":"x"}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": uuid.NewString(),
		"X-Evaluating-Platform-Role":    "expert",
	})
	if !strings.Contains(rejected, "enterprise or admin") {
		t.Fatalf("expert role should be rejected: %s", rejected)
	}

	// run_promptfoo_redteam_evaluation requires an execution grant.
	noGrant := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"run_promptfoo_redteam_evaluation","arguments":{"run_id":"r1","purpose":"p"}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": uuid.NewString(),
		"X-Evaluating-Platform-Role":    "enterprise",
	})
	if !strings.Contains(noGrant, "grant") {
		t.Fatalf("run tool without grant should fail: %s", noGrant)
	}

	// get_promptfoo_evaluation_result requires enterprise/admin role.
	expert := performRedteamMCPRequestWithHeaders(t, handler, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_promptfoo_evaluation_result","arguments":{"engine_run_id":"er-x"}}}`, map[string]string{
		"X-Evaluating-Platform-User-ID": uuid.NewString(),
		"X-Evaluating-Platform-Role":    "expert",
	})
	if !strings.Contains(expert, "enterprise or admin") {
		t.Fatalf("expert role should be rejected: %s", expert)
	}
}
