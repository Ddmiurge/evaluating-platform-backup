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
	RunID          string `json:"run_id"`
	PlatformUserID string `json:"platform_user_id"`
	// Purpose is what the TESTED SYSTEM IS ("客服问答机器人：回答售后、退款、物流
	// 咨询"), not what the evaluation is for. promptfoo renders it into every
	// generation template and every grader rubric, so both the attacks and the
	// verdicts are aimed by it. Resolve it with EngineRunPurposeForTarget — an
	// evaluation purpose here is the defect that produced six meta-request
	// payloads and a 0/6 run on 2026-10-20.
	Purpose     string                   `json:"purpose"`
	NumTests    int                      `json:"num_tests"`
	Plugins     []EngineCapabilityRef    `json:"plugins"`
	Strategies  []EngineCapabilityRef    `json:"strategies"`
	JudgeMode   EngineJudgeMode          `json:"judge_mode"`
	Credentials EngineOneShotCredentials `json:"credentials"`
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

	// --- Phase 3 开篇 · 任务 2: expert-published YAML strategy packages ---

	// CustomStrategies carries the resolved strategy packages for this run. The
	// BFF has already applied admin governance, validated the document against
	// the schema the engine re-checks, and re-serialized it canonically — so
	// `Definition` is exactly the document that was validated, byte for byte.
	//
	// Shipped inline because that is how promptfoo consumes a strategy: a custom
	// strategy is `redteam.strategies[] = { id: "custom:<variant>", config:
	// { strategyText } }`. There is no file, no workdir entry and no `file://`
	// reference — on promptfoo 0.123.0 a file-based strategy is necessarily
	// JS/TS (`loadStrategy` enforces `isJavascriptFile`), which would be
	// arbitrary code execution inside the engine process. The yaml-native form
	// removes that path entirely rather than guarding it.
	CustomStrategies []EngineCustomStrategy `json:"custom_strategies,omitempty"`

	// CustomStrategyCode carries CODE strategy packages (`pf_strategy_form=js`).
	//
	// Separate from CustomStrategies because promptfoo loads the two through
	// different hooks: a text package is inlined as `custom:<variant>` +
	// strategyText, while a code package becomes a `file://` ref that promptfoo
	// imports. The engine writes each module into the per-run workdir under a
	// name derived from the validated variant, validates it before writing, and
	// normalises the file:// path back to a public id before anything reaches the
	// run record or the report (Step 0, 2026-10-28).
	CustomStrategyCode []EngineCustomStrategy `json:"custom_strategy_code,omitempty"`
}

type EngineCapabilityRef struct {
	ID     string                 `json:"id"`
	Config map[string]interface{} `json:"config,omitempty"`
}

// EngineMaxNumTests is the single cap on a run's num_tests, shared by both Go
// entry points (the wizard handler and the chat confirm fast path) so they
// cannot disagree about what is acceptable.
//
// A cap that differs per entry point is worse than a low cap: the wizard used
// to accept 200 while the engine silently clamped to 50, so a caller asking for
// 120 got 50 with no notice anywhere in the response. The number that runs must
// be the number that was requested, or the request must be refused.
//
// The engine keeps its own clamp as a last-resort backstop (nativeRedteam.ts),
// but it should be unreachable: both Go paths pre-validate.
const EngineMaxNumTests = 50

// EngineErrNumTestsTooLarge is the machine-readable refusal code for a supported
// over-limit request. It is shared by both entry points so the frontend maps one
// code, not two spellings of the same condition.
const EngineErrNumTestsTooLarge = "num_tests_too_large"

// EngineNumTestsTooLargeError refuses a count above EngineMaxNumTests.
//
// It exists so the chat confirm path REFUSES instead of clamping. The confirmed
// count is the number the user approved on the plan card; quietly running fewer
// rounds would make the report and the approval disagree, and the user would
// have no way to learn that the run they paid for was not the run they asked
// for. Refusing costs a re-confirmation; clamping costs trust in every count the
// platform reports.
type EngineNumTestsTooLargeError struct {
	Requested int
	Max       int
}

// Error implements error.
func (e *EngineNumTestsTooLargeError) Error() string {
	return fmt.Sprintf("num_tests %d 超过上限 %d；平台不会静默改成较小值，请重新确认执行轮次。", e.Requested, e.Max)
}

// EngineCatalog is the plugin/strategy catalog the BFF sends with each run so
// the engine classifies and scores results from a single source of truth
// (U2/T2.2). The engine looks every plugin_id up here instead of carrying its
// own regex/mapping tables for category and severity.
type EngineCatalog struct {
	Plugins    []EngineCatalogPlugin   `json:"plugins"`
	Strategies []EngineCatalogStrategy `json:"strategies"`
	// RejectedPlugins carries the plugin ids the platform has MEASURED
	// promptfoo to reject, as id -> reason (P2-2 layer L3).
	//
	// The engine refuses a request naming one of these with
	// `native_generation_plugin_id_invalid`. It is deliberately NOT "every id
	// absent from Plugins": the catalogue is the platform's OFFERED set, a
	// strict subset of what promptfoo accepts, and U8 already decided that an
	// unlisted-but-acceptable plugin still runs (judged by fallback:unmapped so
	// the gap stays visible). Rejecting on catalogue membership would refuse
	// valid plugins — `sql-injection` / `shell-injection` are the measured
	// examples, and the plan treats those as catalogue gaps to fill rather than
	// as runs to block.
	//
	// The verdict is shipped rather than re-derived in the engine because it is
	// only knowable from promptfoo's zod schema, which lives behind a
	// content-hashed chunk and a minified export; resolving that at engine boot
	// would break on every promptfoo upgrade. This mirrors U6's
	// generated-table-plus-CI-pin discipline.
	//
	// omitempty keeps pre-P2-2 payloads byte-identical on the wire.
	RejectedPlugins map[string]string `json:"rejected_plugin_ids,omitempty"`
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
	// Fanout is how many redteam sub-plugins this id expands into (P2-4):
	// `harmful` -> 26, `pii` -> 4, `bias` -> 6, everything else 1. promptfoo
	// applies `numTests` to each EXPANDED sub-plugin, so this is the weight the
	// engine's "platform num_tests means a TOTAL" allocator splits by — without
	// it the engine would have to guess, and a wrong guess is a silent cost
	// change (the documented worst case is 50 x 2950 probes).
	//
	// Measured, never hand-set: `PluginFanout` reads the generated legality
	// table. omitempty keeps pre-P2-4 payloads unchanged on the wire.
	Fanout int `json:"fanout,omitempty"`
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
	// ErrorMessage is the engine-side redacted diagnosis for a terminal failure
	// (P2-5). It is what `engine_runs.error_brief` was always meant to hold: before
	// it existed the column had a writer nowhere in the chain, so the promptfoo
	// exception explaining a failure died with the engine process.
	//
	// Redacted by the engine AND again by ApplyStatus before storage — see
	// engine_error_brief.go for why the platform does not trust the engine's copy.
	ErrorMessage string `json:"error_message,omitempty"`
}

// EngineSafeResult mirrors SafeRunResult (counts/severities/redacted reasons).
type EngineSafeResult struct {
	// GenerationPath is the path that produced the run: "plan_a" (platform-synthesised
	// prompts, promptfoo in evaluate mode) or "plan_c" (promptfoo's native redteam
	// machinery). P2-6 — the two are not comparable and looked identical before this
	// field existed. Fixed enum, safe to store and render.
	GenerationPath string `json:"generation_path,omitempty"`
	// CodeStrategyCount / CodeStrategyIDs name the expert CODE strategy packages
	// that executed during this evaluation. Count + public ids only — the report
	// states the trust level of its numbers, never what the code contained.
	CodeStrategyCount int      `json:"code_strategy_count,omitempty"`
	CodeStrategyIDs   []string `json:"code_strategy_ids,omitempty"`
	// NumTestsEffective is the probe count actually configured, after the platform
	// total was divided across the selection (P2-4). Distinct from
	// `totals.probes`, which counts results that came back.
	NumTestsEffective int                  `json:"num_tests_effective,omitempty"`
	Totals            EngineRunTotals      `json:"totals"`
	SeverityCounts    map[string]int       `json:"severity_counts"`
	RiskCategories    []EngineRiskCategory `json:"risk_categories"`
	PluginStats       []EnginePluginStat   `json:"plugin_stats"`
	StrategyStats     []EngineStrategyStat `json:"strategy_stats"`
	TokenUsage        EngineTokenUsage     `json:"token_usage"`
	// CaseResults is the per-probe text (2026-10-16). Absent means the engine did
	// not report any — a pre-change engine, or a run that produced no probes —
	// which is a different statement from "this run attacked nothing", so the
	// report must say "未记录" rather than render an empty section.
	CaseResults []EngineCaseResult `json:"case_results,omitempty"`
	// JudgeSources counts how many probes were judged by each rubric (U8).
	// This is the observability half of the fallback chain: a run that fell
	// back to the unmapped rubric for some probes says so here, per rubric,
	// instead of the whole run looking uniformly judged. Empty means the run
	// predates U8 — absence is not "everything used the generic rubric".
	JudgeSources []EngineJudgeSourceCount `json:"judge_sources,omitempty"`
	// ToolCallSignals (U9): probes whose target response carried a tool-call
	// marker. Side-effect visibility for agent targets — a run pointed at a
	// production agent is otherwise indistinguishable from one pointed at a
	// sandbox in every other number here.
	//
	// Two integers and nothing else, deliberately: tool names and arguments are
	// attacker-influenced content, which AGENTS.md forbids from reports. A
	// detector that kept them would be a leak wearing a safety label. Absent
	// means the engine did not report it (pre-U9 engine, or a run whose result
	// was persisted before the field existed) — NOT "no tool was used".
	ToolCallSignals *EngineToolCallSignals `json:"tool_call_signals,omitempty"`
}

// EngineToolCallSignals is U9's tool-call visibility counters.
type EngineToolCallSignals struct {
	// ProbesWithToolCalls counts probes whose response carried at least one
	// recognised tool-call marker.
	ProbesWithToolCalls int `json:"probes_with_tool_calls"`
	// TotalToolCallMarkers is the sum of markers across those probes.
	TotalToolCallMarkers int `json:"total_tool_call_markers"`
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

// EngineCaseResult is one probe's text, as the engine reported it.
//
// # Why the platform now receives this (2026-10-16)
//
// Until this field existed, an engine run reached the report as aggregates only
// — "插件 harmful 共执行 26 条探针，攻击成功 3 条" — so a user asking "did it leak
// the system prompt?" had no way to find out. The product decision is that the
// attack and the answer are the report's payload
// (see audit-2026-10-09/23-聊天链路报告原文与promptfoo对齐.md).
//
// The engine side carries a matching doc on `SafeCaseResult`; the field names are
// identical on purpose so neither side needs a translation table that could drift.
type EngineCaseResult struct {
	// Index is 1-based, matching the report's 测试项 numbering.
	Index    int    `json:"index"`
	PluginID string `json:"plugin_id"`
	// StrategyID is "none" when no strategy wrapped this probe — the same
	// sentinel `strategy_stats` uses, so the report must NOT render it as a
	// strategy name.
	StrategyID    string `json:"strategy_id"`
	Severity      string `json:"severity"`
	AttackSuccess bool   `json:"attack_success"`
	// Prompt is the attack prompt as the target received it — EXCEPT when
	// promptfoo redacted it on the way into its database (see OriginalPrompt).
	Prompt string `json:"prompt"`
	// OriginalPrompt is the pre-strategy payload promptfoo recorded in
	// `metadata.originalText`.
	//
	// It exists because promptfoo's secret heuristic (`^[a-zA-Z0-9+/=_-]{64,}$`)
	// classifies a base64-encoded payload as a credential and replaces it with
	// `[REDACTED]` before the row is written — and the engine reads results back
	// from that row. The pre-transform text is prose and survives, so it is the
	// only remaining evidence of what the probe was about.
	//
	// It is a FALLBACK, not the sent bytes: the report labels it as such.
	OriginalPrompt string `json:"original_prompt,omitempty"`
	// Response is the target's answer, verbatim.
	Response string `json:"response"`
	// Error is the target call's failure, when the call did not complete
	// (2026-10-21). Present means the probe produced NO answer: the target
	// refused the connection, answered non-2xx, or answered with a body the
	// configured extraction path could not resolve.
	//
	// It exists because those three cases used to arrive as `output: undefined`
	// with `error: null` — the report rendered 目标回答 as `{}` and promptfoo's
	// prompt-extraction rubric graded an empty answer as a PASS, so a target that
	// never answered produced 0/6 successes and a 「最高安全 100/100」 report.
	// See probeAnswerMissing.
	Error string `json:"error,omitempty"`
	// JudgeReason is the grader's reasoning, verbatim.
	JudgeReason string `json:"judge_reason"`
	// Score is promptfoo's own grading score, absent when its grader produced
	// none. This is NOT the platform judge's score_0_to_5; the report renders it
	// only for engine-judged runs, so the two never share a document.
	Score *float64 `json:"score,omitempty"`
	// Turns is the multi-turn transcript when the strategy held a conversation.
	Turns []EngineCaseTurn `json:"turns,omitempty"`
}

// EngineCaseTurn is one exchange in a multi-turn attack.
type EngineCaseTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

// CodeStrategyProbeResult is the verdict of the engine's publication-time check
// for a CODE strategy module (design §6.1). It carries a code and one short
// message — never the module's output, a stack, or a path.
type CodeStrategyProbeResult struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ValidateCodeStrategy asks the engine whether a module loads and transforms one
// synthetic test case.
//
// # Why the BFF calls this before writing the publication record
//
// The BFF's own checks (size, capability tokens, shape) are cheap and
// deterministic, but none of them can answer "does this module actually load".
// Left to the run, a module that fails to load fails after the user has paid for
// the probes, and reads as a mid-run engine failure. The probe moves that to
// publish time.
//
// A disabled engine, an unreachable engine, or a disabled capability is returned
// as an error so the caller can decide (the current policy: refuse the publish,
// because accepting an unverified module would defeat the point of the check).
func (c *PromptfooEngineClient) ValidateCodeStrategy(ctx context.Context, source string) (*CodeStrategyProbeResult, error) {
	if !c.Enabled() {
		return nil, ErrEngineUnavailable
	}
	body, err := json.Marshal(map[string]string{"source": source})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/strategy-packages/validate", bytes.NewReader(body))
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
	case http.StatusOK, http.StatusUnprocessableEntity, http.StatusConflict:
		var out CodeStrategyProbeResult
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("promptfoo_engine: decode strategy probe: %w", err)
		}
		return &out, nil
	case http.StatusUnauthorized:
		return nil, ErrEngineUnauthorized
	default:
		return nil, fmt.Errorf("promptfoo_engine: strategy probe failed with status %d", resp.StatusCode)
	}
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
