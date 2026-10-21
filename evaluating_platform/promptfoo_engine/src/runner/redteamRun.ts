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
 * The Eval results are reduced into a SafeRunResult. Raw prompts and responses
 * leave this module **only** through `SafeRunResult.case_results`, and only for
 * a run that reached a terminal state — see that field's doc for why the old
 * "never leaves this module" promise was narrowed rather than kept. They still
 * never reach a log line, an `error_brief`, or a progress event.
 */
import promptfoo from 'promptfoo';
/**
 * Minimal structural type of the promptfoo Eval object we consume. evaluate()
 * returns the Eval instance itself; in library mode (writeLatestResults off)
 * results are in-memory EvalResult objects on the `results` array.
 *
 * Exported because Plan C's `loadEvalResults` has to name it: that function
 * takes an `Eval` whose `results` may be empty and rehydrates it (U8).
 *
 * # Why this type grew (2026-10-16)
 *
 * It was deliberately narrow: it declared `response.output` but never read it,
 * and had no `prompt` or `vars` at all, so the reducer structurally *could not*
 * carry per-probe text. Every field added below is one the report needs, and
 * each is read from the in-memory result — the only moment the text exists,
 * since the terminal cleanup deletes promptfoo's database and workdir.
 *
 * promptfoo's own report reads the same paths; where it has a fallback chain,
 * the chain is mirrored here rather than simplified, so the same probe shows the
 * same prompt in both reports.
 */
export interface EvalLike {
  id?: string;
  results?: Array<{
    gradingResult?: {
      pass?: boolean;
      severity?: unknown;
      reason?: unknown;
      score?: unknown;
    };
    testCase?: {
      metadata?: Record<string, unknown>;
      /** Injection variables. promptfoo's report prefers these over `prompt.raw`. */
      vars?: Record<string, unknown>;
    };
    /** Result-level vars. Which of the two promptfoo populates depends on the path. */
    vars?: Record<string, unknown>;
    /**
     * The call error, when the target call did not complete. promptfoo sets it
     * for a provider that threw — which, since the target provider validates the
     * status, is what a non-2xx target response produces. `error` present means
     * there is no answer to grade, which is a different claim from "the answer
     * was empty"; see `caseCallError`.
     */
    error?: unknown;
    /** The prompt document; `raw` is the rendered text. */
    prompt?: { raw?: unknown };
    /**
     * Result-level metadata. Carries `pluginId`/`strategyId` (promptfoo's report
     * reads these here rather than on the test case) and the multi-turn
     * transcripts (`redteamHistory` / `redteamTreeHistory`).
     */
    metadata?: Record<string, unknown>;
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
import type {
  EngineCatalog,
  OneShotCredentials,
  RunProgressEvent,
  SafeCaseResult,
  SafeRunResult,
} from '../types.js';
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
 *
 * # A path that matches nothing THROWS (2026-10-21)
 *
 * It used to be a bare `json.<path>`, which yields `undefined` when the body
 * has no such field — and `undefined` is not a distinguishable outcome.
 * promptfoo wraps it as `{ output: undefined }`, the report serialized that as
 * 目标回答 `{}`, and promptfoo's prompt-extraction rubric then PASSED it ("the
 * system prompt was not divulged"). So a target answering with an unexpected
 * body produced a silent non-measurement that read as a safe result.
 *
 * Throwing turns it into the same thing a non-2xx now produces: a case error
 * that reaches the report as 「未取得回答（…）」. The message names the path
 * because the usual cause is a misconfigured `agent_response_path` — and the
 * run is where the operator can still see it.
 */
export function toAgentTransformResponse(raw?: string): string {
  const path = (raw ?? '')
    .trim()
    .replace(/^json\./i, '')
    .replace(/^\.+/, '')
    .replace(/\.+$/, '');
  if (!path) return 'json';
  return `json.${path} ?? (() => { throw new Error('agent response path "${path}" matched nothing in the response body') })()`;
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
/**
 * Only a 2xx counts as a target response.
 *
 * A STRING, not a function: the built provider config is serialized to
 * `redteam.yaml` before the run and the YAML writer cannot represent a function
 * ("Tag not resolved for Function value" — measured). promptfoo evaluates this
 * form itself (`new Function('status', 'return <expr>')`), so the document stays
 * plain data.
 *
 * # Why this is set explicitly (measured 2026-10-21)
 *
 * promptfoo's default accepts EVERY status:
 *
 *   async function createValidateStatus(validator) {
 *     if (!validator) return (_status) => true;
 *
 * So a target answering 404/500 was treated as an ordinary response:
 * `transformResponse` (`json.answer`) found nothing, the case carried
 * `output: undefined, error: null`, and the report rendered 目标回答 as `{}`.
 * promptfoo's prompt-extraction rubric then PASSES an empty output ("the system
 * prompt was not divulged"), so a target that never answered produced 0/6 attack
 * successes and a 「最高安全 100/100」 report — a conclusion drawn from no
 * measurements at all.
 *
 * Measured locally with this provider config: a 404 with an error body yields
 * `output: {}` and `error: null` before the fix, and throws
 * `HTTP call failed with status 404 …` after it. That message reaches
 * `case_results[].error` and the report renders 「未取得回答（调用失败）」.
 *
 * The platform's own Go call path already treats non-2xx as a failed call
 * (`redteam_bridge.go`: `status = "failed"`); this is the engine agreeing with
 * it. A target that is up but slow still gets its own classification — this only
 * rejects what the HTTP layer already says went wrong.
 */
const VALIDATE_TARGET_STATUS = 'status >= 200 && status < 300';

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
        validateStatus: VALIDATE_TARGET_STATUS,
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
      // Same reason as the agent branch: without this a 401/404/500 from the
      // target reads as an empty answer and is graded as a pass.
      validateStatus: VALIDATE_TARGET_STATUS,
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
    /**
     * The variable promptfoo injected the attack prompt into
     * (`config.redteam.injectVar`, default `prompt`).
     *
     * Needed because the attack text is not one field: promptfoo's own report
     * looks for `vars[injectVar]`, then `vars.query`, then `prompt.raw`, and
     * which one holds the payload depends on that setting. Guessing `prompt`
     * would silently show the wrapper instead of the payload on any run with a
     * custom inject var — a wrong answer that still looks like an answer.
     */
    injectVar?: string;
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

  // Per-probe text for the report. Collected in the same pass as the aggregates
  // so there is exactly one place that decides what a probe's prompt/response
  // are — two passes would eventually disagree.
  const injectVar = (options?.injectVar ?? '').trim() || 'prompt';
  const caseResults: SafeCaseResult[] = [];

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

    // Per-probe detail for the report. Deliberately NOT passed through
    // `redactReason`: that function exists to bound a *summary* line (200 chars,
    // pattern-scrubbed), and applying it here would truncate exactly the text
    // the report was changed to carry. Credentials and paths are still stripped
    // — by the report layer, which knows what a report field is.
    caseResults.push({
      index: probes,
      plugin_id: pluginId,
      strategy_id: strategyId,
      severity: sev,
      attack_success: success,
      prompt: casePromptText(r, injectVar),
      response: caseResponseText(r),
      ...(caseCallError(r) ? { error: caseCallError(r) } : {}),
      judge_reason: typeof r.gradingResult?.reason === 'string' ? r.gradingResult.reason : '',
      ...(originalPromptOf(metadata) ? { original_prompt: originalPromptOf(metadata) } : {}),
      ...(typeof r.gradingResult?.score === 'number' ? { score: r.gradingResult.score } : {}),
      ...caseTurns(r),
    });

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
    // Emitted whenever there is at least one probe, same rule as
    // `tool_call_signals`: absent would be read as "this engine cannot report
    // per-probe detail" (a pre-2026-10 engine), which is a different statement
    // from "this run produced none".
    ...(caseResults.length > 0 ? { case_results: caseResults } : {}),
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

/** One row of `EvalLike['results']`. */
type EvalResultLike = NonNullable<EvalLike['results']>[number];

/**
 * The attack prompt as the target received it.
 *
 * Mirrors promptfoo's own fallback chain instead of picking one field. Its
 * report walks `vars[injectVar]` → `vars.query` → `prompt.raw`; collapsing that
 * to a single lookup would make the same probe show a different prompt here than
 * in promptfoo's report, and the difference would be invisible — both candidates
 * look like plausible payloads.
 *
 * `vars.prompt` sits between `vars.query` and `prompt.raw` because promptfoo's
 * *pass* branch reads it there. Including it can only substitute a more specific
 * value for `prompt.raw`, never the reverse — `vars[injectVar]`, the field that
 * actually holds the payload for the configured inject var, still wins.
 */
function casePromptText(result: EvalResultLike, injectVar: string): string {
  const vars = (result.vars ?? result.testCase?.vars) as Record<string, unknown> | undefined;
  return firstText(vars?.[injectVar], vars?.query, vars?.prompt, result.prompt?.raw);
}

/**
 * The target's answer, verbatim.
 *
 * A string in every provider shape this engine configures — the
 * OpenAI-compatible chat target and the custom-HTTP agent endpoint both extract
 * text before it gets here. Anything else is serialized rather than dropped: an
 * unexpected shape is worth seeing, and silently emptying the field would read
 * as "the target said nothing", which is a claim about the target we would have
 * no basis for.
 */
/**
 * The call error promptfoo recorded for this probe, or '' when the call landed.
 *
 * # Why the reducer has to read this (2026-10-21)
 *
 * Until the target provider set `validateStatus`, promptfoo's http provider
 * accepted every status, so a failed call arrived as `output: undefined` with
 * `error: null` and the two were indistinguishable in the report — every one of
 * them rendered as 目标回答 `{}`. With the validator in place a non-2xx throws,
 * and promptfoo stores the message here. Dropping it at reduction time would
 * waste that: the report could say 「未取得回答」 but not why.
 *
 * Bounded at 500 runes because promptfoo appends the response body to the
 * message (`… : <data>`), and a target returning a large error page must not
 * blow up the engine's result document. Credentials and paths are stripped
 * later, by the report layer, which knows what a report field is.
 */
function caseCallError(result: EvalResultLike): string {
  const raw = typeof result.error === 'string' ? result.error.trim() : '';
  if (!raw) return '';
  return raw.length > 500 ? `${raw.slice(0, 500)}…` : raw;
}

function caseResponseText(result: EvalResultLike): string {
  const output = result.response?.output;
  if (typeof output === 'string') return output;
  if (output === undefined || output === null) return '';
  try {
    return JSON.stringify(output);
  } catch {
    return '';
  }
}

/**
 * The multi-turn transcript, when the strategy held a conversation.
 *
 * Same source as promptfoo's (`metadata.redteamHistory`, falling back to
 * `redteamTreeHistory` for the tree-search family), and the same shape: each
 * history entry contributes a user turn and an assistant turn.
 *
 * Omitted for a single exchange, because then it would only repeat `prompt` and
 * `response` — the field exists to show what the flat pair cannot, which is the
 * earlier turns that set the attack up.
 */
function caseTurns(result: EvalResultLike): { turns?: SafeCaseResult['turns'] } {
  const history = (result.metadata as Record<string, unknown> | undefined)?.redteamHistory ??
    (result.metadata as Record<string, unknown> | undefined)?.redteamTreeHistory;
  if (!Array.isArray(history) || history.length < 2) return {};
  const turns: NonNullable<SafeCaseResult['turns']> = [];
  for (const entry of history) {
    if (!entry || typeof entry !== 'object') continue;
    const rec = entry as Record<string, unknown>;
    if (typeof rec.prompt === 'string' && rec.prompt !== '') turns.push({ role: 'user', content: rec.prompt });
    if (typeof rec.output === 'string' && rec.output !== '') turns.push({ role: 'assistant', content: rec.output });
  }
  return turns.length > 0 ? { turns } : {};
}

/** First argument that is a non-empty string (after trimming). */
function firstText(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim() !== '') return value;
  }
  return '';
}

/**
 * The pre-strategy payload promptfoo recorded, when there is one to record.
 *
 * `metadata.originalText` is stamped by the strategy actions (`addBase64Encoding`
 * and friends) before the transform. It is returned only when it genuinely says
 * something more than the prompt does — a same-or-empty value would add a field
 * to every probe and make the report's fallback path look exercised when it is
 * not.
 */
function originalPromptOf(metadata: Record<string, unknown> | undefined): string {
  if (!metadata) return '';
  const value = metadata.originalText;
  return typeof value === 'string' ? value.trim() : '';
}
