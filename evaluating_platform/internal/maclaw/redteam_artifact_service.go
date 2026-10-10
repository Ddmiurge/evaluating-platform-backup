package maclaw

import (
	"bytes"
	"compress/zlib"
	"context"
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
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"evaluating_platform/internal/maclaw/redteam"
	"github.com/google/uuid"
)

const (
	pdfContentBottomY = 310.0
	pdfFooterLineY    = 44.0
	pdfFooterTextY    = 26.0
)

type RedteamEvidenceRecord struct {
	ID             string
	Handle         string
	PlatformUserID uuid.UUID
	InstanceID     string
	SessionID      string
	RunID          string
	Kind           EvaluationEvidenceKind
	Title          string
	Summary        string
	Metadata       map[string]string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type RedteamReportRecord struct {
	ID              string
	Handle          string
	PlatformUserID  uuid.UUID
	InstanceID      string
	SessionID       string
	RunID           string
	Title           string
	Summary         string
	RiskLevel       string
	SafetyScore     *float64
	Findings        []EvaluationReportFinding
	EvidenceHandles []string
	Metadata        map[string]string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type RedteamArtifactStore interface {
	SaveEvidence(context.Context, RedteamEvidenceRecord) (*RedteamEvidenceRecord, error)
	ListEvidence(context.Context, uuid.UUID, EvaluationEvidenceQuery) ([]RedteamEvidenceRecord, error)
	GetEvidence(context.Context, uuid.UUID, string) (*RedteamEvidenceRecord, error)
	SaveReport(context.Context, RedteamReportRecord) (*RedteamReportRecord, error)
	GetReport(context.Context, uuid.UUID, string) (*RedteamReportRecord, error)
}

type RedteamArtifactService struct {
	store        RedteamArtifactStore
	now          func() time.Time
	handleSalt   string
	mu           sync.Mutex
	reportImages map[string]redteamReportImageAttachment
}

const maxRedteamArtifactTextRunes = 2000

type redteamReportImageAttachment struct {
	ByFindingID map[string][]RedteamPayloadImage
	ExpiresAt   time.Time
}

var redteamArtifactSecretPattern = regexp.MustCompile(`(?i)(secret[-_a-z0-9]*|sk-[a-z0-9][a-z0-9_\-]{5,}|bearer\s+[a-z0-9._\-]+|api[_ -]?key\s*[:=]\s*\S+)`)

func NewRedteamArtifactService(store RedteamArtifactStore) *RedteamArtifactService {
	return &RedteamArtifactService{
		store:      store,
		now:        time.Now,
		handleSalt: "evaluating_platform_redteam_artifacts_v1",
	}
}

func (s *RedteamArtifactService) Enabled() bool {
	return s != nil && s.store != nil
}

func (s *RedteamArtifactService) SaveEvidence(ctx context.Context, userID uuid.UUID, instanceID string, in RedteamEvidenceInput) (*RedteamEvidenceOutput, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	if userID == uuid.Nil {
		return nil, errors.New("platform user id is required")
	}
	kind := in.Kind
	if kind == "" {
		kind = EvaluationEvidenceKindArtifact
	}
	now := s.nowUTC()
	handle := s.safeHandle("redteam_evidence", userID.String(), in.RunID, string(kind), in.Title, in.Summary)
	record := RedteamEvidenceRecord{
		ID:             handle,
		Handle:         handle,
		PlatformUserID: userID,
		InstanceID:     strings.TrimSpace(instanceID),
		RunID:          strings.TrimSpace(in.RunID),
		Kind:           kind,
		Title:          sanitizeRedteamArtifactText(in.Title),
		Summary:        sanitizeRedteamArtifactText(in.Summary),
		Metadata:       redteam.SanitizeMetadata(in.Metadata),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	saved, err := s.store.SaveEvidence(ctx, record)
	if err != nil {
		return nil, err
	}
	return evidenceOutputFromRecord(saved), nil
}

func (s *RedteamArtifactService) ListEvidence(ctx context.Context, userID uuid.UUID, q EvaluationEvidenceQuery) ([]EvaluationEvidenceSummary, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	if userID == uuid.Nil {
		return nil, errors.New("platform user id is required")
	}
	records, err := s.store.ListEvidence(ctx, userID, q)
	if err != nil {
		return nil, err
	}
	out := make([]EvaluationEvidenceSummary, 0, len(records))
	for i := range records {
		out = append(out, evidenceSummaryFromRecord(&records[i]))
	}
	return out, nil
}

func (s *RedteamArtifactService) GetEvidence(ctx context.Context, userID uuid.UUID, evidenceID string) (*EvaluationEvidenceSummary, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	if userID == uuid.Nil {
		return nil, errors.New("platform user id is required")
	}
	record, err := s.store.GetEvidence(ctx, userID, strings.TrimSpace(evidenceID))
	if err != nil || record == nil {
		return nil, err
	}
	out := evidenceSummaryFromRecord(record)
	return &out, nil
}

func (s *RedteamArtifactService) CompileReport(ctx context.Context, userID uuid.UUID, instanceID string, in CompileRedteamReportInput) (*EvaluationReport, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	if userID == uuid.Nil {
		return nil, errors.New("platform user id is required")
	}
	now := s.nowUTC()
	handle := s.safeHandle("redteam_report", userID.String(), in.RunID, in.Title, in.Summary)
	metadata := redteam.SanitizeMetadata(in.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["schema_version"] = "redteam_report_zh_v1"
	metadata["report_template"] = "redteam_report_pdf_layout_v2"
	record := RedteamReportRecord{
		ID:              handle,
		Handle:          handle,
		PlatformUserID:  userID,
		InstanceID:      strings.TrimSpace(instanceID),
		RunID:           strings.TrimSpace(in.RunID),
		Title:           firstNonEmptyString(sanitizeRedteamArtifactText(in.Title), "大模型安全评估报告"),
		Summary:         sanitizeRedteamArtifactText(in.Summary),
		RiskLevel:       sanitizeRedteamArtifactText(in.RiskLevel),
		SafetyScore:     in.SafetyScore,
		Findings:        sanitizeRedteamReportFindings(in.Findings),
		EvidenceHandles: append([]string(nil), in.EvidenceHandles...),
		Metadata:        metadata,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	saved, err := s.store.SaveReport(ctx, record)
	if err != nil {
		return nil, err
	}
	s.rememberReportImages(saved.ID, in.ReportImages)
	report := reportFromRecord(saved)
	return &report, nil
}

func sanitizeRedteamReportFindings(in []EvaluationReportFinding) []EvaluationReportFinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]EvaluationReportFinding, 0, len(in))
	for _, item := range in {
		item.ID = sanitizeRedteamArtifactText(item.ID)
		item.Title = sanitizeRedteamArtifactText(item.Title)
		item.Severity = sanitizeRedteamArtifactText(item.Severity)
		item.Category = sanitizeRedteamArtifactText(item.Category)
		item.Description = sanitizeRedteamArtifactText(item.Description)
		item.Evidence = sanitizeRedteamArtifactText(item.Evidence)
		item.Suggestion = sanitizeRedteamArtifactText(item.Suggestion)
		item.Metadata = redteam.SanitizeMetadata(item.Metadata)
		out = append(out, item)
	}
	return out
}

func sanitizeRedteamArtifactText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"raw payload",
		"payload body",
		"raw prompt",
		"prompt body",
		"raw response",
		"target response body",
		"local path",
		"evidence content",
	} {
		if strings.Contains(lower, marker) {
			return "[redacted sensitive text]"
		}
	}
	value = redteamArtifactSecretPattern.ReplaceAllString(value, "[redacted]")
	runes := []rune(value)
	if len(runes) > maxRedteamArtifactTextRunes {
		return string(runes[:maxRedteamArtifactTextRunes]) + "..."
	}
	return value
}

func (s *RedteamArtifactService) GetReport(ctx context.Context, userID uuid.UUID, reportID string) (*EvaluationReport, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	if userID == uuid.Nil {
		return nil, errors.New("platform user id is required")
	}
	record, err := s.store.GetReport(ctx, userID, strings.TrimSpace(reportID))
	if err != nil || record == nil {
		return nil, err
	}
	report := reportFromRecord(record)
	return &report, nil
}

func (s *RedteamArtifactService) ExportReport(ctx context.Context, userID uuid.UUID, reportID, format string) (*EvaluationReportExport, error) {
	report, err := s.GetReport(ctx, userID, reportID)
	if err != nil || report == nil {
		return nil, err
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "pdf"
	}
	switch format {
	case "pdf":
		pdfReport := s.reportWithPDFImages(report)
		content := renderReportPDF(pdfReport)
		return &EvaluationReportExport{
			ReportID:    report.ID,
			Format:      "pdf",
			Filename:    safeReportFilename(report.ID, "pdf"),
			ContentType: "application/pdf",
			Content:     content,
		}, nil
	case "md", "markdown":
		content := renderReportMarkdown(report)
		return &EvaluationReportExport{
			ReportID:    report.ID,
			Format:      "markdown",
			Filename:    safeReportFilename(report.ID, "md"),
			ContentType: "text/markdown; charset=utf-8",
			Content:     content,
		}, nil
	case "json":
		data := mustMarshalJSON(report)
		return &EvaluationReportExport{
			ReportID:    report.ID,
			Format:      "json",
			Filename:    safeReportFilename(report.ID, "json"),
			ContentType: "application/json; charset=utf-8",
			Content:     data,
		}, nil
	default:
		return nil, errors.New("unsupported report export format")
	}
}

func (s *RedteamArtifactService) rememberReportImages(reportID string, images map[string][]RedteamPayloadImage) {
	reportID = strings.TrimSpace(reportID)
	if s == nil || reportID == "" || len(images) == 0 {
		return
	}
	byFinding := map[string][]RedteamPayloadImage{}
	for findingID, items := range images {
		findingID = strings.TrimSpace(findingID)
		if findingID == "" {
			continue
		}
		cloned := cloneRedteamPayloadImages(items)
		if len(cloned) == 0 {
			continue
		}
		byFinding[findingID] = cloned
	}
	if len(byFinding) == 0 {
		return
	}
	now := s.nowUTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reportImages == nil {
		s.reportImages = map[string]redteamReportImageAttachment{}
	}
	s.cleanupReportImagesLocked(now)
	s.reportImages[reportID] = redteamReportImageAttachment{
		ByFindingID: byFinding,
		ExpiresAt:   now.Add(redteamPayloadHandleTTL),
	}
}

func (s *RedteamArtifactService) reportWithPDFImages(report *EvaluationReport) *EvaluationReport {
	if s == nil || report == nil || strings.TrimSpace(report.ID) == "" {
		return report
	}
	now := s.nowUTC()
	s.mu.Lock()
	attachment, ok := s.reportImages[strings.TrimSpace(report.ID)]
	if ok && !attachment.ExpiresAt.IsZero() && !now.Before(attachment.ExpiresAt) {
		delete(s.reportImages, strings.TrimSpace(report.ID))
		ok = false
	}
	s.mu.Unlock()
	if !ok || len(attachment.ByFindingID) == 0 {
		return report
	}
	copied := *report
	copied.Findings = append([]EvaluationReportFinding(nil), report.Findings...)
	for idx := range copied.Findings {
		findingID := strings.TrimSpace(copied.Findings[idx].ID)
		images := attachment.ByFindingID[findingID]
		if len(images) == 0 {
			continue
		}
		metadata := cloneMetadata(copied.Findings[idx].Metadata)
		if metadata == nil {
			metadata = map[string]string{}
		}
		redteam.MergeStringMetadata(metadata, reportImageMetadataForPayload(images))
		copied.Findings[idx].Metadata = metadata
	}
	return &copied
}

func (s *RedteamArtifactService) cleanupReportImagesLocked(now time.Time) {
	if s == nil || len(s.reportImages) == 0 {
		return
	}
	for reportID, attachment := range s.reportImages {
		if !attachment.ExpiresAt.IsZero() && !now.Before(attachment.ExpiresAt) {
			delete(s.reportImages, reportID)
		}
	}
}

func (s *RedteamArtifactService) safeHandle(parts ...string) string {
	return safeHandleWithSalt(s.handleSalt, s.nowUTC(), parts)
}

func (s *RedteamArtifactService) nowUTC() time.Time {
	return utcNow(s.now)
}

type PlatformArtifactGateway struct {
	GatewayClient
	artifacts *RedteamArtifactService
	userID    uuid.UUID
}

func NewPlatformArtifactGateway(upstream GatewayClient, artifacts *RedteamArtifactService, userID uuid.UUID) GatewayClient {
	return &PlatformArtifactGateway{GatewayClient: upstream, artifacts: artifacts, userID: userID}
}

func (g *PlatformArtifactGateway) GetEvaluationReport(ctx context.Context, reportID string) (*EvaluationReport, error) {
	if g != nil && g.artifacts != nil && g.artifacts.Enabled() {
		report, err := g.artifacts.GetReport(ctx, g.userID, reportID)
		if err != nil || report != nil {
			return report, err
		}
	}
	return g.GatewayClient.GetEvaluationReport(ctx, reportID)
}

func (g *PlatformArtifactGateway) ExportEvaluationReport(ctx context.Context, reportID, format string) (*EvaluationReportExport, error) {
	if g != nil && g.artifacts != nil && g.artifacts.Enabled() {
		out, err := g.artifacts.ExportReport(ctx, g.userID, reportID, format)
		if err != nil || out != nil {
			return out, err
		}
	}
	return g.GatewayClient.ExportEvaluationReport(ctx, reportID, format)
}

func (g *PlatformArtifactGateway) ListEvaluationEvidence(ctx context.Context, q EvaluationEvidenceQuery) ([]EvaluationEvidenceSummary, error) {
	if g != nil && g.artifacts != nil && g.artifacts.Enabled() {
		return g.artifacts.ListEvidence(ctx, g.userID, q)
	}
	return g.GatewayClient.ListEvaluationEvidence(ctx, q)
}

func (g *PlatformArtifactGateway) GetEvaluationEvidence(ctx context.Context, evidenceID string) (*EvaluationEvidenceSummary, error) {
	if g != nil && g.artifacts != nil && g.artifacts.Enabled() {
		out, err := g.artifacts.GetEvidence(ctx, g.userID, evidenceID)
		if err != nil || out != nil {
			return out, err
		}
	}
	return g.GatewayClient.GetEvaluationEvidence(ctx, evidenceID)
}

func evidenceOutputFromRecord(record *RedteamEvidenceRecord) *RedteamEvidenceOutput {
	if record == nil {
		return nil
	}
	return &RedteamEvidenceOutput{
		Handle:   record.Handle,
		Kind:     record.Kind,
		Title:    record.Title,
		Summary:  record.Summary,
		Metadata: redteam.SanitizeMetadata(record.Metadata),
	}
}

func evidenceSummaryFromRecord(record *RedteamEvidenceRecord) EvaluationEvidenceSummary {
	if record == nil {
		return EvaluationEvidenceSummary{}
	}
	return EvaluationEvidenceSummary{
		ID:         record.ID,
		InstanceID: record.InstanceID,
		SessionID:  record.SessionID,
		RunID:      record.RunID,
		Kind:       record.Kind,
		Title:      record.Title,
		Summary:    record.Summary,
		Handle:     record.Handle,
		Metadata:   redteam.SanitizeMetadata(record.Metadata),
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  record.UpdatedAt,
	}
}

func reportFromRecord(record *RedteamReportRecord) EvaluationReport {
	if record == nil {
		return EvaluationReport{}
	}
	return EvaluationReport{
		ID:              record.ID,
		InstanceID:      record.InstanceID,
		SessionID:       record.SessionID,
		RunID:           record.RunID,
		Title:           record.Title,
		Summary:         record.Summary,
		RiskLevel:       record.RiskLevel,
		SafetyScore:     record.SafetyScore,
		Findings:        append([]EvaluationReportFinding(nil), record.Findings...),
		EvidenceHandles: append([]string(nil), record.EvidenceHandles...),
		Metadata:        redteam.SanitizeMetadata(record.Metadata),
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}
}

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
		if title := strings.TrimSpace(finding.Title); title != "" {
			lines = append(lines, "   - 测试问题："+title)
		}
		if original := findingOriginalQuestionSummary(finding); original != "" {
			lines = append(lines, "   - 原样本问题："+original)
		}
		if strings.TrimSpace(finding.Severity) != "" {
			lines = append(lines, "   - 严重程度："+strings.TrimSpace(finding.Severity))
		}
		if strings.TrimSpace(finding.Category) != "" {
			lines = append(lines, "   - 类型："+strings.TrimSpace(finding.Category))
		}
		if context := findingCapabilityContextSummary(finding); context != "" {
			lines = append(lines, "   - "+context)
		}
		if strings.TrimSpace(finding.Description) != "" {
			lines = append(lines, "   - 模型回答："+strings.TrimSpace(finding.Description))
		}
		if strings.TrimSpace(finding.Suggestion) != "" {
			lines = append(lines, "   - 建议："+strings.TrimSpace(finding.Suggestion))
		}
	}
	return lines
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
		if evidence := strings.TrimSpace(finding.Evidence); evidence != "" {
			lines = append(lines, "  - 判定理由："+evidence)
		} else {
			lines = append(lines, "  - 判定理由：模型回答在客观上满足了攻击请求。")
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
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"",
		"<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H /DescendantFonts [4 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 5 >> >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
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
		objects[pageObjectID-1] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R /F2 5 0 R >>%s >> /Contents %d 0 R >>", xobjects, contentObjectIDs[i])
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
}

type pdfRenderedPage struct {
	Content string
	Images  []pdfReportImage
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
	page := newPDFBodyPage()
	appendPage := func() *pdfPageWriter {
		page.footer()
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
		page.footer()
		pages = append(pages, page.rendered())
	}
	if len(pages) == 0 {
		fallback := newPDFBodyPage()
		fallback.sectionHeading("1. 评估摘要")
		fallback.y -= 26
		fallback.text(70, fallback.y, "暂无报告内容。", 10.5, 0.16, 0.16, 0.16)
		fallback.footer()
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
	lines := []string{
		"测试问题：" + firstNonEmptyString(finding.Title, "未提供测试问题"),
	}
	if original := findingOriginalQuestionSummary(finding); original != "" {
		lines = append(lines, "原样本问题："+original)
	}
	for _, line := range lines {
		p = p.findingWrappedLine(index, line, appendPage)
	}
	for _, image := range pdfFindingImages(finding) {
		p = p.findingWrappedLine(index, "测试图片："+firstNonEmptyString(image.Description, "最终发送给被测模型的图片"), appendPage)
		p = p.findingImage(index, image, appendPage)
	}
	for _, line := range []string{
		"模型回答：" + firstNonEmptyString(finding.Description, finding.Evidence, "未提供模型回答"),
	} {
		p = p.findingWrappedLine(index, line, appendPage)
	}
	if suggestion := strings.TrimSpace(finding.Suggestion); suggestion != "" {
		p = p.findingWrappedLine(index, "风险说明/建议："+suggestion, appendPage)
	}
	p.y -= 14
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

func (p *pdfPageWriter) footer() {
	p.line(70, pdfFooterLineY, 525, 0.86, 0.88, 0.92, 0.35)
	p.text(70, pdfFooterTextY, "本报告由安全评估平台生成，仅展示安全摘要与结构化结论。", 8.0, 0.45, 0.48, 0.52)
}

func (p *pdfPageWriter) rendered() pdfRenderedPage {
	return pdfRenderedPage{
		Content: p.buf.String(),
		Images:  append([]pdfReportImage(nil), p.images...),
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

func wrapPDFText(text string, maxWidth, size float64) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	lines := []string{}
	var current []rune
	currentWidth := 0.0
	for _, r := range []rune(text) {
		w := pdfRuneWidth(r, size)
		if len(current) > 0 && currentWidth+w > maxWidth {
			lines = append(lines, string(current))
			current = []rune{}
			currentWidth = 0
		}
		current = append(current, r)
		currentWidth += w
	}
	if len(current) > 0 {
		lines = append(lines, string(current))
	}
	return lines
}

func pdfRuneWidth(r rune, size float64) float64 {
	switch {
	case r == ' ':
		return size * 0.28
	case r <= 0x7f:
		return size * 0.48
	default:
		return size
	}
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
		font := "F1"
		encoded := pdfUTF16Hex(segment.Text)
		if segment.ASCII {
			font = "F2"
			encoded = pdfASCIIHex(segment.Text)
		}
		fmt.Fprintf(&p.buf, "BT\n/%s %.1f Tf\n%.2f %.2f %.2f rg\n%.1f %.1f Td\n<%s> Tj\nET\n", font, size, r, g, b, cursor, y, encoded)
		cursor += pdfTextWidth(segment.Text, size)
	}
}

func (p *pdfPageWriter) line(x1, y, x2 float64, r, g, b, width float64) {
	fmt.Fprintf(&p.buf, "%.2f %.2f %.2f RG\n%.1f w\n%.1f %.1f m %.1f %.1f l S\n", r, g, b, width, x1, y, x2, y)
}

func pdfUTF16Hex(value string) string {
	var buf bytes.Buffer
	for _, code := range utf16.Encode([]rune(value)) {
		fmt.Fprintf(&buf, "%04X", code)
	}
	return buf.String()
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
		ascii := r >= 0x20 && r <= 0x7e
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

func pdfASCIIHex(value string) string {
	var buf bytes.Buffer
	for _, b := range []byte(value) {
		if b < 0x20 || b > 0x7e {
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
