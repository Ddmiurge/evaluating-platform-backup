package maclaw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
	"evaluating_platform/internal/maclaw/redteam"
)

type ResourceRecord = model.MaclawResourceRecord

type ResourceStore interface {
	UpsertResource(context.Context, ResourceRecord) (*ResourceRecord, error)
	ListResources(context.Context, uuid.UUID, EvaluationResourceQuery) ([]ResourceRecord, error)
	GetResource(context.Context, uuid.UUID, string) (*ResourceRecord, error)
}

type ResourceStoreService struct {
	store    ResourceStore
	keyStore *appcrypto.KeyStore
	now      func() time.Time
}

func NewResourceStoreService(store ResourceStore, keyStore *appcrypto.KeyStore) *ResourceStoreService {
	return &ResourceStoreService{store: store, keyStore: keyStore}
}

func (s *ResourceStoreService) Enabled() bool {
	return s != nil && s.store != nil && s.keyStore != nil
}

func (s *ResourceStoreService) SaveResource(ctx context.Context, userID uuid.UUID, tenantID string, in EvaluationResourceInput) (*EvaluationResourceSummary, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	userID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("resource name is required")
	}
	kind := in.Kind
	if kind == "" {
		kind = EvaluationResourceKindSample
	}
	status := in.Status
	if status == "" {
		status = EvaluationResourceStatusDraft
	}
	health := in.HealthStatus
	if health == "" {
		health = EvaluationResourceHealthUnknown
	}
	id, err := parseOptionalUUID(in.ID)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		id = uuid.New()
	}
	handle := strings.TrimSpace(in.Metadata["handle"])
	if handle == "" {
		handle = "platform_resource_" + id.String()
	}
	keyID, key := s.keyStore.CurrentKey()
	encrypted, err := appcrypto.Encrypt([]byte(in.Payload), key)
	if err != nil {
		return nil, fmt.Errorf("encrypt maclaw resource payload: %w", err)
	}
	record := ResourceRecord{
		ID:                  id,
		OwnerUserID:         userID,
		OwnerMaclawTenantID: strings.TrimSpace(tenantID),
		Handle:              handle,
		Name:                name,
		Description:         strings.TrimSpace(in.Description),
		Kind:                string(kind),
		Version:             normalizeVersion(in.Version),
		Status:              string(status),
		Enabled:             in.Enabled,
		HealthStatus:        string(health),
		AssessmentTypes:     append([]string(nil), in.AssessmentTypes...),
		Tags:                append([]string(nil), in.Tags...),
		Summary:             strings.TrimSpace(in.Summary),
		EncryptedPayload:    encrypted,
		PayloadKeyID:        keyID,
		Metadata:            redteam.SanitizeMetadata(in.Metadata),
	}
	saved, err := s.store.UpsertResource(ctx, record)
	if err != nil {
		return nil, err
	}
	return resourceSummaryFromRecord(saved), nil
}

func (s *ResourceStoreService) ListResources(ctx context.Context, userID uuid.UUID, q EvaluationResourceQuery) ([]EvaluationResourceSummary, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	userID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	records, err := s.store.ListResources(ctx, userID, q)
	if err != nil {
		return nil, err
	}
	out := make([]EvaluationResourceSummary, 0, len(records))
	for i := range records {
		out = append(out, *resourceSummaryFromRecord(&records[i]))
	}
	return out, nil
}

func (s *ResourceStoreService) PreviewResource(ctx context.Context, userID uuid.UUID, idOrHandle string) (*EvaluationResourcePreview, error) {
	materialized, summary, err := s.materialize(ctx, userID, idOrHandle)
	if err != nil || materialized == nil {
		return nil, err
	}
	payload := materialized.Payload
	sum := sha256.Sum256([]byte(payload))
	return &EvaluationResourcePreview{
		Resource:         *summary,
		PayloadBytes:     len([]byte(payload)),
		PayloadLineCount: countLines(payload),
		PayloadSHA256:    hex.EncodeToString(sum[:]),
	}, nil
}

func (s *ResourceStoreService) MaterializeResource(ctx context.Context, userID uuid.UUID, idOrHandle string) (*EvaluationResourceMaterialization, error) {
	materialized, _, err := s.materialize(ctx, userID, idOrHandle)
	return materialized, err
}

func (s *ResourceStoreService) materialize(ctx context.Context, userID uuid.UUID, idOrHandle string) (*EvaluationResourceMaterialization, *EvaluationResourceSummary, error) {
	if !s.Enabled() {
		return nil, nil, ErrNotConfigured
	}
	userID, err := normalizeUserID(userID)
	if err != nil {
		return nil, nil, err
	}
	idOrHandle = strings.TrimSpace(idOrHandle)
	if idOrHandle == "" {
		return nil, nil, errors.New("resource id is required")
	}
	record, err := s.store.GetResource(ctx, userID, idOrHandle)
	if err != nil || record == nil {
		return nil, nil, err
	}
	key, err := s.keyStore.GetKey(record.PayloadKeyID)
	if err != nil {
		return nil, nil, fmt.Errorf("load maclaw resource key: %w", err)
	}
	plain, err := appcrypto.Decrypt(record.EncryptedPayload, key)
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt maclaw resource payload: %w", err)
	}
	summary := resourceSummaryFromRecord(record)
	return &EvaluationResourceMaterialization{
		ResourceID: record.ID.String(),
		Handle:     record.Handle,
		Name:       record.Name,
		Kind:       EvaluationResourceKind(record.Kind),
		Version:    normalizeVersion(record.Version),
		Payload:    string(plain),
		Metadata:   redteam.SanitizeMetadata(record.Metadata),
	}, summary, nil
}

type PlatformResourceGateway struct {
	GatewayClient
	resources *ResourceStoreService
	userID    uuid.UUID
	tenantID  string
}

func NewPlatformResourceGateway(upstream GatewayClient, resources *ResourceStoreService, userID uuid.UUID, tenantID string) GatewayClient {
	return &PlatformResourceGateway{GatewayClient: upstream, resources: resources, userID: userID, tenantID: strings.TrimSpace(tenantID)}
}

func (g *PlatformResourceGateway) Enabled() bool {
	return (g != nil && g.resources != nil && g.resources.Enabled()) || (g != nil && g.GatewayClient != nil && g.GatewayClient.Enabled())
}

func (g *PlatformResourceGateway) SearchEvaluationResources(ctx context.Context, q EvaluationResourceQuery) ([]EvaluationResourceSummary, error) {
	if g != nil && g.resources != nil && g.resources.Enabled() {
		return g.resources.ListResources(ctx, g.userID, q)
	}
	return g.GatewayClient.SearchEvaluationResources(ctx, q)
}

func (g *PlatformResourceGateway) SaveEvaluationResource(ctx context.Context, in EvaluationResourceInput) (*EvaluationResourceSummary, error) {
	if g != nil && g.resources != nil && g.resources.Enabled() {
		return g.resources.SaveResource(ctx, g.userID, g.tenantID, in)
	}
	return g.GatewayClient.SaveEvaluationResource(ctx, in)
}

func (g *PlatformResourceGateway) PreviewEvaluationResource(ctx context.Context, resourceID string) (*EvaluationResourcePreview, error) {
	if g != nil && g.resources != nil && g.resources.Enabled() {
		return g.resources.PreviewResource(ctx, g.userID, resourceID)
	}
	return g.GatewayClient.PreviewEvaluationResource(ctx, resourceID)
}

func (g *PlatformResourceGateway) MaterializeEvaluationResource(ctx context.Context, handle string) (*EvaluationResourceMaterialization, error) {
	if g != nil && g.resources != nil && g.resources.Enabled() {
		return g.resources.MaterializeResource(ctx, g.userID, handle)
	}
	return g.GatewayClient.MaterializeEvaluationResource(ctx, handle)
}

func resourceSummaryFromRecord(record *ResourceRecord) *EvaluationResourceSummary {
	if record == nil {
		return nil
	}
	return &EvaluationResourceSummary{
		ID:              record.ID.String(),
		Handle:          record.Handle,
		Name:            record.Name,
		Description:     record.Description,
		Kind:            EvaluationResourceKind(record.Kind),
		Version:         normalizeVersion(record.Version),
		Status:          EvaluationResourceStatus(record.Status),
		Enabled:         record.Enabled,
		HealthStatus:    EvaluationResourceHealthStatus(record.HealthStatus),
		AssessmentTypes: append([]string(nil), record.AssessmentTypes...),
		Tags:            append([]string(nil), record.Tags...),
		Summary:         record.Summary,
		Metadata:        redteam.SanitizeMetadata(record.Metadata),
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}
}

func parseOptionalUUID(value string) (uuid.UUID, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid resource id: %w", err)
	}
	return id, nil
}

func countLines(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}
