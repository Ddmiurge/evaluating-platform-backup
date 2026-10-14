#!/usr/bin/env node
/**
 * scripts/gen_remote_dep.mjs — regenerate internal/maclaw/testdata/promptfoo_remote_dep.json.
 *
 * U6 · 远程依赖能力护栏. Run this after ANY promptfoo upgrade:
 *
 *   cd evaluating_platform && node scripts/gen_strategy_catalog.mjs   # FIRST
 *   cd evaluating_platform && node scripts/gen_remote_dep.mjs
 *
 * P2-2: the strategy gate (`remote_gated_strategies`) is DERIVED from the
 * strategy catalogue's measured verdict, so step 1 must run first. It used to be
 * a hand-written four-entry map, and that is how `iterative:meta` and its
 * deprecated alias `jailbreak` stayed un-gated while both failed mid-run.
 *
 * Why a generator instead of a hand-maintained list:
 *   1. `REMOTE_ONLY_PLUGIN_IDS` is built from 26 literals + 8 `...spread`
 *      expressions that reference other tables. Grepping the source counts
 *      source lines, not runtime values, and undercounts by 67 entries. The
 *      only faithful reading is a runtime `import()` of the built chunk.
 *   2. The built chunk filename is content-hashed (`tables-BboCJNxW.js` today,
 *      three variants on disk). We therefore resolve the tables by SHAPE
 *      (a 93-string array; a 22-key all-string record; a 4-string array
 *      containing `bias:age`) rather than by filename, so a re-hash cannot
 *      silently break the guardrail.
 *   3. The output is committed and asserted by CI, so a promptfoo upgrade that
 *      changes the remote-dependency set fails the build until someone runs
 *      this script and reviews the diff.
 *
 * The generated file is the single source of truth consumed by
 * internal/maclaw/promptfoo_remote_dep.go (via go:embed).
 */
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '..');
const tablesDir = path.join(repoRoot, 'promptfoo_engine', 'node_modules', 'promptfoo', 'dist', 'src');
const catalogGo = path.join(repoRoot, 'internal', 'maclaw', 'promptfoo_plugin_catalog.go');
const outFile = path.join(repoRoot, 'internal', 'maclaw', 'testdata', 'promptfoo_remote_dep.json');

/**
 * The three upstream constants this guardrail mirrors. They are resolved by the
 * SEMANTIC NAMES promptfoo itself imports them under, not by the minified export
 * names (`zt`/`Ut`/`St` today) and not by the content-hashed chunk filename
 * (`tables-BboCJNxW.js`, three variants on disk).
 *
 * Resolution path: `dist/src/index.js` re-exports these tables under their real
 * names (`import { zt as REMOTE_ONLY_PLUGIN_IDS, ... } from "./tables-XXXX.js"`).
 * We parse that import statement to learn which export of which chunk is which
 * constant, then import the chunk and read that export. This is the most
 * faithful method available: it follows promptfoo's own wiring rather than
 * guessing from the shape of unrelated tables in the same chunk (the chunk also
 * exports a plugin→severity map and two plugin→label maps that a shape heuristic
 * confuses with UNALIGNED_PROVIDER_HARM_PLUGINS).
 */
const UPSTREAM_CONSTANTS = [
  'REMOTE_ONLY_PLUGIN_IDS',
  'UNALIGNED_PROVIDER_HARM_PLUGINS',
  'BIAS_PLUGINS',
];

const EXPECTED_RUNTIME_TOTALS = { REMOTE_ONLY_PLUGIN_IDS: 93, UNALIGNED_PROVIDER_HARM_PLUGINS: 22, BIAS_PLUGINS: 4 };

if (!fs.existsSync(tablesDir)) {
  console.error(
    `[gen_remote_dep] promptfoo is not installed at ${tablesDir}.\n` +
      '  Run `cd promptfoo_engine && npm ci` first.',
  );
  process.exit(2);
}

/**
 * Parse `dist/src/index.js` for the aliased import of the tables chunk and
 * return a map of upstream constant name -> { chunk, export }.
 */
function resolveTableBindings() {
  const indexJs = path.join(repoRoot, 'promptfoo_engine', 'node_modules', 'promptfoo', 'dist', 'src', 'index.js');
  const src = fs.readFileSync(indexJs, 'utf8');
  const stmt = src
    .split('\n')
    .find((line) => /import\s*\{[^}]*\}\s*from\s*"\.\/tables-[^"]+"/.test(line));
  if (!stmt) throw new Error(`could not find the tables import statement in ${indexJs}`);

  const match = stmt.match(/import\s*\{([^}]*)\}\s*from\s*"\.\/(tables-[^"]+)"/s);
  if (!match) throw new Error('malformed tables import statement in promptfoo dist/src/index.js');

  const [, aliasList, chunk] = match;
  const bindings = new Map();
  for (const spec of aliasList.split(',').map((s) => s.trim()).filter(Boolean)) {
    const parts = spec.split(/\s+as\s+/);
    const exported = parts[0].trim();
    const local = (parts[1] ?? parts[0]).trim();
    if (UPSTREAM_CONSTANTS.includes(local)) bindings.set(local, { chunk, export: exported });
  }

  const missing = UPSTREAM_CONSTANTS.filter((name) => !bindings.has(name));
  if (missing.length > 0) {
    throw new Error(
      `promptfoo dist/src/index.js no longer imports ${missing.join(', ')} from a tables chunk. ` +
        'The upstream constant was renamed or removed; re-check this script against the new promptfoo build.',
    );
  }
  return bindings;
}

/** Import the chunk and read each upstream constant by its resolved export name. */
async function resolveRemoteTables() {
  const bindings = resolveTableBindings();
  const chunks = new Set([...bindings.values()].map((b) => b.chunk));
  const tables = new Map();

  for (const chunk of chunks) {
    const mod = await import(path.join(tablesDir, chunk));
    for (const [name, binding] of bindings) {
      if (binding.chunk !== chunk) continue;
      const value = mod[binding.export];
      if (value === undefined) {
        throw new Error(`${chunk} does not export ${binding.export} (resolved for ${name})`);
      }
      tables.set(name, value);
    }
  }
  if (tables.size !== UPSTREAM_CONSTANTS.length) {
    throw new Error(`resolved only ${[...tables.keys()].join(', ')} of the three upstream constants`);
  }

  const actual = {
    REMOTE_ONLY_PLUGIN_IDS: tables.get('REMOTE_ONLY_PLUGIN_IDS').length,
    UNALIGNED_PROVIDER_HARM_PLUGINS: Object.keys(tables.get('UNALIGNED_PROVIDER_HARM_PLUGINS')).length,
    BIAS_PLUGINS: tables.get('BIAS_PLUGINS').length,
  };
  const drifted = Object.entries(EXPECTED_RUNTIME_TOTALS).filter(([k, v]) => actual[k] !== v);
  if (drifted.length > 0) {
    console.warn(
      `[gen_remote_dep] WARNING: promptfoo remote-dependency tables changed since this script was written ` +
        `(${drifted.map(([k, v]) => `${k}: expected ${v}, got ${actual[k]}`).join('; ')}). ` +
        'The regenerated file will differ — review that diff before committing.',
    );
  }

  return {
    chunk: [...chunks][0],
    remoteOnly: tables.get('REMOTE_ONLY_PLUGIN_IDS'),
    unaligned: Object.keys(tables.get('UNALIGNED_PROVIDER_HARM_PLUGINS')),
    bias: tables.get('BIAS_PLUGINS'),
  };
}

/**
 * Plugin ids declared with the compact `p("id", ...)` constructor.
 *
 * Comment-only lines are dropped first. The catalog documents its own
 * withdrawals inline and those comments quote the withdrawn constructor calls
 * verbatim (`// P2-1移出 \`p("prompt-injection", …)\``); a bare `p("` search reads
 * that prose as a declaration, which silently inflated this count by 4 and made
 * the table describe a catalog that does not exist. Indentation is not anchored
 * on, so a reformat cannot break the parse.
 */
function readCatalogPluginIds() {
  const go = fs.readFileSync(catalogGo, 'utf8');
  const pluginStart = go.indexOf('var promptfooPluginCatalog');
  const strategyStart = go.indexOf('var promptfooStrategyCatalog');
  if (pluginStart < 0 || strategyStart < 0) throw new Error('could not locate the catalog var blocks');
  const body = go
    .slice(pluginStart, strategyStart)
    .split('\n')
    .filter((line) => !line.trim().startsWith('//'))
    .join('\n');
  const ids = [...body.matchAll(/\bp\("([^"]+)"/g)].map((m) => m[1]);
  if (ids.length === 0) throw new Error('parsed zero plugin ids from the catalog');
  const dupes = ids.filter((id, i) => ids.indexOf(id) !== i);
  if (dupes.length > 0) {
    throw new Error(`catalog declares duplicate plugin ids: ${[...new Set(dupes)].join(', ')}`);
  }
  return ids;
}

/** Sorted platform strategy ids the Go catalogue offers (`s("…")` literals). */
function readCatalogStrategyIds() {
  const go = fs.readFileSync(catalogGo, 'utf8');
  const start = go.indexOf('var promptfooStrategyCatalog');
  if (start < 0) throw new Error('could not locate var promptfooStrategyCatalog in the Go catalog');
  const end = go.indexOf('\n}\n', start);
  const ids = [...go.slice(start, end).matchAll(/\bs\("([^"]+)"/g)].map((m) => m[1]);
  if (ids.length === 0) throw new Error('parsed zero strategy ids from the catalog');
  return ids;
}

/**
 * Platform id -> promptfoo native id for strategies whose translation is NOT in
 * the strategy catalogue's `platform_to_native_map`.
 *
 * Why these four are separate: `nativeStrategyMap.ts` deliberately omits them
 * (they are remote-gated, so their translation has always lived with the gate),
 * and the platform's id vocabulary for them predates the native one. The
 * translation is curated vocabulary; the VERDICT is not — each native id here
 * must come back from the strategy catalogue as `remote_required`, and the
 * generator throws otherwise. A typo therefore fails the build instead of
 * silently gating nothing.
 */
const GATED_NATIVE_IDS = {
  bestOfN: 'best-of-n',
  likert: 'jailbreak:likert',
  citation: 'citation',
  singleTurnComposite: 'jailbreak:composite',
};

/**
 * Platform strategy id -> promptfoo native id for every strategy this
 * deployment cannot run, DERIVED from the measured strategy catalogue.
 *
 * This used to be a hand-written four-entry map, and that hand-writing is what
 * let two un-gated strategies through: `jailbreak` (promptfoo's deprecated
 * alias, whose action delegates to the remote-only iterative:meta provider) and
 * `iterative:meta` (whose provider refuses to construct under fail-closed).
 * Both were offered in the selector and both failed mid-run instead of at
 * submit — the same "hand-kept list drifts" failure U6's 29 had.
 *
 * The verdict now comes from `promptfoo_strategy_catalog.json`, where it is
 * MEASURED: `gen_strategy_catalog.mjs` reads promptfoo's own
 * "remote generation explicitly disabled" throw sites out of the installed dist
 * and follows the deprecated-alias delegations. Run that generator FIRST.
 */
function deriveRemoteGatedStrategies() {
  const strategyCatalogPath = path.join(repoRoot, 'internal', 'maclaw', 'testdata', 'promptfoo_strategy_catalog.json');
  if (!fs.existsSync(strategyCatalogPath)) {
    throw new Error(
      `${strategyCatalogPath} is missing. Run \`node scripts/gen_strategy_catalog.mjs\` first — the ` +
        'remote-gate table is derived from its measured verdict, and a missing input would emit an ' +
        'empty gate.',
    );
  }
  const catalog = JSON.parse(fs.readFileSync(strategyCatalogPath, 'utf8'));
  const nativeRows = new Map((catalog.native_strategies ?? []).map((row) => [row.id, row]));
  const remoteRequired = new Set([...nativeRows].filter(([, row]) => row.remote_required).map(([id]) => id));
  if (remoteRequired.size === 0) {
    throw new Error('the strategy catalogue reports zero remote-required strategies; that is the shape of a failed measurement');
  }
  const platformToNative = catalog.platform_to_native_map ?? {};

  for (const [platformId, nativeId] of Object.entries(GATED_NATIVE_IDS)) {
    if (!nativeRows.has(nativeId)) {
      throw new Error(`GATED_NATIVE_IDS maps ${platformId} -> ${nativeId}, which is not in promptfoo's strategy registry`);
    }
    if (!remoteRequired.has(nativeId)) {
      throw new Error(
        `GATED_NATIVE_IDS maps ${platformId} -> ${nativeId}, but the measurement does not gate it. ` +
          'Either the translation is wrong or promptfoo no longer needs remote generation here; ' +
          'both need a human decision, so refusing to emit the table.',
      );
    }
  }

  // Every platform id this deployment can name:
  //   - the Go `s("…")` dimensions, and the native idents exposed under their
  //     own name (both selectable, so both need a verdict);
  //   - plus every key of the platform→native map, which includes ids that are
  //     NOT in the selector any more. `jailbreak` is the case: it is the
  //     deprecated alias of the gated `iterative:meta`, so it is no longer
  //     offered — but a stored request or a direct API call can still name it,
  //     and if it is left un-gated it reaches promptfoo and fails mid-run. The
  //     verdict belongs at submit, so it is gated even though it is not offered.
  const offered = new Set([
    ...readCatalogStrategyIds(),
    ...Object.keys(catalog.native_exposure ?? {}),
    ...Object.keys(platformToNative),
  ]);

  const gated = {};
  for (const platformId of [...offered].sort()) {
    const nativeId = GATED_NATIVE_IDS[platformId] ?? platformToNative[platformId] ?? platformId;
    if (remoteRequired.has(nativeId)) gated[platformId] = nativeId;
  }

  // Fail closed on an un-gating. A strategy disappearing from this table means
  // promptfoo changed its mind — which may be correct after an upgrade, but it
  // must be a reviewed decision, not a silent side effect of regenerating.
  const previousPath = path.join(repoRoot, 'internal', 'maclaw', 'testdata', 'promptfoo_remote_dep.json');
  if (fs.existsSync(previousPath)) {
    const previous = JSON.parse(fs.readFileSync(previousPath, 'utf8')).remote_gated_strategies ?? {};
    const dropped = Object.keys(previous).filter((id) => !(id in gated));
    if (dropped.length > 0) {
      throw new Error(
        `regenerating would UN-GATE these strategies: ${dropped.join(', ')}. Either the measurement ` +
          'lost them (fix the strategy generator) or promptfoo no longer requires remote generation ' +
          'for them — in which case confirm it with a real run before accepting the change.',
      );
    }
  }
  return gated;
}

const { chunk, remoteOnly, bias, unaligned } = await resolveRemoteTables();
const catalogPluginIds = readCatalogPluginIds();
const REMOTE_GATED_STRATEGIES = deriveRemoteGatedStrategies();

const remoteSet = new Set(remoteOnly);
const unalignedSet = new Set(unaligned);
const biasSet = new Set(bias);

// Source attribution is exclusive and ordered so the mapping table is a
// partition: an id appears under exactly one source. Priority matters —
// REMOTE_ONLY is the strongest signal (the plugin has no local implementation
// at all), then provider-harm (needs the aligned provider), then bias.
const catalogMarkedPlugins = {};
for (const id of catalogPluginIds) {
  if (remoteSet.has(id)) catalogMarkedPlugins[id] = 'remote_only';
  else if (unalignedSet.has(id)) catalogMarkedPlugins[id] = 'unaligned_provider_harm';
  else if (biasSet.has(id)) catalogMarkedPlugins[id] = 'bias';
}

const marked = Object.keys(catalogMarkedPlugins).length;

const payload = {
  _comment:
    'GENERATED by scripts/gen_remote_dep.mjs — do not hand-edit. Single source of truth for the U6 remote-generation guardrail. Regenerate after any promptfoo upgrade.',
  promptfoo_version: JSON.parse(fs.readFileSync(path.join(repoRoot, 'promptfoo_engine', 'package.json'), 'utf8')).dependencies.promptfoo,
  promptfoo_tables_chunk: chunk,
  promptfoo_runtime_totals: {
    remote_only_plugin_ids: remoteOnly.length,
    unaligned_provider_harm_plugins: unaligned.length,
    bias_plugins: bias.length,
  },
  platform_catalog_totals: {
    plugins: catalogPluginIds.length,
    marked,
    local_only: catalogPluginIds.length - marked,
  },
  remote_only_plugin_ids: [...remoteOnly].sort(),
  unaligned_provider_harm_plugin_ids: [...unaligned].sort(),
  bias_plugin_ids: [...bias].sort(),
  catalog_marked_plugins: catalogMarkedPlugins,
  remote_gated_strategies: REMOTE_GATED_STRATEGIES,
};

fs.mkdirSync(path.dirname(outFile), { recursive: true });
fs.writeFileSync(outFile, `${JSON.stringify(payload, null, 2)}\n`);

const bySource = catalogMarkedPlugins && Object.values(catalogMarkedPlugins).reduce((acc, k) => {
  acc[k] = (acc[k] ?? 0) + 1;
  return acc;
}, {});
console.log(`[gen_remote_dep] chunk            ${chunk}`);
console.log(`[gen_remote_dep] promptfoo        ${payload.promptfoo_version}`);
console.log(`[gen_remote_dep] runtime totals   ${JSON.stringify(payload.promptfoo_runtime_totals)}`);
console.log(`[gen_remote_dep] catalog totals   ${JSON.stringify(payload.platform_catalog_totals)}`);
console.log(`[gen_remote_dep] hits by source   ${JSON.stringify(bySource)}`);
console.log(`[gen_remote_dep] wrote            ${path.relative(repoRoot, outFile)}`);
