import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

import {
  indexAnchor,
  patchIndexSource,
  patchTablesSource,
  resolveTablesFile,
  tablesAnchor,
  tablesSetAnchor,
} from '../scripts/patch-embedded-plugins.mjs';

/**
 * P28 / E-4 · Route B patch tests.
 *
 * Two layers:
 *   1. anchor stability — the patch is applied to the REAL installed promptfoo
 *      dist (read-only). If promptfoo is upgraded and the layout moves, this
 *      fails FIRST, which is the intended upgrade tripwire (same discipline as
 *      the gen_* measurement scripts).
 *   2. loader behavior — the injected registry block is extracted verbatim from
 *      the patched source and executed against a temp embedded dir, so the
 *      assertions cover the exact code that ships inside the image, not a
 *      re-implementation of it.
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

function markedBlock(src: string, mark: string): string {
  const begin = src.indexOf(`// ${mark}-BEGIN`);
  const end = src.indexOf(`// ${mark}-END`);
  expect(begin).toBeGreaterThanOrEqual(0);
  expect(end).toBeGreaterThan(begin);
  return src.slice(begin, end + `// ${mark}-END`.length);
}

/** Remove an existing schema marker block (the installed dist may already be patched). */
function stripSchemaBlock(src: string): string {
  return src.replace(/\/\/ P28-EMBEDDED-SCHEMA-BEGIN[\s\S]*?\/\/ P28-EMBEDDED-SCHEMA-END\n?/, '');
}

describe('anchor stability against the installed promptfoo dist', () => {
  it('index.js exposes exactly one Plugins anchor and the tables chunk is resolvable', () => {
    const src = readFileSync(INDEX_PATH, 'utf8');
    expect(src.split(indexAnchor)).toHaveLength(2); // exactly one occurrence
    expect(resolveTablesFile(src).length).toBeGreaterThanOrEqual(1);
  });

  it('both patch halves apply to the real dist and are idempotent', () => {
    const indexPath = readFileSync(INDEX_PATH, 'utf8');
    // The dev checkout may already be patched (npm run patch:embedded); the
    // contract is "patched output contains the loader and re-patching is a
    // fixed point", which holds for both pristine and already-patched input.
    const onceIndex = patchIndexSource(indexPath);
    expect(onceIndex).toContain('// P28-EMBEDDED-REGISTRY-BEGIN');
    expect(patchIndexSource(onceIndex)).toBe(onceIndex);

    const tablesFiles = resolveTablesFile(indexPath);
    let applied = 0;
    for (const file of tablesFiles) {
      const filePath = path.join(PROMPTFOO_ROOT, 'dist', 'src', file);
      const src = readFileSync(filePath, 'utf8');
      if (!src.includes(tablesAnchor)) continue;
      const once = patchTablesSource(src);
      expect(once).toContain('__p28EmbeddedPluginIds');
      expect(patchTablesSource(once)).toBe(once);
      applied += 1;
    }
    expect(applied).toBeGreaterThanOrEqual(1);
  });

  it('fails loud when the anchor is missing (promptfoo upgraded, layout moved)', () => {
    expect(() => patchIndexSource('const nothing = [];\n')).toThrow(/layout changed/);
    expect(() => patchTablesSource('const nothing = [];\n')).toThrow(/layout changed/);
  });
});

describe('schema acceptance (patched tables chunk, runtime read)', () => {
  it('accepts a baked id from index.json and still rejects unknown ids', () => {
    const dir = tempDir('p28-schema-');
    writeFileSync(
      path.join(dir, 'index.json'),
      JSON.stringify({ packages: [{ id: 'p28-probe-plugin', entry: 'src/plugin.mjs' }] }),
    );

    const tablesDir = path.join(PROMPTFOO_ROOT, 'dist', 'src');
    const tablesFile = resolveTablesFile(readFileSync(INDEX_PATH, 'utf8')).find((file) =>
      readFileSync(path.join(tablesDir, file), 'utf8').includes(tablesAnchor),
    );
    expect(tablesFile).toBeTruthy();

    // The chunk imports its siblings by relative path, so the patched copy has
    // to live in the same directory. Unique name per test run; cleaned up below.
    const scratchName = `__p28_tables_probe_${process.pid}_${Date.now()}.mjs`;
    const scratchPath = path.join(tablesDir, scratchName);
    const pristine = stripSchemaBlock(
      readFileSync(path.join(tablesDir, tablesFile as string), 'utf8'),
    );
    writeFileSync(scratchPath, patchTablesSource(pristine), 'utf8');
    try {
      const script = `
        import * as tables from ${JSON.stringify('./' + scratchName)};
        const schema = tables.Y;
        const base = { numTests: 1, strategies: [], purpose: 'probe', plugins: [] };
        const ok = schema.safeParse({ ...base, plugins: ['p28-probe-plugin'] });
        const bad = schema.safeParse({ ...base, plugins: ['definitely-not-a-plugin'] });
        console.log(JSON.stringify({ accepted: ok.success, unknownRejected: !bad.success }));
      `;
      const scriptPath = path.join(tablesDir, scratchName.replace(/\.mjs$/, '.check.mjs'));
      writeFileSync(scriptPath, script, 'utf8');
      temps.push(scriptPath);
      const result = spawnSync(process.execPath, [scriptPath], {
        env: { ...process.env, PROMPTFOO_EMBEDDED_PLUGINS_DIR: dir },
        encoding: 'utf8',
      });
      expect(result.status, result.stderr).toBe(0);
      const verdict = JSON.parse(result.stdout.trim().split('\n').pop() as string);
      expect(verdict).toEqual({ accepted: true, unknownRejected: true });
    } finally {
      rmSync(scratchPath, { force: true });
    }
  });
});

describe('registry loader behavior (the exact injected block)', () => {
  function buildHarness(): { scriptPath: string; embeddedDir: string } {
    const dir = tempDir('p28-registry-');
    const embeddedDir = path.join(dir, 'embedded-plugins');
    const pkgDir = path.join(embeddedDir, 'p28-demo-plugin', 'src');
    mkdirSync(pkgDir, { recursive: true });
    writeFileSync(
      path.join(embeddedDir, 'index.json'),
      JSON.stringify({
        packages: [
          { id: 'p28-demo-plugin', name: 'demo', category: 'injection', severity: 'high', fanout: 1, entry: 'src/plugin.mjs' },
        ],
      }),
    );
    writeFileSync(
      path.join(pkgDir, 'plugin.mjs'),
      [
        'export default {',
        '  async action(params) {',
        '    return [{ vars: { [params.injectVar]: "probe payload" }, assert: [{ type: "contains", value: "x" }] }];',
        '  },',
        '};',
      ].join('\n'),
    );

    const block = markedBlock(patchIndexSource(readFileSync(INDEX_PATH, 'utf8')), 'P28-EMBEDDED-REGISTRY');
    const scriptPath = path.join(dir, 'harness.mjs');
    writeFileSync(
      scriptPath,
      [
        'const Plugins = [];',
        block,
        'const plugin = Plugins.find((p) => p.key === "p28-demo-plugin");',
        'if (!plugin) { console.error("plugin not registered"); process.exit(3); }',
        'const tests = await plugin.action({ injectVar: "prompt", n: 1, purpose: "probe", config: {} });',
        'console.log(JSON.stringify({ count: tests.length, payload: tests[0].vars.prompt, assert: tests[0].assert.length }));',
        'try {',
        '  const broken = Plugins.find((p) => p.key === "p28-broken");',
        '  if (broken) await broken.action({ injectVar: "prompt", n: 1, config: {} });',
        '} catch (err) {',
        '  console.log("BROKEN:" + String(err.message).slice(0, 80));',
        '}',
      ].join('\n'),
    );
    return { scriptPath, embeddedDir };
  }

  it('registers each baked package and returns its validated cases', () => {
    const { scriptPath, embeddedDir } = buildHarness();
    const result = spawnSync(process.execPath, [scriptPath], {
      env: { ...process.env, PROMPTFOO_EMBEDDED_PLUGINS_DIR: embeddedDir },
      encoding: 'utf8',
    });
    expect(result.status, result.stderr).toBe(0);
    const verdict = JSON.parse(result.stdout.trim().split('\n')[0]);
    expect(verdict).toEqual({ count: 1, payload: 'probe payload', assert: 1 });
  });

  it('is a no-op when index.json is absent (fresh clone, nothing staged)', () => {
    const dir = tempDir('p28-empty-');
    const script = [
      'const Plugins = [];',
      markedBlock(patchIndexSource(readFileSync(INDEX_PATH, 'utf8')), 'P28-EMBEDDED-REGISTRY'),
      'console.log("plugins=" + Plugins.length);',
    ].join('\n');
    const scriptPath = path.join(dir, 'empty.mjs');
    writeFileSync(scriptPath, script);
    const result = spawnSync(process.execPath, [scriptPath], {
      env: { ...process.env, PROMPTFOO_EMBEDDED_PLUGINS_DIR: path.join(dir, 'missing') },
      encoding: 'utf8',
    });
    expect(result.status, result.stderr).toBe(0);
    expect(result.stdout.trim()).toBe('plugins=0');
  });

  it('fails loud at load time on a malformed index.json (bad bake must not go live)', () => {
    const dir = tempDir('p28-bad-');
    writeFileSync(path.join(dir, 'index.json'), '{not json');
    const script = [
      'const Plugins = [];',
      markedBlock(patchIndexSource(readFileSync(INDEX_PATH, 'utf8')), 'P28-EMBEDDED-REGISTRY'),
      'console.log("plugins=" + Plugins.length);',
    ].join('\n');
    const scriptPath = path.join(dir, 'bad.mjs');
    writeFileSync(scriptPath, script);
    const result = spawnSync(process.execPath, [scriptPath], {
      env: { ...process.env, PROMPTFOO_EMBEDDED_PLUGINS_DIR: dir },
      encoding: 'utf8',
    });
    expect(result.status).not.toBe(0);
  });

  it('rejects a package entry that violates the contract only when selected (lazy)', () => {
    const dir = tempDir('p28-lazy-');
    const embeddedDir = path.join(dir, 'embedded-plugins');
    mkdirSync(path.join(embeddedDir, 'p28-broken', 'src'), { recursive: true });
    writeFileSync(
      path.join(embeddedDir, 'index.json'),
      JSON.stringify({ packages: [{ id: 'p28-broken', entry: 'src/plugin.mjs' }] }),
    );
    // no assert array — the grading contract violation
    writeFileSync(
      path.join(embeddedDir, 'p28-broken', 'src', 'plugin.mjs'),
      'export default { async action(params) { return [{ vars: { [params.injectVar]: "x" } }]; } };',
    );
    const script = [
      'const Plugins = [];',
      markedBlock(patchIndexSource(readFileSync(INDEX_PATH, 'utf8')), 'P28-EMBEDDED-REGISTRY'),
      'const plugin = Plugins.find((p) => p.key === "p28-broken");',
      'try { await plugin.action({ injectVar: "prompt", n: 1, config: {} }); console.log("NO_THROW"); }',
      'catch (err) { console.log("THREW:" + String(err.message).slice(0, 120)); }',
    ].join('\n');
    const scriptPath = path.join(dir, 'lazy.mjs');
    writeFileSync(scriptPath, script);
    const result = spawnSync(process.execPath, [scriptPath], {
      env: { ...process.env, PROMPTFOO_EMBEDDED_PLUGINS_DIR: embeddedDir },
      encoding: 'utf8',
    });
    expect(result.status, result.stderr).toBe(0);
    expect(result.stdout).toContain('THREW:');
    expect(result.stdout).toContain('assert');
  });
});
