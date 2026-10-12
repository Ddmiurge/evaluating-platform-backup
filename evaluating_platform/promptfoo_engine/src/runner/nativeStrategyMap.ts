/**
 * nativeStrategyMap.ts — U7 · platform strategy id → promptfoo native strategy id.
 *
 * # Why a mapping is unavoidable
 *
 * The platform ships 30 strategy ids (`internal/maclaw/promptfoo_plugin_catalog.go`)
 * and promptfoo ships 35 (`promptfoo.redteam.Strategies`). They only partially
 * overlap, and promptfoo *throws* on an id it does not recognise:
 *
 *     validateStrategies()  // strategies-<hash>.js
 *     throw new Error(`Invalid strategy(s): ${invalid}. Valid strategies are: …`)
 *
 * So an unmapped platform id is not a degraded run, it is a hard mid-run crash.
 * This module is the single place that decides, per platform id, one of:
 *
 *   - `native`     → forward promptfoo's own id; promptfoo generates with it
 *   - `plan_a_only`→ the platform invented this dimension; there is no native
 *                    counterpart, so it stays on the Plan A prompt-synthesis
 *                    path and is NOT put into promptfoo's `strategies`
 *
 * # Why this is not a second copy of U6's table
 *
 * U6's `remote_dep.json` answers a different question — "does this capability
 * need remote generation?" — and its strategy section only holds the 4 entries
 * that are *gated*, because that is all that table is used for. This module
 * answers "what is promptfoo's id for it, and can promptfoo generate it
 * locally?". The two are complementary, and the overlap is asserted empty in
 * `nativeStrategyMap.test.ts`: a gated strategy must never appear here as
 * `native`, because handing a remote-gated strategy to promptfoo is precisely
 * the silent-0-coverage failure U6 exists to prevent.
 *
 * The table is validated against the *installed* promptfoo at runtime
 * (see assertNativeStrategyMapIsCurrent), so a promptfoo upgrade that renames or
 * drops a strategy fails loudly instead of producing a run that generates
 * nothing.
 */
import promptfoo from 'promptfoo';

/** How a platform strategy id participates in the native generation path. */
export type NativeStrategyDisposition = 'native' | 'plan_a_only';

export interface NativeStrategyMapping {
  /** promptfoo's own id. Absent exactly when disposition is `plan_a_only`. */
  readonly promptfooId?: string;
  readonly disposition: NativeStrategyDisposition;
  /** Why this id is not natively generatable; surfaced in error messages. */
  readonly reason?: string;
}

/**
 * Platform-invented strategy dimensions. These describe *how the platform wraps
 * an attack prompt* (role-play framing, encoding, translation, …). They are not
 * promptfoo attack transforms, and promptfoo has no strategy that does the same
 * job: `base64`/`rot13`/`hex`/`leetspeak`/`homoglyph`/`morse`/`piglatin`/
 * `camelcase`/`emoji` are the real encoding strategies and ARE mapped below.
 * Forwarding `role-play` to promptfoo would throw; silently dropping it would
 * understate coverage. Plan A keeps producing these.
 */
const PLAN_A_ONLY: Readonly<Record<string, string>> = {
  direct: '直接攻击是平台自造的提示词维度，promptfoo 无对应策略',
  'role-play': '角色扮演是平台自造的提示词维度，promptfoo 无对应策略',
  encoding: '编码混淆是平台自造的总称维度；promptfoo 的编码策略为 base64/rot13/hex/leetspeak 等具体项',
  'multi-turn': '多轮诱导是平台自造的提示词维度；promptfoo 的多轮策略为 crescendo/goat/jailbreak:* 等具体项',
  'assumed-knowledge': '假设已知是平台自造的提示词维度，promptfoo 无对应策略',
  'few-shot': '少样本诱导是平台自造的提示词维度，promptfoo 无对应策略',
  'many-shot': '多样本诱导是平台自造的提示词维度，promptfoo 无对应策略',
  translate: '翻译绕过是平台自造的提示词维度；promptfoo 用 language 配置而非 strategy 表达',
  otherEncodings: '其他编码变体是平台对 morse/piglatin/camelcase/emoji 等的聚合项，无法映射为单一原生 id',
  // Phase 3 · `simba` used to be mapped `simba → simba` below. That was wrong
  // and is the reason this list is worth reading: promptfoo 0.123.0 keeps
  // `simba` only as a deprecated stub whose action logs
  // "The \"simba\" strategy has been deprecated and removed. This strategy will
  // be skipped" and returns `[]`. Forwarding it produced a green run in which
  // the strategy provably generated zero cases and the report still listed it as
  // applied — the silent-0-coverage shape U6 exists to eliminate, reached
  // through a path U6's remote-dependency table does not cover (simba needs no
  // remote generation; it simply does nothing).
  //
  // Reclassified rather than deleted so the id still resolves to a decision:
  // an expert who selected it gets Plan A synthesis plus this reason instead of
  // a strategy that silently generates nothing.
  simba: 'promptfoo 0.123.0 已废弃并移除该策略（实际生成 0 条用例），平台不再转发；请改用 jailbreak:hydra',

  // `retry` needs `config.targetIds`, which the platform does not send (it
  // forwards `{ id }` only), so promptfoo throws *"No target IDs found in
  // config"* — mid-run, after the run record exists. U6's table does not gate it
  // because it needs no remote generation; the dependency is on caller-supplied
  // config instead, which is a different failure U6 does not model.
  retry: '需要调用方提供 targetIds 配置，平台当前不下发，promptfoo 会在评测进行中抛错',

  // `math-prompt` falls back to a local path that calls an OpenAI provider
  // directly when remote generation is disabled, so it fails with "API key is
  // not set" in this deployment. Measured, not inferred.
  mathPrompt: '当前部署下需要可用的 OpenAI 凭据（远程生成已屏蔽），无法执行',
};

/**
 * Platform id → promptfoo id for strategies promptfoo owns. Every value here is
 * asserted to exist in `promptfoo.redteam.Strategies` by
 * assertNativeStrategyMapIsCurrent().
 *
 * Note the four ids U6 also knows about (bestOfN, likert, citation,
 * singleTurnComposite) are deliberately ABSENT: they are remote-gated, so U6's
 * pre-flight rejects the run before it ever reaches this module. Listing them
 * here would be a second source of truth that could drift out of step with
 * U6's table and turn a clean rejection into a mid-run throw.
 */
const NATIVE: Readonly<Record<string, string>> = {
  base64: 'base64',
  rot13: 'rot13',
  hex: 'hex',
  leetspeak: 'leetspeak',
  homoglyph: 'homoglyph',
  crescendo: 'crescendo',
  goat: 'goat',
  hydra: 'jailbreak:hydra',
  goblin: 'jailbreak:goblin',
  iterative: 'jailbreak:tree',
  'iterative:tree': 'jailbreak:tree',
  'iterative:meta': 'jailbreak:meta',
  // `retry` and `mathPrompt` used to be mapped here. Both abort or fail when the
  // platform forwards them, because the platform sends `{ id }` with **no**
  // strategy config (`resolveNativeStrategies` builds the id only), and:
  //
  //   retry       -> invariant "No target IDs found in config. The retry
  //                  strategy requires at least one target ID" — thrown after
  //                  the run record is persisted.
  //   math-prompt -> needs an OPENAI_API_KEY: with remote generation fail-closed
  //                  it falls through to a local path that calls an OpenAI
  //                  provider directly, and dies with "API key is not set".
  //
  // U6's remote-dependency table gates neither, so neither was caught before
  // this discovery pass. Both are reclassified below rather than deleted, so an
  // expert who selects them gets Plan A synthesis plus a reason instead of a
  // mid-run abort.
  mischievousUser: 'mischievous-user',
  authoritativeMarkupInjection: 'authoritative-markup-injection',

  // ---- Phase 3 · identity mappings for strategies found by discovering
  // promptfoo's catalogue instead of hand-listing 30 ids.
  //
  // These need no translation: promptfoo already calls them by these names.
  // They were unreachable before only because the platform's list was
  // hand-maintained and had drifted, not because anything blocked them.
  //
  // Every id below is asserted to exist in the installed promptfoo by
  // assertNativeStrategyMapIsCurrent(), and the platform only offers the ones
  // its own generated table classifies as runnable — so a strategy that cannot
  // generate here never reaches this map from the API. The four that cannot
  // (best-of-n, citation, jailbreak:composite, jailbreak:likert) stay absent
  // for U6's reason; simba/basic/media stay absent for the reasons recorded in
  // PLAN_A_ONLY and in the platform's catalogue table.
  morse: 'morse',
  piglatin: 'piglatin',
  camelcase: 'camelcase',
  emoji: 'emoji',
  'jailbreak-templates': 'jailbreak-templates',
  layer: 'layer',
  'indirect-web-pwn': 'indirect-web-pwn',
  // Deprecated upstream aliases that still work. Kept mapped because they do
  // generate cases; promptfoo logs a deprecation warning naming the
  // replacement, which is advice, not a refusal.
  'prompt-injection': 'prompt-injection',
  jailbreak: 'jailbreak',

  // `custom` is promptfoo's own yaml-native custom-strategy hook: a strategy
  // whose id is `custom` or `custom:<variant>` and whose `config.strategyText`
  // is the attacker's instruction. Phase 3 任务 2 publishes expert-authored
  // strategy packages into it — no `file://`, no JS. It is mapped here so a
  // `custom:<handle>` reference forwarded by the BFF resolves to itself.
  //
  // Only the bare `custom` id is listed. `custom:<variant>` ids are generated
  // per published package and are resolved by `resolveNativeStrategies` before
  // this table is consulted (see isCustomStrategyRef), because a per-package id
  // cannot be enumerated here.
  custom: 'custom',
};

/** The whole table, keyed by platform strategy id. */
const MAPPING: Readonly<Record<string, NativeStrategyMapping>> = Object.freeze(
  Object.fromEntries([
    ...Object.entries(NATIVE).map(([id, promptfooId]) => [id, { promptfooId, disposition: 'native' as const }]),
    ...Object.entries(PLAN_A_ONLY).map(([id, reason]) => [id, { disposition: 'plan_a_only' as const, reason }]),
  ]),
);

/** Resolve one platform strategy id. Unknown ids degrade to `plan_a_only`. */
export function nativeStrategyMapping(strategyId: string): NativeStrategyMapping | undefined {
  return MAPPING[(strategyId ?? '').trim()];
}

/** Every platform strategy id this module knows how to classify. */
export function knownNativeStrategyIds(): string[] {
  return Object.keys(MAPPING).sort();
}

/** Platform strategy ids with no promptfoo counterpart (Plan A only). */
export function planAOnlyStrategyIds(): string[] {
  return Object.keys(PLAN_A_ONLY).sort();
}

/** promptfoo strategy ids this module can produce. */
export function mappedPromptfooStrategyIds(): string[] {
  return Object.values(NATIVE);
}

/**
 * Assert that every id we forward actually exists in the installed promptfoo.
 *
 * This is the anti-drift guard for the table above, and it is deliberately
 * fail-loud (throws) rather than degrading: a renamed upstream strategy would
 * otherwise reach `validateStrategies()` and abort a run mid-generation, or —
 * worse — a future promptfoo could accept an unknown id and generate nothing,
 * reproducing the silent-0-coverage shape U6 was built to eliminate.
 */
export function assertNativeStrategyMapIsCurrent(): void {
  const available = new Set(promptfoo.redteam.Strategies.map((s) => s.id));
  const missing = mappedPromptfooStrategyIds().filter((id) => !available.has(id));
  if (missing.length > 0) {
    throw new Error(
      `[U7] native strategy map is stale: the installed promptfoo does not expose ${missing.join(', ')}. ` +
        'A promptfoo upgrade changed its strategy vocabulary — update src/runner/nativeStrategyMap.ts before enabling ' +
        'PROMPTFOO_ENGINE_NATIVE_GENERATION.',
    );
  }
}