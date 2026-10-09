package maclaw

// engine_credential_materializer_test.go — covers the one-shot credential
// mapping from a decrypted target config. Secrets must map verbatim (they are
// consumed in-memory only) and never leave this boundary as logs or DTOs.

import (
	"testing"
)

func TestMaterializeEngineCredentialsNil(t *testing.T) {
	if got := MaterializeEngineCredentialsWithGeneration(nil, nil); got != nil {
		t.Fatalf("nil target must map to nil, got %+v", got)
	}
}

func TestMaterializeEngineCredentialsMapsTarget(t *testing.T) {
	target := &EvaluationTargetInput{
		ID:               "t-1",
		BaseURL:          "  https://target.example/v1  ",
		Model:            "  deepseek-chat  ",
		CredentialSecret: "sk-plain-secret",
		Enabled:          true,
		Metadata:         map[string]string{"X-Org": "acme"},
	}
	creds := MaterializeEngineCredentialsWithGeneration(target, nil)
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.Target.BaseURL != "https://target.example/v1" {
		t.Fatalf("base url not trimmed: %q", creds.Target.BaseURL)
	}
	if creds.Target.Model != "deepseek-chat" {
		t.Fatalf("model not trimmed: %q", creds.Target.Model)
	}
	if creds.Target.APIKey != "sk-plain-secret" {
		t.Fatalf("api key must map verbatim: %q", creds.Target.APIKey)
	}
	if creds.Target.Headers["X-Org"] != "acme" {
		t.Fatalf("metadata headers not forwarded: %+v", creds.Target.Headers)
	}
	if creds.Generation != nil {
		t.Fatalf("generation creds must stay nil when absent: %+v", creds.Generation)
	}
}

func TestMaterializeEngineCredentialsEmptyHeaders(t *testing.T) {
	creds := MaterializeEngineCredentialsWithGeneration(&EvaluationTargetInput{
		BaseURL: "https://t.example/v1",
		Model:   "m",
		Enabled: true,
	}, nil)
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.Target.Headers != nil {
		t.Fatalf("empty metadata must not produce a headers map: %+v", creds.Target.Headers)
	}
}
