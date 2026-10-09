package maclaw

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
)

type SkillPublicationStore interface {
	UpsertSkillPublication(context.Context, *model.MaclawSkillPublication) error
	MarkSkillPublicationUnavailable(context.Context, uuid.UUID, string) error
	ListPublishedSkills(context.Context, SkillSearchInput) ([]model.MaclawSkillPublication, error)
}

type SkillShadowStore interface {
	GetSkillShadow(context.Context, uuid.UUID, uuid.UUID, string, string) (*model.MaclawSkillShadow, error)
	UpsertSkillShadow(context.Context, *model.MaclawSkillShadow) error
}

type SkillHubConfigProvider interface {
	GetHubConfig(context.Context) (*MaclawHubConfig, error)
}

type SkillAccountMappingStore interface {
	List(context.Context, int, int) ([]model.MaclawAccountMapping, error)
}

type SkillProjectionService struct {
	provider     GatewayProvider
	publications SkillPublicationStore
	shadows      SkillShadowStore
	capabilities RuntimeCapabilities
	hubConfig    SkillHubConfigProvider
	mappings     SkillAccountMappingStore
}

func NewSkillProjectionService(provider GatewayProvider, publications SkillPublicationStore, shadows SkillShadowStore) *SkillProjectionService {
	return &SkillProjectionService{
		provider:     provider,
		publications: publications,
		shadows:      shadows,
		capabilities: ShadowRuntimeCapabilities(),
	}
}

func (s *SkillProjectionService) SetRuntimeCapabilities(capabilities RuntimeCapabilities) {
	if s != nil {
		s.capabilities = capabilities.normalized()
	}
}

func (s *SkillProjectionService) SetHubConfigProvider(provider SkillHubConfigProvider) {
	if s != nil {
		s.hubConfig = provider
	}
}

func (s *SkillProjectionService) SetAccountMappingStore(store SkillAccountMappingStore) {
	if s != nil {
		s.mappings = store
	}
}

func (s *SkillProjectionService) RecordExpertSkills(ctx context.Context, identity RuntimeIdentity, session *GatewaySession, skills []SkillSummary) error {
	if s == nil || s.publications == nil || strings.TrimSpace(identity.Role) != "expert" {
		return nil
	}
	expertID, err := uuid.Parse(strings.TrimSpace(identity.UserID))
	if err != nil {
		return fmt.Errorf("parse expert user id: %w", err)
	}
	tenantID := ""
	if session != nil && session.Mapping != nil {
		tenantID = session.Mapping.MaclawTenantID
	}
	for _, item := range skills {
		if strings.TrimSpace(item.Name) == "" {
			continue
		}
		if !skillIsPublished(item) {
			if err := s.publications.MarkSkillPublicationUnavailable(ctx, expertID, item.Name); err != nil {
				return err
			}
			continue
		}
		metadata := cloneMetadata(item.Metadata)
		metadata["source"] = strings.TrimSpace(item.Source)
		metadata["type"] = strings.TrimSpace(item.Type)
		metadata["mode"] = strings.TrimSpace(item.Mode)
		if strings.TrimSpace(item.HubSkillID) != "" {
			metadata["hub_skill_id"] = strings.TrimSpace(item.HubSkillID)
		}
		if err := s.publications.UpsertSkillPublication(ctx, &model.MaclawSkillPublication{
			SourceExpertUserID:   expertID,
			SourceMaclawTenantID: tenantID,
			SourceSkillName:      item.Name,
			SourceVersion:        normalizeVersion(item.Version),
			Name:                 item.Name,
			Description:          item.Description,
			Status:               firstNonEmptyString(item.Status, "active"),
			Enabled:              true,
			Triggers:             append([]string(nil), item.Triggers...),
			Tags:                 append([]string(nil), item.Triggers...),
			Metadata:             metadata,
		}); err != nil {
			return err
		}
	}
	return nil
}

// logFailuresSummary 统一投影同步的失败摘要日志（P2-16 合并自三处逐字相同的尾声）。
// wrap 为 true 时返回错误（严格路径），否则仅记录日志（尽力而为路径）。
func logFailuresSummary(prefix, unit string, failures []string) error {
	if len(failures) == 0 {
		return nil
	}
	preview := strings.Join(failures, "; ")
	if len(preview) > 500 {
		preview = preview[:500] + "..."
	}
	log.Printf("[WARN] %s completed with %d failed %s: %s", prefix, len(failures), unit, preview)
	return fmt.Errorf("%s had %d failed %s", prefix, len(failures), unit)
}

func (s *SkillProjectionService) SyncPublishedSkillsToAllEnterpriseMappings(ctx context.Context) error {
	if s == nil || s.publications == nil || s.mappings == nil || s.provider == nil {
		return nil
	}
	if s.capabilities.SupportsNativeSkillProjection() {
		return nil
	}
	publications, err := s.publications.ListPublishedSkills(ctx, SkillSearchInput{})
	if err != nil {
		return fmt.Errorf("list published skills: %w", err)
	}
	publications = latestSkillPublications(publications)
	if len(publications) == 0 {
		return nil
	}
	const pageSize = 200
	var failures []string
	for offset := 0; ; offset += pageSize {
		mappings, err := s.mappings.List(ctx, pageSize, offset)
		if err != nil {
			return fmt.Errorf("list maclaw account mappings: %w", err)
		}
		for _, mapping := range mappings {
			if mapping.PlatformRole != model.RoleEnterprise || mapping.ProvisioningStatus != model.MaclawProvisioningReady {
				continue
			}
			identity := RuntimeIdentity{UserID: mapping.PlatformUserID.String(), Role: string(mapping.PlatformRole)}
			session, err := s.provider.Resolve(ctx, identity)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: resolve enterprise maclaw identity: %v", mapping.PlatformUserID, err))
				continue
			}
			if session == nil {
				failures = append(failures, fmt.Sprintf("%s: resolve enterprise maclaw identity: empty session", mapping.PlatformUserID))
				continue
			}
			for i := range publications {
				if err := s.ensureShadow(ctx, identity, session, &publications[i]); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", mapping.PlatformUserID, err))
				}
			}
		}
		if len(mappings) < pageSize {
			break
		}
	}
	return logFailuresSummary("maclaw skill distribution", "enterprise mapping(s)", failures)
}

func (s *SkillProjectionService) ListEnterpriseCatalog(ctx context.Context, own []SkillSummary, q SkillSearchInput) ([]SkillSummary, error) {
	if s == nil || s.publications == nil {
		return own, nil
	}
	publications, err := s.publications.ListPublishedSkills(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list maclaw skill publications: %w", err)
	}
	publications = latestSkillPublications(publications)
	out := append([]SkillSummary{}, own...)
	for _, pub := range publications {
		if skillPublicationExistsInSummaries(out, pub) {
			continue
		}
		out = append(out, s.skillPublicationSummary(pub))
	}
	return out, nil
}

func (s *SkillProjectionService) SearchEnterpriseCatalog(ctx context.Context, own []SkillSearchResult, q SkillSearchInput) ([]SkillSearchResult, error) {
	if s == nil || s.publications == nil {
		return own, nil
	}
	publications, err := s.publications.ListPublishedSkills(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list maclaw skill publications: %w", err)
	}
	publications = latestSkillPublications(publications)
	out := append([]SkillSearchResult{}, own...)
	for _, pub := range publications {
		if skillPublicationExistsInSearchResults(out, pub) {
			continue
		}
		metadata := cloneMetadata(pub.Metadata)
		if s.capabilities.SupportsNativeSkillProjection() {
			metadata["requires_shadow_copy"] = "false"
			metadata["maclaw_native_catalog"] = metadataTrue
		} else {
			metadata["requires_shadow_copy"] = metadataTrue
		}
		metadata["source_expert_user_id"] = pub.SourceExpertUserID.String()
		metadata["source_skill_name"] = pub.SourceSkillName
		metadata["source_version"] = normalizeVersion(pub.SourceVersion)
		out = append(out, SkillSearchResult{
			Source:      "expert_publication",
			ID:          pub.SourceSkillName,
			Name:        pub.Name,
			Description: pub.Description,
			Version:     normalizeVersion(pub.SourceVersion),
			Tags:        append([]string(nil), pub.Tags...),
			Installed:   false,
			Metadata:    metadata,
		})
	}
	return out, nil
}

func skillPublicationExistsInSummaries(items []SkillSummary, pub model.MaclawSkillPublication) bool {
	pubKeys := skillPublicationDedupKeys(pub)
	for _, item := range items {
		if skillKeySetsOverlap(pubKeys, skillSummaryDedupKeys(item)) {
			return true
		}
	}
	return false
}

func skillPublicationExistsInSearchResults(items []SkillSearchResult, pub model.MaclawSkillPublication) bool {
	pubKeys := skillPublicationDedupKeys(pub)
	for _, item := range items {
		if skillKeySetsOverlap(pubKeys, skillSearchResultDedupKeys(item)) {
			return true
		}
	}
	return false
}

func skillPublicationDedupKeys(pub model.MaclawSkillPublication) map[string]struct{} {
	keys := map[string]struct{}{}
	addSkillDedupKey(keys, pub.SourceSkillName)
	addSkillDedupKey(keys, pub.Name)
	addSkillDedupKey(keys, publishedHubSkillID(&pub))
	return keys
}

func skillSummaryDedupKeys(item SkillSummary) map[string]struct{} {
	keys := map[string]struct{}{}
	addSkillDedupKey(keys, item.Name)
	addSkillDedupKey(keys, item.HubSkillID)
	if item.Metadata != nil {
		addSkillDedupKey(keys, item.Metadata["hub_skill_id"])
		addSkillDedupKey(keys, item.Metadata["source_skill_name"])
	}
	return keys
}

func skillSearchResultDedupKeys(item SkillSearchResult) map[string]struct{} {
	keys := map[string]struct{}{}
	addSkillDedupKey(keys, item.ID)
	addSkillDedupKey(keys, item.Name)
	if item.Metadata != nil {
		addSkillDedupKey(keys, item.Metadata["hub_skill_id"])
		addSkillDedupKey(keys, item.Metadata["source_skill_name"])
	}
	return keys
}

func addSkillDedupKey(keys map[string]struct{}, value string) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return
	}
	keys[value] = struct{}{}
	if canonical := canonicalSkillProjectionRef(value); canonical != "" {
		keys[canonical] = struct{}{}
	}
}

func skillKeySetsOverlap(a, b map[string]struct{}) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for key := range a {
		if _, ok := b[key]; ok {
			return true
		}
	}
	return false
}

func (s *SkillProjectionService) SyncEnterprisePublishedSkills(ctx context.Context, identity RuntimeIdentity, enterpriseSession *GatewaySession) error {
	if !isEnterpriseLike(identity.Role) || s == nil || s.publications == nil {
		return nil
	}
	if s.capabilities.SupportsNativeSkillProjection() {
		return nil
	}
	publications, err := s.publications.ListPublishedSkills(ctx, SkillSearchInput{})
	if err != nil {
		return fmt.Errorf("list published skills: %w", err)
	}
	publications = latestSkillPublications(publications)
	for _, pub := range publications {
		if err := s.ensureShadow(ctx, identity, enterpriseSession, &pub); err != nil {
			return err
		}
	}
	return nil
}

func (s *SkillProjectionService) SyncEnterprisePublishedHubSkillsBestEffort(ctx context.Context, identity RuntimeIdentity, enterpriseSession *GatewaySession) error {
	if !isEnterpriseLike(identity.Role) || s == nil || s.publications == nil {
		return nil
	}
	if s.capabilities.SupportsNativeSkillProjection() {
		return nil
	}
	publications, err := s.publications.ListPublishedSkills(ctx, SkillSearchInput{})
	if err != nil {
		return fmt.Errorf("list published skills: %w", err)
	}
	publications = latestSkillPublications(publications)
	var failures []string
	for _, pub := range publications {
		if publishedHubSkillID(&pub) == "" {
			continue
		}
		if err := s.ensureShadow(ctx, identity, enterpriseSession, &pub); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", pub.SourceSkillName, err))
		}
	}
	logFailuresSummary("maclaw enterprise hub skill backfill", "skill(s)", failures)
	return nil
}

func (s *SkillProjectionService) SyncExpertPublishedHubSkillsBestEffort(ctx context.Context, identity RuntimeIdentity, expertSession *GatewaySession) error {
	if strings.TrimSpace(identity.Role) != "expert" || s == nil || s.publications == nil || expertSession == nil || expertSession.Client == nil {
		return nil
	}
	expertID, err := uuid.Parse(strings.TrimSpace(identity.UserID))
	if err != nil {
		return fmt.Errorf("parse expert user id: %w", err)
	}
	currentTenantID := ""
	if expertSession.Mapping != nil {
		currentTenantID = strings.TrimSpace(expertSession.Mapping.MaclawTenantID)
	}
	publications, err := s.publications.ListPublishedSkills(ctx, SkillSearchInput{})
	if err != nil {
		return fmt.Errorf("list published skills: %w", err)
	}
	publications = latestSkillPublications(publications)
	hubURL, err := s.skillHubURL(ctx)
	if err != nil {
		return fmt.Errorf("load maclaw skill hub config: %w", err)
	}
	var failures []string
	for _, pub := range publications {
		if pub.SourceExpertUserID != expertID {
			continue
		}
		hubSkillID := firstNonEmptyHubSkillID(&pub)
		if strings.TrimSpace(hubSkillID) == "" {
			continue
		}
		if currentTenantID != "" && strings.TrimSpace(pub.SourceMaclawTenantID) == currentTenantID {
			continue
		}
		installed, err := expertSession.Client.InstallSkill(ctx, SkillInstallInput{
			Source:      DefaultHubSkillSource,
			SkillHubURL: hubURL,
			SkillID:     hubSkillID,
			Overwrite:   true,
		})
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", pub.SourceSkillName, err))
			continue
		}
		if len(installed) == 0 {
			installed = []SkillSummary{s.skillPublicationSummary(pub)}
			installed[0].Source = DefaultHubSkillSource
			installed[0].HubSkillID = hubSkillID
			installed[0].Metadata["hub_skill_id"] = hubSkillID
		}
		if err := s.RecordExpertSkills(ctx, identity, expertSession, installed); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", pub.SourceSkillName, err))
		}
	}
	logFailuresSummary("maclaw expert hub skill backfill", "skill(s)", failures)
	return nil
}

func (s *SkillProjectionService) PrepareEnterprisePublishedSkills(ctx context.Context, identity RuntimeIdentity, enterpriseSession *GatewaySession, refs []string) error {
	if !isEnterpriseLike(identity.Role) || s == nil || s.publications == nil || len(refs) == 0 {
		return nil
	}
	if s.capabilities.SupportsNativeSkillProjection() {
		return nil
	}
	publications, err := s.publications.ListPublishedSkills(ctx, SkillSearchInput{})
	if err != nil {
		return fmt.Errorf("list published skills: %w", err)
	}
	publications = latestSkillPublications(publications)
	wanted := map[string]bool{}
	for _, ref := range refs {
		ref = strings.ToLower(strings.TrimSpace(ref))
		if ref != "" {
			wanted[ref] = true
			if canonical := canonicalSkillProjectionRef(ref); canonical != "" {
				wanted[canonical] = true
			}
		}
	}
	var matched bool
	var lastErr error
	for _, pub := range publications {
		sourceName := strings.ToLower(strings.TrimSpace(pub.SourceSkillName))
		displayName := strings.ToLower(strings.TrimSpace(pub.Name))
		if !wanted[sourceName] && !wanted[displayName] && !wanted[canonicalSkillProjectionRef(sourceName)] && !wanted[canonicalSkillProjectionRef(displayName)] {
			continue
		}
		matched = true
		if publishedHubSkillID(&pub) == "" {
			lastErr = fmt.Errorf("published skill %q is missing hub_skill_id; republish it through Hub", pub.SourceSkillName)
			continue
		}
		if err := s.ensureShadow(ctx, identity, enterpriseSession, &pub); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if matched && lastErr != nil {
		return lastErr
	}
	return nil
}

func latestSkillPublications(items []model.MaclawSkillPublication) []model.MaclawSkillPublication {
	if len(items) <= 1 {
		return items
	}
	selected := make(map[string]model.MaclawSkillPublication, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.SourceExpertUserID.String())) + "|" + strings.ToLower(strings.TrimSpace(item.SourceSkillName))
		if strings.TrimSpace(item.SourceSkillName) == "" {
			key = strings.ToLower(strings.TrimSpace(item.SourceExpertUserID.String())) + "|" + strings.ToLower(strings.TrimSpace(item.Name))
		}
		if key == "|" {
			key = strings.ToLower(strings.TrimSpace(item.Name)) + "|" + normalizeVersion(item.SourceVersion)
		}
		current, ok := selected[key]
		if !ok {
			selected[key] = item
			order = append(order, key)
			continue
		}
		if skillPublicationIsNewer(item, current) {
			selected[key] = item
		}
	}
	out := make([]model.MaclawSkillPublication, 0, len(order))
	for _, key := range order {
		out = append(out, selected[key])
	}
	return out
}

func skillPublicationIsNewer(candidate, current model.MaclawSkillPublication) bool {
	if !candidate.UpdatedAt.Equal(current.UpdatedAt) {
		return candidate.UpdatedAt.After(current.UpdatedAt)
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	candidateHub := publishedHubSkillID(&candidate) != ""
	currentHub := publishedHubSkillID(&current) != ""
	if candidateHub != currentHub {
		return candidateHub
	}
	return normalizeVersion(candidate.SourceVersion) > normalizeVersion(current.SourceVersion)
}

func canonicalSkillProjectionRef(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{"skillhub:", "skill:"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			break
		}
	}
	if before, _, ok := strings.Cut(value, "/"); ok && strings.TrimSpace(before) != "" {
		value = strings.TrimSpace(before)
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *SkillProjectionService) ensureShadow(ctx context.Context, identity RuntimeIdentity, enterpriseSession *GatewaySession, pub *model.MaclawSkillPublication) error {
	if s.provider == nil || s.shadows == nil {
		return errors.New("maclaw skill projection is not configured")
	}
	enterpriseID, err := uuid.Parse(strings.TrimSpace(identity.UserID))
	if err != nil {
		return fmt.Errorf("parse enterprise user id: %w", err)
	}
	version := normalizeVersion(pub.SourceVersion)
	existing, err := s.shadows.GetSkillShadow(ctx, enterpriseID, pub.SourceExpertUserID, pub.SourceSkillName, version)
	if err != nil {
		return fmt.Errorf("load maclaw skill shadow: %w", err)
	}
	if enterpriseSession == nil || enterpriseSession.Client == nil {
		return errors.New("enterprise maclaw identity is not configured")
	}
	enterpriseTenantID := ""
	if enterpriseSession.Mapping != nil {
		enterpriseTenantID = enterpriseSession.Mapping.MaclawTenantID
	}
	if existing != nil &&
		strings.TrimSpace(existing.ShadowSkillName) != "" &&
		existing.SyncStatus == shadowSyncReady &&
		(enterpriseTenantID == "" || strings.TrimSpace(existing.EnterpriseMaclawTenantID) == strings.TrimSpace(enterpriseTenantID)) {
		return nil
	}
	hubURL, err := s.skillHubURL(ctx)
	if err != nil {
		return fmt.Errorf("load maclaw skill hub config: %w", err)
	}
	hubSkillID := firstNonEmptyHubSkillID(pub)
	if strings.TrimSpace(hubSkillID) == "" {
		return fmt.Errorf("published skill %q is missing hub_skill_id; republish it through Hub", pub.SourceSkillName)
	}
	imported, err := enterpriseSession.Client.InstallSkill(ctx, SkillInstallInput{
		Source:      DefaultHubSkillSource,
		SkillHubURL: hubURL,
		SkillID:     hubSkillID,
		Overwrite:   true,
	})
	if err != nil {
		return fmt.Errorf("install enterprise skill from hub: %w", err)
	}
	shadowName := pub.SourceSkillName
	if len(imported) > 0 && strings.TrimSpace(imported[0].Name) != "" {
		shadowName = imported[0].Name
	}
	if err := s.shadows.UpsertSkillShadow(ctx, &model.MaclawSkillShadow{
		EnterpriseUserID:         enterpriseID,
		EnterpriseMaclawTenantID: enterpriseTenantID,
		SourceExpertUserID:       pub.SourceExpertUserID,
		SourceSkillName:          pub.SourceSkillName,
		SourceVersion:            version,
		ShadowSkillName:          shadowName,
		SyncStatus:               shadowSyncReady,
	}); err != nil {
		return fmt.Errorf("store maclaw skill shadow: %w", err)
	}
	return nil
}

func (s *SkillProjectionService) skillHubURL(ctx context.Context) (string, error) {
	if s == nil || s.hubConfig == nil {
		return "", errors.New("maclaw skill hub config is not configured")
	}
	cfg, err := s.hubConfig.GetHubConfig(ctx)
	if err != nil {
		return "", err
	}
	if cfg == nil || !cfg.Enabled || strings.TrimSpace(cfg.HubURL) == "" {
		return "", errors.New("maclaw skill hub is not enabled")
	}
	for _, source := range cfg.AllowedSources {
		if strings.ToLower(strings.TrimSpace(source)) == DefaultHubSkillSource {
			return strings.TrimSpace(cfg.HubURL), nil
		}
	}
	return "", errors.New("skillhub source is not allowed")
}

func firstNonEmptyHubSkillID(pub *model.MaclawSkillPublication) string {
	if value := publishedHubSkillID(pub); value != "" {
		return value
	}
	return strings.TrimSpace(pub.SourceSkillName)
}

func publishedHubSkillID(pub *model.MaclawSkillPublication) string {
	if pub == nil {
		return ""
	}
	if pub.Metadata != nil {
		if value := strings.TrimSpace(pub.Metadata["hub_skill_id"]); value != "" {
			return value
		}
	}
	return ""
}

func (s *SkillProjectionService) skillPublicationSummary(pub model.MaclawSkillPublication) SkillSummary {
	metadata := cloneMetadata(pub.Metadata)
	if s != nil && s.capabilities.SupportsNativeSkillProjection() {
		metadata["requires_shadow_copy"] = "false"
		metadata["maclaw_native_catalog"] = metadataTrue
	} else {
		metadata["requires_shadow_copy"] = metadataTrue
	}
	metadata["source_expert_user_id"] = pub.SourceExpertUserID.String()
	metadata["source_skill_name"] = pub.SourceSkillName
	metadata["source_version"] = normalizeVersion(pub.SourceVersion)
	return SkillSummary{
		Name:        pub.Name,
		Description: pub.Description,
		Triggers:    append([]string(nil), pub.Triggers...),
		Status:      pub.Status,
		Source:      "expert_publication",
		Version:     normalizeVersion(pub.SourceVersion),
		Metadata:    metadata,
	}
}

func skillIsPublished(item SkillSummary) bool {
	status := strings.TrimSpace(item.Status)
	return status == "" || status == "active" || status == "published" || status == "enabled"
}

