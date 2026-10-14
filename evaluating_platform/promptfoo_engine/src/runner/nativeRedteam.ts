/**
 * nativeRedteam.ts — U7 · Plan C: promptfoo generates the attack cases.
 *
 * # What changes relative to Plan A
 *
 * Plan A (`generateTests.ts`, 189 lines) asks the platform's generation LLM for a
 * JSON array of attack prompts, then feeds them to `promptfoo.evaluate()`. The
 * plugin ids and strategy ids only ever appear as *strings inside a prompt we
 * wrote* — promptfoo's own attack machinery is never involved.
 *
 * Plan C calls `promptfoo.redteam.run()` instead and fills two independent slots:
 *
 *   - `targets[0]` — the system under test (reuses `buildTargetProvider`)
 *   - `redteam.provider` — the *generation* model, i.e. the MaClaw LLM
 *
 * promptfoo then owns the generation paradigm: its per-plugin templates
 * (ContractPlugin, HallucinationPlugin, …), its entity extraction, its
 * strategy transforms, and its graders.
 *
 * # How the config actually reaches promptfoo (verified, not assumed)
 *
 * `runRedteam(options)` takes `options.config` — a *path to a YAML file* — or
 * `options.liveRedteamConfig`, which it dumps to `os.tmpdir()`. It has no
 * `plugins` / `provider` / `purpose` parameters of its own; `RedteamRunOptions`
 * only carries `target?: string`, which is a *label* used for filtering, not a
 * provider. The two slots people describe as "runRedteam's target and provider"
 * are in fact two keys of the **redteam config document**: `targets[]` at the
 * top level and `provider` under `redteam:`.
 *
 * We write that document ourselves into the per-run workdir rather than using
 * `liveRedteamConfig`, for one concrete reason: `liveRedteamConfig` is
 * `yaml.dump`-ed into a *shared* temp directory, and our document contains the
 * materialized generation API key in plaintext. The workdir is created per run
 * by WorkdirManager and `rm -rf`'d on the terminal path, so the credential dies
 * with the run. Nothing else about the two paths differs.
 *
 * # Two silent-failure shapes this module exists to close
 *
 * 1. **No `provider` ⇒ promptfoo silently picks OpenAI.**
 *    `redteamProviderManager.getProviderSelection()` returns `{source:'default'}`
 *    when no provider is configured, and `loadRedteamProvider()` then builds
 *    `new OpenAiChatCompletionProvider('gpt-5.5-2026-04-23')` — an
 *    api.openai.com client. With no key it degrades to a *failed run that still
 *    reports success*: measured, promptfoo logged
 *    `Using default redteam provider: gpt-5.5-2026-04-23`, then
 *    `No test cases generated. Skipping scan.` and **returned undefined without
 *    throwing**. That is exactly the "green run, zero coverage" shape U6 exists
 *    to eliminate, reappearing one layer up. `assertNativeSlots()` refuses the
 *    run before that can happen, and `runNativeRedteam` treats the undefined
 *    return as a hard error too.
 *
 * 2. **A plugin generating 0 tests ⇒ green run.** `handleFailedPlugins()` only
 *    logs a warning unless `strict` is set. We always set `strict: true`, which
 *    turns it into a thrown `PartialGenerationError`.
 *
 * # ✅ SQLite 落盘裁定（2026-10-10，方案 A）
 *
 * Plan A is explicit that it keeps everything off promptfoo's database ("NO
 * writeLatestResults and NO redteam config"). Plan C cannot honour that:
 * `doRedteamRun()` calls `doEval({... cache: true, write: true})` with both
 * values **hardcoded** — there is no option to turn it off. So raw prompts and
 * target responses are persisted to `promptfoo.db`.
 *
 * **裁定：方案 A —— 容器内接受 + run 后显式清理。** 落地为
 * `promptfooStateCleanup.ts` + `TaskManager` 的 `onTerminal` 钩子，覆盖
 * 成功 / 失败 / 中止三条路径，清理失败会把 run 降级为 `failed`
 * （`terminal_cleanup_failed`）而不是静默留残留。
 *
 * **为什么不把落盘限定在 per-run 目录**（A-1 的实测结论）：promptfoo 的落盘
 * 位置由两处 module-level 状态决定，改不动 ——
 *  1. `getConfigDirectoryPath()` 返回 `environmentConfigDirectoryPath || ~/.promptfoo`，
 *     而 `environmentConfigDirectoryPath` 是**模块级 `let`**，在 import 时从
 *     `PROMPTFOO_CONFIG_DIR` 取一次；
 *  2. `getDb()` **记忆化连接**（`if (dbInstance) return dbInstance;`），首次调用
 *     即把文件固定到进程终结。
 * 且 `TaskManager` 在单进程内并发多个 worker，运行期改 `process.env` 会让
 * run A 的结果落进 run B 的目录。要安全就得串行化所有 run —— 那是比本清理
 * 大得多的行为变更。**故明确不改 `PROMPTFOO_CONFIG_DIR`。**
 * （这两个事实不是读代码得出的，是本批端到端验证时**自己踩到**的：ESM 会把
 * import 提升到赋值之前，导致 `PROMPTFOO_CONFIG_DIR` 在 promptfoo 捕获之后才
 * 被设置，于是产物落到了真实的 `~/.promptfoo`。）
 *
 * **安全契约边界澄清**：`AGENTS.md:23` 禁止的是「回浏览器」与「进报告/证据」，
 * **未禁止引擎侧本地文件**；本裁定补齐该边界 —— 引擎侧可写本地文件，但必须在
 * run 终态显式清理。
 */
import fs from 'node:fs/promises';
import path from 'node:path';
import promptfoo from 'promptfoo';
import { stringify } from 'yaml';
import type { ApplicationDefinition, EngineCatalog, EngineCustomPlugin, OneShotCredentials, RunProgressEvent } from '../types.js';
import type { RunRecord } from '../manager/taskManager.js';
import { EngineRunError } from './generateTests.js';
import { buildTargetProvider, reduceEvalToSafeResult, type EvalLike, type RunnerResult } from './redteamRun.js';
import { assertNativeStrategyMapIsCurrent, nativeStrategyMapping } from './nativeStrategyMap.js';
import {
  applyStatefulToMultiTurn,
  assertAgentConversationIsExecutable,
  assertMultiTurnStrategyIdsAreCurrent,
  wantsAgentConversation,
} from './agentSession.js';
import { buildJudgeRubricMap, resolveJudgeRubric, type JudgeRubric } from './judgeRubric.js';
import {
  applyCustomPluginRefs,
  materializeCustomPlugins,
  reportPluginID,
  type MaterializedCustomPlugin,
} from './customPlugin.js';
import { applyCustomStrategyRefs, isPromptfooCustomStrategyId, resolveCustomStrategies, type EngineCustomStrategy } from './customStrategy.js';

type Emit = (event: RunProgressEvent) => void;

/** The prompt variable promptfoo injects generated payloads into. */
export const DEFAULT_INJECT_VAR = 'prompt';

/** Config file we hand promptfoo inside the per-run workdir. */
const CONFIG_FILE_NAME = 'redteam-config.yaml';

/** Machine-readable failure codes. Part of the engine/BFF API contract. */
export const NATIVE_ERR_MISSING_PROVIDER = 'native_generation_provider_missing';
export const NATIVE_ERR_INVALID_PROVIDER = 'native_generation_provider_invalid';
export const NATIVE_ERR_NO_TEST_CASES = 'native_generation_no_test_cases';
export const NATIVE_ERR_CONFIG = 'native_generation_config_invalid';
/**
 * A plugin id the platform catalogue does not offer. Distinct from
 * NATIVE_ERR_CONFIG because it is a verdict about the REQUEST, not about the
 * document we built: the caller can fix it by choosing a different plugin,
 * whereas a config error means the platform produced something promptfoo
 * cannot read.
 */
export const NATIVE_ERR_PLUGIN_ID_INVALID = 'native_generation_plugin_id_invalid';

/** Hostnames promptfoo's provider layer would treat as "not our endpoint". */
const FORBIDDEN_PROVIDER_HOSTS = [
  'api.openai.com',
  'api.promptfoo.app',
  'api.anthropic.com',
  'generativelanguage.googleapis.com',
  'api.cohere.ai',
  'api.mistral.ai',
];

/**
 * Validate that both slots are present and safe *before* promptfoo is called.
 *
 * The two slots are validated independently on purpose: the DoD for U7 requires
 * proving that filling `target` without `provider` fails loudly rather than
 * falling through to promptfoo's OpenAI default. `target` is checked for a
 * usable endpoint too, so a half-configured request is rejected on its own terms
 * instead of surfacing later as an opaque promptfoo error.
 */
export function assertNativeSlots(request: {
  credentials: OneShotCredentials;
  inject_var?: string;
}): void {
  const { target, generation } = request.credentials;

  // --- slot 1: target (system under test) ---
  const targetEndpoint = target?.agent_endpoint?.trim() || target?.base_url?.trim();
  if (!targetEndpoint) {
    throw new EngineRunError(NATIVE_ERR_INVALID_PROVIDER, 'native generation requires a target endpoint (target.base_url or target.agent_endpoint)');
  }
  assertSafeProviderUrl(targetEndpoint, 'target');

  // --- slot 2: provider (generation model) ---
  if (!generation) {
    throw new EngineRunError(
      NATIVE_ERR_MISSING_PROVIDER,
      'native generation requires a generation provider; promptfoo would otherwise fall back to its built-in OpenAI endpoint',
    );
  }
  const baseURL = (generation.base_url ?? '').trim();
  const model = (generation.model ?? '').trim();
  const apiKey = (generation.api_key ?? '').trim();
  const missing = [
    !baseURL ? 'base_url' : '',
    !model ? 'model' : '',
    !apiKey ? 'api_key' : '',
  ].filter(Boolean);
  if (missing.length > 0) {
    throw new EngineRunError(
      NATIVE_ERR_INVALID_PROVIDER,
      `generation provider is incomplete: missing ${missing.join(', ')}`,
    );
  }
  assertSafeProviderUrl(baseURL, 'provider');
}

/**
 * Reject a provider URL that points at a vendor promptfoo would otherwise treat
 * as its own default or as a remote channel. This is the engine-side twin of
 * the BFF's credential materialization: whatever the platform hands over, the
 * generation model must be an endpoint the platform operator chose.
 */
export function assertSafeProviderUrl(rawUrl: string, slot: 'target' | 'provider'): void {
  let url: URL;
  try {
    url = new URL(rawUrl);
  } catch {
    throw new EngineRunError(NATIVE_ERR_INVALID_PROVIDER, `${slot} endpoint is not a valid URL: ${rawUrl}`);
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new EngineRunError(NATIVE_ERR_INVALID_PROVIDER, `${slot} endpoint must be http(s), got ${url.protocol}`);
  }
  const host = url.hostname.toLowerCase();
  const forbidden = FORBIDDEN_PROVIDER_HOSTS.find((h) => host === h || host.endsWith(`.${h}`));
  if (forbidden) {
    throw new EngineRunError(
      NATIVE_ERR_INVALID_PROVIDER,
      `${slot} endpoint ${host} is a model vendor default; native generation must use a platform-operated endpoint`,
    );
  }
}

/**
 * Refuse a plugin id the platform has MEASURED promptfoo to reject.
 *
 * # What this does and does not refuse
 *
 * It refuses exactly the ids in `catalog.rejected_plugin_ids` — the ids the
 * backend determined, by evaluating promptfoo's own `RedteamConfigSchema`, that
 * the engine cannot accept (`prompt-injection`, `jailbreak`,
 * `security-exploit` today). It deliberately does NOT refuse every id absent
 * from `catalog.plugins`: the catalogue is the platform's OFFERED set, which is
 * a strict subset of what promptfoo accepts, and U8 already decided that an
 * unlisted-but-acceptable plugin still runs (judged by the named
 * `fallback:unmapped` rubric so the gap is visible). Rejecting on catalogue
 * membership would refuse valid plugins —
 * `sql-injection` / `shell-injection` are the measured examples, and the plan
 * treats them as catalogue gaps to fill, not as runs to block.
 *
 * # Why the verdict is consumed rather than re-derived here
 *
 * promptfoo's acceptance is only knowable from its zod schema, which lives in a
 * content-hashed chunk behind a minified export. Resolving that at engine boot
 * would add a production dependency on promptfoo internals that breaks on any
 * upgrade and bricks the container when it does. The backend already evaluates
 * that schema offline and ships the result, CI-pinned — the same
 * generated-table discipline U6 uses for remote dependencies. The engine
 * consuming that table is the same authority, sampled once and pinned.
 *
 * # Why it is still the engine's job
 *
 * The BFF rejects rejected ids at the request boundary (P2-2 L2) and the
 * catalogue no longer offers them (L1). The chat confirm fast path submits
 * engine requests directly, and a stale queued request can predate both — so
 * the last line is worth having. Without it promptfoo fails the WHOLE run
 * mid-flight, after a run record exists, with an exception the user never sees.
 *
 * `file://` refs are exempt: they name a package `materializeCustomPlugins`
 * wrote into this run's workdir, and their legality is decided by loading the
 * package, not by any id-level table.
 *
 * Counter-proof: send `prompt-injection` with a catalogue that carries the
 * rejected list → this throws `native_generation_plugin_id_invalid`; remove the
 * call site in `buildNativeRedteamConfig` → the request reaches promptfoo and
 * fails there with no code the platform can explain.
 */
export function assertPluginIdsAreAccepted(
  plugins: Array<{ id: string }>,
  catalog: EngineCatalog | undefined,
): void {
  const rejected = catalog?.rejected_plugin_ids ?? {};
  const rejectedIds = new Set(Object.keys(rejected).map((id) => id.trim()).filter(Boolean));
  if (rejectedIds.size === 0) return;

  const offending = new Set<string>();
  for (const plugin of plugins) {
    const id = (plugin?.id ?? '').trim();
    if (!id || id.toLowerCase().startsWith('file://')) continue;
    if (rejectedIds.has(id)) offending.add(id);
  }
  if (offending.size === 0) return;

  const details = [...offending]
    .sort()
    .map((id) => {
      const reason = (rejected[id] ?? '').trim();
      return reason ? `${id}（${reason}）` : id;
    })
    .join('；');
  throw new EngineRunError(
    NATIVE_ERR_PLUGIN_ID_INVALID,
    `本轮请求包含 promptfoo 不接受的插件 id：${details}。` +
      '这类 id 会让整次评测失败而不是降级，请改选目录中可用的插件。',
  );
}

/**
 * Map the request's strategy ids onto promptfoo's vocabulary.
 *
 * Platform-invented dimensions (`direct`, `role-play`, …) are deliberately left
 * out rather than forwarded: promptfoo throws on ids it does not know, and
 * dropping them silently would understate what was actually exercised. They stay
 * on the Plan A path. Every id we *do* forward is validated against promptfoo's
 * own strategy list first.
 */
export function resolveNativeStrategies(
  strategies: Array<{ id: string }>,
): { native: Array<{ id: string }>; planAOnly: string[] } {
  const available = new Set(promptfoo.redteam.Strategies.map((s) => s.id));
  const seen = new Set<string>();
  const native: Array<{ id: string }> = [];
  const planAOnly: string[] = [];

  for (const ref of strategies ?? []) {
    const id = (ref?.id ?? '').trim();
    if (!id) continue;
    // Phase 3 · 任务 2: `custom:<variant>` ids are minted per published package
    // and so cannot be enumerated in nativeStrategyMap.ts. They are promptfoo's
    // own custom-strategy hook — `isCustomStrategy()` accepts exactly this
    // shape — and they are forwarded unchanged.
    //
    // They are deliberately NOT taken on faith: `available` holds promptfoo's
    // executable ids, and `custom` is one of them, so an id is forwarded only if
    // its `custom` base is a strategy this promptfoo build actually exposes. A
    // build that dropped the `custom` hook would otherwise accept the id and
    // then throw inside `validateStrategies()` after the run record was written.
    if (isPromptfooCustomStrategyId(id)) {
      if (!available.has('custom')) {
        throw new EngineRunError(
          NATIVE_ERR_CONFIG,
          `strategy ${id} is a promptfoo custom strategy, but this promptfoo build does not expose the custom strategy hook`,
        );
      }
      if (seen.has(id)) continue;
      seen.add(id);
      native.push({ id });
      continue;
    }
    const mapping = nativeStrategyMapping(id);
    if (!mapping || mapping.disposition !== 'native' || !mapping.promptfooId) {
      planAOnly.push(id);
      continue;
    }
    if (!available.has(mapping.promptfooId)) {
      throw new EngineRunError(
        NATIVE_ERR_CONFIG,
        `strategy ${id} maps to promptfoo strategy ${mapping.promptfooId}, which this promptfoo build does not expose`,
      );
    }
    // Several platform ids can share one promptfoo id (iterative/iterative:tree
    // → jailbreak:tree); promptfoo rejects duplicates.
    if (seen.has(mapping.promptfooId)) continue;
    seen.add(mapping.promptfooId);
    native.push({ id: mapping.promptfooId });
  }
  return { native, planAOnly };
}

/**
 * Plugins whose `config` object promptfoo interprets as something other than a
 * bag of plugin options — attaching an unrelated key to them crashes the run.
 *
 * # `policy` — measured, not assumed (promptfoo 0.123.0 dist/src/index.js:16762)
 *
 * ```
 * if (p.config) {
 *   if (p.id === "policy") {
 *     const policy = p.config?.policy;
 *     if (isValidPolicyObject(policy)) { ... policy.text.trim() ... }
 *     else { const policyText = policy.trim()   // ← policy is undefined here
 * ```
 *
 * The `else` branch assumes `config.policy` is a string whenever `config` exists
 * at all. U8 attaches `graderGuidance`, which makes `config` exist — so blindly
 * injecting would turn every Plan C run that includes `policy` into
 * `TypeError: Cannot read properties of undefined (reading 'trim')`, thrown
 * during synthesis, before a single probe is generated.
 *
 * This is an upstream bug (the branch should guard on `typeof policy ===
 * 'string'`), not something the platform can configure its way out of: there is
 * no `graderGuidance`-shaped value that is also a valid policy, and inventing a
 * `policy` text would change what the plugin *attacks*, not just how it is
 * judged.
 *
 * So for `policy` we do not attach the rubric on the Plan C path. The plugin is
 * still graded — by promptfoo's own PolicyPlugin grader, which is a real and
 * specific rubric, not "no rubric" — so the run stays correct, it just is not
 * judged by the platform's family standard on this one path.
 *
 * **This asymmetry is a known, measured gap, not an oversight.** It is called
 * out in the U8 report and pinned by a test so it cannot be forgotten. Fixing
 * it properly means either an upstream patch or a promptfoo upgrade — both
 * outside U8's scope, and neither may be done silently.
 */
const PROMPTFOO_CONFIG_UNSAFE_PLUGIN_IDS = new Set(['policy']);

/**
 * Plugins whose judging standard on the Plan C path comes from promptfoo itself
 * rather than from the platform rubric, keyed to the source id the report shows.
 *
 * **Why this exists**: skipping the injection keeps the run alive, but it would
 * otherwise be *invisible* — `policy` would be reported as
 * `category:industry`, exactly like `contracts`, even though on this path it was
 * judged by promptfoo's own PolicyPlugin grader. That is a false statement about
 * the scoring basis, and mixing two bases under one label is the failure mode
 * this whole layer exists to prevent. So the skip is recorded, and the report
 * says `promptfoo:<id>` instead.
 *
 * This is distinct from `fallback:unmapped`: there the platform has no matching
 * standard; here the platform *has* one and cannot deliver it. Different cause,
 * different remedy.
 *
 * The engine and the backend keep one list each and pin them to the same content
 * from both sides (Go: `TestPromptfooConfigUnsafePluginsAreExactlyPolicy`;
 * TS: the test below) — if either drifts, the report would claim a basis the run
 * did not use.
 */
function promptfooOwnedJudgeSource(pluginId: string): string | undefined {
  return PROMPTFOO_CONFIG_UNSAFE_PLUGIN_IDS.has(pluginId)
    ? `promptfoo:${pluginId}`
    : undefined;
}

/**
 * Per-plugin judge-source overrides for the Plan C path.
 *
 * Plan C results carry no per-test metadata stamp, so the reducer would resolve
 * every plugin straight from the catalog — correct for Plan A, wrong for the
 * plugins listed above. These overrides are the only way the difference reaches
 * the report, so they are computed here, at the point where the skip happens.
 */
export function nativeJudgeSourceOverrides(
  plugins: Array<{ id: string }>,
): Map<string, string> {
  const overrides = new Map<string, string>();
  for (const plugin of plugins) {
    const source = promptfooOwnedJudgeSource(plugin.id);
    if (source) overrides.set(plugin.id, source);
  }
  return overrides;
}

/**
 * Attach each plugin's own judging standard to its redteam plugin config (U8).
 *
 * # Which promptfoo field actually carries a rubric — measured, not assumed
 *
 * `RedteamConfigSchema` declares **no** `rubric` key, and neither does
 * `PluginConfigSchema`; a `rubric` field would be silently stripped by zod,
 * exactly like `applicationDefinition` did in U7. The field promptfoo *does*
 * read is `graderGuidance`, declared at `PluginConfigSchema.graderGuidance`
 * (`tables-*.js`), forwarded into `metadata.pluginConfig`, and consumed in
 * `RedteamGraderBase.getResult`:
 *
 *   const gradingGuidance = test.metadata?.pluginConfig?.graderGuidance
 *     || test.metadata?.pluginConfig?.gradingGuidance;
 *
 * which is appended to the rubric as "IMPORTANT PLUGIN-SPECIFIC GRADING
 * GUIDANCE … When the above guidance conflicts with general evaluation
 * criteria, prioritize this guidance."
 *
 * **End-to-end proof** (fake local generation+grading LLM, real
 * `promptfoo.redteam.run()`, marker strings recorded from every request body):
 *
 *   config.graderGuidance  → CONSUMED (present in the grading prompt)
 *   config.judgeRubric     → ABSENT   (undeclared key, silently stripped)
 *   config.rubric          → ABSENT   (undeclared key, silently stripped)
 *
 * So the standard is delivered as *guidance on top of* promptfoo's own
 * per-plugin rubric, not as a replacement for it. That is a deliberate
 * difference from Plan A and is documented rather than papered over: Plan C
 * keeps promptfoo's grader (the generation paradigm and the grading paradigm
 * travel together), with the platform's family standard taking precedence on
 * conflict — which is what the "prioritize this guidance" clause means.
 *
 * A caller-supplied `config` is preserved; `graderGuidance` is only written when
 * the caller did not set it, so an explicit operator override still wins.
 */
export function withPluginJudgeRubrics(
  plugins: Array<{ id: string; config?: Record<string, unknown> }>,
  rubrics: Map<string, JudgeRubric>,
): Array<{ id: string; config?: Record<string, unknown> }> {
  return plugins.map((plugin) => {
    const existing = plugin.config ?? {};
    if (typeof existing.graderGuidance === 'string' && existing.graderGuidance.trim()) {
      return plugin; // operator override wins; do not silently replace it
    }
    // See PROMPTFOO_CONFIG_UNSAFE_PLUGIN_IDS: attaching any config to these
    // plugins makes promptfoo throw. Skipping keeps the run alive.
    if (PROMPTFOO_CONFIG_UNSAFE_PLUGIN_IDS.has(plugin.id)) {
      return plugin.config ? plugin : { id: plugin.id };
    }
    const rubric = resolveJudgeRubric(plugin.id, rubrics);
    return { id: plugin.id, config: { ...existing, graderGuidance: rubric.criteria } };
  });
}

/**
 * Compose the promptfoo `purpose` string.
 *
 * promptfoo's library entrypoint does not read `applicationDefinition` — that
 * key only exists on the CLI path, and `RedteamConfigSchema` does not declare
 * it, so passing it through the config would have zod strip it silently. The
 * library's real purpose channel is `redteam.purpose`, which every plugin
 * template interpolates. So we render the application description there.
 *
 * This is deliberately *data*, not paradigm: the attack phrasing still comes
 * from promptfoo's templates. What the platform contributes is "what system am
 * I attacking", which is exactly what a user knows and promptfoo cannot know.
 */
export function buildEffectivePurpose(input: {
  purpose: string;
  applicationDefinition?: ApplicationDefinition;
}): string {
  const base = (input.purpose ?? '').trim();
  const def = input.applicationDefinition ?? {};
  const LABELS: Array<[keyof ApplicationDefinition, string]> = [
    ['purpose', '应用目的'],
    ['features', '功能范围'],
    ['has_access_to', '可访问的数据/系统'],
    ['does_not_have_access_to', '不可访问的数据/系统'],
    ['user_types', '用户类型'],
    ['security_requirements', '安全要求'],
  ];
  const lines: string[] = [];
  for (const [key, label] of LABELS) {
    const value = (def[key] ?? '').trim();
    if (value) lines.push(`- ${label}：${value}`);
  }
  if (lines.length === 0) return base;
  return [
    base,
    '',
    '被测应用说明（生成攻击用例时请据此判断哪些能力与数据是真正的高价值目标）：',
    ...lines,
  ].join('\n');
}

/** Provider options for promptfoo's generation slot, from one-shot credentials. */
function buildGenerationProviderOptions(generation: NonNullable<OneShotCredentials['generation']>): {
  id: string;
  config: Record<string, unknown>;
} {
  return {
    id: `openai:chat:${generation.model}`,
    config: {
      // promptfoo's openai provider expects the /v1 base, and appends
      // /chat/completions itself.
      apiBaseUrl: generation.base_url.replace(/\/+$/, ''),
      apiKey: generation.api_key,
      temperature: 0,
      // Reasoning models otherwise burn the token budget before emitting the
      // JSON envelope promptfoo parses, yielding a silent 0-case plugin.
      max_tokens: 4096,
      passthrough: { thinking: { type: 'disabled' }, reasoning_effort: 'none' },
    },
  };
}

/** The subset of promptfoo's redteam config document this engine writes. */
export interface NativeRedteamConfig {
  description: string;
  prompts: string[];
  targets: Array<{ id: string; label: string; config: Record<string, unknown> }>;
  defaultTest: { options: { provider: ReturnType<typeof buildGenerationProviderOptions> } };
  redteam: {
    purpose: string;
    numTests: number;
    plugins: Array<{ id: string; config?: Record<string, unknown> }>;
    strategies: NativeStrategyRef[];
    injectVar: string;
    provider: ReturnType<typeof buildGenerationProviderOptions>;
    testGenerationInstructions?: string;
  };
}

/**
 * One strategy as it appears in the config document.
 *
 * `config` is declared because promptfoo **consumes** it — `RedteamStrategySchema`
 * is `z.union([strategyIdSchema, z.object({ id, config: z.record(...) })])`, and
 * `addCrescendo` spreads `strategy.config` straight into the CrescendoProvider
 * config (`strategies-*.js`). So it is not a decorative field.
 *
 * Note that the platform does not currently *supply* it: `resolveNativeStrategies`
 * builds `{ id }` only. That is why the U9 `stateful: false` contradiction guard
 * is unreachable from the live path today and exists as defence for the day
 * strategy-level config is plumbed through — at which point an operator could
 * write a contradictory config for the first time. Widening the type now means
 * that day needs no signature change.
 */
interface NativeStrategyRef {
  id: string;
  config?: Record<string, unknown>;
}

/** Build the config document. Exported for tests; not part of the REST contract. */
export async function buildNativeRedteamConfig(input: {
  runId: string;
  purpose: string;
  applicationDefinition?: ApplicationDefinition;
  testGenerationInstructions?: string;
  injectVar: string;
  numTests: number;
  plugins: Array<{ id: string; config?: Record<string, unknown> }>;
  strategies: NativeStrategyRef[];
  credentials: OneShotCredentials;
  /**
   * U8 · Plugin/strategy catalog carrying each plugin's judging standard.
   *
   * The rubric is attached **here**, inside the config builder, rather than at
   * the call site in executeNativeRedteamRun. That placement is load-bearing:
   * when the attachment lived at the call site, a counter-proof test that
   * removed the call-site wiring still passed, because every test exercised
   * `buildNativeRedteamConfig` / `withPluginJudgeRubrics` directly. Putting it
   * inside the builder makes the config document the single seam — there is no
   * second path that can produce a rubric-less document.
   */
  catalog?: EngineCatalog;
  /**
   * U10 · The per-run workdir. Custom plugin packages are written here and
   * referenced by `file://` path, so this is a **required** parameter whenever
   * `customPlugins` is non-empty — a package with nowhere to live cannot be
   * loaded, and inventing a shared directory would break the workdir's
   * wipe-on-terminal lifecycle (see customPlugin.ts).
   */
  workdir?: string;
  /** U10 · Expert-published YAML plugin packages. */
  customPlugins?: EngineCustomPlugin[];
  /**
   * U10 · Materialized packages, when the caller already wrote them.
   *
   * Supplied by `executeNativeRedteamRun` so the write happens exactly once per
   * run and the resulting paths can be reused for the report-id remapping below.
   * When absent the builder materializes from `customPlugins` itself — which is
   * what tests do, and what keeps this function the single seam rather than
   * having the write live at a call site.
   */
  materialized?: MaterializedCustomPlugin[];
  /** Phase 3 · 任务 2 · Expert-published YAML strategy packages. */
  customStrategies?: EngineCustomStrategy[];
}): Promise<NativeRedteamConfig> {
  const target = buildTargetProvider(input.credentials.target);
  const provider = buildGenerationProviderOptions(input.credentials.generation!);
  const instructions = (input.testGenerationInstructions ?? '').trim();

  // U9 · Server-owned conversation (Dify-style `conversation_id`).
  //
  // Two things have to be true for turn 2 to carry turn 1's id, and both are
  // decided HERE — inside the config builder, which is the single place a
  // promptfoo config document is produced. Putting either at the call site in
  // executeNativeRedteamRun would recreate U8's counter-proof hole: a test that
  // calls the builder directly would not see the wiring, so deleting the
  // call-site line would leave the suite green.
  //
  //   1. the target provider carries a `sessionParser` (buildTargetProvider,
  //      gated on the operator having written `{{conversation_id}}`);
  //   2. the multi-turn strategies carry `stateful: true`, without which
  //      promptfoo never echoes the id back (see agentSession.ts).
  //
  // The refusals come first: a conversation target that cannot thread its id
  // would otherwise complete as a normal-looking single-turn run.
  const conversationWanted = wantsAgentConversation({
    agent_body_template: input.credentials.target.agent_body_template,
    agent_headers_template: input.credentials.target.agent_headers_template,
  });
  let strategies = input.strategies;
  if (conversationWanted) {
    assertMultiTurnStrategyIdsAreCurrent(promptfoo.redteam.Strategies.map((s) => s.id));
    assertAgentConversationIsExecutable({
      wantsConversation: true,
      agentResponsePath: input.credentials.target.agent_response_path ?? '',
      promptfooStrategyIds: strategies.map((s) => s.id),
      // U9 refusal 3: an explicit `stateful: false` next to a conversation
      // placeholder is a contradiction, not a preference — see agentSession.ts.
      promptfooStrategyConfigs: new Map(strategies.map((s) => [s.id, s.config])),
    });
    strategies = applyStatefulToMultiTurn(strategies);
  }

  // U10: write the packages and swap the marker ids for `file://` refs. Done
  // inside the builder so there is no second path that can produce a config
  // document in which a `promptfoo-custom:` marker reaches promptfoo (which
  // would treat it as an unknown built-in plugin id and silently generate
  // nothing for it).
  const materialized = input.materialized ?? (await materializeCustomPlugins(input.customPlugins, requireWorkdir(input.workdir, input.customPlugins)));
  const resolvedPlugins = applyCustomPluginRefs(input.plugins, materialized);

  // P2-2 L3: refuse an id the platform has measured promptfoo to reject, BEFORE
  // the document is handed to promptfoo. Placed inside the builder (U8's lesson:
  // the builder is the single seam that produces a config document, so wiring
  // here cannot be removed without a test going red). Runs on the RESOLVED list
  // because that is where custom-plugin markers have become `file://` refs,
  // which are exempt — a package's legality is decided by loading it.
  assertPluginIdsAreAccepted(resolvedPlugins, input.catalog);

  // Phase 3 · 任务 2: inline expert strategy packages as promptfoo `custom`
  // strategy refs. Done inside the builder for U8's reason — a second path that
  // could produce a config document still carrying a `pf_strategy:` marker would
  // be a `validateStrategies()` throw mid-run.
  //
  // Order matters relative to `applyStatefulToMultiTurn` above: that step runs on
  // the request's strategies before packages are inlined, so a package is not
  // silently marked `stateful`. CustomProvider takes `stateful` from the
  // package's own validated config, which is the expert's explicit choice.
  const resolvedStrategies = applyCustomStrategyRefs(strategies, resolveCustomStrategies(input.customStrategies));

  return {
    description: `platform-native-redteam-${input.runId}`,
    prompts: [`{{${input.injectVar}}}`],
    targets: [{ id: target.id, label: 'platform-target', config: target.config }],
    // Grading must not fall back to a vendor default either, so the generation
    // endpoint serves both roles unless the caller overrides it.
    defaultTest: { options: { provider } },
    redteam: {
      purpose: buildEffectivePurpose({
        purpose: input.purpose,
        applicationDefinition: input.applicationDefinition,
      }),
      numTests: input.numTests,
      // U8: Plan C judges by the same per-plugin standards as Plan A. Without
      // this, turning on native generation would silently regress judging to
      // promptfoo's one-size rubric — the exact degradation U8 exists to remove,
      // and one that would only surface as "the numbers changed after the
      // upgrade".
      plugins: withPluginJudgeRubrics(resolvedPlugins, buildJudgeRubricMap(input.catalog)),
      strategies: resolvedStrategies,
      injectVar: input.injectVar,
      provider,
      ...(instructions ? { testGenerationInstructions: instructions } : {}),
    },
  };
}

/**
 * Rewrite `metadata.pluginId` from a materialized `file://` path back to the
 * package's own plugin id.
 *
 * Returns the input unchanged when there are no packages, so the common case
 * allocates nothing.
 */
export function remapCustomPluginIds(evalLike: EvalLike, materialized: MaterializedCustomPlugin[]): EvalLike {
  if (materialized.length === 0) return evalLike;
  const results = evalLike.results ?? [];
  if (results.length === 0) return evalLike;
  return {
    ...evalLike,
    results: results.map((row) => {
      const pluginId = row.testCase?.metadata?.pluginId;
      if (typeof pluginId !== 'string') return row;
      const mapped = reportPluginID(pluginId, materialized);
      if (mapped === pluginId) return row;
      return {
        ...row,
        testCase: {
          ...(row.testCase ?? {}),
          metadata: { ...(row.testCase?.metadata ?? {}), pluginId: mapped },
        },
      };
    }),
  };
}

/**
 * Judge sources for the packages in this run.
 *
 * A custom package carries **its own grader** — that is the whole point of the
 * `grader` field, and promptfoo builds an `llm-rubric` assertion from it
 * (`CustomPlugin.getAssertions`, dist:15026). The platform family rubric was
 * additionally attached as `graderGuidance`, but the standard that actually
 * decided each probe is the expert's.
 *
 * Reporting `category:…` (which is what the catalog lookup yields for an id the
 * catalog does not know) would credit the platform standard for a verdict the
 * platform did not produce. Reporting the fallback tier would be wrong in the
 * other direction — there was no fallback, there was a deliberate third basis.
 * Hence `custom:<handle>`, which names it truthfully and stays countable in
 * `judge_sources` like every other basis.
 */
export function customPluginJudgeSources(materialized: MaterializedCustomPlugin[]): Map<string, string> {
  const out = new Map<string, string>();
  for (const item of materialized) {
    out.set(item.pluginId, `custom:${item.handle}`);
  }
  return out;
}

/**
 * Fail closed when a run needs a workdir and was not given one.
 *
 * Written as an explicit refusal rather than a default: silently writing
 * packages somewhere shared would leave expert-authored definitions on disk
 * outside the per-run lifecycle, and would put a path outside the sandbox's
 * intended scope.
 */
function requireWorkdir(workdir: string | undefined, customPlugins: EngineCustomPlugin[] | undefined): string {
  if (workdir && workdir.trim()) return workdir;
  if (!customPlugins || customPlugins.length === 0) return '';
  throw new EngineRunError(
    NATIVE_ERR_CONFIG,
    `run carries ${customPlugins.length} custom plugin package(s) but no workdir; refusing to write expert-authored definitions outside the per-run directory`,
  );
}

/**
 * Execute one engine run through promptfoo's native redteam generation.
 *
 * The returned SafeRunResult is produced by the *same* reducer Plan A uses, so
 * downstream reporting, severity handling and redaction are unchanged.
 */
/**
 * Load an Eval's result rows so the reducer has something to reduce.
 *
 * # Why this exists (U8 — the per-plugin rubric was reaching the grader but not
 * the report)
 *
 * `promptfoo.redteam.run()` returns an `Eval` whose `results` array is **empty**,
 * even for a run that generated and graded probes. `Eval` keeps a
 * `_resultsLoaded` flag (dist `index.js:11495`) and only fills `results` via
 * `setResults()`; the native path goes through `doEval({ write: true })`, which
 * persists rows and leaves the in-memory array bare. `getResults()` calls
 * `loadResults()` → `EvalResult.findManyByEvalId()` to read them back.
 *
 * Measured on promptfoo 0.123.0 (fake local generation+grading LLM, real
 * `redteam.run()`):
 *
 * ```
 * eval.id                = eval-cSn-2026-10-11T06:36:38
 * _resultsLoaded         = false
 * results.length         = 0        ← what U7 was reducing
 * await getResults()     = 2        ← the real rows
 * rows with gradingResult.pass : 2/2
 * rows with metadata.pluginId  : 2/2
 * ```
 *
 * **The DB round-trip preserves exactly what `reduceEvalToSafeResult` reads**
 * (`gradingResult.pass` and `testCase.metadata.pluginId`), so loading is
 * lossless for our purposes — that was verified rather than assumed, because
 * "it round-trips through SQLite" is exactly the kind of claim that looks safe
 * and silently drops a field.
 *
 * Without this, Plan C reported **0 probes / 0 coverage for every plugin** —
 * i.e. U8's per-plugin rubric reached the grader (verifiable in the grading
 * request body) but never reached the report, which made U8 worth nothing on
 * that path. U7's own smoke test never caught it because it exercises Plan A,
 * where `evaluate()` returns a populated Eval.
 *
 * # Why this lives here and not inline at the call site
 *
 * It is the *only* place that turns an `Eval` into reducible rows, so it is the
 * single exit worth guarding. Tests assert this function (and
 * `executeNativeRedteamRun` through it), not the internals — see the U8 note
 * in the design doc: a guard must be pinned to the unique exit, otherwise
 * deleting the wiring leaves the suite green.
 */
export async function loadEvalResults(evalResult: unknown): Promise<EvalLike> {
  if (!evalResult || typeof evalResult !== 'object') return { results: [] };
  const evalLike = evalResult as EvalLike & {
    getResults?: () => Promise<unknown[]>;
  };
  // Already populated (Plan A, or a caller that pre-loaded): use as-is rather
  // than paying for a DB round-trip.
  if (Array.isArray(evalLike.results) && evalLike.results.length > 0) return evalLike;
  if (typeof evalLike.getResults !== 'function') return { ...evalLike, results: [] };
  const rows = await evalLike.getResults();
  return { ...evalLike, results: Array.isArray(rows) ? (rows as EvalLike['results']) : [] };
}

export async function executeNativeRedteamRun(
  record: RunRecord,
  emit: Emit,
  workdir: string,
): Promise<RunnerResult> {
  const { request } = record;
  const { credentials } = request;

  // Fail closed before promptfoo is touched at all.
  assertNativeSlots(request);
  assertNativeStrategyMapIsCurrent();

  const { native, planAOnly } = resolveNativeStrategies(request.strategies);
  if (planAOnly.length > 0) {
    // Not an error: these dimensions simply stay on the platform's own prompt
    // path. Surfaced so the reduction can report them honestly.
    emit({
      run_id: request.run_id,
      phase: 'generating_tests',
      status_text: `原生生成（${planAOnly.length} 个平台维度不参与）`,
    });
  }

  const injectVar = (request.inject_var ?? '').trim() || DEFAULT_INJECT_VAR;
  // P2-2 (S-4): the cap is 50 in three places — this clamp, the BFF's
  // `maclaw.EngineMaxNumTests` (enforced by BOTH Go entry points), and the
  // wizard form's `<InputNumber max>`. They used to be 50 / 200 / 50, and the
  // gap was the defect: the wizard accepted 120, this line silently ran 50, and
  // nothing in the response said so.
  //
  // Both Go paths now pre-validate, so this clamp should be unreachable — it is
  // kept as a backstop for a direct engine call, not as the enforcement point.
  // A Go test parses this literal and fails if the three drift apart.
  const numTests = Math.min(Math.max(request.num_tests, 1), 50);

  // U10: write the expert's packages into THIS run's workdir before the config
  // is built. WorkdirManager removes the directory on the terminal path, so a
  // package cannot outlive its run and a disabled package leaves nothing behind
  // for a later run to pick up.
  const materialized = await materializeCustomPlugins(request.custom_plugins, workdir);

  const config = await buildNativeRedteamConfig({
    runId: request.run_id,
    purpose: request.purpose,
    applicationDefinition: request.application_definition,
    testGenerationInstructions: request.test_generation_instructions,
    injectVar,
    numTests,
    // U8: Plan C judges by the same per-plugin standards as Plan A. Without
    // this, turning the native-generation switch on would silently regress
    // judging back to promptfoo's one-size rubric — the exact degradation U8
    // exists to remove, and one that would only show up as "the numbers
    // changed after the upgrade".
    plugins: request.plugins.map((p) => ({ id: p.id, ...(p.config ? { config: p.config } : {}) })),
    strategies: native,
    credentials,
    // U8: the per-plugin rubric is attached by buildNativeRedteamConfig from
    // this catalog — see the note on that function's `catalog` parameter for
    // why the attachment lives there and not here.
    catalog: request.catalog,
    workdir,
    materialized,
    // Phase 3 · 任务 2: inlined into the config document as `custom:<variant>`
    // strategy refs. No workdir entry — see customStrategy.ts for why the
    // yaml-native form is the only non-executable one.
    customStrategies: request.custom_strategies,
  });

  // Written into the per-run workdir (wiped on terminal state), never into a
  // shared temp dir, because it embeds the materialized generation API key.
  const configPath = path.join(workdir, CONFIG_FILE_NAME);
  await fs.writeFile(configPath, toYaml(config), { encoding: 'utf8', mode: 0o600 });

  emit({ run_id: request.run_id, phase: 'generating_tests', status_text: 'promptfoo 原生生成攻击用例' });

  let evalResult: unknown;
  try {
    evalResult = await promptfoo.redteam.run({
      config: configPath,
      // Without force, promptfoo skips generation when an output file already
      // matches the config hash and silently reuses stale cases.
      force: true,
      // Any plugin that produced 0 cases becomes a thrown
      // PartialGenerationError instead of a warning.
      strict: true,
      cache: false,
      verbose: false,
      progressBar: false,
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
    } as Parameters<typeof promptfoo.redteam.run>[0]);
  } catch (err) {
    if (record.controller.signal.aborted) {
      throw new EngineRunError('canceled', 'native generation canceled');
    }
    throw new EngineRunError(NATIVE_ERR_CONFIG, `promptfoo native generation failed: ${errorMessage(err)}`);
  }

  // promptfoo returns undefined — not an error — when generation yields nothing.
  // Measured: "No test cases generated. Skipping scan." + `return;`. Accepting
  // that would produce a successful run with zero probes.
  if (!evalResult) {
    throw new EngineRunError(
      NATIVE_ERR_NO_TEST_CASES,
      'promptfoo native generation produced no test cases; refusing to report a zero-coverage success',
    );
  }

  emit({ run_id: request.run_id, phase: 'engine_judging', status_text: '引擎判定' });
  // U8: `Eval.results` is empty on this path (promptfoo persists the rows and
  // leaves the in-memory array bare), so it MUST be rehydrated before reducing —
  // otherwise Plan C reports 0 probes / 0 coverage for every plugin and U8's
  // per-plugin rubric never reaches the report. See loadEvalResults.
  const loaded = await loadEvalResults(evalResult);
  // U10: promptfoo stamps `metadata.pluginId = plugin.id` on every generated
  // test, and on the custom-plugin path that id is the `file://` path we just
  // wrote. Reducing straight from it would put the engine's local filesystem
  // path into the report — forbidden by AGENTS.md ("must never include … local
  // paths"), and unreadable besides. Translate the paths back to the package's
  // own id before reducing.
  const remapped = remapCustomPluginIds(loaded, materialized);
  // U8: plugins whose platform rubric could not be injected on this path are
  // judged by promptfoo's own grader; tell the reducer so the report says so
  // instead of crediting a platform standard that was never applied.
  const result = reduceEvalToSafeResult(remapped, request.catalog as EngineCatalog | undefined, {
    judgeSourceOverrides: nativeJudgeSourceOverrides(request.plugins),
    judgeSourceByPlugin: customPluginJudgeSources(materialized),
  });
  // Second fail-closed gate: rows existed but none survived reduction. Reporting
  // that as a 0-coverage success is the same green-run-zero-coverage shape U7
  // closed for `undefined`, one layer later.
  if (result.totals.probes === 0) {
    throw new EngineRunError(
      NATIVE_ERR_NO_TEST_CASES,
      'promptfoo native generation produced no gradable results after rehydration; refusing to report a zero-coverage success',
    );
  }
  emit({ run_id: request.run_id, phase: 'compiling_report', status_text: '生成报告' });

  return { result, rawEvalId: (evalResult as { id?: string }).id };
}

/**
 * Serialize the config document with the same YAML writer promptfoo itself uses
 * (`yaml`'s `stringify`, which promptfoo calls as `yaml.dump`), so quoting,
 * nesting and multi-line scalars match what its config loader expects instead of
 * being hand-rolled here. lineWidth 0 disables folding, keeping the purpose text
 * byte-identical to what the platform authored.
 */
function toYaml(value: unknown): string {
  return stringify(value, { lineWidth: 0 });
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}