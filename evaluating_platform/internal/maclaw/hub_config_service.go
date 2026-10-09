package maclaw

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
)

const DefaultHubSkillSource = "skillhub"

type HubConfigStore interface {
	GetHubConfig(context.Context) (*model.MaclawHubConfigRecord, error)
	UpsertHubConfig(context.Context, []byte, string, *uuid.UUID) error
}

type MaclawHubConfig struct {
	Enabled        bool      `json:"enabled"`
	HubURL         string    `json:"hub_url,omitempty"`
	AllowedSources []string  `json:"allowed_sources,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type MaclawHubConfigService struct {
	store    HubConfigStore
	keyStore *appcrypto.KeyStore
}

func NewMaclawHubConfigService(store HubConfigStore, keyStore *appcrypto.KeyStore) *MaclawHubConfigService {
	return &MaclawHubConfigService{store: store, keyStore: keyStore}
}

func (s *MaclawHubConfigService) Enabled() bool {
	return s != nil && s.store != nil && s.keyStore != nil
}

func (s *MaclawHubConfigService) GetHubConfig(ctx context.Context) (*MaclawHubConfig, error) {
	if !s.Enabled() {
		return nil, nil
	}
	record, err := s.store.GetHubConfig(ctx)
	if err != nil {
		return nil, err
	}
	if record == nil || len(record.EncryptedConfig) == 0 {
		return &MaclawHubConfig{Enabled: false, AllowedSources: []string{DefaultHubSkillSource}}, nil
	}
	cfg, err := s.decryptConfig(record)
	if err != nil {
		return nil, err
	}
	cfg.UpdatedAt = record.UpdatedAt
	return cfg, nil
}

func (s *MaclawHubConfigService) SaveHubConfig(ctx context.Context, next MaclawHubConfig, updatedBy *uuid.UUID) (*MaclawHubConfig, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	normalized, err := NormalizeHubConfig(next)
	if err != nil {
		return nil, err
	}
	encrypted, keyID, err := encryptJSON(s.keyStore, normalized, "maclaw hub config")
	if err != nil {
		return nil, err
	}
	if err := s.store.UpsertHubConfig(ctx, encrypted, keyID, updatedBy); err != nil {
		return nil, err
	}
	normalized.UpdatedAt = time.Now()
	return &normalized, nil
}

func (s *MaclawHubConfigService) RuntimeConfigPatch(ctx context.Context) (*RuntimeAppConfig, error) {
	cfg, err := s.GetHubConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}
	return &RuntimeAppConfig{
		RemoteHubURL:        cfg.HubURL,
		SkillSourcesAllowed: append([]string(nil), cfg.AllowedSources...),
	}, nil
}

func (s *MaclawHubConfigService) decryptConfig(record *model.MaclawHubConfigRecord) (*MaclawHubConfig, error) {
	var cfg MaclawHubConfig
	if err := decryptJSON(s.keyStore, record.EncryptedConfig, record.ConfigKeyID, &cfg, "maclaw hub config"); err != nil {
		return nil, err
	}
	normalized, err := NormalizeHubConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}

func NormalizeHubConfig(in MaclawHubConfig) (MaclawHubConfig, error) {
	out := MaclawHubConfig{
		Enabled:        in.Enabled,
		HubURL:         strings.TrimRight(strings.TrimSpace(in.HubURL), "/"),
		AllowedSources: normalizeSkillSources(in.AllowedSources),
	}
	if out.Enabled {
		if out.HubURL == "" {
			return MaclawHubConfig{}, fmt.Errorf("hub_url is required when hub config is enabled")
		}
		parsed, err := url.Parse(out.HubURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return MaclawHubConfig{}, fmt.Errorf("hub_url must be an absolute URL")
		}
	}
	return out, nil
}

func normalizeSkillSources(items []string) []string {
	if len(items) == 0 {
		return []string{DefaultHubSkillSource}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.ToLower(strings.TrimSpace(item))
		switch item {
		case "skillhub", "github", "skillmarket", "clawhub":
		default:
			continue
		}
		if item == "skillmarket" {
			item = "skillhub"
		}
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return []string{DefaultHubSkillSource}
	}
	return out
}
