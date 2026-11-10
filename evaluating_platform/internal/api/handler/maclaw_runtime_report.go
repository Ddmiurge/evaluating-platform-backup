package handler

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"evaluating_platform/internal/maclaw"
)

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
		for _, key := range []string{"risk_level", "safety_score", "executed_count", "planned_count", "success_count", "failure_count", maclaw.RedteamJudgeTrackMetadataKey, maclaw.RedteamGenerationPathMetadataKey, maclaw.RedteamNumTestsEffectiveMetadataKey} {
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
	for _, key := range []string{"executed_count", "planned_count", "success_count", "failure_count", "total_cases", "attack_success_rate", maclaw.RedteamGenerationPathMetadataKey, maclaw.RedteamNumTestsEffectiveMetadataKey} {
		if strings.TrimSpace(msg.Metadata[key]) == "" && strings.TrimSpace(report.Metadata[key]) != "" {
			msg.Metadata[key] = strings.TrimSpace(report.Metadata[key])
		}
	}
	// U4：把判定口径透传到聊天卡片 metadata，报告卡才能显示「这份分数是哪套口径算出来的」。
	// 用 report.JudgeTrack（已由后端从 metadata 解析、含旧报告推断）而非原始
	// metadata["judge_track"]，这样历史报告的卡片也能显示推断出的口径。
	if strings.TrimSpace(msg.Metadata[maclaw.RedteamJudgeTrackMetadataKey]) == "" {
		if track := strings.TrimSpace(string(report.JudgeTrack)); track != "" {
			msg.Metadata[maclaw.RedteamJudgeTrackMetadataKey] = track
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

