package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
)

const (
	redteamVerifiedSessionContextKey    = "redteam_verified_execution_session_id"
	redteamVerifiedTestCountContextKey  = "redteam_verified_execution_test_count"
	redteamVerifiedCapabilityRefsKey    = "redteam_verified_capability_refs"
	redteamVerifiedSkillNamesKey        = "redteam_verified_skill_names"
	redteamVerifiedSelectionStrategyKey = "redteam_verified_selection_strategy"
)

type RedteamMCPHandler struct {
	bridge *maclaw.RedteamToolBridge
	secret string
}

type redteamMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type redteamMCPToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func NewRedteamMCPHandler(bridge *maclaw.RedteamToolBridge, secret string) *RedteamMCPHandler {
	return &RedteamMCPHandler{bridge: bridge, secret: strings.TrimSpace(secret)}
}

func (h *RedteamMCPHandler) Handle(c *gin.Context) {
	if !h.authorized(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req redteamMCPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json-rpc request"})
		return
	}
	c.Header("Mcp-Session-Id", firstNonEmpty(c.GetHeader("Mcp-Session-Id"), "evaluating-platform-redteam"))
	switch req.Method {
	case "initialize":
		h.writeResult(c, req.ID, gin.H{
			"protocolVersion": "2025-03-26",
			"capabilities":    gin.H{"tools": gin.H{}},
			"serverInfo":      gin.H{"name": "evaluating-platform-redteam-tools", "version": "1.0.0"},
		})
	case "tools/list":
		h.writeResult(c, req.ID, gin.H{"tools": redteamMCPToolDefinitions()})
	case "tools/call":
		h.handleToolCall(c, req)
	default:
		h.writeError(c, req.ID, -32601, "method not found")
	}
}

func (h *RedteamMCPHandler) authorized(c *gin.Context) bool {
	if h == nil || h.bridge == nil || h.secret == "" {
		return false
	}
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	return auth == "Bearer "+h.secret
}

func (h *RedteamMCPHandler) handleToolCall(c *gin.Context, req redteamMCPRequest) {
	var params redteamMCPToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		h.writeError(c, req.ID, -32602, "invalid tool call params")
		return
	}
	out, err := h.callTool(c, strings.TrimSpace(params.Name), params.Arguments)
	if err != nil {
		h.writeToolText(c, req.ID, gin.H{"error": err.Error()}, true)
		return
	}
	h.writeToolText(c, req.ID, out, false)
}

func (h *RedteamMCPHandler) callTool(c *gin.Context, name string, raw json.RawMessage) (any, error) {
	name = strings.TrimSpace(name)
	if redteamToolRequiresExecutionGrant(name) {
		if err := h.requireExecutionGrant(c, raw); err != nil {
			return nil, err
		}
	}
	switch name {
	case "search_platform_redteam_capabilities", "search_redteam_capabilities":
		if err := requireEnterpriseRedteamMCPRole(c); err != nil {
			return nil, err
		}
		var in maclaw.SearchRedteamCapabilitiesInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return h.bridge.SearchRedteamCapabilities(c.Request.Context(), in)
	case "search_redteam_plugin_catalog":
		if err := requireEnterpriseRedteamMCPRole(c); err != nil {
			return nil, err
		}
		var in maclaw.SearchRedteamPluginCatalogInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return h.bridge.SearchRedteamPluginCatalog(in)
	case "run_promptfoo_redteam_evaluation":
		var in maclaw.PromptfooEngineRunInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		in.ExecutionUserID = redteamMCPPlatformUserID(c)
		in.ExecutionSessionID = redteamVerifiedExecutionSessionID(c, raw)
		in.Metadata = metadataWithVerifiedSession(in.Metadata, in.ExecutionSessionID)
		if confirmedTestCount := redteamVerifiedExecutionTestCount(c); confirmedTestCount > 0 && in.NumTests <= 0 {
			in.NumTests = confirmedTestCount
		}
		return h.bridge.RunPromptfooRedteamEvaluation(c.Request.Context(), in.ExecutionUserID, redteamMCPInstanceID(c), in)
	case "get_promptfoo_evaluation_result":
		if err := requireEnterpriseRedteamMCPRole(c); err != nil {
			return nil, err
		}
		var in struct {
			EngineRunID string `json:"engine_run_id,omitempty"`
		}
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return h.bridge.GetPromptfooEvaluationResult(c.Request.Context(), redteamMCPPlatformUserID(c), in.EngineRunID)
	case "get_capability_detail":
		if err := requireEnterpriseRedteamMCPRole(c); err != nil {
			return nil, err
		}
		var in maclaw.GetCapabilityDetailInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return h.bridge.GetCapabilityDetail(c.Request.Context(), in)
	case "prepare_redteam_capability":
		var in maclaw.PrepareRedteamCapabilityInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return &maclaw.PrepareRedteamCapabilityOutput{
			PreparedRefs: uniqueNonEmptyStrings(in.CapabilityRefs),
			Mode:         "mcp_context_only",
		}, nil
	case "prepare_skill_input_data":
		var in maclaw.PrepareSkillInputDataInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		in.ExecutionUserID = redteamMCPPlatformUserID(c)
		in.ExecutionSessionID = redteamVerifiedExecutionSessionID(c, raw)
		in.Metadata = metadataWithVerifiedSession(in.Metadata, in.ExecutionSessionID)
		if in.Limit <= 0 {
			in.Limit = redteamVerifiedExecutionTestCount(c)
		}
		if len(in.SampleRefs) == 0 && len(in.ComposedAttackRefs) == 0 {
			sampleRefs, composedRefs := selectedRedteamDataRefs(redteamVerifiedSelectedCapabilityRefs(c))
			in.SampleRefs = sampleRefs
			in.ComposedAttackRefs = composedRefs
		}
		return h.bridge.PrepareSkillInputData(c.Request.Context(), in)
	case "compose_redteam_payloads":
		var in maclaw.ComposeRedteamPayloadsInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		in.ExecutionUserID = redteamMCPPlatformUserID(c)
		in.ExecutionSessionID = redteamVerifiedExecutionSessionID(c, raw)
		in.Metadata = metadataWithVerifiedSession(in.Metadata, in.ExecutionSessionID)
		return h.bridge.ComposeRedteamPayloads(c.Request.Context(), in)
	case "register_skill_payload_dataset":
		var in maclaw.RegisterSkillPayloadDatasetInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		in.ExecutionUserID = redteamMCPPlatformUserID(c)
		in.ExecutionSessionID = redteamVerifiedExecutionSessionID(c, raw)
		in.Metadata = metadataWithVerifiedSession(in.Metadata, in.ExecutionSessionID)
		return h.bridge.RegisterSkillPayloadDataset(c.Request.Context(), in)
	case "execute_redteam_evaluation_batch":
		var in maclaw.ExecuteRedteamEvaluationBatchInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		in.ExecutionUserID = redteamMCPPlatformUserID(c)
		in.ExecutionSessionID = redteamVerifiedExecutionSessionID(c, raw)
		if confirmedTestCount := redteamVerifiedExecutionTestCount(c); confirmedTestCount > 0 {
			in.TestCount = confirmedTestCount
		}
		in.Metadata = metadataWithVerifiedSession(in.Metadata, in.ExecutionSessionID)
		if len(in.SelectedSkills) == 0 {
			in.SelectedSkills = redteamVerifiedSelectedSkillNames(c)
		}
		if strings.TrimSpace(in.SelectionStrategy) == "" {
			in.SelectionStrategy = redteamVerifiedSelectionStrategy(c)
		}
		return h.bridge.ExecuteRedteamEvaluationBatch(c.Request.Context(), redteamMCPPlatformUserID(c), redteamMCPInstanceID(c), in)
	case "call_evaluation_target":
		var in maclaw.CallEvaluationTargetInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		in.ExecutionSessionID = redteamVerifiedExecutionSessionID(c, raw)
		in.Metadata = metadataWithVerifiedSession(in.Metadata, in.ExecutionSessionID)
		return h.bridge.CallEvaluationTarget(c.Request.Context(), redteamMCPPlatformUserID(c), in)
	case "judge_attack_result":
		var in maclaw.JudgeAttackResultInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return h.bridge.JudgeAttackResult(c.Request.Context(), in)
	case "save_redteam_evidence":
		var in maclaw.RedteamEvidenceInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return h.bridge.SaveRedteamEvidence(c.Request.Context(), redteamMCPPlatformUserID(c), redteamMCPInstanceID(c), in)
	case "compile_redteam_report":
		var in maclaw.CompileRedteamReportInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return h.bridge.CompileRedteamReport(c.Request.Context(), redteamMCPPlatformUserID(c), redteamMCPInstanceID(c), in)
	default:
		return nil, errUnknownMCPTool(name)
	}
}

func requireEnterpriseRedteamMCPRole(c *gin.Context) error {
	role := redteamMCPPlatformRole(c)
	if role == "enterprise" || role == "admin" {
		return nil
	}
	return mcpToolError("enterprise or admin platform identity is required for redteam capability search")
}

func redteamToolRequiresExecutionGrant(name string) bool {
	switch strings.TrimSpace(name) {
	case "compose_redteam_payloads",
		"prepare_skill_input_data",
		"register_skill_payload_dataset",
		"execute_redteam_evaluation_batch",
		"run_promptfoo_redteam_evaluation",
		"call_evaluation_target",
		"judge_attack_result",
		"save_redteam_evidence",
		"compile_redteam_report":
		return true
	default:
		return false
	}
}

func (h *RedteamMCPHandler) requireExecutionGrant(c *gin.Context, raw json.RawMessage) error {
	userID := ""
	if c != nil {
		userID = strings.TrimSpace(c.GetHeader("X-Evaluating-Platform-User-ID"))
	}
	if userID == "" {
		return mcpToolError("execution grant requires platform user identity")
	}
	token, fallbackSessionID := redteamExecutionGrantContextFromRawArgs(raw)
	payload, err := verifyRedteamExecutionGrantPayload(h.secret, token, userID, time.Now())
	if err != nil {
		return mcpToolError(err.Error())
	}
	sessionID := strings.TrimSpace(payload.SessionID)
	if sessionID == "" {
		sessionID = fallbackSessionID
	}
	if sessionID == "" && c != nil {
		sessionID = strings.TrimSpace(c.GetHeader("X-Evaluating-Platform-Session-ID"))
	}
	if c != nil && sessionID != "" {
		c.Set(redteamVerifiedSessionContextKey, sessionID)
	}
	if c != nil && payload.TestCount > 0 {
		c.Set(redteamVerifiedTestCountContextKey, payload.TestCount)
	}
	if c != nil && len(payload.SelectedCapabilityRefs) > 0 {
		c.Set(redteamVerifiedCapabilityRefsKey, append([]string(nil), payload.SelectedCapabilityRefs...))
	}
	if c != nil && len(payload.SelectedSkillNames) > 0 {
		c.Set(redteamVerifiedSkillNamesKey, append([]string(nil), payload.SelectedSkillNames...))
	}
	if c != nil && strings.TrimSpace(payload.SelectionStrategy) != "" {
		c.Set(redteamVerifiedSelectionStrategyKey, strings.TrimSpace(payload.SelectionStrategy))
	}
	return nil
}

func redteamVerifiedExecutionSessionID(c *gin.Context, raw json.RawMessage) string {
	if c != nil {
		if value, ok := c.Get(redteamVerifiedSessionContextKey); ok {
			if sessionID := strings.TrimSpace(stringFromAny(value)); sessionID != "" {
				return sessionID
			}
		}
	}
	_, sessionID := redteamExecutionGrantContextFromRawArgs(raw)
	return sessionID
}

func redteamVerifiedExecutionTestCount(c *gin.Context) int {
	if c == nil {
		return 0
	}
	value, ok := c.Get(redteamVerifiedTestCountContextKey)
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func redteamVerifiedSelectedCapabilityRefs(c *gin.Context) []string {
	if c == nil {
		return nil
	}
	value, ok := c.Get(redteamVerifiedCapabilityRefsKey)
	if !ok {
		return nil
	}
	items, ok := value.([]string)
	if !ok {
		return nil
	}
	return append([]string(nil), items...)
}

func redteamVerifiedSelectedSkillNames(c *gin.Context) []string {
	if c == nil {
		return nil
	}
	value, ok := c.Get(redteamVerifiedSkillNamesKey)
	if !ok {
		return nil
	}
	items, ok := value.([]string)
	if !ok {
		return nil
	}
	return append([]string(nil), items...)
}

func redteamVerifiedSelectionStrategy(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, ok := c.Get(redteamVerifiedSelectionStrategyKey)
	if !ok {
		return ""
	}
	return strings.TrimSpace(stringFromAny(value))
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case *string:
		if typed != nil {
			return *typed
		}
	default:
		return ""
	}
	return ""
}

func metadataWithVerifiedSession(metadata map[string]string, sessionID string) map[string]string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return metadata
	}
	out := make(map[string]string, len(metadata)+1)
	for key, value := range metadata {
		out[key] = value
	}
	out["session_id"] = sessionID
	return out
}

func redteamExecutionGrantContextFromRawArgs(raw json.RawMessage) (token string, sessionID string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	var body struct {
		SessionID string            `json:"session_id"`
		Metadata  map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", ""
	}
	sessionID = firstNonEmpty(body.SessionID, body.Metadata["session_id"], body.Metadata["evaluation_session_id"])
	return strings.TrimSpace(body.Metadata[redteamExecutionGrantMetadataKey]), strings.TrimSpace(sessionID)
}

func (h *RedteamMCPHandler) writeResult(c *gin.Context, id any, result any) {
	c.JSON(http.StatusOK, gin.H{"jsonrpc": "2.0", "id": id, "result": result})
}

func (h *RedteamMCPHandler) writeError(c *gin.Context, id any, code int, message string) {
	c.JSON(http.StatusOK, gin.H{"jsonrpc": "2.0", "id": id, "error": gin.H{"code": code, "message": message}})
}

func (h *RedteamMCPHandler) writeToolText(c *gin.Context, id any, out any, isError bool) {
	data, err := json.Marshal(out)
	if err != nil {
		data = []byte(`{"error":"failed to encode tool result"}`)
		isError = true
	}
	c.JSON(http.StatusOK, gin.H{
		"jsonrpc": "2.0",
		"id":      id,
		"result": gin.H{
			"content": []gin.H{{"type": "text", "text": string(data)}},
			"isError": isError,
		},
	})
}

func redteamMCPToolDefinitions() []gin.H {
	return []gin.H{
		toolDef("search_platform_redteam_capabilities", "Search safe platform security-evaluation data resources and expert MCP capability summaries. Use MaClaw native manage_skill for Skills.", []string{"query"}, gin.H{
			"query":        stringSchema("Natural-language search query, for example classical Chinese jailbreak."),
			"risk_types":   stringArraySchema("Optional risk filters such as jailbreak or prompt_injection."),
			"target_types": stringArraySchema("Optional target filters such as llm."),
			"languages":    stringArraySchema("Optional language filters such as zh or classical_chinese."),
			"limit":        integerSchema("Maximum results. Default 5, maximum 8."),
		}),
		toolDef("search_redteam_capabilities", "Compatibility alias for search_platform_redteam_capabilities. Does not execute or return Skill archives.", []string{"query"}, gin.H{
			"query":        stringSchema("Natural-language search query, for example classical Chinese jailbreak."),
			"risk_types":   stringArraySchema("Optional risk filters such as jailbreak or prompt_injection."),
			"target_types": stringArraySchema("Optional target filters such as llm."),
			"languages":    stringArraySchema("Optional language filters such as zh or classical_chinese."),
			"limit":        integerSchema("Maximum results. Default 5, maximum 8."),
		}),
		toolDef("search_redteam_plugin_catalog", "Search the built-in promptfoo-engine plugin and strategy catalog (harmful, pii, prompt-injection, jailbreak, bias, security-exploit and strategy wrappers). Returns safe cards with plugin ids for run_promptfoo_redteam_evaluation.", []string{"query"}, gin.H{
			"query": stringSchema("Natural-language search query such as harmful content, privacy leak, prompt injection, jailbreak, or promptfoo."),
			"limit": integerSchema("Maximum results. Default 5, maximum 8."),
		}),
		toolDef("run_promptfoo_redteam_evaluation", "Execute a confirmed promptfoo-engine security evaluation in one platform-controlled tool call. Generates attack cases with the platform generation LLM, runs them against the configured target, judges with llm-rubric, saves safe evidence, and compiles the fixed Chinese report. Requires an execution grant. Never returns raw payloads, raw target responses, credentials, tokens, or local paths.", []string{"run_id", "purpose"}, gin.H{
			"run_id":     stringSchema("Runtime run id."),
			"session_id": stringSchema("Runtime session id that received the execution confirmation grant."),
			"purpose":    stringSchema("Chinese evaluation purpose describing the system under test."),
			"num_tests":  gin.H{"type": "integer", "description": "Number of attack cases to generate. Default 5, maximum 50.", "minimum": 1, "maximum": 50},
			"plugins":    stringArraySchema("Plugin ids from search_redteam_plugin_catalog, for example harmful, pii, prompt-injection, jailbreak."),
			"strategies": stringArraySchema("Optional strategy ids such as direct, role-play, encoding, multi-turn."),
			"judge_mode": stringSchema("Optional judge mode: auto, promptfoo_native, or platform_rejudge."),
			"metadata":   metadataSchema(),
		}),
		toolDef("get_promptfoo_evaluation_result", "Get the safe status and sanitized result of one promptfoo-engine run owned by the caller. Returns counts, severity, and plugin statistics only.", []string{"engine_run_id"}, gin.H{
			"engine_run_id": stringSchema("Engine run id returned by run_promptfoo_redteam_evaluation."),
		}),
		toolDef("get_capability_detail", "Get one safe security-evaluation capability card by ref.", []string{"capability_ref"}, gin.H{
			"capability_ref": stringSchema("Capability source_ref returned by search_platform_redteam_capabilities."),
		}),
		toolDef("prepare_redteam_capability", "Declare selected capability refs for a confirmed run. Does not expose payloads or archives.", []string{"capability_refs"}, gin.H{
			"capability_refs": stringArraySchema("Capability refs selected in plan_confirm."),
		}),
		toolDef("prepare_skill_input_data", "Prepare selected expert sample/composed-attack questions for a confirmed native Skill run. Requires an execution grant and returns only the selected data needed by the Skill; never exposes credentials, tokens, archives, target secrets, evidence content, or local paths.", []string{"run_id"}, gin.H{
			"run_id":               stringSchema("Runtime run id."),
			"session_id":           stringSchema("Runtime session id that received the execution confirmation grant."),
			"sample_refs":          stringArraySchema("Sample refs selected in plan_confirm, for example sample:<uuid>."),
			"composed_attack_refs": stringArraySchema("Composed attack refs selected in plan_confirm, for example composed_attack:<uuid>."),
			"limit":                gin.H{"type": "integer", "description": "Maximum questions to prepare. Default 5, maximum 20.", "minimum": 1, "maximum": 20},
			"metadata":             metadataSchema(),
		}),
		toolDef("compose_redteam_payloads", "Compose confirmed expert samples/templates, directly use confirmed expert samples, or use composed attacks into server-side payload handles. Returns handles and safe summaries only, never raw prompt content.", []string{"run_id"}, gin.H{
			"run_id":               stringSchema("Runtime run id."),
			"session_id":           stringSchema("Runtime session id that received the execution confirmation grant."),
			"sample_refs":          stringArraySchema("Sample refs selected in plan_confirm, for example sample:<uuid>."),
			"template_refs":        stringArraySchema("Template refs selected in plan_confirm, for example template:<uuid>."),
			"composed_attack_refs": stringArraySchema("Ready-to-run composed attack refs selected in plan_confirm, for example composed_attack:<uuid>."),
			"limit":                gin.H{"type": "integer", "description": "Maximum payload handles. Default 5, maximum 50.", "minimum": 1, "maximum": 50},
			"strategy":             stringSchema("Optional composition strategy label."),
			"metadata":             metadataSchema(),
		}),
		toolDef("register_skill_payload_dataset", "Register payload_dataset output from a confirmed MaClaw native Skill run as server-side payload handles for this run. Use before execute_redteam_evaluation_batch when plan_confirm selected a Skill. Never returns raw payload text.", []string{"run_id", "skill_name", "payload_dataset"}, gin.H{
			"run_id":          stringSchema("Runtime run id."),
			"session_id":      stringSchema("Runtime session id that received the execution confirmation grant."),
			"skill_name":      stringSchema("Selected native Skill name, for example ccbos-classical-chinese-skill."),
			"payload_dataset": gin.H{"type": "object", "description": "Skill output payload_dataset object containing payloads[].payload_text."},
			"metadata":        metadataSchema(),
		}),
		toolDef("execute_redteam_evaluation_batch", "Execute a confirmed security-evaluation batch in one platform-controlled tool call. Composes payload handles, calls the configured target with bounded concurrency, judges results, saves evidence, and compiles the fixed Chinese report. Never returns raw payloads, raw target responses, credentials, tokens, or local paths.", []string{"run_id"}, gin.H{
			"run_id":               stringSchema("Runtime run id."),
			"session_id":           stringSchema("Runtime session id that received the execution confirmation grant."),
			"test_count":           gin.H{"type": "integer", "description": "Maximum final payloads to execute. Default 5, maximum 50.", "minimum": 1, "maximum": 50},
			"selection_strategy":   stringSchema("Selection strategy from plan_confirm, for example random, sequential, risk_coverage, or maclaw_selected."),
			"sample_refs":          stringArraySchema("Sample refs selected in plan_confirm, for example sample:<uuid>."),
			"template_refs":        stringArraySchema("Template refs selected in plan_confirm, for example template:<uuid>."),
			"composed_attack_refs": stringArraySchema("Ready-to-run composed attack refs selected in plan_confirm, for example composed_attack:<uuid>."),
			"payload_handles":      stringArraySchema("Optional payload handles already prepared for this run."),
			"selected_skills":      stringArraySchema("Skill names selected in plan_confirm. If provided, payload_handles must come from register_skill_payload_dataset."),
			"judge_mode":           stringSchema("Optional judge mode: auto, rules, or llm."),
			"metadata":             metadataSchema(),
		}),
		toolDef("call_evaluation_target", "Call or prepare a controlled OpenAI-compatible LLM target interaction. Consumes payload_handle server-side and never returns target secret, raw payload, or full target response.", []string{"run_id"}, gin.H{
			"run_id":         stringSchema("Runtime run id."),
			"session_id":     stringSchema("Runtime session id that received the execution confirmation grant."),
			"target_id":      stringSchema("Optional evaluation target id."),
			"payload_handle": stringSchema("Optional prepared payload handle."),
			"prompt":         stringSchema("Confirmed target input to send. This value is consumed server-side and never returned."),
			"summary":        stringSchema("Safe summary of the intended target call."),
			"metadata":       metadataSchema(),
		}),
		toolDef("judge_attack_result", "Judge whether a confirmed target interaction is attack success or attack failure. Returns binary success/failure only and never returns raw payload or target response.", []string{"run_id"}, gin.H{
			"run_id":            stringSchema("Runtime run id."),
			"session_id":        stringSchema("Runtime session id that received the execution confirmation grant."),
			"judge_mode":        stringSchema("Optional judge mode: auto, rules, or llm. Default auto uses the configured judge model when available and falls back to rules."),
			"payload_handle":    stringSchema("Prepared payload handle."),
			"response_handle":   stringSchema("Target response or call handle."),
			"call_handle":       stringSchema("Alias for response_handle."),
			"attack_type":       stringSchema("Attack type such as jailbreak or prompt_injection."),
			"expected_behavior": stringSchema("Safe description of the expected secure behavior."),
			"target_summary":    stringSchema("Safe target summary."),
			"response_summary":  stringSchema("Safe response summary. Do not include full target response content."),
			"status":            stringSchema("Target call status such as called, failed, timeout, or error."),
			"metadata":          metadataSchema(),
		}),
		toolDef("save_redteam_evidence", "Save security-evaluation evidence metadata and return a handle.", []string{"run_id"}, gin.H{
			"run_id":     stringSchema("Runtime run id."),
			"session_id": stringSchema("Runtime session id that received the execution confirmation grant."),
			"kind":       stringSchema("Evidence kind, for example artifact or tool_call."),
			"title":      stringSchema("Safe evidence title."),
			"summary":    stringSchema("Safe evidence summary without raw payload or response content."),
			"metadata":   metadataSchema(),
		}),
		toolDef("compile_redteam_report", "Compile a fixed Chinese production-audit security evaluation report. Returns a schema report and evidence handles only.", []string{"run_id"}, gin.H{
			"run_id":           stringSchema("Runtime run id."),
			"session_id":       stringSchema("Runtime session id that received the execution confirmation grant."),
			"title":            stringSchema("Chinese report title."),
			"summary":          stringSchema("Chinese safe report summary."),
			"risk_level":       stringSchema("Chinese risk level label."),
			"safety_score":     gin.H{"type": "number", "description": "Optional safety score."},
			"findings":         gin.H{"type": "array", "description": "Structured findings.", "items": gin.H{"type": "object"}},
			"evidence_handles": stringArraySchema("Evidence handles only, not evidence content."),
			"metadata":         metadataSchema(),
		}),
	}
}

func toolDef(name, description string, required []string, properties gin.H) gin.H {
	return gin.H{
		"name":        name,
		"description": description,
		"inputSchema": gin.H{
			"type":       "object",
			"required":   required,
			"properties": properties,
		},
	}
}

func stringSchema(description string) gin.H {
	return gin.H{"type": "string", "description": description}
}

func stringArraySchema(description string) gin.H {
	return gin.H{"type": "array", "description": description, "items": gin.H{"type": "string"}}
}

func integerSchema(description string) gin.H {
	return gin.H{"type": "integer", "description": description, "minimum": 1, "maximum": 8}
}

func metadataSchema() gin.H {
	return gin.H{
		"type":                 "object",
		"description":          "Safe metadata only. Do not include secrets, tokens, credentials, payloads, evidence content, archives, or local paths.",
		"additionalProperties": gin.H{"type": "string"},
	}
}

func decodeToolArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte(`{}`)
	}
	return json.Unmarshal(raw, out)
}

type mcpToolError string

func (e mcpToolError) Error() string { return string(e) }

func errUnknownMCPTool(name string) error {
	return mcpToolError("unknown redteam mcp tool: " + name)
}

func selectedRedteamDataRefs(items []string) (sampleRefs []string, composedAttackRefs []string) {
	for _, item := range uniqueNonEmptyStrings(items) {
		lower := strings.ToLower(strings.TrimSpace(item))
		switch {
		case strings.HasPrefix(lower, "sample:"):
			sampleRefs = append(sampleRefs, strings.TrimSpace(item))
		case strings.HasPrefix(lower, "composed_attack:"), strings.HasPrefix(lower, "composed:"):
			composedAttackRefs = append(composedAttackRefs, strings.TrimSpace(item))
		}
	}
	return sampleRefs, composedAttackRefs
}

func redteamMCPPlatformUserID(c *gin.Context) uuid.UUID {
	if c == nil {
		return uuid.Nil
	}
	raw := strings.TrimSpace(c.GetHeader("X-Evaluating-Platform-User-ID"))
	if raw == "" {
		return uuid.Nil
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}

func redteamMCPPlatformRole(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(c.GetHeader("X-Evaluating-Platform-Role")))
}

func redteamMCPInstanceID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetHeader("X-Evaluating-Platform-Instance-ID"))
}
