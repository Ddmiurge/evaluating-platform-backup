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
}

/** One plugin family as classified by the backend catalog. */
export interface EngineCatalogPlugin {
  id: string;
  category: string;
  category_label: string;
  severity: string;
}

/** One attack strategy as named by the backend catalog. */
export interface EngineCatalogStrategy {
  id: string;
  name: string;
}

/** Plugin/strategy catalog shipped with a run request. */
export interface EngineCatalog {
  plugins: EngineCatalogPlugin[];
  strategies: EngineCatalogStrategy[];
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
  }>;
  strategy_stats: Array<{
    id: string;
    label: string;
    probes: number;
    attack_success: number;
    success_rate: number;
    max_severity: 'critical' | 'high' | 'medium' | 'low' | 'none';
  }>;
  token_usage: {
    generation: number;
    judging: number;
    target: number;
  };
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
}
