package maclaw

// client_runtime.go —— Runtime 与 Config 服务域（T4.2 client.go 分层）。
//
// 承载运行时交互的 DTO 与网关方法：实例 / 会话（session）/ 消息（message）/
// 运行（run），以及应用配置（RuntimeAppConfig 一族）的读写与校验；
// 另含流式事件 RuntimeEventStream。
// 传输层原语见 transport.go；Evaluation 域见 client_evaluation.go。
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

// RefreshRuntimeInstanceReadiness 是配置域唯一的游离方法（不属会话/消息/运行任一子域）。
//
// 【待退役候选 —— 勿删】`11-U3结构治理详细设计.md` §7.4 把它列为「待退役」候选：
// 它与实例就绪探测职责重叠。**但判定「待退役」是产品/架构决策，不是重构能顺带做的**
// （`08-统一执行计划.md` §5纪律第 4 条），故本文件头按 §7.4 的建议**只标注、不删**。
// 删除前需先从 GatewayClient 接口与 gateway_contract_test.go 的注册表中移除。
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

// CancelEvaluationRun 按 evaluation/run_id 取消运行，与按 instance/run 取消的
// CancelRuntimeRun 疑似职责重复。
//
// 【待退役候选 —— 勿删】`11-U3结构治理详细设计.md` §7.4 把本方法与
// StreamEvaluationRunEvents 一并列为「与 `*RuntimeRun` 版本疑似重复」的待退役候选。
// 本次仅做物理分层、**不删任何方法**（`08-统一执行计划.md` §5纪律第 4 条）。
// 注意本方法在 maclawsrv_client.go 有同名转发，删除需连带处理。
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

// StreamEvaluationRunEvents 按 evaluation/run_id 订阅事件流，与按 instance/run 的
// StreamRuntimeRunEvents 疑似职责重复。
//
// 【待退役候选 —— 勿删】见 `11-U3结构治理详细设计.md` §7.4 与本文件内
// CancelEvaluationRun 上方注释。判定「待退役」是产品/架构决策，重构不代做。
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
