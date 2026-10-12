package maclaw

// client_evaluation.go —— Evaluation 服务域（T4.2 client.go 分层）。
//
// 承载评测链路的 DTO 与网关方法：资源（resource）/ 目标（target）/
// 运行与作业（run & job）/ 报告（report）/ 证据（evidence）。
// 传输层原语见 transport.go；Runtime 与 Config 域见 client_runtime.go。
//
// 由 U3-b T4.2 从原 client.go 物理拆分而来：仅搬运，未改逻辑或签名。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type EvaluationResourceKind string

const (
	EvaluationResourceKindSample         EvaluationResourceKind = "sample"
	EvaluationResourceKindTemplate       EvaluationResourceKind = "template"
	EvaluationResourceKindComposedAttack EvaluationResourceKind = "composed_attack"
	EvaluationResourceKindGeneratorSkill EvaluationResourceKind = "generator_skill"
)

type EvaluationResourceStatus string

const (
	EvaluationResourceStatusDraft     EvaluationResourceStatus = "draft"
	EvaluationResourceStatusPublished EvaluationResourceStatus = "published"
	EvaluationResourceStatusArchived  EvaluationResourceStatus = "archived"
)

type EvaluationResourceHealthStatus string

const (
	EvaluationResourceHealthUnknown     EvaluationResourceHealthStatus = "unknown"
	EvaluationResourceHealthHealthy     EvaluationResourceHealthStatus = "healthy"
	EvaluationResourceHealthDegraded    EvaluationResourceHealthStatus = "degraded"
	EvaluationResourceHealthUnavailable EvaluationResourceHealthStatus = "unavailable"
)

type EvaluationResourceInput struct {
	ID              string                         `json:"id,omitempty"`
	Name            string                         `json:"name"`
	Description     string                         `json:"description,omitempty"`
	Kind            EvaluationResourceKind         `json:"kind"`
	Version         string                         `json:"version,omitempty"`
	Status          EvaluationResourceStatus       `json:"status,omitempty"`
	Enabled         bool                           `json:"enabled"`
	HealthStatus    EvaluationResourceHealthStatus `json:"health_status,omitempty"`
	AssessmentTypes []string                       `json:"assessment_types,omitempty"`
	Tags            []string                       `json:"tags,omitempty"`
	Summary         string                         `json:"summary,omitempty"`
	Payload         string                         `json:"payload,omitempty"`
	Metadata        map[string]string              `json:"metadata,omitempty"`
}

type EvaluationResourceQuery struct {
	Kind            EvaluationResourceKind
	AssessmentTypes []string
	Query           string
	IncludeInactive bool
	Limit           int
}

type EvaluationResourceSummary struct {
	ID              string                         `json:"id"`
	Handle          string                         `json:"handle"`
	Name            string                         `json:"name"`
	Description     string                         `json:"description,omitempty"`
	Kind            EvaluationResourceKind         `json:"kind"`
	Version         string                         `json:"version,omitempty"`
	Status          EvaluationResourceStatus       `json:"status"`
	Enabled         bool                           `json:"enabled"`
	HealthStatus    EvaluationResourceHealthStatus `json:"health_status"`
	AssessmentTypes []string                       `json:"assessment_types,omitempty"`
	Tags            []string                       `json:"tags,omitempty"`
	Summary         string                         `json:"summary,omitempty"`
	Metadata        map[string]string              `json:"metadata,omitempty"`
	CreatedAt       time.Time                      `json:"created_at"`
	UpdatedAt       time.Time                      `json:"updated_at"`
}

type EvaluationResourceMaterialization struct {
	ResourceID string                 `json:"resource_id"`
	Handle     string                 `json:"handle"`
	Name       string                 `json:"name"`
	Kind       EvaluationResourceKind `json:"kind"`
	Version    string                 `json:"version,omitempty"`
	Payload    string                 `json:"payload"`
	Metadata   map[string]string      `json:"metadata,omitempty"`
}

type EvaluationResourcePreview struct {
	Resource         EvaluationResourceSummary `json:"resource"`
	PayloadBytes     int                       `json:"payload_bytes"`
	PayloadLineCount int                       `json:"payload_line_count"`
	PayloadSHA256    string                    `json:"payload_sha256,omitempty"`
}

type EvaluationTargetKind string

const (
	EvaluationTargetKindLLM   EvaluationTargetKind = "llm"
	EvaluationTargetKindHTTP  EvaluationTargetKind = "http"
	EvaluationTargetKindAgent EvaluationTargetKind = "agent"
)

type EvaluationTargetAuthType string

const (
	EvaluationTargetAuthTypeNone   EvaluationTargetAuthType = "none"
	EvaluationTargetAuthTypeBearer EvaluationTargetAuthType = "bearer"
	EvaluationTargetAuthTypeAPIKey EvaluationTargetAuthType = "api_key"
)

type EvaluationTargetStatus string

const (
	EvaluationTargetStatusDraft     EvaluationTargetStatus = "draft"
	EvaluationTargetStatusPublished EvaluationTargetStatus = "published"
	EvaluationTargetStatusArchived  EvaluationTargetStatus = "archived"
)

type EvaluationTargetHealthStatus string

const (
	EvaluationTargetHealthUnknown     EvaluationTargetHealthStatus = "unknown"
	EvaluationTargetHealthHealthy     EvaluationTargetHealthStatus = "healthy"
	EvaluationTargetHealthDegraded    EvaluationTargetHealthStatus = "degraded"
	EvaluationTargetHealthUnavailable EvaluationTargetHealthStatus = "unavailable"
)

type EvaluationTargetInput struct {
	ID               string                       `json:"id,omitempty"`
	Name             string                       `json:"name"`
	Description      string                       `json:"description,omitempty"`
	Kind             EvaluationTargetKind         `json:"kind"`
	Provider         string                       `json:"provider,omitempty"`
	BaseURL          string                       `json:"base_url,omitempty"`
	Model            string                       `json:"model,omitempty"`
	AuthType         EvaluationTargetAuthType     `json:"auth_type,omitempty"`
	CredentialSecret string                       `json:"credential_secret,omitempty"`
	Status           EvaluationTargetStatus       `json:"status,omitempty"`
	Enabled          bool                         `json:"enabled"`
	HealthStatus     EvaluationTargetHealthStatus `json:"health_status,omitempty"`
	Tags             []string                     `json:"tags,omitempty"`
	Metadata         map[string]string            `json:"metadata,omitempty"`
}

type EvaluationTargetQuery struct {
	Kind            EvaluationTargetKind
	Provider        string
	Query           string
	IncludeInactive bool
	Limit           int
}

type EvaluationTargetSummary struct {
	ID                  string                       `json:"id"`
	Name                string                       `json:"name"`
	Description         string                       `json:"description,omitempty"`
	Kind                EvaluationTargetKind         `json:"kind"`
	Provider            string                       `json:"provider,omitempty"`
	BaseURL             string                       `json:"base_url,omitempty"`
	Model               string                       `json:"model,omitempty"`
	AuthType            EvaluationTargetAuthType     `json:"auth_type"`
	CredentialSecretSet bool                         `json:"credential_secret_set"`
	Status              EvaluationTargetStatus       `json:"status"`
	Enabled             bool                         `json:"enabled"`
	HealthStatus        EvaluationTargetHealthStatus `json:"health_status"`
	Tags                []string                     `json:"tags,omitempty"`
	Metadata            map[string]string            `json:"metadata,omitempty"`
	CreatedAt           time.Time                    `json:"created_at"`
	UpdatedAt           time.Time                    `json:"updated_at"`
}

type EvaluationTargetProbeResult struct {
	Target     EvaluationTargetSummary      `json:"target"`
	Status     EvaluationTargetHealthStatus `json:"status"`
	Message    string                       `json:"message,omitempty"`
	StatusCode int                          `json:"status_code,omitempty"`
	CheckedAt  time.Time                    `json:"checked_at,omitempty"`
}

type EvaluationRunInput struct {
	InstanceID      string            `json:"instance_id"`
	SessionID       string            `json:"session_id"`
	Goal            string            `json:"goal,omitempty"`
	TargetID        string            `json:"target_id"`
	ResourceHandles []string          `json:"resource_handles,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type EvaluationRunResult struct {
	Run      *RuntimeRun                `json:"run,omitempty"`
	Evidence *EvaluationEvidenceSummary `json:"evidence,omitempty"`
	Report   *EvaluationReport          `json:"report,omitempty"`
}

type EvaluationJobKind string

const (
	EvaluationJobKindRun EvaluationJobKind = "evaluation.run"
)

type EvaluationJobStatus string

const (
	EvaluationJobStatusPending   EvaluationJobStatus = "pending"
	EvaluationJobStatusRunning   EvaluationJobStatus = "running"
	EvaluationJobStatusSucceeded EvaluationJobStatus = "succeeded"
	EvaluationJobStatusFailed    EvaluationJobStatus = "failed"
	EvaluationJobStatusCanceled  EvaluationJobStatus = "canceled"
)

type EvaluationJobQuery struct {
	Kind      EvaluationJobKind
	Status    EvaluationJobStatus
	SessionID string
	Limit     int
}

type EvaluationJobProgress struct {
	Phase              string                      `json:"phase,omitempty"`
	Step               string                      `json:"step,omitempty"`
	StepStatus         string                      `json:"step_status,omitempty"`
	RetryableOnRestart bool                        `json:"retryable_on_restart,omitempty"`
	Steps              []EvaluationJobStepProgress `json:"steps,omitempty"`
	RestartInterrupted bool                        `json:"restart_interrupted,omitempty"`
	RecoveryStrategy   string                      `json:"recovery_strategy,omitempty"`
	RecoveryAction     string                      `json:"recovery_action,omitempty"`
	RunID              string                      `json:"run_id,omitempty"`
	InstanceID         string                      `json:"instance_id,omitempty"`
	SessionID          string                      `json:"session_id,omitempty"`
	UserMessageID      string                      `json:"user_message_id,omitempty"`
	AssistantMessageID string                      `json:"assistant_message_id,omitempty"`
	StatusText         string                      `json:"status_text,omitempty"`
	PlannedCount       int                         `json:"planned_count,omitempty"`
	ExecutedCount      int                         `json:"executed_count,omitempty"`
	CurrentStage       string                      `json:"current_stage,omitempty"`
	DurationMs         int64                       `json:"duration_ms,omitempty"`
	StageDurationsJSON string                      `json:"stage_durations_json,omitempty"`
	UpdatedAt          time.Time                   `json:"updated_at,omitempty"`
}

type EvaluationJobStepProgress struct {
	Step               string     `json:"step"`
	Status             string     `json:"status"`
	RetryableOnRestart bool       `json:"retryable_on_restart,omitempty"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type EvaluationJobRecovery struct {
	JobID                  string   `json:"job_id"`
	RunID                  string   `json:"run_id,omitempty"`
	Action                 string   `json:"action,omitempty"`
	Strategy               string   `json:"strategy,omitempty"`
	Step                   string   `json:"step,omitempty"`
	CompletedStepIDs       []string `json:"completed_step_ids,omitempty"`
	CanResume              bool     `json:"can_resume,omitempty"`
	ResumeStep             string   `json:"resume_step,omitempty"`
	ResumeJobEndpoint      string   `json:"resume_job_endpoint,omitempty"`
	CanRetry               bool     `json:"can_retry,omitempty"`
	ManualReviewRequired   bool     `json:"manual_review_required,omitempty"`
	Reason                 string   `json:"reason,omitempty"`
	RecoveryIndexed        bool     `json:"recovery_indexed,omitempty"`
	ReplacementJobEndpoint string   `json:"replacement_job_endpoint,omitempty"`
}

type EvaluationJob struct {
	ID          string                 `json:"id"`
	Kind        EvaluationJobKind      `json:"kind"`
	Status      EvaluationJobStatus    `json:"status"`
	TenantID    string                 `json:"tenant_id,omitempty"`
	UserID      string                 `json:"user_id,omitempty"`
	Progress    *EvaluationJobProgress `json:"progress,omitempty"`
	Result      *EvaluationRunResult   `json:"result,omitempty"`
	Error       string                 `json:"error,omitempty"`
	CreatedAt   time.Time              `json:"created_at,omitempty"`
	StartedAt   *time.Time             `json:"started_at,omitempty"`
	CompletedAt *time.Time             `json:"completed_at,omitempty"`
}

type EvaluationReportFinding struct {
	ID          string            `json:"id,omitempty"`
	Title       string            `json:"title"`
	Severity    string            `json:"severity,omitempty"`
	Category    string            `json:"category,omitempty"`
	Description string            `json:"description,omitempty"`
	Evidence    string            `json:"evidence,omitempty"`
	Suggestion  string            `json:"suggestion,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type EvaluationReport struct {
	ID              string                    `json:"id"`
	TenantID        string                    `json:"tenant_id,omitempty"`
	UserID          string                    `json:"user_id,omitempty"`
	InstanceID      string                    `json:"instance_id,omitempty"`
	SessionID       string                    `json:"session_id,omitempty"`
	RunID           string                    `json:"run_id,omitempty"`
	Title           string                    `json:"title"`
	Summary         string                    `json:"summary,omitempty"`
	RiskLevel       string                    `json:"risk_level,omitempty"`
	SafetyScore     *float64                  `json:"safety_score,omitempty"`
	JudgeTrack      RedteamJudgeTrack         `json:"judge_track,omitempty"`
	Findings        []EvaluationReportFinding `json:"findings,omitempty"`
	EvidenceHandles []string                  `json:"evidence_handles,omitempty"`
	RawContent      string                    `json:"raw_content,omitempty"`
	Metadata        map[string]string         `json:"metadata,omitempty"`
	CreatedAt       time.Time                 `json:"created_at,omitempty"`
	UpdatedAt       time.Time                 `json:"updated_at,omitempty"`
}

type EvaluationReportExport struct {
	ReportID    string
	Format      string
	Filename    string
	ContentType string
	Content     []byte
}

type EvaluationEvidenceKind string

const (
	EvaluationEvidenceKindToolCall EvaluationEvidenceKind = "tool_call"
	EvaluationEvidenceKindPayload  EvaluationEvidenceKind = "payload"
	EvaluationEvidenceKindResult   EvaluationEvidenceKind = "result"
	EvaluationEvidenceKindLog      EvaluationEvidenceKind = "log"
	EvaluationEvidenceKindArtifact EvaluationEvidenceKind = "artifact"
)

type EvaluationEvidenceQuery struct {
	InstanceID string
	SessionID  string
	RunID      string
	Kind       EvaluationEvidenceKind
	Limit      int
}

type EvaluationEvidenceSummary struct {
	ID         string                 `json:"id"`
	TenantID   string                 `json:"tenant_id,omitempty"`
	UserID     string                 `json:"user_id,omitempty"`
	InstanceID string                 `json:"instance_id,omitempty"`
	SessionID  string                 `json:"session_id,omitempty"`
	RunID      string                 `json:"run_id,omitempty"`
	Kind       EvaluationEvidenceKind `json:"kind"`
	Title      string                 `json:"title"`
	Summary    string                 `json:"summary,omitempty"`
	Handle     string                 `json:"handle"`
	Metadata   map[string]string      `json:"metadata,omitempty"`
	CreatedAt  time.Time              `json:"created_at,omitempty"`
	UpdatedAt  time.Time              `json:"updated_at,omitempty"`
}

func (c *Client) SearchEvaluationResources(ctx context.Context, q EvaluationResourceQuery) ([]EvaluationResourceSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	values := url.Values{}
	if q.Kind != "" {
		values.Set("kind", string(q.Kind))
	}
	if strings.TrimSpace(q.Query) != "" {
		values.Set("query", strings.TrimSpace(q.Query))
	}
	if q.IncludeInactive {
		values.Set("include_inactive", "true")
	}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	for _, item := range q.AssessmentTypes {
		item = strings.TrimSpace(item)
		if item != "" {
			values.Add("assessment_type", item)
		}
	}
	path := "/api/v1/evaluation/resources"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []EvaluationResourceSummary `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) SaveEvaluationResource(ctx context.Context, in EvaluationResourceInput) (*EvaluationResourceSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out EvaluationResourceSummary
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/evaluation/resources", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) PreviewEvaluationResource(ctx context.Context, resourceID string) (*EvaluationResourcePreview, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return nil, errors.New("resource id is required")
	}
	var out EvaluationResourcePreview
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/evaluation/resources/"+url.PathEscape(resourceID)+"/preview", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SearchEvaluationTargets(ctx context.Context, q EvaluationTargetQuery) ([]EvaluationTargetSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	values := url.Values{}
	if q.Kind != "" {
		values.Set("kind", string(q.Kind))
	}
	if strings.TrimSpace(q.Provider) != "" {
		values.Set("provider", strings.TrimSpace(q.Provider))
	}
	if strings.TrimSpace(q.Query) != "" {
		values.Set("query", strings.TrimSpace(q.Query))
	}
	if q.IncludeInactive {
		values.Set("include_inactive", "true")
	}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/api/v1/evaluation/targets"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []EvaluationTargetSummary `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) SaveEvaluationTarget(ctx context.Context, in EvaluationTargetInput) (*EvaluationTargetSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out EvaluationTargetSummary
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/evaluation/targets", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetEvaluationTarget(ctx context.Context, targetID string) (*EvaluationTargetSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return nil, errors.New("target id is required")
	}
	var out EvaluationTargetSummary
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/evaluation/targets/"+url.PathEscape(targetID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ProbeEvaluationTarget(ctx context.Context, targetID string) (*EvaluationTargetProbeResult, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return nil, errors.New("target id is required")
	}
	var out EvaluationTargetProbeResult
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/evaluation/targets/"+url.PathEscape(targetID)+"/health-check", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) StartEvaluationRun(ctx context.Context, in EvaluationRunInput) (*EvaluationRunResult, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out EvaluationRunResult
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/evaluation/runs", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListEvaluationJobs(ctx context.Context, q EvaluationJobQuery) ([]EvaluationJob, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	values := url.Values{}
	kind := q.Kind
	if kind == "" {
		kind = EvaluationJobKindRun
	}
	values.Set("kind", string(kind))
	if q.Status != "" {
		values.Set("status", string(q.Status))
	}
	if strings.TrimSpace(q.SessionID) != "" {
		values.Set("session_id", strings.TrimSpace(q.SessionID))
	}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/api/v1/jobs"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []EvaluationJob `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetEvaluationJob(ctx context.Context, jobID string) (*EvaluationJob, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	var out EvaluationJob
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelEvaluationJob(ctx context.Context, jobID string) (*EvaluationJob, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	var out EvaluationJob
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/jobs/"+url.PathEscape(jobID)+"/cancel", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RetryEvaluationJob(ctx context.Context, jobID string) (*EvaluationJob, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	var out EvaluationJob
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/jobs/"+url.PathEscape(jobID)+"/retry", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ResumeEvaluationJob(ctx context.Context, jobID string) (*EvaluationJob, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	var out EvaluationJob
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/jobs/"+url.PathEscape(jobID)+"/resume", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetEvaluationJobRecovery(ctx context.Context, jobID string) (*EvaluationJobRecovery, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	var out EvaluationJobRecovery
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID)+"/recovery", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetEvaluationReport(ctx context.Context, reportID string) (*EvaluationReport, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	reportID = strings.TrimSpace(reportID)
	if reportID == "" {
		return nil, errors.New("report id is required")
	}
	var out EvaluationReport
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/evaluation/reports/"+url.PathEscape(reportID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ExportEvaluationReport(ctx context.Context, reportID, format string) (*EvaluationReportExport, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	reportID = strings.TrimSpace(reportID)
	if reportID == "" {
		return nil, errors.New("report id is required")
	}
	format = strings.TrimSpace(format)
	values := url.Values{}
	if format != "" {
		values.Set("format", format)
	}
	path := "/api/v1/evaluation/reports/" + url.PathEscape(reportID) + "/export"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(data))
		return nil, NewUpstreamError(http.MethodGet, path, resp.StatusCode, msg)
	}
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	filename := filenameFromContentDisposition(resp.Header.Get("Content-Disposition"), reportID+".md")
	return &EvaluationReportExport{
		ReportID:    reportID,
		Format:      format,
		Filename:    filename,
		ContentType: contentType,
		Content:     content,
	}, nil
}

func (c *Client) ListEvaluationEvidence(ctx context.Context, q EvaluationEvidenceQuery) ([]EvaluationEvidenceSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	values := url.Values{}
	if strings.TrimSpace(q.InstanceID) != "" {
		values.Set("instance_id", strings.TrimSpace(q.InstanceID))
	}
	if strings.TrimSpace(q.SessionID) != "" {
		values.Set("session_id", strings.TrimSpace(q.SessionID))
	}
	if strings.TrimSpace(q.RunID) != "" {
		values.Set("run_id", strings.TrimSpace(q.RunID))
	}
	if q.Kind != "" {
		values.Set("kind", string(q.Kind))
	}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/api/v1/evaluation/evidence"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []EvaluationEvidenceSummary `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetEvaluationEvidence(ctx context.Context, evidenceID string) (*EvaluationEvidenceSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	evidenceID = strings.TrimSpace(evidenceID)
	if evidenceID == "" {
		return nil, errors.New("evidence id is required")
	}
	var out EvaluationEvidenceSummary
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/evaluation/evidence/"+url.PathEscape(evidenceID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Client) MaterializeEvaluationResource(ctx context.Context, handle string) (*EvaluationResourceMaterialization, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out EvaluationResourceMaterialization
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/evaluation/resources/materialize", map[string]string{"handle": strings.TrimSpace(handle)}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
