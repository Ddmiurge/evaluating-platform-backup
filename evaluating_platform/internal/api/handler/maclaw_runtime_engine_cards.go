package handler

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
)

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

// appendRunningEngineProgressMessages synthesizes a progress card for every
// engine job that has not reached a terminal phase.
//
// # Why (measured 2026-10-26)
//
// The engine fast path runs inside the BFF. The session snapshot used to
// synthesize cards for FINISHED engine jobs only, so a running evaluation
// existed solely in the browser's local state: refreshing the page, switching
// sessions and coming back, or reopening the route made it disappear. One user
// watched it vanish, concluded the run had failed, and pressed confirm again —
// two jobs, two reports, and twice the target calls on an already rate-limited
// target (session sess_ea7aee56, record 37).
//
// Only safe progress metadata is emitted — stage name, counts, milliseconds —
// the same fields AGENTS.md allows for progress cards. No prompts, payloads,
// target responses, credentials or paths.
func appendRunningEngineProgressMessages(messages []maclaw.RuntimeMessage, sessionID string, jobs []*maclaw.PlatformEngineJob) []maclaw.RuntimeMessage {
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
		if job == nil || strings.TrimSpace(job.ID) == "" || existing[job.ID] {
			continue
		}
		// Terminal jobs are the report path's business; a progress card next to a
		// finished report would claim work is still running.
		if job.Status != maclaw.EvaluationJobStatusPending && job.Status != maclaw.EvaluationJobStatusRunning {
			continue
		}
		metadata := map[string]string{
			"card_type":     "progress",
			"job_id":        job.ID,
			"assessment_id": firstNonEmpty(job.EngineRunID, job.ID),
			"phase":         string(job.Status),
			"status_text":   firstNonEmpty(job.StatusText, "评测正在执行，请稍候。"),
		}
		if job.PlannedCount > 0 {
			metadata["planned_count"] = strconv.Itoa(job.PlannedCount)
		}
		if job.PlannedCount > 0 && job.ExecutedCount >= 0 {
			metadata["executed_count"] = strconv.Itoa(job.ExecutedCount)
		}
		if stage := strings.TrimSpace(job.CurrentStage); stage != "" {
			metadata["current_stage"] = stage
		}
		if job.DurationMs > 0 {
			metadata["duration_ms"] = strconv.FormatInt(job.DurationMs, 10)
		}
		createdAt := job.StartedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		card := maclaw.RuntimeMessage{
			ID:         "engine-progress:" + job.ID,
			SessionID:  sessionID,
			Role:       "assistant",
			OutputType: "application/vnd.maclaw.progress+json",
			Metadata:   metadata,
			CreatedAt:  createdAt,
		}
		// Same placement rule as the report card: keep it beside the plan the user
		// confirmed instead of at the tail, where it drifts on every reload.
		messages = insertMessageAfterID(messages, job.PlanMessageID, card)
	}
	return messages
}

// appendCompletedEngineReportMessages adds the report card for every finished
// platform engine run in this session.
//
// # The gap this closes (2026-10-16)
//
// The BFF confirm fast path orchestrates the engine run itself, so MaClaw is not
// in that loop and nothing ever wrote a report message into the session.
// Everything else was already in place — the report is stored,
// `/api/v1/maclaw/evaluation/reports/:id/export` serves the PDF, the frontend has
// a report card with a download button, and this very handler already synthesizes
// messages (the running-job progress cards just below). The missing link was
// telling the browser WHICH report to show, so the PDF never appeared in the chat
// even though it existed and was downloadable through the API.
//
// # Placement: after its plan, not at the end
//
// This snapshot is rebuilt on every session load. An appended card would sink
// below every message sent after the evaluation and re-appear at the bottom each
// time the user came back. So it goes immediately after the plan_confirm card the
// user approved.
//
// # What does NOT survive a restart
//
// The job registry is in memory, and the recovery path
// (`PlatformEngineJobFromRecord`) does not restore `ReportID` either. After a
// backend restart the card stops being synthesized; the report itself is
// persisted and stays reachable through the report API. Making it durable needs a
// session→reports query and somewhere to keep the plan-message link — a migration
// this change deliberately does not add.
func appendCompletedEngineReportMessages(
	ctx context.Context,
	bridge *maclaw.RedteamToolBridge,
	userID uuid.UUID,
	messages []maclaw.RuntimeMessage,
	sessionID string,
	jobs []*maclaw.PlatformEngineJob,
) []maclaw.RuntimeMessage {
	if len(jobs) == 0 {
		return messages
	}
	// A report already present in the session — from this path on an earlier load,
	// or written by MaClaw on the agent-loop path — is not duplicated.
	existing := map[string]bool{}
	for _, msg := range messages {
		if msg.Metadata == nil {
			continue
		}
		if id := strings.TrimSpace(msg.Metadata["report_id"]); id != "" {
			existing[id] = true
		}
	}
	for _, job := range jobs {
		if job == nil || job.Status != maclaw.EvaluationJobStatusSucceeded {
			continue
		}
		reportID := strings.TrimSpace(job.ReportID)
		if reportID == "" || existing[reportID] {
			continue
		}
		existing[reportID] = true

		metadata := map[string]string{
			"card_type":             "report",
			"response_source":       "report",
			"evaluation_event_type": "report",
			"report_id":             reportID,
			"job_id":                job.ID,
			"assessment_id":         job.ID,
			"download_format":       "pdf",
			"downloadable":          "true",
			"status_text":           reportCardStatusText,
		}
		if job.PlannedCount > 0 {
			metadata["planned_count"] = strconv.Itoa(job.PlannedCount)
		}
		if job.ExecutedCount > 0 {
			metadata["executed_count"] = strconv.Itoa(job.ExecutedCount)
		}
		// The header fields come from the platform's own report store: the runtime
		// gateway cannot serve reports on MaClawSrv (`GetEvaluationReport` returns
		// `ErrUnsupported`), which is why `hydrateRuntimeReportMessages` is a no-op
		// on this deployment. Best effort — a card with a working download button
		// and a missing score beats no card at all.
		if bridge != nil {
			if report, err := bridge.GetRedteamReport(ctx, userID, reportID); err == nil {
				mergeEngineReportCardMetadata(metadata, report)
			}
		}
		messages = insertMessageAfterID(messages, job.PlanMessageID, maclaw.RuntimeMessage{
			ID:         "engine-report:" + reportID,
			SessionID:  sessionID,
			Role:       "assistant",
			OutputType: "application/vnd.maclaw.report+json",
			Content:    reportCardStatusText,
			Metadata:   metadata,
			CreatedAt:  engineReportCardCreatedAt(job),
		})
	}
	return messages
}

const reportCardStatusText = "评估任务已完成，可下载 PDF 报告。"

// mergeEngineReportCardMetadata copies the report's own fields into the card.
//
// Only the keys the report card reads are copied, and only when the report has
// them — an absent key renders as 「未记录」 rather than as a zero.
func mergeEngineReportCardMetadata(metadata map[string]string, report *maclaw.EvaluationReport) {
	if report == nil {
		return
	}
	if value := strings.TrimSpace(report.RiskLevel); value != "" {
		metadata["risk_level"] = value
	}
	if report.SafetyScore != nil {
		metadata["safety_score"] = strconv.FormatFloat(*report.SafetyScore, 'f', -1, 64)
	}
	for key, value := range report.Metadata {
		switch key {
		// The counts, so the card and the PDF cannot disagree: the report splits
		// 攻击失败 (measured and held) from 未取得回答, and the card renders the
		// same numbers.
		case "total_cases", "success_count", "failure_count", "unanswered_count",
			maclaw.RedteamJudgeTrackMetadataKey,
			maclaw.RedteamJudgeSourceMetadataKey,
			maclaw.RedteamGenerationPathMetadataKey,
			maclaw.RedteamNumTestsEffectiveMetadataKey:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				metadata[key] = trimmed
			}
		}
	}
}

func engineReportCardCreatedAt(job *maclaw.PlatformEngineJob) time.Time {
	if job.CompletedAt != nil {
		return *job.CompletedAt
	}
	if !job.StartedAt.IsZero() {
		return job.StartedAt
	}
	return time.Now().UTC()
}

// insertMessageAfterID places card directly after the message with anchorID,
// falling back to appending.
//
// The fallback matters: the report is the evaluation's output, and hiding it
// because a plan id went missing would turn a placement problem into a lost
// result.
func insertMessageAfterID(messages []maclaw.RuntimeMessage, anchorID string, card maclaw.RuntimeMessage) []maclaw.RuntimeMessage {
	anchorID = strings.TrimSpace(anchorID)
	if anchorID != "" {
		for i := range messages {
			if strings.TrimSpace(messages[i].ID) != anchorID {
				continue
			}
			out := make([]maclaw.RuntimeMessage, 0, len(messages)+1)
			out = append(out, messages[:i+1]...)
			out = append(out, card)
			out = append(out, messages[i+1:]...)
			return out
		}
	}
	return append(messages, card)
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

