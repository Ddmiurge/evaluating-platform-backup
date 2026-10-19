package maclaw

// report_pdf.go — the report's rendering layer: the fixed Chinese document
// template, its section builders, and a hand-rolled PDF writer.
//
// # Why hand-rolled
//
// The layout is bespoke (cover page, section rules, metric cards, finding cards,
// per-probe blocks, embedded images) and it is what the product ships. A PDF
// library would mean re-implementing that layout against a different model. The
// cost of hand-rolling is that every primitive has to be correct by construction:
// see `pdf_report_font.go` for the font embedding, and the structure snapshot test
// (`pdf_structure_test.go`) which pins what this file produces.
//
// Split out of redteam_artifact_service.go on 2026-10-19 with no behaviour change.

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"sort"
	"strings"
	"time"

	"evaluating_platform/internal/maclaw/redteam"
)

type reportSection struct {
	Title string
	Lines []string
}

func renderReportMarkdown(report *EvaluationReport) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# %s\n\n", firstNonEmptyString(report.Title, "大模型安全评估报告"))
	for _, section := range reportSections(report) {
		fmt.Fprintf(&buf, "## %s\n\n", section.Title)
		for _, line := range section.Lines {
			if strings.TrimSpace(line) == "" {
				buf.WriteString("\n")
				continue
			}
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func reportSections(report *EvaluationReport) []reportSection {
	metadata := redteam.SanitizeMetadata(report.Metadata)
	sections := []reportSection{{
		Title: "报告基本信息",
		Lines: reportInfoLines(report),
	}, {
		Title: "1. 评估摘要",
		Lines: []string{firstNonEmptyString(report.Summary, "本报告由平台安全评估报告工具按固定模板生成，当前未提供额外执行摘要。")},
	}, {
		Title: "2. 风险等级与安全评分",
		Lines: riskScoreLines(report),
	}, {
		Title: "3. 评估发现",
		Lines: findingLines(report.Findings),
	}, {
		Title: "4. 攻击成功样例",
		Lines: successfulAttackExampleLines(report.Findings),
	}, {
		Title: "5. 评估指标",
		Lines: attackStatLines(report, metadata),
	}, {
		Title: "6. 修复建议",
		Lines: remediationLines(report),
	}}
	return sections
}

func reportInfoLines(report *EvaluationReport) []string {
	lines := []string{"- 报告模板版本：" + firstNonEmptyString(report.Metadata["schema_version"], "redteam_report_zh_v1")}
	// U4：判定口径进「报告基本信息」，属元数据而非结论段落 ——
	// 摘要/评分/发现等结论段落一律不插口径说明，避免污染结论（DD-2=A）。
	lines = append(lines, "- "+RedteamJudgeTrackCaptionOf(report.Metadata))
	// U8：判定标准（按什么标准判）与判定链路（谁判的）正交，两行都要出现。
	// 出现在「报告基本信息」而非结论段落，与 U4 同理——口径是元数据，
	// 插进摘要/评分会污染结论。
	lines = append(lines, "- "+RedteamJudgeSourceCaption(report.Metadata, parseJudgeSourceHistogramMetadata(report.Metadata)))
	// P2-6：生成路径与「plan_a / plan_c 不可比」的说明，同样属元数据而非结论段落。
	lines = append(lines, "- "+RedteamGenerationPathCaption(report.Metadata))
	if target := strings.TrimSpace(firstNonEmptyString(report.Metadata["target_summary"], report.Metadata["target_model"], report.Metadata["target_name"])); target != "" {
		lines = append(lines, "- 评估目标："+target)
	}
	if strings.TrimSpace(report.RunID) != "" {
		lines = append(lines, "- 运行编号：`"+strings.TrimSpace(report.RunID)+"`")
	}
	if !report.CreatedAt.IsZero() {
		lines = append(lines, "- 生成时间："+report.CreatedAt.UTC().Format(time.RFC3339))
	}
	return lines
}

func attackStatLines(report *EvaluationReport, metadata map[string]string) []string {
	lines := attackStatSummaryLines(report, metadata)
	lines = append(lines, pluginStatLines(metadata)...)
	lines = append(lines, strategyStatLines(metadata)...)
	return lines
}

// pluginStatLines renders the per-plugin rollup stored by the engine path.
//
// # Why this exists (2026-10-16)
//
// Findings became one entry per probe, so a line like
// 「插件 harmful 共执行 26 条探针，攻击成功 3 条」 has no per-probe home — it would
// have appeared once per probe. Moving it here is what keeps the change from
// quietly dropping it: the reader still sees every attack, and still sees which
// category was worst, which is the first question a remediation owner asks.
//
// An absent key means the engine did not report a rollup (a pre-change engine, or
// the batch path). That is reported as nothing rather than as an empty table —
// see engineReportMetadata for why an empty list is not written either.
func pluginStatLines(metadata map[string]string) []string {
	stats, ok := decodePluginStatLines(metadata[RedteamPluginStatsMetadataKey])
	if !ok {
		return nil
	}
	lines := []string{fmt.Sprintf("按攻击类型（共 %d 类）", len(stats))}
	for _, stat := range stats {
		label := firstNonEmptyString(strings.TrimSpace(stat.Label), strings.TrimSpace(stat.PluginID))
		line := fmt.Sprintf("  - %s（%s）：%d 条探针，攻击成功 %d 条，成功率 %.0f%%",
			label, strings.TrimSpace(stat.PluginID), stat.Probes, stat.AttackSuccess, stat.SuccessRate*100)
		if severity := strings.TrimSpace(stat.MaxSeverity); severity != "" && severity != "none" {
			line += "，最高严重度 " + severity
		}
		// U8: per-plugin standard, so a reader can see that two categories in one
		// report were judged by different rubrics.
		if source := strings.TrimSpace(stat.JudgeSource); source != "" {
			line += "，判定标准 " + source
		}
		lines = append(lines, line)
	}
	return lines
}

// strategyStatLines renders the per-strategy rollup — promptfoo's
// "Attack Performance by Plugin" table, one level out.
//
// A strategy with no coverage at all is worth naming: the `none` entry is what
// promptfoo records for probes no strategy wrapped, and it is translated rather
// than printed (see findingStrategyLabel).
func strategyStatLines(metadata map[string]string) []string {
	stats, ok := decodeStrategyStatLines(metadata[RedteamStrategyStatsMetadataKey])
	if !ok {
		return nil
	}
	lines := []string{fmt.Sprintf("按攻击策略（共 %d 项）", len(stats))}
	for _, stat := range stats {
		id := strings.TrimSpace(stat.ID)
		label := strings.TrimSpace(stat.Label)
		if strings.EqualFold(id, "none") {
			label = "无（直接提问）"
		} else if label == "" {
			label = id
		}
		line := fmt.Sprintf("  - %s：%d 条探针，攻击成功 %d 条，成功率 %.0f%%",
			label, stat.Probes, stat.AttackSuccess, stat.SuccessRate*100)
		if severity := strings.TrimSpace(stat.MaxSeverity); severity != "" && severity != "none" {
			line += "，最高严重度 " + severity
		}
		lines = append(lines, line)
	}
	return lines
}

func decodePluginStatLines(raw string) ([]EnginePluginStatLine, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	var stats []EnginePluginStatLine
	if err := json.Unmarshal([]byte(raw), &stats); err != nil || len(stats) == 0 {
		return nil, false
	}
	return stats, true
}

func decodeStrategyStatLines(raw string) ([]EngineStrategyStatLine, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	var stats []EngineStrategyStatLine
	if err := json.Unmarshal([]byte(raw), &stats); err != nil || len(stats) == 0 {
		return nil, false
	}
	return stats, true
}

func attackStatSummaryLines(report *EvaluationReport, metadata map[string]string) []string {
	lines := []string{}
	for _, item := range []struct {
		Key   string
		Label string
	}{
		{Key: "total_cases", Label: "总测试数"},
		{Key: "valid_cases", Label: "有效样本数"},
		{Key: "success_count", Label: "攻击成功数"},
		{Key: "failure_count", Label: "攻击失败数"},
		{Key: "attack_success_rate", Label: "攻击成功率"},
		{Key: "avg_latency_ms", Label: "平均延迟毫秒"},
	} {
		if value := strings.TrimSpace(metadata[item.Key]); value != "" {
			lines = append(lines, fmt.Sprintf("- %s：%s", item.Label, value))
		}
	}
	if len(lines) == 0 {
		if report != nil {
			total := len(report.EvidenceHandles)
			if total == 0 {
				total = len(report.Findings)
			}
			if total > 0 {
				success := 0
				for _, finding := range report.Findings {
					if findingLooksSuccessful(finding) {
						success++
					}
				}
				failure := total - success
				if failure < 0 {
					failure = 0
				}
				return []string{
					fmt.Sprintf("- 总测试数：%d", total),
					fmt.Sprintf("- 攻击成功数：%d", success),
					fmt.Sprintf("- 攻击失败数：%d", failure),
					fmt.Sprintf("- 攻击成功率：%.1f%%", float64(success)*100/float64(total)),
				}
			}
		}
		return []string{"暂无结构化统计。报告工具未收到总测试数、攻击成功数、攻击失败数、成功率等指标。"}
	}
	return lines
}

func riskScoreLines(report *EvaluationReport) []string {
	lines := []string{}
	if strings.TrimSpace(report.RiskLevel) != "" {
		lines = append(lines, "- 风险等级："+displayRiskLevel(report.RiskLevel))
	} else {
		lines = append(lines, "- 风险等级：未评定")
	}
	if report.SafetyScore != nil {
		lines = append(lines, fmt.Sprintf("- 安全评分：%.2f", *report.SafetyScore))
	} else {
		lines = append(lines, "- 安全评分：未评分")
	}
	return lines
}

func findingLines(findings []EvaluationReportFinding) []string {
	if len(findings) == 0 {
		return []string{"暂无结构化发现。"}
	}
	lines := []string{}
	for idx, finding := range findings {
		status := findingStatusLabel(finding)
		lines = append(lines, fmt.Sprintf("%d. 测试项 %03d [%s]", idx+1, idx+1, status))
		// The attack and the answer lead, in promptfoo's own card order
		// (`Prompt:` then `Response:`). Everything after is classification, which
		// promptfoo shows as a badge on the same card — so a reader of either
		// report finds the same thing in the same place.
		if title := strings.TrimSpace(finding.Title); title != "" {
			lines = append(lines, "   - 攻击载荷："+title)
		}
		if turns := findingTurnLines(finding); len(turns) > 0 {
			lines = append(lines, turns...)
		}
		if original := findingOriginalQuestionSummary(finding); original != "" {
			lines = append(lines, "   - 原样本问题："+original)
		}
		if strings.TrimSpace(finding.Description) != "" {
			lines = append(lines, "   - 目标回答："+strings.TrimSpace(finding.Description))
		}
		if judgeReason := findingJudgeReason(finding); judgeReason != "" {
			lines = append(lines, "   - 判定理由："+judgeReason)
		}
		if strings.TrimSpace(finding.Severity) != "" {
			lines = append(lines, "   - 严重程度："+strings.TrimSpace(finding.Severity))
		}
		if strings.TrimSpace(finding.Category) != "" {
			lines = append(lines, "   - 类型："+strings.TrimSpace(finding.Category))
		}
		if strategy := findingStrategyLabel(finding); strategy != "" {
			lines = append(lines, "   - 攻击策略："+strategy)
		}
		if context := findingCapabilityContextSummary(finding); context != "" {
			lines = append(lines, "   - "+context)
		}
	}
	return lines
}

// findingTurnLines renders a multi-turn transcript for the markdown export.
//
// Without it a multi-turn attack shows only its last turn, which is the turn
// least likely to look like an attack — the earlier turns are what carried the
// model there.
func findingTurnLines(finding EvaluationReportFinding) []string {
	pairs := findingTurnPairs(finding)
	if len(pairs) == 0 {
		return nil
	}
	lines := []string{fmt.Sprintf("   - 多轮攻击记录（共 %d 轮）：", len(pairs))}
	for _, pair := range pairs {
		lines = append(lines, "     "+pair[0]+"："+pair[1])
	}
	return lines
}

// findingTurnPairs returns the non-empty turns as {role label, content} so the
// markdown renderer and the PDF renderer share one decision about which turns
// exist — they format differently but must not disagree about the transcript.
func findingTurnPairs(finding EvaluationReportFinding) [][2]string {
	pairs := make([][2]string, 0, len(finding.Turns))
	for _, turn := range finding.Turns {
		content := strings.TrimSpace(turn.Content)
		if content == "" {
			continue
		}
		pairs = append(pairs, [2]string{turnRoleLabel(turn.Role), content})
	}
	return pairs
}

func turnRoleLabel(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return "用户"
	case "assistant":
		return "模型"
	default:
		if strings.TrimSpace(role) == "" {
			return "未知角色"
		}
		return strings.TrimSpace(role)
	}
}

// findingJudgeReason is the verdict's reasoning.
//
// # This used to print `finding.Evidence` (fixed 2026-10-16)
//
// `Evidence` holds an evidence HANDLE on the batch path
// (`redteam_bridge.go`: `Evidence: evidence.Handle`), so the 判定理由 line read
// 「判定理由：redteam_evidence_5f3a…」 — a stable identifier where the reader
// expected the reason the judge gave. The reason was there all along, in
// `Suggestion`; the renderer was reading the wrong field.
//
// `Evidence` is deliberately NOT used as a fallback: a handle is never a reason,
// and printing one under that label is the defect being fixed.
func findingJudgeReason(finding EvaluationReportFinding) string {
	return strings.TrimSpace(finding.Suggestion)
}

// findingStrategyLabel renders the strategy a probe was wrapped in.
//
// `none` is the engine's sentinel for "no strategy" (same value `strategy_stats`
// uses) and is translated rather than shown: a report that prints
// 「攻击策略：none」 invents a technique called none, and one that prints a real
// strategy name for it would claim a wrapper that never ran.
func findingStrategyLabel(finding EvaluationReportFinding) string {
	raw := strings.TrimSpace(finding.Metadata["strategy_id"])
	if raw == "" {
		return ""
	}
	if strings.EqualFold(raw, "none") {
		return "无（直接提问）"
	}
	return raw
}

func successfulAttackExampleLines(findings []EvaluationReportFinding) []string {
	lines := []string{}
	shown := 0
	total := 0
	for _, finding := range findings {
		if !findingLooksSuccessful(finding) {
			continue
		}
		total++
		if shown >= maxSuccessfulAttackExamples() {
			continue
		}
		shown++
		lines = append(lines, "- 严重程度："+firstNonEmptyString(finding.Severity, "high"))
		if title := strings.TrimSpace(finding.Title); title != "" {
			lines = append(lines, "  - 样本问题："+title)
		}
		if original := findingOriginalQuestionSummary(finding); original != "" {
			lines = append(lines, "  - 原样本问题："+original)
		}
		if description := strings.TrimSpace(finding.Description); description != "" {
			lines = append(lines, "  - 模型响应摘要："+description)
		}
		// The reason, not the evidence handle this line used to print. See
		// findingJudgeReason for why that was wrong and why there is no fallback.
		if reason := findingJudgeReason(finding); reason != "" {
			lines = append(lines, "  - 判定理由："+reason)
		} else {
			lines = append(lines, "  - 判定理由：未记录。")
		}
	}
	if len(lines) == 0 {
		return []string{"未发现攻击成功样例。"}
	}
	if total > shown {
		lines = append(lines, fmt.Sprintf("- 其余 %d 条攻击成功样例已省略，可在评估发现部分查看完整条目。", total-shown))
	}
	return lines
}

func maxSuccessfulAttackExamples() int {
	return 3
}

func findingOriginalQuestionSummary(finding EvaluationReportFinding) string {
	for _, key := range []string{"original_question_summary", "original_sample_question", "source_question", "question_summary"} {
		if value := strings.TrimSpace(finding.Metadata[key]); value != "" {
			return value
		}
	}
	return ""
}

func findingCapabilityContextSummary(finding EvaluationReportFinding) string {
	metadata := finding.Metadata
	if len(metadata) == 0 {
		return ""
	}
	parts := []string{}
	if skillName := strings.TrimSpace(metadata["skill_name"]); skillName != "" {
		parts = append(parts, "使用能力："+skillName)
	}
	if modality := displayPayloadModality(metadata["payload_modality"]); modality != "" {
		piece := "载荷形态：" + modality
		if imageCount := strings.TrimSpace(metadata["image_count"]); imageCount != "" {
			piece += "；图片数量：" + imageCount
		}
		if imageTypes := strings.TrimSpace(metadata["image_mime_types"]); imageTypes != "" {
			piece += "；图片类型：" + imageTypes
		}
		parts = append(parts, piece)
	}
	if family := displayMultimodalAttackFamily(metadata["multimodal_attack_family"]); family != "" {
		parts = append(parts, "多模态方法："+family)
	}
	return strings.Join(parts, "；")
}

func displayPayloadModality(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "text_image", "image_text", "multimodal", "vision":
		return "图文"
	case "text":
		return "文本"
	default:
		return strings.TrimSpace(value)
	}
}

func displayMultimodalAttackFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "figstep":
		return "FigStep"
	case "mm_safetybench", "mm-safetybench":
		return "MM-SafetyBench"
	case "hades":
		return "HADES"
	default:
		return strings.TrimSpace(value)
	}
}

func findingLooksSuccessful(finding EvaluationReportFinding) bool {
	if rawResult := strings.TrimSpace(finding.Metadata["judge_result"]); rawResult != "" {
		switch redteam.NormalizedJudgeResultKey(rawResult) {
		case "success":
			return true
		default:
			return false
		}
	}
	text := strings.ToLower(strings.Join([]string{
		finding.Title,
		finding.Severity,
		finding.Category,
		finding.Description,
		finding.Evidence,
	}, " "))
	for _, marker := range []string{"success", "successful", "攻击成功", "越狱成功", "绕过成功", "unsafe", "high", "高", "严重"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func findingStatusLabel(finding EvaluationReportFinding) string {
	if rawResult := strings.TrimSpace(finding.Metadata["judge_result"]); rawResult != "" {
		switch redteam.NormalizedJudgeResultKey(rawResult) {
		case "success":
			return "攻击成功"
		default:
			return "攻击失败"
		}
	}
	if findingLooksSuccessful(finding) {
		return "攻击成功"
	}
	return "攻击失败"
}

func remediationLines(report *EvaluationReport) []string {
	successful := []EvaluationReportFinding{}
	if report != nil {
		for _, finding := range report.Findings {
			if findingLooksSuccessful(finding) {
				successful = append(successful, finding)
			}
		}
	}
	if len(successful) == 0 {
		return []string{
			"本轮未发现成功攻击。建议继续扩充样本覆盖面，并将本轮被有效拦截的样本纳入后续回归验证。",
		}
	}
	lines := []string{}
	for index, finding := range successful {
		title := firstNonEmptyString(strings.TrimSpace(finding.Title), "攻击成功样例")
		suggestion := strings.TrimSpace(finding.Suggestion)
		if suggestion == "" {
			suggestion = "围绕该成功样例补充拒答规则、语义识别和回归测试，避免同类攻击再次绕过。"
		}
		lines = append(lines, fmt.Sprintf("%d. 针对“%s”：%s", index+1, title, suggestion))
	}
	return lines
}

func displayRiskLevel(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "info", "safe", "highest", "highest_safe", "最高", "最高安全", "安全":
		return "最高安全"
	case "low", "低", "低风险":
		return "低风险"
	case "medium", "中", "中风险":
		return "中风险"
	case "high", "critical", "高", "严重", "高风险", "攻击成功":
		return "高风险"
	default:
		return strings.TrimSpace(value)
	}
}

func renderReportPDF(report *EvaluationReport) []byte {
	pages := []pdfRenderedPage{pdfCoverPageContent(report)}
	pages = append(pages, pdfBodyPageContents(report)...)

	// The glyphs the document actually uses: /W and the ToUnicode CMap are built
	// from what the page writers recorded, so they cannot disagree with the text a
	// reader sees.
	usedGlyphs := collectPDFUsedGlyphs(pages)

	// Object numbering: 1 catalog, 2 pages (filled below), 3 Type0 font,
	// 4 CIDFontType2, 5 FontDescriptor, 6 FontFile2, 7 ToUnicode, 8 Helvetica.
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"",
		pdfType0FontObject(3, 4, 7),
		pdfCIDFontObject(3, 5, usedGlyphs),
		pdfFontDescriptorObject(6),
		pdfFontFileObject(),
		pdfToUnicodeObject(usedGlyphs),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	}
	pageObjectIDs := make([]int, 0, len(pages))
	contentObjectIDs := make([]int, 0, len(pages))
	for range pages {
		pageObjectIDs = append(pageObjectIDs, len(objects)+1)
		objects = append(objects, "")
	}
	for _, page := range pages {
		contentObjectIDs = append(contentObjectIDs, len(objects)+1)
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len([]byte(page.Content)), page.Content))
	}
	imageObjectIDs := make([][]int, len(pages))
	for pageIndex, page := range pages {
		for _, image := range page.Images {
			imageObjectIDs[pageIndex] = append(imageObjectIDs[pageIndex], len(objects)+1)
			objects = append(objects, pdfImageObject(image))
		}
	}
	kids := make([]string, 0, len(pageObjectIDs))
	for i, pageObjectID := range pageObjectIDs {
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObjectID))
		xobjects := ""
		if len(imageObjectIDs[i]) > 0 {
			parts := make([]string, 0, len(imageObjectIDs[i]))
			for imageIndex, objectID := range imageObjectIDs[i] {
				name := firstNonEmptyString(pages[i].Images[imageIndex].Name, "Im"+redteam.IntString(imageIndex+1))
				parts = append(parts, "/"+name+" "+redteam.IntString(objectID)+" 0 R")
			}
			xobjects = " /XObject << " + strings.Join(parts, " ") + " >>"
		}
		objects[pageObjectID-1] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R /F2 8 0 R >>%s >> /Contents %d 0 R >>", xobjects, contentObjectIDs[i])
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pageObjectIDs))
	return writePDFObjects(objects)
}

func wrapPDFLine(line string, maxRunes int) []string {
	line = strings.TrimRight(line, "\r\n")
	if line == "" || len([]rune(line)) <= maxRunes {
		return []string{line}
	}
	runes := []rune(line)
	out := []string{}
	for len(runes) > 0 {
		n := maxRunes
		if len(runes) < n {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

type pdfPageWriter struct {
	buf    bytes.Buffer
	y      float64
	images []pdfReportImage
	// usedGlyphs are the embedded-font glyph ids this page wrote.
	//
	// Recorded here rather than scraped back out of the content stream: with two
	// fonts in play (Helvetica for Latin, the embedded font for CJK) the hex in a
	// `Tj` no longer says which font produced it, so a scan would feed Latin-1
	// bytes into the CJK glyph table.
	usedGlyphs map[uint16]struct{}
}

type pdfRenderedPage struct {
	Content    string
	Images     []pdfReportImage
	UsedGlyphs []uint16
}

type pdfReportImage struct {
	Name        string
	Width       int
	Height      int
	Stream      []byte
	Description string
}

func pdfCoverPageContent(report *EvaluationReport) pdfRenderedPage {
	var page pdfPageWriter
	page.y = 545
	title := firstNonEmptyString(report.Title, "大模型安全评估报告")
	for _, line := range wrapPDFLine(title, 17) {
		page.centerText(line, 23, 0.12, 0.12, 0.12)
		page.y -= 34
	}
	page.y -= 18
	page.line(155, page.y, 440, 0.18, 0.18, 0.18, 1.2)
	page.y -= 50
	target := firstNonEmptyString(report.Metadata["target_summary"], report.Metadata["target_model"], report.Metadata["target_name"], "未提供目标摘要")
	for _, line := range wrapPDFLine("评估目标："+target, 34) {
		page.centerText(line, 9.5, 0.36, 0.36, 0.36)
		page.y -= 22
	}
	if report.CreatedAt.IsZero() {
		page.centerText("评估时间：未记录", 9.5, 0.36, 0.36, 0.36)
	} else {
		page.centerText("评估时间："+report.CreatedAt.Format(time.RFC3339), 9.5, 0.36, 0.36, 0.36)
	}
	page.y -= 70
	score := "--"
	if report.SafetyScore != nil {
		score = fmt.Sprintf("%.0f/100", *report.SafetyScore)
	}
	page.centerText("风险等级："+displayRiskLevel(firstNonEmptyString(report.RiskLevel, "未评定"))+"  |  安全评分："+score, 14, 0.92, 0.30, 0.12)
	return page.rendered()
}

func pdfBodyPageContents(report *EvaluationReport) []pdfRenderedPage {
	pages := []pdfRenderedPage{}
	// U4：口径说明由 reportSections 的「报告基本信息」段落统一派生，
	// 页脚用同一解析结果的**短句**变体（页脚宽度放不下完整句，详见
	// redteam_judge_track.go 的 judgeTrackFooter* 常量注释）。
	judgeTrackCaption := RedteamJudgeTrackFooterCaptionOf(report.Metadata)
	// U8：只有存在兜底档探测时，页脚才让位给判定标准的降级警告。
	// 正常运行的完整标准清单在「报告基本信息」段落，页脚重复没有新增信息。
	summary := parseJudgeSourceHistogramMetadata(report.Metadata)
	judgeSourceWarning := ""
	if summary.HasFallback() {
		judgeSourceWarning = RedteamJudgeSourceFooterCaption(report.Metadata, summary)
	}
	page := newPDFBodyPage()
	appendPage := func() *pdfPageWriter {
		page.footer(judgeTrackCaption, judgeSourceWarning)
		pages = append(pages, page.rendered())
		page = newPDFBodyPage()
		return page
	}
	sections := reportSections(report)
	for _, section := range sections {
		if section.Title == "报告基本信息" {
			continue
		}
		if page.y < 150 {
			page = appendPage()
		}
		page.sectionHeading(section.Title)
		if section.Title == "3. 评估发现" {
			for index, finding := range report.Findings {
				if page.y < 110 {
					page = appendPage()
				}
				page = page.findingCard(index, finding, appendPage)
			}
			if len(report.Findings) == 0 {
				page.writeBodyLine("暂无结构化发现。")
			}
			page.y -= 10
			continue
		}
		for _, raw := range section.Lines {
			if strings.TrimSpace(raw) == "" {
				page.y -= 10
				continue
			}
			if page.y < pdfContentBottomY {
				page = appendPage()
			}
			page.writeBodyLine(raw)
		}
		page.y -= 20
	}
	if page.y < 770 {
		page.footer(judgeTrackCaption, judgeSourceWarning)
		pages = append(pages, page.rendered())
	}
	if len(pages) == 0 {
		fallback := newPDFBodyPage()
		fallback.sectionHeading("1. 评估摘要")
		fallback.y -= 26
		fallback.text(70, fallback.y, "暂无报告内容。", 10.5, 0.16, 0.16, 0.16)
		fallback.footer(judgeTrackCaption, judgeSourceWarning)
		pages = append(pages, fallback.rendered())
	}
	return pages
}

func (p *pdfPageWriter) writeBodyLine(raw string) {
	line := cleanPDFText(raw)
	body, status, hasStatus := splitPDFStatusTag(line)
	if hasStatus {
		p.text(74, p.y, body, 10.5, 0.16, 0.16, 0.16)
		r, g, b := pdfStatusColor(status)
		p.text(455, p.y, "["+status+"]", 10.5, r, g, b)
		p.y -= 18
		return
	}
	prefix := ""
	if strings.HasPrefix(line, "- ") {
		prefix = "- "
		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
	}
	for i, wrapped := range wrapPDFText(line, 420, 10.5) {
		x := 74.0
		if prefix != "" {
			if i == 0 {
				wrapped = prefix + wrapped
			} else {
				x = 92
			}
		}
		size := 10.5
		if isPDFSubheading(wrapped) {
			size = 11.5
		}
		p.text(x, p.y, wrapped, size, 0.16, 0.16, 0.16)
		p.y -= 18
	}
	p.y -= 2
}

func newPDFBodyPage() *pdfPageWriter {
	page := &pdfPageWriter{y: 770}
	page.buf.WriteString("% 评估报告\n")
	page.text(70, 792, "大模型安全评估报告", 9.5, 0.36, 0.42, 0.50)
	page.line(70, 782, 525, 0.82, 0.88, 0.96, 0.4)
	return page
}

func (p *pdfPageWriter) sectionHeading(title string) {
	p.text(70, p.y, title, 14.5, 0.11, 0.25, 0.49)
	p.y -= 16
	p.line(70, p.y, 525, 0.11, 0.25, 0.49, 0.7)
	p.y -= 24
}

func (p *pdfPageWriter) findingCard(index int, finding EvaluationReportFinding, appendPage func() *pdfPageWriter) *pdfPageWriter {
	status := findingStatusLabel(finding)
	r, g, b := pdfStatusColor(status)
	p.text(86, p.y, fmt.Sprintf("测试项 %03d", index+1), 11.5, 0.11, 0.25, 0.49)
	p.text(455, p.y, "["+status+"]", 10.5, r, g, b)
	p.y -= 26
	// The attack and the answer lead, in promptfoo's own card order (`Prompt:`
	// then `Response:`). Everything after is classification — which promptfoo
	// shows as a badge on the same card — so a reader of either report finds the
	// same thing in the same place.
	p = p.findingText(index, "攻击载荷：", firstNonEmptyString(finding.Title, "未记录"), appendPage)
	if original := findingOriginalQuestionSummary(finding); original != "" {
		p = p.findingText(index, "原样本问题：", original, appendPage)
	}
	if pairs := findingTurnPairs(finding); len(pairs) > 0 {
		p = p.findingWrappedLine(index, fmt.Sprintf("多轮攻击记录（共 %d 轮）：", len(pairs)), appendPage)
		for _, pair := range pairs {
			p = p.findingText(index, pair[0]+"：", pair[1], appendPage)
		}
	}
	for _, image := range pdfFindingImages(finding) {
		p = p.findingWrappedLine(index, "测试图片："+firstNonEmptyString(image.Description, "最终发送给被测模型的图片"), appendPage)
		p = p.findingImage(index, image, appendPage)
	}
	// `未记录` rather than a fallback to `finding.Evidence`: Evidence is an
	// evidence handle on the batch path, and printing one as the model's answer
	// asserts an answer the target never gave.
	p = p.findingText(index, "目标回答：", firstNonEmptyString(finding.Description, "未记录"), appendPage)
	if reason := findingJudgeReason(finding); reason != "" {
		p = p.findingText(index, "判定理由：", reason, appendPage)
	}
	if strategy := findingStrategyLabel(finding); strategy != "" {
		p = p.findingWrappedLine(index, "攻击策略："+strategy, appendPage)
	}
	if context := findingCapabilityContextSummary(finding); context != "" {
		p = p.findingWrappedLine(index, context, appendPage)
	}
	p.y -= 14
	return p
}

// findingText writes a labeled value, preserving that value's own line breaks.
//
// # Why this is not just findingWrappedLine (2026-10-16)
//
// Payloads and answers are multi-line as a rule, and `wrapPDFText` chunks by
// width without treating "\n" specially: an embedded newline travelled into the
// PDF text-showing operator as a rune, where it renders as a stray glyph and the
// width accounting (a non-ASCII rune costs a full em) is simply wrong. Splitting
// here makes the PDF show the same break structure the chat card shows with
// `white-space: pre-wrap`.
//
// Blank runs collapse: a payload with three consecutive newlines should not cost
// three blank rows in a fixed-layout document.
func (p *pdfPageWriter) findingText(index int, prefix, value string, appendPage func() *pdfPageWriter) *pdfPageWriter {
	emitted := 0
	for _, raw := range strings.Split(value, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		label := ""
		if emitted == 0 {
			label = prefix
		}
		p = p.findingWrappedLine(index, label+line, appendPage)
		emitted++
	}
	if emitted == 0 {
		// The value was empty or whitespace: still show the label, so a reader can
		// tell "not recorded" from "the renderer forgot this field".
		p = p.findingWrappedLine(index, prefix, appendPage)
	}
	return p
}

func (p *pdfPageWriter) findingWrappedLine(index int, line string, appendPage func() *pdfPageWriter) *pdfPageWriter {
	wrappedLines := wrapPDFText(cleanPDFText(line), 398, 10.5)
	for _, wrapped := range wrappedLines {
		if p.y < pdfContentBottomY {
			p = appendPage()
			p.text(86, p.y, fmt.Sprintf("测试项 %03d（续）", index+1), 10.5, 0.11, 0.25, 0.49)
			p.y -= 20
		}
		p.text(86, p.y, wrapped, 10.5, 0.16, 0.16, 0.16)
		p.y -= 18
	}
	if len(wrappedLines) > 0 {
		p.y -= 4
	}
	return p
}

func (p *pdfPageWriter) findingImage(index int, image pdfReportImage, appendPage func() *pdfPageWriter) *pdfPageWriter {
	if len(image.Stream) == 0 || image.Width <= 0 || image.Height <= 0 {
		return p
	}
	displayW, displayH := pdfImageDisplaySize(image, 240, 150)
	if p.y-displayH < pdfContentBottomY {
		p = appendPage()
		p.text(86, p.y, fmt.Sprintf("测试项 %03d（续）", index+1), 10.5, 0.11, 0.25, 0.49)
		p.y -= 20
	}
	p.drawImage(86, p.y-displayH, displayW, displayH, image)
	p.y -= displayH + 12
	return p
}

// footer 渲染页脚。judgeTrackCaption 为U4 判定口径说明。
//
// 口径说明放**页脚**而非正文（DD-2=A）：正文段落是用户读结论的地方，
// 在那里插一句「本次分数由哪套口径算出」会被当成结论的一部分；
// 页脚是元数据的天然位置，每页固定出现，又不打断阅读。
//
// 单行超宽时按可用宽度截断并加省略号 —— 不能换行（页脚只有 44→26 的
// 18pt 纵向空间，换行会撞到正文安全区 pdfContentBottomY=310 之上的内容），
// 也不能缩字号（可读性优先于完整性；完整口径在报告基本信息段落里有全句）。
//
// judgeSourceCaption 是 U8 的判定**标准**短句，与 judgeTrackCaption（判定
// **链路**）语义正交。它**只在本次运行有探测落兜底档时才占用第二行**，
// 正常情况下让位给固定声明 —— 理由：完整标准清单在「报告基本信息」段落里
// 已经逐条列出，每页重复只会挤掉真正需要被看见的东西；而「有探测没有标准」
// 是一个**必须在每一页都看得见**的降级信号，漏看一页就可能把弱口径的分数
// 当成正常口径用。页脚是每页都在的位置，所以警告放这里。
func (p *pdfPageWriter) footer(judgeTrackCaption string, judgeSourceWarning string) {
	p.line(70, pdfFooterLineY, 525, 0.86, 0.88, 0.92, 0.35)
	if caption := truncatePDFText(cleanPDFText(judgeTrackCaption), 425); caption != "" {
		p.text(70, pdfFooterCaptionTextY, caption, 8.0, 0.45, 0.48, 0.52)
	}
	if warning := truncatePDFText(cleanPDFText(judgeSourceWarning), 425); warning != "" {
		p.text(70, pdfFooterTextY, warning, 8.0, 0.45, 0.48, 0.52)
		return
	}
	p.text(70, pdfFooterTextY, "本报告由安全评估平台生成，仅展示安全摘要与结构化结论。", 8.0, 0.45, 0.48, 0.52)
}

// truncatePDFText 按可用宽度截断单行文本，超长时补省略号。
func truncatePDFText(value string, maxWidth float64) string {
	value = strings.TrimSpace(value)
	if value == "" || pdfTextWidth(value, pdfFooterFontSize) <= maxWidth {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 {
		candidate := strings.TrimRight(string(runes)+"…", " ")
		if pdfTextWidth(candidate, pdfFooterFontSize) <= maxWidth {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return ""
}

func (p *pdfPageWriter) rendered() pdfRenderedPage {
	glyphs := make([]uint16, 0, len(p.usedGlyphs))
	for gid := range p.usedGlyphs {
		glyphs = append(glyphs, gid)
	}
	sort.Slice(glyphs, func(i, j int) bool { return glyphs[i] < glyphs[j] })
	return pdfRenderedPage{
		Content:    p.buf.String(),
		Images:     append([]pdfReportImage(nil), p.images...),
		UsedGlyphs: glyphs,
	}
}

func (p *pdfPageWriter) drawImage(x, y, width, height float64, image pdfReportImage) {
	if len(image.Stream) == 0 || image.Width <= 0 || image.Height <= 0 {
		return
	}
	image.Name = "Im" + redteam.IntString(len(p.images)+1)
	p.images = append(p.images, image)
	fmt.Fprintf(&p.buf, "q\n%.1f 0 0 %.1f %.1f %.1f cm\n/%s Do\nQ\n", width, height, x, y, image.Name)
}

func pdfFindingImages(finding EvaluationReportFinding) []pdfReportImage {
	metadata := finding.Metadata
	if len(metadata) == 0 || strings.TrimSpace(metadata["payload_modality"]) != "text_image" {
		return nil
	}
	out := []pdfReportImage{}
	for index := 1; index <= 3; index++ {
		prefix := "report_image_" + redteam.IntString(index) + "_"
		raw := firstNonEmptyString(metadata[prefix+"base64"], metadata[prefix+"data_url"])
		if raw == "" {
			continue
		}
		image, err := pdfReportImageFromBase64(raw, metadata[prefix+"description"])
		if err != nil {
			continue
		}
		out = append(out, image)
	}
	return out
}

func pdfReportImageFromBase64(raw, description string) (pdfReportImage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return pdfReportImage{}, errors.New("image data is empty")
	}
	if comma := strings.Index(raw, ","); strings.HasPrefix(raw, "data:") && comma >= 0 {
		raw = raw[comma+1:]
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil {
		return pdfReportImage{}, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return pdfReportImage{}, err
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return pdfReportImage{}, errors.New("image has invalid dimensions")
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), img, bounds.Min, draw.Over)
	var rawRGB bytes.Buffer
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := canvas.At(x, y).RGBA()
			rawRGB.WriteByte(byte(r >> 8))
			rawRGB.WriteByte(byte(g >> 8))
			rawRGB.WriteByte(byte(b >> 8))
		}
	}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(rawRGB.Bytes()); err != nil {
		_ = zw.Close()
		return pdfReportImage{}, err
	}
	if err := zw.Close(); err != nil {
		return pdfReportImage{}, err
	}
	return pdfReportImage{
		Width:       width,
		Height:      height,
		Stream:      compressed.Bytes(),
		Description: strings.TrimSpace(description),
	}, nil
}

func pdfImageDisplaySize(image pdfReportImage, maxWidth, maxHeight float64) (float64, float64) {
	width := float64(image.Width)
	height := float64(image.Height)
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	scale := maxWidth / width
	if hScale := maxHeight / height; hScale < scale {
		scale = hScale
	}
	if scale > 1 {
		scale = 1
	}
	return width * scale, height * scale
}

func pdfImageObject(image pdfReportImage) string {
	return fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", image.Width, image.Height, len(image.Stream), string(image.Stream))
}

func cleanPDFText(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "###")
	value = strings.TrimPrefix(value, "##")
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "`")
	value = strings.ReplaceAll(value, "**", "")
	value = strings.ReplaceAll(value, "---", "")
	return strings.TrimSpace(value)
}

// wrapPDFText breaks text to a width, honouring the text's OWN line breaks.
//
// # Why "\n" is handled here and not by callers (2026-10-16)
//
// It used to be ignored: the loop charged every rune a width and appended it,
// so a newline travelled into the PDF text-showing operator as a rune. That is
// wrong twice over — it renders as a stray glyph, and `pdfRuneWidth` charges a
// non-ASCII rune a full em, so the line accounting is off as well.
//
// The report now carries payloads and answers, which are multi-line as a rule,
// and they reach the PDF by several routes (the per-probe cards, the successful-
// attacks section, the markdown export's lines). Fixing it here covers every
// route, including ones a caller-by-caller fix would miss — this was found by a
// test that only rendered section 4, after the per-probe card had already been
// "fixed".
func wrapPDFText(text string, maxWidth, size float64) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	lines := []string{}
	var current []rune
	currentWidth := 0.0
	flush := func() {
		// Trailing whitespace on a source line would otherwise be charged to the
		// next wrapped segment's width.
		line := strings.TrimRight(string(current), " \t")
		current = []rune{}
		currentWidth = 0
		if line == "" {
			return
		}
		lines = append(lines, line)
	}
	for _, r := range []rune(text) {
		if r == '\n' {
			flush()
			continue
		}
		w := pdfRuneWidth(r, size)
		if len(current) > 0 && currentWidth+w > maxWidth {
			flush()
			current = append(current, r)
			currentWidth = w
			continue
		}
		current = append(current, r)
		currentWidth += w
	}
	flush()
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// pdfRuneWidth is the advance of one rune at a given size, read from the embedded
// font's hmtx table.
//
// It replaced a hand-written table (`space=0.28em`, ASCII`=0.48em`, everything
// else one em). Those numbers were guesses, so line breaking and centred text
// were laid out against a width the glyph did not have — invisible with a
// monospaced-looking CJK font, wrong for the Latin runs inside Chinese sentences
// (plugin ids, `sk-…`, URLs) that the reports are full of.
// helveticaWidths are the Adobe StandardEncoding advance widths for the printable
// ASCII range (0x20–0x7E), in 1/1000 em.
//
// Latin text is drawn with Helvetica — one of the PDF standard 14, present in
// every reader, and the font the embedded CJK fallback face does NOT cover. Using
// its real metrics replaces the old flat `0.48em per ASCII rune`, which made
// English inside Chinese sentences break at the wrong column.
var helveticaWidths = [95]uint16{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556,
	278, 278, 584, 584, 584, 556, 1015,
	667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, 667,
	778, 722, 667, 611, 722, 667, 944, 667, 667, 611,
	278, 278, 278, 469, 556, 333,
	556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, 556,
	556, 333, 500, 278, 556, 500, 722, 500, 500, 500,
	334, 260, 334, 584,
}

// winAnsiExtra maps the code points WinAnsiEncoding carries above ASCII.
//
// # Why this exists (2026-10-19)
//
// The embedded CJK face has **no glyphs for Western punctuation**: `—` (U+2014),
// `·` (U+00B7), `…`, `•` and the curly quotes all resolved to glyph 0, which draws
// as .notdef — a blank. The structural snapshot test caught it in our own footer
// caption (`判定口径：… —— 分数口径…`), where the dashes silently disappeared.
//
// WinAnsi (PDF's Latin-1 + CP1252 superset) has every one of them, and Helvetica
// — already the ASCII font — is a standard-14 face every reader has. So those
// code points are routed to Helvetica with the rest of the Latin text.
var winAnsiExtra = map[rune]byte{
	0x20AC: 0x80, 0x201A: 0x82, 0x0192: 0x83, 0x201E: 0x84, 0x2026: 0x85,
	0x2020: 0x86, 0x2021: 0x87, 0x02C6: 0x88, 0x2030: 0x89, 0x0160: 0x8A,
	0x2039: 0x8B, 0x0152: 0x8C, 0x017D: 0x8E, 0x2018: 0x91, 0x2019: 0x92,
	0x201C: 0x93, 0x201D: 0x94, 0x2022: 0x95, 0x2013: 0x96, 0x2014: 0x97,
	0x02DC: 0x98, 0x2122: 0x99, 0x0161: 0x9A, 0x203A: 0x9B, 0x0153: 0x9C,
	0x017E: 0x9E, 0x0178: 0x9F,
}

// winAnsiByte returns the WinAnsiEncoding byte for a rune, or ok=false when the
// encoding has no code point for it (which is everything CJK).
func winAnsiByte(r rune) (byte, bool) {
	if r >= 0x20 && r <= 0x7e {
		return byte(r), true
	}
	if r >= 0xA0 && r <= 0xFF {
		return byte(r), true
	}
	if b, ok := winAnsiExtra[r]; ok {
		return b, true
	}
	return 0, false
}

// helveticaExtraWidths are the StandardEncoding advances (1/1000 em) for the
// WinAnsi punctuation above ASCII. Values are Helvetica's published metrics.
var helveticaExtraWidths = map[rune]uint16{
	0x2018: 222, 0x2019: 222, 0x201C: 333, 0x201D: 333,
	0x2013: 556, 0x2014: 1000, 0x2026: 1000, 0x2022: 350,
	0x00B7: 278, 0x20AC: 556, 0x2020: 556, 0x2021: 556,
	0x2122: 1000, 0x2030: 1000, 0x0160: 667, 0x0161: 500,
}

// pdfRuneWidth is the advance of one rune at a given size: Helvetica's metrics
// for everything WinAnsi can encode, the embedded font's hmtx for the rest.
func pdfRuneWidth(r rune, size float64) float64 {
	if _, ok := winAnsiByte(r); ok {
		if r >= 0x20 && r <= 0x7e {
			return float64(helveticaWidths[r-0x20]) * size / 1000
		}
		if width, ok := helveticaExtraWidths[r]; ok {
			return float64(width) * size / 1000
		}
		return size * 0.5
	}
	return reportPDFFont.advancePoints(r, size)
}

func splitPDFStatusTag(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	end := strings.LastIndex(line, "]")
	start := strings.LastIndex(line, "[")
	if start < 0 || end <= start || end != len(line)-1 {
		return line, "", false
	}
	return strings.TrimSpace(line[:start]), strings.TrimSpace(line[start+1 : end]), true
}

func pdfStatusColor(status string) (float64, float64, float64) {
	if findingLooksSuccessful(EvaluationReportFinding{Title: status}) {
		return 0.92, 0.30, 0.12
	}
	return 0.18, 0.62, 0.35
}

func isPDFSubheading(line string) bool {
	return strings.HasSuffix(line, "摘要") || strings.HasSuffix(line, "回答") || strings.HasSuffix(line, "建议")
}

func (p *pdfPageWriter) centerText(text string, size float64, r, g, b float64) {
	width := pdfTextWidth(text, size)
	x := (595 - width) / 2
	if x < 50 {
		x = 50
	}
	p.text(x, p.y, text, size, r, g, b)
}

// text writes one run of text with the embedded font.
//
// # One font, glyph ids, real widths (2026-10-19)
//
// It used to split the string into ASCII and non-ASCII segments, draw the ASCII
// half with Helvetica and the rest with a non-embedded STSong-Light, and advance
// the cursor with a hand-written per-rune width table. Browsers have no
// STSong-Light, so every Chinese character came out as garbage; the width table
// was an approximation that the real font makes unnecessary.
//
// Now every rune becomes its glyph id (Identity-H) in the one embedded font, and
// the cursor advances by that glyph's real advance width.
func (p *pdfPageWriter) text(x, y float64, text string, size float64, r, g, b float64) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	cursor := x
	for _, segment := range splitPDFTextSegments(text) {
		if segment.Text == "" {
			continue
		}
		fontName := "F1"
		encoded := reportPDFFont.encodeText(segment.Text)
		for _, rn := range []rune(segment.Text) {
			if p.usedGlyphs == nil {
				p.usedGlyphs = map[uint16]struct{}{}
			}
			p.usedGlyphs[reportPDFFont.GlyphID(rn)] = struct{}{}
		}
		if segment.ASCII {
			// Latin stays on Helvetica: it is one of the PDF standard 14, present
			// in every reader, needs no embedding, and its Latin metrics are what
			// the layout was tuned against. The embedded font is a CJK *fallback*
			// face with no Latin glyphs at all.
			fontName = "F2"
			encoded = pdfASCIIHex(segment.Text)
		}
		fmt.Fprintf(&p.buf, "BT\n/%s %.1f Tf\n%.2f %.2f %.2f rg\n%.1f %.1f Td\n<%s> Tj\nET\n", fontName, size, r, g, b, cursor, y, encoded)
		cursor += pdfTextWidth(segment.Text, size)
	}
}

func (p *pdfPageWriter) line(x1, y, x2 float64, r, g, b, width float64) {
	fmt.Fprintf(&p.buf, "%.2f %.2f %.2f RG\n%.1f w\n%.1f %.1f m %.1f %.1f l S\n", r, g, b, width, x1, y, x2, y)
}

type pdfTextSegment struct {
	Text  string
	ASCII bool
}

func splitPDFTextSegments(value string) []pdfTextSegment {
	segments := []pdfTextSegment{}
	var buf []rune
	currentASCII := false
	hasCurrent := false
	flush := func() {
		if len(buf) == 0 {
			return
		}
		segments = append(segments, pdfTextSegment{Text: string(buf), ASCII: currentASCII})
		buf = nil
	}
	for _, r := range []rune(value) {
		_, ascii := winAnsiByte(r)
		if hasCurrent && ascii != currentASCII {
			flush()
		}
		currentASCII = ascii
		hasCurrent = true
		buf = append(buf, r)
	}
	flush()
	return segments
}

func pdfTextWidth(value string, size float64) float64 {
	width := 0.0
	for _, r := range []rune(value) {
		width += pdfRuneWidth(r, size)
	}
	return width
}

// pdfASCIIHex writes WinAnsi bytes. Characters the encoding cannot represent are
// dropped rather than emitted as a bogus byte — the segment splitter puts those
// on the embedded font, so reaching this with one would be a mapping bug, not
// something to paper over.
func pdfASCIIHex(value string) string {
	var buf bytes.Buffer
	for _, r := range []rune(value) {
		b, ok := winAnsiByte(r)
		if !ok {
			continue
		}
		fmt.Fprintf(&buf, "%02X", b)
	}
	return buf.String()
}

func writePDFObjects(objects []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, 0, len(objects))
	for i, object := range objects {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefStart)
	return buf.Bytes()
}

func safeReportFilename(reportID, ext string) string {
	reportID = strings.TrimSpace(reportID)
	if reportID == "" {
		reportID = "redteam_report"
	}
	reportID = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, reportID)
	return reportID + "." + ext
}

func mustMarshalJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return []byte(`{}`)
	}
	return data
}
