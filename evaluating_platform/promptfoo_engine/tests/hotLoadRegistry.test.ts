import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

import { indexAnchor, patchIndexSource } from '../scripts/patch-embedded-plugins.mjs';

/**
 * E-4d-1 · the hot-reload hook injected into promptfoo's index.js.
 *
 * The harness runs the EXACT injected block (extracted from patchIndexSource,
 * same discipline as patchEmbeddedPlugins.test.ts) against a temp
 * embedded-plugins dir, then drives `reload()` through the states the design
 * §14.5 criteria pin: busy refusal, malformed index = zero mutation, add /
 * replace / remove, hot-key bookkeeping, missing index.
 *
 * `globalThis.__p28SchemaApi` is faked here: the real one lives in the tables
 * chunk and splices `pluginOptions` (Step-0 probe A measured that effective);
 * this harness only asserts that a REMOVED BAKED id asks for the splice and a
 * removed HOT id does not (it was never in the list).
 */

const PROMPTFOO_ROOT = path.join(process.cwd(), 'node_modules', 'promptfoo');
const INDEX_PATH = path.join(PROMPTFOO_ROOT, 'dist', 'src', 'index.js');

const temps: string[] = [];
afterEach(() => {
  for (const dir of temps.splice(0)) rmSync(dir, { recursive: true, force: true });
});

function tempDir(prefix: string): string {
  const dir = mkdtempSync(path.join(os.tmpdir(), prefix));
  temps.push(dir);
  return dir;
}

function markedBlock(): string {
  const src = readFileSync(INDEX_PATH, 'utf8');
  const patched = patchIndexSource(src);
  const begin = patched.indexOf('// P28-EMBEDDED-REGISTRY-BEGIN');
  const end = patched.indexOf('// P28-EMBEDDED-REGISTRY-END');
  expect(begin).toBeGreaterThanOrEqual(0);
  expect(end).toBeGreaterThan(begin);
  return patched.slice(begin, end + '// P28-EMBEDDED-REGISTRY-END'.length);
}

/** Run one reload scenario; returns the harness's JSON verdict. */
function runScenario(initialIndex: Record<string, unknown>): Record<string, any> {
  const dir = tempDir('p28-hotload-');
  writeFileSync(path.join(dir, 'index.json'), JSON.stringify(initialIndex), 'utf8');

  const scriptPath = path.join(dir, 'harness.mjs');
  writeFileSync(
    scriptPath,
    [
      "import { readFileSync, rmSync, writeFileSync } from 'node:fs';",
      "import path from 'node:path';",
      'const Plugins = [];',
      'const removeCalls = [];',
      'globalThis.__p28SchemaApi = { removePlainId: (id) => removeCalls.push(id) };',
      markedBlock(),
      "const dir = process.env.PROMPTFOO_EMBEDDED_PLUGINS_DIR;",
      "const idxPath = path.join(dir, 'index.json');",
      'const out = {};',
      'out.bootKeys = Plugins.map((p) => p.key);',
      'out.bootBatch = globalThis.__p28Embedded.loadedBatch;',
      // 1. busy refuses before touching anything.
      "out.busy = globalThis.__p28Embedded.reload({ isBusy: () => true });",
      'out.afterBusyKeys = Plugins.map((p) => p.key);',
      // 2. malformed marker: zero mutation + static reason.
      "const original = readFileSync(idxPath, 'utf8');",
      "writeFileSync(idxPath, '{broken json');",
      "out.bad = globalThis.__p28Embedded.reload({ isBusy: () => false });",
      'out.afterBadKeys = Plugins.map((p) => p.key);',
      // 3. good reload: drop pkg-a (baked), keep pkg-b, add pkg-c (hot).
      'const originalDoc = JSON.parse(original);',
      'const nextPackages = originalDoc.packages.filter((p) => p.id !== "pkg-a");',
      'nextPackages.push({ id: "pkg-c", entry: "src/plugin.mjs" });',
      'writeFileSync(idxPath, JSON.stringify({ batch_id: "batch-2", packages: nextPackages }));',
      "out.good = globalThis.__p28Embedded.reload({ isBusy: () => false });",
      'out.afterGoodKeys = Plugins.map((p) => p.key);',
      'out.hotKeys = globalThis.__p28Embedded.hotKeys();',
      'out.loadedBatch = globalThis.__p28Embedded.loadedBatch;',
      'out.removeCalls = removeCalls.slice();',
      // 4. the hot id itself is removed next batch: registry entry goes, but
      //    removePlainId must NOT be called (it never entered pluginOptions).
      'writeFileSync(idxPath, JSON.stringify({ batch_id: "batch-3", packages: originalDoc.packages.filter((p) => p.id !== "pkg-a") }));',
      "out.good2 = globalThis.__p28Embedded.reload({ isBusy: () => false });",
      'out.hotKeys2 = globalThis.__p28Embedded.hotKeys();',
      'out.removeCalls2 = removeCalls.slice();',
      'out.afterGood2Keys = Plugins.map((p) => p.key);',
      // 5. missing index: fail closed, no mutation.
      'rmSync(idxPath);',
      "out.missing = globalThis.__p28Embedded.reload({ isBusy: () => false });",
      'out.afterMissingKeys = Plugins.map((p) => p.key);',
      'console.log(JSON.stringify(out));',
    ].join('\n'),
    'utf8',
  );

  const result = spawnSync(process.execPath, [scriptPath], {
    env: { ...process.env, PROMPTFOO_EMBEDDED_PLUGINS_DIR: dir },
    encoding: 'utf8',
  });
  expect(result.status, result.stderr).toBe(0);
  return JSON.parse(result.stdout.trim().split('\n').pop() as string);
}

const INITIAL = {
  batch_id: 'batch-1',
  packages: [
    { id: 'pkg-a', entry: 'src/plugin.mjs' },
    { id: 'pkg-b', entry: 'src/plugin.mjs' },
  ],
};

describe('hot reload hook (E-4d-1)', () => {
  it('boots with plain keys, refuses busy, fails closed on bad data, applies add/replace/remove', () => {
    const out = runScenario(INITIAL);

    // Boot: plain keys, batch from index.json (Route B unchanged).
    expect(out.bootKeys).toEqual(['pkg-a', 'pkg-b']);
    expect(out.bootBatch).toBe('batch-1');

    // §14.5-3: busy → static receipt, registry untouched.
    expect(out.busy).toEqual({ ok: false, batch: 'batch-1', reason: 'busy' });
    expect(out.afterBusyKeys).toEqual(out.bootKeys);

    // §14.5-2: malformed → zero mutation + static reason (no path, no content).
    expect(out.bad).toEqual({ ok: false, batch: 'batch-1', reason: 'malformed_index' });
    expect(out.afterBadKeys).toEqual(out.bootKeys);

    // Good reload: pkg-a removed (baked → registry + schema splice requested),
    // pkg-c added under the synthetic key, pkg-b kept under its plain key.
    expect(out.good).toEqual({ ok: true, batch: 'batch-2', reason: '' });
    expect(out.afterGoodKeys.sort()).toEqual(['file://p28-hot/pkg-c', 'pkg-b']);
    expect(out.hotKeys).toEqual({ 'pkg-c': 'file://p28-hot/pkg-c' });
    expect(out.loadedBatch).toBe('batch-2');
    expect(out.removeCalls).toEqual(['pkg-a']);

    // Removing the hot id: registry entry goes, no schema splice for it.
    expect(out.good2.ok).toBe(true);
    expect(out.afterGood2Keys).toEqual(['pkg-b']);
    expect(out.hotKeys2).toEqual({});
    expect(out.removeCalls2).toEqual(['pkg-a']);

    // Missing index fails closed without mutating.
    expect(out.missing).toEqual({ ok: false, batch: 'batch-3', reason: 'missing_index' });
    expect(out.afterMissingKeys).toEqual(['pkg-b']);
  });

  it('exposes the block exactly once and keeps patch idempotent', () => {
    const once = patchIndexSource(readFileSync(INDEX_PATH, 'utf8'));
    expect(patchIndexSource(once)).toBe(once);
    expect(once.split('// P28-EMBEDDED-REGISTRY-BEGIN')).toHaveLength(2);
    // The synthetic-key prefix is the contract the config rewrite matches on.
    expect(once).toContain('file://p28-hot/');
  });

  it('keeps the schema file:// door and registry-first anchors (Step-0 B)', () => {
    const src = readFileSync(INDEX_PATH, 'utf8');
    expect(src.includes('const { action } = Plugins.find((p) => p.key === plugin.id) || {};')).toBe(true);
    expect(src.includes('if (Plugins.some((p) => p.key === plugin.id))')).toBe(true);
  });

  it('anchors survive on a pristine (unpatched) dist', () => {
    const src = readFileSync(INDEX_PATH, 'utf8');
    expect(src.split(indexAnchor)).toHaveLength(2);
  });
});
