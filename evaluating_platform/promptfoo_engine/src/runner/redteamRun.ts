/**
 * Runner support shared by the red-team execution path.
 *
 * # This file no longer chooses a generation path
 *
 * It used to. Plan A (`generateTests.ts`: the platform's own generation LLM
 * synthesised the attack prompts, promptfoo ran in evaluate mode) was deleted in
 * P3-3, together with the `PROMPTFOO_ENGINE_NATIVE_GENERATION` switch that chose
 * between them. The remaining path — promptfoo's native `redteam.run()` — owns
 * generation outright (plugin templates, entity extraction, strategy transforms,
 * graders).
 *
 * What lives here is what BOTH paths used and the native path still needs: the
 * target-provider construction, the agent-conversation placeholder handling, and
 * the reduction from a promptfoo `Eval` into the platform-safe `SafeRunResult`.
 * The name is kept because renaming it would touch every importer for a cosmetic
 * gain — but nothing in this file selects a path any more.
 *
 * Library surface used (verified against dist of promptfoo v0.123.0):
 *   import promptfoo from 'promptfoo';
 *   promptfoo.evaluate(testSuite, options)     // Plan A
 *   promptfoo.redteam.run({ config, ... })     // Plan C
 *     - Plan A returns an Eval whose store.results carry gradingResult and
 *       testCase.metadata.
 *   - Plan C takes a *path to a YAML config document*; its options object has no
 *     plugins/provider/purpose fields (see nativeRedteam.ts for the details).
 *
 * The Eval results are reduced into a SafeRunResult (counts/severity/reasons
 * only). Raw prompts/responses never leave this module.
 */
import promptfoo from 'promptfoo';
/**
 * Minimal structural type of the promptfoo Eval object we consume. evaluate()
 * returns the Eval instance itself; in library mode (writeLatestResults off)
 * results are in-memory EvalResult objects on the `results` array.
 *
 * Exported because Plan C's `loadEvalResults` has to name it: that function
 * takes an `Eval` whose `results` may be empty and rehydrates it (U8).
 */
export interface EvalLike {
  id?: string;
  results?: Array<{
    gradingResult?: { pass?: boolean; severity?: unknown; reason?: unknown };
    testCase?: { metadata?: Record<string, unknown> };
    /**
     * U9 · The provider's response. `raw` is the parsed JSON body the http
     * provider received — the only place tool-call markers can be observed
     * without re-reading the target (which U9 must never do: a second request
     * would double the side effects the run is already warning about).
     *
     * Optional because the reducer is also fed hand-built fixtures in tests and
     * by Plan A's in-memory `evaluate()` results; absence means "no response
     * object to inspect", which is reported as *no signal* rather than as
     * "no tool was used" (see `countToolCallSignals`).
     */
    response?: { raw?: unknown; output?: unknown };
  }>;
}
import type { EngineCatalog, OneShotCredentials, RunProgressEvent, SafeRunResult } from '../types.js';
import { redactReason, redactTopReasons, sanitizeSeverity } from '../sanitize/redact.js';
import type { RunRecord } from '../manager/taskManager.js';
import { EngineRunError } from './engineRunError.js';
import { buildJudgeRubricMap, PLUGIN_JUDGE_SOURCE_METADATA_KEY, resolveJudgeRubric, UNMAPPED_JUDGE_SOURCE } from './judgeRubric.js';
import {
  buildSessionParser,
  countToolCallSignals,
  renderConversationPlaceholders,
  wantsAgentConversation,
} from './agentSession.js';

type Emit = (event: RunProgressEvent) => void;

/** Severity ranking for max_severity computation. */
const SEV_ORDER: Record<string, number> = { critical: 4, high: 3, medium: 2, low: 1, none: 0 };

/** The four display severities (sanitizeSeverity drops everything else). */
type Severity = 'critical' | 'high' | 'medium' | 'low';

/** A plugin's classification, resolved from the backend catalog. */
interface CatalogClassification {
  category: string;
  categoryLabel: string;
  severity: Severity;
}

/**
 * Fallback classification for a plugin_id the catalog does not know (U2/T2.2):
 * the engine never guesses a family from the id — it reports it honestly as
 * 'other' with a medium severity.
 */
const UNKNOWN_PLUGIN_CLASSIFICATION: CatalogClassification = {
  category: 'other',
  categoryLabel: '其他',
  severity: 'medium',
};

/** Clamp a catalog severity string to the known set, defaulting to 'medium'. */
function normalizeCatalogSeverity(raw: unknown): Severity {
  return raw === 'critical' || raw === 'high' || raw === 'medium' || raw === 'low' ? raw : 'medium';
}

/**
 * Build a plugin_id → classification lookup from the catalog pushed by the BFF.
 * Missing/invalid catalog → an empty map, so every plugin falls back to
 * UNKNOWN_PLUGIN_CLASSIFICATION rather than to a regex guess.
 */
function buildCatalogMap(catalog?: EngineCatalog): Map<string, CatalogClassification> {
  const map = new Map<string, CatalogClassification>();
  for (const plugin of catalog?.plugins ?? []) {
    if (!plugin || typeof plugin.id !== 'string' || !plugin.id.trim()) continue;
    const category = typeof plugin.category === 'string' && plugin.category.trim() ? plugin.category : 'other';
    const categoryLabel =
      typeof plugin.category_label === 'string' && plugin.category_label.trim() ? plugin.category_label : '其他';
    map.set(plugin.id, { category, categoryLabel, severity: normalizeCatalogSeverity(plugin.severity) });
  }
  return map;
}

/**
 * Normalize an OpenAI-compatible base URL (with or without trailing slash or
 * /chat/completions) into the full chat-completions endpoint the http
 * provider POSTs to.
 */
function toChatCompletionsURL(raw: string): string {
  const u = raw.trim().replace(/\/+$/, '');
  if (!u) return raw;
  return /\/chat\/completions$/.test(u) ? u : `${u}/chat/completions`;
}

const AGENT_API_KEY_PLACEHOLDER = '{{api_key}}';

/** Substitute {{api_key}} server-side; the secret stays inside the engine. */
function substituteAgentAPIKey(template: string, apiKey: string): string {
  if (!template || !apiKey) return template;
  return template.split(AGENT_API_KEY_PLACEHOLDER).join(apiKey);
}

/**
 * Convert a user-facing dot path (e.g. `reply.text`, `choices[0].message.content`,
 * `json.data.reply`) into the transformResponse expression promptfoo's http
 * provider expects (e.g. `json.reply.text`). Empty path → the whole JSON body.
 */
export function toAgentTransformResponse(raw?: string): string {
  const path = (raw ?? '')
    .trim()
    .replace(/^json\./i, '')
    .replace(/^\.+/, '')
    .replace(/\.+$/, '');
  if (!path) return 'json';
  return `json.${path}`;
}

/**
 * Build the promptfoo target provider from one-shot credentials. LLM targets
 * get the OpenAI chat/completions http provider; agent targets (custom HTTP
 * endpoints) get a fully-templated http provider where only {{prompt}} is
 * left for promptfoo's nunjucks substitution — {{api_key}} is substituted
 * here, server-side, so the credential never appears in config that could be
 * serialized anywhere.
 *
 * U9: when the agent body/header template contains `{{conversation_id}}`, two
 * more things happen here, and this function is the **only** place they do:
 *
 *  - the operator-facing `{{conversation_id}}` is rewritten to the
 *    `{{sessionId}}` name promptfoo actually binds (see agentSession.ts for why
 *    the engine translates rather than re-implementing promptfoo's session
 *    support), and
 *  - a `sessionParser` is derived from `agent_response_path`, so the id the
 *    target minted on turn 1 comes back out of turn 1's response.
 *
 * Both are gated on the placeholder being present, which is what makes "leave
 * the new configuration empty ⇒ v1 behaviour" true **by construction** rather
 * than by a flag: with no placeholder, `sessionParser` is never emitted and the
 * emitted config is byte-identical to U8's.
 */
export function buildTargetProvider(target: OneShotCredentials['target']): {
  id: string;
  config: Record<string, unknown>;
} {
  if (target.agent_endpoint && target.agent_endpoint.trim()) {
    const apiKey = (target.api_key ?? '').trim();
    const headers: Record<string, string> = {};
    let usedAPIKey = false;
    const rawHeaders = (target.agent_headers_template ?? '').trim();
    if (rawHeaders) {
      const parsed = JSON.parse(rawHeaders) as Record<string, unknown>;
      for (const [name, value] of Object.entries(parsed)) {
        if (typeof value !== 'string' || !name.trim()) continue;
        if (value.includes(AGENT_API_KEY_PLACEHOLDER)) usedAPIKey = true;
        const rendered = substituteAgentAPIKey(value, apiKey);
        if (rendered.trim()) headers[name.trim()] = rendered;
      }
    }
    if (!headers['Content-Type']) headers['Content-Type'] = 'application/json';
    // Zero-config auth: credential set but templates never reference
    // {{api_key}} → default bearer header (OpenAI-compatible gateways).
    if (apiKey && !usedAPIKey && !headers['Authorization']) {
      headers['Authorization'] = `Bearer ${apiKey}`;
    }
    let body: Record<string, unknown> = { prompt: '{{prompt}}' };
    const rawBody = (target.agent_body_template ?? '').trim();
    if (rawBody) {
      body = JSON.parse(substituteAgentAPIKey(rawBody, apiKey)) as Record<string, unknown>;
    }

    // U9 · Server-owned conversation. `wantsAgentConversation` is checked
    // against the *original* templates (before rendering), because that is what
    // the operator wrote; the rendered copies no longer contain the placeholder
    // once it has been rewritten.
    const conversationWanted = wantsAgentConversation({
      agent_body_template: target.agent_body_template,
      agent_headers_template: target.agent_headers_template,
    });
    if (conversationWanted) {
      if (rawBody) body = remapConversationPlaceholders(body);
      for (const name of Object.keys(headers)) {
        headers[name] = renderConversationPlaceholders(headers[name] ?? '');
      }
    }

    const responsePath = (target.agent_response_path ?? '').trim();
    return {
      id: 'http',
      config: {
        url: substituteAgentAPIKey(target.agent_endpoint.trim(), apiKey),
        method: (target.agent_method ?? 'POST').trim().toUpperCase() || 'POST',
        headers,
        body,
        transformResponse: toAgentTransformResponse(target.agent_response_path),
        // Only present when a conversation was asked for. See the function
        // header for why its absence is what preserves v1 behaviour.
        ...(conversationWanted ? { sessionParser: buildSessionParser(responsePath) } : {}),
        ...(target.timeout_seconds ? { timeout: target.timeout_seconds * 1000 } : {}),
      },
    };
  }
  return {
    id: 'http',
    config: {
      // promptfoo's http provider POSTs to config.url verbatim; the platform
      // stores an OpenAI-compatible base URL (…/v1), so normalize it to the
      // full chat/completions endpoint.
      url: toChatCompletionsURL(target.base_url),
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${target.api_key}`,
        ...(target.headers ?? {}),
      },
      body: {
        model: target.model,
        messages: [{ role: 'user', content: '{{prompt}}' }],
      },
      transformResponse: 'json.choices[0].message.content',
      ...(target.timeout_seconds ? { timeout: target.timeout_seconds * 1000 } : {}),
    },
  };
}

/**
 * Rewrite `{{conversation_id}}` throughout a parsed body template, at any depth.
 *
 * The body is a JSON tree, not a flat string, so a naive `replace` on the
 * serialized form would be fragile: it would also rewrite occurrences inside
 * values that must stay literal, and JSON-escaping differences between the
 * stored template and a re-serialization make byte-level replacement unsafe.
 * Walking the tree only touches string *values*, which is exactly the set the
 * placeholder can legitimately appear in.
 */
function remapConversationPlaceholders(node: unknown): any {
  if (typeof node === 'string') return renderConversationPlaceholders(node);
  if (Array.isArray(node)) return node.map(remapConversationPlaceholders);
  if (node && typeof node === 'object') {
    const out: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
      out[key] = remapConversationPlaceholders(value);
    }
    return out;
  }
  return node;
}

/**
 * Shared llm-rubric: PASS means the target stayed safe (refused / no harmful
 * output); FAIL means the target complied — i.e. attack success. The polarity
 * flip in reduceEvalToSafeResult relies on this.
 *
 * U8: this is no longer a single string mounted on every test. Each test now
 * carries the rubric of its own plugin family (see judgeRubric.ts), because one
 * shared "was the output harmful?" question scores most families wrongly — a
 * successful prompt injection usually produces entirely harmless output. The
 * polarity contract is unchanged and still asserted in judgeRubric.ts.
 */

export interface RunnerResult {
  result: SafeRunResult;
  rawEvalId?: string;
}

/**
 * Reduce a promptfoo Eval into the platform-safe summary. Only counts,
 * severities, and redacted short reasons are extracted. The `fail` polarity
 * of promptfoo grader results is flipped here: pf fail = vulnerability
 * confirmed = platform "attack_success".
 *
 * evaluate mode has no redteam grader severity, so a successful attack gets a
 * deterministic display severity looked up from the backend catalog (U2/T2.2).
 * An id the catalog does not know is reported as other/medium — never guessed
 * from a regex. The grader's own severity (when present) always wins over the
 * catalog value.
 */
export function reduceEvalToSafeResult(
  evalResult: EvalLike,
  catalog?: EngineCatalog,
  options?: {
    /**
     * U8 · Per-plugin judge-source corrections for paths where the catalog's
     * answer is not the truth.
     *
     * Plan A judges every plugin with the platform rubric, so the catalog
     * lookup is always right. Plan C is different: for the plugins listed in
     * `PROMPTFOO_CONFIG_UNSAFE_PLUGIN_IDS` the platform rubric cannot be
     * injected (promptfoo misreads their config), so they are graded by
     * promptfoo's own grader instead. Without this the report would show
     * `category:industry` for such a plugin — indistinguishable from a plugin
     * that really did get the family standard, which is a false claim about the
     * scoring basis.
     */
    judgeSourceOverrides?: Map<string, string>;
    /**
     * U10 · Judge source for plugins whose standard is neither the platform's
     * family rubric nor promptfoo's built-in grader.
     *
     * An expert-published package carries its **own** `grader`, and that is what
     * decided each probe (`CustomPlugin.getAssertions` builds the llm-rubric
     * assertion from it). Without this override the lookup falls through to the
     * catalog, does not find the id, and lands on `fallback:unmapped` — which
     * would claim a degradation that did not happen, while the real basis (the
     * expert's grader) went unrecorded.
     *
     * Consulted before the override map and after the per-test stamp, so a
     * Plan-A style explicit stamp still wins.
     */
    judgeSourceByPlugin?: Map<string, string>;
  },
): SafeRunResult {
  const catalogMap = buildCatalogMap(catalog);
  const rubricMap = buildJudgeRubricMap(catalog);
  const classify = (pluginId: string): CatalogClassification =>
    catalogMap.get(pluginId) ?? UNKNOWN_PLUGIN_CLASSIFICATION;

  const severityCounts = { critical: 0, high: 0, medium: 0, low: 0 };
  const pluginAgg = new Map<
    string,
    {
      label: string;
      probes: number;
      attackSuccess: number;
      maxSev: string;
      reasons: string[];
      category: string;
      categoryLabel: string;
      severity: Severity;
      judgeSource: string;
    }
  >();
  const strategyAgg = new Map<
    string,
    { label: string; probes: number; attackSuccess: number; maxSev: string }
  >();
  // U8: probes per rubric, so a run judged by several standards (or one that
  // degraded to the unmapped tier) is visible as such in the report.
  const judgeSourceCounts = new Map<string, number>();

  // U9 · Tool-call visibility for agent targets. Counts only — no tool
  // arguments, no tool names, nothing attacker-influenced (see the type doc).
  let probesWithToolCalls = 0;
  let totalToolCallMarkers = 0;

  let probes = 0;
  let attackSuccess = 0;

  const results = evalResult.results ?? [];
  for (const r of results) {
    probes++;
    const pass = r.gradingResult?.pass;
    const success = pass === false; // polarity flip: pf fail = attack success
    if (success) attackSuccess++;

    const pluginId = String((r.testCase?.metadata as Record<string, unknown> | undefined)?.pluginId ?? 'unknown');
    const pluginLabel = pluginId;
    const strategyId = String(
      ((r.testCase?.metadata as Record<string, unknown> | undefined)?.strategyId as string | undefined) ?? 'none',
    );
    const classification = classify(pluginId);

    // U8: which rubric judged this probe. Plan A stamps it into test metadata;
    // for any result that lacks the stamp (Plan C, or a pre-U8 payload) fall
    // back to resolving it from the catalog, and to the unmapped tier when the
    // plugin is unknown. Never guess from the id.
    const metadata = r.testCase?.metadata as Record<string, unknown> | undefined;
    const stamped = metadata?.[PLUGIN_JUDGE_SOURCE_METADATA_KEY];
    const judgeSource =
      typeof stamped === 'string' && stamped.trim()
        ? stamped.trim()
        : (options?.judgeSourceByPlugin?.get(pluginId) ??
          options?.judgeSourceOverrides?.get(pluginId) ??
          resolveJudgeRubric(pluginId, rubricMap).source);
    judgeSourceCounts.set(judgeSource, (judgeSourceCounts.get(judgeSource) ?? 0) + 1);

    // U9: look for tool-call markers in the target's own response. Read-only
    // inspection of data promptfoo already holds — the engine never re-requests
    // the target, which would double this run's side effects.
    const toolCalls = countToolCallSignals(r.response?.raw);
    if (toolCalls.count > 0) {
      probesWithToolCalls++;
      totalToolCallMarkers += toolCalls.count;
    }

    // evaluate mode: use the catalog severity for display when the grader did
    // not provide one (llm-rubric results never do).
    let sev = sanitizeSeverity(r.gradingResult?.severity ?? undefined);
    if (success && sev === 'none') {
      sev = classification.severity;
    }
    if (success && sev !== 'none') severityCounts[sev]++;

    const plugin = pluginAgg.get(pluginId) ?? {
      label: pluginLabel,
      probes: 0,
      attackSuccess: 0,
      maxSev: 'none',
      reasons: [],
      category: classification.category,
      categoryLabel: classification.categoryLabel,
      severity: classification.severity,
      judgeSource,
    };
    plugin.probes++;
    if (success) {
      plugin.attackSuccess++;
      if ((SEV_ORDER[sev] ?? 0) > (SEV_ORDER[plugin.maxSev] ?? 0)) plugin.maxSev = sev;
      const reason = redactReason(r.gradingResult?.reason ?? '');
      if (reason && plugin.reasons.length < 3) plugin.reasons.push(reason);
    }
    pluginAgg.set(pluginId, plugin);

    const strategy = strategyAgg.get(strategyId) ?? { label: strategyId, probes: 0, attackSuccess: 0, maxSev: 'none' };
    strategy.probes++;
    if (success) {
      strategy.attackSuccess++;
      if ((SEV_ORDER[sev] ?? 0) > (SEV_ORDER[strategy.maxSev] ?? 0)) strategy.maxSev = sev;
    }
    strategyAgg.set(strategyId, strategy);
  }

  // Group plugins into risk categories (grouping/labels come from the catalog).
  const categoryMap = new Map<string, { key: string; label: string; count: number; severityCounts: typeof severityCounts; plugins: SafeRunResult['risk_categories'][number]['plugins'] }>();
  for (const [pluginId, agg] of pluginAgg) {
    const cat = categoryMap.get(agg.category) ?? {
      key: agg.category,
      label: agg.categoryLabel,
      count: 0,
      severityCounts: { critical: 0, high: 0, medium: 0, low: 0 },
      plugins: [],
    };
    cat.count += agg.attackSuccess;
    cat.plugins.push({
      plugin_id: pluginId,
      probes: agg.probes,
      attack_success: agg.attackSuccess,
      top_reasons: redactTopReasons(agg.reasons),
    });
    categoryMap.set(agg.category, cat);
  }

  // Fill per-category severity counts from raw results (recount).
  const catSeverity = new Map<string, typeof severityCounts>();
  for (const r of results) {
    const pass = r.gradingResult?.pass;
    if (pass !== false) continue;
    const pluginId = String((r.testCase?.metadata as Record<string, unknown> | undefined)?.pluginId ?? 'unknown');
    const agg = pluginAgg.get(pluginId);
    if (!agg) continue;
    const cat = catSeverity.get(agg.category) ?? { critical: 0, high: 0, medium: 0, low: 0 };
    const sev = sanitizeSeverity(r.gradingResult?.severity ?? undefined);
    cat[sev === 'none' ? agg.severity : sev]++;
    catSeverity.set(agg.category, cat);
  }
  for (const [key, cat] of categoryMap) {
    const sev = catSeverity.get(key);
    if (sev) cat.severityCounts = sev;
  }

  const pluginStats: SafeRunResult['plugin_stats'] = [...pluginAgg.entries()].map(([pluginId, agg]) => ({
    plugin_id: pluginId,
    label: agg.label,
    probes: agg.probes,
    attack_success: agg.attackSuccess,
    success_rate: agg.probes > 0 ? round2(agg.attackSuccess / agg.probes) : 0,
    max_severity: sanitizeSeverity(agg.maxSev),
    // U8: per-plugin standard, so a reader can see two plugins in one run were
    // judged by different rubrics.
    judge_source: agg.judgeSource,
  }));

  const strategyStats: SafeRunResult['strategy_stats'] = [...strategyAgg.entries()].map(([id, agg]) => ({
    id,
    label: agg.label,
    probes: agg.probes,
    attack_success: agg.attackSuccess,
    success_rate: agg.probes > 0 ? round2(agg.attackSuccess / agg.probes) : 0,
    max_severity: sanitizeSeverity(agg.maxSev),
  }));

  return {
    totals: {
      probes,
      attack_success: attackSuccess,
      pass_rate: probes > 0 ? round2((probes - attackSuccess) / probes) : 1,
    },
    severity_counts: severityCounts,
    risk_categories: [...categoryMap.entries()].map(([key, cat]) => ({
      key,
      label: cat.label,
      count: cat.count,
      severity_counts: cat.severityCounts,
      plugins: cat.plugins,
    })),
    plugin_stats: pluginStats,
    strategy_stats: strategyStats,
    // U8: emitted whenever there is at least one probe, so "this run was judged
    // by N different standards — and these M probes had none" is answerable from
    // the run record alone.
    judge_sources: [...judgeSourceCounts.entries()]
      .map(([source, count]) => ({ source, probes: count, is_fallback: source === UNMAPPED_JUDGE_SOURCE }))
      .sort((a, b) => (a.source < b.source ? -1 : a.source > b.source ? 1 : 0)),
    // U9: always present once there is at least one probe, including at zero.
    // An absent field would be read as "not measured"; a present 0 is the
    // honest "no marker was recognised in any response" (and the type doc is
    // explicit that this is not a guarantee no tool was used).
    ...(probes > 0
      ? { tool_call_signals: { probes_with_tool_calls: probesWithToolCalls, total_tool_call_markers: totalToolCallMarkers } }
      : {}),
    token_usage: {
      generation: 0,
      judging: 0,
      target: 0,
    },
  };
}

/**
 * Deterministic display-level severity for evaluate-mode results now comes from
 * the backend catalog (U2/T2.2) — see buildCatalogMap / UNKNOWN_PLUGIN_CLASSIFICATION.
 * The former inferSeverityFromPlugin / CATEGORY_PREFIXES / CATEGORY_LABELS
 * tables were removed so classification has a single source of truth.
 */
function round2(n: number): number {
  return Math.round(n * 100) / 100;
}
