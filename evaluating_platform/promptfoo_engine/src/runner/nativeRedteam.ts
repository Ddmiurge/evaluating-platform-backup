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
import type { ApplicationDefinition, EngineCatalog, OneShotCredentials, RunProgressEvent } from '../types.js';
import type { RunRecord } from '../manager/taskManager.js';
import { EngineRunError } from './generateTests.js';
import { buildTargetProvider, reduceEvalToSafeResult, type RunnerResult } from './redteamRun.js';
import { assertNativeStrategyMapIsCurrent, nativeStrategyMapping } from './nativeStrategyMap.js';

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
    strategies: Array<{ id: string }>;
    injectVar: string;
    provider: ReturnType<typeof buildGenerationProviderOptions>;
    testGenerationInstructions?: string;
  };
}

/** Build the config document. Exported for tests; not part of the REST contract. */
export function buildNativeRedteamConfig(input: {
  runId: string;
  purpose: string;
  applicationDefinition?: ApplicationDefinition;
  testGenerationInstructions?: string;
  injectVar: string;
  numTests: number;
  plugins: Array<{ id: string; config?: Record<string, unknown> }>;
  strategies: Array<{ id: string }>;
  credentials: OneShotCredentials;
}): NativeRedteamConfig {
  const target = buildTargetProvider(input.credentials.target);
  const provider = buildGenerationProviderOptions(input.credentials.generation!);
  const instructions = (input.testGenerationInstructions ?? '').trim();

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
      plugins: input.plugins,
      strategies: input.strategies,
      injectVar: input.injectVar,
      provider,
      ...(instructions ? { testGenerationInstructions: instructions } : {}),
    },
  };
}

/**
 * Execute one engine run through promptfoo's native redteam generation.
 *
 * The returned SafeRunResult is produced by the *same* reducer Plan A uses, so
 * downstream reporting, severity handling and redaction are unchanged.
 */
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
  const numTests = Math.min(Math.max(request.num_tests, 1), 50);

  const config = buildNativeRedteamConfig({
    runId: request.run_id,
    purpose: request.purpose,
    applicationDefinition: request.application_definition,
    testGenerationInstructions: request.test_generation_instructions,
    injectVar,
    numTests,
    plugins: request.plugins.map((p) => ({ id: p.id, ...(p.config ? { config: p.config } : {}) })),
    strategies: native,
    credentials,
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
  const result = reduceEvalToSafeResult(evalResult as never, request.catalog as EngineCatalog | undefined);
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