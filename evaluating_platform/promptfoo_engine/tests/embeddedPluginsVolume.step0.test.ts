import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

import { patchIndexSource } from '../scripts/patch-embedded-plugins.mjs';

/**
 * Step-0 · 卷挂载演进（volume-mounted embedded-plugins）的加载语义实测。
 *
 * 提案：把 `PROMPTFOO_EMBEDDED_PLUGINS_DIR` 指到 Docker 卷（镜像里该环境变量
 * 已指向 `/app/embedded-plugins`，Dockerfile 的 `COPY` 换成卷挂载即可），
 * 插件批次更新 = 工具写卷 + 重启，**免镜像重建**。该提案成立依赖三个运行时
 * 事实——本文件对补丁注入的**原始字节**（`patchIndexSource` 输出切片，即
 * 镜像里真正跑的代码，不重实现）逐条实测：
 *
 *   1. boot 读一次：进程内改 index.json 不影响已注册表 → 卷更新必须重启才生效；
 *   2. 惰性 import：注册时包体顶层代码**不**执行 → 卷里躺着的未选中文件不跑
 *      （「引擎只读挂载 + 唯一写入方」论证的安全前提）；
 *   3. 默认路径：无环境变量时回落 `cwd/embedded-plugins` → compose 挂载点
 *      可选「设环境变量」或「挂默认路径」二选一。
 *
 * 与 `patchEmbeddedPlugins.test.ts` 的分工：那份管补丁本身的锚点/幂等/契约
 * 校验；本文件只管**卷方案依赖的加载时机语义**。Docker 卷挂载本身（:ro、
 * 健康检查、具名卷首次装载的镜像内容拷贝）不在本机可测范围——上机判据见
 * development-status §5。
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

/** Slice the registry block from the patched source — the exact bytes that ship. */
function registryBlock(): string {
  const patched = patchIndexSource(readFileSync(INDEX_PATH, 'utf8'));
  const begin = patched.indexOf('// P28-EMBEDDED-REGISTRY-BEGIN');
  const end = patched.indexOf('// P28-EMBEDDED-REGISTRY-END');
  expect(begin).toBeGreaterThanOrEqual(0);
  expect(end).toBeGreaterThan(begin);
  return patched.slice(begin, end + '// P28-EMBEDDED-REGISTRY-END'.length);
}

/** Write a one-package embedded dir: index.json + entry module. */
function writeEmbeddedDir(
  embeddedDir: string,
  id: string,
  entrySource: string,
  entryRel = 'src/plugin.mjs',
): void {
  mkdirSync(path.join(embeddedDir, id, path.dirname(entryRel)), { recursive: true });
  writeFileSync(
    path.join(embeddedDir, 'index.json'),
    JSON.stringify({ packages: [{ id, entry: entryRel }] }),
  );
  writeFileSync(path.join(embeddedDir, id, entryRel), entrySource);
}

const VALID_ENTRY = [
  'export default {',
  '  async action(params) {',
  '    return [{ vars: { [params.injectVar]: "probe" }, assert: [{ type: "contains", value: "x" }] }];',
  '  },',
  '};',
].join('\n');

function runScript(scriptPath: string, options: { env?: Record<string, string | undefined>; cwd?: string } = {}) {
  const env: Record<string, string> = {};
  for (const [key, value] of Object.entries({ ...process.env, ...options.env })) {
    if (value !== undefined) env[key] = value;
  }
  if (options.env && 'PROMPTFOO_EMBEDDED_PLUGINS_DIR' in options.env && options.env.PROMPTFOO_EMBEDDED_PLUGINS_DIR === undefined) {
    delete env.PROMPTFOO_EMBEDDED_PLUGINS_DIR;
  }
  return spawnSync(process.execPath, [scriptPath], {
    env,
    cwd: options.cwd,
    encoding: 'utf8',
  });
}

describe('Step-0: volume-mounted plugins dir load semantics', () => {
  it('1. reads index.json once at load — in-process edits do not re-register; a fresh process does (restart semantics)', () => {
    const dir = tempDir('p28-vol-boot-');
    const embeddedDir = path.join(dir, 'embedded-plugins');
    writeEmbeddedDir(embeddedDir, 'p28-vol-a', VALID_ENTRY);

    // Child A: load (registers A), then rewrite the dir to B (simulates a volume
    // update while the engine process is alive).
    const scriptA = [
      'const Plugins = [];',
      registryBlock(),
      'import { writeFileSync as __volW } from "node:fs";',
      'import { join as __volJ } from "node:path";',
      'const __volDir = process.env.PROMPTFOO_EMBEDDED_PLUGINS_DIR;',
      '__volW(__volJ(__volDir, "index.json"), JSON.stringify({ packages: [{ id: "p28-vol-b", entry: "src/plugin.mjs" }] }));',
      'console.log(JSON.stringify({ afterRewrite: Plugins.map((p) => p.key) }));',
    ].join('\n');
    const scriptAPath = path.join(dir, 'bootA.mjs');
    writeFileSync(scriptAPath, scriptA);

    const resultA = runScript(scriptAPath, { env: { PROMPTFOO_EMBEDDED_PLUGINS_DIR: embeddedDir } });
    expect(resultA.status, resultA.stderr).toBe(0);
    const verdictA = JSON.parse(resultA.stdout.trim().split('\n').pop() as string);
    // The live process still serves the list it read at load — no re-read path exists.
    expect(verdictA).toEqual({ afterRewrite: ['p28-vol-a'] });

    // Child B (fresh process = restart): picks up the rewritten content.
    const scriptB = [
      'const Plugins = [];',
      registryBlock(),
      'console.log(JSON.stringify({ freshProcess: Plugins.map((p) => p.key) }));',
    ].join('\n');
    const scriptBPath = path.join(dir, 'bootB.mjs');
    writeFileSync(scriptBPath, scriptB);
    const resultB = runScript(scriptBPath, { env: { PROMPTFOO_EMBEDDED_PLUGINS_DIR: embeddedDir } });
    expect(resultB.status, resultB.stderr).toBe(0);
    const verdictB = JSON.parse(resultB.stdout.trim().split('\n').pop() as string);
    expect(verdictB).toEqual({ freshProcess: ['p28-vol-b'] });
  });

  it('2. does not execute package top-level code at registration — only on selection (lazy import)', () => {
    const dir = tempDir('p28-vol-lazy-');
    const embeddedDir = path.join(dir, 'embedded-plugins');
    const marker = path.join(dir, 'entry-was-imported');
    // Entry writes a marker at MODULE LOAD (top-level side effect).
    const entry = [
      'import { writeFileSync } from "node:fs";',
      'writeFileSync(process.env.P28_MARKER, "imported");',
      VALID_ENTRY,
    ].join('\n');
    writeEmbeddedDir(embeddedDir, 'p28-lazy-vol', entry);

    const script = [
      'const Plugins = [];',
      registryBlock(),
      'import { existsSync as __volExists } from "node:fs";',
      'const __volMarker = process.env.P28_MARKER;',
      'const __volPlugin = Plugins.find((p) => p.key === "p28-lazy-vol");',
      'const markerBeforeAction = __volExists(__volMarker);',
      'await __volPlugin.action({ injectVar: "prompt", n: 1, config: {} });',
      'const markerAfterAction = __volExists(__volMarker);',
      'console.log(JSON.stringify({ registered: !!__volPlugin, markerBeforeAction, markerAfterAction }));',
    ].join('\n');
    const scriptPath = path.join(dir, 'lazy.mjs');
    writeFileSync(scriptPath, script);

    const result = runScript(scriptPath, {
      env: { PROMPTFOO_EMBEDDED_PLUGINS_DIR: embeddedDir, P28_MARKER: marker },
    });
    expect(result.status, result.stderr).toBe(0);
    const verdict = JSON.parse(result.stdout.trim().split('\n').pop() as string);
    // Registered (listed in the registry) but the entry module has NOT run yet;
    // selecting the plugin is what imports it. An unselected file in the volume
    // therefore never executes — the safety premise of read-only mount + single writer.
    expect(verdict).toEqual({ registered: true, markerBeforeAction: false, markerAfterAction: true });
  });

  it('3. falls back to cwd/embedded-plugins when the env var is unset (compose may mount either)', () => {
    const dir = tempDir('p28-vol-default-');
    writeEmbeddedDir(path.join(dir, 'embedded-plugins'), 'p28-default-path', VALID_ENTRY);

    const script = [
      'const Plugins = [];',
      registryBlock(),
      'console.log(JSON.stringify({ defaultPath: Plugins.map((p) => p.key) }));',
    ].join('\n');
    const scriptPath = path.join(dir, 'default.mjs');
    writeFileSync(scriptPath, script);

    const result = runScript(scriptPath, {
      cwd: dir,
      env: { PROMPTFOO_EMBEDDED_PLUGINS_DIR: undefined }, // explicitly absent
    });
    expect(result.status, result.stderr).toBe(0);
    const verdict = JSON.parse(result.stdout.trim().split('\n').pop() as string);
    expect(verdict).toEqual({ defaultPath: ['p28-default-path'] });
  });

  it('4. missing dir is still a no-op and malformed index still fails at load (fail-closed survives the volume path)', () => {
    const dir = tempDir('p28-vol-fail-');
    const missing = path.join(dir, 'never-staged');

    const noopScript = ['const Plugins = [];', registryBlock(), 'console.log("plugins=" + Plugins.length);'].join('\n');
    const noopPath = path.join(dir, 'noop.mjs');
    writeFileSync(noopPath, noopScript);
    const noopResult = runScript(noopPath, { env: { PROMPTFOO_EMBEDDED_PLUGINS_DIR: missing } });
    expect(noopResult.status, noopResult.stderr).toBe(0);
    expect(noopResult.stdout.trim()).toBe('plugins=0');

    // Malformed index.json in the volume → throw at module load (healthcheck-failing).
    const badDir = path.join(dir, 'bad-volume');
    mkdirSync(badDir, { recursive: true });
    writeFileSync(path.join(badDir, 'index.json'), '{not json');
    const badPath = path.join(dir, 'bad.mjs');
    writeFileSync(badPath, noopScript);
    const badResult = runScript(badPath, { env: { PROMPTFOO_EMBEDDED_PLUGINS_DIR: badDir } });
    expect(badResult.status).not.toBe(0);
    expect(badResult.stderr).toMatch(/JSON|SyntaxError/i);
  });
});
