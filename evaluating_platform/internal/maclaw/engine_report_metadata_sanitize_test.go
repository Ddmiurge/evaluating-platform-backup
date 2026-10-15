package maclaw

// engine_report_metadata_sanitize_test.go — P2-9 补批 · 平台报告元数据必须活过清洗。
//
// # 这个文件拦的是什么
//
// `redteam.SanitizeMetadata` 是安全边界，它的判定是**按键名子串的黑名单**
// （secret / token / payload / prompt / content / body / **path** …）。黑名单天然不知道
// 平台自己的词汇表，于是任何含这些子串的平台键都会被静默丢掉 —— 而它丢掉的是报告，
// 不是日志。
//
// 2026-10-14 服务器实测的形态：`generation_path` 含 "path" → 被剥掉；
// `num_tests_effective` 不含敏感子串 → 存活；`ValidateReportGenerationMetadata`
// 按设计判定「有计数无路径」并**拒绝写报告** → confirm 流产不出任何报告（P0）。
//
// 因此这里断言的不是「那一个键」，而是**整份平台报告元数据在清洗后逐键、逐值不变**。
// 下一个加入含敏感子串的键（例如 `evidence_path_hint`、`payload_source_uri`）会在
// 这里变红，而不是等到某天报告又编译不出来。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw/redteam"
)

// platformReportMetadataKeys 是平台自己写进报告 metadata 的键。
//
// 与写入点同源：引擎链路由 engineReportMetadata（下面用真实调用覆盖），
// 平台链路（batch / MCP compile）由 redteam_bridge.go 的字面量写。这里逐个列出并注明
// 用途，新增键时同步加一行。
var platformReportMetadataKeys = []string{
	// U4/U8 判定口径（两个正交维度）
	RedteamJudgeTrackMetadataKey,
	RedteamJudgeSourceMetadataKey,
	RedteamJudgeSourcesMetadataKey,
	"judge_source_fallback",
	// P2-6 生成路径与 P2-4 实际探针数
	RedteamGenerationPathMetadataKey,
	RedteamNumTestsEffectiveMetadataKey,
	// 报告模板与统计
	"schema_version",
	"report_template",
	"risk_level",
	"safety_score",
	"planned_count",
	"executed_count",
	"success_count",
	"failure_count",
	"total_cases",
	"attack_success_rate",
	"probes",
	"attack_success",
	"pass_rate",
	// 目标与摘要
	"target_summary",
	"target_model",
	"target_name",
	"summary",
	"purpose",
	// 引擎标识
	"engine",
	"engine_run_id",
	"engine_tool",
	"judge_mode",
}

// TestPlatformReportMetadataSurvivesSanitization 是 P0 的回归护栏。
//
// 反证：把 generation_path 从 isSafePayloadMetadataKey 的放行列表里删掉 → 本测试红，
// 且报出「key 被清洗掉」，正是 2026-10-14 的事故形态。
func TestPlatformReportMetadataSurvivesSanitization(t *testing.T) {
	t.Run("引擎链路：真实写入点的每个键都存活", func(t *testing.T) {
		// 用真实构造函数而不是手写 map —— 手写的 map 会在写入点变更时过期，
		// 而那正是「键被丢掉」这件事最容易被漏掉的地方。
		metadata := engineReportMetadata(&EngineRunRecord{
			ID:            "pferr_sanitize",
			Purpose:       "合规安全评测",
			JudgeMode:     EngineJudgePromptfooNative,
			PlannedCount:  12,
			ExecutedCount: 12,
		}, &EngineSafeResult{
			GenerationPath:    GenerationPathPlanC,
			NumTestsEffective: 12,
		})
		if len(metadata) == 0 {
			t.Fatal("engineReportMetadata 返回空 map，断言没有意义")
		}
		sanitized := redteam.SanitizeMetadata(metadata)
		for key, want := range metadata {
			got, ok := sanitized[key]
			if !ok {
				t.Errorf("键 %q 被 SanitizeMetadata 丢掉（这正是 P2-6 报告编译失败的原因）", key)
				continue
			}
			if got != want {
				t.Errorf("键 %q 的值被改写：期望 %q，实际 %q", key, want, got)
			}
		}
	})

	t.Run("平台链路：键表逐项存活", func(t *testing.T) {
		in := make(map[string]string, len(platformReportMetadataKeys))
		for i, key := range platformReportMetadataKeys {
			in[key] = "1"
			_ = i
		}
		sanitized := redteam.SanitizeMetadata(in)
		for _, key := range platformReportMetadataKeys {
			if _, ok := sanitized[key]; !ok {
				t.Errorf("平台报告元数据键 %q 被 SanitizeMetadata 丢掉", key)
			}
		}
	})

	t.Run("两个 P2 键的值形态是真的安全，不是靠放行", func(t *testing.T) {
		// 放行一个键的前提是它的值域封闭。这两条钉住那个前提：
		// generation_path 只接受固定枚举（写路径 fail-closed），
		// num_tests_effective 只接受正整数。
		if _, err := NormalizeRedteamGenerationPath("plan_b"); err == nil {
			t.Error("generation_path 接受了枚举外的值；放行它就不再安全")
		}
		if err := ValidateReportGenerationMetadata(map[string]string{
			RedteamGenerationPathMetadataKey:    GenerationPathPlanC,
			RedteamNumTestsEffectiveMetadataKey: "many",
		}); err == nil {
			t.Error("num_tests_effective 接受了非正整数；放行它就不再安全")
		}
	})
}

// TestSanitizerStillStripsRealPathKeys 是上一条的反向约束：修 P0 不能把 "path"
// 这个判断放开。真正的本地路径键必须继续被丢弃。
func TestSanitizerStillStripsRealPathKeys(t *testing.T) {
	in := map[string]string{
		"artifact_path":   "/home/node/.promptfoo/runs/run-1/redteam-config.yaml",
		"local_path":      "/da2/docker-files/evaluating-platform-backup",
		"target_secret":   "sk-should-not-survive",
		"raw_prompt_body": "should not survive",
		"response_body":   "should not survive",
		"generation_path": GenerationPathPlanC, // 唯一被显式放行的含 path 键
	}
	sanitized := redteam.SanitizeMetadata(in)

	for _, key := range []string{"artifact_path", "local_path", "target_secret", "raw_prompt_body", "response_body"} {
		if _, ok := sanitized[key]; ok {
			t.Errorf("键 %q 本应被清洗掉，却存活了（安全边界被放开）", key)
		}
	}
	if sanitized["generation_path"] != GenerationPathPlanC {
		t.Error("generation_path 应当存活且值不变")
	}
}

// TestGenerationMetadataKeysMatchSanitizerLiterals 钉住跨包的键名字面量。
//
// redteam 包不能 import maclaw（方向是反的），所以放行列表里写的是字面量。这个断言
// 是那条契约的机械版本：改任何一侧都会红。
func TestGenerationMetadataKeysMatchSanitizerLiterals(t *testing.T) {
	if RedteamGenerationPathMetadataKey != "generation_path" {
		t.Errorf("常量被改名（%q），redteam/payload_store.go 的字面量会失配", RedteamGenerationPathMetadataKey)
	}
	if RedteamNumTestsEffectiveMetadataKey != "num_tests_effective" {
		t.Errorf("常量被改名（%q），redteam/payload_store.go 的字面量会失配", RedteamNumTestsEffectiveMetadataKey)
	}
}

// TestCompileReportKeepsGenerationPath is the interaction-layer test the P2-6 batch
// was missing.
//
// P2-6's own tests covered the caption and the validator as pure functions, so they
// stayed green while `CompileReport` cleaned the metadata *before* validating it and
// dropped the very key under test. That is the gap this test closes: it goes through
// the real service, the real sanitizer and the real store, and asserts on what a
// reader would actually see.
//
// Counter-proof: remove the sanitizer allowance → the write is refused and this goes
// red with the "num_tests_effective is set but generation_path is not" message.
func TestCompileReportKeepsGenerationPath(t *testing.T) {
	newService := func() (*RedteamArtifactService, *memoryArtifactStore) {
		store := &memoryArtifactStore{}
		service := NewRedteamArtifactService(store)
		service.now = func() time.Time { return time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC) }
		return service, store
	}
	input := func(path string) CompileRedteamReportInput {
		score := 91.0
		return CompileRedteamReportInput{
			RunID:       "run_p26",
			Title:       "阶段二报告",
			Summary:     "摘要",
			SafetyScore: &score,
			Metadata: map[string]string{
				RedteamGenerationPathMetadataKey:    path,
				RedteamNumTestsEffectiveMetadataKey: "12",
			},
		}
	}

	t.Run("a plan_c report compiles and keeps the path", func(t *testing.T) {
		service, store := newService()
		userID := uuid.New()
		report, err := service.CompileReport(context.Background(), userID, "inst_p26", input(GenerationPathPlanC))
		if err != nil {
			t.Fatalf("带 generation_path 的报告被拒绝（这正是 confirm 流产不出报告的原因）：%v", err)
		}
		if got := report.Metadata[RedteamGenerationPathMetadataKey]; got != GenerationPathPlanC {
			t.Errorf("返回的报告里 generation_path = %q，期望 %q", got, GenerationPathPlanC)
		}
		if got := report.Metadata[RedteamNumTestsEffectiveMetadataKey]; got != "12" {
			t.Errorf("num_tests_effective = %q，期望 12", got)
		}
		if len(store.reports) != 1 {
			t.Fatalf("报告未落库：%d 行", len(store.reports))
		}
		for _, record := range store.reports {
			if got := record.Metadata[RedteamGenerationPathMetadataKey]; got != GenerationPathPlanC {
				t.Errorf("落库的 metadata 丢了 generation_path：%#v", record.Metadata)
			}
		}
	})

	t.Run("the read path renders plan_c, not 未记录", func(t *testing.T) {
		// 报告卡与 PDF 都从读路径的 metadata 取这句话。清洗把键丢掉时，caption
		// 永远显示「未记录」—— 报告仍是 plan_a / plan_c 不可比的旧世界。
		service, _ := newService()
		userID := uuid.New()
		compiled, err := service.CompileReport(context.Background(), userID, "inst_p26", input(GenerationPathPlanC))
		if err != nil {
			t.Fatalf("CompileReport: %v", err)
		}
		read, err := service.GetReport(context.Background(), userID, compiled.ID)
		if err != nil || read == nil {
			t.Fatalf("GetReport: %v", err)
		}
		caption := RedteamGenerationPathCaption(read.Metadata)
		if !strings.Contains(caption, GenerationPathPlanC) {
			t.Errorf("读回的 caption = %q，未标出 plan_c", caption)
		}
		if strings.Contains(caption, "未记录") {
			t.Errorf("读回的 caption 说「未记录」，说明 generation_path 在读路径被剥掉：%q", caption)
		}
	})

	t.Run("an uninterpretable path is refused, naming the enum", func(t *testing.T) {
		// 写保护分支必须真的可达（清洗把键丢掉时它虽然也拒绝，但理由变成
		// 「有计数无路径」—— 那是一条会掩盖真问题的错误信息）。
		service, store := newService()
		userID := uuid.New()
		_, err := service.CompileReport(context.Background(), userID, "inst_p26", input("plan_b"))
		if err == nil {
			t.Fatal("非法生成路径未被拒绝，报告被写入")
		}
		if !strings.Contains(err.Error(), GenerationPathPlanA) || !strings.Contains(err.Error(), GenerationPathPlanC) {
			t.Errorf("拒绝信息应说明合法取值，实际：%v", err)
		}
		for _, record := range store.reports {
			if _, ok := record.Metadata[RedteamGenerationPathMetadataKey]; ok {
				t.Errorf("非法生成路径落库了：%#v", record.Metadata)
			}
		}
	})

	t.Run("a hand-edited illegal value on read is called out, not shown as a path", func(t *testing.T) {
		// 读路径不报错（报告必须打得开），但也不许装作没事：数据被改过时要明说。
		caption := RedteamGenerationPathCaption(map[string]string{
			RedteamGenerationPathMetadataKey: "plan_b",
		})
		if !strings.Contains(caption, "非法") {
			t.Errorf("手工改坏的 generation_path 应显示「非法」，实际：%q", caption)
		}
	})
}
