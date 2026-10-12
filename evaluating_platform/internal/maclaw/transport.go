package maclaw

// transport.go —— MaClaw 网关传输层（T4.2 client.go 分层）。
//
// 本文件只承载「与具体服务域无关」的传输层关注点：
//   - 哨兵错误（ErrNotConfigured / ErrUnsupported）与上游错误包装 UpstreamError
//   - 客户端配置 Config 与连接持有者 Client
//   - 三个网关接口声明（EvaluationGateway / SkillGateway / RuntimeGateway）
//   - 构造器 NewClient、连通性判定 Enabled
//   - 底层收发原语 doJSON / streamEvents
//   - 响应文件名解析 filenameFromContentDisposition（仅 ExportEvaluationReport 使用）
//
// 各服务域的 DTO 与方法分别位于：
//   - client_evaluation.go  Evaluation 域（资源 / 目标 / 运行 / 作业 / 报告 / 证据）
//   - client_runtime.go     Runtime + Config 域（实例 / 会话 / 消息 / 运行 / 应用配置）
//   - client_legacy.go      Skill 域与共享的纯函数辅助
//
// ⚠️ 本文件由 U3-b T4.2 从原 client.go 物理拆分而来：git mv 保留了历史
// （git log --follow transport.go 可追溯到 client.go）。拆分只搬运代码，
// 未改动任何逻辑、签名或方法集。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	InstallSkill(context.Context, SkillInstallInput) ([]SkillSummary, error)
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
