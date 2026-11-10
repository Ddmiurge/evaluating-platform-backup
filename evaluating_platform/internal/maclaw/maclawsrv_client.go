package maclaw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type MaclawSrvClient struct {
	*Client
	instanceID string
}

const RedteamMCPBridgeName = "evaluating-platform-redteam-tools"

type MCPToolView struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type MCPServerInput struct {
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	EndpointURL string            `json:"endpoint_url,omitempty"`
	AuthType    string            `json:"auth_type,omitempty"`
	AuthSecret  string            `json:"auth_secret,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Disabled    bool              `json:"disabled,omitempty"`
	AutoStart   bool              `json:"auto_start,omitempty"`
}

type mcpServerUpdateInput struct {
	Name        *string           `json:"name,omitempty"`
	EndpointURL *string           `json:"endpoint_url,omitempty"`
	AuthType    *string           `json:"auth_type,omitempty"`
	AuthSecret  *string           `json:"auth_secret,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Command     *string           `json:"command,omitempty"`
	Args        *[]string         `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Disabled    *bool             `json:"disabled,omitempty"`
	AutoStart   *bool             `json:"auto_start,omitempty"`
}

type MCPServerView struct {
	ID            string        `json:"id"`
	Kind          string        `json:"kind"`
	Name          string        `json:"name"`
	EndpointURL   string        `json:"endpoint_url,omitempty"`
	AuthType      string        `json:"auth_type,omitempty"`
	HasAuthSecret bool          `json:"has_auth_secret,omitempty"`
	HeaderNames   []string      `json:"header_names,omitempty"`
	Command       string        `json:"command,omitempty"`
	Args          []string      `json:"args,omitempty"`
	EnvKeys       []string      `json:"env_keys,omitempty"`
	HasEnv        bool          `json:"has_env,omitempty"`
	Disabled      bool          `json:"disabled,omitempty"`
	AutoStart     bool          `json:"auto_start,omitempty"`
	Source        string        `json:"source,omitempty"`
	Running       bool          `json:"running,omitempty"`
	HealthStatus  string        `json:"health_status,omitempty"`
	FailCount     int           `json:"fail_count,omitempty"`
	LastCheckAt   string        `json:"last_check_at,omitempty"`
	CreatedAt     string        `json:"created_at,omitempty"`
	Tools         []MCPToolView `json:"tools,omitempty"`
}

type MCPGateway interface {
	Enabled() bool
	ListMCPServers(context.Context, int) ([]MCPServerView, error)
	CreateMCPServer(context.Context, MCPServerInput) (*MCPServerView, error)
	GetMCPServer(context.Context, string) (*MCPServerView, error)
	UpdateMCPServer(context.Context, string, MCPServerInput) (*MCPServerView, error)
	DeleteMCPServer(context.Context, string) error
	StartMCPServer(context.Context, string) (*MCPServerView, error)
	StopMCPServer(context.Context, string) (*MCPServerView, error)
	HealthCheckMCPServer(context.Context, string) (*MCPServerView, error)
	ListMCPServerTools(context.Context, string) ([]MCPToolView, error)
}

type RedteamMCPBridgeInput struct {
	EndpointURL string            `json:"endpoint_url"`
	AuthSecret  string            `json:"auth_secret,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	AutoStart   bool              `json:"auto_start,omitempty"`
}

func NewMaclawSrvClient(cfg Config) (*MaclawSrvClient, error) {
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &MaclawSrvClient{Client: client}, nil
}

func (c *MaclawSrvClient) WithInstanceID(instanceID string) *MaclawSrvClient {
	if c == nil {
		return nil
	}
	clone := *c
	clone.instanceID = strings.TrimSpace(instanceID)
	return &clone
}

func (c *MaclawSrvClient) defaultInstanceID() (string, error) {
	if !c.Enabled() {
		return "", ErrNotConfigured
	}
	instanceID := strings.TrimSpace(c.instanceID)
	if instanceID == "" {
		return "", errors.New("maclawsrv instance id is required")
	}
	return instanceID, nil
}

func (c *MaclawSrvClient) ListMCPServers(ctx context.Context, limit int) ([]MCPServerView, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/v1/mcp/servers"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []MCPServerView `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *MaclawSrvClient) CreateMCPServer(ctx context.Context, in MCPServerInput) (*MCPServerView, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	in = normalizeMCPServerInput(in)
	if err := validateMCPServerInput(in); err != nil {
		return nil, err
	}
	var out MCPServerView
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/mcp/servers", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *MaclawSrvClient) UpdateMCPServer(ctx context.Context, serverID string, in MCPServerInput) (*MCPServerView, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return nil, errors.New("mcp server id is required")
	}
	in = normalizeMCPServerInput(in)
	if err := validateMCPServerInput(in); err != nil {
		return nil, err
	}
	var out MCPServerView
	if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/mcp/servers/"+url.PathEscape(serverID), mcpServerUpdateInputFromInput(in), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *MaclawSrvClient) GetMCPServer(ctx context.Context, serverID string) (*MCPServerView, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return nil, errors.New("mcp server id is required")
	}
	var out MCPServerView
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/mcp/servers/"+url.PathEscape(serverID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *MaclawSrvClient) DeleteMCPServer(ctx context.Context, serverID string) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return errors.New("mcp server id is required")
	}
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/mcp/servers/"+url.PathEscape(serverID), nil, nil)
}

func (c *MaclawSrvClient) StartMCPServer(ctx context.Context, serverID string) (*MCPServerView, error) {
	return c.mutateMCPServer(ctx, serverID, "start")
}

func (c *MaclawSrvClient) StopMCPServer(ctx context.Context, serverID string) (*MCPServerView, error) {
	return c.mutateMCPServer(ctx, serverID, "stop")
}

func (c *MaclawSrvClient) HealthCheckMCPServer(ctx context.Context, serverID string) (*MCPServerView, error) {
	return c.mutateMCPServer(ctx, serverID, "health-check")
}

func (c *MaclawSrvClient) mutateMCPServer(ctx context.Context, serverID, action string) (*MCPServerView, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	serverID = strings.TrimSpace(serverID)
	action = strings.TrimSpace(action)
	if serverID == "" {
		return nil, errors.New("mcp server id is required")
	}
	var out MCPServerView
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/mcp/servers/"+url.PathEscape(serverID)+"/"+action, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *MaclawSrvClient) ListMCPServerTools(ctx context.Context, serverID string) ([]MCPToolView, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return nil, errors.New("mcp server id is required")
	}
	var out struct {
		Items []MCPToolView `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/mcp/servers/"+url.PathEscape(serverID)+"/tools", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *MaclawSrvClient) ListEvaluationJobs(ctx context.Context, q EvaluationJobQuery) ([]EvaluationJob, error) {
	instanceID, err := c.defaultInstanceID()
	if err != nil {
		return nil, err
	}
	values := url.Values{}
	if q.Status != "" {
		values.Set("status", maclawSrvRunStatusFromEvaluationJobStatus(q.Status))
	}
	if strings.TrimSpace(q.SessionID) != "" {
		values.Set("session_id", strings.TrimSpace(q.SessionID))
	}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/runs"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []RuntimeRun `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	jobs := make([]EvaluationJob, 0, len(out.Items))
	for i := range out.Items {
		jobs = append(jobs, *evaluationJobFromRuntimeRun(&out.Items[i]))
	}
	return jobs, nil
}

func (c *MaclawSrvClient) GetEvaluationJob(ctx context.Context, jobID string) (*EvaluationJob, error) {
	instanceID, err := c.defaultInstanceID()
	if err != nil {
		return nil, err
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	var out RuntimeRun
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/runs/" + url.PathEscape(jobID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return evaluationJobFromRuntimeRun(&out), nil
}

func (c *MaclawSrvClient) SearchEvaluationResources(context.Context, EvaluationResourceQuery) ([]EvaluationResourceSummary, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) ArchiveResource(context.Context, string) (*EvaluationResourceSummary, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) SaveEvaluationResource(context.Context, EvaluationResourceInput) (*EvaluationResourceSummary, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) PreviewEvaluationResource(context.Context, string) (*EvaluationResourcePreview, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) SearchEvaluationTargets(context.Context, EvaluationTargetQuery) ([]EvaluationTargetSummary, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) SaveEvaluationTarget(context.Context, EvaluationTargetInput) (*EvaluationTargetSummary, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) GetEvaluationTarget(context.Context, string) (*EvaluationTargetSummary, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) ProbeEvaluationTarget(context.Context, string) (*EvaluationTargetProbeResult, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) StartEvaluationRun(context.Context, EvaluationRunInput) (*EvaluationRunResult, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) RetryEvaluationJob(_ context.Context, jobID string) (*EvaluationJob, error) {
	if _, err := c.defaultInstanceID(); err != nil {
		return nil, err
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	return nil, NewUpstreamError(http.MethodPost, "/api/v1/instances/{instance_id}/runs/"+url.PathEscape(jobID)+"/retry", http.StatusConflict, "manual review is required before retry")
}

func (c *MaclawSrvClient) CancelEvaluationJob(ctx context.Context, jobID string) (*EvaluationJob, error) {
	instanceID, err := c.defaultInstanceID()
	if err != nil {
		return nil, err
	}
	run, err := c.CancelRuntimeRun(ctx, instanceID, jobID)
	if err != nil {
		return nil, err
	}
	return evaluationJobFromRuntimeRun(run), nil
}

func (c *MaclawSrvClient) ResumeEvaluationJob(_ context.Context, jobID string) (*EvaluationJob, error) {
	if _, err := c.defaultInstanceID(); err != nil {
		return nil, err
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	return nil, NewUpstreamError(http.MethodPost, "/api/v1/instances/{instance_id}/runs/"+url.PathEscape(jobID)+"/resume", http.StatusConflict, "manual review is required before resume")
}

func (c *MaclawSrvClient) GetEvaluationJobRecovery(ctx context.Context, jobID string) (*EvaluationJobRecovery, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	job, err := c.GetEvaluationJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	runID := jobID
	if job != nil && job.Progress != nil && strings.TrimSpace(job.Progress.RunID) != "" {
		runID = strings.TrimSpace(job.Progress.RunID)
	}
	return &EvaluationJobRecovery{
		JobID:                jobID,
		RunID:                runID,
		Action:               "manual_review",
		Strategy:             "manual_review",
		CanResume:            false,
		CanRetry:             false,
		ManualReviewRequired: true,
		Reason:               "MaClawSrv native retry/resume recovery is not available for this run; review the safe run summary and start a new confirmed evaluation if needed.",
		RecoveryIndexed:      false,
	}, nil
}

func (c *MaclawSrvClient) GetEvaluationReport(context.Context, string) (*EvaluationReport, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) ExportEvaluationReport(context.Context, string, string) (*EvaluationReportExport, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) ListEvaluationEvidence(context.Context, EvaluationEvidenceQuery) ([]EvaluationEvidenceSummary, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) GetEvaluationEvidence(context.Context, string) (*EvaluationEvidenceSummary, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) MaterializeEvaluationResource(context.Context, string) (*EvaluationResourceMaterialization, error) {
	return nil, ErrUnsupported
}

func (c *MaclawSrvClient) CancelEvaluationRun(ctx context.Context, runID string) (*RuntimeRun, error) {
	instanceID, err := c.defaultInstanceID()
	if err != nil {
		return nil, err
	}
	return c.CancelRuntimeRun(ctx, instanceID, runID)
}

func (c *MaclawSrvClient) StreamEvaluationRunEvents(ctx context.Context, runID string) (*RuntimeEventStream, error) {
	instanceID, err := c.defaultInstanceID()
	if err != nil {
		return nil, err
	}
	return c.StreamRuntimeRunEvents(ctx, instanceID, runID)
}

func (c *MaclawSrvClient) ListRuntimeSessions(ctx context.Context, instanceID string, q RuntimeSessionQuery) ([]RuntimeSession, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	values := url.Values{}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.IncludeArchived {
		values.Set("include_archived", "true")
	}
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/sessions"
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

func (c *MaclawSrvClient) CreateRuntimeSession(ctx context.Context, instanceID string, in RuntimeSessionInput) (*RuntimeSession, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil, errors.New("instance id is required")
	}
	var out RuntimeSession
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/instances/"+url.PathEscape(instanceID)+"/sessions", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *MaclawSrvClient) GetRuntimeSession(ctx context.Context, instanceID, sessionID string) (*RuntimeSession, error) {
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
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/sessions/" + url.PathEscape(sessionID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *MaclawSrvClient) DeleteRuntimeSession(ctx context.Context, instanceID, sessionID string) error {
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
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/sessions/" + url.PathEscape(sessionID)
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil)
}

func (c *MaclawSrvClient) ListRuntimeMessages(ctx context.Context, instanceID, sessionID string, q RuntimeMessageQuery) ([]RuntimeMessage, error) {
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
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	if strings.TrimSpace(q.Role) != "" {
		values.Set("role", strings.TrimSpace(q.Role))
	}
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/sessions/" + url.PathEscape(sessionID) + "/messages"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Items []RuntimeMessage `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Items {
		normalizeMaclawSrvMessage(&out.Items[i])
	}
	return out.Items, nil
}

func (c *MaclawSrvClient) PostRuntimeMessage(ctx context.Context, instanceID, sessionID string, in RuntimeMessageInput) (*RuntimeMessageResponse, error) {
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
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/sessions/" + url.PathEscape(sessionID) + "/messages"
	if err := c.doJSON(ctx, http.MethodPost, path, in, &out); err != nil {
		return nil, err
	}
	if out.Message != nil {
		normalizeMaclawSrvMessage(out.Message)
	}
	return &out, nil
}

func (c *MaclawSrvClient) postRuntimeMessageAsync(ctx context.Context, instanceID, sessionID string, in RuntimeMessageInput) (*RuntimeMessageResponse, error) {
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
	path := "/api/v1/instances/" + url.PathEscape(instanceID) + "/sessions/" + url.PathEscape(sessionID) + "/messages?async=true"
	started := time.Now()
	if err := c.doJSON(ctx, http.MethodPost, path, in, &out); err != nil {
		return nil, err
	}
	if out.Run != nil {
		annotateRuntimeRunProgressMetadata(out.Run, in.Metadata)
		annotateRuntimeRunDuration(out.Run, "maclaw_confirm", durationMillisSince(started))
	}
	if out.Message != nil {
		normalizeMaclawSrvMessage(out.Message)
	}
	return &out, nil
}

func annotateRuntimeRunProgressMetadata(run *RuntimeRun, inputMetadata map[string]string) {
	if run == nil || len(inputMetadata) == 0 {
		return
	}
	if run.Metadata == nil {
		run.Metadata = map[string]string{}
	}
	if strings.TrimSpace(run.Metadata["planned_count"]) == "" {
		if raw := strings.TrimSpace(inputMetadata["test_count"]); raw != "" {
			run.Metadata["planned_count"] = raw
		}
	}
	if strings.TrimSpace(run.Metadata["executed_count"]) == "" && strings.TrimSpace(run.Metadata["planned_count"]) != "" {
		run.Metadata["executed_count"] = "0"
	}
	if strings.TrimSpace(run.Metadata["current_stage"]) == "" {
		run.Metadata["current_stage"] = "starting"
	}
}

func normalizeMaclawSrvMessage(msg *RuntimeMessage) {
	if msg == nil {
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	if !strings.EqualFold(strings.TrimSpace(msg.Metadata["hard_exit"]), "true") || strings.TrimSpace(msg.Content) != "" || strings.TrimSpace(msg.Role) != "assistant" {
		return
	}
	msg.Content = "\u672c\u8f6e MaClaw \u6ca1\u6709\u751f\u6210\u53ef\u5c55\u793a\u7684\u6709\u6548\u56de\u590d\u3002\u8bf7\u6362\u4e2a\u8bf4\u6cd5\u6216\u7a0d\u540e\u91cd\u8bd5\uff1b\u5728\u4f60\u786e\u8ba4\u6267\u884c\u524d\uff0c\u7cfb\u7edf\u4e0d\u4f1a\u8c03\u7528\u771f\u5b9e\u76ee\u6807\u6216\u751f\u6210\u6b63\u5f0f\u62a5\u544a\u3002"
	if strings.TrimSpace(msg.OutputType) == "" {
		msg.OutputType = "text/plain"
	}
	msg.Metadata["response_source"] = "ask_user"
	msg.Metadata["evaluation_event_type"] = "ask_user"
	msg.Metadata["maclaw_hard_exit_fallback"] = "true"
}

func (c *MaclawSrvClient) ConfirmRuntimePlan(ctx context.Context, instanceID, sessionID string, in RuntimeMessageInput) (*EvaluationJob, error) {
	out, err := c.postRuntimeMessageAsync(ctx, instanceID, sessionID, in)
	if err != nil {
		if job := c.latestCompletedRuntimeMessageJob(ctx, instanceID, sessionID); job != nil {
			return job, nil
		}
		return nil, err
	}
	if out == nil || out.Run == nil {
		if out != nil && out.Message != nil {
			return evaluationJobFromRuntimeMessage(out.Message, sessionID)
		}
		return nil, errors.New("maclawsrv confirm response did not include a run")
	}
	return evaluationJobFromRuntimeRun(out.Run), nil
}

func (c *MaclawSrvClient) latestCompletedRuntimeMessageJob(ctx context.Context, instanceID, sessionID string) *EvaluationJob {
	messages, err := c.ListRuntimeMessages(ctx, instanceID, sessionID, RuntimeMessageQuery{Limit: 10})
	if err != nil {
		return nil
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if strings.TrimSpace(msg.Role) != "assistant" {
			continue
		}
		if !hasAdjacentConfirmUserMessage(messages, i) {
			continue
		}
		body := map[string]any{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(msg.Content)), &body); err != nil {
			continue
		}
		if !strings.EqualFold(jsonMapString(body, "status"), "completed") {
			continue
		}
		if strings.TrimSpace(jsonMapString(body, "run_id")) == "" {
			continue
		}
		// 同一段 JSON 上面刚解析成功过，所以这里的 error 不可达；若将来重构
		// 改变了顺序，skip 也比返回一个「捏造出 completed 的任务」安全。
		job, err := evaluationJobFromRuntimeMessage(&msg, sessionID)
		if err != nil {
			continue
		}
		return job
	}
	return nil
}

func hasAdjacentConfirmUserMessage(messages []RuntimeMessage, assistantIndex int) bool {
	if assistantIndex <= 0 || assistantIndex > len(messages)-1 {
		return false
	}
	prev := messages[assistantIndex-1]
	return strings.TrimSpace(prev.Role) == "user" &&
		strings.TrimSpace(prev.Metadata["evaluation_action"]) == "confirm_plan"
}
