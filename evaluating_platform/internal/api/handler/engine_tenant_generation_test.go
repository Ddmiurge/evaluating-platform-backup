package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/model"
	"evaluating_platform/pkg/config"
)

// Wizard door, full stack: a REAL maclaw.RuntimeConfigService over fake stores,
// so the assertion covers the actual call site (handler → ForUser resolver →
// service → mirror), not just the resolver in isolation.

type handlerDefaultStore struct{ record *model.MaclawModelDefault }

func (s *handlerDefaultStore) GetDefault(context.Context) (*model.MaclawModelDefault, error) {
	return s.record, nil
}

func (s *handlerDefaultStore) UpsertDefault(_ context.Context, enc []byte, keyID string, by *uuid.UUID) error {
	s.record = &model.MaclawModelDefault{ID: "default", EncryptedConfig: enc, ConfigKeyID: keyID, UpdatedBy: by}
	return nil
}

type handlerOverrideStore struct{ records map[uuid.UUID]*model.MaclawModelDefault }

func (s *handlerOverrideStore) GetOverride(_ context.Context, id uuid.UUID) (*model.MaclawModelDefault, error) {
	return s.records[id], nil
}

func (s *handlerOverrideStore) UpsertOverride(_ context.Context, id uuid.UUID, enc []byte, keyID string, by *uuid.UUID) error {
	s.records[id] = &model.MaclawModelDefault{ID: "user:" + id.String(), EncryptedConfig: enc, ConfigKeyID: keyID, UpdatedBy: by}
	return nil
}

func tenantGenerationService(t *testing.T) (*maclaw.RuntimeConfigService, uuid.UUID) {
	t.Helper()
	ks, err := appcrypto.NewKeyStore(config.CryptoConfig{
		MasterKey: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		KeyID:     "test",
	})
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	svc := maclaw.NewRuntimeConfigService(&handlerDefaultStore{}, ks)
	svc.SetOverrideStore(&handlerOverrideStore{records: map[uuid.UUID]*model.MaclawModelDefault{}})
	return svc, uuid.New()
}

func TestWizardGenerationCredentialFollowsTenantMirror(t *testing.T) {
	svc, _ := tenantGenerationService(t)
	ctx := context.Background()
	admin := uuid.New()
	platformCfg := maclaw.RuntimeAppConfig{
		MaclawLLMProviders:       []maclaw.RuntimeLLMProvider{{Name: "gen", URL: "https://gen.example", Key: "sk-platform", Model: "platform-model"}},
		MaclawLLMCurrentProvider: "gen",
	}
	if _, err := svc.SaveDefaultRuntimeConfig(ctx, platformCfg, &admin); err != nil {
		t.Fatalf("default: %v", err)
	}
	// The wizard router authenticates as 1111...1111; mirror THAT account.
	wizardUser := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	tenantCfg := maclaw.RuntimeAppConfig{
		MaclawLLMProviders:       []maclaw.RuntimeLLMProvider{{Name: "gen", URL: "https://acme.example/v1", Key: "sk-acme", Model: "acme-model"}},
		MaclawLLMCurrentProvider: "gen",
	}
	if _, _, err := svc.SaveUserRuntimeConfig(ctx, wizardUser, tenantCfg, &admin); err != nil {
		t.Fatalf("override: %v", err)
	}

	store := newFakeRunStore()
	engine := newEnabledFakeEngine()
	h := newEngineRunHandler(engine, maclaw.NewEngineRunService(store), wizardTargetService(), svc)
	r := newEngineRunRouter(h, "enterprise")
	w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", `{"purpose":"合规安全评测","plugins":[{"id":"contracts"}],"num_tests":1}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	gen := engine.createReq.Credentials.Generation
	if gen == nil || gen.Model != "acme-model" || gen.APIKey != "sk-acme" {
		t.Fatalf("wizard must submit with the tenant generation key, got %+v", gen)
	}
}
