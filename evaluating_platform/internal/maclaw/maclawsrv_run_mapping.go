package maclaw

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// errRuntimeMessageBodyCorrupt 表示「这条 runtime 消息的 Content 声称自己是
// JSON，但解析失败」。
//
// 为什么不复用 internal/repository 的 CorruptRowError：那一套的定位信息是
// 「表 + 行 + 列」，适用于 DB 读路径。这里是 maclaw 服务推给我们的 SSE 消息，
// 没有表也没有列，强行套用只会造出一个填不满的结构体。此处按 CRITICAL 的
// 最小形态处理：向上返回 error，由调用方决定拒绝对条消息。
var errRuntimeMessageBodyCorrupt = errors.New("maclaw runtime message content is not valid json")

// looksLikeJSONObject 粗判「这条 Content 是否自称 JSON」。
//
// 存在的理由（很重要）：ask_user 兜底路径（normalizeMaclawSrvMessage）会把
// Content 置为一段**中文提示语**，那是合法的非 JSON 内容。如果对任何解析失败
// 都 fail closed，ask_user 会被误判成数据损坏 —— 这是把修复做成了回归。
//
// 所以只有「自称 JSON 却解析失败」才算损坏：
//   - Content 为空            → 缺席，不是损坏（与 json_columns.go 的
//     「Absent vs corrupt」约定一致）
//   - Content 是自然语言散文   → ask_user 兜底，合法
//   - Content 以 { 或 [ 开头  → 自称 JSON，解析失败即为损坏
func looksLikeJSONObject(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

// evaluationJobFromRuntimeMessage 把一条 assistant 消息转成 EvaluationJob。
//
// CRITICAL 判据（对应 audit 2026-10-12 的分级原则）：
// 解析失败时 `body` 会保持为空，而下游 status 的兜底值是 "completed"
// （见下方 firstNonEmptyString(..., "completed")）。也就是说：一条内容损坏的
// 消息，会被**凭空捏造成一个"已完成"的评测任务**。在红队评测平台上这正是
// 那条最不能错的断言 —— 把"没跑成"渲染成"跑完了"。
// 因此这里 fail closed，让调用方拿不到这个假阳性。
//
// 注意与 latestCompletedRuntimeMessageJob 的区别：那里在调用本函数**之前**
// 已经成功解析过同一段 JSON（见其内的 Unmarshal 错误分支 continue），所以它
// 拿到 error 只应 skip；而这里是对外入口，需要把错误如实上抛。
func evaluationJobFromRuntimeMessage(msg *RuntimeMessage, sessionID string) (*EvaluationJob, error) {
	if msg == nil {
		return nil, nil
	}
	rawContent := strings.TrimSpace(msg.Content)
	body := map[string]any{}
	if rawContent != "" {
		if err := json.Unmarshal([]byte(rawContent), &body); err != nil {
			if looksLikeJSONObject(rawContent) {
				return nil, fmt.Errorf("%w: session=%s message=%s: %v",
					errRuntimeMessageBodyCorrupt, sessionID, msg.ID, err)
			}
			// 非 JSON 的自然语言内容（ask_user 兜底）：不是损坏，按缺席处理。
			body = map[string]any{}
		}
	}
	metadata := map[string]string{}
	for _, key := range []string{"response_source", "run_id", "report_id", "target_model", "risk_type", "test_count"} {
		if value := firstNonEmptyString(jsonMapString(body, key), msg.Metadata[key]); value != "" {
			metadata[key] = value
		}
	}
	runID := firstNonEmptyString(metadata["run_id"], msg.ID)
	status := firstNonEmptyString(jsonMapString(body, "status"), msg.Metadata["status"], "completed")
	run := &RuntimeRun{
		ID:                 runID,
		SessionID:          firstNonEmptyString(msg.SessionID, sessionID),
		AssistantMessageID: msg.ID,
		Status:             status,
		ResponseSource:     firstNonEmptyString(metadata["response_source"], msg.Metadata["evaluation_event_type"], status),
		Metadata:           metadata,
	}
	return evaluationJobFromRuntimeRun(run), nil
}

func jsonMapString(body map[string]any, key string) string {
	if len(body) == 0 {
		return ""
	}
	value, _ := body[key].(string)
	return strings.TrimSpace(value)
}

func evaluationJobFromRuntimeRun(run *RuntimeRun) *EvaluationJob {
	if run == nil {
		return nil
	}
	stageDurations := ""
	plannedCount := 0
	executedCount := 0
	currentStage := ""
	if run.Metadata != nil {
		stageDurations = run.Metadata["stage_durations_json"]
		plannedCount = positiveIntFromMetadata(run.Metadata, "planned_count", "total_count", "test_count")
		executedCount = nonNegativeIntFromMetadata(run.Metadata, "executed_count", "completed_count", "current_count")
		currentStage = firstNonEmptyString(run.Metadata["current_stage"], run.Metadata["phase"], run.Metadata["progress_phase"])
	}
	job := &EvaluationJob{
		ID:     run.ID,
		Kind:   EvaluationJobKindRun,
		Status: evaluationJobStatusFromRuntimeRun(run.Status),
		Progress: &EvaluationJobProgress{
			RunID:              run.ID,
			InstanceID:         run.Metadata["instance_id"],
			SessionID:          run.SessionID,
			UserMessageID:      run.UserMessageID,
			AssistantMessageID: run.AssistantMessageID,
			Phase:              firstNonEmptyString(run.ResponseSource, run.Status),
			StatusText:         firstNonEmptyString(run.Metadata["status_text"], runtimeRunStatusText(run)),
			PlannedCount:       plannedCount,
			ExecutedCount:      executedCount,
			CurrentStage:       currentStage,
			DurationMs:         run.DurationMs,
			StageDurationsJSON: stageDurations,
		},
		Error:       run.Error,
		CompletedAt: run.CompletedAt,
	}
	if !run.StartedAt.IsZero() {
		startedAt := run.StartedAt
		job.StartedAt = &startedAt
	}
	if job.Status == EvaluationJobStatusSucceeded {
		job.Result = &EvaluationRunResult{Run: run}
	}
	return job
}

func positiveIntFromMetadata(metadata map[string]string, keys ...string) int {
	value := nonNegativeIntFromMetadata(metadata, keys...)
	if value <= 0 {
		return 0
	}
	return value
}

func nonNegativeIntFromMetadata(metadata map[string]string, keys ...string) int {
	if len(metadata) == 0 {
		return 0
	}
	for _, key := range keys {
		raw := strings.TrimSpace(metadata[key])
		if raw == "" {
			continue
		}
		parsed, err := strconv.Atoi(raw)
		if err == nil && parsed >= 0 {
			return parsed
		}
	}
	return 0
}

func annotateRuntimeRunDuration(run *RuntimeRun, stage string, durationMs int64) {
	if run == nil {
		return
	}
	if durationMs <= 0 {
		durationMs = 1
	}
	if run.DurationMs <= 0 {
		run.DurationMs = durationMs
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return
	}
	if run.Metadata == nil {
		run.Metadata = map[string]string{}
	}
	if strings.TrimSpace(run.Metadata["stage_durations_json"]) == "" {
		run.Metadata["stage_durations_json"] = fmt.Sprintf(`{"%s":%d}`, stage, durationMs)
	}
}

func durationMillisSince(started time.Time) int64 {
	if started.IsZero() {
		return 0
	}
	ms := time.Since(started).Milliseconds()
	if ms <= 0 {
		return 1
	}
	return ms
}

func runtimeRunStatusText(run *RuntimeRun) string {
	if run == nil {
		return ""
	}
	switch evaluationJobStatusFromRuntimeRun(run.Status) {
	case EvaluationJobStatusRunning:
		return "评估任务正在执行"
	case EvaluationJobStatusSucceeded:
		return "评估任务已完成"
	case EvaluationJobStatusFailed:
		return "评估任务执行失败"
	case EvaluationJobStatusCanceled:
		return "评估任务已取消"
	default:
		return ""
	}
}

func evaluationJobStatusFromRuntimeRun(status string) EvaluationJobStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		return EvaluationJobStatusRunning
	case "succeeded", "success", "completed":
		return EvaluationJobStatusSucceeded
	case "failed", "error":
		return EvaluationJobStatusFailed
	case "canceled", "cancelled":
		return EvaluationJobStatusCanceled
	default:
		return EvaluationJobStatusPending
	}
}

func maclawSrvRunStatusFromEvaluationJobStatus(status EvaluationJobStatus) string {
	switch status {
	case EvaluationJobStatusCanceled:
		return "cancelled"
	default:
		return string(status)
	}
}
