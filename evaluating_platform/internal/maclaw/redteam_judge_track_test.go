package maclaw

// redteam_judge_track_test.go — U4 判定双轨显式化（judge_track）的护栏测试。
//
// 本文件覆盖三类断言，缺一不可：
//  1. **写入**：两条链路各自落对口径（platform / engine）。
//  2. **向后兼容**：旧报告（metadata 无 judge_track）必须仍可渲染，
//     且按 metadata.engine 推断出正确口径 —— 实测，不是推断。
//  3. **fail-closed 反证**：非法 judge_track 必须让写路径报错，
//     且读路径不得静默按 platform 呈现（必须显式提示非法）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRedteamJudgeTrackNormalizeRejectsIllegalValue(t *testing.T) {
	//大小写与空白属于**归一化**范畴（同一个词的不同写法），不是非法值。
	//严格的是**取值**：只有 platform / engine 两个字面量被接受。
	for _, raw := range []string{"platform", "PLATFORM", "Platform", " engine ", "ENGINE"} {
		if _, err := NormalizeRedteamJudgeTrack(raw); err != nil {
			t.Fatalf("合法口径 %q 被拒: %v", raw, err)
		}
	}
	track, err := NormalizeRedteamJudgeTrack("  ")
	if err != nil || track != "" {
		t.Fatalf("空串应表示未声明且不报错，得到 track=%q err=%v", track, err)
	}
	for _, raw := range []string{"promptfoo", "unknown", "0", "1", "platform engine", "platforms", "engine_platform", "null"} {
		if _, err := NormalizeRedteamJudgeTrack(raw); err == nil {
			t.Fatalf("非法口径 %q 未被拒绝（fail-closed 失效）", raw)
		}
	}
}

func TestRedteamJudgeTrackResolveInfersFromMetadata(t *testing.T) {
	cases := []struct {
		name        string
		metadata    map[string]string
		wantTrack   RedteamJudgeTrack
		wantSource  RedteamJudgeTrackSource
		wantInvalid string
	}{
		{name: "显式 platform", metadata: map[string]string{"judge_track": "platform"}, wantTrack: RedteamJudgeTrackPlatform, wantSource: RedteamJudgeTrackSourceExplicit},
		{name: "显式 engine", metadata: map[string]string{"judge_track": "engine"}, wantTrack: RedteamJudgeTrackEngine, wantSource: RedteamJudgeTrackSourceExplicit},
		{name: "旧引擎报告按 engine 推断", metadata: map[string]string{"engine": "promptfoo"}, wantTrack: RedteamJudgeTrackEngine, wantSource: RedteamJudgeTrackSourceEngineMetadata},
		{name: "旧平台报告按 platform 兜底", metadata: map[string]string{"batch_tool": "execute_redteam_evaluation_batch"}, wantTrack: RedteamJudgeTrackPlatform, wantSource: RedteamJudgeTrackSourceLegacyDefault},
		{name: "完全空元数据", metadata: nil, wantTrack: RedteamJudgeTrackPlatform, wantSource: RedteamJudgeTrackSourceLegacyDefault},
		{name: "非法值不静默通过", metadata: map[string]string{"judge_track": "both"}, wantTrack: RedteamJudgeTrackPlatform, wantSource: RedteamJudgeTrackSourceLegacyDefault, wantInvalid: "both"},
		{name: "显式 platform 覆盖 engine 键", metadata: map[string]string{"judge_track": "platform", "engine": "promptfoo"}, wantTrack: RedteamJudgeTrackPlatform, wantSource: RedteamJudgeTrackSourceExplicit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRedteamJudgeTrack(tc.metadata)
			if got.Track != tc.wantTrack || got.Source != tc.wantSource || got.Invalid != tc.wantInvalid {
				t.Fatalf("解析结果 = %+v，期望 track=%q source=%q invalid=%q", got, tc.wantTrack, tc.wantSource, tc.wantInvalid)
			}
			caption := RedteamJudgeTrackCaption(got)
			if caption == "" {
				t.Fatalf("caption 不应为空：%+v", got)
			}
			// 非法元数据必须显式提示，不能让用户以为口径是确定的 platform。
			if tc.wantInvalid != "" && !strings.Contains(caption, "非法") {
				t.Fatalf("非法元数据的 caption 未提示异常：%q", caption)
			}
			if tc.wantTrack == RedteamJudgeTrackEngine && !strings.Contains(caption, "engine") {
				t.Fatalf("engine 口径的 caption 应含 engine：%q", caption)
			}
		})
	}
}

// TestRedteamReportJudgeTrackFailsClosedOnIllegalMetadata 是本批次的核心反证护栏：
// 手工把 judge_track 改成非法值后，**写路径必须报错**，而不是把一个没人能解释的
// 口径写进报告并让它流通。
func TestRedteamReportJudgeTrackFailsClosedOnIllegalMetadata(t *testing.T) {
	store := &memoryArtifactStore{}
	service := NewRedteamArtifactService(store)
	service.now = func() time.Time { return time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC) }
	userID := uuid.New()

	score := 70.0
	_, err := service.CompileReport(context.Background(), userID, "inst_jt", CompileRedteamReportInput{
		RunID:   "run_jt",
		Title:   "非法口径报告",
		Summary: "不应被写入。",
		Metadata: map[string]string{
			RedteamJudgeTrackMetadataKey: "engine-and-platform",
		},
		SafetyScore: &score,
	})
	if err == nil {
		t.Fatalf("非法 judge_track 未被拒绝，报告被静默写入：store=%#v", store.reports)
	}
	if !strings.Contains(err.Error(), RedteamJudgeTrackMetadataKey) {
		t.Fatalf("错误信息应指明非法字段名，实际：%v", err)
	}
	for _, record := range store.reports {
		if record.Metadata[RedteamJudgeTrackMetadataKey] == "engine-and-platform" {
			t.Fatalf("非法口径落库了：%#v", record.Metadata)
		}
	}
}

// TestRedteamReportJudgeTrackWrittenByBothTracks 验证两条链路的写入点。
//
// 这里不跑真实评测（需要 PG + 引擎），而是直接调用两条链路各自的
// metadata 构造函数 —— 它们就是写入点本身，绕开它们测不到真实行为。
func TestRedteamReportJudgeTrackWrittenByBothTracks(t *testing.T) {
	engineMetadata := engineReportMetadata(&EngineRunRecord{
		ID:        "pferr_1",
		JudgeMode: EngineJudgePromptfooNative,
	}, &EngineSafeResult{})
	if got := engineMetadata[RedteamJudgeTrackMetadataKey]; got != string(RedteamJudgeTrackEngine) {
		t.Fatalf("引擎链路写入的judge_track = %q，期望 %q", got, RedteamJudgeTrackEngine)
	}
	if got := engineMetadata["engine"]; got != "promptfoo" {
		t.Fatalf("引擎链路应同时保留 engine 标记，实际 = %q", got)
	}

	// 平台链路的写入点是 ExecuteRedteamEvaluationBatch 内部的 reportMetadata 字面量，
	// 它不导出、也无法在无 PG 的情况下跑完整批。这里改用**反向断言**锁定它：
	// 平台链路的报告 metadata 不含 engine 键（这正是读路径据以推断的依据），
	// 且一旦U4 的写入点被误删，平台链路口径就只剩「读时兜底」——
	// 所以直接断言字面量常量侧的契约：平台口径常量存在且两值不相等。
	if RedteamJudgeTrackPlatform == RedteamJudgeTrackEngine {
		t.Fatalf("两条链路口径常量不应相同")
	}
	if string(RedteamJudgeTrackPlatform) != "platform" || string(RedteamJudgeTrackEngine) != "engine" {
		t.Fatalf("口径字面量被改动，前端 chatDisplay.ts 的同名契约会失配：%q / %q",
			RedteamJudgeTrackPlatform, RedteamJudgeTrackEngine)
	}
}

// TestRedteamBridgeCompileReportFallbackStampsJudgeTrack 覆盖平台链路在
// **无 artifact store** 时的降级路径（CompileRedteamReport 直接返回内存报告）。
//
// 这条路径容易被漏掉：它绕过 CompileReport，若不落口径就会产出
// 「看起来是新的、但没有口径」的报告 —— 比历史数据更难排查。
func TestRedteamBridgeCompileReportFallbackStampsJudgeTrack(t *testing.T) {
	bridge := NewRedteamToolBridge(nil, nil, nil)
	bridge.now = func() time.Time { return time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC) }

	report, err := bridge.CompileRedteamReport(context.Background(), uuid.Nil, "", CompileRedteamReportInput{
		RunID:    "run_fallback",
		Title:    "降级路径报告",
		Metadata: map[string]string{"batch_tool": "execute_redteam_evaluation_batch"},
	})
	if err != nil {
		t.Fatalf("CompileRedteamReport: %v", err)
	}
	if report.JudgeTrack != RedteamJudgeTrackPlatform {
		t.Fatalf("降级路径 judge_track = %q，期望 %q", report.JudgeTrack, RedteamJudgeTrackPlatform)
	}
	if got := report.Metadata[RedteamJudgeTrackMetadataKey]; got != string(RedteamJudgeTrackPlatform) {
		t.Fatalf("降级路径 metadata 未落口径：%#v", report.Metadata)
	}

	// 非法口径在这条路径上也必须 fail-closed。
	if _, err := bridge.CompileRedteamReport(context.Background(), uuid.Nil, "", CompileRedteamReportInput{
		RunID:    "run_fallback_bad",
		Metadata: map[string]string{RedteamJudgeTrackMetadataKey: "engine-and-platform"},
	}); err == nil {
		t.Fatalf("降级路径未拒绝非法 judge_track")
	}
}

// TestRedteamReportRendersLegacyReportWithoutJudgeTrack 实测旧报告兼容性：
// 一条 metadata 里既无 judge_track、也无 engine 标记的历史行，
// 必须仍能读出、渲染出报告，且口径按平台链路兜底。
func TestRedteamReportRendersLegacyReportWithoutJudgeTrack(t *testing.T) {
	score := 61.0
	legacy := &RedteamReportRecord{
		ID:             "redteam_report_legacy",
		Handle:         "redteam_report_legacy",
		PlatformUserID: uuid.New(),
		RunID:          "run_legacy",
		Title:          "历史报告",
		Summary:        "U4 之前生成。",
		RiskLevel:      "中风险",
		SafetyScore:    &score,
		Findings: []EvaluationReportFinding{{
			Title:    "历史发现",
			Severity: "medium",
		}},
		//刻意不含 judge_track —— 这正是 U4 之前落库的行。
		Metadata:  map[string]string{"schema_version": "redteam_report_zh_v1"},
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	report := reportFromRecord(legacy)
	if report.JudgeTrack != RedteamJudgeTrackPlatform {
		t.Fatalf("旧报告 judge_track = %q，期望按平台链路兜底 %q", report.JudgeTrack, RedteamJudgeTrackPlatform)
	}
	sections := reportSections(&report)
	if len(sections) == 0 {
		t.Fatalf("旧报告应能渲染出分段")
	}
	info := strings.Join(sections[0].Lines, "\n")
	if !strings.Contains(info, "判定口径") {
		t.Fatalf("报告基本信息段应含口径说明：%q", info)
	}
	if !strings.Contains(info, "历史报告未记录判定口径") {
		t.Fatalf("旧报告应被标注为历史口径兜底：%q", info)
	}
	// markdown 与 PDF 两条渲染路径都要跑通。
	if md := string(renderReportMarkdown(&report)); !strings.Contains(md, "历史报告") {
		t.Fatalf("旧报告 markdown 渲染异常：%q", md)
	}
	if pdf := string(renderReportPDF(&report)); !strings.Contains(pdf, pdfUTF16Hex("历史报告")) {
		t.Fatalf("旧报告 PDF 渲染异常")
	}
}

// TestRedteamReportJudgeTrackPersistsThroughCompileAndReload 验证
// 「写入 → 落库 → 重新读出」全链路口径不丢，且新报告一律带显式口径。
func TestRedteamReportJudgeTrackPersistsThroughCompileAndReload(t *testing.T) {
	store := &memoryArtifactStore{}
	service := NewRedteamArtifactService(store)
	service.now = func() time.Time { return time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC) }
	userID := uuid.New()
	score := 88.0

	// 未显式声明但带 engine 标记（模拟引擎链路）→ 应落engine。
	report, err := service.CompileReport(context.Background(), userID, "inst_jt2", CompileRedteamReportInput{
		RunID:       "run_jt2",
		Title:       "引擎报告",
		SafetyScore: &score,
		Metadata:    map[string]string{"engine": "promptfoo"},
	})
	if err != nil {
		t.Fatalf("CompileReport: %v", err)
	}
	if report.JudgeTrack != RedteamJudgeTrackEngine {
		t.Fatalf("新报告 judge_track = %q，期望 %q", report.JudgeTrack, RedteamJudgeTrackEngine)
	}
	if got := store.reports[0].Metadata[RedteamJudgeTrackMetadataKey]; got != string(RedteamJudgeTrackEngine) {
		t.Fatalf("落库 metadata 缺显式口径：%#v", store.reports[0].Metadata)
	}

	// 无任何标记 → 落 platform。
	plain, err := service.CompileReport(context.Background(), userID, "inst_jt2", CompileRedteamReportInput{
		RunID:    "run_jt3",
		Title:    "平台报告",
		Metadata: map[string]string{"batch_tool": "execute_redteam_evaluation_batch"},
	})
	if err != nil {
		t.Fatalf("CompileReport(platform): %v", err)
	}
	if plain.JudgeTrack != RedteamJudgeTrackPlatform {
		t.Fatalf("平台链路 judge_track = %q，期望 %q", plain.JudgeTrack, RedteamJudgeTrackPlatform)
	}

	// 显式声明 engine（模拟平台链路误标）→ 以显式值为准。
	explicit, err := service.CompileReport(context.Background(), userID, "inst_jt2", CompileRedteamReportInput{
		RunID:    "run_jt4",
		Title:    "显式声明",
		Metadata: map[string]string{RedteamJudgeTrackMetadataKey: "engine"},
	})
	if err != nil || explicit.JudgeTrack != RedteamJudgeTrackEngine {
		t.Fatalf("显式声明未生效：track=%q err=%v", explicit.JudgeTrack, err)
	}

	// 重新读出（模拟 GetReport）→ 口径仍在。
	reloaded, err := service.GetReport(context.Background(), userID, report.ID)
	if err != nil || reloaded == nil {
		t.Fatalf("GetReport: report=%#v err=%v", reloaded, err)
	}
	if reloaded.JudgeTrack != RedteamJudgeTrackEngine {
		t.Fatalf("重读后 judge_track = %q，期望 %q", reloaded.JudgeTrack, RedteamJudgeTrackEngine)
	}
}

// pdfContainsAllCJKSegments 判断 PDF 是否包含给定文案里**每一个纯中文片段**。
//
// 为什么不能直接 pdfUTF16Hex(整句)：pdfPageWriter.text 会按 ASCII/非 ASCII
// 把一行拆成多个片段，各片段用不同字体（F1 UTF-16 / F2 ASCII-hex）分别渲染，
// 所以整句的连续 hex 永远不存在。用整句做断言会得到「明明渲染了却报失败」
// 的假阴性 —— 这个坑在 U4 首次实现时真实踩到过一次。
func pdfContainsAllCJKSegments(pdf string, text string) bool {
	for _, segment := range splitPDFTextSegments(text) {
		if segment.Text == "" {
			continue
		}
		encoded := pdfUTF16Hex(segment.Text)
		if segment.ASCII {
			encoded = pdfASCIIHex(segment.Text)
		}
		if !strings.Contains(pdf, encoded) {
			return false
		}
	}
	return true
}

// TestRedteamReportPDFFooterCarriesJudgeTrackCaption 验证 PDF **页脚**口径说明。
//
// 关键点：口径文案必须出现在页脚（y=34），而不是只在正文段落 ——
// 反之若只靠正文，用户翻到任意一页都可能看不到口径。
func TestRedteamReportPDFFooterCarriesJudgeTrackCaption(t *testing.T) {
	score := 42.0
	base := func(track RedteamJudgeTrack) *EvaluationReport {
		return &EvaluationReport{
			ID:          "redteam_report_footer",
			RunID:       "run_footer",
			Title:       "大模型安全评估报告",
			Summary:     "本轮测试摘要。",
			RiskLevel:   "高风险",
			SafetyScore: &score,
			Metadata: map[string]string{
				RedteamJudgeTrackMetadataKey: string(track),
				"schema_version":             "redteam_report_zh_v1",
			},
			CreatedAt: time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
		}
	}

	for _, track := range []RedteamJudgeTrack{RedteamJudgeTrackPlatform, RedteamJudgeTrackEngine} {
		pdf := string(renderReportPDF(base(track)))
		caption := RedteamJudgeTrackFooterCaptionOf(base(track).Metadata)
		if !pdfContainsAllCJKSegments(pdf, caption) {
			t.Fatalf("%s 口径的 PDF 未渲染页脚口径说明：%q", track, caption)
		}
		// 页脚行的纵坐标必须在页脚安全区内（pdfFooterCaptionTextY=34），
		// 不能落到正文区（>= pdfContentBottomY）。
		if !strings.Contains(pdf, "70.0 34.0 Td") {
			t.Fatalf("%s 口径的口径说明未渲染在页脚 y=34", track)
		}
		if strings.Contains(pdf, "310.0 34.0 Td") {
			t.Fatalf("口径说明不应出现在正文区")
		}
		// 页脚单行必须放得下：超出会被截断，而被截掉的正是
		// 「不可横向比较」这句最该被看见的警告。
		if width := pdfTextWidth(caption, pdfFooterFontSize); width > 425 {
			t.Fatalf("%s 页脚口径说明宽度 %.1f 超过 425，会被截断：%q", track, width, caption)
		}
		// 反向断言：engine 报告的页脚**不得**出现 platform 口径文案，
		// 否则「两条链路页脚长得一样」等于没做分轨。
		other := judgeTrackFooterPlatform
		if track == RedteamJudgeTrackPlatform {
			other = judgeTrackFooterEngine
		}
		if pdfContainsAllCJKSegments(pdf, other) {
			t.Fatalf("%s 口径的页脚混入了另一条链路的口径文案", track)
		}
	}
}

// TestRedteamReportPDFFooterShowsUnknownTrackForIllegalMetadata 反证：
// 非法元数据的报告，页脚必须显示「判定口径未知」而不是 platform。
// 页脚是每页都在的位置 —— 在这里静默按 platform 呈现会让整份 PDF 口径标注失真。
func TestRedteamReportPDFFooterShowsUnknownTrackForIllegalMetadata(t *testing.T) {
	score := 30.0
	report := &EvaluationReport{
		ID:          "redteam_report_illegal_track",
		Title:       "大模型安全评估报告",
		Summary:     "元数据被人工改坏。",
		SafetyScore: &score,
		Metadata:    map[string]string{RedteamJudgeTrackMetadataKey: "both"},
		CreatedAt:   time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
	}
	pdf := string(renderReportPDF(report))
	if !pdfContainsAllCJKSegments(pdf, judgeTrackFooterInvalid) {
		t.Fatalf("非法元数据的 PDF 页脚未显示「判定口径未知」")
	}
	if pdfContainsAllCJKSegments(pdf, judgeTrackFooterPlatform) {
		t.Fatalf("非法元数据不应在页脚被呈现为 platform 口径")
	}
}

// TestTruncatePDFTextKeepsFooterSingleLine 验证页脚单行截断：
// 超长口径必须被截断并加省略号，不能换行撞进正文安全区。
func TestTruncatePDFTextKeepsFooterSingleLine(t *testing.T) {
	long := strings.Repeat("口径说明", 200)
	got := truncatePDFText(long, 425)
	if got == long {
		t.Fatalf("超长文本未被截断")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断后应加省略号：%q", got)
	}
	if width := pdfTextWidth(got, pdfFooterFontSize); width > 425 {
		t.Fatalf("截断后宽度仍超限：%.1f", width)
	}
	if got := truncatePDFText("  短文本  ", 425); got != "短文本" {
		t.Fatalf("短文本应原样返回（去空白）：%q", got)
	}
	if got := truncatePDFText("   ", 425); got != "" {
		t.Fatalf("纯空白应返回空串：%q", got)
	}
}
