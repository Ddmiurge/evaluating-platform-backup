/**
 * customStrategy.ts — Phase 3 · 任务 2 · loading expert-published YAML strategy
 * packages into promptfoo's `custom` strategy.
 *
 * # Why this needs no `file://` at all — and why that is the whole design
 *
 * The brief for this task assumed a strategy package would load the way a
 * plugin package does: write a file, reference it by `file://`. Measured against
 * promptfoo 0.123.0, that is not how custom strategies work, and building it
 * that way would have produced a package that loads and does nothing.
 *
 * promptfoo has **two** extension points, and they are not the same shape:
 *
 *   1. `file://x.js` — `loadStrategy()` requires `isJavascriptFile(filePath)`
 *      and throws *"Custom strategy file must be a JavaScript file"* otherwise
 *      (`dist/src/fileExtensions-*.js`). A `file://` strategy is therefore
 *      **only ever JS/TS**: an `import()` of a module exporting `id` and
 *      `action`, i.e. arbitrary code execution inside the engine process, which
 *      holds the materialized generation API key. D-03 rules that out for the
 *      first release, and this module is where that is enforced rather than
 *      merely documented.
 *
 *   2. `custom` / `custom:<variant>` with `config.strategyText` — a **yaml-native**
 *      hook. `isCustomStrategy(id)` is literally
 *      `id === 'custom' || id.startsWith('custom:')`
 *      (`dist/src/tables-*.js`), `RedteamStrategySchema` accepts it via
 *      `strategyIdSchema`, and `validateStrategies()` requires
 *      `config.strategyText` to be a string:
 *
 *          if (isCustomStrategy(strategy.id)) {
 *            if (!strategy.config?.strategyText || typeof strategy.config.strategyText !== 'string')
 *              throw new Error('Custom strategy requires strategyText in config');
 *            continue;
 *          }
 *
 *      `addCustom()` then re-points each test case at
 *      `promptfoo:redteam:custom[:<variant>]` and spreads the config through.
 *
 * So a strategy package is `{ strategyText, … }` inlined into the redteam
 * config. No materialization, no workdir file, no import. That also means it
 * **cannot** smuggle code: there is no path by which a package's bytes reach an
 * `import()`.
 *
 * # Proof that `strategyText` is really consumed (not silently stripped)
 *
 * `14` §5 rule 1 exists because U7's `applicationDefinition` and U8's
 * `config.rubric` were both accepted by a schema and then dropped, and nobody
 * noticed until a run produced no signal. So this was verified by capture, not
 * by reading a schema:
 *
 *   - `dist/src/custom-*.js:26` interpolates `{{customStrategyText}}` into
 *     `CUSTOM_PARENT_TEMPLATE`, the prompt sent to the attack model;
 *   - line 187 sets `customStrategyText: this.config.strategyText || <default>`;
 *   - driving `CustomProvider.callApi()` against a local OpenAI-compatible stub
 *     with a sentinel in `strategyText` produced 11 requests whose **system
 *     message** contained the sentinel verbatim under `###Custom Strategy###`.
 *
 * The default is the reason the round-trip matters: an unconsumed field would
 * fall back to *"Follow the conversation naturally to achieve the objective."*
 * and the run would still succeed, with every package behaving identically to
 * every other package. `assertStrategyTextReachesPrompt` in the test suite
 * pins that the sentinel survives, so the failure mode cannot return quietly.
 *
 * # What is NOT claimed
 *
 * The engine cannot prove `strategyText` is *safe*, only that it is delivered.
 * A strategy text is attacker-controlled prose fed to the generation model, so
 * the sandbox (non-root uid, read-only rootfs, egress restriction) remains the
 * control that matters — see `promptfoo_engine/src/sandbox.ts`.
 */
import { parse as parseYaml } from 'yaml';

/** One strategy package as it arrives on RunRequest. Mirrors Go's EngineCustomStrategy. */
export interface EngineCustomStrategy {
  handle: string;
  strategy_id: string;
  /** Canonical YAML, re-serialized by the BFF from the validated struct. */
  definition: string;
  source_expert_user_id?: string;
  version?: string;
}

/** Machine-readable failure codes. Mirrored by the BFF; part of the contract. */
export const CUSTOM_STRATEGY_ERR_INVALID = 'custom_strategy_definition_invalid';
/** Raised when a strategy id is not a legal `custom` / `custom:<variant>`. */
export const CUSTOM_STRATEGY_ERR_BAD_ID = 'custom_strategy_bad_id';

/**
 * Keys a strategy package may declare.
 *
 * promptfoo does **not** validate this object against a strict schema — its
 * strategy `config` is `z.record(z.string(), z.unknown())`, so unknown keys
 * survive parsing and are spread into the provider config. Two consequences,
 * and they pull in opposite directions:
 *
 *  - Unknown keys are not a load-time failure, so there is no U10-style
 *    "silently dropped, run stays green" hazard to defend against.
 *  - But an unknown key is *worse* than dropped: it is forwarded into the
 *    provider config, where a typo'd `maxTurn` instead of `maxTurns` looks
 *    accepted and does nothing.
 *
 * So this is an allow-list, and it is pinned to the fields measured as consumed
 * in `CustomProvider`'s constructor and `CUSTOM_PARENT_TEMPLATE`. `strategyText`
 * is the only required one; everything else has a documented upstream default.
 */
export const ALLOWED_STRATEGY_KEYS = new Set([
  // Required. The attacker's instruction, interpolated into the attack prompt.
  'strategyText',
  // Measured in CustomProvider's constructor / provider construction:
  'maxTurns',
  'maxBacktracks',
  'stateful',
  'continueAfterSuccess',
  'excludeTargetOutputFromAgenticAttackGeneration',
  // `redteamProvider` selects the generation provider for the attack loop. The
  // engine sets it explicitly from the materialized generation credential: with
  // remote generation fail-closed, CustomProvider falls back to
  // `redteamProviderManager.getProvider({ provider: this.config.redteamProvider })`,
  // and a package must not be able to point that at an endpoint of its choosing.
  // Accepted here only so the field round-trips; `assertStrategyTextReachesPrompt`
  // and the BFF both treat an expert-supplied value as untrusted.
  'redteamProvider',
  'targetId',
]);

/** Raised when a strategy package cannot be used. Carries a readable message. */
export class CustomStrategyError extends Error {
  constructor(
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = 'CustomStrategyError';
  }
}

/** The parsed, validated shape of a strategy package. */
export interface ParsedCustomStrategy {
  strategyText: string;
  maxTurns?: number;
  maxBacktracks?: number;
  stateful?: boolean;
  continueAfterSuccess?: boolean;
  excludeTargetOutputFromAgenticAttackGeneration?: boolean;
}

/**
 * Id prefix the BFF uses to mark a strategy ref as a published package.
 *
 * Mirrors `promptfoo-custom:` for plugins but is deliberately a different
 * string: the two travel in the same request, and a shared prefix would let a
 * plugin marker be read as a strategy ref (or the reverse) by any future code
 * that pattern-matches. `applyCustomStrategyRefs` matches the **full** string,
 * so a substring can never be rewritten.
 */
export const CUSTOM_STRATEGY_MARKER = 'pf_strategy:';

/**
 * promptfoo's own predicate for a custom strategy id.
 *
 * Mirrors `isCustomStrategy` from `dist/src/tables-*.js` rather than importing
 * it: that function is not exported from the package entrypoint, and this
 * engine is a separately deployable service that must not depend on a hashed
 * chunk path. Kept as a separate definition so the two can be asserted equal by
 * `customStrategy.test.ts`, which reads the real one out of the installed
 * promptfoo — if upstream changes the rule, that test fails here rather than at
 * run time.
 */
export function isPromptfooCustomStrategyId(id: string): boolean {
  const value = (id ?? '').trim();
  return value === 'custom' || value.startsWith('custom:');
}

/**
 * Reject a package the engine will not inline.
 *
 * Three checks, each for a different reason:
 *
 *  - **shape**: promptfoo forwards unknown `config` keys into the provider, so
 *    a typo'd field is accepted and inert. Failing loudly at load time is the
 *    only way the submitter learns.
 *  - **required field**: `validateStrategies()` throws
 *    *"Custom strategy requires strategyText in config"* otherwise — and it
 *    throws **inside the run**, after the run record is persisted. Catching it
 *    here converts a mid-run abort into a pre-flight rejection.
 *  - **id**: the id becomes promptfoo's provider id and the report's
 *    `metadata.strategyId`, so it must be a legal `custom:<variant>` token.
 */
export function assertUsableStrategy(strategy: EngineCustomStrategy): ParsedCustomStrategy {
  const handle = strategy.handle ?? '<unknown>';
  const definition = (strategy.definition ?? '').trim();
  if (!definition) {
    throw new CustomStrategyError(CUSTOM_STRATEGY_ERR_INVALID, `自定义策略 ${handle} 的内容为空，拒绝加载。`);
  }
  let parsed: unknown;
  try {
    parsed = parseYaml(definition);
  } catch {
    throw new CustomStrategyError(
      CUSTOM_STRATEGY_ERR_INVALID,
      `自定义策略 ${handle} 不是合法的 YAML，拒绝加载。请让专家修正后重新发布。`,
    );
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new CustomStrategyError(
      CUSTOM_STRATEGY_ERR_INVALID,
      `自定义策略 ${handle} 的顶层必须是键值映射（strategyText 等）。`,
    );
  }
  const record = parsed as Record<string, unknown>;
  const extra = Object.keys(record).filter((key) => !ALLOWED_STRATEGY_KEYS.has(key));
  if (extra.length > 0) {
    throw new CustomStrategyError(
      CUSTOM_STRATEGY_ERR_INVALID,
      `自定义策略 ${handle} 包含未声明字段 ${extra.join(', ')}。` +
        'promptfoo 不校验策略 config 的字段名（z.record 会放行任意键），拼错的字段会被原样转发给 provider 且不生效，' +
        `因此在加载前拦下。允许字段：${[...ALLOWED_STRATEGY_KEYS].join('、')}。`,
    );
  }
  if (typeof record.strategyText !== 'string' || !record.strategyText.trim()) {
    throw new CustomStrategyError(
      CUSTOM_STRATEGY_ERR_INVALID,
      `自定义策略 ${handle} 缺少 strategyText。` +
        'promptfoo 的 custom 策略要求 config.strategyText 为非空字符串，缺失时会在评测进行中抛错。',
    );
  }
  assertStrategyIdIsLegal(handle, strategy.strategy_id);

  const out: ParsedCustomStrategy = { strategyText: record.strategyText };
  // Optional numeric fields are range-checked here rather than forwarded blindly:
  // `maxTurns: 0` would make CustomProvider's loop terminate immediately and
  // produce a zero-turn run that still reports success.
  if (record.maxTurns !== undefined) out.maxTurns = assertPositiveInt(handle, 'maxTurns', record.maxTurns);
  if (record.maxBacktracks !== undefined) out.maxBacktracks = assertPositiveInt(handle, 'maxBacktracks', record.maxBacktracks);
  if (record.stateful !== undefined) out.stateful = assertBoolean(handle, 'stateful', record.stateful);
  if (record.continueAfterSuccess !== undefined) {
    out.continueAfterSuccess = assertBoolean(handle, 'continueAfterSuccess', record.continueAfterSuccess);
  }
  if (record.excludeTargetOutputFromAgenticAttackGeneration !== undefined) {
    out.excludeTargetOutputFromAgenticAttackGeneration = assertBoolean(
      handle,
      'excludeTargetOutputFromAgenticAttackGeneration',
      record.excludeTargetOutputFromAgenticAttackGeneration,
    );
  }
  return out;
}

function assertPositiveInt(handle: string, field: string, value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1) {
    throw new CustomStrategyError(
      CUSTOM_STRATEGY_ERR_INVALID,
      `自定义策略 ${handle} 的 ${field} 必须是 ≥1 的整数（收到 ${JSON.stringify(value)}）。`,
    );
  }
  return value;
}

function assertBoolean(handle: string, field: string, value: unknown): boolean {
  if (typeof value !== 'boolean') {
    throw new CustomStrategyError(
      CUSTOM_STRATEGY_ERR_INVALID,
      `自定义策略 ${handle} 的 ${field} 必须是布尔值（收到 ${JSON.stringify(value)}）。`,
    );
  }
  return value;
}

/**
 * `custom:<variant>` suffix rules.
 *
 * promptfoo derives two things from the variant, both of which end up in the
 * report and in a provider id: `addCustom()` splits on `:` and uses the tail as
 * `provider.config.variant`, and it appends the variant to every assertion
 * metric (`Harmful/Custom:<variant>`). A variant containing `:` would split
 * wrong; one containing path or whitespace characters would put junk into a
 * metric label and an id. The publication handle is platform-issued and already
 * constrained, so deriving the variant from it keeps this a check rather than a
 * sanitizer that could mangle two handles into one strategy.
 */
function assertStrategyIdIsLegal(handle: string, strategyId: string): void {
  const value = (strategyId ?? '').trim();
  if (!value) {
    throw new CustomStrategyError(CUSTOM_STRATEGY_ERR_BAD_ID, `自定义策略 ${handle} 缺少策略 id，拒绝加载。`);
  }
  const match = /^custom:([A-Za-z0-9._-]{1,64})$/.exec(value);
  if (!match) {
    throw new CustomStrategyError(
      CUSTOM_STRATEGY_ERR_BAD_ID,
      `自定义策略 ${handle} 的 id ${JSON.stringify(value)} 非法。` +
        'id 必须是 custom:<变体名> 形式，变体名限 [A-Za-z0-9._-] 且 1~64 字符 —— promptfoo 会把它拼进 provider id 与报告指标名。',
    );
  }
}

/** The promptfoo strategy ref for a published package, after validation. */
export interface ResolvedCustomStrategy {
  /** `custom:<variant>` — what promptfoo's `strategies[]` receives. */
  strategyId: string;
  /** `strategyText` plus any validated options. */
  config: Record<string, unknown>;
  /** The publication handle, for errors and the judge-source label. */
  handle: string;
}

/**
 * Turn published packages into promptfoo strategy refs.
 *
 * Pure and total: it performs no I/O, and the BFF has already applied admin
 * governance and canonical re-serialization, so what is validated here is
 * byte-identical to what promptfoo receives. Called from inside
 * `buildNativeRedteamConfig` — the single seam that produces the config
 * document — so there is no call site at which a `pf_strategy:` marker could
 * survive into the document (U8's wiring lesson).
 */
export function resolveCustomStrategies(strategies: EngineCustomStrategy[] | undefined): ResolvedCustomStrategy[] {
  if (!strategies || strategies.length === 0) return [];
  const out: ResolvedCustomStrategy[] = [];
  const seen = new Set<string>();
  for (const strategy of strategies) {
    const parsed = assertUsableStrategy(strategy);
    const strategyId = strategy.strategy_id.trim();
    // Two packages resolving to one promptfoo id would silently collapse: the
    // second strategy's config would never be applied and the report would show
    // one strategy for both. The BFF makes handles unique, so this is a
    // backstop rather than the primary defence.
    if (seen.has(strategyId)) {
      throw new CustomStrategyError(
        CUSTOM_STRATEGY_ERR_BAD_ID,
        `本次运行中有多个自定义策略解析为同一个 promptfoo 策略 id ${strategyId}，拒绝加载以免其中一个被静默覆盖。`,
      );
    }
    seen.add(strategyId);
    // Spread rather than cast: every field was range-checked above, and a copy
    // keeps the validated object from being mutated by a later caller.
    out.push({ strategyId, config: { ...parsed }, handle: strategy.handle });
  }
  return out;
}

/**
 * Replace `pf_strategy:<handle>` refs with the `custom:<variant>` ids promptfoo
 * understands, attaching each package's validated config.
 *
 * Returns a new array rather than mutating: the caller's array is the parsed
 * request body, and the original ids are still needed for the run record.
 *
 * Non-package refs pass through **unchanged**, including their existing config.
 * That matters because promptfoo genuinely consumes per-strategy config
 * (`RedteamStrategySchema` declares `config: z.record(...)`, and `addCrescendo`
 * spreads `strategy.config` straight into the provider), so rebuilding entries
 * unconditionally would drop configuration a caller set on a built-in strategy.
 */
export function applyCustomStrategyRefs(
  strategies: Array<{ id: string; config?: Record<string, unknown> }>,
  resolved: ResolvedCustomStrategy[],
): Array<{ id: string; config?: Record<string, unknown> }> {
  if (resolved.length === 0) return strategies;
  const byHandle = new Map<string, ResolvedCustomStrategy>();
  for (const item of resolved) byHandle.set(item.handle, item);
  return strategies.map((ref) => {
    const id = (ref?.id ?? '').trim();
    if (!id.startsWith(CUSTOM_STRATEGY_MARKER)) return ref;
    const handle = id.slice(CUSTOM_STRATEGY_MARKER.length);
    const item = byHandle.get(handle);
    if (!item) {
      throw new CustomStrategyError(
        CUSTOM_STRATEGY_ERR_BAD_ID,
        `请求引用了自定义策略 ${handle}，但本次运行没有携带对应内容，拒绝发起评测。`,
      );
    }
    return { id: item.strategyId, config: { ...item.config } };
  });
}