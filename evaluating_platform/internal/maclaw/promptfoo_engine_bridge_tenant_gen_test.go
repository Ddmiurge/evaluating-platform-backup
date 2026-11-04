package maclaw

import (
	"context"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/pkg/config"
)

// The bridge (chat confirm) door must carry the tenant's generation key the
// same way the wizard does — two doors, one resolution result (the P10/P11
// lesson applied to the credential channel).

func TestBridgePrepareCarriesTenantGenerationKey(t *testing.T) {
	ctx := context.Background()
	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{
		MasterKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		KeyID:     "test",
	})
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	service := NewRuntimeConfigService(&fakeModelDefaultStore{}, keyStore)
	service.SetOverrideStore(newFakeOverrideStore())
	tenant, other, admin := uuid.New(), uuid.New(), uuid.New()
	if _, err := service.SaveDefaultRuntimeConfig(ctx, cfgWith("platform-model", "sk-platform"), &admin); err != nil {
		t.Fatalf("default: %v", err)
	}
	if _, _, err := service.SaveUserRuntimeConfig(ctx, tenant, cfgWith("acme-model", "sk-acme"), &admin); err != nil {
		t.Fatalf("override: %v", err)
	}

	runner := &fakePromptfooEngineRunner{enabled: true, statuses: map[string]*EngineRunStatus{}}
	bridge := NewRedteamToolBridge(nil, nil, nil)
	bridge.SetPromptfooEngine(runner, NewEngineRunService(newFakeEngineRunStore()), &fakeEngineTargetProvider{
		target: &EvaluationTargetInput{ID: "t1", BaseURL: "https://t.example/v1", Model: "m", CredentialSecret: "sk-t", Enabled: true},
	}, service)

	if _, err := bridge.PreparePromptfooEngineRun(ctx, tenant, "inst-1", PromptfooEngineRunInput{
		Purpose: "role", Plugins: []string{"contracts"}, NumTests: 1,
	}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(runner.created) != 1 {
		t.Fatalf("created = %d", len(runner.created))
	}
	gen := runner.created[0].Credentials.Generation
	if gen == nil || gen.Model != "acme-model" || gen.APIKey != "sk-acme" {
		t.Fatalf("bridge door must send the TENANT generation key, got %+v", gen)
	}
	// Same bridge, tenant without mirror: platform default (behavior of every
	// account before P26b).
	runner.created = nil
	if _, err := bridge.PreparePromptfooEngineRun(ctx, other, "inst-1", PromptfooEngineRunInput{
		Purpose: "role", Plugins: []string{"contracts"}, NumTests: 1,
	}); err != nil {
		t.Fatalf("prepare B: %v", err)
	}
	if gen := runner.created[0].Credentials.Generation; gen == nil || gen.Model != "platform-model" {
		t.Fatalf("mirrorless tenant = %+v, want platform default", gen)
	}
}
