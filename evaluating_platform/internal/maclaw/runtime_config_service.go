package maclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
)

type ModelDefaultStore interface {
	GetDefault(context.Context) (*model.MaclawModelDefault, error)
	UpsertDefault(context.Context, []byte, string, *uuid.UUID) error
}

// ModelOverrideStore is the P26b tenant mirror (docs/architecture/
// tenant-isolation-g1-g3.md D-3, option b). The single write is the admin
// portal save; the single hot read is engine generation resolution.
type ModelOverrideStore interface {
	GetOverride(context.Context, uuid.UUID) (*model.MaclawModelDefault, error)
	UpsertOverride(context.Context, uuid.UUID, []byte, string, *uuid.UUID) error
}

type RuntimeConfigService struct {
	store     ModelDefaultStore
	overrides ModelOverrideStore
	keyStore  *appcrypto.KeyStore
}

func NewRuntimeConfigService(store ModelDefaultStore, keyStore *appcrypto.KeyStore) *RuntimeConfigService {
	return &RuntimeConfigService{store: store, keyStore: keyStore}
}

// SetOverrideStore wires the tenant mirror store. Optional by design: a
// deployment without it keeps the platform default for every account, which is
// exactly the pre-P26b behavior.
func (s *RuntimeConfigService) SetOverrideStore(store ModelOverrideStore) {
	if s != nil {
		s.overrides = store
	}
}

func (s *RuntimeConfigService) HasOverrideStore() bool {
	return s != nil && s.overrides != nil
}

func (s *RuntimeConfigService) Enabled() bool {
	return s != nil && s.store != nil && s.keyStore != nil
}

func (s *RuntimeConfigService) GetDefaultRuntimeConfig(ctx context.Context) (*RuntimeAppConfig, error) {
	if !s.Enabled() {
		return nil, nil
	}
	record, err := s.store.GetDefault(ctx)
	if err != nil {
		return nil, err
	}
	if record == nil || len(record.EncryptedConfig) == 0 {
		return nil, nil
	}
	return s.decryptConfig(record)
}

// GetUserOverrideRuntimeConfig returns the tenant's mirrored config.
// (nil, false, nil) means "no mirror row" — callers fall back to the platform
// default, which is the pre-P26b behavior. A row that EXISTS but cannot be
// decrypted is returned as an error, never as "absent": silently using the
// platform key where a tenant key was configured is exactly the key-scope
// mixing the tenant gates exist to prevent, and it would fail silently.
func (s *RuntimeConfigService) GetUserOverrideRuntimeConfig(ctx context.Context, userID uuid.UUID) (*RuntimeAppConfig, bool, error) {
	if !s.Enabled() || !s.HasOverrideStore() || userID == uuid.Nil {
		return nil, false, nil
	}
	record, err := s.overrides.GetOverride(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	if record == nil || len(record.EncryptedConfig) == 0 {
		return nil, false, nil
	}
	cfg, err := s.decryptConfig(record)
	if err != nil {
		return nil, true, err
	}
	return cfg, true, nil
}

// GetUserRuntimeConfig is the tenant-aware effective model config: mirror row
// when present, platform default otherwise.
func (s *RuntimeConfigService) GetUserRuntimeConfig(ctx context.Context, userID uuid.UUID) (*RuntimeAppConfig, error) {
	cfg, present, err := s.GetUserOverrideRuntimeConfig(ctx, userID)
	if err != nil {
		return nil, err
	}
	if present {
		return cfg, nil
	}
	return s.GetDefaultRuntimeConfig(ctx)
}

// SaveUserRuntimeConfig writes the tenant mirror. Masked/empty secrets are
// restored from the tenant's PREVIOUS effective config (its own mirror first,
// platform default only when no mirror exists yet) — never from another
// tenant's row.
func (s *RuntimeConfigService) SaveUserRuntimeConfig(ctx context.Context, userID uuid.UUID, next RuntimeAppConfig, updatedBy *uuid.UUID) (*RuntimeUserConfig, *RuntimeAppConfig, error) {
	if !s.Enabled() || !s.HasOverrideStore() || userID == uuid.Nil {
		return nil, nil, ErrNotConfigured
	}
	previous, _, err := s.GetUserOverrideRuntimeConfig(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if previous == nil {
		previous, _ = s.GetDefaultRuntimeConfig(ctx)
	}
	if previous != nil {
		next = MergeRuntimeConfigSecrets(*previous, next)
	}
	keyID, key := s.keyStore.CurrentKey()
	data, err := json.Marshal(next)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal maclaw model override: %w", err)
	}
	encrypted, err := appcrypto.Encrypt(data, key)
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt maclaw model override: %w", err)
	}
	if err := s.overrides.UpsertOverride(ctx, userID, encrypted, keyID, updatedBy); err != nil {
		return nil, nil, err
	}
	return &RuntimeUserConfig{AppConfig: MaskRuntimeAppConfig(next)}, &next, nil
}

func (s *RuntimeConfigService) GetMaskedDefaultRuntimeConfig(ctx context.Context) (*RuntimeUserConfig, error) {
	cfg, err := s.GetDefaultRuntimeConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &RuntimeUserConfig{AppConfig: RuntimeAppConfig{}}, nil
	}
	masked := MaskRuntimeAppConfig(*cfg)
	return &RuntimeUserConfig{AppConfig: masked}, nil
}

func (s *RuntimeConfigService) SaveDefaultRuntimeConfig(ctx context.Context, next RuntimeAppConfig, updatedBy *uuid.UUID) (*RuntimeUserConfig, error) {
	masked, _, err := s.SaveDefaultRuntimeConfigForSync(ctx, next, updatedBy)
	return masked, err
}

func (s *RuntimeConfigService) SaveDefaultRuntimeConfigForSync(ctx context.Context, next RuntimeAppConfig, updatedBy *uuid.UUID) (*RuntimeUserConfig, *RuntimeAppConfig, error) {
	if !s.Enabled() {
		return nil, nil, ErrNotConfigured
	}
	current, err := s.GetDefaultRuntimeConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	if current != nil {
		next = MergeRuntimeConfigSecrets(*current, next)
	}
	keyID, key := s.keyStore.CurrentKey()
	data, err := json.Marshal(next)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal maclaw default model config: %w", err)
	}
	encrypted, err := appcrypto.Encrypt(data, key)
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt maclaw default model config: %w", err)
	}
	if err := s.store.UpsertDefault(ctx, encrypted, keyID, updatedBy); err != nil {
		return nil, nil, err
	}
	return &RuntimeUserConfig{AppConfig: MaskRuntimeAppConfig(next), UpdatedAt: time.Now()}, &next, nil
}

func (s *RuntimeConfigService) decryptConfig(record *model.MaclawModelDefault) (*RuntimeAppConfig, error) {
	key, err := s.keyStore.GetKey(record.ConfigKeyID)
	if err != nil {
		return nil, fmt.Errorf("load maclaw default model config key: %w", err)
	}
	plain, err := appcrypto.Decrypt(record.EncryptedConfig, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt maclaw default model config: %w", err)
	}
	var cfg RuntimeAppConfig
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return nil, fmt.Errorf("decode maclaw default model config: %w", err)
	}
	return &cfg, nil
}

func MaskRuntimeAppConfig(cfg RuntimeAppConfig) RuntimeAppConfig {
	if cfg.MaclawLLMProviders != nil {
		cfg.MaclawLLMProviders = append([]RuntimeLLMProvider(nil), cfg.MaclawLLMProviders...)
	}
	if strings.TrimSpace(cfg.MaclawLLMKey) != "" {
		cfg.MaclawLLMKey = "******"
	}
	for i := range cfg.MaclawLLMProviders {
		if strings.TrimSpace(cfg.MaclawLLMProviders[i].Key) != "" {
			cfg.MaclawLLMProviders[i].Key = "******"
		}
		if strings.TrimSpace(cfg.MaclawLLMProviders[i].OAuthAccessToken) != "" {
			cfg.MaclawLLMProviders[i].OAuthAccessToken = "******"
		}
		if strings.TrimSpace(cfg.MaclawLLMProviders[i].RefreshToken) != "" {
			cfg.MaclawLLMProviders[i].RefreshToken = "******"
		}
	}
	return cfg
}

func MergeRuntimeConfigSecrets(current, next RuntimeAppConfig) RuntimeAppConfig {
	if isMaskedOrEmpty(next.MaclawLLMKey) {
		next.MaclawLLMKey = current.MaclawLLMKey
	}
	currentProviders := make(map[string]RuntimeLLMProvider, len(current.MaclawLLMProviders))
	for _, provider := range current.MaclawLLMProviders {
		currentProviders[provider.Name] = provider
	}
	for i := range next.MaclawLLMProviders {
		currentProvider, ok := currentProviders[next.MaclawLLMProviders[i].Name]
		if !ok {
			continue
		}
		if isMaskedOrEmpty(next.MaclawLLMProviders[i].Key) {
			next.MaclawLLMProviders[i].Key = currentProvider.Key
		}
		if isMaskedOrEmpty(next.MaclawLLMProviders[i].OAuthAccessToken) {
			next.MaclawLLMProviders[i].OAuthAccessToken = currentProvider.OAuthAccessToken
		}
		if isMaskedOrEmpty(next.MaclawLLMProviders[i].RefreshToken) {
			next.MaclawLLMProviders[i].RefreshToken = currentProvider.RefreshToken
		}
	}
	return next
}

func isMaskedOrEmpty(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "******"
}
