#!/usr/bin/env node
/**
 * scripts/gen_plugin_legality.mjs — regenerate
 * internal/maclaw/testdata/promptfoo_plugin_legality.json.
 *
 * P2-1 · plugin-id legality + fanout guardrail. Run this after ANY promptfoo
 * upgrade:
 *
 *   cd evaluating_platform && node scripts/gen_strategy_catalog.mjs
 *   cd evaluating_platform && node scripts/gen_remote_dep.mjs
 *   cd evaluating_platform && node scripts/gen_plugin_legality.mjs
 *
 * # The defect this file exists to prevent
 *
 * U6's gen_remote_dep.mjs answers "does this plugin need remote generation".
 * It never answered "does this plugin EXIST". Its 29 `local_only` ids were
 * read as "runnable here", and three of them —
 * `prompt-injection`, `jailbreak`, `security-exploit` — are not promptfoo
 * plugin ids at all. A user could select one, the BFF would accept it, and
 * promptfoo's zod would reject it mid-run:
 *
 *   Invalid plugin id "prompt-injection". Custom plugins must start with
 *   file:// or use a built-in plugin.
 *
 * "Unmarked" is not "legal". This generator closes that gap.
 *
 * # Why safeParse is the ONLY definition of "legal"
 *
 * Three cheaper methods were tried and all three are wrong:
 *
 *   1. Reading the `ALL_PLUGINS` literal. It is NOT the zod acceptance set.
 *      The latter is `[...COLLECTIONS, ...ALL_PLUGINS, ...ALIASED_PLUGIN_MAPPINGS]`
 *      — omitting COLLECTIONS would judge the legal collection ids (`pii`,
 *      `harmful`, `bias` — the ones that EXPAND) as illegal.
 *   2. Reading the `redteam.Plugins` instance table (157 entries). It is a
 *      different set: `jailbreak` is absent (it is a strategy), while `pii` /
 *      `harmful` appear only in their expanded forms.
 *   3. Hand-maintaining the list. That is precisely how U6's 29 drifted into
 *      containing 3 ids that cannot run.
 *
 * `RedteamConfigSchema.safeParse()` is the definition promptfoo itself
 * enforces, so it is the only one that cannot disagree with the engine.
 *
 * # Why the schema is resolved by SEMANTIC name
 *
 * The built chunk filename is content-hashed (`tables-BboCJNxW.js` today,
 * three more variants on disk) and the export is minified (`Y` today). This
 * script follows the same discipline as gen_remote_dep.mjs: parse
 * `dist/src/index.js` for the `from "./tables-XXXX.js"` import statement and
 * read which chunk export is aliased to `RedteamConfigSchema`. A re-hash or a
 * re-minify therefore cannot silently change the answer.
 *
 * Resolution failure is FAIL-CLOSED: the script throws and writes nothing. A
 * fallback that "guessed" a schema would produce a legality table that looks
 * authoritative and is fiction.
 *
 * # Two products, one run
 *
 *   (a) legality  — which catalog ids promptfoo accepts (P0-2, layer L1)
 *   (b) fanout    — how many sub-plugins each accepted id expands into
 *                   (P0-1's equal-split weight source; `numTests` is applied
 *                   per expanded sub-plugin, so `harmful` costs 26x `policy`)
 *
 * Regenerate after any promptfoo upgrade; the committed file is asserted by
 * CI so a drift fails the build until someone reviews the diff.
 */
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '..');
const distSrc = path.join(repoRoot, 'promptfoo_engine', 'node_modules', 'promptfoo', 'dist', 'src');
const indexJs = path.join(distSrc, 'index.js');
const catalogGo = path.join(repoRoot, 'internal', 'maclaw', 'promptfoo_plugin_catalog.go');
const remoteDepJson = path.join(repoRoot, 'internal', 'maclaw', 'testdata', 'promptfoo_remote_dep.json');
const outFile = path.join(repoRoot, 'internal', 'maclaw', 'testdata', 'promptfoo_plugin_legality.json');

/** The one upstream symbol this script needs, by its unminified name. */
const UPSTREAM_SCHEMA = 'RedteamConfigSchema';

/**
 * The probe input handed to safeParse. `numTests: 1` keeps the evaluation
 * cheap while still exercising the per-plugin numTests resolution the same way
 * a real run does; `strategies: []` and a non-empty `purpose` satisfy the
 * surrounding config schema so a failure can only mean "this plugin id is not
 * accepted", never "the surrounding object was malformed".
 */
const PROBE_CONFIG = { numTests: 1, strategies: [], purpose: 'legality probe' };

/**
 * Platform-side capability gaps: ids that promptfoo ACCEPTS but that the
 * platform cannot feed because it holds no text to assert against.
 *
 * This is deliberately NOT derived from promptfoo — `policy` is a legal plugin
 * id with fanout 1, and the gap is ours (we ship no policy document), not
 * upstream's. It is recorded here rather than inferred so the distinction
 * stays visible: `requires_config` is an admission, `illegal` is a verdict.
 * `policy` must never be listed as illegal — doing so would disguise a
 * capability gap as a broken id and would discard U8's `promptfoo:policy`
 * judging semantics along with it.
 */
const REQUIRES_CONFIG_PLUGINS = {
  policy:
    'legal plugin id (safeParse passes, fanout 1) but generates 0 test cases without ' +
    'config.policy text, which the platform does not yet collect; see plan 18 §4.3',
};

/**
 * Rationale for the ids promptfoo rejects. Recorded so the report and the UI
 * can explain WHY an id was withdrawn instead of silently dropping it.
 */
const ILLEGAL_RATIONALE = {
  'prompt-injection':
    'platform-invented; promptfoo 0.123.0 has no such plugin. Nearest neighbour is ' +
    'indirect-prompt-injection, which is REMOTE_ONLY and therefore fail-closed here',
  jailbreak:
    'jailbreak is a promptfoo STRATEGY, not a plugin. The platform catalog reused the ' +
    'word as a plugin id by coincidence; its legal home is the strategy slot ' +
    '(jailbreak:meta / jailbreak:tree)',
  'security-exploit':
    'platform-invented; promptfoo splits this intent into sql-injection / ' +
    'shell-injection (both locally runnable, both absent from the platform catalog) ' +
    'and ssrf (REMOTE_ONLY)',
};

if (!fs.existsSync(distSrc)) {
  console.error(
    `[gen_plugin_legality] promptfoo is not installed at ${distSrc}.\n` +
      '  Run `cd promptfoo_engine && npm ci` first.',
  );
  process.exit(2);
}

/**
 * Parse `dist/src/index.js` for the aliased import of the tables chunk and
 * return `{ chunk, export }` for the requested semantic name. Mirrors
 * resolveTableBindings() in gen_remote_dep.mjs.
 */
function resolveSchemaBinding(semanticName) {
  const src = fs.readFileSync(indexJs, 'utf8');
  const stmt = src
    .split('\n')
    .find((line) => /import\s*\{[^}]*\}\s*from\s*"\.\/tables-[^"]+"/.test(line));
  if (!stmt) throw new Error(`could not find the tables import statement in ${indexJs}`);

  const match = stmt.match(/import\s*\{([^}]*)\}\s*from\s*"\.\/(tables-[^"]+)"/s);
  if (!match) throw new Error('malformed tables import statement in promptfoo dist/src/index.js');

  const [, aliasList, chunk] = match;
  for (const spec of aliasList.split(',').map((s) => s.trim()).filter(Boolean)) {
    const parts = spec.split(/\s+as\s+/);
    const exported = parts[0].trim();
    const local = (parts[1] ?? parts[0]).trim();
    if (local === semanticName) return { chunk, export: exported };
  }
  throw new Error(
    `promptfoo dist/src/index.js no longer imports ${semanticName} from a tables chunk. ` +
      'The upstream schema was renamed or removed; re-check this script against the new ' +
      'promptfoo build. Refusing to guess — a guessed schema would emit a legality table ' +
      'that looks authoritative and is fiction.',
  );
}

/** Import the chunk and return the zod schema plus the chunk it came from. */
async function resolveRedteamConfigSchema() {
  const binding = resolveSchemaBinding(UPSTREAM_SCHEMA);
  const mod = await import(path.join(distSrc, binding.chunk));
  const schema = mod[binding.export];
  if (!schema || typeof schema.safeParse !== 'function') {
    throw new Error(
      `${binding.chunk} export ${binding.export} (resolved for ${UPSTREAM_SCHEMA}) is not a ` +
        'zod schema (no safeParse). Refusing to continue.',
    );
  }
  return { schema, chunk: binding.chunk, export: binding.export };
}

/**
 * The catalog var body with Go line comments removed.
 *
 * The catalog documents its own withdrawals inline, and those comments quote
 * the withdrawn constructor calls verbatim — `// P2-1移出 \`p("prompt-injection", …)\``.
 * A bare `p("` search reads that prose as a declaration: it inflated the parsed
 * catalog by 4 ids and tripped the duplicate guard below on `security-exploit`,
 * so a withdrawal note masqueraded as a declaration and the generator refused to
 * run. Dropping comment-only lines keeps the guardrail describing the catalog
 * rather than the commentary about it, and is robust to indentation (unlike
 * anchoring on a leading tab).
 */
function catalogBodyWithoutComments() {
  const go = fs.readFileSync(catalogGo, 'utf8');
  const pluginStart = go.indexOf('var promptfooPluginCatalog');
  const strategyStart = go.indexOf('var promptfooStrategyCatalog');
  if (pluginStart < 0 || strategyStart < 0) throw new Error('could not locate the catalog var blocks');
  return go
    .slice(pluginStart, strategyStart)
    .split('\n')
    .filter((line) => !line.trim().startsWith('//'))
    .join('\n');
}

/** Plugin ids declared with the compact `p("id", ...)` constructor. */
function readCatalogPluginIds() {
  const ids = [...catalogBodyWithoutComments().matchAll(/\bp\("([^"]+)"/g)].map((m) => m[1]);
  if (ids.length === 0) throw new Error('parsed zero plugin ids from the catalog');
  const dupes = ids.filter((id, i) => ids.indexOf(id) !== i);
  if (dupes.length > 0) throw new Error(`catalog declares duplicate plugin ids: ${[...new Set(dupes)].join(', ')}`);
  return ids;
}

/**
 * Ids the platform WITHDREW, read from `retiredPluginCatalog` in the catalog
 * file (`ID: "..."` entries).
 *
 * These are probed alongside the live catalog on purpose. Once L1 removes the
 * three rejected ids, a catalog-only probe would report an empty illegal set —
 * which is indistinguishable from "schema resolution silently failed", the exact
 * condition the Go init guard refuses to ship. Probing the retired ids keeps
 * both the record of what was withdrawn and the canary that proves the schema
 * resolved. The Go test then asserts the two sets agree, so a withdrawal can
 * never happen without a reason being recorded, and a newly-rejected id can
 * never be dropped from the catalog silently.
 */
function readRetiredPluginIds() {
  const go = fs.readFileSync(catalogGo, 'utf8');
  const start = go.indexOf('var retiredPluginCatalog');
  if (start < 0) throw new Error('could not locate retiredPluginCatalog in the catalog file');
  const end = go.indexOf('\nvar ', start + 1);
  const body = go.slice(start, end < 0 ? undefined : end);
  const ids = [...body.matchAll(/\bID:\s*"([^"]+)"/g)].map((m) => m[1]);
  if (ids.length === 0) throw new Error('retiredPluginCatalog declares zero ids; the withdrawal record would be empty');
  return ids;
}

/**
 * Cross-reference U6's remote-dependency table so the legality file also
 * reports how many ids are legal AND locally runnable — the number the
 * rejection message quotes. U6 answered "needs remote?"; this file answers
 * "exists?"; only the intersection of both is a capability this deployment can
 * actually deliver, and it is smaller than either alone.
 */
function readRemoteMarkedPlugins() {
  if (!fs.existsSync(remoteDepJson)) {
    throw new Error(
      `${remoteDepJson} is missing. Run \`node scripts/gen_remote_dep.mjs\` first — the legality ` +
        'table cross-references it and a missing input would understate the local set.',
    );
  }
  return new Set(Object.keys(JSON.parse(fs.readFileSync(remoteDepJson, 'utf8')).catalog_marked_plugins ?? {}));
}

/** First zod issue message, trimmed — the engine's own rejection text. */
function firstIssueMessage(error) {
  const issues = error?.issues ?? [];
  if (issues.length === 0) return 'rejected by promptfoo zod (no issue detail)';
  const message = String(issues[0].message ?? '').replace(/\s+/g, ' ').trim();
  return message.length > 0 ? message : 'rejected by promptfoo zod (empty message)';
}

const { schema, chunk, export: schemaExport } = await resolveRedteamConfigSchema();
const catalogPluginIds = readCatalogPluginIds();
const retiredPluginIds = readRetiredPluginIds();
const remoteMarked = readRemoteMarkedPlugins();

// Probe the live catalog AND the withdrawn ids. A catalog-only probe would
// report an empty illegal set once L1 has removed the rejected ids, and an
// empty illegal set is exactly the shape the Go init guard treats as "the
// schema never resolved" — see readRetiredPluginIds.
const catalogIdSet = new Set(catalogPluginIds);
const probeIds = [...new Set([...catalogPluginIds, ...retiredPluginIds])];

/** Per-id verdict. `fanout` is 0 for rejected ids — they expand to nothing. */
const plugins = {};
const fanout = {};
const illegal = {};
let localOnlyLegal = 0;

for (const id of probeIds) {
  const result = schema.safeParse({ ...PROBE_CONFIG, plugins: [id] });
  if (!result.success) {
    plugins[id] = {
      legal: false,
      fanout: 0,
      reason: ILLEGAL_RATIONALE[id] ?? 'not accepted by promptfoo RedteamConfigSchema',
      rejection: firstIssueMessage(result.error),
    };
    illegal[id] = ILLEGAL_RATIONALE[id] ?? 'not accepted by promptfoo RedteamConfigSchema';
    fanout[id] = 0;
    continue;
  }
  const expanded = result.data.plugins.map((p) => p.id);
  plugins[id] = {
    legal: true,
    fanout: expanded.length,
    // The expansion is recorded only when it actually expands. 116 entries of
    // `"expands_to": ["itself"]` would bury the three ids that matter.
    ...(expanded.length > 1 ? { expands_to: expanded } : {}),
    ...(REQUIRES_CONFIG_PLUGINS[id] ? { requires_config: REQUIRES_CONFIG_PLUGINS[id] } : {}),
  };
  fanout[id] = expanded.length;
  if (catalogIdSet.has(id) && !remoteMarked.has(id)) localOnlyLegal++;
}

// A withdrawal and a legality verdict are the same fact seen from two sides, so
// the two sets must agree exactly. Without this the artifact could keep
// certifying an id the engine rejects (if someone re-adds it to the catalog) or
// carry a withdrawal note for an id promptfoo actually accepts (if a
// withdrawal is left behind after upstream starts supporting it). Either way
// the guardrail would be describing a catalog nobody has.
for (const id of retiredPluginIds) {
  if (plugins[id].legal) {
    throw new Error(
      `${id} is recorded in retiredPluginCatalog but promptfoo ACCEPTS it. The withdrawal is stale — ` +
        'either restore it to the catalog or drop the withdrawal note. Refusing to emit a table that ' +
        'contradicts the catalog.',
    );
  }
}
for (const id of catalogPluginIds) {
  if (!plugins[id].legal) {
    throw new Error(
      `${id} is in the live catalog but promptfoo REJECTS it. A run selecting it fails outright, so L1 ` +
        'must remove it (and record the withdrawal in retiredPluginCatalog). Refusing to emit.',
    );
  }
}

const requiresConfig = Object.fromEntries(
  Object.entries(REQUIRES_CONFIG_PLUGINS).filter(([id]) => plugins[id]?.legal),
);
for (const id of Object.keys(REQUIRES_CONFIG_PLUGINS)) {
  if (!plugins[id]?.legal) {
    throw new Error(
      `${id} is declared REQUIRES_CONFIG_PLUGINS but promptfoo rejects it. ` +
        'An id cannot be both illegal and merely config-gated; one of the two tables is wrong.',
    );
  }
}

const legal = catalogPluginIds.filter((id) => plugins[id].legal);
const remoteLegal = legal.length - localOnlyLegal;
const requiresConfigLegal = legal.filter((id) => plugins[id].requires_config).length;

// Sanity floor: a legality table in which almost nothing is legal means the
// schema resolution went wrong, not that the catalog rotted. Fail loudly.
if (legal.length === 0) throw new Error('every catalog id was rejected — the resolved schema is not the redteam config schema');

const payload = {
  _comment:
    'GENERATED by scripts/gen_plugin_legality.mjs — do not hand-edit. Single source of truth for plugin-id legality (P0-2 L1) and expansion fanout (P0-1 split weight). Regenerate after any promptfoo upgrade.',
  promptfoo_version: JSON.parse(fs.readFileSync(path.join(repoRoot, 'promptfoo_engine', 'package.json'), 'utf8')).dependencies.promptfoo,
  promptfoo_tables_chunk: chunk,
  promptfoo_schema_binding: { semantic_name: UPSTREAM_SCHEMA, chunk_export: schemaExport },
  probe_config: PROBE_CONFIG,
  catalog_totals: {
    plugins: catalogPluginIds.length,
    // `legal` counts the live catalog; `illegal` counts the WITHDRAWN ids. They
    // are deliberately not complements: after L1 the catalog holds no rejected
    // id, so `legal == plugins` and `illegal == len(retiredPluginCatalog)`.
    // `catalog_illegal` is the number that must stay 0 — a rejected id still in
    // the catalog is a landmine, and the generator refuses to emit one.
    legal: legal.length,
    catalog_illegal: catalogPluginIds.filter((id) => !plugins[id].legal).length,
    illegal: Object.keys(illegal).length,
    // Legal AND not remote-gated: the capabilities this deployment can
    // actually deliver end to end. Strictly smaller than either count alone.
    local_only_legal: localOnlyLegal,
    remote_legal: remoteLegal,
    requires_config: requiresConfigLegal,
  },
  illegal_plugin_ids: Object.fromEntries(Object.keys(illegal).sort().map((id) => [id, illegal[id]])),
  requires_config_plugin_ids: requiresConfig,
  fanout: Object.fromEntries(Object.entries(fanout).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))),
  plugins,
};

fs.mkdirSync(path.dirname(outFile), { recursive: true });
fs.writeFileSync(outFile, `${JSON.stringify(payload, null, 2)}\n`);

console.log(`[gen_plugin_legality] chunk            ${chunk}`);
console.log(`[gen_plugin_legality] schema binding  ${UPSTREAM_SCHEMA} -> ${schemaExport}`);
console.log(`[gen_plugin_legality] promptfoo        ${payload.promptfoo_version}`);
console.log(`[gen_plugin_legality] catalog totals   ${JSON.stringify(payload.catalog_totals)}`);
console.log(`[gen_plugin_legality] illegal ids      ${Object.keys(illegal).sort().join(', ') || '(none)'}`);
const expanded = Object.entries(fanout)
  .filter(([, n]) => n > 1)
  .sort((a, b) => b[1] - a[1])
  .map(([id, n]) => `${id}=${n}`);
console.log(`[gen_plugin_legality] expanding ids    ${expanded.join(', ') || '(none)'}`);
console.log(`[gen_plugin_legality] wrote            ${path.relative(repoRoot, outFile)}`);