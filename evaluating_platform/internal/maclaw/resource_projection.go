package maclaw

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
)

const (
	shadowSyncReady = "ready"
	shadowCreatedBy = "bff"
	metadataTrue    = "true"
)

type ResourcePublicationStore interface {
	UpsertPublication(context.Context, *model.MaclawResourcePublication) error
	MarkPublicationUnavailable(context.Context, uuid.UUID, string) error
	ListPublished(context.Context, EvaluationResourceQuery) ([]model.MaclawResourcePublication, error)
	GetPublishedByHandle(context.Context, string) (*model.MaclawResourcePublication, error)
}

type ResourceShadowStore interface {
	GetShadow(context.Context, uuid.UUID, string, string) (*model.MaclawResourceShadow, error)
	UpsertShadow(context.Context, *model.MaclawResourceShadow) error
}

type ResourceProjectionService struct {
	provider     GatewayProvider
	publications ResourcePublicationStore
	shadows      ResourceShadowStore
	capabilities RuntimeCapabilities
}

func NewResourceProjectionService(provider GatewayProvider, publications ResourcePublicationStore, shadows ResourceShadowStore) *ResourceProjectionService {
	return &ResourceProjectionService{
		provider:     provider,
		publications: publications,
		shadows:      shadows,
		capabilities: ShadowRuntimeCapabilities(),
	}
}

func (s *ResourceProjectionService) SetRuntimeCapabilities(capabilities RuntimeCapabilities) {
	if s != nil {
		s.capabilities = capabilities.normalized()
	}
}

func (s *ResourceProjectionService) RecordExpertResource(ctx context.Context, identity RuntimeIdentity, session *GatewaySession, resource *EvaluationResourceSummary) error {
	if s == nil || s.publications == nil || resource == nil || strings.TrimSpace(identity.Role) != "expert" {
		return nil
	}
	expertID, err := uuid.Parse(strings.TrimSpace(identity.UserID))
	if err != nil {
		return fmt.Errorf("parse expert user id: %w", err)
	}
	if resource.Status != EvaluationResourceStatusPublished || !resource.Enabled {
		return s.publications.MarkPublicationUnavailable(ctx, expertID, resource.ID)
	}
	tenantID := ""
	if session != nil && session.Mapping != nil {
		tenantID = session.Mapping.MaclawTenantID
	}
	return s.publications.UpsertPublication(ctx, &model.MaclawResourcePublication{
		SourceExpertUserID:   expertID,
		SourceMaclawTenantID: tenantID,
		SourceResourceID:     resource.ID,
		SourceResourceHandle: resource.Handle,
		SourceVersion:        normalizeVersion(resource.Version),
		Name:                 resource.Name,
		Kind:                 string(resource.Kind),
		Status:               string(resource.Status),
		Enabled:              resource.Enabled,
		Summary:              resource.Summary,
		AssessmentTypes:      append([]string(nil), resource.AssessmentTypes...),
		Tags:                 append([]string(nil), resource.Tags...),
		Metadata:             cloneMetadata(resource.Metadata),
	})
}

// CatalogCaller says who the catalog is being rendered for. The zero value
// keeps the enterprise behavior byte-for-byte: no ownership tags, owner
// identity present. An expert may browse the published engine-package
// catalog (read-only safe metadata, the one exception to "experts see only
// their own tenant"), and that view must mark the caller's own rows
// (UserID) and must not reveal who published anyone else's (HideOwnerMetadata).
type CatalogCaller struct {
	UserID            string
	HideOwnerMetadata bool
}

func (s *ResourceProjectionService) ListEnterpriseCatalog(ctx context.Context, own []EvaluationResourceSummary, q EvaluationResourceQuery, caller CatalogCaller) ([]EvaluationResourceSummary, error) {
	if s == nil || s.publications == nil {
		return own, nil
	}
	publications, err := s.publications.ListPublished(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list maclaw resource publications: %w", err)
	}
	callerUserID := strings.TrimSpace(caller.UserID)
	ownIDs := make(map[string]struct{}, len(own))
	out := append([]EvaluationResourceSummary{}, own...)
	for i := range out {
		ownIDs[out[i].ID] = struct{}{}
		out[i] = markOwnCatalogItem(out[i], callerUserID)
	}
	for _, pub := range publications {
		// The caller's own row already carries this package with its FULL
		// status (draft / archived / disabled included); the publication row
		// would be a stale duplicate of the same fact.
		if _, duplicate := ownIDs[pub.SourceResourceID]; duplicate {
			continue
		}
		item := s.publicationSummary(pub)
		if caller.HideOwnerMetadata {
			delete(item.Metadata, "source_expert_user_id")
		}
		if callerUserID != "" && strings.EqualFold(pub.SourceExpertUserID.String(), callerUserID) {
			item = markOwnCatalogItem(item, callerUserID)
		}
		out = append(out, item)
	}
	return out, nil
}

// markOwnCatalogItem copies the metadata before tagging: the items in `own`
// belong to the caller's gateway response, and mutating their maps in place
// would leak this view's rendering decision into the store layer's data.
func markOwnCatalogItem(summary EvaluationResourceSummary, callerUserID string) EvaluationResourceSummary {
	if callerUserID == "" {
		return summary
	}
	metadata := cloneMetadata(summary.Metadata)
	metadata["is_own"] = metadataTrue
	summary.Metadata = metadata
	return summary
}

func (s *ResourceProjectionService) SyncEnterprisePublishedResources(ctx context.Context, identity RuntimeIdentity, enterpriseSession *GatewaySession) error {
	if !isEnterpriseLike(identity.Role) || s == nil || s.publications == nil {
		return nil
	}
	if s.capabilities.SupportsNativeResourceProjection() {
		return nil
	}
	publications, err := s.publications.ListPublished(ctx, EvaluationResourceQuery{})
	if err != nil {
		return fmt.Errorf("list published resources: %w", err)
	}
	for _, pub := range publications {
		if _, err := s.ensureShadow(ctx, identity, enterpriseSession, &pub); err != nil {
			return err
		}
	}
	return nil
}

func (s *ResourceProjectionService) ResolveEnterpriseRunResourceHandles(ctx context.Context, identity RuntimeIdentity, enterpriseSession *GatewaySession, handles []string) ([]string, error) {
	if !isEnterpriseLike(identity.Role) || s == nil || s.publications == nil {
		return handles, nil
	}
	if s.capabilities.SupportsNativeResourceProjection() {
		return append([]string(nil), handles...), nil
	}
	out := append([]string(nil), handles...)
	for i, handle := range out {
		pub, err := s.publications.GetPublishedByHandle(ctx, strings.TrimSpace(handle))
		if err != nil {
			return nil, fmt.Errorf("lookup published resource: %w", err)
		}
		if pub == nil {
			continue
		}
		shadowHandle, err := s.ensureShadow(ctx, identity, enterpriseSession, pub)
		if err != nil {
			return nil, err
		}
		out[i] = shadowHandle
	}
	return out, nil
}

func (s *ResourceProjectionService) PrepareEnterprisePublishedResources(ctx context.Context, identity RuntimeIdentity, enterpriseSession *GatewaySession, refs []string) error {
	if !isEnterpriseLike(identity.Role) || s == nil || s.publications == nil || len(refs) == 0 {
		return nil
	}
	if s.capabilities.SupportsNativeResourceProjection() {
		return nil
	}
	for _, ref := range refs {
		pub, err := s.publications.GetPublishedByHandle(ctx, strings.TrimSpace(ref))
		if err != nil {
			return fmt.Errorf("lookup published resource: %w", err)
		}
		if pub == nil {
			continue
		}
		if _, err := s.ensureShadow(ctx, identity, enterpriseSession, pub); err != nil {
			return err
		}
	}
	return nil
}

func (s *ResourceProjectionService) ensureShadow(ctx context.Context, identity RuntimeIdentity, enterpriseSession *GatewaySession, pub *model.MaclawResourcePublication) (string, error) {
	if s.provider == nil || s.shadows == nil {
		return "", errors.New("maclaw resource projection is not configured")
	}
	enterpriseID, err := uuid.Parse(strings.TrimSpace(identity.UserID))
	if err != nil {
		return "", fmt.Errorf("parse enterprise user id: %w", err)
	}
	version := normalizeVersion(pub.SourceVersion)
	existing, err := s.shadows.GetShadow(ctx, enterpriseID, pub.SourceResourceID, version)
	if err != nil {
		return "", fmt.Errorf("load maclaw resource shadow: %w", err)
	}
	if existing != nil && strings.TrimSpace(existing.ShadowResourceHandle) != "" && existing.SyncStatus == shadowSyncReady {
		return existing.ShadowResourceHandle, nil
	}
	sourceSession, err := s.provider.Resolve(ctx, RuntimeIdentity{UserID: pub.SourceExpertUserID.String(), Role: "expert"})
	if err != nil {
		return "", fmt.Errorf("resolve source expert maclaw identity: %w", err)
	}
	if sourceSession == nil || sourceSession.Client == nil {
		return "", errors.New("source expert maclaw identity is not configured")
	}
	if enterpriseSession == nil || enterpriseSession.Client == nil {
		return "", errors.New("enterprise maclaw identity is not configured")
	}
	materialized, err := sourceSession.Client.MaterializeEvaluationResource(ctx, pub.SourceResourceHandle)
	if err != nil {
		return "", fmt.Errorf("materialize source expert resource: %w", err)
	}
	metadata := cloneMetadata(pub.Metadata)
	metadata["source_expert_user_id"] = pub.SourceExpertUserID.String()
	metadata["source_resource_id"] = pub.SourceResourceID
	metadata["source_resource_handle"] = pub.SourceResourceHandle
	metadata["source_version"] = version
	metadata["shadow_created_by"] = shadowCreatedBy
	shadow, err := enterpriseSession.Client.SaveEvaluationResource(ctx, EvaluationResourceInput{
		Name:            firstNonEmptyString(pub.Name, materialized.Name),
		Kind:            EvaluationResourceKind(firstNonEmptyString(pub.Kind, string(materialized.Kind))),
		Version:         version,
		Status:          EvaluationResourceStatusPublished,
		Enabled:         true,
		AssessmentTypes: append([]string(nil), pub.AssessmentTypes...),
		Tags:            append([]string(nil), pub.Tags...),
		Summary:         pub.Summary,
		Payload:         materialized.Payload,
		Metadata:        metadata,
	})
	if err != nil {
		return "", fmt.Errorf("create enterprise shadow resource: %w", err)
	}
	enterpriseTenantID := ""
	if enterpriseSession.Mapping != nil {
		enterpriseTenantID = enterpriseSession.Mapping.MaclawTenantID
	}
	if err := s.shadows.UpsertShadow(ctx, &model.MaclawResourceShadow{
		EnterpriseUserID:         enterpriseID,
		EnterpriseMaclawTenantID: enterpriseTenantID,
		SourceExpertUserID:       pub.SourceExpertUserID,
		SourceResourceID:         pub.SourceResourceID,
		SourceVersion:            version,
		ShadowResourceID:         shadow.ID,
		ShadowResourceHandle:     shadow.Handle,
		SyncStatus:               shadowSyncReady,
	}); err != nil {
		return "", fmt.Errorf("store maclaw resource shadow: %w", err)
	}
	return shadow.Handle, nil
}

func (s *ResourceProjectionService) publicationSummary(pub model.MaclawResourcePublication) EvaluationResourceSummary {
	metadata := cloneMetadata(pub.Metadata)
	if s != nil && s.capabilities.SupportsNativeResourceProjection() {
		metadata["requires_shadow_copy"] = "false"
		metadata["maclaw_native_catalog"] = metadataTrue
	} else {
		metadata["requires_shadow_copy"] = metadataTrue
	}
	metadata["source_expert_user_id"] = pub.SourceExpertUserID.String()
	metadata["source_resource_id"] = pub.SourceResourceID
	metadata["source_version"] = normalizeVersion(pub.SourceVersion)
	return EvaluationResourceSummary{
		ID:              pub.SourceResourceID,
		Handle:          pub.SourceResourceHandle,
		Name:            pub.Name,
		Kind:            EvaluationResourceKind(pub.Kind),
		Version:         normalizeVersion(pub.SourceVersion),
		Status:          EvaluationResourceStatus(pub.Status),
		Enabled:         pub.Enabled,
		AssessmentTypes: append([]string(nil), pub.AssessmentTypes...),
		Tags:            append([]string(nil), pub.Tags...),
		Summary:         pub.Summary,
		Metadata:        metadata,
		CreatedAt:       pub.CreatedAt,
		UpdatedAt:       pub.UpdatedAt,
	}
}

func isEnterpriseLike(role string) bool {
	role = strings.TrimSpace(role)
	return role == "enterprise" || role == "admin"
}

func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "default"
	}
	return version
}

func cloneMetadata(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.TrimSpace(k) != "" {
			out[k] = v
		}
	}
	return out
}
