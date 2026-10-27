package maclaw

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"evaluating_platform/internal/maclaw/redteam"
	"github.com/google/uuid"
)

const (
	pdfContentBottomY     = 310.0
	pdfFooterLineY        = 44.0
	pdfFooterCaptionTextY = 34.0
	pdfFooterTextY        = 26.0
	// pdfFooterFontSize 是页脚两行共用的字号（截断计算与渲染必须同值，
	// 否则会算出「按8pt 放得下」但实际用别的字号渲染的错位）。
	pdfFooterFontSize = 8.0
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
	// Metadata 是 judge_track 的**唯一真源**（DD-5=A：复用同一 schema，不新增列）。
	//
	// 刻意不给本record 加 JudgeTrack 字段：那会造出一个「内存里有、DB 里没存」
	// 的幽灵字段 —— 仓库层的 INSERT/SELECT 都不含它，一旦有人直接读record.JudgeTrack
	// 就会拿到零值并误判为「未声明」。判定口径一律经由 Metadata["judge_track"] 读写，
	// 对外暴露为 EvaluationReport.JudgeTrack（由 reportFromRecord 解析后回填）。
	Metadata  map[string]string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type RedteamArtifactStore interface {
	SaveEvidence(context.Context, RedteamEvidenceRecord) (*RedteamEvidenceRecord, error)
	ListEvidence(context.Context, uuid.UUID, EvaluationEvidenceQuery) ([]RedteamEvidenceRecord, error)
	GetEvidence(context.Context, uuid.UUID, string) (*RedteamEvidenceRecord, error)
	SaveReport(context.Context, RedteamReportRecord) (*RedteamReportRecord, error)
	GetReport(context.Context, uuid.UUID, string) (*RedteamReportRecord, error)
	// GetReportByRunID finds the newest report compiled for one engine run.
	//
	// The run detail lists runs, not reports; without this the one-click download
	// would need the browser to know a report id it never receives (2026-10-27).
	// Newest wins because recompiling a run overwrites its report in place today,
	// and a later re-compile should never hand back an older snapshot.
	GetReportByRunID(context.Context, uuid.UUID, string) (*RedteamReportRecord, error)
}

type RedteamArtifactService struct {
	store        RedteamArtifactStore
	now          func() time.Time
	handleSalt   string
	mu           sync.Mutex
	reportImages map[string]redteamReportImageAttachment
}

type redteamReportImageAttachment struct {
	ByFindingID map[string][]RedteamPayloadImage
	ExpiresAt   time.Time
}

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
		Title:          sanitizeRedteamEvidenceText(in.Title),
		Summary:        sanitizeRedteamEvidenceText(in.Summary),
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
	// 降级放行：DB 里个别证据行的 JSONB 损坏时，records 仍可用，
	// 不应让整个证据列表 500（见 data_corruption.go）。
	if err != nil && !IsPartialCorruption(err) {
		return nil, err
	}
	out := make([]EvaluationEvidenceSummary, 0, len(records))
	for i := range records {
		out = append(out, evidenceSummaryFromRecord(&records[i]))
	}
	return out, err
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
	// U4fail-closed：显式声明的 judge_track 非法时**拒绝写报告**。
	// 静默接受非法口径 = 把一个没人能解释的分数口径写进报告并让它流通，
	// 这比直接报错危险得多（用户看不出异常，只会当成正常结论）。
	judgeTrack, err := ValidateReportJudgeTrackMetadata(metadata)
	if err != nil {
		return nil, err
	}
	if judgeTrack == "" {
		// 未显式声明时按 metadata.engine 落库，保证新报告一律带口径，
		// 读路径就不必再靠推断（推断逻辑保留给历史行）。
		judgeTrack = RedteamJudgeTrackOf(metadata)
	}
	metadata[RedteamJudgeTrackMetadataKey] = string(judgeTrack)
	// U8 fail-closed：judge_source 非法时同样**拒绝写报告**。
	// 它是一个可枚举的溯源标识（档位前缀 + 已知族名/插件名），不在枚举内说明
	// 写报告的一侧算错了族或数据被改过；静默接受等于把一个没人能解释的
	// 「按什么标准判」写进报告。注意这**不覆盖**上面的 judge_track：
	// 两者正交，判定链路与判定标准各自独立校验。
	// 「未声明」（历史报告 / 平台链路）一律放行，不推断——
	// U8 之前只有一条 rubric，但那是不是「通用档」取决于当时的代码版本，
	// 猜出来的答案不可信，宁可显示「未记录」。
	if _, err := ValidateReportJudgeSourceMetadata(metadata); err != nil {
		return nil, err
	}
	// P2-6 fail-closed：生成路径是固定枚举，非法值同样**拒绝写报告** —— 一个没人
	// 能解释的「哪条链路生成的」会被当作解释报告数字的注脚发布出去。
	// 与 judge_track 的区别是它**不需要推断**：缺键就是「未记录」（P2-6 之前的
	// 报告天然如此），渲染层明说「未记录」而不是二选一。
	if err := ValidateReportGenerationMetadata(metadata); err != nil {
		return nil, err
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

// GetReportByRunID returns the newest report for one run, or nil when the run has
// not produced one (still running, failed, or from before reports were compiled).
func (s *RedteamArtifactService) GetReportByRunID(ctx context.Context, userID uuid.UUID, runID string) (*EvaluationReport, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	if userID == uuid.Nil {
		return nil, errors.New("platform user id is required")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	record, err := s.store.GetReportByRunID(ctx, userID, runID)
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
	metadata := redteam.SanitizeMetadata(record.Metadata)
	return EvaluationReport{
		ID:         record.ID,
		InstanceID: record.InstanceID,
		SessionID:  record.SessionID,
		RunID:      record.RunID,
		Title:      record.Title,
		Summary:    record.Summary,
		RiskLevel:  record.RiskLevel,
		// U4向后兼容：judge_track 由metadata 解析（旧行缺该键时按 engine 推断），
		// 因此**读**历史报告永远拿得到口径，且不需要数据回填。
		JudgeTrack:      RedteamJudgeTrackOf(metadata),
		SafetyScore:     record.SafetyScore,
		Findings:        append([]EvaluationReportFinding(nil), record.Findings...),
		EvidenceHandles: append([]string(nil), record.EvidenceHandles...),
		Metadata:        metadata,
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}
}
