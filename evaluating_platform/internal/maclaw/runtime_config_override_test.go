package maclaw

import (
	"context"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
	"evaluating_platform/pkg/config"
)

// P26b · tenant generation key (D-3 option b: platform-side mirror). The
// properties that carry weight: a tenant mirror wins over the platform default
// for THAT account only; masked re-saves restore from the tenant's own
// previous mirror (never another tenant's, never the platform's, when a
// mirror exists); an unreadable mirror fails LOUDLY instead of quietly
// switching the run back to the platform key.

type fakeModelOverrideStore struct {
	records map[uuid.UUID]*model.MaclawModelDefault
	err     error
}

func newFakeOverrideStore() *fakeModelOverrideStore {
	return &fakeModelOverrideStore{records: map[uuid.UUID]*model.MaclawModelDefault{}}
}

func (s *fakeModelOverrideStore) GetOverride(_ context.Context, id uuid.UUID) (*model.MaclawModelDefault, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.records[id], nil
}

func (s *fakeModelOverrideStore) UpsertOverride(_ context.Context, id uuid.UUID, encrypted []byte, keyID string, updatedBy *uuid.UUID) error {
	s.records[id] = &model.MaclawModelDefault{
		ID: "user:" + id.String(), EncryptedConfig: encrypted, ConfigKeyID: keyID, UpdatedBy: updatedBy,
	}
	return nil
}

func overrideTestService(t *testing.T) (*RuntimeConfigService, *fakeModelDefaultStore, *fakeModelOverrideStore) {
	t.Helper()
	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{
		MasterKey: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		KeyID:     "test",
	})
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	defaults := &fakeModelDefaultStore{}
	overrides := newFakeOverrideStore()
	service := NewRuntimeConfigService(defaults, keyStore)
	service.SetOverrideStore(overrides)
	return service, defaults, overrides
}

func cfgWith(modelName, key string) RuntimeAppConfig {
	return RuntimeAppConfig{
		MaclawLLMProviders:       []RuntimeLLMProvider{{Name: "gen", URL: "https://gen.example", Key: key, Model: modelName}},
		MaclawLLMCurrentProvider: "gen",
	}
}

func TestUserRuntimeConfigPrefersMirrorAndFallsBackToDefault(t *testing.T) {
	service, defaults, _ := overrideTestService(t)
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	admin := uuid.New()

	if _, err := service.SaveDefaultRuntimeConfig(ctx, cfgWith("platform-model", "sk-platform"), &admin); err != nil {
		t.Fatalf("save default: %v", err)
	}
	// No mirror yet: both tenants resolve to the platform default (pre-P26b).
	for _, id := range []uuid.UUID{tenantA, tenantB} {
		cfg, err := service.GetUserRuntimeConfig(ctx, id)
		if err != nil || cfg == nil || cfg.MaclawLLMProviders[0].Model != "platform-model" {
			t.Fatalf("user %s effective = %+v err=%v, want default", id, cfg, err)
		}
	}

	if _, full, err := service.SaveUserRuntimeConfig(ctx, tenantA, cfgWith("acme-model", "sk-acme"), &admin); err != nil || full.MaclawLLMProviders[0].Model != "acme-model" {
		t.Fatalf("save override A: %v", err)
	}
	cfgA, err := service.GetUserRuntimeConfig(ctx, tenantA)
	if err != nil || cfgA.MaclawLLMProviders[0].Key != "sk-acme" {
		t.Fatalf("A must resolve its own mirror: %+v %v", cfgA, err)
	}
	cfgB, err := service.GetUserRuntimeConfig(ctx, tenantB)
	if err != nil || cfgB.MaclawLLMProviders[0].Model != "platform-model" {
		t.Fatalf("B must be untouched by A's mirror: %+v %v", cfgB, err)
	}
	_ = defaults
}

func TestOverrideMaskedSaveRestoresFromOwnMirrorOnly(t *testing.T) {
	service, _, overrides := overrideTestService(t)
	ctx := context.Background()
	tenantA, tenantB, admin := uuid.New(), uuid.New(), uuid.New()
	if _, err := service.SaveDefaultRuntimeConfig(ctx, cfgWith("platform-model", "sk-platform"), &admin); err != nil {
		t.Fatalf("default: %v", err)
	}
	if _, _, err := service.SaveUserRuntimeConfig(ctx, tenantA, cfgWith("acme-model", "sk-acme"), &admin); err != nil {
		t.Fatalf("A first save: %v", err)
	}
	// Re-save with masked secret (the browser only ever sees ******): the key
	// must come back from A's OWN mirror.
	masked := cfgWith("acme-model-v2", "******")
	if _, full, err := service.SaveUserRuntimeConfig(ctx, tenantA, masked, &admin); err != nil || full.MaclawLLMProviders[0].Key != "sk-acme" {
		t.Fatalf("masked re-save A lost its own key: %+v %v", full, err)
	}
	// And B's masked save must draw from the PLATFORM default, never A.
	maskedB := cfgWith("b-model", "******")
	if _, fullB, err := service.SaveUserRuntimeConfig(ctx, tenantB, maskedB, &admin); err != nil || fullB.MaclawLLMProviders[0].Key != "sk-platform" {
		t.Fatalf("B picked up the wrong secret source: %+v %v", fullB, err)
	}
	_ = overrides
}

func TestCorruptMirrorFailsLoudlyNotViaDefault(t *testing.T) {
	service, _, overrides := overrideTestService(t)
	ctx := context.Background()
	tenantA, admin := uuid.New(), uuid.New()
	if _, err := service.SaveDefaultRuntimeConfig(ctx, cfgWith("platform-model", "sk-platform"), &admin); err != nil {
		t.Fatalf("default: %v", err)
	}
	if _, _, err := service.SaveUserRuntimeConfig(ctx, tenantA, cfgWith("acme-model", "sk-acme"), &admin); err != nil {
		t.Fatalf("A save: %v", err)
	}
	// Corrupt the stored ciphertext (rotated master key / bit rot).
	rec := overrides.records[tenantA]
	rec.EncryptedConfig = append([]byte{0xAA}, rec.EncryptedConfig...)

	cfg, err := service.GetUserRuntimeConfig(ctx, tenantA)
	if err == nil {
		t.Fatalf("corrupt mirror must error, got cfg=%+v", cfg)
	}
	if cfg != nil {
		t.Fatal("corrupt mirror must not also return a config")
	}
	// The resolver turns that error into nil (run refused) — never the
	// platform key on a tenant that has its own.
	if cred := ResolveEngineGenerationCredentialForUser(ctx, tenantA, service); cred != nil {
		t.Fatalf("resolver must refuse on undecryptable mirror, got %+v", cred)
	}
}

func TestResolveEngineGenerationCredentialForUserScopesPerTenant(t *testing.T) {
	service, _, _ := overrideTestService(t)
	ctx := context.Background()
	tenantA, tenantB, admin := uuid.New(), uuid.New(), uuid.New()
	if _, err := service.SaveDefaultRuntimeConfig(ctx, cfgWith("platform-model", "sk-platform"), &admin); err != nil {
		t.Fatalf("default: %v", err)
	}
	if _, _, err := service.SaveUserRuntimeConfig(ctx, tenantA, cfgWith("acme-model", "sk-acme"), &admin); err != nil {
		t.Fatalf("A: %v", err)
	}

	credA := ResolveEngineGenerationCredentialForUser(ctx, tenantA, service)
	if credA == nil || credA.Model != "acme-model" || credA.APIKey != "sk-acme" {
		t.Fatalf("A credential = %+v, want A's own mirror", credA)
	}
	credB := ResolveEngineGenerationCredentialForUser(ctx, tenantB, service)
	if credB == nil || credB.Model != "platform-model" {
		t.Fatalf("B credential = %+v, want platform default", credB)
	}
	// A provider WITHOUT the user-scoped method (old fake, old deployments)
	// must keep resolving the default — the compat branch.
	legacy, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", KeyID: "t"})
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	def := &fakeModelDefaultStore{}
	defService := NewRuntimeConfigService(def, legacy)
	if _, err := defService.SaveDefaultRuntimeConfig(ctx, cfgWith("only-default", "sk-d"), &admin); err != nil {
		t.Fatalf("legacy default: %v", err)
	}
	cred := ResolveEngineGenerationCredentialForUser(ctx, uuid.New(), &legacyDefaultProvider{svc: defService})
	if cred == nil || cred.Model != "only-default" {
		t.Fatalf("legacy provider path = %+v, want default", cred)
	}
	// A nil user id skips the mirror lookup and resolves the default (no
	// panic, no phantom tenant).
	if credNil := ResolveEngineGenerationCredentialForUser(ctx, uuid.Nil, service); credNil == nil || credNil.Model != "platform-model" {
		t.Fatalf("uuid.Nil = %+v, want platform default", credNil)
	}
}

type legacyDefaultProvider struct {
	svc *RuntimeConfigService
}

func (p *legacyDefaultProvider) GetDefaultRuntimeConfig(ctx context.Context) (*RuntimeAppConfig, error) {
	return p.svc.GetDefaultRuntimeConfig(ctx)
}
