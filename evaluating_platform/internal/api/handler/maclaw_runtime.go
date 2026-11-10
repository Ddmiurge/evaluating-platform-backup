package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
)

type MaclawRuntimeHandler struct {
	gateway            maclaw.RuntimeGateway
	resolver           *maclaw.InstanceResolver
	provider           maclaw.GatewayProvider
	resourceProjection *maclaw.ResourceProjectionService
	skillProjection    *maclaw.SkillProjectionService
	capabilityCatalog  *maclaw.CapabilityCatalogService
	targetConfig       *maclaw.TargetConfigService
	engineBridge       *maclaw.RedteamToolBridge
	engineJobs         *maclaw.PlatformEngineJobStore
	engineRunService   *maclaw.EngineRunService
	executionGrantKey  string
	skillPreflightMu   sync.Mutex
	skillPreflightOK   map[string]time.Time
}

type maclawRuntimeSessionSnapshot struct {
	Session  *maclaw.RuntimeSession  `json:"session"`
	Messages []maclaw.RuntimeMessage `json:"messages"`
}

type maclawRuntimeConfirmInput struct {
	TestCount     int    `json:"test_count,omitempty"`
	PlanMessageID string `json:"plan_message_id,omitempty"`
	Content       string `json:"content,omitempty"`
	// RemovedCapabilityRefs names plan capabilities the user took off the card
	// before confirming (2026-10-16). REMOVAL ONLY: the plan is the authorisation,
	// so a ref it did not offer is ignored rather than added — see
	// promptfooEnginePlanSelection.withoutRemovedRefs.
	RemovedCapabilityRefs []string `json:"removed_capability_refs,omitempty"`
	// AddedCapabilityRefs names promptfoo STRATEGIES the user added on the card
	// before confirming (2026-10-21). Strategies only, and every id is proven
	// runnable before the run starts — see addedStrategyRefusal for why this
	// axis is the one that may be widened.
	AddedCapabilityRefs []string `json:"added_capability_refs,omitempty"`
}

func NewMaclawRuntimeHandler(gateway maclaw.RuntimeGateway, instanceID string) *MaclawRuntimeHandler {
	return NewMaclawRuntimeHandlerWithResolver(gateway, maclaw.NewInstanceResolver(instanceID, nil))
}

func NewMaclawRuntimeHandlerWithResolver(gateway maclaw.RuntimeGateway, resolver *maclaw.InstanceResolver) *MaclawRuntimeHandler {
	return &MaclawRuntimeHandler{gateway: gateway, resolver: resolver}
}

func NewMaclawRuntimeHandlerWithProvider(provider maclaw.GatewayProvider) *MaclawRuntimeHandler {
	return &MaclawRuntimeHandler{provider: provider}
}

func NewMaclawRuntimeHandlerWithProviderAndProjections(provider maclaw.GatewayProvider, resourceProjection *maclaw.ResourceProjectionService, skillProjection *maclaw.SkillProjectionService) *MaclawRuntimeHandler {
	return &MaclawRuntimeHandler{provider: provider, resourceProjection: resourceProjection, skillProjection: skillProjection}
}

func NewMaclawRuntimeHandlerWithProviderProjectionsAndCatalog(provider maclaw.GatewayProvider, resourceProjection *maclaw.ResourceProjectionService, skillProjection *maclaw.SkillProjectionService, catalog *maclaw.CapabilityCatalogService) *MaclawRuntimeHandler {
	return &MaclawRuntimeHandler{provider: provider, resourceProjection: resourceProjection, skillProjection: skillProjection, capabilityCatalog: catalog}
}

func (h *MaclawRuntimeHandler) SetExecutionGrantSecret(secret string) {
	if h != nil {
		h.executionGrantKey = strings.TrimSpace(secret)
	}
}

func (h *MaclawRuntimeHandler) SetTargetConfigService(targetConfig *maclaw.TargetConfigService) {
	if h != nil {
		h.targetConfig = targetConfig
	}
}

// SetPromptfooEngine wires the Phase-1 confirm fast path: plan cards that
// select only promptfoo engine capabilities are executed platform-side
// instead of re-entering the MaClaw agent loop.
func (h *MaclawRuntimeHandler) SetPromptfooEngine(bridge *maclaw.RedteamToolBridge, jobs *maclaw.PlatformEngineJobStore, runs *maclaw.EngineRunService) {
	if h != nil {
		h.engineBridge = bridge
		h.engineJobs = jobs
		h.engineRunService = runs
	}
}

func (h *MaclawRuntimeHandler) ListSessions(c *gin.Context) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return
	}
	gateway, instanceID, ok := h.runtimeGatewayWithInstance(c)
	if !ok {
		return
	}
	limit, err := parseOptionalPositiveInt(c.Query("limit"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
		return
	}
	includeArchived, err := parseOptionalBool(c.Query("include_archived"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "include_archived must be a boolean"})
		return
	}
	items, err := gateway.ListRuntimeSessions(c.Request.Context(), instanceID, maclaw.RuntimeSessionQuery{
		Limit:           limit,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

func (h *MaclawRuntimeHandler) CreateSession(c *gin.Context) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return
	}
	gateway, instanceID, ok := h.runtimeGatewayWithInstance(c)
	if !ok {
		return
	}
	var in maclaw.RuntimeSessionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if in.Metadata == nil {
		in.Metadata = map[string]string{}
	}
	in.Metadata["agent_profile"] = "redteam_evaluation_v1"
	out, err := gateway.CreateRuntimeSession(c.Request.Context(), instanceID, in)
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *MaclawRuntimeHandler) GetSession(c *gin.Context) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return
	}
	gateway, instanceID, ok := h.runtimeGatewayWithInstance(c)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(firstNonEmpty(c.Param("id"), c.Param("session_id"), c.Param("sessionId")))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session id is required"})
		return
	}
	session, err := gateway.GetRuntimeSession(c.Request.Context(), instanceID, sessionID)
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	messages, err := gateway.ListRuntimeMessages(c.Request.Context(), instanceID, sessionID, maclaw.RuntimeMessageQuery{Limit: 200})
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	h.normalizeRuntimeMessages(c, messages)
	hydrateRuntimeReportMessages(c.Request.Context(), gateway, messages)
	messages = filterBrowserRuntimeMessages(messages)
	if jobs, err := gateway.ListEvaluationJobs(c.Request.Context(), maclaw.EvaluationJobQuery{
		Kind:      maclaw.EvaluationJobKindRun,
		Status:    maclaw.EvaluationJobStatusRunning,
		SessionID: sessionID,
		Limit:     20,
	}); err == nil {
		messages = appendRuntimeJobProgressMessages(messages, sessionID, jobs)
	}
	// The engine fast path orchestrates the run in the BFF, so MaClaw never writes
	// a report message — this is where the PDF becomes visible in the chat.
	// Synthesized beside the progress cards and only after filtering, so a report
	// message the runtime DID write is already in the list and wins the dedupe.
	if jobs := h.engineJobs.ListBySession(sessionID); len(jobs) > 0 {
		// Running jobs need a card too, or a refresh makes the evaluation look
		// like it never started (record 37: the user re-submitted because of it).
		messages = appendRunningEngineProgressMessages(messages, sessionID, jobs)
		// A missing or unparseable user id costs only the card's header fields
		// (risk level, score): the report lookup is scoped by user, so it returns
		// nothing and the card is still built with its download button.
		userID, _ := uuid.Parse(strings.TrimSpace(c.GetString("user_id")))
		messages = appendCompletedEngineReportMessages(c.Request.Context(), h.engineBridge, userID, messages, sessionID, jobs)
	}
	c.JSON(http.StatusOK, maclawRuntimeSessionSnapshot{Session: session, Messages: messages})
}

func (h *MaclawRuntimeHandler) DeleteSession(c *gin.Context) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return
	}
	gateway, instanceID, ok := h.runtimeGatewayWithInstance(c)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(firstNonEmpty(c.Param("id"), c.Param("session_id"), c.Param("sessionId")))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session id is required"})
		return
	}
	if err := gateway.DeleteRuntimeSession(c.Request.Context(), instanceID, sessionID); err != nil {
		writeMaclawError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *MaclawRuntimeHandler) PostMessage(c *gin.Context) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return
	}
	gateway, instanceID, ok := h.runtimeGatewayWithInstance(c)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(firstNonEmpty(c.Param("id"), c.Param("session_id"), c.Param("sessionId")))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session id is required"})
		return
	}
	var in maclaw.RuntimeMessageInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.applyRedteamMessageProfile(&in)
	h.attachCurrentTargetContext(c, &in)
	if h.skillProjection != nil && h.provider != nil {
		session, _, ok := h.runtimeSessionWithInstance(c)
		if !ok {
			return
		}
		if err := h.skillProjection.SyncEnterprisePublishedSkills(c.Request.Context(), maclawRuntimeIdentity(c), session); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw skill projection failed", "code": "maclaw_skill_projection_failed"})
			return
		}
		gateway = session.Client
		instanceID = session.InstanceID
	}
	messageCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Minute)
	defer cancel()
	out, err := gateway.PostRuntimeMessage(messageCtx, instanceID, sessionID, in)
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	h.normalizeRuntimeMessageResponse(c, out)
	c.JSON(http.StatusOK, out)
}

func (h *MaclawRuntimeHandler) applyRedteamMessageProfile(in *maclaw.RuntimeMessageInput) {
	if in == nil {
		return
	}
	in.CapabilityContext = nil
	if in.Metadata == nil {
		in.Metadata = map[string]string{}
	}
	for _, key := range []string{
		"maclaw_capability_context_json",
		"capability_context",
		"evaluation_action",
		"selected_skill_names_json",
		"selected_resource_refs_json",
		"evaluation_execution_grant",
		"evaluation_execution_grant_expires_at",
		"response_source",
		"evaluation_event_type",
		"tool_policy",
		"ops_approved_commands",
		"current_target_configured",
		"current_target_id",
		"current_target_name",
		"current_target_kind",
		"current_target_provider",
		"current_target_model",
		"current_target_health_status",
		"current_target_credential_secret_set",
	} {
		delete(in.Metadata, key)
	}
	in.Metadata["agent_profile"] = "redteam_evaluation_v1"
}

func (h *MaclawRuntimeHandler) attachCurrentTargetContext(c *gin.Context, in *maclaw.RuntimeMessageInput) {
	if h == nil || in == nil || h.targetConfig == nil || !h.targetConfig.Enabled() {
		return
	}
	userID, err := uuid.Parse(strings.TrimSpace(c.GetString("user_id")))
	if err != nil {
		return
	}
	// Accept every callable target kind (llm / http / agent): the store holds
	// at most one target per user, and an agent target is as valid as an LLM
	// target for planning context.
	targets, err := h.targetConfig.ListTargets(c.Request.Context(), userID, maclaw.EvaluationTargetQuery{
		IncludeInactive: true,
	})
	if err != nil || len(targets) == 0 {
		return
	}
	target := targets[0]
	if in.Metadata == nil {
		in.Metadata = map[string]string{}
	}
	in.Metadata["current_target_configured"] = "true"
	in.Metadata["current_target_id"] = strings.TrimSpace(target.ID)
	in.Metadata["current_target_name"] = strings.TrimSpace(target.Name)
	in.Metadata["current_target_kind"] = string(target.Kind)
	in.Metadata["current_target_provider"] = strings.TrimSpace(target.Provider)
	in.Metadata["current_target_model"] = strings.TrimSpace(target.Model)
	in.Metadata["current_target_health_status"] = string(target.HealthStatus)
	if target.CredentialSecretSet {
		in.Metadata["current_target_credential_secret_set"] = "true"
	} else {
		in.Metadata["current_target_credential_secret_set"] = "false"
	}
}

func (h *MaclawRuntimeHandler) ConfirmPlan(c *gin.Context) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return
	}
	gateway, instanceID, ok := h.runtimeGatewayWithInstance(c)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(firstNonEmpty(c.Param("id"), c.Param("session_id"), c.Param("sessionId")))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session id is required"})
		return
	}
	var in maclawRuntimeConfirmInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	content := strings.TrimSpace(in.Content)
	if content == "" {
		content = "纭鎵ц"
	}
	if strings.TrimSpace(in.Content) == "" {
		content = "确认执行"
	}
	metadata := map[string]string{"evaluation_action": "confirm_plan"}
	if in.TestCount > 0 {
		metadata["test_count"] = strconv.Itoa(in.TestCount)
	}
	metadata["session_id"] = sessionID
	messages, err := h.listRecentRuntimeMessages(c, gateway, instanceID, sessionID)
	if err != nil {
		return
	}
	planMessage, ok := planConfirmMessageForConfirm(messages, in.PlanMessageID)
	if !ok {
		c.JSON(http.StatusConflict, gin.H{
			"error": "execution confirmation requires a plan_confirm card",
			"code":  "maclaw_plan_confirm_required",
		})
		return
	}
	if planMessage.ID != "" {
		metadata["plan_message_id"] = planMessage.ID
	}
	targetConfigured, err := h.confirmTargetConfigured(c)
	if err != nil {
		return
	}
	effectiveTestCount, missing := planConfirmExecutionRequirements(planMessage.Content, in.TestCount, targetConfigured)
	if len(missing) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "execution confirmation requires target, risk types, and test_count",
			"code":    "maclaw_plan_confirm_incomplete",
			"missing": missing,
		})
		return
	}
	resources, selectedSkills := selectedCapabilityRefsFromContent(planMessage.Content)
	// Phase-1 fast path: plan cards that select only promptfoo engine
	// capabilities run platform-side (the MaClaw agent loop has no engine
	// fast path, and its LLM tool-calling is unreliable here).
	if engineSelection, ok := promptfooEnginePlanSelectionFromContent(planMessage.Content); ok && len(selectedSkills) == 0 && len(resources) == 0 {
		// Strategies the user added on the card are validated BEFORE the
		// removals are applied: an unusable addition must refuse the confirm,
		// not be silently dropped and leave the card promising a technique that
		// never ran.
		if code, message, refused := addedStrategyRefusal(in.AddedCapabilityRefs); refused {
			c.JSON(http.StatusConflict, gin.H{"error": message, "code": code})
			return
		}
		selection := engineSelection.
			withoutRemovedRefs(normalizedRefSet(in.RemovedCapabilityRefs)).
			withAddedStrategies(in.AddedCapabilityRefs)
		h.confirmPromptfooEnginePlan(c, sessionID, instanceID, planMessage, in, effectiveTestCount, selection)
		return
	}
	// Removals are only honourable on the engine fast path, where the plan's
	// capability refs are the selection. A plan card whose refs are empty (MaClaw
	// sometimes writes the plugins only into `risk_types`) falls through to the
	// runtime path, which has no consumer for a removal list — measured on ai02:
	// "remove every plugin" on such a card was **accepted and re-ran the whole
	// plan**, burning quota while the card claimed the user had narrowed it.
	//
	// Refusing is the only honest option here. The alternative — executing the
	// plan unchanged — is the silent downgrade this whole mechanism exists to
	// prevent.
	if len(normalizedRefSet(in.RemovedCapabilityRefs)) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error": "这张计划卡没有列出可逐项调整的能力（selected_capability_refs 为空），因此无法执行你移除的项；按原计划执行会忽略你的选择。请在对话里重新描述评测需求以生成新的计划卡。",
			"code":  "plan_capability_removal_unsupported",
		})
		return
	}
	if err := h.preflightSelectedSkillTenantModel(c, selectedSkills); err != nil {
		return
	}
	if err := h.prepareSelectedCapabilities(c, resources, selectedSkills); err != nil {
		return
	}
	metadata["agent_profile"] = "redteam_evaluation_v1"
	if effectiveTestCount > 0 {
		metadata["test_count"] = strconv.Itoa(effectiveTestCount)
	}
	if len(selectedSkills) > 0 {
		if data, err := json.Marshal(selectedSkills); err == nil {
			metadata["selected_skill_names_json"] = string(data)
		}
	}
	if len(resources) > 0 {
		if data, err := json.Marshal(resources); err == nil {
			metadata["selected_capability_refs_json"] = string(data)
		}
	}
	if body, ok := runtimePlanPayloadJSON(planMessage.Content); ok {
		if strategy := strings.TrimSpace(fmtAnyString(body["selection_strategy"])); strategy != "" {
			metadata["selection_strategy"] = strategy
		}
	}
	grantExpiresAt := time.Now().Add(redteamExecutionGrantTTL)
	if grant := mintRedteamExecutionGrantWithContext(h.executionGrantKey, c.GetString("user_id"), sessionID, grantExpiresAt, redteamExecutionGrantContext{
		TestCount:              effectiveTestCount,
		SelectedCapabilityRefs: resources,
		SelectedSkillNames:     selectedSkills,
		SelectionStrategy:      metadata["selection_strategy"],
	}); grant != "" {
		metadata[redteamExecutionGrantMetadataKey] = grant
		metadata["evaluation_execution_grant_expires_at"] = strconv.FormatInt(grantExpiresAt.Unix(), 10)
	}
	confirmCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Minute)
	defer cancel()
	confirmStarted := time.Now()
	out, err := gateway.ConfirmRuntimePlan(confirmCtx, instanceID, sessionID, maclaw.RuntimeMessageInput{
		Content:  content,
		Metadata: metadata,
	})
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	annotateEvaluationJobProgressDuration(out, "bff_confirm", time.Since(confirmStarted).Milliseconds())
	c.JSON(http.StatusAccepted, out)
}

func (h *MaclawRuntimeHandler) preflightSelectedSkillTenantModel(c *gin.Context, selectedSkills []string) error {
	if h == nil || h.provider == nil || len(selectedSkills) == 0 {
		return nil
	}
	session, instanceID, ok := h.runtimeSessionWithInstance(c)
	if !ok {
		return http.ErrAbortHandler
	}
	cacheKey := strings.Join([]string{strings.TrimSpace(c.GetString("user_id")), strings.TrimSpace(instanceID)}, ":")
	if h.skillTenantModelPreflightRecentlyOK(cacheKey) {
		return nil
	}
	preflightCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), skillModelPreflightTimeout())
	defer cancel()
	out, err := session.Client.TestRuntimeConfig(preflightCtx, nil)
	if err != nil || out == nil || !out.Success {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "maclaw model config is not reachable; configure a reachable model before running Skill-backed evaluation",
			"code":  "maclaw_model_unreachable",
		})
		if err != nil {
			return err
		}
		return http.ErrAbortHandler
	}
	h.markSkillTenantModelPreflightOK(cacheKey)
	return nil
}

func (h *MaclawRuntimeHandler) skillTenantModelPreflightRecentlyOK(cacheKey string) bool {
	if h == nil || strings.TrimSpace(cacheKey) == "" {
		return false
	}
	h.skillPreflightMu.Lock()
	defer h.skillPreflightMu.Unlock()
	if h.skillPreflightOK == nil {
		return false
	}
	checkedAt, ok := h.skillPreflightOK[cacheKey]
	if !ok {
		return false
	}
	if time.Since(checkedAt) <= skillModelPreflightCacheTTL() {
		return true
	}
	delete(h.skillPreflightOK, cacheKey)
	return false
}

func (h *MaclawRuntimeHandler) markSkillTenantModelPreflightOK(cacheKey string) {
	if h == nil || strings.TrimSpace(cacheKey) == "" {
		return
	}
	h.skillPreflightMu.Lock()
	defer h.skillPreflightMu.Unlock()
	if h.skillPreflightOK == nil {
		h.skillPreflightOK = map[string]time.Time{}
	}
	h.skillPreflightOK[cacheKey] = time.Now()
}

func skillModelPreflightTimeout() time.Duration {
	timeout := 12 * time.Second
	if raw := strings.TrimSpace(os.Getenv("MACLAW_SKILL_MODEL_PREFLIGHT_TIMEOUT_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			timeout = time.Duration(parsed) * time.Second
		}
	}
	if timeout > 60*time.Second {
		return 60 * time.Second
	}
	return timeout
}

func skillModelPreflightCacheTTL() time.Duration {
	ttl := 5 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("MACLAW_SKILL_MODEL_PREFLIGHT_CACHE_TTL_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			ttl = time.Duration(parsed) * time.Second
		}
	}
	if ttl > 30*time.Minute {
		return 30 * time.Minute
	}
	return ttl
}

func (h *MaclawRuntimeHandler) confirmTargetConfigured(c *gin.Context) (bool, error) {
	if h == nil || h.targetConfig == nil || !h.targetConfig.Enabled() {
		return true, nil
	}
	userID, err := uuid.Parse(strings.TrimSpace(c.GetString("user_id")))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid user id", "code": "invalid_user"})
		return false, err
	}
	// Accept every callable target kind (llm / http / agent): the user's single
	// stored target is the evaluation target regardless of kind.
	targets, err := h.targetConfig.ListTargets(c.Request.Context(), userID, maclaw.EvaluationTargetQuery{
		IncludeInactive: true,
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw target config unavailable", "code": "maclaw_target_config_unavailable"})
		return false, err
	}
	return len(targets) > 0, nil
}

func annotateEvaluationJobProgressDuration(job *maclaw.EvaluationJob, stage string, durationMs int64) {
	if job == nil || job.Progress == nil {
		return
	}
	if durationMs <= 0 {
		durationMs = 1
	}
	if job.Progress.DurationMs <= 0 {
		job.Progress.DurationMs = durationMs
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return
	}
	stageDurations := map[string]int64{}
	if raw := strings.TrimSpace(job.Progress.StageDurationsJSON); raw != "" {
		_ = json.Unmarshal([]byte(raw), &stageDurations)
	}
	if _, exists := stageDurations[stage]; !exists {
		stageDurations[stage] = durationMs
	}
	if data, err := json.Marshal(stageDurations); err == nil {
		job.Progress.StageDurationsJSON = string(data)
	}
}

func (h *MaclawRuntimeHandler) listRecentRuntimeMessages(c *gin.Context, gateway maclaw.RuntimeGateway, instanceID, sessionID string) ([]maclaw.RuntimeMessage, error) {
	messages, err := gateway.ListRuntimeMessages(c.Request.Context(), instanceID, sessionID, maclaw.RuntimeMessageQuery{Limit: 50})
	if err != nil {
		writeMaclawError(c, err)
		return nil, err
	}
	return messages, nil
}

func (h *MaclawRuntimeHandler) prepareSelectedCapabilities(c *gin.Context, resources []string, skills []string) error {
	if h.provider == nil || (h.resourceProjection == nil && h.skillProjection == nil) {
		return nil
	}
	session, _, ok := h.runtimeSessionWithInstance(c)
	if !ok {
		return http.ErrAbortHandler
	}
	if h.resourceProjection != nil && len(resources) > 0 {
		if err := h.resourceProjection.PrepareEnterprisePublishedResources(c.Request.Context(), maclawRuntimeIdentity(c), session, resources); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw resource projection failed", "code": "maclaw_resource_projection_failed"})
			return err
		}
	}
	if h.skillProjection != nil && len(skills) > 0 {
		if err := h.skillProjection.PrepareEnterprisePublishedSkills(c.Request.Context(), maclawRuntimeIdentity(c), session, skills); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw skill projection failed", "code": "maclaw_skill_projection_failed"})
			return err
		}
	}
	return nil
}

// platformTargetKind returns the KIND the caller's connected target has, or "".
//
// The plan card's 「目标类型」 is whatever the planner wrote — maclaw sees the
// target at most as a name, and its fast path hardcodes llm — while the kind is a
// platform fact: it is stored in maclaw_target_configs and decides whether the run
// calls an HTTP model or an agent endpoint. Reading it here is what makes the card
// stop saying llm for an agent target (2026-10-28).
//
// "" means "unknown", and callers must leave the planner's value alone in that
// case: a card that says llm because maclaw said llm is wrong, but a card that
// says 「未配置」 because the lookup failed would be wrong differently.
func (h *MaclawRuntimeHandler) CancelRun(c *gin.Context) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return
	}
	gateway, ok := h.runtimeGateway(c)
	if !ok {
		return
	}
	runID := strings.TrimSpace(firstNonEmpty(c.Param("id"), c.Param("run_id"), c.Param("runId")))
	if runID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "run id is required"})
		return
	}
	out, err := gateway.CancelEvaluationRun(c.Request.Context(), runID)
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *MaclawRuntimeHandler) StreamRunEvents(c *gin.Context) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return
	}
	gateway, ok := h.runtimeGateway(c)
	if !ok {
		return
	}
	runID := strings.TrimSpace(firstNonEmpty(c.Param("id"), c.Param("run_id"), c.Param("runId")))
	if runID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "run id is required"})
		return
	}
	stream, err := gateway.StreamEvaluationRunEvents(c.Request.Context(), runID)
	if err != nil {
		writeMaclawError(c, err)
		return
	}
	defer stream.Body.Close()

	contentType := strings.TrimSpace(stream.ContentType)
	if contentType == "" {
		contentType = "text/event-stream"
	}
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "no-cache")
	c.Status(http.StatusOK)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		_ = copySanitizedRuntimeEventStream(c.Writer, stream.Body, flusher)
		flusher.Flush()
		return
	}
	_ = copySanitizedRuntimeEventStream(c.Writer, stream.Body, nil)
}

func copySanitizedRuntimeEventStream(dst io.Writer, src io.Reader, flusher http.Flusher) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			prefix := "data:"
			value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			if sanitized := sanitizeRuntimeEventData(value); sanitized != "" {
				line = prefix + " " + sanitized
			}
		}
		if _, err := io.WriteString(dst, line+"\n"); err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" && flusher != nil {
			flusher.Flush()
		}
	}
	return scanner.Err()
}

func sanitizeRuntimeEventData(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[DONE]" {
		return raw
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	value = sanitizeRuntimeEventValue(value, "")
	data, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(data)
}

func sanitizeRuntimeEventValue(value any, key string) any {
	if runtimeEventKeyIsSensitive(key) {
		return "[redacted by bff]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = sanitizeRuntimeEventValue(v, k)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeRuntimeEventValue(item, key)
		}
		return out
	default:
		return value
	}
}

func runtimeEventKeyIsSensitive(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	sensitiveTokens := []string{
		"payload",
		"credential",
		"secret",
		"api_key",
		"apikey",
		"token",
		"grant",
		"authorization",
		"raw_prompt",
		"raw_response",
		"target_response",
		"evidence_content",
		"archive",
	}
	for _, token := range sensitiveTokens {
		if strings.Contains(key, token) {
			return true
		}
	}
	return key == "content" || key == "prompt" || key == "response"
}

func (h *MaclawRuntimeHandler) resolveInstance(c *gin.Context) (string, bool) {
	if !h.available(c) {
		return "", false
	}
	if h.resolver == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw instance mapping is not configured"})
		return "", false
	}
	instanceID, ok := h.resolver.Resolve(maclaw.RuntimeIdentity{
		UserID: c.GetString("user_id"),
		Role:   c.GetString("user_role"),
	})
	if !ok || strings.TrimSpace(instanceID) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw instance is not configured for current user"})
		return "", false
	}
	return instanceID, true
}

func (h *MaclawRuntimeHandler) runtimeGateway(c *gin.Context) (maclaw.RuntimeGateway, bool) {
	if h != nil && h.provider != nil {
		session, ok := resolveMaclawGatewaySession(c, h.provider)
		if !ok {
			return nil, false
		}
		return session.Client, true
	}
	if !h.available(c) {
		return nil, false
	}
	return h.gateway, true
}

func (h *MaclawRuntimeHandler) runtimeGatewayWithInstance(c *gin.Context) (maclaw.RuntimeGateway, string, bool) {
	if h != nil && h.provider != nil {
		session, instanceID, ok := h.runtimeSessionWithInstance(c)
		if !ok {
			return nil, "", false
		}
		return session.Client, instanceID, true
	}
	instanceID, ok := h.resolveInstance(c)
	if !ok {
		return nil, "", false
	}
	return h.gateway, instanceID, true
}

func (h *MaclawRuntimeHandler) runtimeSessionWithInstance(c *gin.Context) (*maclaw.GatewaySession, string, bool) {
	if h != nil && h.provider != nil {
		session, ok := resolveMaclawGatewaySession(c, h.provider)
		if !ok {
			return nil, "", false
		}
		if strings.TrimSpace(session.InstanceID) == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw instance is not configured for current user"})
			return nil, "", false
		}
		return session, session.InstanceID, true
	}
	instanceID, ok := h.resolveInstance(c)
	if !ok {
		return nil, "", false
	}
	return &maclaw.GatewaySession{Client: nil, InstanceID: instanceID}, instanceID, true
}

func (h *MaclawRuntimeHandler) available(c *gin.Context) bool {
	if h == nil || h.gateway == nil || !h.gateway.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw runtime is not configured"})
		return false
	}
	return true
}
