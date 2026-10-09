package maclaw

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
)

type TargetConfigRecord = model.MaclawTargetConfig

type TargetConfigStore interface {
	GetByUserID(context.Context, uuid.UUID) (*TargetConfigRecord, error)
	Upsert(context.Context, TargetConfigRecord) (*TargetConfigRecord, error)
}

type TargetConfigService struct {
	store    TargetConfigStore
	keyStore *appcrypto.KeyStore
}

var targetConfigHTTPClient = &http.Client{Timeout: 30 * time.Second}

type storedEvaluationTarget struct {
	Name             string                       `json:"name"`
	Description      string                       `json:"description,omitempty"`
	Kind             EvaluationTargetKind         `json:"kind"`
	Provider         string                       `json:"provider,omitempty"`
	BaseURL          string                       `json:"base_url,omitempty"`
	Model            string                       `json:"model,omitempty"`
	AuthType         EvaluationTargetAuthType     `json:"auth_type,omitempty"`
	CredentialSecret string                       `json:"credential_secret,omitempty"`
	Status           EvaluationTargetStatus       `json:"status,omitempty"`
	Enabled          bool                         `json:"enabled"`
	HealthStatus     EvaluationTargetHealthStatus `json:"health_status,omitempty"`
	Tags             []string                     `json:"tags,omitempty"`
	Metadata         map[string]string            `json:"metadata,omitempty"`
}

func NewTargetConfigService(store TargetConfigStore, keyStore *appcrypto.KeyStore) *TargetConfigService {
	return &TargetConfigService{store: store, keyStore: keyStore}
}

func (s *TargetConfigService) Enabled() bool {
	return s != nil && s.store != nil && s.keyStore != nil
}

func (s *TargetConfigService) SaveTarget(ctx context.Context, userID uuid.UUID, in EvaluationTargetInput) (*EvaluationTargetSummary, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	userID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	currentRecord, err := s.store.GetByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	var current *storedEvaluationTarget
	if currentRecord != nil {
		current, err = s.decryptTarget(currentRecord)
		if err != nil {
			return nil, err
		}
	}
	next := normalizeStoredTarget(in)
	if current != nil && isMaskedOrEmpty(next.CredentialSecret) {
		next.CredentialSecret = current.CredentialSecret
	}
	encrypted, keyID, err := encryptJSON(s.keyStore, next, "maclaw target config")
	if err != nil {
		return nil, err
	}
	record := TargetConfigRecord{
		UserID:          userID,
		EncryptedConfig: encrypted,
		ConfigKeyID:     keyID,
	}
	if currentRecord != nil {
		record.ID = currentRecord.ID
	}
	saved, err := s.store.Upsert(ctx, record)
	if err != nil {
		return nil, err
	}
	return targetSummaryFromStored(saved, next), nil
}

func (s *TargetConfigService) ListTargets(ctx context.Context, userID uuid.UUID, q EvaluationTargetQuery) ([]EvaluationTargetSummary, error) {
	target, record, err := s.getStoredTarget(ctx, userID)
	if err != nil || target == nil {
		return nil, err
	}
	summary := targetSummaryFromStored(record, *target)
	if !matchesTargetQuery(summary, q) {
		return []EvaluationTargetSummary{}, nil
	}
	return []EvaluationTargetSummary{*summary}, nil
}

func (s *TargetConfigService) GetTarget(ctx context.Context, userID uuid.UUID, targetID string) (*EvaluationTargetSummary, error) {
	target, record, err := s.getStoredTarget(ctx, userID)
	if err != nil || target == nil {
		return nil, err
	}
	summary := targetSummaryFromStored(record, *target)
	if strings.TrimSpace(targetID) != "" && strings.TrimSpace(targetID) != summary.ID {
		return nil, NewUpstreamError(http.MethodGet, "/platform/evaluation/targets/"+targetID, http.StatusNotFound, "target not found")
	}
	return summary, nil
}

func (s *TargetConfigService) GetTargetWithSecret(ctx context.Context, userID uuid.UUID) (*EvaluationTargetInput, error) {
	target, record, err := s.getStoredTarget(ctx, userID)
	if err != nil || target == nil {
		return nil, err
	}
	out := EvaluationTargetInput{
		ID:               record.ID.String(),
		Name:             target.Name,
		Description:      target.Description,
		Kind:             target.Kind,
		Provider:         target.Provider,
		BaseURL:          target.BaseURL,
		Model:            target.Model,
		AuthType:         target.AuthType,
		CredentialSecret: target.CredentialSecret,
		Status:           target.Status,
		Enabled:          target.Enabled,
		HealthStatus:     target.HealthStatus,
		Tags:             append([]string(nil), target.Tags...),
		Metadata:         cloneMetadata(target.Metadata),
	}
	return &out, nil
}

func (s *TargetConfigService) ProbeTarget(ctx context.Context, userID uuid.UUID, targetID string) (*EvaluationTargetProbeResult, error) {
	target, err := s.GetTargetWithSecret(ctx, userID)
	if err != nil {
		return nil, err
	}
	if target == nil || (strings.TrimSpace(targetID) != "" && target.ID != strings.TrimSpace(targetID)) {
		return nil, NewUpstreamError(http.MethodPost, "/platform/evaluation/targets/"+targetID+"/health-check", http.StatusNotFound, "target not found")
	}
	healthURL := strings.TrimSpace(target.Metadata["health_url"])
	if healthURL == "" {
		if TargetIsAgent(*target) {
			// Agent targets: probe the conversation endpoint itself.
			healthURL = strings.TrimSpace(target.Metadata[TargetMetadataAgentEndpoint])
		} else {
			healthURL = strings.TrimRight(strings.TrimSpace(target.BaseURL), "/") + "/models"
		}
	}
	healthURL = substituteAgentAPIKey(healthURL, strings.TrimSpace(target.CredentialSecret))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return nil, err
	}
	applyTargetAuth(req, *target)
	started := time.Now()
	resp, err := targetConfigHTTPClient.Do(req)
	summary := targetSummaryFromInput(*target)
	if err != nil {
		summary.HealthStatus = EvaluationTargetHealthUnavailable
		return &EvaluationTargetProbeResult{Target: *summary, Status: EvaluationTargetHealthUnavailable, Message: "target connection failed", CheckedAt: started}, nil
	}
	defer resp.Body.Close()
	status := EvaluationTargetHealthUnavailable
	message := "target health check failed"
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		status = EvaluationTargetHealthHealthy
		message = "target responded"
	} else if resp.StatusCode < 500 {
		status = EvaluationTargetHealthDegraded
	}
	summary.HealthStatus = status
	return &EvaluationTargetProbeResult{Target: *summary, Status: status, Message: message, StatusCode: resp.StatusCode, CheckedAt: started}, nil
}

func (s *TargetConfigService) getStoredTarget(ctx context.Context, userID uuid.UUID) (*storedEvaluationTarget, *TargetConfigRecord, error) {
	if !s.Enabled() {
		return nil, nil, ErrNotConfigured
	}
	userID, err := normalizeUserID(userID)
	if err != nil {
		return nil, nil, err
	}
	record, err := s.store.GetByUserID(ctx, userID)
	if err != nil || record == nil || len(record.EncryptedConfig) == 0 {
		return nil, nil, err
	}
	target, err := s.decryptTarget(record)
	if err != nil {
		return nil, nil, err
	}
	return target, record, nil
}

func (s *TargetConfigService) decryptTarget(record *TargetConfigRecord) (*storedEvaluationTarget, error) {
	var out storedEvaluationTarget
	if err := decryptJSON(s.keyStore, record.EncryptedConfig, record.ConfigKeyID, &out, "maclaw target config"); err != nil {
		return nil, err
	}
	return &out, nil
}

type PlatformTargetGateway struct {
	GatewayClient
	targets *TargetConfigService
	userID  uuid.UUID
}

func NewPlatformTargetGateway(upstream GatewayClient, targets *TargetConfigService, userID uuid.UUID) GatewayClient {
	return &PlatformTargetGateway{GatewayClient: upstream, targets: targets, userID: userID}
}

func (g *PlatformTargetGateway) Enabled() bool {
	return (g != nil && g.targets != nil && g.targets.Enabled()) || (g != nil && g.GatewayClient != nil && g.GatewayClient.Enabled())
}

func (g *PlatformTargetGateway) SearchEvaluationTargets(ctx context.Context, q EvaluationTargetQuery) ([]EvaluationTargetSummary, error) {
	if g != nil && g.targets != nil && g.targets.Enabled() {
		return g.targets.ListTargets(ctx, g.userID, q)
	}
	return g.GatewayClient.SearchEvaluationTargets(ctx, q)
}

func (g *PlatformTargetGateway) SaveEvaluationTarget(ctx context.Context, in EvaluationTargetInput) (*EvaluationTargetSummary, error) {
	if g != nil && g.targets != nil && g.targets.Enabled() {
		return g.targets.SaveTarget(ctx, g.userID, in)
	}
	return g.GatewayClient.SaveEvaluationTarget(ctx, in)
}

func (g *PlatformTargetGateway) GetEvaluationTarget(ctx context.Context, targetID string) (*EvaluationTargetSummary, error) {
	if g != nil && g.targets != nil && g.targets.Enabled() {
		return g.targets.GetTarget(ctx, g.userID, targetID)
	}
	return g.GatewayClient.GetEvaluationTarget(ctx, targetID)
}

func (g *PlatformTargetGateway) ProbeEvaluationTarget(ctx context.Context, targetID string) (*EvaluationTargetProbeResult, error) {
	if g != nil && g.targets != nil && g.targets.Enabled() {
		return g.targets.ProbeTarget(ctx, g.userID, targetID)
	}
	return g.GatewayClient.ProbeEvaluationTarget(ctx, targetID)
}

func normalizeStoredTarget(in EvaluationTargetInput) storedEvaluationTarget {
	kind := in.Kind
	if kind == "" {
		kind = EvaluationTargetKindLLM
	}
	authType := in.AuthType
	if authType == "" {
		authType = EvaluationTargetAuthTypeBearer
	}
	status := in.Status
	if status == "" {
		status = EvaluationTargetStatusPublished
	}
	health := in.HealthStatus
	if health == "" {
		health = EvaluationTargetHealthUnknown
	}
	enabled := in.Enabled || status == EvaluationTargetStatusPublished
	metadata := sanitizeTargetMetadata(in.Metadata)
	if metadata != nil {
		if healthURL := strings.TrimSpace(metadata["health_url"]); healthURL != "" {
			metadata["health_url"] = sanitizeCredentialURL(healthURL)
		}
	}
	return storedEvaluationTarget{
		Name:             strings.TrimSpace(in.Name),
		Description:      strings.TrimSpace(in.Description),
		Kind:             kind,
		Provider:         strings.TrimSpace(in.Provider),
		BaseURL:          sanitizeCredentialURL(in.BaseURL),
		Model:            strings.TrimSpace(in.Model),
		AuthType:         authType,
		CredentialSecret: strings.TrimSpace(in.CredentialSecret),
		Status:           status,
		Enabled:          enabled,
		HealthStatus:     health,
		Tags:             cleanStringSlice(in.Tags),
		Metadata:         metadata,
	}
}

func sanitizeCredentialURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return value
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		if isSensitiveTargetURLQueryKey(key) {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func isSensitiveTargetURLQueryKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	for _, marker := range []string{"api_key", "apikey", "access_token", "auth", "bearer", "credential", "key", "secret", "token"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func targetSummaryFromStored(record *TargetConfigRecord, target storedEvaluationTarget) *EvaluationTargetSummary {
	id := ""
	var createdAt, updatedAt time.Time
	if record != nil {
		id = record.ID.String()
		createdAt = record.CreatedAt
		updatedAt = record.UpdatedAt
	}
	return &EvaluationTargetSummary{
		ID:                  id,
		Name:                target.Name,
		Description:         target.Description,
		Kind:                target.Kind,
		Provider:            target.Provider,
		BaseURL:             target.BaseURL,
		Model:               target.Model,
		AuthType:            target.AuthType,
		CredentialSecretSet: strings.TrimSpace(target.CredentialSecret) != "",
		Status:              target.Status,
		Enabled:             target.Enabled,
		HealthStatus:        target.HealthStatus,
		Tags:                append([]string(nil), target.Tags...),
		Metadata:            sanitizeTargetMetadata(target.Metadata),
		CreatedAt:           createdAt,
		UpdatedAt:           updatedAt,
	}
}

func targetSummaryFromInput(target EvaluationTargetInput) *EvaluationTargetSummary {
	stored := normalizeStoredTarget(target)
	record := &TargetConfigRecord{ID: uuid.Nil}
	if parsed, err := uuid.Parse(strings.TrimSpace(target.ID)); err == nil {
		record.ID = parsed
	}
	return targetSummaryFromStored(record, stored)
}

func matchesTargetQuery(target *EvaluationTargetSummary, q EvaluationTargetQuery) bool {
	if target == nil {
		return false
	}
	if q.Kind != "" && target.Kind != q.Kind {
		return false
	}
	if strings.TrimSpace(q.Provider) != "" && !strings.EqualFold(target.Provider, strings.TrimSpace(q.Provider)) {
		return false
	}
	if !q.IncludeInactive && (!target.Enabled || target.Status == EvaluationTargetStatusArchived) {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(q.Query))
	if query != "" {
		haystack := strings.ToLower(strings.Join([]string{target.Name, target.Description, target.Provider, target.Model, strings.Join(target.Tags, " ")}, " "))
		if !strings.Contains(haystack, query) {
			return false
		}
	}
	return true
}

func applyTargetAuth(req *http.Request, target EvaluationTargetInput) {
	secret := strings.TrimSpace(target.CredentialSecret)
	if secret == "" {
		return
	}
	switch target.AuthType {
	case EvaluationTargetAuthTypeAPIKey:
		headerName := strings.TrimSpace(target.Metadata["api_key_header"])
		if headerName == "" {
			headerName = "Authorization"
		}
		if strings.EqualFold(headerName, "Authorization") {
			req.Header.Set(headerName, "Bearer "+secret)
		} else {
			req.Header.Set(headerName, secret)
		}
	case EvaluationTargetAuthTypeBearer:
		req.Header.Set("Authorization", "Bearer "+secret)
	}
}

func sanitizeTargetMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower == "" || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || lower == "api_key" || lower == "credential_secret" {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanStringSlice(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func normalizeUserID(userID uuid.UUID) (uuid.UUID, error) {
	if userID == uuid.Nil {
		return uuid.Nil, errors.New("platform user id is required")
	}
	return userID, nil
}
