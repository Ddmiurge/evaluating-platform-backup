package maclaw

// engine_credential_materializer.go — server-side one-shot credential
// materialization for promptfoo-engine runs (Phase 0; plan P0-12).
//
// Flow: decrypt the enterprise target config (handled by
// TargetConfigService.GetTargetWithSecret) → map to the engine's one-shot
// credential DTO → hand it to CreateRun. The credentials live only in
// request memory: never logged, never persisted, never returned to the
// browser.

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// EngineTargetProvider resolves the decrypted target config for a user.
// Satisfied by *TargetConfigService.
type EngineTargetProvider interface {
	GetTargetWithSecret(ctx context.Context, userID uuid.UUID) (*EvaluationTargetInput, error)
}

// MaterializeEngineCredentialsWithGeneration maps a decrypted target config
// plus an optional generation LLM credential to the engine's one-shot
// credential payload. Never logs the secret. generation may be nil, in which
// case the engine run has no attack-case generator (Phase 0 behavior).
func MaterializeEngineCredentialsWithGeneration(target *EvaluationTargetInput, generation *EngineGenCredential) *EngineOneShotCredentials {
	if target == nil {
		return nil
	}
	return &EngineOneShotCredentials{
		Target: EngineTargetCredential{
			BaseURL: strings.TrimSpace(target.BaseURL),
			Model:   strings.TrimSpace(target.Model),
			APIKey:  target.CredentialSecret,
			// Only genuine header entries pass through; configuration keys
			// (agent templates, health_url, supports_vision) must not be sent
			// to the target as garbage headers.
			Headers: copyEngineHeaders(engineTargetRequestHeaders(target.Metadata)),
			// Agent targets: forward the custom HTTP template verbatim (templates
				// contain only placeholders, never the secret itself).
			AgentEndpoint:        strings.TrimSpace(target.Metadata[TargetMetadataAgentEndpoint]),
			AgentMethod:          strings.TrimSpace(target.Metadata[TargetMetadataAgentMethod]),
			AgentHeadersTemplate: strings.TrimSpace(target.Metadata[TargetMetadataAgentHeadersTmpl]),
			AgentBodyTemplate:    strings.TrimSpace(target.Metadata[TargetMetadataAgentBodyTmpl]),
			AgentResponsePath:    strings.TrimSpace(target.Metadata[TargetMetadataAgentResponsePath]),
		},
		Generation: generation,
	}
}

// engineTargetRequestHeaders filters target metadata down to entries that
// are safe to forward as HTTP headers to the engine target. Configuration
// keys used by the platform (agent templates, health_url, supports_vision)
// are excluded.
func engineTargetRequestHeaders(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		switch strings.TrimSpace(key) {
		case TargetMetadataAgentEndpoint,
			TargetMetadataAgentMethod,
			TargetMetadataAgentHeadersTmpl,
			TargetMetadataAgentBodyTmpl,
			TargetMetadataAgentResponsePath,
			"health_url",
			"supports_vision":
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// EngineGenerationProvider resolves the platform default LLM (the same model
// the MaClaw/Skill attack-generation path uses) into a one-shot generation
// credential for the promptfoo engine. Satisfied by *RuntimeConfigService.
type EngineGenerationProvider interface {
	GetDefaultRuntimeConfig(ctx context.Context) (*RuntimeAppConfig, error)
}

// ResolveEngineGenerationCredential resolves the current default LLM provider
// from the platform default model config and maps it to the engine's
// generation credential. It reuses the same provider-selection logic as the
// red-team LLM judge (selectJudgeProvider), so promptfoo synthesizes attack
// cases with the same model the rest of the platform uses. Returns nil when
// no usable provider is configured (caller treats that as "no generator").
func ResolveEngineGenerationCredential(ctx context.Context, provider EngineGenerationProvider) *EngineGenCredential {
	if provider == nil {
		return nil
	}
	cfg, err := provider.GetDefaultRuntimeConfig(ctx)
	if err != nil || cfg == nil {
		return nil
	}
	llm, err := selectJudgeProvider(cfg)
	if err != nil {
		return nil
	}
	baseURL := strings.TrimSpace(llm.URL)
	model := strings.TrimSpace(llm.Model)
	if baseURL == "" || model == "" {
		return nil
	}
	baseURL = normalizeOpenAIBaseURL(baseURL)
	return &EngineGenCredential{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  strings.TrimSpace(llm.Key),
	}
}

// normalizeOpenAIBaseURL ensures the base URL ends with /v1 for
// OpenAI-compatible APIs. promptfoo expects a full base path such as
// https://api.example.com/v1; some provider configs omit the /v1 suffix.
func normalizeOpenAIBaseURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	if u == "" {
		return raw
	}
	if strings.HasSuffix(u, "/v1") {
		return u
	}
	return u + "/v1"
}
func copyEngineHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[strings.TrimSpace(k)] = v
	}
	return out
}
