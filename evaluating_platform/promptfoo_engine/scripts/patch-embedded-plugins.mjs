#!/usr/bin/env node
/**
 * P28 / E-4 · Route B engine-side patch (design:
 * docs/architecture/embedded-plugin-track.md §11).
 *
 * promptfoo's compiled plugin API has no runtime loading hook (measured in
 * Step-0: `redteam.run` only takes a YAML path/live config, and the config
 * schema's `plugins` is `string[]`). Baked embedded-plugin ids therefore cannot
 * reach the engine at runtime. This script applies the ONE-TIME infrastructure
 * patch to the installed promptfoo dist (a build step — never a per-package
 * change, per the design):
 *
 *   1. tables chunk (the one index.js imports `RedteamConfigSchema` from):
 *      append the baked ids to `pluginOptions`, so `RedteamConfigSchema`
 *      accepts them at `redteam.run` instead of throwing
 *      "Invalid redteam configuration".
 *   2. index.js: after `const Plugins = [...].map(withMaxCharsRetries);`
 *      append loader entries `{key, action}` that lazily import each baked
 *      package's entry file and validate the package contract at call time.
 *
 * Both injections read `<PROMPTFOO_EMBEDDED_PLUGINS_DIR>/index.json` AT
 * RUNTIME (default `<cwd>/embedded-plugins`), not at patch time — re-staging
 * needs no re-patch, and the engine sees exactly the catalog baked into its
 * image. The patch carries NO data.
 *
 * The patch is idempotent (marker blocks are stripped before re-inserting, and
 * the tables-set edit is reverted first). A missing anchor means promptfoo
 * changed its layout: exit 1 and let a human re-measure — never silently emit
 * an unpatched image (same discipline as the gen_* measurement scripts).
 */
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const PROMPTFOO_ROOT = path.join(HERE, '..', 'node_modules', 'promptfoo');
const INDEX_PATH = path.join(PROMPTFOO_ROOT, 'dist', 'src', 'index.js');

const REGISTRY_MARK = 'P28-EMBEDDED-REGISTRY';
const SCHEMA_MARK = 'P28-EMBEDDED-SCHEMA';

export const indexAnchor = '].map(withMaxCharsRetries);';
export const tablesAnchor = 'const pluginOptions = ';
export const tablesSetAnchor = '...ALIASED_PLUGINS\n])].sort();';
const tablesSetPatched = '...ALIASED_PLUGINS,\n\t...__p28EmbeddedPluginIds\n])].sort();';

function stripMarkedBlock(src, mark, options = {}) {
  // The two insertions place their block differently, so they must undo
  // differently (record 54 §6.1 — one shared rule made the tables half
  // non-idempotent by eating the source's own newline):
  //
  //   - registry: inserted as `anchor + '\n' + block`; the leading `\n` is OURS,
  //     so stripping it is what restores the original.
  //   - tables: inserted as `block + anchor`; the newline before the block
  //     belongs to the SOURCE (it ends the previous statement). Eating it moved
  //     `const pluginOptions` onto the previous line on the second pass, so
  //     patch(patch(src)) differed from patch(src) by exactly one `\n`.
  const lead = options.leadingNewline ? '\\n?' : '';
  const re = new RegExp(`${lead}\\/\\/ ${mark}-BEGIN[\\s\\S]*?\\/\\/ ${mark}-END\\n?`);
  return src.replace(re, '');
}

function countOccurrences(haystack, needle) {
  let count = 0;
  let idx = haystack.indexOf(needle);
  while (idx !== -1) {
    count += 1;
    idx = haystack.indexOf(needle, idx + needle.length);
  }
  return count;
}

/** Resolve the tables chunk index.js actually imports RedteamConfigSchema from. */
export function resolveTablesFile(indexSrc) {
  const specifiers = [];
  const re = /from "\.\/(tables-[^"]+\.js)"/g;
  let match;
  while ((match = re.exec(indexSrc)) !== null) {
    specifiers.push(match[1]);
  }
  if (specifiers.length === 0) {
    throw new Error('promptfoo layout changed: index.js no longer imports a ./tables-*.js chunk');
  }
  return specifiers;
}

/**
 * Inject the schema half: baked ids join `pluginOptions`, so the zod enum in
 * `RedteamConfigSchema` accepts them. Reads index.json lazily via a top-level
 * IIFE (fail-open — this is only the acceptance gate; a malformed index is the
 * registry half's job to fail on, and it does so at boot).
 */
export function patchTablesSource(src) {
  let out = stripMarkedBlock(src, SCHEMA_MARK);
  // Revert our Set edit so a second run can re-apply it (strip alone cannot).
  out = out.split(tablesSetPatched).join(tablesSetAnchor);

  if (countOccurrences(out, tablesAnchor) !== 1) {
    throw new Error(`promptfoo layout changed: expected exactly one "${tablesAnchor}" in tables chunk`);
  }
  if (countOccurrences(out, tablesSetAnchor) !== 1) {
    throw new Error('promptfoo layout changed: expected exactly one "...ALIASED_PLUGINS" set tail in tables chunk');
  }

  const block = `// ${SCHEMA_MARK}-BEGIN
import { readFileSync as __p28SchemaReadFileSync } from "node:fs";
import { join as __p28SchemaJoin } from "node:path";
const __p28EmbeddedPluginIds = (() => {
	try {
		const __p28Dir = process.env.PROMPTFOO_EMBEDDED_PLUGINS_DIR || __p28SchemaJoin(process.cwd(), "embedded-plugins");
		const __p28Doc = JSON.parse(__p28SchemaReadFileSync(__p28SchemaJoin(__p28Dir, "index.json"), "utf8"));
		return Array.isArray(__p28Doc?.packages) ? __p28Doc.packages.map((p) => p && p.id).filter((id) => typeof id === "string" && id) : [];
	} catch {
		return [];
	}
})();
// E-4d hot reload: the schema half of the removal path. Probe A (Step-0)
// measured that splicing the exported pluginOptions array IS effective —
// superRefine reads the live binding — while pushing a new id is not (zod's
// enum snapshots at construction). So a baked id leaving the catalog is
// removed here; hot-ADDED ids never enter this list at all (they ride the
// file:// synthetic-key door instead). The body touches pluginOptions only
// when called, long after this module finished loading.
globalThis.__p28SchemaApi = {
	removePlainId(id) {
		const i = pluginOptions.indexOf(id);
		if (i >= 0) pluginOptions.splice(i, 1);
	},
};
// ${SCHEMA_MARK}-END
`;

  out = out.replace(tablesAnchor, block + tablesAnchor);
  out = out.replace(tablesSetAnchor, tablesSetPatched);
  return out;
}

/**
 * Inject the registry half: baked packages become real `Plugins` entries.
 *
 * - index.json absent (fresh clone, never staged) → no-op.
 * - index.json malformed → throw at module load → engine fails its healthcheck
 *   and the deployment stays red (D-6: a bad bake must not go live).
 * - a package entry that fails to import or violates the entry contract throws
 *   only when ITS plugin is selected (lazy), so one broken package cannot take
 *   down every run on the shared engine.
 */
export function patchIndexSource(src) {
  let out = stripMarkedBlock(src, REGISTRY_MARK, { leadingNewline: true });

  if (countOccurrences(out, indexAnchor) !== 1) {
    throw new Error(`promptfoo layout changed: expected exactly one "${indexAnchor}" in index.js, found ${countOccurrences(out, indexAnchor)}`);
  }

  const block = `// ${REGISTRY_MARK}-BEGIN
import { readFileSync as __p28RegistryReadFileSync } from "node:fs";
import { join as __p28RegistryJoin } from "node:path";
import { pathToFileURL as __p28RegistryPathToFileURL } from "node:url";
{
	const __p28RegistryDir = process.env.PROMPTFOO_EMBEDDED_PLUGINS_DIR || __p28RegistryJoin(process.cwd(), "embedded-plugins");
	// E-4d: hot-added ids ride the file:// synthetic-key door (Step-0 probe B:
	// the schema accepts ANY file:// prefix and registry lookup wins before
	// CustomPlugin's YAML loader). The map is plainId -> registry key; only
	// entries whose key differs from the plain id appear in hotKeys().
	const __p28HotKeyPrefix = "file://p28-hot/";
	const __p28KeyById = new Map();
	let __p28LoadedBatch = null;

	const __p28ReadRaw = () => {
		try {
			return JSON.parse(__p28RegistryReadFileSync(__p28RegistryJoin(__p28RegistryDir, "index.json"), "utf8"));
		} catch (err) {
			if (err && err.code === "ENOENT") return null;
			throw err; // malformed JSON: boot throws (D-6), reload fails closed
		}
	};

	// Reload-side shape validation: read + validate BEFORE any mutation, so a
	// bad marker leaves the registry and the schema exactly as they were
	// (design §14.5-2). Boot keeps the historical lenient shape handling — a
	// package entry missing id/entry is skipped there, not fatal.
	const __p28ValidateDoc = (doc) => {
		if (!doc || (doc.packages !== undefined && !Array.isArray(doc.packages))) {
			throw new Error("index.json: packages must be an array");
		}
		for (const pkg of (Array.isArray(doc.packages) ? doc.packages : [])) {
			if (!pkg || typeof pkg.id !== "string" || !pkg.id || typeof pkg.entry !== "string" || !pkg.entry) {
				throw new Error("index.json: every package needs a string id and entry");
			}
		}
		return doc;
	};

	const __p28MakeAction = (id, entryUrl) => async (params) => {
		const mod = await import(entryUrl);
		const def = mod && mod.default;
		if (!def || typeof def.action !== "function") {
			throw new Error("embedded plugin " + id + ": entry must default-export { action }");
		}
		const tests = await def.action(params);
		if (!Array.isArray(tests)) {
			throw new Error("embedded plugin " + id + ": action must return an array of test cases");
		}
		for (let i = 0; i < tests.length; i++) {
			const t = tests[i];
			const v = t && t.vars ? t.vars[params.injectVar] : undefined;
			if (typeof v !== "string" || !v.trim()) {
				throw new Error("embedded plugin " + id + ": case #" + (i + 1) + " needs a non-empty vars[" + params.injectVar + "] string");
			}
			if (!Array.isArray(t.assert) || t.assert.length === 0) {
				throw new Error("embedded plugin " + id + ": case #" + (i + 1) + " needs a non-empty assert array (grading contract — a case without assertions always grades pass, which the reduction reads as attack failure)");
			}
		}
		return tests;
	};

	// bust appends ?v=<batch> so a REPLACED package re-imports fresh: ESM
	// caches by URL, and a hot batch must not serve the previous module body.
	const __p28Register = (pkg, key, bust) => {
		const url = __p28RegistryPathToFileURL(__p28RegistryJoin(__p28RegistryDir, pkg.id, pkg.entry)).href + (bust ? "?v=" + encodeURIComponent(bust) : "");
		const idx = Plugins.findIndex((p) => p.key === key);
		if (idx >= 0) Plugins.splice(idx, 1);
		Plugins.push({ key: key, action: __p28MakeAction(pkg.id, url) });
	};

	// Boot: the historical Route B behavior (§11.1) — plain keys, lazy import,
	// malformed JSON throws so a bad bake fails the healthcheck.
	const __p28BootDoc = __p28ReadRaw();
	for (const pkg of (Array.isArray(__p28BootDoc?.packages) ? __p28BootDoc.packages : [])) {
		if (!pkg || typeof pkg.id !== "string" || !pkg.id || typeof pkg.entry !== "string" || !pkg.entry) continue;
		__p28Register(pkg, pkg.id, null);
		__p28KeyById.set(pkg.id, pkg.id);
	}
	__p28LoadedBatch = typeof __p28BootDoc?.batch_id === "string" ? __p28BootDoc.batch_id : null;

	// E-4d reload hook. Called from the engine's SIGHUP handler with an isBusy
	// predicate (single worker: a run in flight refuses the reload, design
	// §14.5-3). Receipt is static strings only — no paths, no content — and is
	// surfaced verbatim on /health for the deploy script's poll.
	const __p28Reload = (opts) => {
		const receipt = { ok: false, batch: __p28LoadedBatch, reason: "" };
		try {
			if (opts && typeof opts.isBusy === "function" && opts.isBusy()) {
				receipt.reason = "busy";
				return receipt;
			}
			const raw = __p28ReadRaw();
			if (!raw) {
				receipt.reason = "missing_index";
				return receipt;
			}
			const doc = __p28ValidateDoc(raw);
			const desired = new Map();
			for (const pkg of doc.packages) desired.set(pkg.id, pkg);
			const batch = typeof doc.batch_id === "string" ? doc.batch_id : null;

			// Removals first: registry entry, then (for a baked plain id) the
			// schema acceptance list. Hot ids were never in pluginOptions.
			for (const pair of Array.from(__p28KeyById)) {
				const id = pair[0];
				const key = pair[1];
				if (desired.has(id)) continue;
				const idx = Plugins.findIndex((p) => p.key === key);
				if (idx >= 0) Plugins.splice(idx, 1);
				if (key === id && globalThis.__p28SchemaApi && typeof globalThis.__p28SchemaApi.removePlainId === "function") {
					globalThis.__p28SchemaApi.removePlainId(id);
				}
				__p28KeyById.delete(id);
			}
			// Adds and replaces: an id that already has a key keeps it (config
			// rewrites only the hot ones); every entry is re-registered with a
			// batch-busted URL so a replaced package re-imports.
			for (const pkg of desired.values()) {
				let key = __p28KeyById.get(pkg.id);
				if (!key) {
					key = __p28HotKeyPrefix + pkg.id;
					__p28KeyById.set(pkg.id, key);
				}
				__p28Register(pkg, key, batch || "hot");
			}
			__p28LoadedBatch = batch;
			receipt.ok = true;
			receipt.batch = batch;
			return receipt;
		} catch (err) {
			// Fail closed: nothing above may have mutated (validation precedes
			// mutation), and the receipt says only the static reason.
			receipt.ok = false;
			receipt.reason = "malformed_index";
			return receipt;
		}
	};

	globalThis.__p28Embedded = {
		get loadedBatch() { return __p28LoadedBatch; },
		hotKeys() {
			const out = {};
			for (const pair of __p28KeyById) {
				if (pair[1] !== pair[0]) out[pair[0]] = pair[1];
			}
			return out;
		},
		reload: __p28Reload,
	};
}
// ${REGISTRY_MARK}-END
`;

  out = out.replace(indexAnchor, indexAnchor + '\n' + block);
  return out;
}

function main() {
  let indexSrc;
  try {
    indexSrc = readFileSync(INDEX_PATH, 'utf8');
  } catch (err) {
    console.error(`patch-embedded-plugins: cannot read ${INDEX_PATH} — run npm install first (${err.message})`);
    process.exit(1);
  }

  const tablesCandidates = resolveTablesFile(indexSrc);
  const patchedIndex = patchIndexSource(indexSrc);
  writeFileSync(INDEX_PATH, patchedIndex, 'utf8');
  console.log(`patched ${path.relative(PROMPTFOO_ROOT, INDEX_PATH)}`);

  const patchedTables = [];
  for (const candidate of tablesCandidates) {
    const tablesPath = path.join(PROMPTFOO_ROOT, 'dist', 'src', candidate);
    const tablesSrc = readFileSync(tablesPath, 'utf8');
    if (!tablesSrc.includes(tablesAnchor)) {
      continue; // not the schema chunk
    }
    writeFileSync(tablesPath, patchTablesSource(tablesSrc), 'utf8');
    patchedTables.push(candidate);
    console.log(`patched ${path.relative(PROMPTFOO_ROOT, tablesPath)}`);
  }
  if (patchedTables.length === 0) {
    console.error('patch-embedded-plugins: no tables chunk exposes pluginOptions — promptfoo layout changed, re-measure the anchors');
    process.exit(1);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}
