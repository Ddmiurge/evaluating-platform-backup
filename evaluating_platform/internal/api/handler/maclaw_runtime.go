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
	normalizeRuntimeMessages(messages)
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
	normalizeRuntimeMessageResponse(out)
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
	if enginePlugins, ok := promptfooEngineRefsFromPlan(planMessage.Content); ok && len(selectedSkills) == 0 && len(resources) == 0 {
		h.confirmPromptfooEnginePlan(c, sessionID, instanceID, planMessage, in, effectiveTestCount, enginePlugins)
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

func selectedCapabilityRefs(messages []maclaw.RuntimeMessage) (resources []string, skills []string) {
	if msg, ok := latestPlanConfirmMessage(messages); ok {
		return selectedCapabilityRefsFromContent(msg.Content)
	}
	return nil, nil
}

func latestPlanConfirmMessage(messages []maclaw.RuntimeMessage) (*maclaw.RuntimeMessage, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if isRuntimeMessageEmpty(msg) {
			continue
		}
		if strings.TrimSpace(msg.Role) == "user" && strings.TrimSpace(msg.Metadata["evaluation_action"]) == "confirm_plan" {
			continue
		}
		if isPlanConfirmMessage(msg) {
			return &messages[i], true
		}
		return nil, false
	}
	return nil, false
}

func planConfirmMessageForConfirm(messages []maclaw.RuntimeMessage, planMessageID string) (*maclaw.RuntimeMessage, bool) {
	planMessageID = strings.TrimSpace(planMessageID)
	if planMessageID == "" {
		return latestPlanConfirmMessage(messages)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if strings.TrimSpace(msg.ID) != planMessageID {
			continue
		}
		if isPlanConfirmMessage(msg) {
			return &messages[i], true
		}
		return nil, false
	}
	return nil, false
}

func isRuntimeMessageEmpty(msg maclaw.RuntimeMessage) bool {
	return strings.TrimSpace(msg.ID) == "" &&
		strings.TrimSpace(msg.Role) == "" &&
		strings.TrimSpace(msg.Content) == "" &&
		strings.TrimSpace(msg.OutputType) == "" &&
		len(msg.Metadata) == 0
}

func isPlanConfirmMessage(msg maclaw.RuntimeMessage) bool {
	if strings.TrimSpace(msg.Metadata["response_source"]) == "plan_confirm" || strings.TrimSpace(msg.Metadata["evaluation_event_type"]) == "plan_confirm" {
		return true
	}
	if body, ok := runtimePlanJSON(msg.Content); ok && strings.TrimSpace(fmtAnyString(body["response_source"])) == "plan_confirm" {
		return true
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(msg.OutputType)), "plan-confirm")
}

func isRuntimeReportMessage(msg maclaw.RuntimeMessage) bool {
	if msg.Metadata != nil {
		if strings.TrimSpace(msg.Metadata["response_source"]) == "report" || strings.TrimSpace(msg.Metadata["evaluation_event_type"]) == "report" {
			return true
		}
		if strings.TrimSpace(msg.Metadata["report_id"]) != "" {
			return true
		}
	}
	return extractRedteamReportID(msg.Content) != ""
}

func selectedCapabilityRefsFromContent(content string) (resources []string, skills []string) {
	body, ok := runtimePlanPayloadJSON(content)
	if !ok {
		return nil, nil
	}
	addResource := func(value any) {
		if ref := strings.TrimSpace(fmtAnyString(value)); ref != "" {
			resources = append(resources, ref)
		}
	}
	addSkill := func(value any) {
		if ref := strings.TrimSpace(fmtAnyString(value)); ref != "" {
			skills = append(skills, normalizeSelectedSkillRef(ref))
		}
	}
	addCapabilityRef := func(sourceType string, value any) {
		ref := strings.TrimSpace(fmtAnyString(value))
		if ref == "" {
			return
		}
		switch strings.ToLower(strings.TrimSpace(sourceType)) {
		case "resource", "sample", "template", "composed_attack":
			addResource(ref)
		case "skill":
			addSkill(ref)
		default:
			if isLikelyResourceRef(ref) {
				addResource(ref)
			} else if isLikelySkillRef(ref) {
				addSkill(ref)
			}
		}
	}
	if raw, ok := body["resource_handles"].([]any); ok {
		for _, item := range raw {
			addResource(item)
		}
	}
	addSkill(body["skill_name"])
	if m, ok := body["skill_selection"].(map[string]any); ok {
		addSkill(firstAny(m["skill_name"], m["name"], m["source_ref"], m["ref"], m["id"]))
	}
	if m, ok := body["selected_skill"].(map[string]any); ok {
		addSkill(firstAny(m["skill_name"], m["name"], m["source_ref"], m["ref"], m["id"]))
	}
	if raw, ok := body["skills"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				addSkill(firstAny(m["source_ref"], m["name"]))
			}
		}
	}
	if raw, ok := body["selected_capabilities"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			sourceType := strings.ToLower(strings.TrimSpace(fmtAnyString(firstAny(m["source_type"], m["type"]))))
			ref := firstAny(m["source_ref"], m["ref"], m["handle"], m["capability_ref"], m["id"])
			addCapabilityRef(sourceType, ref)
		}
	}
	if raw, ok := body["selected_skills"].([]any); ok {
		for _, item := range raw {
			switch typed := item.(type) {
			case string:
				addSkill(typed)
			case map[string]any:
				addSkill(firstAny(typed["skill_name"], typed["source_ref"], typed["name"], typed["ref"], typed["id"]))
			}
		}
	}
	if raw, ok := body["selected_capability_refs"].([]any); ok {
		for _, item := range raw {
			switch typed := item.(type) {
			case string:
				addCapabilityRef("", typed)
			case map[string]any:
				sourceType := strings.ToLower(strings.TrimSpace(fmtAnyString(firstAny(typed["source_type"], typed["type"]))))
				ref := firstAny(typed["source_ref"], typed["ref"], typed["handle"], typed["capability_ref"], typed["id"])
				addCapabilityRef(sourceType, ref)
			}
		}
	}
	if raw, ok := body["selection_reasons"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				addSkill(firstAny(m["skill"], m["skill_name"]))
			}
		}
	}
	return uniqueNonEmptyStrings(resources), uniqueNonEmptyStrings(skills)
}

func planConfirmExecutionRequirements(content string, overrideTestCount int, targetConfigured bool) (int, []string) {
	body, ok := runtimePlanPayloadJSON(content)
	if !ok {
		return 0, []string{"plan_confirm"}
	}
	effectiveTestCount := overrideTestCount
	if effectiveTestCount <= 0 {
		effectiveTestCount = intFromAny(body["test_count"])
		if effectiveTestCount <= 0 {
			effectiveTestCount = intFromAny(body["testCount"])
		}
	}
	missing := []string{}
	if !targetConfigured && !planHasTargetSummary(body) {
		missing = append(missing, "target")
	}
	if !planHasRiskTypes(body) {
		missing = append(missing, "risk_types")
	}
	if effectiveTestCount <= 0 {
		missing = append(missing, "test_count")
	}
	return effectiveTestCount, missing
}

func planHasTargetSummary(body map[string]any) bool {
	for _, key := range []string{"target_id", "targetId", "target_type", "targetType", "target_model", "targetModel", "target_url", "targetUrl"} {
		if strings.TrimSpace(fmtAnyString(body[key])) != "" {
			return true
		}
	}
	for _, key := range []string{"target_summary", "targetSummary"} {
		if strings.TrimSpace(fmtAnyString(body[key])) != "" {
			return true
		}
		if summary, ok := body[key].(map[string]any); ok {
			for _, value := range summary {
				if strings.TrimSpace(fmtAnyString(value)) != "" {
					return true
				}
			}
		}
	}
	return false
}

func planHasRiskTypes(body map[string]any) bool {
	for _, key := range []string{"risk_types", "riskTypes", "assessment_types", "assessmentTypes"} {
		if values, ok := body[key].([]any); ok {
			for _, value := range values {
				if strings.TrimSpace(fmtAnyString(value)) != "" {
					return true
				}
			}
		}
	}
	return false
}

func normalizeSelectedSkillRef(ref string) string {
	ref = strings.TrimSpace(ref)
	for _, prefix := range []string{"skillhub:", "skill:"} {
		if strings.HasPrefix(strings.ToLower(ref), prefix) {
			ref = strings.TrimSpace(ref[len(prefix):])
			break
		}
	}
	if before, _, ok := strings.Cut(ref, "/"); ok && strings.TrimSpace(before) != "" {
		return strings.TrimSpace(before)
	}
	return ref
}

func isLikelyResourceRef(ref string) bool {
	ref = strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(ref, "evalres_") ||
		strings.HasPrefix(ref, "resource:") ||
		strings.HasPrefix(ref, "resource_") ||
		strings.HasPrefix(ref, "res_") ||
		strings.HasPrefix(ref, "sample:") ||
		strings.HasPrefix(ref, "template:") ||
		strings.HasPrefix(ref, "composed_attack:")
}

func isLikelySkillRef(ref string) bool {
	ref = strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(ref, "skillhub:") ||
		strings.HasPrefix(ref, "skill:")
}

func normalizeRuntimeMessageResponse(out *maclaw.RuntimeMessageResponse) {
	if out == nil || out.Message == nil {
		return
	}
	normalizeRuntimeMessage(out.Message)
}

func normalizeRuntimeMessages(messages []maclaw.RuntimeMessage) {
	for i := range messages {
		normalizeRuntimeMessage(&messages[i])
	}
}

func appendRuntimeJobProgressMessages(messages []maclaw.RuntimeMessage, sessionID string, jobs []maclaw.EvaluationJob) []maclaw.RuntimeMessage {
	if len(jobs) == 0 {
		return messages
	}
	existing := map[string]bool{}
	for _, msg := range messages {
		if msg.Metadata == nil {
			continue
		}
		if jobID := strings.TrimSpace(msg.Metadata["job_id"]); jobID != "" {
			existing[jobID] = true
		}
	}
	for _, job := range jobs {
		if strings.TrimSpace(job.ID) == "" || existing[job.ID] {
			continue
		}
		if job.Status != maclaw.EvaluationJobStatusRunning && job.Status != maclaw.EvaluationJobStatusPending {
			continue
		}
		progress := job.Progress
		if progress != nil && strings.TrimSpace(progress.SessionID) != "" && strings.TrimSpace(progress.SessionID) != sessionID {
			continue
		}
		if !jobWasTriggeredByConfirmedPlan(messages, job) {
			continue
		}
		createdAt := job.CreatedAt
		if job.StartedAt != nil {
			createdAt = *job.StartedAt
		}
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		runID := job.ID
		phase := string(job.Status)
		statusText := "评估任务正在执行，请稍候。"
		if progress != nil {
			runID = firstNonEmpty(progress.RunID, runID)
			phase = firstNonEmpty(progress.Phase, progress.Step, phase)
			statusText = firstNonEmpty(progress.StatusText, statusText)
		}
		metadata := map[string]string{
			"card_type":     "progress",
			"job_id":        job.ID,
			"assessment_id": runID,
			"phase":         phase,
			"status_text":   statusText,
		}
		if progress != nil {
			if progress.DurationMs > 0 {
				metadata["duration_ms"] = strconv.FormatInt(progress.DurationMs, 10)
			}
			if strings.TrimSpace(progress.StageDurationsJSON) != "" {
				metadata["stage_durations_json"] = progress.StageDurationsJSON
			}
			if progress.PlannedCount > 0 {
				metadata["planned_count"] = strconv.Itoa(progress.PlannedCount)
			}
			if progress.ExecutedCount >= 0 && progress.PlannedCount > 0 {
				metadata["executed_count"] = strconv.Itoa(progress.ExecutedCount)
			}
			if strings.TrimSpace(progress.CurrentStage) != "" {
				metadata["current_stage"] = progress.CurrentStage
			}
		}
		messages = append(messages, maclaw.RuntimeMessage{
			ID:         "job-progress:" + job.ID,
			SessionID:  sessionID,
			Role:       "assistant",
			OutputType: "application/vnd.maclaw.progress+json",
			Metadata:   metadata,
			CreatedAt:  createdAt,
		})
	}
	return messages
}

func jobWasTriggeredByConfirmedPlan(messages []maclaw.RuntimeMessage, job maclaw.EvaluationJob) bool {
	progress := job.Progress
	userMessageID := ""
	if progress != nil {
		userMessageID = strings.TrimSpace(progress.UserMessageID)
	}
	for _, msg := range messages {
		if strings.TrimSpace(msg.ID) != userMessageID || strings.TrimSpace(msg.Role) != "user" {
			continue
		}
		return msg.Metadata != nil && strings.TrimSpace(msg.Metadata["evaluation_action"]) == "confirm_plan"
	}
	return false
}

func filterBrowserRuntimeMessages(messages []maclaw.RuntimeMessage) []maclaw.RuntimeMessage {
	out := make([]maclaw.RuntimeMessage, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Metadata != nil && strings.TrimSpace(msg.Metadata["bff_internal_plan_rewrite"]) == "true" {
			continue
		}
		if strings.TrimSpace(msg.Role) == "user" {
			out = append(out, msg)
			continue
		}
		if isPlanConfirmMessage(msg) {
			out = append(out, msg)
			continue
		}
		if isRuntimeReportMessage(msg) {
			out = append(out, msg)
			continue
		}
		msg = downgradeRuntimeProgressForBrowser(msg)
		out = append(out, msg)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func downgradeRuntimeProgressForBrowser(msg maclaw.RuntimeMessage) maclaw.RuntimeMessage {
	if strings.TrimSpace(msg.Role) == "user" || msg.Metadata == nil {
		return msg
	}
	cardType := strings.ToLower(strings.TrimSpace(msg.Metadata["card_type"]))
	eventType := strings.ToLower(strings.TrimSpace(msg.Metadata["evaluation_event_type"]))
	if cardType != "progress" && eventType != "progress" && eventType != "tool_call" {
		return msg
	}
	statusText := strings.TrimSpace(msg.Metadata["status_text"])
	if statusText == "" {
		statusText = strings.TrimSpace(msg.Content)
	}
	if statusText == "" {
		statusText = "正在生成回复，请稍候。"
	}
	metadata := make(map[string]string, len(msg.Metadata))
	for key, value := range msg.Metadata {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "card_type", "phase", "status_text", "job_id", "assessment_id", "evaluation_event_type", "progress_phase", "steps":
			continue
		default:
			metadata[key] = value
		}
	}
	metadata["card_type"] = "text"
	metadata["response_source"] = firstNonEmpty(metadata["response_source"], "chat")
	msg.Content = statusText
	msg.Metadata = metadata
	return msg
}

func normalizeRuntimeMessage(msg *maclaw.RuntimeMessage) {
	if msg == nil {
		return
	}
	stripPrivilegedRuntimeMessageMetadata(msg)
	body, ok := runtimePlanJSON(msg.Content)
	if !ok {
		normalizeRuntimeReportMessage(msg)
		return
	}
	responseSource := strings.TrimSpace(fmtAnyString(body["response_source"]))
	if responseSource == "ask_user" {
		normalizeAskUserRuntimeMessage(msg, body)
		return
	}
	if responseSource != "plan_confirm" {
		normalizeRuntimeReportMessage(msg)
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	msg.Metadata["response_source"] = "plan_confirm"
	msg.Metadata["evaluation_event_type"] = "plan_confirm"
	if strings.TrimSpace(msg.OutputType) == "" {
		msg.OutputType = "application/vnd.maclaw.plan-confirm+json"
	}
	if data, err := json.Marshal(body); err == nil {
		msg.Content = string(data)
	}
}

func normalizeRuntimeReportMessage(msg *maclaw.RuntimeMessage) {
	if msg == nil {
		return
	}
	reportID := ""
	if msg.Metadata != nil {
		reportID = strings.TrimSpace(msg.Metadata["report_id"])
	}
	if reportID == "" {
		reportID = extractRedteamReportID(msg.Content)
	}
	if reportID == "" {
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	if parsed := runtimeReportJSON(msg.Content); parsed != nil {
		for _, key := range []string{"risk_level", "safety_score", "executed_count", "planned_count", "success_count", "failure_count"} {
			if value := strings.TrimSpace(fmtAnyString(parsed[key])); value != "" && strings.TrimSpace(msg.Metadata[key]) == "" {
				msg.Metadata[key] = value
			}
		}
		if counts, ok := parsed["counts"].(map[string]any); ok {
			for source, target := range map[string]string{"success": "success_count", "failure": "failure_count"} {
				if value := strings.TrimSpace(fmtAnyString(counts[source])); value != "" && strings.TrimSpace(msg.Metadata[target]) == "" {
					msg.Metadata[target] = value
				}
			}
			if strings.TrimSpace(msg.Metadata["failure_count"]) == "" {
				failure := 0
				for _, key := range []string{"blocked", "invalid", "uncertain"} {
					if value := strings.TrimSpace(fmtAnyString(counts[key])); value != "" {
						if parsedValue, err := strconv.Atoi(value); err == nil {
							failure += parsedValue
						}
					}
				}
				if failure > 0 {
					msg.Metadata["failure_count"] = strconv.Itoa(failure)
				}
			}
		}
	}
	msg.Metadata["report_id"] = reportID
	msg.Metadata["response_source"] = "report"
	msg.Metadata["evaluation_event_type"] = "report"
	msg.Metadata["download_format"] = "pdf"
	msg.Metadata["downloadable"] = "true"
	if strings.TrimSpace(msg.Metadata["summary"]) == "" {
		msg.Metadata["summary"] = "评估任务已完成，可下载 PDF 报告。"
	}
}

type runtimeReportGetter interface {
	GetEvaluationReport(context.Context, string) (*maclaw.EvaluationReport, error)
}

func hydrateRuntimeReportMessages(ctx context.Context, gateway maclaw.RuntimeGateway, messages []maclaw.RuntimeMessage) {
	getter, ok := gateway.(runtimeReportGetter)
	if !ok || getter == nil {
		return
	}
	for i := range messages {
		if !isRuntimeReportMessage(messages[i]) {
			continue
		}
		reportID := ""
		if messages[i].Metadata != nil {
			reportID = strings.TrimSpace(messages[i].Metadata["report_id"])
		}
		if reportID == "" {
			reportID = extractRedteamReportID(messages[i].Content)
		}
		if reportID == "" {
			continue
		}
		report, err := getter.GetEvaluationReport(ctx, reportID)
		if err != nil || report == nil {
			continue
		}
		applyRuntimeReportMetadata(&messages[i], report)
	}
}

func applyRuntimeReportMetadata(msg *maclaw.RuntimeMessage, report *maclaw.EvaluationReport) {
	if msg == nil || report == nil {
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	if strings.TrimSpace(msg.Metadata["risk_level"]) == "" && strings.TrimSpace(report.RiskLevel) != "" {
		msg.Metadata["risk_level"] = strings.TrimSpace(report.RiskLevel)
	}
	if strings.TrimSpace(msg.Metadata["safety_score"]) == "" && report.SafetyScore != nil {
		msg.Metadata["safety_score"] = strconv.FormatFloat(*report.SafetyScore, 'f', -1, 64)
	}
	for _, key := range []string{"executed_count", "planned_count", "success_count", "failure_count", "total_cases", "attack_success_rate"} {
		if strings.TrimSpace(msg.Metadata[key]) == "" && strings.TrimSpace(report.Metadata[key]) != "" {
			msg.Metadata[key] = strings.TrimSpace(report.Metadata[key])
		}
	}
	if strings.TrimSpace(msg.Metadata["summary"]) == "" && strings.TrimSpace(report.Summary) != "" {
		msg.Metadata["summary"] = strings.TrimSpace(report.Summary)
	}
}

func runtimeReportJSON(content string) map[string]any {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		return nil
	}
	return body
}

func extractRedteamReportID(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	for _, field := range strings.FieldsFunc(content, func(r rune) bool {
		return !(r == '_' || r == '-' || r == ':' || r == '/' || r == '.' || r == '@' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	}) {
		field = strings.Trim(field, "`，。；;、")
		if strings.HasPrefix(field, "redteam_report_") {
			return field
		}
	}
	return ""
}

func normalizeAskUserRuntimeMessage(msg *maclaw.RuntimeMessage, body map[string]any) {
	if msg == nil {
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	msg.Metadata["response_source"] = "ask_user"
	msg.Metadata["evaluation_event_type"] = "ask_user"
	question := strings.TrimSpace(fmtAnyString(firstAny(body["question"], body["message"], body["content"])))
	if question == "" {
		question = "请补充必要信息。"
	}
	msg.Metadata["ask_user_question"] = question
	if rawOptions, ok := body["options"].([]any); ok && len(rawOptions) > 0 {
		options := make([]string, 0, len(rawOptions))
		for _, item := range rawOptions {
			if value := strings.TrimSpace(fmtAnyString(item)); value != "" {
				options = append(options, value)
			}
		}
		if len(options) > 0 {
			if data, err := json.Marshal(options); err == nil {
				msg.Metadata["ask_user_options_json"] = string(data)
			}
		}
	}
	msg.Content = question
	if strings.TrimSpace(msg.OutputType) == "" || strings.Contains(strings.ToLower(msg.OutputType), "json") {
		msg.OutputType = "text/plain"
	}
}

func stripPrivilegedRuntimeMessageMetadata(msg *maclaw.RuntimeMessage) {
	if msg == nil || msg.Metadata == nil {
		return
	}
	for _, key := range []string{
		redteamExecutionGrantMetadataKey,
		"evaluation_execution_grant_expires_at",
	} {
		delete(msg.Metadata, key)
	}
}

func runtimePlanJSON(content string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, false
	}
	candidates := runtimeJSONCandidates(trimmed)
	for _, candidate := range candidates {
		var body map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(candidate)), &body); err == nil {
			return body, true
		}
	}
	return nil, false
}

func runtimePlanPayloadJSON(content string) (map[string]any, bool) {
	body, ok := runtimePlanJSON(content)
	if !ok {
		return nil, false
	}
	plan, ok := body["plan"].(map[string]any)
	if !ok {
		return body, true
	}
	out := make(map[string]any, len(plan)+2)
	for key, value := range plan {
		out[key] = value
	}
	for _, key := range []string{"response_source", "evaluation_event_type"} {
		if _, exists := out[key]; !exists {
			if value := body[key]; value != nil {
				out[key] = value
			}
		}
	}
	return out, true
}

func runtimeJSONCandidates(content string) []string {
	candidates := []string{content}
	if strings.HasPrefix(content, "```") {
		if fenced := stripMarkdownCodeFence(content); fenced != "" {
			candidates = append([]string{fenced}, candidates...)
		}
	}
	for _, fenced := range extractMarkdownJSONFences(content) {
		candidates = append([]string{fenced}, candidates...)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	return out
}

func extractMarkdownJSONFences(content string) []string {
	lines := strings.Split(content, "\n")
	out := []string{}
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			continue
		}
		start := i + 1
		for j := start; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
				if block := strings.TrimSpace(strings.Join(lines[start:j], "\n")); block != "" {
					out = append(out, block)
				}
				i = j
				break
			}
		}
	}
	return out
}

func stripMarkdownCodeFence(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) < 2 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		return ""
	}
	end := len(lines)
	for i := len(lines) - 1; i > 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			end = i
			break
		}
	}
	if end <= 1 {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines[1:end], "\n"))
}

func firstAny(values ...any) any {
	for _, value := range values {
		if strings.TrimSpace(fmtAnyString(value)) != "" {
			return value
		}
	}
	return nil
}

func fmtAnyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := strconv.Atoi(typed.String())
		return parsed
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

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
