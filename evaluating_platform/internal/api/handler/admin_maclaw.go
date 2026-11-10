package handler

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/repository"
	"evaluating_platform/pkg/logger"
)

type AdminMaclawHandler struct {
	userRepo        *repository.UserRepository
	mappingRepo     *repository.MaclawAccountMappingRepository
	publicationRepo *repository.MaclawResourcePublicationRepository
	shadowRepo      *repository.MaclawResourceShadowRepository
	// resourceRepo lets governance reach the expert's own row. Archiving only
	// the publication left the expert list showing 「published」 for a package no
	// enterprise could use (2026-10-26).
	resourceRepo      *repository.MaclawResourceRecordRepository
	engineAdmin       *maclaw.EngineInstanceAdmin
	embeddedRepo      *repository.MaclawEmbeddedPluginRepository
	embeddedLifecycle *maclaw.EmbeddedPluginLifecycle
	resourceStore     *maclaw.ResourceStoreService
	provider          maclaw.GatewayProvider
	configService     *maclaw.RuntimeConfigService
	hubService        *maclaw.MaclawHubConfigService
	runtimeMode       string
}

func NewAdminMaclawHandler(
	userRepo *repository.UserRepository,
	mappingRepo *repository.MaclawAccountMappingRepository,
	publicationRepo *repository.MaclawResourcePublicationRepository,
	shadowRepo *repository.MaclawResourceShadowRepository,
	resourceRepo *repository.MaclawResourceRecordRepository,
	provider maclaw.GatewayProvider,
	configService *maclaw.RuntimeConfigService,
	hubService *maclaw.MaclawHubConfigService,
	runtimeMode ...string,
) *AdminMaclawHandler {
	mode := "compose"
	if len(runtimeMode) > 0 && strings.TrimSpace(runtimeMode[0]) != "" {
		mode = strings.TrimSpace(runtimeMode[0])
	}
	return &AdminMaclawHandler{
		userRepo:        userRepo,
		mappingRepo:     mappingRepo,
		publicationRepo: publicationRepo,
		shadowRepo:      shadowRepo,
		resourceRepo:    resourceRepo,
		provider:        provider,
		configService:   configService,
		hubService:      hubService,
		runtimeMode:     mode,
	}
}

func (h *AdminMaclawHandler) Overview(c *gin.Context) {
	roleCounts, err := h.userRepo.CountByRole(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count users"})
		return
	}
	published, totalResources, _ := h.publicationRepo.Count(c.Request.Context())
	shadowCount, _ := h.shadowRepo.Count(c.Request.Context())
	mappingCount, _ := h.mappingRepo.Count(c.Request.Context())
	totalUsers := 0
	for _, count := range roleCounts {
		totalUsers += count
	}
	c.JSON(http.StatusOK, gin.H{
		"total_users":                 totalUsers,
		"users_by_role":               roleCounts,
		"maclaw_mapped_accounts":      mappingCount,
		"published_resources":         published,
		"total_resource_publications": totalResources,
		"shadow_resources":            shadowCount,
		"maclaw_runtime_configured":   h.provider != nil && h.provider.Enabled(),
	})
}

func (h *AdminMaclawHandler) ListAccounts(c *gin.Context) {
	limit := parseAdminLimit(c, 50, 200)
	offset := parseAdminOffset(c)
	role := c.Query("role")
	keyword := c.Query("keyword")
	users, total, err := h.userRepo.ListAll(c.Request.Context(), limit, offset, role, keyword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list users"})
		return
	}
	// P26c.1 badges: which accounts carry a model mirror / a registered
	// dedicated engine. A failed badge query must never fail the account
	// list — badges err toward absent.
	var overrideSet map[uuid.UUID]bool
	if h.configService != nil {
		if set, berr := h.configService.UserOverrideIDSet(c.Request.Context()); berr == nil {
			overrideSet = set
		}
	}
	items := make([]gin.H, 0, len(users))
	for _, user := range users {
		item := gin.H{
			"platform_user_id":    user.ID,
			"email":               user.Email,
			"name":                user.Name,
			"role":                user.Role,
			"org_name":            user.OrgName,
			"is_active":           user.IsActive,
			"created_at":          user.CreatedAt,
			"updated_at":          user.UpdatedAt,
			"model_config_status": "not_provisioned",
		}
		mapping, err := h.mappingRepo.GetByPlatformUserID(c.Request.Context(), user.ID)
		if err == nil && mapping != nil {
			item["maclaw_tenant_id"] = mapping.MaclawTenantID
			item["maclaw_user_id"] = mapping.MaclawUserID
			item["maclaw_instance_id"] = mapping.MaclawInstanceID
			item["provisioning_status"] = mapping.ProvisioningStatus
			item["last_error"] = mapping.LastError
			item["model_config_status"] = "unknown"
			registered := strings.TrimSpace(mapping.EngineBaseURL) != ""
			engineInstance := gin.H{
				"registered": registered,
				"base_url":   mapping.EngineBaseURL,
			}
			// P26c.2: registered instances get ONE authenticated probe so the
			// account list shows whether the container actually holds the
			// CURRENT token (registered-but-stale-Env would otherwise look
			// identical to healthy and fail only at the first routed run).
			// Probe failure errs toward omitting the badge, never the list.
			if registered && h.engineAdmin != nil {
				if health, perr := h.engineAdmin.Probe(c.Request.Context(), user.ID); perr == nil {
					engineInstance["health"] = string(health)
				}
			}
			item["engine_instance"] = engineInstance
		}
		item["model_override"] = overrideSet[user.ID]
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (h *AdminMaclawHandler) GetAccountConfig(c *gin.Context) {
	session, ok := h.resolveAccountSession(c)
	if !ok {
		return
	}
	cfg, err := session.Client.GetRuntimeConfig(c.Request.Context())
	if err != nil {
		writeSafeMaclawAdminError(c, err)
		return
	}
	cfg.AppConfig = maclaw.MaskRuntimeAppConfig(cfg.AppConfig)
	c.JSON(http.StatusOK, cfg)
}

func (h *AdminMaclawHandler) UpdateAccountConfig(c *gin.Context) {
	session, ok := h.resolveAccountSession(c)
	if !ok {
		return
	}
	var in maclaw.RuntimeAppConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid maclaw model config"})
		return
	}
	h.normalizeRuntimeConfigLocalhostURLs(&in)
	current, err := session.Client.GetRuntimeConfig(c.Request.Context())
	if err != nil {
		writeSafeMaclawAdminError(c, err)
		return
	}
	in = mergeAccountRuntimeConfigSecrets(current, in)
	cfg, err := session.Client.UpdateRuntimeConfig(c.Request.Context(), in)
	if err != nil {
		writeSafeMaclawAdminError(c, err)
		return
	}
	refreshMaclawInstanceReadiness(c, session)
	// P26b mirror: persist exactly the config WE sent the runtime (browser
	// payload already merged with the runtime's current values by
	// mergeAccountRuntimeConfigSecrets — so every real secret is present
	// regardless of what the browser echoed). Mirroring the runtime RESPONSE
	// was rejected: if an upstream ever learns to mask secrets in responses,
	// the mirror would silently store `******` and never converge. Mirror
	// failure never fails the save — the response says so, the log says so,
	// and the next save converges (merge is idempotent).
	if h.configService.HasOverrideStore() {
		if targetID, perr := uuid.Parse(strings.TrimSpace(c.Param("userId"))); perr == nil {
			var updatedBy *uuid.UUID
			if adminID, aerr := uuid.Parse(strings.TrimSpace(c.GetString("user_id"))); aerr == nil {
				updatedBy = &adminID
			}
			mirrorSaved := true
			if _, _, merr := h.configService.SaveUserRuntimeConfig(c.Request.Context(), targetID, in, updatedBy); merr != nil {
				mirrorSaved = false
				logger.Error("maclaw account model config mirror failed", map[string]interface{}{
					"target_user": targetID.String(),
					"err":         merr.Error(),
				})
			}
			flag := mirrorSaved
			cfg.MirrorSaved = &flag
		}
	}
	cfg.AppConfig = maclaw.MaskRuntimeAppConfig(cfg.AppConfig)
	c.JSON(http.StatusOK, cfg)
}

func mergeAccountRuntimeConfigSecrets(current *maclaw.RuntimeUserConfig, next maclaw.RuntimeAppConfig) maclaw.RuntimeAppConfig {
	if current == nil {
		return next
	}
	return maclaw.MergeRuntimeConfigSecrets(current.AppConfig, next)
}

func (h *AdminMaclawHandler) ValidateAccountConfig(c *gin.Context) {
	session, ok := h.resolveAccountSession(c)
	if !ok {
		return
	}
	candidate, ok := decodeOptionalRuntimeConfig(c)
	if !ok {
		return
	}
	h.normalizeRuntimeConfigLocalhostURLs(candidate)
	out, err := session.Client.ValidateRuntimeConfig(c.Request.Context(), candidate)
	if err != nil {
		writeSafeMaclawAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *AdminMaclawHandler) TestAccountConfig(c *gin.Context) {
	session, ok := h.resolveAccountSession(c)
	if !ok {
		return
	}
	candidate, ok := decodeOptionalRuntimeConfig(c)
	if !ok {
		return
	}
	h.normalizeRuntimeConfigLocalhostURLs(candidate)
	out, err := session.Client.TestRuntimeConfig(c.Request.Context(), candidate)
	if err != nil {
		writeSafeMaclawAdminError(c, err)
		return
	}
	enrichLocalhostConfigTestResult(out, candidate)
	c.JSON(http.StatusOK, out)
}

func (h *AdminMaclawHandler) GetDefaultModelConfig(c *gin.Context) {
	if h.configService == nil || !h.configService.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw model config store is not configured"})
		return
	}
	out, err := h.configService.GetMaskedDefaultRuntimeConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load default maclaw model config"})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *AdminMaclawHandler) UpdateDefaultModelConfig(c *gin.Context) {
	if h.configService == nil || !h.configService.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw model config store is not configured"})
		return
	}
	var in maclaw.RuntimeAppConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid maclaw model config"})
		return
	}
	h.normalizeRuntimeConfigLocalhostURLs(&in)
	var updatedBy *uuid.UUID
	if id, err := uuid.Parse(strings.TrimSpace(c.GetString("user_id"))); err == nil {
		updatedBy = &id
	}
	out, syncConfig, err := h.configService.SaveDefaultRuntimeConfigForSync(c.Request.Context(), in, updatedBy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save default maclaw model config"})
		return
	}
	sync := h.syncDefaultConfigToMappedAccounts(c, *syncConfig)
	c.JSON(http.StatusOK, gin.H{"app_config": out.AppConfig, "updated_at": out.UpdatedAt, "sync": sync})
}

func (h *AdminMaclawHandler) ValidateDefaultModelConfig(c *gin.Context) {
	session, ok := h.resolveCurrentAdminSession(c)
	if !ok {
		return
	}
	candidate, ok := h.decodeDefaultRuntimeConfigCandidate(c)
	if !ok {
		return
	}
	h.normalizeRuntimeConfigLocalhostURLs(candidate)
	out, err := session.Client.ValidateRuntimeConfig(c.Request.Context(), candidate)
	if err != nil {
		writeSafeMaclawAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *AdminMaclawHandler) TestDefaultModelConfig(c *gin.Context) {
	session, ok := h.resolveCurrentAdminSession(c)
	if !ok {
		return
	}
	candidate, ok := h.decodeDefaultRuntimeConfigCandidate(c)
	if !ok {
		return
	}
	h.normalizeRuntimeConfigLocalhostURLs(candidate)
	out, err := session.Client.TestRuntimeConfig(c.Request.Context(), candidate)
	if err != nil {
		writeSafeMaclawAdminError(c, err)
		return
	}
	enrichLocalhostConfigTestResult(out, candidate)
	c.JSON(http.StatusOK, out)
}

func (h *AdminMaclawHandler) GetHubConfig(c *gin.Context) {
	if h.hubService == nil || !h.hubService.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw hub config store is not configured"})
		return
	}
	cfg, err := h.hubService.GetHubConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load maclaw hub config"})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

func (h *AdminMaclawHandler) UpdateHubConfig(c *gin.Context) {
	if h.hubService == nil || !h.hubService.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw hub config store is not configured"})
		return
	}
	var in maclaw.MaclawHubConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid maclaw hub config"})
		return
	}
	var updatedBy *uuid.UUID
	if id, err := uuid.Parse(strings.TrimSpace(c.GetString("user_id"))); err == nil {
		updatedBy = &id
	}
	out, err := h.hubService.SaveHubConfig(c.Request.Context(), in, updatedBy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sync := h.syncHubConfigToMappedAccounts(c)
	c.JSON(http.StatusOK, gin.H{"config": out, "sync": sync})
}

func (h *AdminMaclawHandler) SyncHubConfig(c *gin.Context) {
	if h.hubService == nil || !h.hubService.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw hub config store is not configured"})
		return
	}
	c.JSON(http.StatusOK, h.syncHubConfigToMappedAccounts(c))
}

func (h *AdminMaclawHandler) GetHubConfigStatus(c *gin.Context) {
	items := []gin.H{}
	if h.mappingRepo == nil {
		c.JSON(http.StatusOK, gin.H{"items": items, "total": 0})
		return
	}
	for offset := 0; ; offset += 200 {
		accounts, err := h.mappingRepo.List(c.Request.Context(), 200, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list maclaw account mappings"})
			return
		}
		for _, account := range accounts {
			items = append(items, gin.H{
				"platform_user_id":    account.PlatformUserID,
				"platform_role":       account.PlatformRole,
				"platform_email":      account.PlatformEmail,
				"maclaw_tenant_id":    account.MaclawTenantID,
				"maclaw_user_id":      account.MaclawUserID,
				"maclaw_instance_id":  account.MaclawInstanceID,
				"provisioning_status": account.ProvisioningStatus,
			})
		}
		if len(accounts) < 200 {
			c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
			return
		}
	}
}

func (h *AdminMaclawHandler) ListResources(c *gin.Context) {
	items, err := h.publicationRepo.ListAdmin(c.Request.Context(), maclaw.EvaluationResourceQuery{
		Kind:  maclaw.EvaluationResourceKind(c.Query("kind")),
		Query: c.Query("query"),
		Limit: parseAdminLimit(c, 100, 500),
	}, c.DefaultQuery("include_inactive", "true") == "true")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list maclaw resources"})
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		expertName := ""
		expertEmail := ""
		if user, err := h.userRepo.GetByID(c.Request.Context(), item.SourceExpertUserID); err == nil && user != nil {
			expertName = user.Name
			expertEmail = user.Email
		}
		out = append(out, gin.H{
			"source_expert_user_id":   item.SourceExpertUserID,
			"source_expert_name":      expertName,
			"source_expert_email":     expertEmail,
			"source_maclaw_tenant_id": item.SourceMaclawTenantID,
			"source_resource_id":      item.SourceResourceID,
			"source_resource_handle":  item.SourceResourceHandle,
			"source_version":          item.SourceVersion,
			"name":                    item.Name,
			"kind":                    item.Kind,
			"status":                  item.Status,
			"enabled":                 item.Enabled,
			"summary":                 item.Summary,
			"assessment_types":        item.AssessmentTypes,
			"tags":                    item.Tags,
			"metadata":                item.Metadata,
			"created_at":              item.CreatedAt,
			"updated_at":              item.UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": len(out)})
}

func (h *AdminMaclawHandler) UpdateResourceGovernance(c *gin.Context) {
	var in struct {
		Enabled *bool  `json:"enabled"`
		Status  string `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid resource governance update"})
		return
	}
	status := strings.TrimSpace(in.Status)
	if status != "" && status != "draft" && status != "published" && status != "archived" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid resource status"})
		return
	}
	item, err := h.publicationRepo.UpdateGovernance(c.Request.Context(), c.Param("sourceResourceId"), in.Enabled, status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update maclaw resource"})
		return
	}
	if item == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource publication not found"})
		return
	}
	// The expert's own row follows the publication. Before this, an archived
	// package still read 「published」 in the expert portal — two views of one
	// fact disagreeing, with the admin's action invisible to the owner.
	if h.resourceRepo != nil {
		if err := h.resourceRepo.SetGovernance(c.Request.Context(), c.Param("sourceResourceId"), in.Enabled, status); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update maclaw resource row"})
			return
		}
	}
	c.JSON(http.StatusOK, item)
}

func (h *AdminMaclawHandler) ListJobs(c *gin.Context) {
	accounts, err := h.mappingRepo.List(c.Request.Context(), parseAdminLimit(c, 30, 100), parseAdminOffset(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list maclaw accounts"})
		return
	}
	items := []gin.H{}
	for _, account := range accounts {
		session, err := h.provider.Resolve(c.Request.Context(), maclaw.RuntimeIdentity{
			UserID: account.PlatformUserID.String(),
			Role:   string(account.PlatformRole),
		})
		if err != nil || session == nil || session.Client == nil {
			continue
		}
		jobs, err := session.Client.ListEvaluationJobs(c.Request.Context(), maclaw.EvaluationJobQuery{
			Status: maclaw.EvaluationJobStatus(c.Query("status")),
			Kind:   maclaw.EvaluationJobKind(c.Query("kind")),
			Limit:  10,
		})
		if err != nil {
			continue
		}
		for _, job := range jobs {
			items = append(items, gin.H{
				"platform_user_id":   account.PlatformUserID,
				"platform_role":      account.PlatformRole,
				"platform_email":     account.PlatformEmail,
				"maclaw_tenant_id":   account.MaclawTenantID,
				"maclaw_instance_id": account.MaclawInstanceID,
				"job":                job,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

func (h *AdminMaclawHandler) resolveAccountSession(c *gin.Context) (*maclaw.GatewaySession, bool) {
	if h.provider == nil || !h.provider.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw runtime is not configured"})
		return nil, false
	}
	userID, err := uuid.Parse(strings.TrimSpace(c.Param("userId")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return nil, false
	}
	user, err := h.userRepo.GetByID(c.Request.Context(), userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return nil, false
	}
	session, err := h.provider.Resolve(c.Request.Context(), maclaw.RuntimeIdentity{
		UserID: user.ID.String(),
		Role:   string(user.Role),
	})
	if err != nil {
		writeSafeMaclawAdminError(c, err)
		return nil, false
	}
	return session, true
}

func (h *AdminMaclawHandler) resolveCurrentAdminSession(c *gin.Context) (*maclaw.GatewaySession, bool) {
	if h.provider == nil || !h.provider.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw runtime is not configured"})
		return nil, false
	}
	userID := strings.TrimSpace(c.GetString("user_id"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing admin identity"})
		return nil, false
	}
	session, err := h.provider.Resolve(c.Request.Context(), maclaw.RuntimeIdentity{
		UserID: userID,
		Role:   "admin",
	})
	if err != nil {
		writeSafeMaclawAdminError(c, err)
		return nil, false
	}
	return session, true
}

func (h *AdminMaclawHandler) decodeDefaultRuntimeConfigCandidate(c *gin.Context) (*maclaw.RuntimeAppConfig, bool) {
	candidate, ok := decodeOptionalRuntimeConfig(c)
	if !ok {
		return nil, false
	}
	if candidate != nil && !candidate.IsEmpty() {
		return candidate, true
	}
	if h.configService == nil || !h.configService.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw model config store is not configured"})
		return nil, false
	}
	cfg, err := h.configService.GetDefaultRuntimeConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load default maclaw model config"})
		return nil, false
	}
	if cfg == nil || cfg.IsEmpty() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "default maclaw model config is empty"})
		return nil, false
	}
	return cfg, true
}

func normalizeRuntimeConfigLocalhostURLs(cfg *maclaw.RuntimeAppConfig) {
	normalizeRuntimeConfigLocalhostURLsForRuntimeMode(cfg, "compose")
}

func (h *AdminMaclawHandler) normalizeRuntimeConfigLocalhostURLs(cfg *maclaw.RuntimeAppConfig) {
	mode := "compose"
	if h != nil && strings.TrimSpace(h.runtimeMode) != "" {
		mode = h.runtimeMode
	}
	normalizeRuntimeConfigLocalhostURLsForRuntimeMode(cfg, mode)
}

func normalizeRuntimeConfigLocalhostURLsForRuntimeMode(cfg *maclaw.RuntimeAppConfig, runtimeMode string) {
	if cfg == nil {
		return
	}
	if !shouldRewriteLocalhostForRuntimeMode(runtimeMode) {
		return
	}
	cfg.MaclawLLMUrl = normalizeLocalhostURLForDockerRuntime(cfg.MaclawLLMUrl)
	for i := range cfg.MaclawLLMProviders {
		cfg.MaclawLLMProviders[i].URL = normalizeLocalhostURLForDockerRuntime(cfg.MaclawLLMProviders[i].URL)
	}
}

func shouldRewriteLocalhostForRuntimeMode(runtimeMode string) bool {
	switch strings.ToLower(strings.TrimSpace(runtimeMode)) {
	case "", "compose", "local-build", "docker":
		return true
	case "local", "external":
		return false
	default:
		return true
	}
}

func (h *AdminMaclawHandler) syncDefaultConfigToMappedAccounts(c *gin.Context, cfg maclaw.RuntimeAppConfig) gin.H {
	if h.provider == nil || !h.provider.Enabled() || h.mappingRepo == nil {
		return gin.H{"attempted": 0, "succeeded": 0, "failed": 0}
	}
	return h.forEachMappedAccount(c, cfg)
}

// forEachMappedAccount 分页遍历全部账号映射并应用 runtime 配置补丁（P2-03
// 合并自 default/hub 两个逐字相同的同步循环）。
func (h *AdminMaclawHandler) forEachMappedAccount(c *gin.Context, patch maclaw.RuntimeAppConfig) gin.H {
	result := gin.H{"attempted": 0, "succeeded": 0, "failed": 0}
	for offset := 0; ; offset += 200 {
		accounts, err := h.mappingRepo.List(c.Request.Context(), 200, offset)
		if err != nil {
			result["failed"] = result["failed"].(int) + 1
			return result
		}
		if len(accounts) == 0 {
			return result
		}
		for _, account := range accounts {
			result["attempted"] = result["attempted"].(int) + 1
			session, err := h.provider.Resolve(c.Request.Context(), maclaw.RuntimeIdentity{
				UserID: account.PlatformUserID.String(),
				Role:   string(account.PlatformRole),
			})
			if err != nil || session == nil || session.Client == nil {
				result["failed"] = result["failed"].(int) + 1
				continue
			}
			if err := updateRuntimeConfigPatch(c, session, patch); err != nil {
				result["failed"] = result["failed"].(int) + 1
				continue
			}
			result["succeeded"] = result["succeeded"].(int) + 1
		}
		if len(accounts) < 200 {
			return result
		}
	}
}

func (h *AdminMaclawHandler) syncHubConfigToMappedAccounts(c *gin.Context) gin.H {
	if h.hubService == nil || !h.hubService.Enabled() || h.provider == nil || !h.provider.Enabled() || h.mappingRepo == nil {
		return gin.H{"attempted": 0, "succeeded": 0, "failed": 0}
	}
	patch, err := h.hubService.RuntimeConfigPatch(c.Request.Context())
	if err != nil || patch == nil || patch.IsEmpty() {
		return gin.H{"attempted": 0, "succeeded": 0, "failed": 0}
	}
	return h.forEachMappedAccount(c, *patch)
}

func updateRuntimeConfigPatch(c *gin.Context, session *maclaw.GatewaySession, patch maclaw.RuntimeAppConfig) error {
	current := maclaw.RuntimeAppConfig{}
	if cfg, err := session.Client.GetRuntimeConfig(c.Request.Context()); err == nil && cfg != nil {
		current = cfg.AppConfig
	}
	merged := maclaw.MergeRuntimeConfigPatch(current, patch)
	if _, err := session.Client.UpdateRuntimeConfig(c.Request.Context(), merged); err != nil {
		return err
	}
	refreshMaclawInstanceReadiness(c, session)
	return nil
}

func refreshMaclawInstanceReadiness(c *gin.Context, session *maclaw.GatewaySession) {
	if session == nil || session.Client == nil || strings.TrimSpace(session.InstanceID) == "" {
		return
	}
	_ = session.Client.RefreshRuntimeInstanceReadiness(c.Request.Context(), session.InstanceID)
}

func normalizeLocalhostURLForDockerRuntime(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return raw
	}
	host := strings.ToLower(strings.Trim(parsed.Hostname(), "[]"))
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return raw
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort("host.docker.internal", port)
	} else {
		parsed.Host = "host.docker.internal"
	}
	return parsed.String()
}

func enrichLocalhostConfigTestResult(out *maclaw.RuntimeConfigTestResult, candidate *maclaw.RuntimeAppConfig) {
	if out == nil || out.Success || candidate == nil || !runtimeConfigUsesLocalhost(*candidate) {
		return
	}
	hint := "如果 maclaw-runtime 在 Docker 容器中运行，localhost/127.0.0.1 指向容器自身；请改用 http://host.docker.internal:<port>/v1 访问宿主机模型服务。"
	if strings.TrimSpace(out.Message) == "" {
		out.Message = hint
	} else if !strings.Contains(out.Message, "host.docker.internal") {
		out.Message = out.Message + " " + hint
	}
	if strings.TrimSpace(out.Detail) == "" {
		out.Detail = hint
	}
}

func runtimeConfigUsesLocalhost(cfg maclaw.RuntimeAppConfig) bool {
	values := []string{cfg.MaclawLLMUrl}
	for _, provider := range cfg.MaclawLLMProviders {
		values = append(values, provider.URL)
	}
	for _, value := range values {
		lower := strings.ToLower(strings.TrimSpace(value))
		if strings.Contains(lower, "://localhost") || strings.Contains(lower, "://127.0.0.1") {
			return true
		}
	}
	return false
}

func decodeOptionalRuntimeConfig(c *gin.Context) (*maclaw.RuntimeAppConfig, bool) {
	if c.Request.Body == nil || c.Request.ContentLength == 0 {
		return nil, true
	}
	var in maclaw.RuntimeAppConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid maclaw model config"})
		return nil, false
	}
	return &in, true
}

func parseAdminLimit(c *gin.Context, fallback, max int) int {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(fallback)))
	if limit <= 0 {
		limit = fallback
	}
	if limit > max {
		limit = max
	}
	return limit
}

func parseAdminOffset(c *gin.Context) int {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		return 0
	}
	return offset
}

func writeSafeMaclawAdminError(c *gin.Context, err error) {
	status := http.StatusServiceUnavailable
	var upstream *maclaw.UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode < 500 {
		status = http.StatusBadGateway
	}
	c.JSON(status, gin.H{"error": "maclaw admin operation failed", "code": "maclaw_admin_operation_failed"})
}

// ── P26c · 专属引擎登记（设计稿 §D-2/D-4）──────────────────────────────────
// 明文 token 只在这里出现一次（响应），存储永远是密文；登记前探活，
// 「配了地址但引擎不存在」在登记时暴露而不是第一个 run 提交时。

// SetEmbeddedReviewTools wires the P28 review surface (state rows + machine).
// resourceStore powers the review drawer's source view (R-5 narrow exception).
func (h *AdminMaclawHandler) SetEmbeddedResourceStore(store *maclaw.ResourceStoreService) {
	if h != nil {
		h.resourceStore = store
	}
}
func (h *AdminMaclawHandler) SetEmbeddedReviewTools(repo *repository.MaclawEmbeddedPluginRepository, lifecycle *maclaw.EmbeddedPluginLifecycle) {
	if h != nil {
		h.embeddedRepo = repo
		h.embeddedLifecycle = lifecycle
	}
}

func (h *AdminMaclawHandler) SetEngineInstanceAdmin(admin *maclaw.EngineInstanceAdmin) {
	if h != nil {
		h.engineAdmin = admin
	}
}

func (h *AdminMaclawHandler) RegisterAccountEngine(c *gin.Context) {
	if h.engineAdmin == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "engine instance admin is not configured", "code": "engine_instance_admin_unconfigured"})
		return
	}
	userID, err := uuid.Parse(strings.TrimSpace(c.Param("userId")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id", "code": "invalid_user"})
		return
	}
	var in struct {
		BaseURL string `json:"base_url"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "code": "invalid_request"})
		return
	}
	token, err := h.engineAdmin.Register(c.Request.Context(), userID, in.BaseURL)
	if err != nil {
		code := "engine_instance_unavailable"
		status := http.StatusConflict
		if _, verr := maclaw.ValidateEngineInstanceBaseURL(in.BaseURL); verr != nil {
			code, status = "engine_instance_url_invalid", http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error(), "code": code})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"registered": true,
		"token":      token,
		"note":       "token 仅此一次明文返回：写入部署 .env 并创建该租户的引擎服务块（见 docker-compose.tenant-engines.yml 模板），然后才可开启 MACLAW_TENANT_ENGINES。",
	})
}

func (h *AdminMaclawHandler) UnregisterAccountEngine(c *gin.Context) {
	if h.engineAdmin == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "engine instance admin is not configured", "code": "engine_instance_admin_unconfigured"})
		return
	}
	userID, err := uuid.Parse(strings.TrimSpace(c.Param("userId")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id", "code": "invalid_user"})
		return
	}
	if err := h.engineAdmin.Unregister(c.Request.Context(), userID); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "engine_instance_unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"registered": false})
}

// ── P28 · 内嵌插件审批 ─────────────────────────────────────────────────────

func (h *AdminMaclawHandler) adminActor(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(c.GetString("user_id")))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid user id", "code": "invalid_user"})
		return uuid.Nil, false
	}
	return id, true
}

func (h *AdminMaclawHandler) writeEmbeddedError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, repository.ErrEmbeddedStateConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "该包状态已被其他人变更，请刷新后重试。", "code": "embedded_state_conflict"})
		return
	}
	var verr *maclaw.EmbeddedPluginValidationError
	if errors.As(err, &verr) {
		c.JSON(http.StatusConflict, gin.H{"error": verr.Message, "code": verr.Code})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "embedded plugin operation failed"})
}

// GetEmbeddedPlugins: the admin review queue (optional ?state= filter).
func (h *AdminMaclawHandler) GetEmbeddedPlugins(c *gin.Context) {
	if h.embeddedRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "embedded plugin store is not configured", "code": "embedded_not_configured"})
		return
	}
	rows, err := h.embeddedRepo.ListAll(c.Request.Context(), strings.TrimSpace(c.Query("state")), 200)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list embedded plugins"})
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		gate := map[string]any{}
		if len(row.AutoGate) > 0 {
			_ = json.Unmarshal(row.AutoGate, &gate)
		}
		items = append(items, gin.H{
			"package_id":    row.PackageID,
			"resource_id":   row.ResourceID,
			"owner_user_id": row.OwnerUserID.String(),
			"name":          row.Name,
			"summary":       row.Summary,
			"state":         row.State,
			"state_label":   maclaw.EmbeddedStateLabel(row.State),
			"version":       row.Version,
			"auto_gate":     gate,
			"review_reason": row.ReviewReason,
			"updated_at":    row.UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ReviewEmbeddedPlugin: 初审 approve / 需修改 changes（意见必填，状态机也强制）。
// approve 时按规则 4 重查 id 是否已被目录占用（目录可能在上传后长出同名 id）。
func (h *AdminMaclawHandler) ReviewEmbeddedPlugin(c *gin.Context) {
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	packageID := strings.TrimSpace(c.Param("packageId"))
	var in struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "code": "invalid_request"})
		return
	}
	var err error
	switch in.Action {
	case "approve":
		if h.embeddedRepo != nil {
			if row, rerr := h.embeddedRepo.GetByPackageID(c.Request.Context(), packageID); rerr == nil && row != nil && maclaw.EmbeddedPluginIDReserved(row.PackageID) {
				c.JSON(http.StatusConflict, gin.H{"error": "该 id 与现有插件目录冲突（内置或已下线），无法批准。", "code": "embedded_id_conflict"})
				return
			}
		}
		err = h.embeddedLifecycle.InitialApprove(c.Request.Context(), packageID, actor, strings.TrimSpace(in.Reason))
	case "changes":
		err = h.embeddedLifecycle.RequestChanges(c.Request.Context(), packageID, actor, in.Reason)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "action must be approve|changes", "code": "invalid_action"})
		return
	}
	if err != nil {
		h.writeEmbeddedError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "state": in.Action})
}

// BuildLifecycleEmbedded: request-build / final-approve / disable / enable /
// remove / unmark — thin wrappers over the state machine (rule 5: two-stage
// approval, final approve is manual and only valid from `testing`).
func (h *AdminMaclawHandler) BuildLifecycleEmbedded(c *gin.Context) {
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	packageID := strings.TrimSpace(c.Param("packageId"))
	action := strings.TrimSpace(c.Param("action")) // routed as .../:packageId/:action
	var in struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&in) // body optional except where the state machine demands it
	var err error
	switch action {
	case "request-build":
		err = h.embeddedLifecycle.RequestBuild(c.Request.Context(), packageID, actor)
	case "final-approve":
		err = h.embeddedLifecycle.FinalApprove(c.Request.Context(), packageID, actor, strings.TrimSpace(in.Reason))
	case "disable":
		err = h.embeddedLifecycle.Disable(c.Request.Context(), packageID, actor, strings.TrimSpace(in.Reason))
	case "enable":
		err = h.embeddedLifecycle.Enable(c.Request.Context(), packageID, actor)
	case "remove":
		err = h.embeddedLifecycle.MarkRemove(c.Request.Context(), packageID, actor, strings.TrimSpace(in.Reason))
	case "unmark":
		err = h.embeddedLifecycle.UnmarkRemove(c.Request.Context(), packageID, actor)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown action", "code": "invalid_action"})
		return
	}
	if err != nil {
		h.writeEmbeddedError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Tester whitelist (D-9/option a): who can see & select a `testing` package.

func (h *AdminMaclawHandler) ListEmbeddedTesters(c *gin.Context) {
	if h.embeddedRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "embedded plugin store is not configured", "code": "embedded_not_configured"})
		return
	}
	ids, err := h.embeddedRepo.ListTesters(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list testers"})
		return
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *AdminMaclawHandler) AddEmbeddedTester(c *gin.Context) {
	actor, ok := h.adminActor(c)
	if !ok {
		return
	}
	target, err := uuid.Parse(strings.TrimSpace(c.Param("userId")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id", "code": "invalid_user"})
		return
	}
	if err := h.embeddedRepo.AddTester(c.Request.Context(), target, actor); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add tester"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *AdminMaclawHandler) RemoveEmbeddedTester(c *gin.Context) {
	target, err := uuid.Parse(strings.TrimSpace(c.Param("userId")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id", "code": "invalid_user"})
		return
	}
	if err := h.embeddedRepo.RemoveTester(c.Request.Context(), target); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to remove tester"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GetEmbeddedPluginSource is the R-5 narrow exception: the ONE place an admin
// can read package source (review IS the governance act for embedded code).
// Bounded hard inside AdminReadEmbeddedPackage (per-file 200KB, total 1MB,
// text-ish files only) — a zip bomb or a binary blob never reaches the
// browser, and the payload/credential bans on every other admin surface are
// untouched.
func (h *AdminMaclawHandler) GetEmbeddedPluginSource(c *gin.Context) {
	if h.embeddedRepo == nil || h.resourceStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "embedded review store is not configured", "code": "embedded_not_configured"})
		return
	}
	row, err := h.embeddedRepo.GetByPackageID(c.Request.Context(), strings.TrimSpace(c.Param("packageId")))
	if err != nil || row == nil || row.ResourceID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "包不存在或尚未通过自动关卡（未生成资源行）。", "code": "embedded_not_found"})
		return
	}
	files, err := h.resourceStore.AdminReadEmbeddedPackage(c.Request.Context(), row.OwnerUserID, row.ResourceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read embedded package source"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"package_id": row.PackageID,
		"version":    row.Version,
		"state":      row.State,
		"files":      files,
	})
}
