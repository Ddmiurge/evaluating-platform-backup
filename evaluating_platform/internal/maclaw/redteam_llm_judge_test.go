package maclaw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/pkg/config"
)

func TestRuntimeConfigLLMAttackJudgeUsesSafeSummariesOnly(t *testing.T) {
	var requestBody string
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		requestBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"result\":\"success\",\"severity\":\"high\",\"confidence\":0.91,\"reason\":\"LLM judge found unsafe compliance from the safe summary.\",\"matched_rules\":[\"llm_unsafe_compliance\"]}"}}]}`))
	}))
	defer server.Close()

	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	store := &fakeModelDefaultStore{}
	service := NewRuntimeConfigService(store, keyStore)
	if _, _, err := service.SaveDefaultRuntimeConfigForSync(context.Background(), RuntimeAppConfig{
		MaclawLLMProviders: []RuntimeLLMProvider{{
			Name:       "judge",
			URL:        server.URL + "/v1",
			Key:        "sk-judge",
			Model:      "judge-model",
			WireAPI:    "chat_completions",
			TimeoutSec: 2,
		}},
		MaclawLLMCurrentProvider: "judge",
	}, nil); err != nil {
		t.Fatalf("SaveDefaultRuntimeConfigForSync: %v", err)
	}
	judge := NewRuntimeConfigLLMAttackJudge(service)

	got, err := judge.JudgeAttack(context.Background(), JudgeAttackResultInput{
		RunID:           "run_1",
		AttackType:      "jailbreak",
		ResponseSummary: "安全摘要：目标模型给出了绕过限制的步骤。",
		Metadata: map[string]string{
			"payload": "RAW-PAYLOAD-MUST-NOT-LEAK",
			"case":    "classical_chinese",
		},
	}, JudgeAttackResultOutput{Result: "uncertain", Severity: "medium", Confidence: 0.55})
	if err != nil {
		t.Fatalf("JudgeAttack: %v", err)
	}
	if got.Result != "success" || got.Severity != "high" || got.Confidence < 0.9 || got.JudgeMethod != "llm" {
		t.Fatalf("got = %#v", got)
	}
	if authHeader != "Bearer sk-judge" {
		t.Fatalf("auth = %q", authHeader)
	}
	if !strings.Contains(requestBody, "安全摘要") || !strings.Contains(requestBody, "classical_chinese") {
		t.Fatalf("request body missing safe context: %s", requestBody)
	}
	if strings.Contains(requestBody, "RAW-PAYLOAD-MUST-NOT-LEAK") || strings.Contains(requestBody, "payload") || strings.Contains(requestBody, "sk-judge") {
		t.Fatalf("request body leaked unsafe data: %s", requestBody)
	}
	if !strings.Contains(requestBody, `"max_tokens":1024`) {
		t.Fatalf("request body missing judge max_tokens limit: %s", requestBody)
	}
}

func TestParseLLMAttackJudgeOutputAcceptsFirstJSONWhenProviderRepeatsObjects(t *testing.T) {
	got, err := parseLLMAttackJudgeOutput(`{"result":"blocked"}{"result":"blocked"}`)
	if err != nil {
		t.Fatalf("parseLLMAttackJudgeOutput: %v", err)
	}
	if got.Result != "failure" || got.Severity != "medium" || got.Confidence <= 0 {
		t.Fatalf("got = %#v", got)
	}
}

func TestRuntimeConfigLLMAttackJudgeBatchesCasesInSingleProviderCall(t *testing.T) {
	requests := 0
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		requestBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"results\":[{\"result\":\"success\",\"severity\":\"high\",\"confidence\":0.9,\"reason\":\"case one\",\"score_0_to_5\":3,\"refusal_detected\":false},{\"result\":\"failure\",\"severity\":\"info\",\"confidence\":0.9,\"reason\":\"case two\",\"score_0_to_5\":1,\"refusal_detected\":true}]}"}}]}`))
	}))
	defer server.Close()

	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	store := &fakeModelDefaultStore{}
	service := NewRuntimeConfigService(store, keyStore)
	if _, _, err := service.SaveDefaultRuntimeConfigForSync(context.Background(), RuntimeAppConfig{
		MaclawLLMProviders: []RuntimeLLMProvider{{
			Name:       "judge",
			URL:        server.URL + "/v1",
			Key:        "sk-judge",
			Model:      "judge-model",
			WireAPI:    "chat_completions",
			TimeoutSec: 2,
		}},
		MaclawLLMCurrentProvider: "judge",
	}, nil); err != nil {
		t.Fatalf("SaveDefaultRuntimeConfigForSync: %v", err)
	}
	judge := NewRuntimeConfigLLMAttackJudge(service)

	got, err := judge.JudgeAttackBatch(context.Background(), []JudgeAttackResultInput{{
		RunID:           "run_1",
		OriginalPrompt:  "original one",
		TargetResponse:  "target one",
		ResponseSummary: "summary one",
	}, {
		RunID:           "run_1",
		OriginalPrompt:  "original two",
		TargetResponse:  "target two",
		ResponseSummary: "summary two",
	}}, []JudgeAttackResultOutput{{
		Result: "failure", Severity: "medium", Confidence: 0.5, Reason: "rule one",
	}, {
		Result: "failure", Severity: "medium", Confidence: 0.5, Reason: "rule two",
	}})
	if err != nil {
		t.Fatalf("JudgeAttackBatch: %v", err)
	}
	if requests != 1 {
		t.Fatalf("provider requests = %d, want 1", requests)
	}
	if len(got) != 2 || got[0].Result != "success" || got[0].RubricScore != 3 || got[1].Result != "failure" || !got[1].RefusalDetected {
		t.Fatalf("got = %#v", got)
	}
	if !strings.Contains(requestBody, "cases") || !strings.Contains(requestBody, "original one") || !strings.Contains(requestBody, "summary two") {
		t.Fatalf("request body missing batch case context: %s", requestBody)
	}
	if strings.Contains(requestBody, "sk-judge") {
		t.Fatalf("request body leaked key: %s", requestBody)
	}
}

func TestRuntimeConfigLLMAttackJudgeAppliesProviderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"result\":\"blocked\"}"}}]}`))
	}))
	defer server.Close()

	judge := NewRuntimeConfigLLMAttackJudge(nil)
	started := time.Now()
	_, err := judge.callProvider(context.Background(), RuntimeLLMProvider{
		URL:        server.URL + "/v1",
		Model:      "judge-model",
		WireAPI:    "chat_completions",
		TimeoutSec: 1,
	}, "{}")

	if err == nil {
		t.Fatalf("callProvider returned nil error, want timeout")
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("callProvider elapsed %s, want provider timeout to stop before upstream response", elapsed)
	}
}

func TestRuntimeConfigLLMAttackJudgeLimitsResponsesOutputTokens(t *testing.T) {
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		requestBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"{\"result\":\"failure\",\"severity\":\"info\",\"confidence\":0.9,\"reason\":\"safe refusal\",\"score_0_to_5\":0,\"refusal_detected\":true}"}`))
	}))
	defer server.Close()

	judge := NewRuntimeConfigLLMAttackJudge(nil)
	got, err := judge.callProvider(context.Background(), RuntimeLLMProvider{
		URL:        server.URL + "/v1",
		Model:      "judge-model",
		WireAPI:    "responses",
		Key:        "sk-test",
		TimeoutSec: 2,
	}, "{}")
	if err != nil {
		t.Fatalf("callProvider: %v", err)
	}
	if !strings.Contains(got, `"result":"failure"`) {
		t.Fatalf("got = %s", got)
	}
	if !strings.Contains(requestBody, `"max_output_tokens":1024`) {
		t.Fatalf("request body missing judge max_output_tokens limit: %s", requestBody)
	}
}
