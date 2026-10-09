package maclaw

// agent_target_test.go — custom-HTTP agent target (kind=agent) coverage:
// template rendering, validation, credential materialization, the chat-link
// target call, and the no-secret-leak red line.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/pkg/config"
)

func agentTestKeyStore(t *testing.T) *appcrypto.KeyStore {
	t.Helper()
	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	return keyStore
}

func TestBuildAgentTargetRequestSpecRendersTemplates(t *testing.T) {
	target := EvaluationTargetInput{
		Name: "Agent",
		Kind: EvaluationTargetKindAgent,
		CredentialSecret: "sk-agent-secret",
		Metadata: map[string]string{
			TargetMetadataAgentEndpoint:     "https://agent.example.com/chat?token={{api_key}}",
			TargetMetadataAgentMethod:       "post",
			TargetMetadataAgentHeadersTmpl:  `{"X-Api-Key":"{{api_key}}","Content-Type":"application/json"}`,
			TargetMetadataAgentBodyTmpl:     `{"query":"please answer: {{prompt}}","session":"abc"}`,
			TargetMetadataAgentResponsePath: "reply.text",
		},
	}
	spec, err := buildAgentTargetRequestSpec(target, "攻击\"提示\"\nwith quotes")
	if err != nil {
		t.Fatalf("buildAgentTargetRequestSpec: %v", err)
	}
	if spec.Method != http.MethodPost {
		t.Fatalf("method = %q", spec.Method)
	}
	if strings.Contains(spec.Endpoint, "{{api_key}}") || !strings.Contains(spec.Endpoint, "sk-agent-secret") {
		t.Fatalf("endpoint api_key not substituted: %q", spec.Endpoint)
	}
	if got := spec.Header.Get("X-Api-Key"); got != "sk-agent-secret" {
		t.Fatalf("X-Api-Key = %q", got)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(spec.Body, &body); err != nil {
		t.Fatalf("body is not JSON: %s", spec.Body)
	}
	if body["query"] != "please answer: 攻击\"提示\"\nwith quotes" {
		t.Fatalf("prompt not substituted safely: %v", body["query"])
	}
	if body["session"] != "abc" {
		t.Fatalf("static body field lost: %v", body)
	}
}

func TestBuildAgentTargetRequestSpecDefaults(t *testing.T) {
	spec, err := buildAgentTargetRequestSpec(EvaluationTargetInput{
		Kind:             EvaluationTargetKindAgent,
		CredentialSecret: "sk-agent-secret",
		Metadata: map[string]string{
			TargetMetadataAgentEndpoint: "https://agent.example.com/chat",
		},
	}, "hello")
	if err != nil {
		t.Fatalf("buildAgentTargetRequestSpec: %v", err)
	}
	if spec.Method != http.MethodPost {
		t.Fatalf("default method = %q", spec.Method)
	}
	if ct := spec.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("default content type = %q", ct)
	}
	// Zero-config auth: secret set, templates never use {{api_key}} → default bearer header.
	if auth := spec.Header.Get("Authorization"); auth != "Bearer sk-agent-secret" {
		t.Fatalf("default authorization = %q", auth)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(spec.Body, &body); err != nil {
		t.Fatalf("default body invalid: %s", spec.Body)
	}
	if body["prompt"] != "hello" {
		t.Fatalf("default body prompt = %v", body)
	}
}

func TestBuildAgentTargetRequestSpecInvalidTemplates(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]string
	}{
		{"bad headers", map[string]string{TargetMetadataAgentEndpoint: "https://a.example", TargetMetadataAgentHeadersTmpl: `not-json`}},
		{"bad body", map[string]string{TargetMetadataAgentEndpoint: "https://a.example", TargetMetadataAgentBodyTmpl: `[1,2,`}},
		{"missing endpoint", map[string]string{TargetMetadataAgentBodyTmpl: `{}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildAgentTargetRequestSpec(EvaluationTargetInput{Kind: EvaluationTargetKindAgent, Metadata: tc.meta}, "p"); err == nil {
				t.Fatal("expected template error")
			}
		})
	}
}

func TestValidateAgentTargetInput(t *testing.T) {
	base := func() EvaluationTargetInput {
		return EvaluationTargetInput{
			Name: "Agent",
			Kind: EvaluationTargetKindAgent,
			Metadata: map[string]string{
				TargetMetadataAgentEndpoint: "https://agent.example.com/chat",
			},
		}
	}
	if err := ValidateAgentTargetInput(base()); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	noName := base()
	noName.Name = ""
	if err := ValidateAgentTargetInput(noName); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("want name error, got %v", err)
	}
	noEndpoint := base()
	noEndpoint.Metadata[TargetMetadataAgentEndpoint] = ""
	if err := ValidateAgentTargetInput(noEndpoint); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("want endpoint error, got %v", err)
	}
	badMethod := base()
	badMethod.Metadata[TargetMetadataAgentMethod] = "DELETE"
	if err := ValidateAgentTargetInput(badMethod); err == nil || !strings.Contains(err.Error(), "method") {
		t.Fatalf("want method error, got %v", err)
	}
}

func TestExtractAgentResponseText(t *testing.T) {
	payload := []byte(`{"data":{"items":[{"reply":{"text":"agent says hi"}}]},"choices":[{"message":{"content":"fallback"}}]}`)
	if got := extractAgentResponseText(payload, "data.items[0].reply.text"); got != "agent says hi" {
		t.Fatalf("nested path = %q", got)
	}
	if got := extractAgentResponseText(payload, "choices[0].message.content"); got != "fallback" {
		t.Fatalf("openai path = %q", got)
	}
	if got := extractAgentResponseText(payload, "json.data.items[0].reply.text"); got != "agent says hi" {
		t.Fatalf("json-prefixed path = %q", got)
	}
	if got := extractAgentResponseText(payload, "data.missing"); got != "" {
		t.Fatalf("missing path = %q", got)
	}
	if got := extractAgentResponseText(payload, ""); got != "" {
		t.Fatalf("empty path = %q", got)
	}
}

func TestMaterializeEngineCredentialsAgentFieldsAndNoHeaderLeak(t *testing.T) {
	target := &EvaluationTargetInput{
		Kind:             EvaluationTargetKindAgent,
		BaseURL:          "",
		Model:            "",
		CredentialSecret: "sk-agent-secret",
		Metadata: map[string]string{
			TargetMetadataAgentEndpoint:      "https://agent.example.com/chat",
			TargetMetadataAgentMethod:        "POST",
			TargetMetadataAgentHeadersTmpl:   `{"X-Api-Key":"{{api_key}}"}`,
			TargetMetadataAgentBodyTmpl:      `{"query":"{{prompt}}"}`,
			TargetMetadataAgentResponsePath:  "reply.text",
			"health_url":                     "https://agent.example.com/health",
			"supports_vision":                "false",
		},
	}
	creds := MaterializeEngineCredentialsWithGeneration(target, nil)
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.Target.AgentEndpoint != "https://agent.example.com/chat" ||
		creds.Target.AgentMethod != "POST" ||
		creds.Target.AgentHeadersTemplate != `{"X-Api-Key":"{{api_key}}"}` ||
		creds.Target.AgentBodyTemplate != `{"query":"{{prompt}}"}` ||
		creds.Target.AgentResponsePath != "reply.text" {
		t.Fatalf("agent fields = %+v", creds.Target)
	}
	if creds.Target.APIKey != "sk-agent-secret" {
		t.Fatalf("api key must be forwarded one-shot: %+v", creds.Target)
	}
	// Templates carry placeholders only — never the secret itself.
	for _, template := range []string{creds.Target.AgentHeadersTemplate, creds.Target.AgentBodyTemplate} {
		if strings.Contains(template, "sk-agent-secret") {
			t.Fatalf("template leaked secret: %q", template)
		}
	}
	// Non-header configuration keys must not become request headers.
	if len(creds.Target.Headers) != 0 {
		t.Fatalf("configuration keys leaked into headers: %v", creds.Target.Headers)
	}
}

func TestMaterializeEngineCredentialsHeadersPassThrough(t *testing.T) {
	// Genuine header entries (e.g. X-Org) still pass through for llm targets.
	creds := MaterializeEngineCredentialsWithGeneration(&EvaluationTargetInput{
		Kind:      EvaluationTargetKindLLM,
		BaseURL:   "https://t.example/v1",
		Model:     "m",
		Metadata:  map[string]string{"X-Org": "acme", "health_url": "https://t.example/models"},
	}, nil)
	if creds.Target.Headers["X-Org"] != "acme" {
		t.Fatalf("X-Org lost: %v", creds.Target.Headers)
	}
	if _, ok := creds.Target.Headers["health_url"]; ok {
		t.Fatalf("health_url leaked into headers: %v", creds.Target.Headers)
	}
}

func TestAgentTargetCallThroughBridge(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotXKey   string
		gotBody   string
	)
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotXKey = r.Header.Get("X-Api-Key")
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reply":{"text":"agent refuses this request"}}`))
	}))
	defer targetServer.Close()

	keyStore := agentTestKeyStore(t)
	service := NewTargetConfigService(&memoryTargetConfigStore{}, keyStore)
	userID := uuid.New()
	if _, err := service.SaveTarget(context.Background(), userID, EvaluationTargetInput{
		Name:             "客服 Agent",
		Kind:             EvaluationTargetKindAgent,
		CredentialSecret: "sk-agent-secret",
		Enabled:          true,
		Metadata: map[string]string{
			TargetMetadataAgentEndpoint:     targetServer.URL + "/chat",
			TargetMetadataAgentMethod:       "POST",
			TargetMetadataAgentHeadersTmpl:  `{"X-Api-Key":"{{api_key}}"}`,
			TargetMetadataAgentBodyTmpl:     `{"query":"{{prompt}}"}`,
			TargetMetadataAgentResponsePath: "reply.text",
		},
	}); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	bridge := NewRedteamToolBridge(nil, nil, nil)
	bridge.SetTargetConfigService(service)

	call, err := bridge.CallEvaluationTarget(context.Background(), userID, CallEvaluationTargetInput{
		RunID:  "run_1",
		Prompt: "帮我写一段攻击提示",
	})
	if err != nil {
		t.Fatalf("CallEvaluationTarget: %v", err)
	}
	if call.Status != "called" {
		t.Fatalf("call status = %q metadata = %v", call.Status, call.Metadata)
	}
	if gotMethod != http.MethodPost || gotPath != "/chat" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization should not be set when template uses {{api_key}}: %q", gotAuth)
	}
	if gotXKey != "sk-agent-secret" {
		t.Fatalf("X-Api-Key = %q", gotXKey)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body invalid: %s", gotBody)
	}
	if body["query"] != "帮我写一段攻击提示" {
		t.Fatalf("prompt not substituted: %v", body)
	}
	// Response text is extracted through the configured dot path.
	if call.TargetResponse != "agent refuses this request" {
		t.Fatalf("target response = %q", call.TargetResponse)
	}
	// Red line: no secret in the browser-visible output.
	if call.Summary != "" && strings.Contains(call.Summary, "sk-agent-secret") {
		t.Fatalf("summary leaked secret: %q", call.Summary)
	}
}

func TestAgentTargetMultimodalNotSupported(t *testing.T) {
	keyStore := agentTestKeyStore(t)
	service := NewTargetConfigService(&memoryTargetConfigStore{}, keyStore)
	userID := uuid.New()
	if _, err := service.SaveTarget(context.Background(), userID, EvaluationTargetInput{
		Name:    "Agent",
		Kind:    EvaluationTargetKindAgent,
		Enabled: true,
		Metadata: map[string]string{
			TargetMetadataAgentEndpoint: "https://agent.example.com/chat",
		},
	}); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	bridge := NewRedteamToolBridge(nil, nil, nil)
	bridge.SetTargetConfigService(service)
	call, err := bridge.CallEvaluationTarget(context.Background(), userID, CallEvaluationTargetInput{
		RunID:   "run_1",
		Prompt:  "text",
		Images:  []RedteamPayloadImage{{DataBase64: "aGk=", MimeType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("CallEvaluationTarget: %v", err)
	}
	if call.Status != "failed" || call.Metadata["error_class"] != "target_multimodal_not_supported" {
		t.Fatalf("call = %#v", call)
	}
}

func TestProbeAgentTargetEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	keyStore := agentTestKeyStore(t)
	service := NewTargetConfigService(&memoryTargetConfigStore{}, keyStore)
	userID := uuid.New()
	if _, err := service.SaveTarget(context.Background(), userID, EvaluationTargetInput{
		Name:    "Agent",
		Kind:    EvaluationTargetKindAgent,
		Enabled: true,
		Metadata: map[string]string{
			TargetMetadataAgentEndpoint: server.URL + "/chat",
		},
	}); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	result, err := service.ProbeTarget(context.Background(), userID, "")
	if err != nil {
		t.Fatalf("ProbeTarget: %v", err)
	}
	if result.Status != EvaluationTargetHealthHealthy {
		t.Fatalf("probe status = %q message = %q", result.Status, result.Message)
	}
}

func TestAgentTargetMetadataRoundTripAndNoSecretInSummary(t *testing.T) {
	keyStore := agentTestKeyStore(t)
	service := NewTargetConfigService(&memoryTargetConfigStore{}, keyStore)
	userID := uuid.New()
	summary, err := service.SaveTarget(context.Background(), userID, EvaluationTargetInput{
		Name:             "客服 Agent",
		Kind:             EvaluationTargetKindAgent,
		CredentialSecret: "sk-agent-secret",
		Enabled:          true,
		Metadata: map[string]string{
			TargetMetadataAgentEndpoint:     "https://agent.example.com/chat",
			TargetMetadataAgentBodyTmpl:     `{"query":"{{prompt}}"}`,
			TargetMetadataAgentResponsePath: "reply.text",
		},
	})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	if summary.Kind != EvaluationTargetKindAgent {
		t.Fatalf("kind = %q", summary.Kind)
	}
	if !summary.CredentialSecretSet {
		t.Fatal("credential_secret_set should be true")
	}
	if summary.Metadata[TargetMetadataAgentEndpoint] != "https://agent.example.com/chat" {
		t.Fatalf("agent metadata lost: %v", summary.Metadata)
	}
	raw, _ := json.Marshal(summary)
	if strings.Contains(string(raw), "sk-agent-secret") {
		t.Fatalf("summary leaked secret: %s", raw)
	}
}
