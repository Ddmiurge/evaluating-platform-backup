#!/usr/bin/env node
/**
 * scripts/gen_strategy_catalog.mjs — regenerate
 * internal/maclaw/testdata/promptfoo_strategy_catalog.json.
 *
 * Phase 3 开篇 · 任务 1. Run this after ANY promptfoo upgrade:
 *
 *   cd evaluating_platform && node scripts/gen_strategy_catalog.mjs
 *
 * # Generator run order (P2-2)
 *
 *   1. gen_strategy_catalog.mjs   — MEASURES which strategies need remote
 *                                   generation (promptfoo's own throw sites plus
 *                                   the deprecated-alias delegations)
 *   2. gen_remote_dep.mjs         — derives remote_gated_strategies FROM step 1
 *   3. gen_plugin_legality.mjs    — cross-references step 2 for the local-plugin set
 *
 * Steps 2 and 3 refuse to run when their input is missing or would UN-GATE
 * something, so running them out of order fails loudly instead of emitting a
 * table that quietly opens a gated capability.
 *
 * # Why the catalog is generated rather than hand-written
 *
 * The platform used to ship a hardcoded list of 30 strategy ids
 * (`internal/maclaw/promptfoo_plugin_catalog.go`, the `s(...)` constructor
 * calls). promptfoo 0.123.0 exposes **35** native strategies, so five native
 * capabilities were unreachable — the user could not select `morse`,
 * `piglatin`, `camelcase`, `emoji`, `jailbreak-templates`, `layer`,
 * `indirect-web-pwn`, `gcg`, `image`, `audio`, `video`, or `custom`.
 *
 * # Why runtime evaluation, not source reading (§3.4.1)
 *
 * U6 already learned this lesson the expensive way: grepping the built chunk
 * for string literals counts *source lines*, not *runtime values*, and a
 * spread expression silently undercounts. The strategy registry has the same
 * shape problem — `Strategies` is an array of objects built across several
 * chunks, and the ids also appear inside `ALL_STRATEGIES` (a separate
 * `Array.from(new Set(...))` union of four upstream tables plus a literal
 * `"default"`, i.e. **36 entries for 35 executable strategies** — `default`
 * is accepted by the config schema but has no action).
 *
 * So the only faithful reading is the same one the engine itself uses at
 * runtime: import the built entrypoint and read `redteam.Strategies`. That is
 * verified to yield exactly 35 ids. We deliberately do NOT resolve the
 * content-hashed `strategies-<hash>.js` chunk by filename or by shape.
 *
 * # What is measured rather than assumed
 *
 * Each strategy is classified by **calling its `action()`** against a
 * synthetic test case and observing what happens, not by reading its source:
 *
 *   - `remote`      — the action (or a helper it calls) posts to
 *                     promptfoo's remote generation service. With
 *                     `PROMPTFOO_DISABLE_REMOTE_GENERATION=1` these either
 *                     throw or are gated by U6, so they stay marked.
 *   - `no_op`       — the action returns an **empty array** for a well-formed
 *                     test case. This is the important one: `simba` is a
 *                     deprecated stub that logs "This strategy will be
 *                     skipped" and returns `[]`. Forwarding it produces a
 *                     green run with zero coverage — precisely the silent
 *                     failure shape U6 exists to eliminate, except U6's table
 *                     never covered it because `simba` needs no remote
 *                     generation. `basic` is the same shape by design (it is
 *                     promptfoo's "no strategy" control).
 *   - `requires_runtime_input` — the action throws unless the caller supplies
 *                     config the platform does not send (`retry` needs
 *                     `targetIds`, `layer` needs `steps`). Exposing these
 *                     without the config would abort a run mid-generation.
 *   - `media`       — needs an external binary (`video` shells out to
 *                     `ffmpeg`; `image`/`audio` need base64 media transport).
 *
 * Everything else is classified `local`.
 */
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '..');
const distDir = path.join(repoRoot, 'promptfoo_engine', 'node_modules', 'promptfoo', 'dist', 'src');
const indexJs = path.join(distDir, 'index.js');
const catalogGo = path.join(repoRoot, 'internal', 'maclaw', 'promptfoo_plugin_catalog.go');
const outFile = path.join(repoRoot, 'internal', 'maclaw', 'testdata', 'promptfoo_strategy_catalog.json');

if (!fs.existsSync(indexJs)) {
  console.error(
    `[gen_strategy_catalog] promptfoo is not installed at ${indexJs}.\n` +
      '  Run `cd promptfoo_engine && npm ci` first.',
  );
  process.exit(2);
}

/**
 * Import the built entrypoint and read `redteam.Strategies`.
 *
 * The entrypoint is the *only* stable public surface: the chunk that defines
 * `Strategies` is content-hashed (`strategies-4rVJCUEg.js`,
 * `strategies-DGPYW_OT.js`, `strategies-DpYzBITt.js` are all present on disk
 * for one install), so resolving it by filename would break on the next
 * re-hash. `redteam.Strategies` is also what `nativeStrategyMap.ts` asserts
 * against, so reading it here keeps the generator and the engine's guardrail
 * looking at literally the same array.
 */
async function loadStrategies() {
  const mod = await import(indexJs);
  const strategies = mod.default?.redteam?.Strategies;
  if (!Array.isArray(strategies) || strategies.length === 0) {
    throw new Error(
      'promptfoo dist/src/index.js no longer exposes default.redteam.Strategies as a non-empty array. ' +
        'The engine reads the same property (src/runner/nativeStrategyMap.ts), so upstream moved it — ' +
        're-check this script against the new promptfoo build.',
    );
  }
  return strategies;
}

/** A synthetic, well-formed test case every strategy action can accept. */
function probeTestCase() {
  return [
    {
      vars: { prompt: 'PROBE_PAYLOAD' },
      metadata: { pluginId: 'harmful' },
      assert: [{ type: 'promptfoo:redteam:harmful', metric: 'Harmful' }],
    },
  ];
}

/**
 * Config that satisfies the strategies which require caller-supplied input.
 *
 * Supplied only so the probe measures *generation*, not configuration: a
 * strategy that throws "No target IDs found in config" would be classified as
 * broken when it is merely unconfigured. The strategies that consume these are
 * separately classified `requires_runtime_input` from their source.
 */
const PROBE_CONFIG = {
  strategyText: 'PROBE_STRATEGY_TEXT',
  targetIds: ['probe-target'],
  steps: ['base64'],
  maxTurns: 1,
};

/**
 * Classify one strategy by invoking its action.
 *
 * `remote` is detected from source because a remote-gated strategy does not
 * reliably fail closed in a unit probe: several of them build their test cases
 * and only fail later, inside the provider, when the request is actually sent.
 * Probing with an empty config therefore reports `math-prompt` as producing 3
 * cases when in fact it then throws `API key is not set`, and `gcg` as
 * producing cases when it throws `requires authentication`. Only the call site
 * says which is which. The remote markers are read off the strategy's own
 * `action` source plus the helper functions it delegates to.
 *
 * `no_op` needs the same care, and getting it wrong in either direction is
 * costly. It is asserted ONLY when we can prove the emptiness, by one of:
 *   - the action body itself returns `[]` (`basic`), or
 *   - the strategy logs a deprecation/removal notice while producing nothing
 *     (`simba`: *"has been deprecated and removed. This strategy will be
 *     skipped"*).
 *
 * `retry` produces 0 cases in a probe and is **neither**: `addRetryTestCases`
 * queries promptfoo's own result store for previously *failed* rows, which is
 * empty in a fresh process. It is a working strategy that needs run history, so
 * it is classified `requires_runtime_input` — calling it a no-op would
 * blacklist a capability that works on a second run.
 */
async function classify(strategy, remoteHelperNames) {
  const body = strategy.action.toString();
  const helpers = [...body.matchAll(/\b([A-Za-z0-9_$]+)\s*\(/g)].map((m) => m[1]);
  const remote = [...remoteHelperNames].some((name) => helpers.includes(name));

  const id = strategy.id;
  // Runtime input the platform does not send. Read off the source because the
  // throw is data-dependent (retry only throws when `targetIds` is empty, which
  // our probe config satisfies).
  const needsInput = [];
  if (/invariant\([^)]*targetIds|No target IDs found in config/.test(body)) needsInput.push('targetIds');
  if (/config\?\.steps|config\.steps/.test(body)) needsInput.push('steps');

  let produced = 0;
  let threw = null;
  const logs = [];
  try {
    const out = await withCapturedLogs(logs, () =>
      strategy.action(probeTestCase(), 'prompt', PROBE_CONFIG, id, {}),
    );
    produced = Array.isArray(out) ? out.length : -1;
  } catch (err) {
    threw = String(err && err.message ? err.message : err).slice(0, 200);
  }
  const deprecated = logs.some((line) => /deprecat|has been removed|will be skipped/i.test(line));
  const selfEmpty = /return\s*\[\s*\]\s*;?\s*}/.test(body);

  return { id, produced, threw, needsInput, remote, deprecated, selfEmpty };
}

/**
 * Run `fn` with every promptfoo logger instance intercepted.
 *
 * The deprecation notice is the only reliable evidence that a zero-case
 * strategy is *intentionally* empty rather than starved of input, and it is
 * emitted through `logger.warn` rather than returned, so it has to be captured
 * from the side.
 *
 * **All** `logger-*.js` variants are patched, not just the first one found.
 * The install carries three of them (`logger-C_x9NZ6q.js`,
 * `logger-UW8CaaU_.js`, `logger-ssMoQoJ6.js`) and they are *different module
 * instances*; the strategies chunk imports `o as logger` from one specific
 * hash. Patching only the first match silently captures nothing — which is
 * exactly the failure mode that made `simba` (whose whole signal is one
 * `logger.warn` line) read as an unexplained zero. Reading the dist to find
 * which hash is current would re-introduce the content-hash fragility this
 * whole generator exists to avoid, so patching all of them is both simpler
 * and more robust. Failure to reach any logger must not fail the probe.
 */
async function withCapturedLogs(sink, fn) {
  const chunkFiles = fs.readdirSync(distDir).filter((f) => /^logger-[^/]+\.js$/.test(f));
  const loggers = [];
  for (const file of chunkFiles) {
    try {
      const mod = await import(path.join(distDir, file));
      if (mod && typeof mod.o?.warn === 'function') loggers.push(mod.o);
    } catch {
      /* a variant we cannot import is not the one we need */
    }
  }
  if (loggers.length === 0) return fn();
  const originals = loggers.map((logger) => ({ logger, warn: logger.warn, error: logger.error, debug: logger.debug }));
  const capture = (...args) => sink.push(args.map((a) => String(a)).join(' '));
  for (const { logger } of originals) {
    logger.warn = capture;
    logger.error = capture;
    logger.debug = () => {};
  }
  try {
    return await fn();
  } finally {
    for (const { logger, warn, error, debug } of originals) {
      logger.warn = warn;
      logger.error = error;
      logger.debug = debug;
    }
  }
}

/**
 * Helper functions in the strategies chunk that post to remote generation.
 *
 * This list is a *floor, not a ceiling*. A strategy can be remote-gated
 * somewhere other than its `action` — `best-of-n` is the case that matters:
 * its wrapper happily rewrites each test case into a
 * `promptfoo:redteam:best-of-n` provider and returns them (so the probe counts
 * N cases and looks healthy), while `new BestOfNProvider()` throws
 * *"Best-of-N strategy requires remote generation, which has been explicitly
 * disabled"* at provider construction time. Classifying it from the wrapper
 * alone would have marked it `local` and quietly bypassed U6's guardrail for a
 * strategy U6 already gates.
 *
 * So the probe alone is not trusted for the remote verdict either: the throw-site
 * scan (`scanRemoteRequiredStrategies`) and the delegation table
 * (`REMOTE_DELEGATIONS`) are treated as authoritative, and any of the three
 * marking a strategy remote is enough. The probe's helper scan is the weakest
 * of the three and only ever adds to the remote set, never removes from it.
 */
const REMOTE_HELPERS = [
  'postRemoteGenerationTask',
  'generateCitations',
  'addCitationTestCases',
  'generateGcgPrompts',
  'addGcgTestCases',
  'generateLikertPrompts',
  'addLikertTestCases',
  'generateMathPrompt',
  'addMathPrompt',
  'textToAudio',
  'textToVideo',
  'generateCompositePrompts',
  'addCompositeTestCases',
];

/**
 * Strategies whose implementation REFUSES to run when remote generation is
 * disabled — measured from the throw itself, not listed by hand.
 *
 * promptfoo names the component in the error it raises:
 *
 *   getRemoteGenerationExplicitlyDisabledError("jailbreak:meta strategy")
 *
 * So the LABELS can be read straight out of the installed dist, and this is the
 * authoritative source: it is the same code path that fails a run, and it
 * cannot drift the way a hand-kept list can (U6's hand-kept "29" is exactly how
 * three unrunnable plugin ids reached production).
 *
 * # Why a label table is needed on top of the scan
 *
 * The label is prose, not an id: `"Composite jailbreak strategy"` is
 * `jailbreak:composite` and `"Best-of-N strategy"` is `best-of-n`. Truncating
 * " strategy" would produce two ids that do not exist. The mapping below is
 * therefore reviewed by hand — but the SCAN drives it: any label found in the
 * dist that is missing from this table throws, so an upstream upgrade that
 * gates a new strategy cannot be silently ignored. That is the difference
 * between this and a hand-written list.
 *
 * # Why the action-level probe misses these
 *
 * The probe calls `action()`. These strategies throw in their **provider
 * constructor**, which the probe never reaches — the same blind spot
 * REMOTE_HELPERS documents for `best-of-n`. `jailbreak:meta` is the case that
 * matters here: its wrapper returns a perfectly healthy-looking test case and
 * the run then dies at scan time with *"jailbreak:meta strategy requires remote
 * generation"* (observed on ai02, U7 item8).
 */
const REMOTE_THROW_LABELS = {
  'Best-of-N strategy': 'best-of-n',
  'Citation strategy': 'citation',
  'Composite jailbreak strategy': 'jailbreak:composite',
  'Likert jailbreak strategy': 'jailbreak:likert',
  'GCG strategy': 'gcg',
  'Audio strategy': 'audio',
  'jailbreak:meta strategy': 'jailbreak:meta',
  // Not a strategy: promptfoo's RAG-poisoning provider guards separately. Listed
  // rather than omitted so a scanner hit here is a decision, not an oversight.
  'RAG poisoning': null,
};

function scanRemoteRequiredStrategies(distDir) {
  const pattern = /getRemoteGenerationExplicitlyDisabledError\("([^"]+)"\)|getRemoteGenerationDisabledError\("([^"]+)"\)/g;
  const labels = new Set();
  for (const file of fs.readdirSync(distDir)) {
    if (!/\.(js|cjs)$/.test(file)) continue;
    const src = fs.readFileSync(path.join(distDir, file), 'utf8');
    for (const match of src.matchAll(pattern)) {
      labels.add(match[1] ?? match[2]);
    }
  }
  if (labels.size === 0) {
    // Not "be lenient": an empty set means the pattern stopped matching, which
    // would silently un-gate every remote-required strategy at once.
    throw new Error(
      'found no remote-generation-disabled throw sites in promptfoo dist. The upstream error ' +
        'helper was renamed or the dist layout changed; re-check this scanner against the new ' +
        'build. Refusing to emit a catalogue in which nothing is remote-gated.',
    );
  }
  const unknown = [...labels].filter((label) => !(label in REMOTE_THROW_LABELS));
  if (unknown.length > 0) {
    throw new Error(
      `promptfoo now gates these components on remote generation but this table does not know ` +
        `their strategy ids: ${unknown.join(', ')}. Add them to REMOTE_THROW_LABELS (or map to ` +
        'null when the component is not a strategy). Refusing to emit a catalogue that omits a ' +
        'remote-gated strategy.',
    );
  }
  const ids = new Set();
  for (const label of labels) {
    const id = REMOTE_THROW_LABELS[label];
    if (id) ids.add(id);
  }
  return { labels, ids };
}

/**
 * Deprecated wrappers that delegate to a gated implementation *without naming
 * it as a strategy id*, so the throw-site scan cannot see them.
 *
 * `jailbreak` is the case: its action calls
 * `addIterativeJailbreaks(testCases, injectVar, "iterative:meta", config)` —
 * a provider KIND, not a strategy id — and promptfoo's own warning for it says
 * *'Strategy "jailbreak" is deprecated. Use "jailbreak:meta" instead.'* The
 * scan finds `jailbreak:meta`; nothing in the dist links the two by name.
 *
 * Each entry names the literal that proves the delegation, and the generator
 * verifies it is still present in that strategy's action source. A promptfoo
 * upgrade that removes the delegation therefore FAILS the generator instead of
 * silently dropping the gate again — which is exactly how this defect got in.
 */
const REMOTE_DELEGATIONS = [
  {
    from: 'jailbreak',
    literal: '"iterative:meta"',
    to: 'jailbreak:meta',
    why: 'promptfoo deprecates `jailbreak` in favour of `jailbreak:meta`; its action delegates to the iterative:meta provider, which refuses to construct under fail-closed',
  },
];

/**
 * Platform-invented strategy dimensions read off the Go catalog.
 *
 * These are NOT promptfoo ids. They describe *how the platform wraps an attack
 * prompt* and are produced by the engine's own prompt synthesis (Plan A), so
 * they must survive the switch to dynamic discovery — deleting them would
 * drop real attack-technique coverage that promptfoo has no counterpart for.
 *
 * Note the remote-gate list is deliberately NOT read here. The gate is measured
 * from promptfoo (see `scanRemoteRequiredStrategies`) and `gen_remote_dep.mjs`
 * derives its table from THIS file's verdict; reading that table back here too
 * would make the two generators circular.
 */
function readPlatformStrategyIds() {
  const go = fs.readFileSync(catalogGo, 'utf8');
  const start = go.indexOf('var promptfooStrategyCatalog');
  if (start < 0) throw new Error('could not locate var promptfooStrategyCatalog in the Go catalog');
  const end = go.indexOf('\n}\n', start);
  return [...go.slice(start, end).matchAll(/\bs\("([^"]+)"/g)].map((m) => m[1]);
}

/**
 * Platform strategy id -> promptfoo native id, mirroring nativeStrategyMap.ts.
 *
 * Mirrored by hand on purpose, and asserted equal to the engine's table by
 * `TestPromptfooStrategyNativeMappingMatchesEngineTable`. It cannot be derived:
 * the platform's vocabulary and promptfoo's overlap only partially, and *which*
 * platform ids exist is a platform decision (see `platform_invented_strategy_ids`).
 */
const NATIVE_MAP = {
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
  // `retry` and `mathPrompt` are NOT mapped. Both fail when the platform
  // forwards them, because the platform sends `{ id }` with **no** strategy
  // config (`resolveNativeStrategies` builds the id only):
  //
  //   retry       -> invariant "No target IDs found in config" — thrown after
  //                  the run record is persisted.
  //   math-prompt -> with remote generation fail-closed it falls through to a
  //                  local path calling an OpenAI provider directly, and dies
  //                  with "API key is not set".
  //
  // Both were mapped in the hand-written table and neither was gated by U6, so
  // both were reachable and both would have failed mid-run. Verified against
  // the installed promptfoo, not inferred.
  mischievousUser: 'mischievous-user',
  authoritativeMarkupInjection: 'authoritative-markup-injection',
  // Phase 3 · identity mappings for strategies discovery surfaced. promptfoo
  // already uses these names, so they need no translation — they were
  // unreachable only because the hand-written list had drifted.
  //
  // `simba` is deliberately NOT mapped. promptfoo 0.123.0 keeps it as a
  // deprecated stub (`addSimbaTestCases` logs "has been deprecated and removed"
  // and returns `[]`), so mapping it would forward a strategy that provably
  // generates nothing while the report claims it ran. It is recorded below as
  // `no_op` so the reason is auditable rather than a silent absence.
  morse: 'morse',
  piglatin: 'piglatin',
  camelcase: 'camelcase',
  emoji: 'emoji',
  'jailbreak-templates': 'jailbreak-templates',
  layer: 'layer',
  'indirect-web-pwn': 'indirect-web-pwn',
  // Deprecated upstream aliases that still generate cases. Kept mapped because
  // they work; promptfoo logs a deprecation warning naming the replacement,
  // which is advice rather than a refusal.
  'prompt-injection': 'prompt-injection',
  jailbreak: 'jailbreak',
  custom: 'custom',
};

const strategies = await loadStrategies();
const platformIds = readPlatformStrategyIds();
const strategyIds = new Set(strategies.map((s) => s.id));

// Measured remote gate. The scan names components in prose, so every id it
// maps to must exist in the registry — a rename upstream would otherwise leave
// the gate pointing at nothing while still looking healthy.
const { ids: measuredRemoteIds, labels: remoteLabels } = scanRemoteRequiredStrategies(distDir);
for (const id of measuredRemoteIds) {
  if (!strategyIds.has(id)) {
    throw new Error(
      `promptfoo gates "${id}" on remote generation but no such strategy is in redteam.Strategies. ` +
        'The id was renamed upstream; fix REMOTE_THROW_LABELS. Refusing to emit a gate that matches nothing.',
    );
  }
}

// Deprecated wrappers delegating to a gated implementation. The delegation is
// asserted present in the wrapper's own action source, so an upstream change
// fails the generator instead of silently dropping the gate (the defect this
// table exists to fix).
const delegatedRemote = new Map();
for (const delegation of REMOTE_DELEGATIONS) {
  const strategy = strategies.find((s) => s.id === delegation.from);
  if (!strategy) {
    throw new Error(
      `REMOTE_DELEGATIONS names "${delegation.from}", which is not in promptfoo's strategy registry. ` +
        'Re-check the delegation table against the new build.',
    );
  }
  if (!measuredRemoteIds.has(delegation.to)) {
    throw new Error(
      `REMOTE_DELEGATIONS says "${delegation.from}" delegates to "${delegation.to}", but the measured ` +
        'scan does not gate that target. Fix the table (or the target was un-gated upstream).',
    );
  }
  if (!strategy.action.toString().includes(delegation.literal)) {
    throw new Error(
      `the delegation evidence for "${delegation.from}" is gone: its action no longer contains ` +
        `${delegation.literal}. Either the wrapper no longer delegates to the gated implementation ` +
        '(in which case remove this entry) or the code was restructured. Refusing to emit a gate ' +
        'that may no longer apply.',
    );
  }
  delegatedRemote.set(delegation.from, `action 仍以 ${delegation.literal} 委托给 ${delegation.to}`);
}

const rows = [];
for (const strategy of strategies) {
  const c = await classify(strategy, REMOTE_HELPERS);
  // The remote verdict has two measured sources plus the probe: the throw-site
  // scan (provider-level guards), the delegation table (deprecated aliases),
  // and the probe's helper scan. Any one of them is sufficient.
  const measuredRemote = measuredRemoteIds.has(c.id);
  const delegation = delegatedRemote.get(c.id);
  const remoteRequired = c.remote || measuredRemote || Boolean(delegation);
  // Availability, in precedence order. `remote` outranks the rest: a strategy
  // that cannot generate at all in this deployment must never be offered as
  // usable, whatever else it is. `no_op` is only ever asserted on proven
  // evidence (see `classify`), so it cannot swallow a working strategy.
  let availability = 'local';
  let reason = '';
  if (remoteRequired) {
    availability = 'remote';
    reason = delegation
      ? `需远程生成 · 当前环境已屏蔽（${delegation}，该 provider 在构造时拒绝）`
      : measuredRemote
        ? '需远程生成 · 当前环境已屏蔽（promptfoo 自报的「显式禁用远程生成」抛错点：provider 构造时才拒绝，探针无法从包装层看出）'
        : '需远程生成 · 当前环境已屏蔽';
  } else if (c.needsInput.length > 0) {
    availability = 'requires_runtime_input';
    reason = `需要调用方提供 ${c.needsInput.join('/')} 配置；平台当前不下发（retry 还需 promptfoo 侧存在历史失败用例）`;
  } else if (c.produced === 0 && (c.selfEmpty || c.deprecated)) {
    availability = 'no_op';
    reason = c.deprecated
      ? 'promptfoo 该策略在本版本已废弃并移除，运行会成功但实际生成 0 条用例（覆盖为 0）'
      : 'promptfoo 该策略是空实现（用于「不施加策略」的对照），实际生成 0 条用例';
  } else if (['audio', 'video', 'image'].includes(c.id)) {
    availability = 'media';
    reason = '需要媒体编码通道（外部二进制/多模态传输），当前部署未就绪';
  } else if (c.produced === 0) {
    // Zero cases with no proof of intent and no missing input. Not claimed as a
    // no-op: recorded as `unknown` so it is visible and reviewable rather than
    // silently offered as working.
    availability = 'unknown';
    reason = '探针实测生成 0 条用例，且无法判定为有意空实现；需人工复核后再决定是否开放';
  } else if (c.threw) {
    availability = 'unsupported';
    reason = `在本版本中执行失败：${c.threw}`;
  }
  rows.push({
    id: c.id,
    requires_goal_extraction: Boolean(strategy.requiresGoalExtraction),
    availability,
    reason,
    probe_generated_cases: c.produced,
    probe_error: c.threw,
    probe_logged_deprecation: c.deprecated,
    needs_runtime_input: c.needsInput,
    remote_required: remoteRequired,
    remote_evidence: delegation ? `delegation:${delegation}` : measuredRemote ? 'throw-site' : c.remote ? 'probe-helper' : '',
  });
}

// Native strategies the platform exposes under its own id (identity mapping),
// so `morse` & co. become selectable without inventing a new vocabulary.
const nativeExposure = {};
for (const row of rows) {
  if (row.availability !== 'local') continue;
  nativeExposure[row.id] = row.id;
}

const payload = {
  _comment:
    'GENERATED by scripts/gen_strategy_catalog.mjs — do not hand-edit. Single source of truth for the Phase 3 strategy catalog. ' +
    'Regenerate after any promptfoo upgrade.',
  promptfoo_version: JSON.parse(fs.readFileSync(path.join(repoRoot, 'promptfoo_engine', 'package.json'), 'utf8')).dependencies
    .promptfoo,
  discovery_method:
    "runtime import of promptfoo dist/src/index.js -> default.redteam.Strategies (the same array src/runner/nativeStrategyMap.ts asserts against); each entry classified by invoking its action() against a synthetic test case",
  remote_gate_source:
    'remote_required is measured, not listed: scanRemoteRequiredStrategies() reads promptfoo\'s own ' +
    '"remote generation explicitly disabled" throw sites out of the installed dist (the same code path ' +
    'that fails a run), and REMOTE_DELEGATIONS covers deprecated wrappers that delegate to a gated ' +
    'implementation without naming it as a strategy id. An unlabelled throw site or a missing ' +
    'delegation literal aborts the generator rather than emitting an under-gated catalogue. ' +
    'gen_remote_dep.mjs derives its remote_gated_strategies table from this field.',
  remote_labels_seen: [...remoteLabels].sort(),
  totals: {
    native_strategies: rows.length,
    local: rows.filter((r) => r.availability === 'local').length,
    remote: rows.filter((r) => r.availability === 'remote').length,
    remote_required: rows.filter((r) => r.remote_required).length,
    no_op: rows.filter((r) => r.availability === 'no_op').length,
    requires_runtime_input: rows.filter((r) => r.availability === 'requires_runtime_input').length,
    media: rows.filter((r) => r.availability === 'media').length,
    unknown: rows.filter((r) => r.availability === 'unknown').length,
    unsupported: rows.filter((r) => r.availability === 'unsupported').length,
    platform_invented: platformIds.length,
  },
  native_strategies: rows,
  platform_invented_strategy_ids: platformIds,
  platform_to_native_map: NATIVE_MAP,
  native_exposure: nativeExposure,
};

fs.mkdirSync(path.dirname(outFile), { recursive: true });
fs.writeFileSync(outFile, `${JSON.stringify(payload, null, 2)}\n`);

console.log(`[gen_strategy_catalog] promptfoo      ${payload.promptfoo_version}`);
console.log(`[gen_strategy_catalog] discovery     runtime import of default.redteam.Strategies`);
console.log(`[gen_strategy_catalog] totals        ${JSON.stringify(payload.totals)}`);
console.log(`[gen_strategy_catalog] writable      ${rows.filter((r) => r.availability === 'local').map((r) => r.id).join(', ')}`);
for (const r of rows.filter((x) => x.availability !== 'local')) {
  console.log(`[gen_strategy_catalog]   gated        ${r.id.padEnd(24)} ${r.availability.padEnd(22)} ${r.reason}`);
}
console.log(`[gen_strategy_catalog] wrote         ${path.relative(repoRoot, outFile)}`);