package maclaw

// promptfoo_engine_client.go — anti-corruption layer for the promptfoo-engine
// adapter service (Phase 0 of the merge plan).
//
// All knowledge about the engine REST API lives here. The rest of the
// platform only sees the interfaces below. Mirrors the engine adapter's
// src/routes/runs.ts contract.

import (
	"bufio"
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

// EngineRunPhase mirrors RunPhase in the engine adapter (src/types.ts).
type EngineRunPhase string

const (
	EnginePhaseQueued          EngineRunPhase = "queued"
	EnginePhaseGeneratingTests EngineRunPhase = "generating_tests"
	EnginePhaseExecutingProbes EngineRunPhase = "executing_probes"
	EnginePhaseEngineJudging   EngineRunPhase = "engine_judging"
	EnginePhaseCompilingReport EngineRunPhase = "compiling_report"
	EnginePhaseSucceeded       EngineRunPhase = "succeeded"
	EnginePhaseFailed          EngineRunPhase = "failed"
	EnginePhaseCanceled        EngineRunPhase = "canceled"
)

// EngineJudgeMode mirrors JudgeMode in the engine adapter.
type EngineJudgeMode string

const (
	EngineJudgeAuto            EngineJudgeMode = "auto"
	EngineJudgePromptfooNative EngineJudgeMode = "promptfoo_native"
	EngineJudgePlatformRejudge EngineJudgeMode = "platform_rejudge"
)

// EngineOneShotCredentials are materialized server-side by the BFF and are
// never logged or persisted.
type EngineOneShotCredentials struct {
	Target EngineTargetCredential   `json:"target"`
	Generation *EngineGenCredential `json:"generation,omitempty"`
}

type EngineTargetCredential struct {
	BaseURL        string            `json:"base_url"`
	Model          string            `json:"model"`
	APIKey         string            `json:"api_key"`
	Headers        map[string]string `json:"headers,omitempty"`
	Concurrency    int               `json:"concurrency,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	// Agent target (kind=agent): custom HTTP template fields, mirroring
	// promptfoo's http provider. {{prompt}} stays in the template for
	// promptfoo to substitute per test; {{api_key}} is NOT substituted here —
	// the engine substitutes it server-side so the secret stays inside the
	// engine process.
	AgentEndpoint       string `json:"agent_endpoint,omitempty"`
	AgentMethod         string `json:"agent_method,omitempty"`
	AgentHeadersTemplate string `json:"agent_headers_template,omitempty"`
	AgentBodyTemplate    string `json:"agent_body_template,omitempty"`
	AgentResponsePath    string `json:"agent_response_path,omitempty"`
}

type EngineGenCredential struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key"`
}

// EngineRunRequest is the POST /runs body.
type EngineRunRequest struct {
	RunID          string                  `json:"run_id"`
	PlatformUserID string                  `json:"platform_user_id"`
	Purpose        string                  `json:"purpose"`
	NumTests       int                     `json:"num_tests"`
	Plugins        []EngineCapabilityRef   `json:"plugins"`
	Strategies     []EngineCapabilityRef   `json:"strategies"`
	JudgeMode      EngineJudgeMode         `json:"judge_mode"`
	Credentials    EngineOneShotCredentials `json:"credentials"`
}

type EngineCapabilityRef struct {
	ID     string                 `json:"id"`
	Config map[string]interface{} `json:"config,omitempty"`
}

// EngineRunStatus is the GET /runs/:id response (safe fields only).
type EngineRunStatus struct {
	RunID         string             `json:"run_id"`
	Phase         EngineRunPhase     `json:"phase"`
	StatusText    string             `json:"status_text,omitempty"`
	PlannedCount  int                `json:"planned_count"`
	ExecutedCount int                `json:"executed_count"`
	CurrentStage  string             `json:"current_stage,omitempty"`
	DurationMs    int64              `json:"duration_ms"`
	CreatedAt     string             `json:"created_at"`
	UpdatedAt     string             `json:"updated_at"`
	Result        *EngineSafeResult  `json:"result,omitempty"`
	ErrorCode     string             `json:"error_code,omitempty"`
}

// EngineSafeResult mirrors SafeRunResult (counts/severities/redacted reasons).
type EngineSafeResult struct {
	Totals         EngineRunTotals            `json:"totals"`
	SeverityCounts map[string]int             `json:"severity_counts"`
	RiskCategories []EngineRiskCategory       `json:"risk_categories"`
	PluginStats    []EnginePluginStat         `json:"plugin_stats"`
	StrategyStats  []EngineStrategyStat       `json:"strategy_stats"`
	TokenUsage     EngineTokenUsage           `json:"token_usage"`
}

type EngineRunTotals struct {
	Probes        int     `json:"probes"`
	AttackSuccess int     `json:"attack_success"`
	PassRate      float64 `json:"pass_rate"`
}

type EngineRiskCategory struct {
	Key            string         `json:"key"`
	Label          string         `json:"label"`
	Count          int            `json:"count"`
	SeverityCounts map[string]int `json:"severity_counts"`
	Plugins        []EngineCategoryPlugin `json:"plugins"`
}

type EngineCategoryPlugin struct {
	PluginID      string   `json:"plugin_id"`
	Probes        int      `json:"probes"`
	AttackSuccess int      `json:"attack_success"`
	TopReasons    []string `json:"top_reasons"`
}

type EnginePluginStat struct {
	PluginID      string  `json:"plugin_id"`
	Label         string  `json:"label"`
	Probes        int     `json:"probes"`
	AttackSuccess int     `json:"attack_success"`
	SuccessRate   float64 `json:"success_rate"`
	MaxSeverity   string  `json:"max_severity"`
}

type EngineStrategyStat struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Probes       int     `json:"probes"`
	AttackSuccess int    `json:"attack_success"`
	SuccessRate  float64 `json:"success_rate"`
	MaxSeverity  string  `json:"max_severity"`
}

type EngineTokenUsage struct {
	Generation int `json:"generation"`
	Judging    int `json:"judging"`
	Target     int `json:"target"`
}

// Sentinel errors surfaced to callers.
var (
	ErrEngineQueueFull   = errors.New("promptfoo_engine: queue full")
	ErrEngineRunExists   = errors.New("promptfoo_engine: run already exists")
	ErrEngineNotFound    = errors.New("promptfoo_engine: run not found")
	ErrEngineUnavailable = errors.New("promptfoo_engine: service unavailable")
	ErrEngineUnauthorized = errors.New("promptfoo_engine: unauthorized")
)

// PromptfooEngineClient is the anti-corruption client for the engine service.
type PromptfooEngineClient struct {
	baseURL    string
	bearer     string
	httpClient *http.Client
}

// NewPromptfooEngineClient builds a client. baseURL is e.g.
// http://promptfoo-engine:8090 (compose-internal). An empty baseURL disables
// the engine (all calls return ErrEngineUnavailable).
func NewPromptfooEngineClient(baseURL, bearer string, timeoutSeconds int) *PromptfooEngineClient {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 15
	}
	return &PromptfooEngineClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		bearer:  bearer,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutSeconds) * time.Second,
		},
	}
}

// Enabled reports whether the engine is configured.
func (c *PromptfooEngineClient) Enabled() bool {
	return c.baseURL != ""
}

// Health pings GET /health (no auth).
func (c *PromptfooEngineClient) Health(ctx context.Context) error {
	if !c.Enabled() {
		return ErrEngineUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ErrEngineUnavailable
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return ErrEngineUnavailable
	}
	return nil
}

// CreateRun submits POST /runs.
func (c *PromptfooEngineClient) CreateRun(ctx context.Context, run *EngineRunRequest) error {
	if !c.Enabled() {
		return ErrEngineUnavailable
	}
	body, err := json.Marshal(run)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/runs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ErrEngineUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		io.Copy(io.Discard, resp.Body)
		return nil
	case http.StatusConflict:
		// The engine uses 409 for both "run_already_exists" and "queue_full";
		// distinguish via the error body.
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error == "run_already_exists" {
			return ErrEngineRunExists
		}
		return ErrEngineQueueFull
	case http.StatusUnauthorized:
		return ErrEngineUnauthorized
	default:
		return fmt.Errorf("promptfoo_engine: create run failed with status %d", resp.StatusCode)
	}
}

// GetRun fetches GET /runs/:id.
func (c *PromptfooEngineClient) GetRun(ctx context.Context, runID string) (*EngineRunStatus, error) {
	if !c.Enabled() {
		return nil, ErrEngineUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/runs/"+escapePathSegment(runID), nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, ErrEngineUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// ok
	case http.StatusNotFound:
		return nil, ErrEngineNotFound
	case http.StatusUnauthorized:
		return nil, ErrEngineUnauthorized
	default:
		return nil, fmt.Errorf("promptfoo_engine: get run failed with status %d", resp.StatusCode)
	}
	var status EngineRunStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

// CancelRun posts POST /runs/:id/cancel.
func (c *PromptfooEngineClient) CancelRun(ctx context.Context, runID string) error {
	if !c.Enabled() {
		return ErrEngineUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/runs/"+escapePathSegment(runID)+"/cancel", nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ErrEngineUnavailable
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrEngineNotFound
	case http.StatusConflict:
		// already terminal — treat as success for idempotent cancel
		return nil
	case http.StatusUnauthorized:
		return ErrEngineUnauthorized
	default:
		return fmt.Errorf("promptfoo_engine: cancel run failed with status %d", resp.StatusCode)
	}
}

// StreamRunEvents connects to GET /runs/:id/events (SSE) and invokes onEvent
// for each "progress" event. It returns when the stream ends (terminal event
// or connection close) or ctx is canceled. Line parsing is intentionally
// minimal (data-only events, no multi-line data expected from the engine).
func (c *PromptfooEngineClient) StreamRunEvents(ctx context.Context, runID string, onEvent func(event *EngineRunStatus)) error {
	if !c.Enabled() {
		return ErrEngineUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/runs/"+escapePathSegment(runID)+"/events", nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	req.Header.Set("Accept", "text/event-stream")

	// Streaming needs a client without the short request timeout.
	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return ErrEngineUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return ErrEngineNotFound
		}
		return fmt.Errorf("promptfoo_engine: stream events failed with status %d", resp.StatusCode)
	}

	return parseSSEStream(resp.Body, func(data []byte) {
		var status EngineRunStatus
		if err := json.Unmarshal(data, &status); err != nil {
			return
		}
		if onEvent != nil {
			onEvent(&status)
		}
	})
}

// parseSSEStream reads an SSE body, invoking onData for every data: payload.
func parseSSEStream(body io.Reader, onData func(data []byte)) error {
	reader := bufio.NewReader(body)
	var dataBuf strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return nil // stream ended
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "data:"):
			dataBuf.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case line == "":
			// event boundary
			if dataBuf.Len() > 0 {
				onData([]byte(dataBuf.String()))
				dataBuf.Reset()
			}
		}
		if err != nil {
			// Flush any pending event then stop.
			if dataBuf.Len() > 0 {
				onData([]byte(dataBuf.String()))
			}
			return nil
		}
	}
}

func (c *PromptfooEngineClient) setAuth(req *http.Request) {
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	req.Header.Set("Content-Type", "application/json")
}

func escapePathSegment(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return -1
	}, s)
}