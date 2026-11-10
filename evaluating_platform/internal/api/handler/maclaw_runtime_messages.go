package handler

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
)

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

func (h *MaclawRuntimeHandler) normalizeRuntimeMessageResponse(c *gin.Context, out *maclaw.RuntimeMessageResponse) {
	if out == nil || out.Message == nil {
		return
	}
	h.normalizeRuntimeMessage(c, out.Message)
}

// normalizeRuntimeMessage applies the platform's target facts before the shared
// normalization, so the stored message and the card agree from the first read.
func (h *MaclawRuntimeHandler) normalizeRuntimeMessage(c *gin.Context, msg *maclaw.RuntimeMessage) {
	if msg == nil {
		return
	}
	if body, ok := runtimePlanJSON(msg.Content); ok && strings.TrimSpace(fmtAnyString(body["response_source"])) == "plan_confirm" {
		applyPlatformTargetFacts(body, h.platformTargetKind(c))
		if data, err := json.Marshal(body); err == nil {
			msg.Content = string(data)
		}
	}
	normalizeRuntimeMessage(msg)
}

func (h *MaclawRuntimeHandler) normalizeRuntimeMessages(c *gin.Context, messages []maclaw.RuntimeMessage) {
	kind := h.platformTargetKind(c)
	for i := range messages {
		if body, ok := runtimePlanJSON(messages[i].Content); ok && strings.TrimSpace(fmtAnyString(body["response_source"])) == "plan_confirm" {
			applyPlatformTargetFacts(body, kind)
			if data, err := json.Marshal(body); err == nil {
				messages[i].Content = string(data)
			}
		}
		normalizeRuntimeMessage(&messages[i])
	}
}

func normalizeRuntimeMessages(messages []maclaw.RuntimeMessage) {
	for i := range messages {
		normalizeRuntimeMessage(&messages[i])
	}
}


func (h *MaclawRuntimeHandler) platformTargetKind(c *gin.Context) string {
	if h == nil || h.targetConfig == nil || !h.targetConfig.Enabled() {
		return ""
	}
	userID, err := uuid.Parse(strings.TrimSpace(c.GetString("user_id")))
	if err != nil {
		return ""
	}
	targets, err := h.targetConfig.ListTargets(c.Request.Context(), userID, maclaw.EvaluationTargetQuery{IncludeInactive: true})
	if err != nil || len(targets) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(targets[0].Kind)))
}

// applyPlatformTargetFacts overwrites the target summary fields the planner
// guessed with the values the platform owns.
//
// Only the KIND is overridden. URL / model / name are left to the planner: it may
// legitimately describe a different view of the same target, and overwriting them
// would turn a cosmetic mismatch into a silent rewrite of what the user approved.
// The kind is different — it is an enum with two values, both of which this
// platform can read exactly.
func applyPlatformTargetFacts(body map[string]any, kind string) {
	if body == nil || kind == "" {
		return
	}
	before := fmtAnyString(body["target_type"])
	body["target_type"] = kind
	switch summary := body["target_summary"].(type) {
	case map[string]any:
		summary["target_type"] = kind
	case string:
		// The runtime may serialize the summary as JSON text; rewrite it in place
		// so both the object and the string form agree.
		var parsed map[string]any
		if json.Unmarshal([]byte(summary), &parsed) == nil {
			parsed["target_type"] = kind
			if encoded, err := json.Marshal(parsed); err == nil {
				body["target_summary"] = string(encoded)
			}
		}
	}
	// The card shows what the run will actually call. Recording both values keeps
	// the disagreement visible to anyone reading the stored message, which is how
	// a future planner drift gets noticed instead of silently corrected.
	if before != "" && !strings.EqualFold(before, kind) {
		body["target_type_claimed"] = before
	}
}

