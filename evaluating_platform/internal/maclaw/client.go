package maclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrNotConfigured = errors.New("maclaw client is not configured")
var ErrUnsupported = errors.New("maclaw runtime capability is not supported")

type UpstreamError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func NewUpstreamError(method, path string, statusCode int, message string) *UpstreamError {
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return &UpstreamError{
		Method:     strings.TrimSpace(method),
		Path:       strings.TrimSpace(path),
		StatusCode: statusCode,
		Message:    message,
	}
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("maclaw %s %s failed: %s", e.Method, e.Path, e.Message)
}

type Config struct {
	BaseURL        string
	APIToken       string
	TimeoutSeconds int
}

type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

type RuntimeLLMProvider struct {
	Name             string `json:"name,omitempty"`
	URL              string `json:"url,omitempty"`
	Key              string `json:"key,omitempty"`
	Model            string `json:"model,omitempty"`
	WireAPI          string `json:"wire_api,omitempty"`
	Protocol         string `json:"protocol,omitempty"`
	ContextLength    int    `json:"context_length,omitempty"`
	TimeoutSec       int    `json:"timeout_sec,omitempty"`
	SupportsVision   bool   `json:"supports_vision,omitempty"`
	AgentType        string `json:"agent_type,omitempty"`
	AuthType         string `json:"auth_type,omitempty"`
	OAuthAccessToken string `json:"oauth_access_token,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
}

type RuntimeAppConfig struct {
	MaclawLLMUrl             string               `json:"maclaw_llm_url,omitempty"`
	MaclawLLMKey             string               `json:"maclaw_llm_key,omitempty"`
	MaclawLLMModel           string               `json:"maclaw_llm_model,omitempty"`
	MaclawLLMProtocol        string               `json:"maclaw_llm_protocol,omitempty"`
	MaclawLLMContextLength   int                  `json:"maclaw_llm_context_length,omitempty"`
	MaclawLLMTimeoutSec      int                  `json:"maclaw_llm_timeout_sec,omitempty"`
	MaclawLLMProviders       []RuntimeLLMProvider `json:"maclaw_llm_providers,omitempty"`
	MaclawLLMCurrentProvider string               `json:"maclaw_llm_current_provider,omitempty"`
	RemoteHubURL             string               `json:"remote_hub_url,omitempty"`
	SkillSourcesAllowed      []string             `json:"skill_sources_allowed,omitempty"`
}

type RuntimeUserConfig struct {
	TenantID  string           `json:"tenant_id,omitempty"`
	UserID    string           `json:"user_id,omitempty"`
	AppConfig RuntimeAppConfig `json:"app_config"`
	UpdatedAt time.Time        `json:"updated_at,omitempty"`
}

type RuntimeConfigValidationIssue struct {
	Key     string `json:"key,omitempty"`
	Message string `json:"message,omitempty"`
}

type RuntimeConfigValidation struct {
	Valid  bool                           `json:"valid"`
	Issues []RuntimeConfigValidationIssue `json:"issues,omitempty"`
}

type RuntimeConfigTestResult struct {
	Success      bool                     `json:"success"`
	Message      string                   `json:"message,omitempty"`
	Error        string                   `json:"error,omitempty"`
	Detail       string                   `json:"detail,omitempty"`
	LatencyMs    int64                    `json:"latency_ms,omitempty"`
	Endpoint     string                   `json:"endpoint,omitempty"`
	ProviderName string                   `json:"provider_name,omitempty"`
	Model        string                   `json:"model,omitempty"`
	Protocol     string                   `json:"protocol,omitempty"`
	WireAPI      string                   `json:"wire_api,omitempty"`
	Validation   *RuntimeConfigValidation `json:"validation,omitempty"`
}

func (cfg RuntimeAppConfig) IsEmpty() bool {
	return strings.TrimSpace(cfg.MaclawLLMUrl) == "" &&
		strings.TrimSpace(cfg.MaclawLLMKey) == "" &&
		strings.TrimSpace(cfg.MaclawLLMModel) == "" &&
		strings.TrimSpace(cfg.MaclawLLMCurrentProvider) == "" &&
		strings.TrimSpace(cfg.RemoteHubURL) == "" &&
		len(cfg.SkillSourcesAllowed) == 0 &&
		len(cfg.MaclawLLMProviders) == 0
}

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

type SkillSummary struct {
	Name                    string            `json:"name"`
	Description             string            `json:"description,omitempty"`
	Triggers                []string          `json:"triggers,omitempty"`
	Status                  string            `json:"status,omitempty"`
	Source                  string            `json:"source,omitempty"`
	Version                 string            `json:"version,omitempty"`
	Type                    string            `json:"type,omitempty"`
	Mode                    string            `json:"mode,omitempty"`
	Platforms               []string          `json:"platforms,omitempty"`
	RequiresGUI             bool              `json:"requires_gui,omitempty"`
	RequiredArgs            []string          `json:"required_args,omitempty"`
	RequiredEnv             []string          `json:"required_env,omitempty"`
	RequiredCredentialFiles []string          `json:"required_credential_files,omitempty"`
	HubSkillID              string            `json:"hub_skill_id,omitempty"`
	Metadata                map[string]string `json:"metadata,omitempty"`
}

type SkillSearchInput struct {
	Query            string   `json:"query"`
	Sources          []string `json:"sources,omitempty"`
	TopN             int      `json:"top_n,omitempty"`
	SkillHubURL      string   `json:"skill_hub_url,omitempty"`
	SkillMarketURL   string   `json:"skill_market_url,omitempty"`
	GitHubToken      string   `json:"github_token,omitempty"`
	IncludeInstalled bool     `json:"include_installed,omitempty"`
}

type SkillSearchResult struct {
	Source         string            `json:"source"`
	ID             string            `json:"id,omitempty"`
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	Version        string            `json:"version,omitempty"`
	Author         string            `json:"author,omitempty"`
	TrustLevel     string            `json:"trust_level,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	Downloads      int               `json:"downloads,omitempty"`
	AvgRating      float64           `json:"avg_rating,omitempty"`
	Price          int               `json:"price,omitempty"`
	RepoFullName   string            `json:"repo_full_name,omitempty"`
	RepoURL        string            `json:"repo_url,omitempty"`
	RawURL         string            `json:"raw_url,omitempty"`
	FilePath       string            `json:"file_path,omitempty"`
	Branch         string            `json:"branch,omitempty"`
	DefinitionType string            `json:"definition_type,omitempty"`
	Installed      bool              `json:"installed,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type SkillImportInput struct {
	ZipBase64   string `json:"zip_base64"`
	Overwrite   bool   `json:"overwrite,omitempty"`
	ArchiveName string `json:"archive_name,omitempty"`
}

type SkillInstallInput struct {
	Source         string `json:"source"`
	RepoURL        string `json:"repo_url,omitempty"`
	RawURL         string `json:"raw_url,omitempty"`
	RepoFullName   string `json:"repo_full_name,omitempty"`
	FilePath       string `json:"file_path,omitempty"`
	Branch         string `json:"branch,omitempty"`
	DefinitionType string `json:"definition_type,omitempty"`
	ZipBase64      string `json:"zip_base64,omitempty"`
	SkillHubURL    string `json:"skill_hub_url,omitempty"`
	SkillID        string `json:"skill_id,omitempty"`
	Overwrite      bool   `json:"overwrite,omitempty"`
	GitHubToken    string `json:"github_token,omitempty"`
}

type SkillExport struct {
	Name          string `json:"name"`
	FileName      string `json:"file_name"`
	ArchiveBase64 string `json:"archive_base64"`
	SizeBytes     int64  `json:"size_bytes"`
}

type RuntimeSessionInput struct {
	AgentID  string            `json:"agent_id,omitempty"`
	Title    string            `json:"title,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type RuntimeSessionQuery struct {
	Limit           int
	IncludeArchived bool
}

type RuntimeSession struct {
	ID             string            `json:"id"`
	TenantID       string            `json:"tenant_id,omitempty"`
	UserID         string            `json:"user_id,omitempty"`
	InstanceID     string            `json:"instance_id"`
	AgentID        string            `json:"agent_id,omitempty"`
	Title          string            `json:"title,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Archived       bool              `json:"archived,omitempty"`
	WaitingForUser bool              `json:"waiting_for_user,omitempty"`
	LastMessageAt  *time.Time        `json:"last_message_at,omitempty"`
	CreatedAt      time.Time         `json:"created_at,omitempty"`
	UpdatedAt      time.Time         `json:"updated_at,omitempty"`
}

type RuntimeMessageInput struct {
	Content           string                    `json:"content"`
	InputType         string                    `json:"input_type,omitempty"`
	Metadata          map[string]string         `json:"metadata,omitempty"`
	CapabilityContext *RuntimeCapabilityContext `json:"capability_context,omitempty"`
}

type RuntimeMessageQuery struct {
	Limit int
	Role  string
}

type RuntimeMessage struct {
	ID         string            `json:"id"`
	SessionID  string            `json:"session_id,omitempty"`
	TenantID   string            `json:"tenant_id,omitempty"`
	UserID     string            `json:"user_id,omitempty"`
	InstanceID string            `json:"instance_id,omitempty"`
	Role       string            `json:"role"`
	InputType  string            `json:"input_type,omitempty"`
	OutputType string            `json:"output_type,omitempty"`
	Content    string            `json:"content,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at,omitempty"`
}

type RuntimeRun struct {
	ID                 string            `json:"id"`
	SessionID          string            `json:"session_id,omitempty"`
	UserMessageID      string            `json:"user_message_id,omitempty"`
	AssistantMessageID string            `json:"assistant_message_id,omitempty"`
	Status             string            `json:"status"`
	Error              string            `json:"error,omitempty"`
	ResponseSource     string            `json:"response_source,omitempty"`
	WaitingForUser     bool              `json:"waiting_for_user,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	DurationMs         int64             `json:"duration_ms,omitempty"`
	StartedAt          time.Time         `json:"started_at,omitempty"`
	CompletedAt        *time.Time        `json:"completed_at,omitempty"`
}

type RuntimeMessageResponse struct {
	Run     *RuntimeRun     `json:"run,omitempty"`
	Message *RuntimeMessage `json:"message,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type RuntimeEventStream struct {
	Body        io.ReadCloser
	ContentType string
}

type rawSkillEntry struct {
	Name                    string            `json:"name"`
	Description             string            `json:"description"`
	Triggers                []string          `json:"triggers"`
	Status                  string            `json:"status"`
	Source                  string            `json:"source"`
	Version                 string            `json:"version"`
	HubVersion              string            `json:"hub_version"`
	Type                    string            `json:"type"`
	Mode                    string            `json:"mode"`
	Platforms               []string          `json:"platforms"`
	RequiresGUI             bool              `json:"requires_gui"`
	RequiredArgs            []string          `json:"required_args"`
	RequiredEnv             []string          `json:"required_env"`
	RequiredCredentialFiles []string          `json:"required_credential_files"`
	HubSkillID              string            `json:"hub_skill_id"`
	Metadata                map[string]string `json:"metadata"`
}

type EvaluationGateway interface {
	Enabled() bool
	SearchEvaluationResources(context.Context, EvaluationResourceQuery) ([]EvaluationResourceSummary, error)
	SaveEvaluationResource(context.Context, EvaluationResourceInput) (*EvaluationResourceSummary, error)
	PreviewEvaluationResource(context.Context, string) (*EvaluationResourcePreview, error)
	SearchEvaluationTargets(context.Context, EvaluationTargetQuery) ([]EvaluationTargetSummary, error)
	SaveEvaluationTarget(context.Context, EvaluationTargetInput) (*EvaluationTargetSummary, error)
	GetEvaluationTarget(context.Context, string) (*EvaluationTargetSummary, error)
	ProbeEvaluationTarget(context.Context, string) (*EvaluationTargetProbeResult, error)
	StartEvaluationRun(context.Context, EvaluationRunInput) (*EvaluationRunResult, error)
	ListEvaluationJobs(context.Context, EvaluationJobQuery) ([]EvaluationJob, error)
	GetEvaluationJob(context.Context, string) (*EvaluationJob, error)
	CancelEvaluationJob(context.Context, string) (*EvaluationJob, error)
	RetryEvaluationJob(context.Context, string) (*EvaluationJob, error)
	ResumeEvaluationJob(context.Context, string) (*EvaluationJob, error)
	GetEvaluationJobRecovery(context.Context, string) (*EvaluationJobRecovery, error)
	GetEvaluationReport(context.Context, string) (*EvaluationReport, error)
	ExportEvaluationReport(context.Context, string, string) (*EvaluationReportExport, error)
	ListEvaluationEvidence(context.Context, EvaluationEvidenceQuery) ([]EvaluationEvidenceSummary, error)
	GetEvaluationEvidence(context.Context, string) (*EvaluationEvidenceSummary, error)
}

type SkillGateway interface {
	Enabled() bool
	ListSkills(context.Context, int) ([]SkillSummary, error)
	SearchSkills(context.Context, SkillSearchInput) ([]SkillSearchResult, error)
	ImportSkill(context.Context, SkillImportInput) ([]SkillSummary, error)
	InstallSkill(context.Context, SkillInstallInput) ([]SkillSummary, error)
	ExportSkill(context.Context, string) (*SkillExport, error)
}

type RuntimeGateway interface {
	Enabled() bool
	ListRuntimeSessions(context.Context, string, RuntimeSessionQuery) ([]RuntimeSession, error)
	CreateRuntimeSession(context.Context, string, RuntimeSessionInput) (*RuntimeSession, error)
	GetRuntimeSession(context.Context, string, string) (*RuntimeSession, error)
	DeleteRuntimeSession(context.Context, string, string) error
	ListRuntimeMessages(context.Context, string, string, RuntimeMessageQuery) ([]RuntimeMessage, error)
	PostRuntimeMessage(context.Context, string, string, RuntimeMessageInput) (*RuntimeMessageResponse, error)
	ConfirmRuntimePlan(context.Context, string, string, RuntimeMessageInput) (*EvaluationJob, error)
	ListEvaluationJobs(context.Context, EvaluationJobQuery) ([]EvaluationJob, error)
	CancelRuntimeRun(context.Context, string, string) (*RuntimeRun, error)
	StreamRuntimeRunEvents(context.Context, string, string) (*RuntimeEventStream, error)
	CancelEvaluationRun(context.Context, string) (*RuntimeRun, error)
	StreamEvaluationRunEvents(context.Context, string) (*RuntimeEventStream, error)
}

func NewClient(cfg Config) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	apiToken := strings.TrimSpace(cfg.APIToken)
	if baseURL == "" || apiToken == "" {
		return nil, ErrNotConfigured
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL:    baseURL,
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != "" && c.apiToken != ""
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

func (c *Client) ListSkills(ctx context.Context, limit int) ([]SkillSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/v1/skills"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []rawSkillEntry `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return summarizeSkills(out.Items), nil
}

func (c *Client) SearchSkills(ctx context.Context, in SkillSearchInput) ([]SkillSearchResult, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out struct {
		Items []SkillSearchResult `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/skills/search", in, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) ImportSkill(ctx context.Context, in SkillImportInput) ([]SkillSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out struct {
		Items []rawSkillEntry `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/skills/import", in, &out); err != nil {
		return nil, err
	}
	return summarizeSkills(out.Items), nil
}

func (c *Client) InstallSkill(ctx context.Context, in SkillInstallInput) ([]SkillSummary, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out struct {
		Items []rawSkillEntry `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/skills/install", in, &out); err != nil {
		return nil, err
	}
	return summarizeSkills(out.Items), nil
}

func (c *Client) ExportSkill(ctx context.Context, skillName string) (*SkillExport, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return nil, errors.New("skill name is required")
	}
	var out SkillExport
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/skills/"+url.PathEscape(skillName)+"/export", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListRuntimeSessions(ctx context.Context, instanceID string, q RuntimeSessionQuery) ([]RuntimeSession, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	values := url.Values{}
	values.Set("instance_id", instanceID)
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.IncludeArchived {
		values.Set("include_archived", "true")
	}
	path := "/api/v1/evaluation/sessions"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []RuntimeSession `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) CreateRuntimeSession(ctx context.Context, instanceID string, in RuntimeSessionInput) (*RuntimeSession, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	var out RuntimeSession
	payload := struct {
		InstanceID string            `json:"instance_id"`
		AgentID    string            `json:"agent_id,omitempty"`
		Title      string            `json:"title,omitempty"`
		Metadata   map[string]string `json:"metadata,omitempty"`
	}{
		InstanceID: instanceID,
		AgentID:    in.AgentID,
		Title:      in.Title,
		Metadata:   in.Metadata,
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/evaluation/sessions", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RefreshRuntimeInstanceReadiness(ctx context.Context, instanceID string) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return errors.New("instance id is required")
	}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/instances/"+url.PathEscape(instanceID)+"/refresh-readiness", nil, nil)
}

func (c *Client) GetRuntimeSession(ctx context.Context, instanceID, sessionID string) (*RuntimeSession, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	sessionID = strings.TrimSpace(sessionID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	var out RuntimeSession
	values := url.Values{"instance_id": []string{instanceID}}
	path := "/api/v1/evaluation/sessions/" + url.PathEscape(sessionID) + "?" + values.Encode()
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteRuntimeSession(ctx context.Context, instanceID, sessionID string) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	sessionID = strings.TrimSpace(sessionID)
	if instanceID == "" {
		return errors.New("instance id is required")
	}
	if sessionID == "" {
		return errors.New("session id is required")
	}
	values := url.Values{"instance_id": []string{instanceID}}
	path := "/api/v1/evaluation/sessions/" + url.PathEscape(sessionID) + "?" + values.Encode()
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) ListRuntimeMessages(ctx context.Context, instanceID, sessionID string, q RuntimeMessageQuery) ([]RuntimeMessage, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	sessionID = strings.TrimSpace(sessionID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	values := url.Values{}
	values.Set("instance_id", instanceID)
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	if strings.TrimSpace(q.Role) != "" {
		values.Set("role", strings.TrimSpace(q.Role))
	}
	path := "/api/v1/evaluation/sessions/" + url.PathEscape(sessionID) + "/messages"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []RuntimeMessage `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) PostRuntimeMessage(ctx context.Context, instanceID, sessionID string, in RuntimeMessageInput) (*RuntimeMessageResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	sessionID = strings.TrimSpace(sessionID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	var out RuntimeMessageResponse
	payload := struct {
		InstanceID        string                    `json:"instance_id"`
		Content           string                    `json:"content"`
		InputType         string                    `json:"input_type,omitempty"`
		Metadata          map[string]string         `json:"metadata,omitempty"`
		CapabilityContext *RuntimeCapabilityContext `json:"capability_context,omitempty"`
	}{
		InstanceID:        instanceID,
		Content:           in.Content,
		InputType:         in.InputType,
		Metadata:          in.Metadata,
		CapabilityContext: in.CapabilityContext,
	}
	path := "/api/v1/evaluation/sessions/" + url.PathEscape(sessionID) + "/messages"
	if err := c.doJSON(ctx, http.MethodPost, path, payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ConfirmRuntimePlan(ctx context.Context, instanceID, sessionID string, in RuntimeMessageInput) (*EvaluationJob, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	sessionID = strings.TrimSpace(sessionID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	var out EvaluationJob
	payload := struct {
		InstanceID string            `json:"instance_id"`
		Content    string            `json:"content,omitempty"`
		Metadata   map[string]string `json:"metadata,omitempty"`
		TestCount  int               `json:"test_count,omitempty"`
	}{
		InstanceID: instanceID,
		Content:    in.Content,
		Metadata:   in.Metadata,
	}
	if raw := strings.TrimSpace(in.Metadata["test_count"]); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			payload.TestCount = parsed
		}
	}
	path := "/api/v1/evaluation/sessions/" + url.PathEscape(sessionID) + "/confirm"
	if err := c.doJSON(ctx, http.MethodPost, path, payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelRuntimeRun(ctx context.Context, instanceID, runID string) (*RuntimeRun, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	runID = strings.TrimSpace(runID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	if runID == "" {
		return nil, errors.New("run id is required")
	}
	var out RuntimeRun
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/runs/" + url.PathEscape(runID) + "/cancel"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelEvaluationRun(ctx context.Context, runID string) (*RuntimeRun, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, errors.New("run id is required")
	}
	var out RuntimeRun
	path := "/api/v1/evaluation/runs/" + url.PathEscape(runID) + "/cancel"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) StreamRuntimeRunEvents(ctx context.Context, instanceID, runID string) (*RuntimeEventStream, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	runID = strings.TrimSpace(runID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	if runID == "" {
		return nil, errors.New("run id is required")
	}
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/runs/" + url.PathEscape(runID) + "/events"
	return c.streamEvents(ctx, path)
}

func (c *Client) streamEvents(ctx context.Context, path string) (*RuntimeEventStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(data))
		return nil, NewUpstreamError(http.MethodGet, path, resp.StatusCode, msg)
	}
	return &RuntimeEventStream{Body: resp.Body, ContentType: resp.Header.Get("Content-Type")}, nil
}

func (c *Client) StreamEvaluationRunEvents(ctx context.Context, runID string) (*RuntimeEventStream, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, errors.New("run id is required")
	}
	path := "/api/v1/evaluation/runs/" + url.PathEscape(runID) + "/events"
	return c.streamEvents(ctx, path)
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

func (c *Client) GetRuntimeConfig(ctx context.Context) (*RuntimeUserConfig, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out RuntimeUserConfig
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/config", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateRuntimeConfig(ctx context.Context, in RuntimeAppConfig) (*RuntimeUserConfig, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var out RuntimeUserConfig
	if err := c.doJSON(ctx, http.MethodPut, "/api/v1/config", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ValidateRuntimeConfig(ctx context.Context, candidate *RuntimeAppConfig) (*RuntimeConfigValidation, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var in any
	if candidate != nil {
		in = candidate
	}
	var out RuntimeConfigValidation
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/config/validate", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) TestRuntimeConfig(ctx context.Context, candidate *RuntimeAppConfig) (*RuntimeConfigTestResult, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	var in any
	if candidate != nil {
		in = candidate
	}
	var out RuntimeConfigTestResult
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/config/test", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(data))
		return NewUpstreamError(method, path, resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func summarizeSkills(items []rawSkillEntry) []SkillSummary {
	out := make([]SkillSummary, 0, len(items))
	for _, item := range items {
		metadata := cloneMetadata(item.Metadata)
		if strings.TrimSpace(item.HubSkillID) != "" {
			metadata["hub_skill_id"] = strings.TrimSpace(item.HubSkillID)
		}
		out = append(out, SkillSummary{
			Name:                    strings.TrimSpace(item.Name),
			Description:             strings.TrimSpace(item.Description),
			Triggers:                cloneStringList(item.Triggers),
			Status:                  strings.TrimSpace(item.Status),
			Source:                  strings.TrimSpace(item.Source),
			Version:                 firstNonEmptyString(item.Version, item.HubVersion),
			Type:                    strings.TrimSpace(item.Type),
			Mode:                    strings.TrimSpace(item.Mode),
			Platforms:               cloneStringList(item.Platforms),
			RequiresGUI:             item.RequiresGUI,
			RequiredArgs:            cloneStringList(item.RequiredArgs),
			RequiredEnv:             cloneStringList(item.RequiredEnv),
			RequiredCredentialFiles: cloneStringList(item.RequiredCredentialFiles),
			HubSkillID:              strings.TrimSpace(item.HubSkillID),
			Metadata:                metadata,
		})
	}
	return out
}

func cloneStringList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filenameFromContentDisposition(raw, fallback string) string {
	_, params, err := mime.ParseMediaType(raw)
	if err == nil {
		if filename := strings.TrimSpace(params["filename"]); filename != "" {
			return filename
		}
	}
	return fallback
}
