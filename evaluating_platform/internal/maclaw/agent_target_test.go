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
		Name:             "Agent",
		Kind:             EvaluationTargetKindAgent,
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
			TargetMetadataAgentEndpoint:     "https://agent.example.com/chat",
			TargetMetadataAgentMethod:       "POST",
			TargetMetadataAgentHeadersTmpl:  `{"X-Api-Key":"{{api_key}}"}`,
			TargetMetadataAgentBodyTmpl:     `{"query":"{{prompt}}"}`,
			TargetMetadataAgentResponsePath: "reply.text",
			"health_url":                    "https://agent.example.com/health",
			"supports_vision":               "false",
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
		Kind:     EvaluationTargetKindLLM,
		BaseURL:  "https://t.example/v1",
		Model:    "m",
		Metadata: map[string]string{"X-Org": "acme", "health_url": "https://t.example/models"},
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
		RunID:  "run_1",
		Prompt: "text",
		Images: []RedteamPayloadImage{{DataBase64: "aGk=", MimeType: "image/png"}},
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

// ---------------------------------------------------------------------------
// U9 · Agent multi-turn ({{conversation_id}})
// ---------------------------------------------------------------------------

// TestAgentTargetWantsConversation pins the predicate the save path and the
// engine agree on: a target is a "conversation target" exactly when the operator
// wrote the placeholder into a request template.
func TestAgentTargetWantsConversation(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]string
		want bool
	}{
		{"body placeholder", map[string]string{TargetMetadataAgentBodyTmpl: `{"conversation_id":"{{conversation_id}}"}`}, true},
		{"header placeholder", map[string]string{TargetMetadataAgentHeadersTmpl: `{"X-Sid":"{{conversation_id}}"}`}, true},
		{"v1 body", map[string]string{TargetMetadataAgentBodyTmpl: `{"query":"{{prompt}}"}`}, false},
		{"no templates", map[string]string{}, false},
		{"empty templates", map[string]string{TargetMetadataAgentBodyTmpl: "", TargetMetadataAgentHeadersTmpl: ""}, false},
		{"other placeholders only", map[string]string{TargetMetadataAgentBodyTmpl: `{"q":"{{prompt}}","k":"{{api_key}}"}`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentTargetWantsConversation(EvaluationTargetInput{
				Kind:     EvaluationTargetKindAgent,
				Metadata: tc.meta,
			})
			if got != tc.want {
				t.Fatalf("AgentTargetWantsConversation = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestValidateAgentTargetInputConversationRequiresResponsePath covers the one
// conversation mistake that is cheap to make and invisible in the results: with
// no extraction path the engine cannot learn the id, so every turn would send an
// empty one and the agent would open a fresh conversation each time — producing a
// report that claims multi-turn. Caught at save time instead.
func TestValidateAgentTargetInputConversationRequiresResponsePath(t *testing.T) {
	withConversation := func() EvaluationTargetInput {
		return EvaluationTargetInput{
			Name: "Dify Agent",
			Kind: EvaluationTargetKindAgent,
			Metadata: map[string]string{
				TargetMetadataAgentEndpoint: "https://dify.example.com/v1/chat-messages",
				TargetMetadataAgentBodyTmpl: `{"query":"{{prompt}}","conversation_id":"{{conversation_id}}"}`,
			},
		}
	}

	noPath := withConversation()
	if err := ValidateAgentTargetInput(noPath); err == nil {
		t.Fatal("expected an error: conversation placeholder with no response path")
	} else if !strings.Contains(err.Error(), "conversation_id") {
		t.Fatalf("error should name the placeholder, got %v", err)
	}

	// With the path supplied it must be accepted — this is the whole supported
	// Dify shape from D-04.
	ok := withConversation()
	ok.Metadata[TargetMetadataAgentResponsePath] = "conversation_id"
	if err := ValidateAgentTargetInput(ok); err != nil {
		t.Fatalf("valid conversation target rejected: %v", err)
	}
}

// TestAgentConversationPlaceholderSurvivesOneShotRendering is the load-bearing
// Go-side property for U9: the BFF renders the *stored template* for the
// one-shot Skill path, and it must NOT invent a conversation id there. The
// placeholder has to pass through untouched so the engine can recognize it.
//
// A BFF that substituted "" for it here would make every engine run look
// single-turn while the operator's config asked for multi-turn.
func TestAgentConversationPlaceholderSurvivesOneShotRendering(t *testing.T) {
	target := EvaluationTargetInput{
		Name: "Dify Agent",
		Kind: EvaluationTargetKindAgent,
		Metadata: map[string]string{
			TargetMetadataAgentEndpoint: "https://dify.example.com/v1/chat-messages",
			TargetMetadataAgentBodyTmpl: `{"query":"{{prompt}}","conversation_id":"{{conversation_id}}"}`,
		},
	}
	spec, err := buildAgentTargetRequestSpec(target, "attack prompt")
	if err != nil {
		t.Fatalf("buildAgentTargetRequestSpec: %v", err)
	}
	body := string(spec.Body)
	if !strings.Contains(body, AgentConversationPlaceholder) {
		t.Fatalf("one-shot rendering consumed the conversation placeholder: %s", body)
	}
	if !strings.Contains(body, "attack prompt") {
		t.Fatalf("prompt not substituted: %s", body)
	}
	// And the rendered body must still be valid JSON — the placeholder is
	// rendered as a JSON string value, not spliced in raw.
	var parsed map[string]interface{}
	if err := json.Unmarshal(spec.Body, &parsed); err != nil {
		t.Fatalf("rendered body is not valid JSON: %v (%s)", err, body)
	}
	if got, ok := parsed["conversation_id"].(string); !ok || got != AgentConversationPlaceholder {
		t.Fatalf("conversation_id = %v, want the literal placeholder", parsed["conversation_id"])
	}
}

// TestAgentTargetSummaryNeverCarriesAConversationValue pins the security
// contract at the exit that produces the browser response: a saved target
// summary echoes the *template* (which may contain the placeholder) but never a
// concrete conversation handle — the summary is built from stored config, and no
// conversation exists yet at save time.
func TestAgentTargetSummaryNeverCarriesAConversationValue(t *testing.T) {
	service := NewTargetConfigService(&memoryTargetConfigStore{}, agentTestKeyStore(t))
	userID := uuid.New()
	summary, err := service.SaveTarget(context.Background(), userID, EvaluationTargetInput{
		Name:             "Dify Agent",
		Kind:             EvaluationTargetKindAgent,
		CredentialSecret: "app-dify-secret",
		Enabled:          true,
		Metadata: map[string]string{
			TargetMetadataAgentEndpoint:     "https://dify.example.com/v1/chat-messages",
			TargetMetadataAgentBodyTmpl:     `{"query":"{{prompt}}","conversation_id":"{{conversation_id}}"}`,
			TargetMetadataAgentResponsePath: "conversation_id",
		},
	})
	if err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	// The summary must still describe a conversation target, i.e. the round trip
	// through encrypted storage preserved the placeholder-bearing template.
	if !strings.Contains(summary.Metadata[TargetMetadataAgentBodyTmpl], AgentConversationPlaceholder) {
		t.Fatalf("saved summary lost the conversation configuration: %v", summary.Metadata)
	}
	raw, _ := json.Marshal(summary)
	if strings.Contains(string(raw), "app-dify-secret") {
		t.Fatalf("summary leaked secret: %s", raw)
	}
	// A concrete handle looks like a uuid/uuid-ish token; the only
	// conversation_id in the payload must be the placeholder itself.
	if strings.Contains(string(raw), `"conversation_id"`) &&
		!strings.Contains(string(raw), AgentConversationPlaceholder) {
		t.Fatalf("summary carries a conversation value rather than the placeholder: %s", raw)
	}
}
