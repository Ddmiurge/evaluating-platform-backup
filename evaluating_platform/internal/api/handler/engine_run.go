package handler

// engine_run.go — BFF endpoints for promptfoo-engine runs (Phase 0).
//
// Route surface (registered in cmd/server/main.go):
//   GET    /maclaw/engine/health
//   POST   /maclaw/engine/runs          (submit + engine dispatch)
//   GET    /maclaw/engine/runs          (list)
//   GET    /maclaw/engine/runs/:id      (poll status)
//   POST   /maclaw/engine/runs/:id/cancel
//   GET    /maclaw/engine/runs/:id/events (SSE proxy)
//
// Security contract:
//   * Target credentials are materialized server-side from the encrypted
//     maclaw_target_configs store and forwarded to the engine one-shot over
//     the compose-internal network. They never appear in responses, logs,
//     or the persisted run record.
//   * Only safe progress metadata + redacted aggregate results are returned
//     to the browser.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
	"evaluating_platform/pkg/logger"
)

const (
	engineRunMaxNumTests  = 200
	engineRunDefaultTests = 20
)

// engineClient abstracts the promptfoo-engine REST client so tests can
// substitute fakes. *maclaw.PromptfooEngineClient satisfies it.
type engineClient interface {
	Enabled() bool
	Health(ctx context.Context) error
	CreateRun(ctx context.Context, run *maclaw.EngineRunRequest) error
	GetRun(ctx context.Context, runID string) (*maclaw.EngineRunStatus, error)
	CancelRun(ctx context.Context, runID string) error
	StreamRunEvents(ctx context.Context, runID string, onEvent func(*maclaw.EngineRunStatus)) error
}

// engineTargetProvider abstracts target-credential materialization so tests
// can substitute fakes. *maclaw.TargetConfigService satisfies it.
type engineTargetProvider interface {
	GetTargetWithSecret(ctx context.Context, userID uuid.UUID) (*maclaw.EvaluationTargetInput, error)
}

type EngineRunHandler struct {
	engine             engineClient
	runs               *maclaw.EngineRunService
	targetConfig       engineTargetProvider
	generationProvider maclaw.EngineGenerationProvider
	enabled            bool
}

func NewEngineRunHandler(engine *maclaw.PromptfooEngineClient, runs *maclaw.EngineRunService, targets engineTargetProvider, generationProvider maclaw.EngineGenerationProvider) *EngineRunHandler {
	return newEngineRunHandler(engine, runs, targets, generationProvider)
}

func newEngineRunHandler(engine engineClient, runs *maclaw.EngineRunService, targets engineTargetProvider, generationProvider maclaw.EngineGenerationProvider) *EngineRunHandler {
	return &EngineRunHandler{
		engine:             engine,
		runs:               runs,
		targetConfig:       targets,
		generationProvider: generationProvider,
		enabled:            engine != nil && engine.Enabled() && runs != nil,
	}
}

func (h *EngineRunHandler) Enabled() bool {
	return h != nil && h.enabled
}

// Health reports engine availability (safe metadata only).
func (h *EngineRunHandler) Health(c *gin.Context) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return
	}
	if !h.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "promptfoo engine is not configured", "code": "engine_not_configured"})
		return
	}
	if err := h.engine.Health(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "promptfoo engine unavailable", "code": "engine_unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

type engineRunCreateInput struct {
	SessionID  string                  `json:"session_id"`
	InstanceID string                  `json:"instance_id"`
	Purpose    string                  `json:"purpose"`
	NumTests   int                     `json:"num_tests"`
	JudgeMode  string                  `json:"judge_mode"`
	Plugins    []engineCapabilityInput `json:"plugins"`
	Strategies []engineCapabilityInput `json:"strategies"`
	// U7: optional per-request application description. Fields left empty fall
	// back to the target config's redteam_* metadata, so an operator can set
	// them once per target and still override for a single run.
	ApplicationDefinition *engineApplicationDefinitionInput `json:"application_definition"`
	// U7: extra generation constraints passed to every plugin.
	TestGenerationInstructions string `json:"test_generation_instructions"`
	// U7: prompt variable promptfoo injects payloads into; empty = engine default.
	InjectVar string `json:"inject_var"`
}

// engineApplicationDefinitionInput mirrors maclaw.EngineApplicationDefinition on
// the browser contract, so the same six fields can arrive from either the target
// config or a one-off run request.
type engineApplicationDefinitionInput struct {
	Purpose              string `json:"purpose"`
	Features             string `json:"features"`
	HasAccessTo          string `json:"has_access_to"`
	DoesNotHaveAccessTo  string `json:"does_not_have_access_to"`
	UserTypes            string `json:"user_types"`
	SecurityRequirements string `json:"security_requirements"`
}

// nativeGenerationOverridesFromInput converts the optional request fields into
// the override shape. A nil pointer means "not supplied, keep the target-level
// value"; a non-nil pointer to "" means "explicitly blank this".
func nativeGenerationOverridesFromInput(in engineRunCreateInput) *maclaw.NativeGenerationOverrides {
	ov := &maclaw.NativeGenerationOverrides{}
	if in.ApplicationDefinition != nil {
		ov.ApplicationDefinition = &maclaw.EngineApplicationDefinition{
			Purpose:              in.ApplicationDefinition.Purpose,
			Features:             in.ApplicationDefinition.Features,
			HasAccessTo:          in.ApplicationDefinition.HasAccessTo,
			DoesNotHaveAccessTo:  in.ApplicationDefinition.DoesNotHaveAccessTo,
			UserTypes:            in.ApplicationDefinition.UserTypes,
			SecurityRequirements: in.ApplicationDefinition.SecurityRequirements,
		}
	}
	if strings.TrimSpace(in.TestGenerationInstructions) != "" {
		v := in.TestGenerationInstructions
		ov.TestGenerationInstructions = &v
	}
	if strings.TrimSpace(in.InjectVar) != "" {
		v := in.InjectVar
		ov.InjectVar = &v
	}
	if ov.ApplicationDefinition == nil && ov.TestGenerationInstructions == nil && ov.InjectVar == nil {
		return nil
	}
	return ov
}

type engineCapabilityInput struct {
	ID     string                 `json:"id"`
	Config map[string]interface{} `json:"config"`
}

// CreateRun validates input, materializes target credentials, persists the
// initial record, and submits to the engine. The engine never sees platform
// user credentials; the browser never sees target credentials.
// requireEngineContext 统一 EngineRun 各 handler 的角色/引擎开关/用户前导（P2-02）。
func (h *EngineRunHandler) requireEngineContext(c *gin.Context) (uuid.UUID, bool) {
	if !requireEnterpriseMaclawExecutionRole(c) {
		return uuid.Nil, false
	}
	if !h.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "promptfoo engine is not configured", "code": "engine_not_configured"})
		return uuid.Nil, false
	}
	return h.currentUserID(c)
}

// loadRun 统一 runID 解析 + 平台记录加载 + 404 兜底（P2-02）。
func (h *EngineRunHandler) loadRun(c *gin.Context, userID uuid.UUID) (*maclaw.EngineRunRecord, string, bool) {
	runID := strings.TrimSpace(firstNonEmpty(c.Param("id"), c.Param("run_id"), c.Param("runId")))
	if runID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "run id is required"})
		return nil, "", false
	}
	record, err := h.runs.Get(c.Request.Context(), userID, runID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load engine run"})
		return nil, "", false
	}
	if record == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "engine run not found", "code": "engine_run_not_found"})
		return nil, "", false
	}
	return record, runID, true
}

// parseEngineCapabilityRefs 统一 plugins/strategies 输入的规范化校验（P2-02）。
func parseEngineCapabilityRefs(c *gin.Context, in []engineCapabilityInput, emptyMsg string) ([]maclaw.EngineCapabilityRef, bool) {
	out := make([]maclaw.EngineCapabilityRef, 0, len(in))
	for _, item := range in {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": emptyMsg})
			return nil, false
		}
		out = append(out, maclaw.EngineCapabilityRef{ID: id, Config: item.Config})
	}
	return out, true
}

// writeRemoteDepRejection renders the U6 pre-flight rejection.
//
// 409 rather than 400: the request is well-formed and the caller may retry it
// successfully by choosing a different capability. The body carries the
// machine-readable `code`, the blocked ids, and a `local_only_plugins` hint so
// the UI can offer a working alternative instead of a dead end. No run record is
// created and no engine call is made.
func writeRemoteDepRejection(c *gin.Context, err error) {
	var guard *maclaw.EngineRemoteDepGuardError
	if !errors.As(err, &guard) {
		// Not a guard error — fail closed without leaking internals.
		c.JSON(http.StatusConflict, gin.H{
			"error": "remote generation required",
			"code":  maclaw.EngineErrPluginRequiresRemoteGeneration,
		})
		return
	}
	c.JSON(http.StatusConflict, gin.H{
		"error":              guard.Error(),
		"code":               guard.Code,
		"blocked_plugins":    guard.GatedPlugins,
		"blocked_strategies": guard.GatedStrategies,
		"local_only_plugins": guard.LocalOnlyHint,
	})
}

func (h *EngineRunHandler) CreateRun(c *gin.Context) {
	userID, ok := h.requireEngineContext(c)
	if !ok {
		return
	}

	var in engineRunCreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	in.Purpose = strings.TrimSpace(in.Purpose)
	if in.Purpose == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "purpose is required"})
		return
	}
	if len(in.Plugins) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plugins are required"})
		return
	}
	judgeMode := maclaw.EngineJudgeAuto
	switch mode := maclaw.EngineJudgeMode(strings.TrimSpace(in.JudgeMode)); mode {
	case maclaw.EngineJudgeAuto, maclaw.EngineJudgePromptfooNative, maclaw.EngineJudgePlatformRejudge:
		judgeMode = mode
	case "":
		// default auto
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid judge_mode"})
		return
	}
	numTests := in.NumTests
	if numTests <= 0 {
		numTests = engineRunDefaultTests
	}
	if numTests > engineRunMaxNumTests {
		c.JSON(http.StatusBadRequest, gin.H{"error": "num_tests exceeds the maximum", "code": "num_tests_too_large"})
		return
	}

	// Materialize the stored (encrypted-at-rest) target credentials into a
	// one-shot engine credential struct. Server-side only.
	target, err := h.targetConfig.GetTargetWithSecret(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "maclaw target config unavailable", "code": "maclaw_target_config_unavailable"})
		return
	}
	if target == nil || !target.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no enabled target model configured", "code": "target_not_configured"})
		return
	}

	// Resolve the generation LLM from the platform default MaClaw model config
	// (same source as MaClaw attack generation / judge). The engine synthesizes
	// attack cases with it (Plan A), so a missing generation LLM is a hard
	// fail-fast: no run record is created.
	generation := maclaw.ResolveEngineGenerationCredential(c.Request.Context(), h.generationProvider)
	if generation == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "platform generation model is not configured", "code": "generation_not_configured"})
		return
	}

	plugins, ok := parseEngineCapabilityRefs(c, in.Plugins, "plugin id is required")
	if !ok {
		return
	}
	strategies, ok := parseEngineCapabilityRefs(c, in.Strategies, "strategy id is required")
	if !ok {
		return
	}

	// U6 pre-flight guard: reject remote-generation-gated capabilities BEFORE
	// any credential materialization, persistence or engine dispatch. Without
	// this, promptfoo accepts the plugin, `return []`s mid-run under
	// fail-closed, and the user gets a green run with 0 coverage for the
	// plugin they selected. Failing here also means a rejected request leaves
	// no orphaned engine_runs row.
	if err := maclaw.ValidateEngineRunRemoteGeneration(plugins, strategies); err != nil {
		writeRemoteDepRejection(c, err)
		return
	}

	source := maclaw.EngineRunSourceWizard
	if strings.TrimSpace(in.SessionID) != "" {
		source = maclaw.EngineRunSourceChat
	}
	record := h.runs.NewRun(userID, strings.TrimSpace(in.InstanceID), strings.TrimSpace(in.SessionID), source, in.Purpose, judgeMode, numTests, strings.TrimSpace(target.ID), plugins, strategies)
	saved, err := h.runs.Save(c.Request.Context(), record)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist engine run"})
		return
	}

	engineReq := &maclaw.EngineRunRequest{
		RunID:          saved.ID,
		PlatformUserID: userID.String(),
		Purpose:        in.Purpose,
		NumTests:       numTests,
		Plugins:        plugins,
		Strategies:     strategies,
		JudgeMode:      judgeMode,
		Credentials:    derefOrEmpty(maclaw.MaterializeEngineCredentialsWithGeneration(target, generation)),
		Catalog:        maclaw.PromptfooEngineCatalog(),
	}
	// U7: carry the application description through to the engine so promptfoo's
	// native generation can aim at this system rather than attack generically.
	maclaw.ResolveEngineNativeGenerationInputs(engineReq, target.Metadata)
	maclaw.ApplyNativeGenerationOverrides(engineReq, nativeGenerationOverridesFromInput(in))
	if err := h.engine.CreateRun(c.Request.Context(), engineReq); err != nil {
		// Mark the local record failed so the UI does not show a ghost queued run.
		saved.Status = maclaw.EnginePhaseFailed
		saved.ErrorCode = engineErrorCode(err)
		_, _ = h.runs.Save(c.Request.Context(), saved)
		c.JSON(engineHTTPStatus(err), gin.H{"error": "failed to submit engine run", "code": engineErrorCode(err)})
		return
	}

	c.JSON(http.StatusCreated, engineRunResponse(saved, nil))
}

// ListRuns returns the caller's runs (safe fields only).
func (h *EngineRunHandler) ListRuns(c *gin.Context) {
	userID, ok := h.requireEngineContext(c)
	if !ok {
		return
	}
	limit, err := parseOptionalPositiveInt(c.Query("limit"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
		return
	}
	sessionID := strings.TrimSpace(c.Query("session_id"))
	records, err := h.runs.List(c.Request.Context(), userID, sessionID, limit)
	if err != nil && !maclaw.IsPartialCorruption(err) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list engine runs"})
		return
	}
	// 降级放行：DB 里个别历史行的 JSONB 已损坏时，repository 跳过它们并
	// 上抛可观测的降级错误。这里记ERROR 后仍然返回 200 + 好行 ——
	// 让一条脏数据把整个 run 列表打成 500，代价远大于少显示一行。
	// 绝不静默：降级事实必须留痕（见 internal/maclaw/data_corruption.go）。
	if err != nil {
		logger.Error("engine run list partially degraded", map[string]interface{}{
			"err": err.Error(),
		})
	}
	items := make([]gin.H, 0, len(records))
	for i := range records {
		items = append(items, engineRunResponse(&records[i], nil))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// GetRun polls the engine and refreshes the local record before responding.
func (h *EngineRunHandler) GetRun(c *gin.Context) {
	userID, ok := h.requireEngineContext(c)
	if !ok {
		return
	}
	record, runID, ok := h.loadRun(c, userID)
	if !ok {
		return
	}
	if !maclaw.IsEngineRunTerminal(record.Status) {
		if status, err := h.engine.GetRun(c.Request.Context(), runID); err == nil {
			h.runs.ApplyStatus(record, status)
			if updated, err := h.runs.Save(c.Request.Context(), record); err == nil {
				record = updated
			}
		}
	}
	c.JSON(http.StatusOK, engineRunResponse(record, nil))
}

// CancelRun cancels an in-flight engine run.
func (h *EngineRunHandler) CancelRun(c *gin.Context) {
	userID, ok := h.requireEngineContext(c)
	if !ok {
		return
	}
	record, runID, ok := h.loadRun(c, userID)
	if !ok {
		return
	}
	if !maclaw.IsEngineRunTerminal(record.Status) {
		if err := h.engine.CancelRun(c.Request.Context(), runID); err != nil && err != maclaw.ErrEngineNotFound {
			c.JSON(engineHTTPStatus(err), gin.H{"error": "failed to cancel engine run", "code": engineErrorCode(err)})
			return
		}
		record.Status = maclaw.EnginePhaseCanceled
		if saved, err := h.runs.Save(c.Request.Context(), record); err == nil {
			record = saved
		}
	}
	c.JSON(http.StatusOK, engineRunResponse(record, nil))
}

// StreamRunEvents proxies the engine SSE stream to the browser. Only safe
// progress events pass through; no credentials or prompts are involved.
func (h *EngineRunHandler) StreamRunEvents(c *gin.Context) {
	userID, ok := h.requireEngineContext(c)
	if !ok {
		return
	}
	record, runID, ok := h.loadRun(c, userID)
	if !ok {
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	// Persist terminal snapshots so polling clients see the final state even
	// if they never re-connect to this stream.
	_ = h.engine.StreamRunEvents(c.Request.Context(), runID, func(ev *maclaw.EngineRunStatus) {
		writeEngineSSEEvent(c, ev)
		if flusher != nil {
			flusher.Flush()
		}
		h.runs.ApplyStatus(record, ev)
		if maclaw.IsEngineRunTerminal(ev.Phase) {
			_, _ = h.runs.Save(c.Request.Context(), record)
		}
	})
}

// derefOrEmpty maps a nil materialized credential to the zero value. The
// target is always non-nil here (validated above), but keep the call site
// panic-free regardless.
func derefOrEmpty(creds *maclaw.EngineOneShotCredentials) maclaw.EngineOneShotCredentials {
	if creds == nil {
		return maclaw.EngineOneShotCredentials{}
	}
	return *creds
}
func (h *EngineRunHandler) currentUserID(c *gin.Context) (uuid.UUID, bool) {
	userID, err := uuid.Parse(strings.TrimSpace(c.GetString("user_id")))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid user id", "code": "invalid_user"})
		return uuid.Nil, false
	}
	return userID, true
}

// engineRunResponse maps a persisted record to the safe browser DTO.
func engineRunResponse(record *maclaw.EngineRunRecord, extra map[string]interface{}) gin.H {
	out := gin.H{
		"id":             record.ID,
		"session_id":     record.SessionID,
		"instance_id":    record.InstanceID,
		"source":         record.Source,
		"purpose":        record.Purpose,
		"judge_mode":     string(record.JudgeMode),
		"num_tests":      record.NumTests,
		"phase":          string(record.Status),
		"planned_count":  record.PlannedCount,
		"executed_count": record.ExecutedCount,
		"current_stage":  record.CurrentStage,
		"duration_ms":    record.DurationMs,
		"error_code":     record.ErrorCode,
		"created_at":     record.CreatedAt,
		"updated_at":     record.UpdatedAt,
	}
	plugins := make([]string, 0, len(record.Plugins))
	for _, p := range record.Plugins {
		plugins = append(plugins, p.ID)
	}
	strategies := make([]string, 0, len(record.Strategies))
	for _, s := range record.Strategies {
		strategies = append(strategies, s.ID)
	}
	out["plugins"] = plugins
	out["strategies"] = strategies
	if record.Result != nil {
		out["result"] = record.Result
	}
	if record.StartedAt != nil {
		out["started_at"] = record.StartedAt
	}
	if record.CompletedAt != nil {
		out["finished_at"] = record.CompletedAt
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func writeEngineSSEEvent(c *gin.Context, ev *maclaw.EngineRunStatus) {
	if ev == nil {
		return
	}
	// Safe fields only — mirrors the engine's RunProgressEvent contract.
	payload := gin.H{
		"run_id":         ev.RunID,
		"phase":          string(ev.Phase),
		"status_text":    ev.StatusText,
		"planned_count":  ev.PlannedCount,
		"executed_count": ev.ExecutedCount,
		"current_stage":  ev.CurrentStage,
		"duration_ms":    ev.DurationMs,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = c.Writer.Write([]byte("event: progress\ndata: " + string(data) + "\n\n"))
}

func engineHTTPStatus(err error) int {
	switch err {
	case maclaw.ErrEngineQueueFull:
		return http.StatusTooManyRequests
	case maclaw.ErrEngineRunExists:
		return http.StatusConflict
	case maclaw.ErrEngineNotFound:
		return http.StatusNotFound
	case maclaw.ErrEngineUnauthorized:
		return http.StatusBadGateway
	case maclaw.ErrEngineUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func engineErrorCode(err error) string {
	switch err {
	case maclaw.ErrEngineQueueFull:
		return "engine_queue_full"
	case maclaw.ErrEngineRunExists:
		return "engine_run_exists"
	case maclaw.ErrEngineNotFound:
		return "engine_run_not_found"
	case maclaw.ErrEngineUnauthorized:
		return "engine_unauthorized"
	case maclaw.ErrEngineUnavailable:
		return "engine_unavailable"
	default:
		return "engine_submit_failed"
	}
}
