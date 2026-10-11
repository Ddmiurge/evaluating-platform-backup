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
	Target     EngineTargetCredential `json:"target"`
	Generation *EngineGenCredential   `json:"generation,omitempty"`
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
	AgentEndpoint        string `json:"agent_endpoint,omitempty"`
	AgentMethod          string `json:"agent_method,omitempty"`
	AgentHeadersTemplate string `json:"agent_headers_template,omitempty"`
	AgentBodyTemplate    string `json:"agent_body_template,omitempty"`
	AgentResponsePath    string `json:"agent_response_path,omitempty"`
}

type EngineGenCredential struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key"`
}

// EngineApplicationDefinition is the platform's structured description of the
// system under test (U7, DD-1=C).
//
// Why the engine takes named fields rather than promptfoo's own
// `applicationDefinition` object: promptfoo's *library* entrypoint never reads
// that key. It exists only on the CLI path (dist/src/main.js), and
// `RedteamConfigSchema` does not declare it, so a config document carrying it
// would have the field silently stripped by zod. The engine renders these into
// the promptfoo `purpose` channel instead, which the library does honour.
//
// Every field is optional. A thin Purpose alone degrades to generic attacks
// rather than failing the run.
type EngineApplicationDefinition struct {
	Purpose              string `json:"purpose,omitempty"`
	Features             string `json:"features,omitempty"`
	HasAccessTo          string `json:"has_access_to,omitempty"`
	DoesNotHaveAccessTo  string `json:"does_not_have_access_to,omitempty"`
	UserTypes            string `json:"user_types,omitempty"`
	SecurityRequirements string `json:"security_requirements,omitempty"`
}

// EngineRunRequest is the POST /runs body.
type EngineRunRequest struct {
	RunID          string                   `json:"run_id"`
	PlatformUserID string                   `json:"platform_user_id"`
	Purpose        string                   `json:"purpose"`
	NumTests       int                      `json:"num_tests"`
	Plugins        []EngineCapabilityRef    `json:"plugins"`
	Strategies     []EngineCapabilityRef    `json:"strategies"`
	JudgeMode      EngineJudgeMode          `json:"judge_mode"`
	Credentials    EngineOneShotCredentials `json:"credentials"`
	// Catalog is the plugin/strategy classification source of truth the BFF
	// hands to the engine (U2/T2.2). Optional for backward compatibility: when
	// absent the engine falls back to its "unknown plugin" defaults
	// (category=other / medium). Never contains payloads or secrets.
	Catalog *EngineCatalog `json:"catalog,omitempty"`

	// --- U7: inputs promptfoo's own generation needs and Plan A never used ---

	// ApplicationDefinition describes the system under test. Without it the
	// generated attacks stay generic because the generator does not know what
	// the system can or cannot do.
	ApplicationDefinition *EngineApplicationDefinition `json:"application_definition,omitempty"`
	// TestGenerationInstructions are extra generation constraints (language,
	// domain, taboo topics) applied to every plugin.
	TestGenerationInstructions string `json:"test_generation_instructions,omitempty"`
	// InjectVar is the prompt variable promptfoo injects generated payloads
	// into. Empty means the engine default ("prompt"). Strategy transforms that
	// rewrite a payload need this to know where to write.
	InjectVar string `json:"inject_var,omitempty"`

	// --- U10: expert-published YAML plugin packages ---

	// CustomPlugins carries the resolved packages for this run. The BFF has
	// already applied admin governance (a disabled or archived package is
	// rejected before it gets here), validated the YAML against promptfoo's
	// custom-plugin schema, and re-serialized it canonically — so `Definition`
	// is exactly the document that was validated, byte for byte.
	//
	// Shipped inline rather than fetched by the engine: the engine has no
	// database access and no business having one, and the payload is small (a
	// template plus a rubric). The engine writes each definition into its
	// per-run workdir and references it by `file://` path, which is the form
	// promptfoo's CustomPlugin actually loads.
	CustomPlugins []EngineCustomPlugin `json:"custom_plugins,omitempty"`
}

type EngineCapabilityRef struct {
	ID     string                 `json:"id"`
	Config map[string]interface{} `json:"config,omitempty"`
}

// EngineCatalog is the plugin/strategy catalog the BFF sends with each run so
// the engine classifies and scores results from a single source of truth
// (U2/T2.2). The engine looks every plugin_id up here instead of carrying its
// own regex/mapping tables for category and severity.
type EngineCatalog struct {
	Plugins    []EngineCatalogPlugin   `json:"plugins"`
	Strategies []EngineCatalogStrategy `json:"strategies"`
}

// EngineCatalogPlugin is one engine plugin family as the engine must classify
// it: the risk category key, its Chinese display label, and the default
// display severity. Named fields only — no payloads.
type EngineCatalogPlugin struct {
	ID            string `json:"id"`
	Category      string `json:"category"`
	CategoryLabel string `json:"category_label"`
	Severity      string `json:"severity"`
	// RequiresRemoteGeneration marks plugins whose attack cases promptfoo can
	// only fetch from its remote generation service (U6). The engine uses it
	// to refuse such a run up front with a readable error code instead of
	// letting promptfoo `return []` mid-run and report 0 coverage as success.
	// omitempty keeps pre-U6 BFF payloads byte-identical on the wire.
	RequiresRemoteGeneration bool `json:"requires_remote_generation,omitempty"`
	// UnavailableReason is the user-facing explanation to surface when a run is
	// rejected. Safe to display: it is a static platform-authored string.
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	// JudgeCriteria is the rubric this plugin is judged by (U8). Before U8 the
	// engine used one shared SAFETY_RUBRIC for every plugin, which applies the
	// wrong yardstick to most plugin families (a successful prompt injection
	// often produces entirely harmless output). Empty means "no criteria
	// supplied" — the engine then falls back to its explicit unmapped rubric and
	// records judge_source=fallback:unmapped, so the downgrade is countable
	// rather than silent. omitempty keeps pre-U8 BFF payloads byte-identical.
	JudgeCriteria string `json:"judge_criteria,omitempty"`
	// JudgeSource identifies WHICH rubric was supplied: "plugin:<id>" for a
	// high-risk plugin's own rubric, "category:<key>" for a family rubric. It is
	// the traceable answer to "by what standard was this judged", and is
	// semantically distinct from judge_track ("who judged": platform|engine).
	JudgeSource string `json:"judge_source,omitempty"`
}

// EngineCatalogStrategy is one attack strategy as the engine names it. The
// engine only needs the label for display.
type EngineCatalogStrategy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// RequiresRemoteGeneration mirrors EngineCatalogPlugin's flag for the
	// strategy vocabulary (U6). Platform ids differ from promptfoo's for some
	// gated entries, so PromptfooID carries the upstream id.
	RequiresRemoteGeneration bool   `json:"requires_remote_generation,omitempty"`
	PromptfooID              string `json:"promptfoo_id,omitempty"`
	UnavailableReason        string `json:"unavailable_reason,omitempty"`
}

// EngineRunStatus is the GET /runs/:id response (safe fields only).
type EngineRunStatus struct {
	RunID         string            `json:"run_id"`
	Phase         EngineRunPhase    `json:"phase"`
	StatusText    string            `json:"status_text,omitempty"`
	PlannedCount  int               `json:"planned_count"`
	ExecutedCount int               `json:"executed_count"`
	CurrentStage  string            `json:"current_stage,omitempty"`
	DurationMs    int64             `json:"duration_ms"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	Result        *EngineSafeResult `json:"result,omitempty"`
	ErrorCode     string            `json:"error_code,omitempty"`
}

// EngineSafeResult mirrors SafeRunResult (counts/severities/redacted reasons).
type EngineSafeResult struct {
	Totals         EngineRunTotals      `json:"totals"`
	SeverityCounts map[string]int       `json:"severity_counts"`
	RiskCategories []EngineRiskCategory `json:"risk_categories"`
	PluginStats    []EnginePluginStat   `json:"plugin_stats"`
	StrategyStats  []EngineStrategyStat `json:"strategy_stats"`
	TokenUsage     EngineTokenUsage     `json:"token_usage"`
	// JudgeSources counts how many probes were judged by each rubric (U8).
	// This is the observability half of the fallback chain: a run that fell
	// back to the unmapped rubric for some probes says so here, per rubric,
	// instead of the whole run looking uniformly judged. Empty means the run
	// predates U8 — absence is not "everything used the generic rubric".
	JudgeSources []EngineJudgeSourceCount `json:"judge_sources,omitempty"`
}

// EngineJudgeSourceCount is one rubric's share of a run's judgements.
type EngineJudgeSourceCount struct {
	Source string `json:"source"`
	Probes int    `json:"probes"`
	// IsFallback marks the explicit unmapped tier so consumers do not have to
	// string-match "fallback:unmapped" to decide whether to warn the user.
	IsFallback bool `json:"is_fallback"`
}

type EngineRunTotals struct {
	Probes        int     `json:"probes"`
	AttackSuccess int     `json:"attack_success"`
	PassRate      float64 `json:"pass_rate"`
}

type EngineRiskCategory struct {
	Key            string                 `json:"key"`
	Label          string                 `json:"label"`
	Count          int                    `json:"count"`
	SeverityCounts map[string]int         `json:"severity_counts"`
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
	// JudgeSource (U8): which rubric judged this plugin's probes. Per-plugin
	// rather than per-run so a report reader can see that two plugins in the
	// same run were judged by different standards.
	JudgeSource string `json:"judge_source,omitempty"`
}

type EngineStrategyStat struct {
	ID            string  `json:"id"`
	Label         string  `json:"label"`
	Probes        int     `json:"probes"`
	AttackSuccess int     `json:"attack_success"`
	SuccessRate   float64 `json:"success_rate"`
	MaxSeverity   string  `json:"max_severity"`
}

type EngineTokenUsage struct {
	Generation int `json:"generation"`
	Judging    int `json:"judging"`
	Target     int `json:"target"`
}

// Sentinel errors surfaced to callers.
var (
	ErrEngineQueueFull    = errors.New("promptfoo_engine: queue full")
	ErrEngineRunExists    = errors.New("promptfoo_engine: run already exists")
	ErrEngineNotFound     = errors.New("promptfoo_engine: run not found")
	ErrEngineUnavailable  = errors.New("promptfoo_engine: service unavailable")
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
