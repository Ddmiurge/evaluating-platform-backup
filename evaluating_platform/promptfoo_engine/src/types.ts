/**
 * Types shared across the engine adapter: run request/response contracts.
 * These mirror the BFF-side Go structs in internal/maclaw/promptfoo_engine_client.go.
 */

export type RunPhase =
  | 'queued'
  | 'generating_tests'
  | 'executing_probes'
  | 'engine_judging'
  | 'compiling_report'
  | 'succeeded'
  | 'failed'
  | 'canceled';

export type JudgeMode = 'auto' | 'promptfoo_native' | 'platform_rejudge';

/** One-shot credentials materialized by the BFF; never logged, never persisted. */
export interface OneShotCredentials {
  target: {
    /** OpenAI-compatible base URL, e.g. https://llm.example.com/v1 */
    base_url: string;
    model: string;
    api_key: string;
    /** Optional HTTP headers to forward to the target. */
    headers?: Record<string, string>;
    /** Max concurrent target calls (normalized to <=50 by BFF). */
    concurrency?: number;
    /** Request timeout in seconds. */
    timeout_seconds?: number;
    /** Agent target (kind=agent): custom HTTP template fields, mirroring
     * promptfoo's http provider. {{prompt}} stays in the body template for
     * promptfoo's nunjucks substitution; {{api_key}} is substituted
     * server-side inside the engine so the secret never leaves the process. */
    agent_endpoint?: string;
    agent_method?: string;
    agent_headers_template?: string;
    agent_body_template?: string;
    agent_response_path?: string;
  };
  /** Generation LLM (attack synthesis). Falls back to target creds when absent. */
  generation?: {
    base_url: string;
    model: string;
    api_key: string;
  };
}

export interface RunRequest {
  /** Platform-side run id (engine does not generate ids). */
  run_id: string;
  platform_user_id: string;
  purpose: string;
  num_tests: number;
  plugins: Array<{ id: string; config?: Record<string, unknown> }>;
  strategies: Array<{ id: string; config?: Record<string, unknown> }>;
  judge_mode: JudgeMode;
  credentials: OneShotCredentials;
  /**
   * Plugin/strategy classification catalog pushed by the BFF (U2/T2.2). This is
   * the single source of truth for a plugin's risk category, Chinese label, and
   * default display severity; the engine never keeps its own regex/mapping
   * tables. Optional for backward compatibility — when absent the engine falls
   * back to its "unknown plugin" defaults (category=other / severity=medium).
   */
  catalog?: EngineCatalog;
  /**
   * U7 · What the application under test actually is.
   *
   * promptfoo's library entrypoint never reads an
   * `applicationDefinition` key — that field is only honoured by the CLI
   * (`dist/src/main.js`), and `RedteamConfigSchema` does not even declare it, so
   * a config carrying it would have the key silently stripped by zod.
   *
   * The engine therefore treats this as the platform's *own* structured
   * description of the target and renders it into the promptfoo `purpose`
   * string, which IS the channel the library honours
   * (`resolveEffectivePurpose` → `redteamConfig.purpose`). The generation
   * paradigm still belongs to promptfoo; this is only the "what am I attacking"
   * input its plugin templates interpolate.
   *
   * Every field is optional: a thin `purpose` alone degrades to the old
   * generic-attack behaviour rather than failing.
   */
  application_definition?: ApplicationDefinition;
  /**
   * U7 · Extra generation constraints (language, domain, things to avoid).
   * Maps onto promptfoo's `redteam.testGenerationInstructions`, which is passed
   * to every plugin's generation call.
   */
  test_generation_instructions?: string;
  /**
   * U7 · The prompt variable promptfoo injects generated payloads into.
   *
   * Without it promptfoo infers a name from the prompt template, and every
   * strategy that rewrites the payload (encoding, multi-turn, …) has to guess
   * where to write. Defaults to `prompt`, which is what the engine's own
   * single-variable prompt template uses.
   */
  inject_var?: string;
  /**
   * U10 · Expert-published YAML plugin packages, resolved and governed by the
   * BFF.
   *
   * The BFF has already applied admin governance (a disabled or archived
   * package never reaches this list), validated the YAML against promptfoo's
   * custom-plugin schema, and re-serialized it canonically — so `definition` is
   * byte-identical to the document that was validated. The engine writes each
   * one into its per-run workdir and points promptfoo at it via `file://`.
   *
   * (Until P3-3 this said "Plan C only": Plan A synthesised the attack prompts
   * itself and never invoked promptfoo's plugin machinery, so a package's
   * `generator` template would have been silently ignored — a green run with the
   * expert's capability absent. Plan A is gone, so the qualifier is too; the
   * mechanism is unchanged.)
   */
  custom_plugins?: EngineCustomPlugin[];

  /**
   * Phase 3 开篇 · 任务 2 · Expert-published strategy packages.
   *
   * Unlike `custom_plugins` there is no workdir file and no `file://`
   * reference: a custom strategy is inlined into `redteam.strategies` as
   * `{ id: "custom:<variant>", config: { strategyText } }`, which is the only
   * form promptfoo accepts without executing code (a `file://` strategy is
   * necessarily `.js`/`.ts` — `loadStrategy` enforces `isJavascriptFile`).
   *
   * (The "Plan C only" qualifier this comment used to carry is gone with Plan A:
   * it synthesised attack prompts itself and never invoked promptfoo's strategy
   * machinery, so a package's `strategyText` would have been silently ignored.)
   */
  custom_strategies?: EngineCustomStrategy[];
}

/**
 * Phase 3 开篇 · 任务 2 · One published strategy package, as shipped by the BFF.
 *
 * `handle` is the publication handle admin governance acts on and the package's
 * identity; `strategy_id` is the `custom:<variant>` id promptfoo receives;
 * `definition` is the canonical YAML, inlined rather than written to disk.
 */
export interface EngineCustomStrategy {
  handle: string;
  strategy_id: string;
  definition: string;
  source_expert_user_id?: string;
  version?: string;
}

/**
 * U10 · One published package, as shipped by the BFF.
 *
 * `handle` is the publication handle admin governance acts on; `plugin_id` is
 * the package's own id (or promptfoo's default) and is what the report shows.
 * `definition` is the canonical YAML — promptfoo loads it from a file the
 * engine writes, never from this string directly.
 */
export interface EngineCustomPlugin {
  handle: string;
  plugin_id: string;
  definition: string;
  source_expert_user_id?: string;
  version?: string;
}

/**
 * U7 · The six application-description fields promptfoo's own docs define for
 * `applicationDefinition`, kept as named fields rather than a free-form blob so
 * the BFF can validate each one and so a missing one is visible in review.
 *
 * Field names are snake_case to match the engine's wire contract
 * (`internal/maclaw.EngineApplicationDefinition`'s JSON tags) — the whole
 * RunRequest is snake_case, and a camelCase spelling here would silently
 * deserialize as `undefined` on both sides rather than fail loudly.
 */
export interface ApplicationDefinition {
  purpose?: string;
  features?: string;
  has_access_to?: string;
  does_not_have_access_to?: string;
  user_types?: string;
  security_requirements?: string;
}

/** One plugin family as classified by the backend catalog. */
export interface EngineCatalogPlugin {
  id: string;
  category: string;
  category_label: string;
  severity: string;
  /**
   * U6: the backend flags plugins whose attack cases promptfoo can only fetch
   * from its remote generation service. This deployment is fail-closed on that
   * channel (PROMPTFOO_DISABLE_REMOTE_GENERATION=1), and promptfoo responds by
   * `return []`-ing mid-run — a green run with 0 coverage for that plugin. The
   * engine therefore refuses such a run up front with a readable error code.
   */
  requires_remote_generation?: boolean;
  /** User-facing explanation to surface when the run is rejected. */
  unavailable_reason?: string;
  /**
   * P2-4: how many redteam sub-plugins this id expands into — `harmful` 26,
   * `pii` 4, `bias` 6, everything else 1.
   *
   * promptfoo applies `numTests` to each EXPANDED sub-plugin, so this is the
   * weight `allocateNumTests` splits the platform's total by. Absent means "not
   * supplied by the backend"; the allocator then weighs it 1, which is the
   * documented convention for `file://` plugins (they never expand) rather than a
   * guess that would under-weight a collection.
   */
  fanout?: number;
  /**
   * U8: the rubric this plugin must be judged by. Before U8 the engine used a
   * single shared SAFETY_RUBRIC for all 119 plugins, which applies the wrong
   * yardstick to most families — a successful prompt injection typically
   * produces entirely harmless output, so a "was the output harmful?" rubric
   * scores it as safe.
   *
   * Absent means the BFF did not supply a standard (pre-U8 BFF, or a plugin
   * outside the catalog). The engine then uses its own explicitly-named
   * unmapped rubric and reports `judge_source: "fallback:unmapped"` — counted,
   * never silent.
   */
  judge_criteria?: string;
  /**
   * U8: *which* rubric was supplied — `plugin:<id>` or `category:<key>`.
   * Semantically distinct from judge_track ("who judged"), and this is the
   * traceable answer to "by what standard was this judged".
   */
  judge_source?: string;
}

/** One attack strategy as named by the backend catalog. */
export interface EngineCatalogStrategy {
  id: string;
  name: string;
  /** U6: same remote-generation gate as EngineCatalogPlugin. */
  requires_remote_generation?: boolean;
  /** promptfoo's own id for this strategy (differs for gated entries). */
  promptfoo_id?: string;
  /** User-facing explanation to surface when the run is rejected. */
  unavailable_reason?: string;
}

/** Plugin/strategy catalog shipped with a run request. */
export interface EngineCatalog {
  plugins: EngineCatalogPlugin[];
  strategies: EngineCatalogStrategy[];
  /**
   * P2-2 (L3): plugin ids the platform has MEASURED promptfoo to reject, as
   * id -> reason. The engine refuses a request naming one of these; see
   * `assertPluginIdsAreAccepted`.
   *
   * Deliberately not "every id absent from `plugins`" — see that function for
   * why catalogue membership is the wrong test.
   */
  rejected_plugin_ids?: Record<string, string>;
}

/** Safe progress event emitted over SSE. No prompts, no responses, no secrets. */
export interface RunProgressEvent {
  run_id: string;
  phase: RunPhase;
  status_text?: string;
  planned_count?: number;
  executed_count?: number;
  current_stage?: string;
  duration_ms?: number;
}

/** Safe terminal summary. Raw results stay inside the engine workdir and die with it. */
export interface SafeRunResult {
  /**
   * P2-6 · which generation path produced this run: `plan_a` (the platform's own
   * LLM synthesised the attack prompts) or `plan_c` (promptfoo's native redteam
   * machinery did).
   *
   * P3-3 deleted Plan A, so every run the engine produces now reports `plan_c`.
   * The type keeps `plan_a` for REPORTS FROM HISTORY: they still need to say so,
   * and dropping the variant would make an old report unable to explain itself.
   *
   * The two produce reports that ARE NOT comparable — different probe counts for
   * the same input, different strategies, and for a while a different judging
   * basis — while looking identical. Item 14 of the U7 acceptance run recorded
   * exactly that: Plan A 50 cases vs Plan C 80 probes at the same settings, with
   * nothing on either report saying so.
   *
   * Optional because a run reduced by older code has no answer; absent means
   * "not recorded", and the report says so rather than picking one.
   */
  generation_path?: 'plan_a' | 'plan_c';
  /**
   * P2-6 · how many probes the run was configured for, after the platform total
   * was translated into per-plugin counts (P2-4).
   *
   * Distinct from `totals.probes`, which counts results that came back: a run
   * configured for 50 whose target errored on 3 probes executed 47. Keeping both
   * is what makes "the user asked for 50 and got N" answerable from the record.
   */
  num_tests_effective?: number;
  totals: {
    probes: number;
    attack_success: number;
    pass_rate: number;
  };
  severity_counts: Record<'critical' | 'high' | 'medium' | 'low', number>;
  risk_categories: Array<{
    key: string;
    label: string;
    count: number;
    severity_counts: Record<'critical' | 'high' | 'medium' | 'low', number>;
    plugins: Array<{
      plugin_id: string;
      probes: number;
      attack_success: number;
      top_reasons: string[];
    }>;
  }>;
  plugin_stats: Array<{
    plugin_id: string;
    label: string;
    probes: number;
    attack_success: number;
    success_rate: number;
    max_severity: 'critical' | 'high' | 'medium' | 'low' | 'none';
    /** U8: which rubric judged this plugin's probes. */
    judge_source?: string;
  }>;
  /**
   * U8: probes per rubric, so a report reader can see that this run was not
   * judged by one uniform standard — and can spot a run that fell back to the
   * unmapped rubric. Absent on pre-U8 results; absence is NOT "everything used
   * the generic rubric".
   */
  judge_sources?: Array<{
    source: string;
    probes: number;
    is_fallback: boolean;
  }>;
  strategy_stats: Array<{
    id: string;
    label: string;
    probes: number;
    attack_success: number;
    success_rate: number;
    max_severity: 'critical' | 'high' | 'medium' | 'low' | 'none';
  }>;
  /**
   * U9 · Probes whose target response carried a tool-call marker.
   *
   * Side-effect visibility for agent targets: a run pointed at a production
   * agent looks identical to one pointed at a sandbox in every other number
   * here, so this is the only signal that says "the target reached for a tool
   * during this run".
   *
   * **A count and a boolean, never tool arguments.** Tool arguments are
   * attacker-influenced content, which AGENTS.md forbids from reports — a
   * detector that kept them would be a leak wearing a safety label. Detection
   * is also explicitly a **heuristic** over common response shapes, not a
   * guarantee that every tool invocation is caught; absent means "no marker was
   * recognised", NOT "no tool was used".
   */
  tool_call_signals?: {
    probes_with_tool_calls: number;
    total_tool_call_markers: number;
  };
  token_usage: {
    generation: number;
    judging: number;
    target: number;
  };
  /**
   * Per-probe detail: the attack prompt, the target's answer, and the verdict.
   *
   * # This field reverses a contract on purpose (2026-10-16)
   *
   * Everything above is an aggregate, and the module header of `redteamRun.ts`
   * used to promise that "raw prompts/responses never leave this module". That
   * promise is why a report could show `插件 harmful 共执行 26 条探针，攻击成功 3 条`
   * and nothing else — a user asking "did it leak the system prompt?" had no way
   * to find out. The product decision (see `23-聊天链路报告原文与promptfoo对齐.md`)
   * is that a red-team report without the actual attack and the actual answer is
   * not a report; the payload IS the deliverable.
   *
   * So the promise is narrowed rather than dropped: raw text leaves **only** on
   * the terminal result, **only** through this field, and never through logs,
   * `error_brief`, or progress events.
   *
   * # Why it has to be collected here
   *
   * promptfoo writes every prompt and response to its own SQLite database
   * (`eval_results`) and the generated attack prompts to `<workdir>/redteam.yaml`.
   * The engine deletes both on every terminal path (P2-3), so there is no
   * "read it back afterwards" — the text must be lifted out at reduction time,
   * from the Eval objects still in memory.
   *
   * # Still forbidden here
   *
   * Credentials, tokens, target secrets, filesystem paths, and the platform
   * judge's raw values (`score_0_to_5` / `refusal_detected`) stay out. The only
   * "score" below is promptfoo's own `gradingResult.score`, which is what decided
   * this probe — see the field doc.
   */
  case_results?: SafeCaseResult[];
}

/**
 * One probe, with its text.
 *
 * Field names follow the aggregate vocabulary already in this file
 * (`attack_success`, `plugin_id`, `strategy_id`) so the report layer does not
 * have to translate a second dialect — and so `attack_success` keeps its one
 * meaning project-wide: promptfoo's `pass === false`, i.e. the target failed to
 * defend.
 */
export interface SafeCaseResult {
  /** 1-based position in the run, matching the report's 测试项 numbering. */
  index: number;
  plugin_id: string;
  /** `none` when no strategy wrapped this probe — same sentinel as `strategy_stats`. */
  strategy_id: string;
  severity: 'critical' | 'high' | 'medium' | 'low' | 'none';
  /** Whether the attack succeeded. Same polarity as `totals.attack_success`. */
  attack_success: boolean;
  /** The attack prompt as the target received it. */
  prompt: string;
  /** The target's answer, verbatim. */
  response: string;
  /** The grader's reasoning, verbatim (the aggregate keeps a 200-char redacted copy). */
  judge_reason: string;
  /**
   * promptfoo's own grading score (0..1), when its grader produced one.
   *
   * This is NOT the platform judge's `score_0_to_5` — different producer,
   * different scale, different meaning. The report renders this one only for
   * engine-judged runs (`judge_track=engine`), so the two can never appear in
   * the same document and a reader never has to work out which they are looking
   * at.
   */
  score?: number;
  /**
   * Multi-turn transcript, when the strategy held a conversation (`crescendo`,
   * `goat`, `hydra`, the iterative family). Absent for single-turn probes, which
   * is also promptfoo's own fallback shape — it reads
   * `metadata.redteamHistory ?? metadata.redteamTreeHistory` and renders one
   * exchange when neither exists.
   *
   * Without this, a multi-turn attack report shows only its last turn, which is
   * the turn least likely to look like an attack.
   */
  turns?: Array<{ role: 'user' | 'assistant'; content: string }>;
}

export interface RunStatusResponse {
  run_id: string;
  phase: RunPhase;
  status_text?: string;
  planned_count: number;
  executed_count: number;
  current_stage?: string;
  duration_ms: number;
  created_at: string;
  updated_at: string;
  result?: SafeRunResult;
  error_code?: string;
  /**
   * P2-5 · redacted diagnosis for a terminal failure, carried out of the engine so
   * the platform can persist it as `engine_runs.error_brief`.
   *
   * Before this existed the platform stored nothing and the promptfoo exception
   * died with the process, which is why two U7 findings could not be located
   * without re-running them inside the container. Already redacted by
   * `redactEngineErrorBrief`; the platform redacts again before storing, because a
   * single missed channel is how endpoints and credentials reach a database.
   */
  error_message?: string;
}
