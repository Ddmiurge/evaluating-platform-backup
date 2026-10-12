package maclaw

// 这批测试锁的是 audit 2026-10-12 修复后仍残留的三处 errcheck 漏网
// （maclawsrv_client.go / redteam_llm_judge.go）。
//
// 为什么要单独写测试而不是靠 lint 盯着：
// 修复的实质不是「多判了一个 err」，而是**改变了失败语义** ——
// 从「静默捏造出一个看起来正常的记录」变成「明确报错」。
// 这个语义如果被后人「顺手简化」回去（比如把error 改成 continue、或者把
// status 的兜底值去掉），lint 不会报警，但bug 会原样复活。所以这里用测试
// 把语义钉死。

import (
	"errors"
	"strings"
	"testing"
)

// ── maclawsrv_client：损坏的 JSON 消息体必须 fail closed ──────────────────

// 判据：Content 自称 JSON（有 { 前缀）却解析失败时，必须返回 error，
// 绝不能返回 status="completed" 的任务。
//
// 这是本批最关键的一条断言：旧代码里 status 的兜底值就是 "completed"
// （firstNonEmptyString(..., "completed")），所以解析失败会凭空产出一个
// 「评测已完成」。在红队平台上，「没跑成」被渲染成「跑完了」是最坏的腐败。
func TestEvaluationJobFromRuntimeMessageFailsClosedOnCorruptJSON(t *testing.T) {
	job, err := evaluationJobFromRuntimeMessage(&RuntimeMessage{
		ID:      "msg-1",
		Content: `{"status": "completed", "run_id": "run-1"`, // 少了收尾的 }
	}, "sess-1")

	if err == nil {
		t.Fatalf("expected fail-closed error for corrupt JSON body, got job=%#v", job)
	}
	if !errors.Is(err, errRuntimeMessageBodyCorrupt) {
		t.Fatalf("error should wrap errRuntimeMessageBodyCorrupt, got %v", err)
	}
	if job != nil {
		t.Fatalf("fail-closed must not also return a job: %#v", job)
	}
	// 错误信息必须能定位到具体是哪条消息，否则线上无法排查。
	if !strings.Contains(err.Error(), "sess-1") || !strings.Contains(err.Error(), "msg-1") {
		t.Fatalf("error must carry session/message identity for triage, got %q", err.Error())
	}
}

// 反向纪律：ask_user 兜底路径的 Content 是**中文散文**，不是 JSON。
// normalizeMaclawSrvMessage 会把 hard_exit 的空回复替换成一段提示语。
// 如果这里也 fail closed，ask_user 就会被误判成数据损坏 —— 那是把修复做成了
// 回归。这条测试就是为了钉住「不要过度 fail closed」。
func TestEvaluationJobFromRuntimeMessageAcceptsPlainTextFallback(t *testing.T) {
	job, err := evaluationJobFromRuntimeMessage(&RuntimeMessage{
		ID:      "msg-2",
		Role:    "assistant",
		Content: "本轮 MaClaw 没有生成可展示的有效回复。请换个说法或稍后重试。",
		Metadata: map[string]string{
			"response_source":       "ask_user",
			"evaluation_event_type": "ask_user",
		},
	}, "sess-2")

	if err != nil {
		t.Fatalf("plain-text ask_user fallback must not be treated as corruption: %v", err)
	}
	if job == nil {
		t.Fatal("expected a job for plain-text fallback")
	}
	// response_source 会被 evaluationJobFromRuntimeRun 映射成 Progress.Phase
	// （firstNonEmptyString(run.ResponseSource, run.Status)）。
	if job.Progress == nil || job.Progress.Phase != "ask_user" {
		t.Fatalf("progress.phase = %#v, want ask_user", job.Progress)
	}
}

// 空 Content 是「缺席」，不是损坏 —— 与 json_columns.go 的
// 「Absent vs corrupt」约定保持一致。
func TestEvaluationJobFromRuntimeMessageEmptyContentIsAbsentNotCorrupt(t *testing.T) {
	job, err := evaluationJobFromRuntimeMessage(&RuntimeMessage{ID: "msg-3"}, "sess-3")
	if err != nil {
		t.Fatalf("empty content must be treated as absent, got %v", err)
	}
	if job == nil {
		t.Fatal("expected a job built from message id alone")
	}
	if job.ID != "msg-3" {
		t.Fatalf("job.ID = %q, want fallback to msg.ID", job.ID)
	}
}

// 正常路径不能被修复破坏：合法 JSON 仍要解析出 status / run_id。
func TestEvaluationJobFromRuntimeMessageParsesValidJSON(t *testing.T) {
	job, err := evaluationJobFromRuntimeMessage(&RuntimeMessage{
		ID:      "msg-4",
		Content: `{"status":"running","run_id":"run-42","target_model":"gpt-x"}`,
	}, "sess-4")

	if err != nil {
		t.Fatalf("valid JSON must not error: %v", err)
	}
	if job == nil {
		t.Fatal("expected a job")
	}
	if job.ID != "run-42" {
		t.Fatalf("job.ID = %q, want run_id from body", job.ID)
	}
	if job.Status != EvaluationJobStatus("running") {
		t.Fatalf("job.Status = %q, want running", job.Status)
	}
	// target_model 会进 run.Metadata，再由 evaluationJobFromRuntimeRun 汇入 Progress。
	if job.Progress == nil {
		t.Fatal("expected progress to be populated")
	}
}

func TestLooksLikeJSONObject(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"whitespace only", "   \n\t ", false},
		{"chinese prose", "本轮 MaClaw 没有生成可展示的有效回复。", false},
		{"object", `{"a":1}`, true},
		{"padded object", "  {\"a\":1}  ", true},
		{"array", `[1,2]`, true},
		{"truncated object", `{"status":"completed"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeJSONObject(tc.content); got != tc.want {
				t.Fatalf("looksLikeJSONObject(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// ── redteam_llm_judge：提示词编码失败绝不发空串 ──────────────────────────

// marshalJudgePrompt 是两个 build*Prompt 的共同出口。断言它对可编码 payload
// 产出非空提示词，并且错误可被 errors.Is 识别。
func TestMarshalJudgePromptProducesNonEmptyPrompt(t *testing.T) {
	prompt, err := marshalJudgePrompt(map[string]any{
		"task":    "judge",
		"success": []string{"success", "failure"},
	})
	if err != nil {
		t.Fatalf("marshalJudgePrompt: %v", err)
	}
	if strings.TrimSpace(prompt) == "" {
		t.Fatal("prompt must not be empty for a valid payload")
	}
	if !strings.Contains(prompt, `"task":"judge"`) {
		t.Fatalf("prompt should contain encoded payload, got %q", prompt)
	}
}

// 两个 build*Prompt 在正常输入下必须成功，且内容非空 —— 防止有人为了「简化」
// 把错误吞掉后返回空串，而这正是本次修复要消灭的行为。
func TestBuildJudgePromptsReturnNonEmptyOnValidInput(t *testing.T) {
	in := JudgeAttackResultInput{
		AttackType:      "jailbreak",
		OriginalPrompt:  "请绕过限制。",
		TargetResponse:  "模型给出了内容。",
		ResponseSummary: "疑似越权。",
	}
	rule := JudgeAttackResultOutput{Result: "failure", Severity: "medium", Confidence: 0.5, Reason: "不确定。"}

	single, err := buildLLMAttackJudgePrompt(in, rule)
	if err != nil {
		t.Fatalf("buildLLMAttackJudgePrompt: %v", err)
	}
	if strings.TrimSpace(single) == "" {
		t.Fatal("single judge prompt must not be empty")
	}

	batch, err := buildLLMAttackJudgeBatchPrompt([]JudgeAttackResultInput{in}, []JudgeAttackResultOutput{rule})
	if err != nil {
		t.Fatalf("buildLLMAttackJudgeBatchPrompt: %v", err)
	}
	if strings.TrimSpace(batch) == "" {
		t.Fatal("batch judge prompt must not be empty")
	}
	if !strings.Contains(batch, `"cases"`) {
		t.Fatalf("batch prompt should carry cases, got %q", batch)
	}
}
