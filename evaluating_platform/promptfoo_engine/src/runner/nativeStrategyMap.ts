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

/**
 * How a platform strategy id maps onto promptfoo's vocabulary.
 *
 * P3-3 removed the second disposition (`plan_a_only`). It existed for platform
 * dimensions that only Plan A could produce; with Plan A deleted there is no
 * "elsewhere" for an id to belong to, so an id is either mapped to a promptfoo
 * strategy or it is an error (see `resolveNativeStrategies`).
 */
export interface NativeStrategyMapping {
  /** promptfoo's own id. */
  readonly promptfooId: string;
}

// P3-3：这里原本有一张 PLAN_A_ONLY 表，记录 9 个平台自造维度（direct / role-play /
// encoding / multi-turn / assumed-knowledge / few-shot / many-shot / translate /
// otherEncodings）以及「promptfoo 无对应策略」的理由。它们已从目录移出并在
// retiredStrategyCatalog（Go 侧）与 RETIRED_ENGINE_STRATEGIES（前端）里记录处置，
// 提交边界就会拒绝 —— 所以引擎侧不再需要一份「认识它们但知道它们无效」的表。
//
// 保留这条注释是因为它是这段代码为什么变短的原因，而不是一个可以静默消失的表。

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
  Object.fromEntries(Object.entries(NATIVE).map(([id, promptfooId]) => [id, { promptfooId }])),
);

/** Resolve one platform strategy id. Unknown ids return `undefined` — the caller
 * decides whether that is an error (it is: `resolveNativeStrategies` throws). */
export function nativeStrategyMapping(strategyId: string): NativeStrategyMapping | undefined {
  return MAPPING[(strategyId ?? '').trim()];
}

/** Every platform strategy id this module knows how to classify. */
export function knownNativeStrategyIds(): string[] {
  return Object.keys(MAPPING).sort();
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
        'A promptfoo upgrade changed its strategy vocabulary — update src/runner/nativeStrategyMap.ts before deploying.',
    );
  }
}