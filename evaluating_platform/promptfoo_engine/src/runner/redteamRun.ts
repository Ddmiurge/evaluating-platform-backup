/**
 * Runner: red-team runs executed through promptfoo.
 *
 * Two generation paths coexist behind one switch (U7):
 *
 *   - **Plan A** (default) — `generateTests.ts` synthesizes attack prompts with
 *     the platform's generation LLM, and promptfoo only consumes them in
 *     evaluate mode. promptfoo's own attack machinery is never involved.
 *   - **Plan C** — `nativeRedteam.ts` calls `promptfoo.redteam.run()` and fills
 *     the `provider` slot with the platform generation LLM, so promptfoo owns
 *     the generation paradigm (plugin templates, entity extraction, strategy
 *     transforms, graders).
 *
 * Selected by `PROMPTFOO_ENGINE_NATIVE_GENERATION`, which is off by default and
 * independent of the four fail-closed switches. Both paths reduce through the
 * same `reduceEvalToSafeResult`, so reporting and redaction are identical.
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
import { EngineRunError, generateAttackTests } from './generateTests.js';
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
 * Execute one engine run, choosing the generation path.
 *
 * `workdir` is the isolated per-run directory (created and wiped by
 * WorkdirManager). Only Plan C needs it — it holds the promptfoo config
 * document, which embeds the materialized generation credential. Plan A ignores
 * it, which is exactly what makes "turn the switch off" a complete rollback.
 */
export async function executeRedteamRun(
  record: RunRecord,
  emit: Emit,
  options: { nativeGeneration: boolean; workdir: string },
): Promise<RunnerResult> {
  if (options.nativeGeneration) {
    const { executeNativeRedteamRun } = await import('./nativeRedteam.js');
    return executeNativeRedteamRun(record, emit, options.workdir);
  }
  return executePlanARedteamRun(record, emit);
}

/**
 * U10 · Error code for a published package requested on the Plan A path.
 *
 * Mirrored by the BFF's `engine_runner_error_code`; part of the API contract so
 * the UI can say "需要开启原生生成" instead of "运行失败".
 */
export const PLAN_A_ERR_CUSTOM_PLUGIN = 'custom_plugin_requires_native_generation';

/**
 * Refuse a published package on the Plan A path.
 *
 * The check is on `custom_plugins` (the resolved payloads) rather than on the
 * plugin ids, because ids alone cannot distinguish a package from a built-in:
 * the BFF rewrites package refs to `promptfoo-custom:` markers, and Plan A would
 * pass those markers to the generation LLM as if they were plugin families —
 * producing prompts attributed to a plugin that does not exist.
 */
export function assertNoCustomPluginsOnPlanA(customPlugins: Array<{ handle: string }> | undefined): void {
  if (!customPlugins || customPlugins.length === 0) return;
  const handles = customPlugins.map((p) => p.handle).filter(Boolean);
  throw new EngineRunError(
    PLAN_A_ERR_CUSTOM_PLUGIN,
    `自定义插件（${handles.join(', ')}）只能由 promptfoo 原生生成执行，当前平台生成路径不会调用插件引擎，` +
      '照常运行会得到一份绿色报告，但专家插件的 generator 从未被执行（选中能力、零覆盖且无法察觉）。' +
      '请开启 PROMPTFOO_ENGINE_NATIVE_GENERATION，或在本次评测中不使用自定义插件。',
  );
}

/**
 * Plan A: generate attack tests with the platform generation LLM, run them
 * against the target via promptfoo evaluate, and judge each response with
 * llm-rubric. Emits safe progress events through `emit`. Throws on engine
 * failure or abort (taskManager maps to failed/canceled).
 */
export async function executePlanARedteamRun(record: RunRecord, emit: Emit): Promise<RunnerResult> {
  const { request } = record;
  const { credentials } = request;

  // U10: Plan A cannot execute a published package. It asks the platform LLM
  // for attack prompts and hands plugin ids to it only as strings inside a
  // prompt we wrote — promptfoo's plugin machinery is never involved, so a
  // package's `generator` template would simply be ignored. Running anyway
  // would produce a green report in which the expert's capability was never
  // exercised: the same "selected a capability, got zero coverage, no way to
  // tell" shape U6 exists to eliminate, reached by a different route.
  //
  // Refusing is the honest response. Turning on
  // PROMPTFOO_ENGINE_NATIVE_GENERATION makes the same request work, and the
  // message says so.
  assertNoCustomPluginsOnPlanA(request.custom_plugins);

  if (!credentials.generation) {
    // Plan A requires a generation LLM; the BFF fail-fasts on this too, so
    // reaching here means the request bypassed validation.
    throw new EngineRunError('generation_unavailable', 'generation LLM credentials missing');
  }

  const targetProvider = buildTargetProvider(credentials.target);

  // Grading provider for llm-rubric. Must be a loadable provider-options
  // object (id + config); apiBaseUrl includes the /v1 suffix.
  const gradingProvider = {
    id: `openai:chat:${credentials.generation.model}`,
    config: {
      apiBaseUrl: credentials.generation.base_url,
      apiKey: credentials.generation.api_key,
      temperature: 0,
      // Reasoning models can burn the openai provider's default 1024-token
      // budget on reasoning and return empty content; give the rubric verdict
      // room to land. passthrough fields land in the request body, disabling
      // reasoning mode on providers that support it.
      max_tokens: 4096,
      passthrough: {
        thinking: { type: 'disabled' },
        reasoning_effort: 'none',
      },
    },
  };

  emit({ run_id: request.run_id, phase: 'generating_tests', status_text: '生成攻击用例' });

  const generated = await generateAttackTests({
    purpose: request.purpose,
    numTests: Math.min(Math.max(request.num_tests, 1), 50),
    plugins: request.plugins,
    strategies: request.strategies,
    generation: credentials.generation,
    signal: record.controller.signal,
  });

  // U8: every generated case is judged by ITS OWN plugin's rubric rather than
  // one shared string. The rubric id rides along in the test metadata so the
  // reduction can report which standard produced which verdict — with a flat
  // test array there is otherwise no way to recover that.
  const rubricMap = buildJudgeRubricMap(request.catalog);
  const tests = generated.map((t) => {
    const rubric = resolveJudgeRubric(t.plugin_id, rubricMap);
    return {
      vars: { prompt: t.prompt },
      assert: [{ type: 'llm-rubric' as const, value: rubric.criteria }],
      metadata: {
        pluginId: t.plugin_id,
        strategyId: t.strategy_id ?? 'none',
        [PLUGIN_JUDGE_SOURCE_METADATA_KEY]: rubric.source,
      },
    };
  });

  const testSuite = {
    description: `platform-run-${request.run_id}`,
    prompts: ['{{prompt}}'],
    providers: [targetProvider],
    tests,
    defaultTest: {
      options: {
        provider: gradingProvider,
      },
    },
    metadata: {
      platform_user_id: request.platform_user_id,
    },
    // Deliberately NO writeLatestResults and NO redteam key: evaluate mode
    // stays off promptfoo's SQLite and its remote generation/grading paths.
  };

  emit({
    run_id: request.run_id,
    phase: 'executing_probes',
    status_text: '执行探针',
    planned_count: tests.length,
    executed_count: 0,
  });

  const evalResult = (await promptfoo.evaluate(testSuite, {
    cache: false,
    maxConcurrency: Math.min(Math.max(credentials.target.concurrency ?? 5, 1), 50),
    abortSignal: record.controller.signal,
    progressCallback: (completed: number, total: number) => {
      emit({
        run_id: request.run_id,
        phase: 'executing_probes',
        status_text: '执行探针',
        planned_count: total,
        executed_count: completed,
      });
    },
  } as Parameters<typeof promptfoo.evaluate>[1])) as unknown as EvalLike & { id?: string };

  if (!evalResult) {
    throw new EngineRunError('engine_error', 'empty evaluation result');
  }

  emit({ run_id: request.run_id, phase: 'engine_judging', status_text: '引擎判定' });

  const result = reduceEvalToSafeResult(evalResult, request.catalog);

  emit({ run_id: request.run_id, phase: 'compiling_report', status_text: '生成报告' });

  return { result, rawEvalId: evalResult.id };
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
