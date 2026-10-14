package maclaw

// redteam_judge_source_test.go — U8 判定标准溯源层的护栏测试。
//
// 覆盖：
//   - 分布汇总与直方图的**往返一致**（写得进就读得回）；
//   - 非法档位标识 fail-closed（这是本批最核心的反证之一）；
//   - 报告写路径对非法 judge_source **拒绝写入**（反证之二）；
//   - 页脚在存在兜底探测时显示降级警告，且警告文案放得下（不被截断）；
//   - 历史报告（无 judge_source）仍可渲染，且**不冒充**某个标准。

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSummarizeEngineJudgeSourcesCountsEachRubric(t *testing.T) {
	summary := SummarizeEngineJudgeSources(&EngineSafeResult{
		JudgeSources: []EngineJudgeSourceCount{
			{Source: "category:injection", Probes: 6},
			{Source: "category:privacy", Probes: 3},
			{Source: JudgeSourceFallbackUnmapped, Probes: 2, IsFallback: true},
		},
	})
	if summary.Distinct != 3 {
		t.Fatalf("档位数 = %d，期望 3", summary.Distinct)
	}
	if summary.Sources["category:injection"] != 6 {
		t.Errorf("injection 档探测数 = %d，期望 6", summary.Sources["category:injection"])
	}
	if !summary.HasFallback() || summary.FallbackProbes != 2 {
		t.Errorf("兜底探测数 = %d，HasFallback = %v；期望 2/true", summary.FallbackProbes, summary.HasFallback())
	}
}

func TestSummarizeEngineJudgeSourcesNilResultIsUnrecordedNotZero(t *testing.T) {
	// 「未记录」与「全部按族判、无人落兜底」是两种不同事实，不能互相顶替：
	// 前者说明这份结果产生于分档判定上线之前。
	summary := SummarizeEngineJudgeSources(nil)
	if summary.Distinct != 0 || summary.HasFallback() {
		t.Fatalf("nil 结果应得零值分布，实际 %+v", summary)
	}
	empty := SummarizeEngineJudgeSources(&EngineSafeResult{})
	if empty.Distinct != 0 || empty.HasFallback() {
		t.Fatalf("无 judge_sources 字段（U8 之前的引擎）应得零值分布，实际 %+v", empty)
	}
}

func TestJudgeSourceHistogramRoundTrips(t *testing.T) {
	summary := JudgeSourceSummary{
		Sources:        map[string]int{"category:harmful": 5, "category:injection": 2, JudgeSourceFallbackUnmapped: 1},
		FallbackProbes: 1,
		Distinct:       3,
	}
	encoded := formatJudgeSourceHistogram(summary)
	// 排序必须稳定，否则同一个 run 反复渲染会得到不同的 metadata。
	if encoded != "category:harmful=5,category:injection=2,fallback:unmapped=1" {
		t.Fatalf("直方图输出未按字典序稳定排序：%q", encoded)
	}
	parsed, err := ParseJudgeSourceHistogram(encoded)
	if err != nil {
		t.Fatalf("自己写下的直方图读不回来：%v", err)
	}
	if parsed["category:harmful"] != 5 || parsed[JudgeSourceFallbackUnmapped] != 1 {
		t.Fatalf("往返后数值不一致：%v", parsed)
	}
}

func TestParseJudgeSourceHistogramRejectsMalformed(t *testing.T) {
	// 读回的是**自己**写下的 metadata，格式非法说明写入方有 bug，
	// 此时返回错误比尽力猜更好。
	for _, raw := range []string{"category:harmful", "=5", "category:harmful=x", "category:harmful=-1"} {
		if _, err := ParseJudgeSourceHistogram(raw); err == nil {
			t.Errorf("非法直方图 %q 未被拒绝", raw)
		}
	}
	if got, err := ParseJudgeSourceHistogram("  "); err != nil || len(got) != 0 {
		t.Errorf("空串应得空分布且无错误，实际 %v / %v", got, err)
	}
}

func TestNormalizeJudgeSourceAcceptsKnownTiers(t *testing.T) {
	for _, raw := range []string{
		JudgeSourceFallbackUnmapped,
		"category:harmful",
		"category:industry",
		// P2-1：此处原为 "plugin:prompt-injection"。该 id 已作为非法 plugin id
		// 移出目录（见 retiredPluginCatalog），于是它从「合法档位」变成了
		// 「目录里没有的插件」——继续拿它当正例，测的就不再是「已知档位被接受」
		// 而是「已下线的 id 仍被接受」，与本测试的目的相反。改用 pii：
		// 它在目录里、且是**带专属覆盖**的插件，正好覆盖 plugin:<id> 这一档。
		"plugin:pii",
	} {
		if _, err := NormalizeJudgeSource(raw); err != nil {
			t.Errorf("合法档位 %q 被拒：%v", raw, err)
		}
	}
	if got, err := NormalizeJudgeSource("   "); err != nil || got != "" {
		t.Errorf("空串应表示「未记录」且无错误，实际 %q / %v", got, err)
	}
}

// TestNormalizeJudgeSourceRejectsIllegalTiers 是 U8 的核心反证之一：
// **非法分组**必须在写路径 fail-closed。
//
// 逐条构造「看起来像档位、实际不合法」的输入：未知前缀、未知族名、
// 不在目录里的插件 id、缺值。它们都代表「写报告的一侧算错了标准」，
// 静默接受等于把一个没人能解释的口径写进报告并让它流通。
func TestNormalizeJudgeSourceRejectsIllegalTiers(t *testing.T) {
	for _, raw := range []string{
		"category:不存在的族",
		"plugin:不存在的插件",
		"unknown:harmful",
		"harmful",
		"category:",
		"plugin:",
		"category:harmful:extra",
		"fallback:其它",
	} {
		if got, err := NormalizeJudgeSource(raw); err == nil {
			t.Errorf("非法档位 %q 未被拒绝（返回 %q）", raw, got)
		}
	}
}

// TestReportWriteFailsClosedOnIllegalJudgeSource 是 U8 的核心反证之二：
// 手工把 judge_source 改成非法值后，**写路径必须报错**，
// 而不是把一个没人能解释的判定标准写进报告。
func TestReportWriteFailsClosedOnIllegalJudgeSource(t *testing.T) {
	store := &memoryArtifactStore{}
	service := NewRedteamArtifactService(store)
	service.now = func() time.Time { return time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC) }
	userID := uuid.New()
	score := 70.0
	_, err := service.CompileReport(context.Background(), userID, "inst_js", CompileRedteamReportInput{
		RunID:   "run_js",
		Title:   "非法判定标准报告",
		Summary: "不应被写入。",
		Metadata: map[string]string{
			RedteamJudgeTrackMetadataKey:  string(RedteamJudgeTrackEngine),
			RedteamJudgeSourceMetadataKey: "category:不存在的族",
		},
		SafetyScore: &score,
	})
	if err == nil {
		t.Fatal("非法 judge_source 未被拒绝，报告被静默写入")
	}
	if !strings.Contains(err.Error(), RedteamJudgeSourceMetadataKey) {
		t.Fatalf("错误信息未点名 %s：%v", RedteamJudgeSourceMetadataKey, err)
	}
	for _, record := range store.reports {
		if record.Metadata[RedteamJudgeSourceMetadataKey] == "category:不存在的族" {
			t.Fatalf("非法判定标准落库了：%#v", record.Metadata)
		}
	}
}

// TestReportWriteAcceptsLegalJudgeSource 并列的正向断言：合法档位必须写得进。
func TestReportWriteAcceptsLegalJudgeSource(t *testing.T) {
	store := &memoryArtifactStore{}
	service := NewRedteamArtifactService(store)
	service.now = func() time.Time { return time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC) }
	userID := uuid.New()
	score := 70.0
	written := 0
	for i, source := range []string{"category:privacy", "plugin:pii", JudgeSourceFallbackUnmapped} {
		// 报告 handle 由内容派生，三次必须用不同 RunID，否则会互相覆盖，
		// 断言「写了 3 份」就会因为 store 的去重而假失败。
		runID := "run_js_ok_" + strconv.Itoa(i)
		if _, err := service.CompileReport(context.Background(), userID, "inst_js_ok", CompileRedteamReportInput{
			RunID:   runID,
			Title:   "合法判定标准报告",
			Summary: "应当被写入。",
			Metadata: map[string]string{
				RedteamJudgeTrackMetadataKey:  string(RedteamJudgeTrackEngine),
				RedteamJudgeSourceMetadataKey: source,
			},
			SafetyScore: &score,
		}); err != nil {
			t.Errorf("合法档位 %q 写报告被拒：%v", source, err)
		}
		written++
	}
	if len(store.reports) != written {
		t.Fatalf("落库报告数 = %d，期望 %d", len(store.reports), written)
	}
}

func TestJudgeSourceCaptionDistinguishesTiers(t *testing.T) {
	metadata := map[string]string{
		RedteamJudgeTrackMetadataKey:  string(RedteamJudgeTrackEngine),
		RedteamJudgeSourceMetadataKey: "category:injection",
	}
	// 未记录：明确说未记录，**不替它选一个标准**。
	if got := RedteamJudgeSourceCaption(nil, JudgeSourceSummary{}); !strings.Contains(got, "未记录") {
		t.Errorf("历史报告的判定标准说明应说明未记录，实际 %q", got)
	}
	// 单档：直接给出该档。
	single := RedteamJudgeSourceCaption(metadata, JudgeSourceSummary{
		Sources: map[string]int{"category:injection": 4}, Distinct: 1,
	})
	if !strings.Contains(single, "category:injection") {
		t.Errorf("单档说明未给出档位：%q", single)
	}
	// 多档：说明「同一报告内尺度不同」——这是 U8 生效的标志，不是异常。
	multi := RedteamJudgeSourceCaption(metadata, JudgeSourceSummary{
		Sources: map[string]int{"category:injection": 4, "category:privacy": 2}, Distinct: 2,
	})
	if !strings.Contains(multi, "2 档标准") {
		t.Errorf("多档说明未点明档位数：%q", multi)
	}
}

// TestJudgeSourceCaptionWarnsOnFallback 断言兜底降级**必然可见**，
// 且带具体条数 —— 只有「有兜底」与「无兜底」两种报告长得一样时，
// 降级才算真的被藏起来了。
func TestJudgeSourceCaptionWarnsOnFallback(t *testing.T) {
	metadata := map[string]string{
		RedteamJudgeSourceMetadataKey: "category:harmful",
	}
	caption := RedteamJudgeSourceCaption(metadata, JudgeSourceSummary{
		Sources:        map[string]int{"category:harmful": 8, JudgeSourceFallbackUnmapped: 3},
		FallbackProbes: 3,
		Distinct:       2,
	})
	if !strings.Contains(caption, "3 条探测") {
		t.Errorf("兜底警告未给出条数：%q", caption)
	}
	footer := RedteamJudgeSourceFooterCaption(metadata, JudgeSourceSummary{
		Sources:        map[string]int{"category:harmful": 8, JudgeSourceFallbackUnmapped: 3},
		FallbackProbes: 3,
		Distinct:       2,
	})
	if !strings.Contains(footer, "3") {
		t.Errorf("页脚兜底警告未给出条数：%q", footer)
	}
	// 页脚宽度是硬约束：超出会被截断，而被截掉的正是这个警告。
	if width := pdfTextWidth(footer, pdfFooterFontSize); width > 425 {
		t.Errorf("页脚兜底警告宽度 %.1f 超过 425，会被截断：%q", width, footer)
	}
}

func TestJudgeSourceCaptionFlagsIllegalMetadata(t *testing.T) {
	caption := RedteamJudgeSourceCaption(map[string]string{
		RedteamJudgeSourceMetadataKey: "category:伪造的族",
	}, JudgeSourceSummary{Sources: map[string]int{"category:harmful": 1}, Distinct: 1})
	if !strings.Contains(caption, "非法") {
		t.Errorf("非法 judge_source 的说明必须提示非法，而不是照抄：%q", caption)
	}
	// 页脚同理：不能在每页都在的位置呈现一个没人能解释的口径。
	if footer := RedteamJudgeSourceFooterCaption(map[string]string{
		RedteamJudgeSourceMetadataKey: "category:伪造的族",
	}, JudgeSourceSummary{}); footer != judgeSourceFooterUnknown {
		t.Errorf("非法元数据的页脚 = %q，期望 %q", footer, judgeSourceFooterUnknown)
	}
}

// TestReportPDFCarriesJudgeSourceFallbackWarning 验证 PDF 页脚承载降级警告。
//
// 关键点：警告必须出现在**页脚**（每页都在），不能只在正文段落 ——
// 只靠正文的话，用户翻到任意一页都可能看不到「这份报告有一部分没有判定标准」。
func TestReportPDFCarriesJudgeSourceFallbackWarning(t *testing.T) {
	score := 42.0
	report := &EvaluationReport{
		ID:          "redteam_report_u8_footer",
		RunID:       "run_u8",
		Title:       "大模型安全评估报告",
		Summary:     "本轮测试摘要。",
		RiskLevel:   "高风险",
		SafetyScore: &score,
		Metadata: map[string]string{
			RedteamJudgeTrackMetadataKey:          string(RedteamJudgeTrackEngine),
			RedteamJudgeSourceMetadataKey:         "category:harmful",
			RedteamJudgeSourcesMetadataKey:        "category:harmful=8," + JudgeSourceFallbackUnmapped + "=3",
			redteamJudgeSourceFallbackMetadataKey: RedteamJudgeSourceFallback,
		},
		CreatedAt: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC),
	}
	pdf := string(renderReportPDF(report))
	if !pdfContainsAllCJKSegments(pdf, "含 3 条兜底判定") {
		t.Fatal("PDF 页脚未渲染判定标准的兜底警告")
	}
	// 无兜底时，页脚必须让位给固定声明，不得凭空出现警告。
	clean := *report
	clean.Metadata = map[string]string{
		RedteamJudgeTrackMetadataKey:  string(RedteamJudgeTrackEngine),
		RedteamJudgeSourceMetadataKey: "category:harmful",
	}
	if pdfContainsAllCJKSegments(string(renderReportPDF(&clean)), "含 3 条兜底判定") {
		t.Fatal("无兜底探测的 PDF 页脚不应出现兜底警告")
	}
}

// TestReportSectionsCarryJudgeSourceLine 验证判定标准进「报告基本信息」
// 段落而非结论段落 —— 口径是元数据，插进摘要/评分会污染结论（DD-2=A）。
func TestReportSectionsCarryJudgeSourceLine(t *testing.T) {
	score := 40.0
	report := &EvaluationReport{
		Title:       "大模型安全评估报告",
		SafetyScore: &score,
		Metadata: map[string]string{
			RedteamJudgeTrackMetadataKey:          string(RedteamJudgeTrackEngine),
			RedteamJudgeSourceMetadataKey:         "category:privacy",
			RedteamJudgeSourcesMetadataKey:        "category:privacy=5," + JudgeSourceFallbackUnmapped + "=1",
			redteamJudgeSourceFallbackMetadataKey: RedteamJudgeSourceFallback,
		},
	}
	var infoLines []string
	for _, section := range reportSections(report) {
		if section.Title == "报告基本信息" {
			infoLines = section.Lines
		}
	}
	joined := strings.Join(infoLines, "\n")
	if !strings.Contains(joined, "判定标准") {
		t.Fatalf("报告基本信息缺少判定标准说明：%q", joined)
	}
	// 摘要段落不得含判定标准措辞。
	for _, section := range reportSections(report) {
		if section.Title == "1. 评估摘要" || section.Title == "5. 评估指标" {
			for _, line := range section.Lines {
				if strings.Contains(line, "判定标准") {
					t.Errorf("结论段落 %q 混入了判定口径说明：%q", section.Title, line)
				}
			}
		}
	}
}

// TestJudgeSourceAndJudgeTrackAreIndependent 钉住 U8 与 U4 的语义分离。
//
// 反证方式：同一份 metadata 同时带两个键时，两者各自解析出自己的值；
// 把 judge_track 改掉不影响 judge_source，反之亦然。
func TestJudgeSourceAndJudgeTrackAreIndependent(t *testing.T) {
	metadata := map[string]string{
		RedteamJudgeTrackMetadataKey:  string(RedteamJudgeTrackPlatform),
		RedteamJudgeSourceMetadataKey: "category:bias",
	}
	if got := RedteamJudgeTrackOf(metadata); got != RedteamJudgeTrackPlatform {
		t.Fatalf("judge_track = %q，期望 platform", got)
	}
	source, err := ValidateReportJudgeSourceMetadata(metadata)
	if err != nil || source != "category:bias" {
		t.Fatalf("judge_source = %q / %v，期望 category:bias / nil", source, err)
	}
	// 改链路不改标准。
	metadata[RedteamJudgeTrackMetadataKey] = string(RedteamJudgeTrackEngine)
	if got := RedteamJudgeTrackOf(metadata); got != RedteamJudgeTrackEngine {
		t.Fatalf("judge_track = %q，期望 engine", got)
	}
	if source, _ := ValidateReportJudgeSourceMetadata(metadata); source != "category:bias" {
		t.Fatalf("改 judge_track 后 judge_source 被污染为 %q", source)
	}
	// 改标准不改链路。
	delete(metadata, RedteamJudgeTrackMetadataKey)
	metadata[RedteamJudgeSourceMetadataKey] = "category:brand"
	if got := RedteamJudgeTrackOf(metadata); got != RedteamJudgeTrackPlatform {
		t.Fatalf("删 judge_track 后推断了 engine（U8 不该影响 U4）：%q", got)
	}
	if source, _ := ValidateReportJudgeSourceMetadata(metadata); source != "category:brand" {
		t.Fatalf("judge_source = %q，期望 category:brand", source)
	}
}

// TestEngineReportMetadataRecordsJudgeSources 验证引擎链路的报告 metadata
// 同时带上单值键与计数键 —— 少任何一个都会让降级被抹掉。
func TestEngineReportMetadataRecordsJudgeSources(t *testing.T) {
	metadata := engineReportMetadata(&EngineRunRecord{ID: "pferr_u8", JudgeMode: EngineJudgePromptfooNative}, &EngineSafeResult{
		Totals: EngineRunTotals{Probes: 9},
		JudgeSources: []EngineJudgeSourceCount{
			{Source: "category:harmful", Probes: 5},
			{Source: "category:injection", Probes: 3},
			{Source: JudgeSourceFallbackUnmapped, Probes: 1, IsFallback: true},
		},
	})
	if metadata[RedteamJudgeTrackMetadataKey] != string(RedteamJudgeTrackEngine) {
		t.Fatalf("judge_track = %q，期望 engine", metadata[RedteamJudgeTrackMetadataKey])
	}
	if got := metadata[RedteamJudgeSourcesMetadataKey]; got != "category:harmful=5,category:injection=3,fallback:unmapped=1" {
		t.Fatalf("judge_sources 直方图 = %q", got)
	}
	if got := metadata[RedteamJudgeSourceMetadataKey]; got != "category:harmful" {
		t.Fatalf("judge_source = %q，期望探测数最多的档位", got)
	}
	if metadata[redteamJudgeSourceFallbackMetadataKey] != RedteamJudgeSourceFallback {
		t.Error("存在兜底探测但未打 fallback 标记")
	}
}

// TestEngineReportMetadataOmitsJudgeSourceWhenUnrecorded 断言 U8 之前的
// 结果**不**被补一个档位：没记录就是没记录，不能反推。
func TestEngineReportMetadataOmitsJudgeSourceWhenUnrecorded(t *testing.T) {
	metadata := engineReportMetadata(&EngineRunRecord{ID: "pferr_u8", JudgeMode: EngineJudgePromptfooNative}, &EngineSafeResult{
		Totals: EngineRunTotals{Probes: 4},
	})
	if _, ok := metadata[RedteamJudgeSourceMetadataKey]; ok {
		t.Error("无 judge_sources 的结果被补了一个 judge_source —— 等于伪造判定口径")
	}
	if _, ok := metadata[RedteamJudgeSourcesMetadataKey]; ok {
		t.Error("无 judge_sources 的结果被补了直方图")
	}
}

// TestEngineFindingsCarryPerPluginJudgeSource 验证逐插件的判定标准落到
// finding metadata —— 这是「同一报告里不同插件按不同尺度判」的唯一可见处。
func TestEngineFindingsCarryPerPluginJudgeSource(t *testing.T) {
	findings := EngineSafeResultFindings(&EngineSafeResult{
		PluginStats: []EnginePluginStat{
			{PluginID: "pii:direct", Probes: 3, AttackSuccess: 1, JudgeSource: "plugin:pii"},
			{PluginID: "harmful:hate", Probes: 2, AttackSuccess: 0, JudgeSource: "category:harmful"},
		},
	})
	if len(findings) != 2 {
		t.Fatalf("findings 数 = %d，期望 2", len(findings))
	}
	got := map[string]string{}
	for _, finding := range findings {
		got[finding.Metadata["plugin_id"]] = finding.Metadata[RedteamJudgeSourceMetadataKey]
	}
	if got["pii:direct"] != "plugin:pii" {
		t.Errorf("pii:direct 的 judge_source = %q，期望 plugin:pii", got["pii:direct"])
	}
	if got["harmful:hate"] != "category:harmful" {
		t.Errorf("harmful:hate 的 judge_source = %q，期望 category:harmful", got["harmful:hate"])
	}
}

// ---------------------------------------------------------------------------
// U8 ①：`policy` 在原生生成路径上由 promptfoo 自带标准判定，这一档必须可见。
//
// 为什么不能让它继续显示 category:industry：那样它与contracts 完全同形，
// 用户会以为平台族标准生效了 —— 那是关于评分基础的不实陈述。
// ---------------------------------------------------------------------------

// TestPromptfooConfigUnsafePluginsAreExactlyPolicy 钉住 Go 侧的清单。
// 引擎侧 nativeRedteam.test.ts 有一条同名断言；两侧任一改动而另一方未改，
// 报告就会声称一个本次运行并未使用的基础。
func TestPromptfooConfigUnsafePluginsAreExactlyPolicy(t *testing.T) {
	unsafe := []string{}
	for id, affected := range promptfooJudgeRubricInjectedByPromptfoo {
		if affected {
			unsafe = append(unsafe, id)
		}
	}
	sort.Strings(unsafe)
	if len(unsafe) != 1 || unsafe[0] != "policy" {
		t.Fatalf("平台标准注入不了的插件清单 = %v，期望恰为 [policy]；改动时必须同步引擎侧的 PROMPTFOO_CONFIG_UNSAFE_PLUGIN_IDS", unsafe)
	}
	if !PromptfooJudgeRubricInjectedByPromptfoo("policy") {
		t.Error("policy 应被标记为注入不了")
	}
	if PromptfooJudgeRubricInjectedByPromptfoo("contracts") {
		t.Error("contracts 不应被标记（它能正常注入）")
	}
}

// TestNormalizeJudgeSourceAcceptsPromptfooTierForAffectedPluginsOnly 是防后门断言：
// `promptfoo:<id>` 这一档只对**确实受影响**的插件开放，否则任何插件都能
// 声称「我这条是上游判的」来绕过平台标准而不被察觉。
func TestNormalizeJudgeSourceAcceptsPromptfooTierForAffectedPluginsOnly(t *testing.T) {
	if _, err := NormalizeJudgeSource("promptfoo:policy"); err != nil {
		t.Errorf("受影响插件应能取 promptfoo 档：%v", err)
	}
	for _, raw := range []string{"promptfoo:contracts", "promptfoo:harmful", "promptfoo:不存在的插件"} {
		if _, err := NormalizeJudgeSource(raw); err == nil {
			t.Errorf("未受影响插件 %q 不应能声称 promptfoo 档（否则成后门）", raw)
		}
	}
}

func TestSummarizeEngineJudgeSourcesCountsPromptfooTier(t *testing.T) {
	summary := SummarizeEngineJudgeSources(&EngineSafeResult{
		JudgeSources: []EngineJudgeSourceCount{
			{Source: "category:industry", Probes: 6},
			{Source: "promptfoo:policy", Probes: 2},
			{Source: JudgeSourceFallbackUnmapped, Probes: 1, IsFallback: true},
		},
	})
	if summary.PromptfooOwnedProbes != 2 {
		t.Errorf("promptfoo 档探测数 = %d，期望 2", summary.PromptfooOwnedProbes)
	}
	if summary.FallbackProbes != 1 {
		t.Errorf("兜底档探测数 = %d，期望 1", summary.FallbackProbes)
	}
	// 两档必须互不吞并：成因不同，处置也不同。
	if summary.HasFallback() && summary.PromptfooOwnedProbes == 0 {
		t.Error("promptfoo 档被当成兜底档吞掉了")
	}
}

func TestJudgeSourceCaptionDistinguishesPromptfooTierFromFallback(t *testing.T) {
	metadata := map[string]string{
		RedteamJudgeSourcesMetadataKey: "category:industry=6,promptfoo:policy=2",
	}
	caption := RedteamJudgeSourceCaption(metadata, parseJudgeSourceHistogramMetadata(metadata))
	if !strings.Contains(caption, "2 条探测由 promptfoo 自带") {
		t.Errorf("说明未点出 promptfoo 自带标准这一档：%q", caption)
	}
	// 不能被写成「未匹配到族标准」—— 那是另一回事，会误导排查方向。
	if strings.Contains(caption, "未匹配到插件族标准") {
		t.Errorf("promptfoo 档被误写成兜底档：%q", caption)
	}
}
