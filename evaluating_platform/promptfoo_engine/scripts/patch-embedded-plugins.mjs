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

function stripMarkedBlock(src, mark) {
  // Consume the separator newline we added before the block too, so repeated
  // patching does not accumulate blank lines (idempotency is asserted in tests).
  const re = new RegExp(`\\n?\\/\\/ ${mark}-BEGIN[\\s\\S]*?\\/\\/ ${mark}-END\\n?`);
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
  let out = stripMarkedBlock(src, REGISTRY_MARK);

  if (countOccurrences(out, indexAnchor) !== 1) {
    throw new Error(`promptfoo layout changed: expected exactly one "${indexAnchor}" in index.js, found ${countOccurrences(out, indexAnchor)}`);
  }

  const block = `// ${REGISTRY_MARK}-BEGIN
import { readFileSync as __p28RegistryReadFileSync } from "node:fs";
import { join as __p28RegistryJoin } from "node:path";
import { pathToFileURL as __p28RegistryPathToFileURL } from "node:url";
{
	const __p28RegistryDir = process.env.PROMPTFOO_EMBEDDED_PLUGINS_DIR || __p28RegistryJoin(process.cwd(), "embedded-plugins");
	let __p28RegistryDoc = null;
	try {
		__p28RegistryDoc = JSON.parse(__p28RegistryReadFileSync(__p28RegistryJoin(__p28RegistryDir, "index.json"), "utf8"));
	} catch (__p28RegistryErr) {
		if (!(__p28RegistryErr && __p28RegistryErr.code === "ENOENT")) throw __p28RegistryErr;
	}
	for (const __p28RegistryPkg of (Array.isArray(__p28RegistryDoc?.packages) ? __p28RegistryDoc.packages : [])) {
		if (!__p28RegistryPkg || typeof __p28RegistryPkg.id !== "string" || !__p28RegistryPkg.id || typeof __p28RegistryPkg.entry !== "string" || !__p28RegistryPkg.entry) continue;
		const __p28RegistryId = __p28RegistryPkg.id;
		const __p28RegistryEntryUrl = __p28RegistryPathToFileURL(__p28RegistryJoin(__p28RegistryDir, __p28RegistryId, __p28RegistryPkg.entry)).href;
		Plugins.push({
			key: __p28RegistryId,
			action: async (__p28RegistryParams) => {
				const __p28RegistryMod = await import(__p28RegistryEntryUrl);
				const __p28RegistryDefault = __p28RegistryMod?.default;
				if (!__p28RegistryDefault || typeof __p28RegistryDefault.action !== "function") {
					throw new Error(\`embedded plugin \${__p28RegistryId}: entry must default-export { action }\`);
				}
				const __p28RegistryTests = await __p28RegistryDefault.action(__p28RegistryParams);
				if (!Array.isArray(__p28RegistryTests)) {
					throw new Error(\`embedded plugin \${__p28RegistryId}: action must return an array of test cases\`);
				}
				for (const __p28RegistryTest of __p28RegistryTests) {
					const __p28RegistryVar = __p28RegistryTest?.vars?.[__p28RegistryParams.injectVar];
					if (typeof __p28RegistryVar !== "string" || !__p28RegistryVar.trim()) {
						throw new Error(\`embedded plugin \${__p28RegistryId}: every test case needs a non-empty vars[\${__p28RegistryParams.injectVar}] string\`);
					}
					if (!Array.isArray(__p28RegistryTest.assert) || __p28RegistryTest.assert.length === 0) {
						throw new Error(\`embedded plugin \${__p28RegistryId}: every test case needs a non-empty assert array (grading contract — a case without assertions always grades pass, which the reduction reads as attack failure)\`);
					}
				}
				return __p28RegistryTests;
			}
		});
	}
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
