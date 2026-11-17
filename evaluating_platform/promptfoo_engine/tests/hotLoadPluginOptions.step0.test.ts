import { spawnSync } from 'node:child_process';
import { readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

/**
 * Step-0 · E-4d 热加载（方案 B-v2 全链零重启）的探测记录，两轮，全部对
 * 已安装 dist 的真实代码测量、不重实现：
 *
 * 探针 A（2026-11-16，zod 4.6.5）—— 裸 id 路线判死：
 *   运行时往 `pluginOptions` 数组 push 新 id → safeParse 仍拒：
 *   `z.enum` 构造时定格数组（深遍历含 symbol 键无可变容器）；
 *   splice 移除有效（`superRefine` 活读闭包）。
 *   ⇒ 裸新 id 永远过不了 schema；「工厂+let 重建 schema」手术曾是备选，
 *      被探针 B 取代（见下），本绊线继续钉住「裸 id 被拒」这一前提。
 *
 * 探针 B（2026-11-17）—— file:// 合成 key 通路成立：
 *   ① schema 实测：任意 `file://` 前缀字符串通过 safeParse（分支是
 *      `startsWith("file://")`，不查文件存在、不查扩展名）；
 *   ② 解析顺序（index.js 静态读实）：全文仅两处 `Plugins.find`——
 *      生成循环 `const { action } = Plugins.find(key===id) || {}`，
 *      **注册表精确命中优先**，`new CustomPlugin`（file:// YAML 门，
 *      会去读盘）在 else；别名展开被 `Plugins.some(key===id)` 短路。
 *   ⇒ 热加载以合成 key `file://p28-hot/<id>` 注册即可：config 改写为该
 *      key → schema 放行 → 注册表命中 → 我们的 action；报告侧用现成的
 *      reportPluginID 机器归一回公开 id。**schema 手术不需要。**
 *
 * 本文件把两轮事实钉成绊线：zod/promptfoo 升级若改变任一语义
 * （push 变可接受 / file:// 分支收窄 / 解析顺序换位），这里先红——
 * E-4d 的设计前提必须重估，而不是部署后静默失败。
 */

const PROMPTFOO_ROOT = path.join(process.cwd(), 'node_modules', 'promptfoo');
const INDEX_PATH = path.join(PROMPTFOO_ROOT, 'dist', 'src', 'index.js');

const temps: string[] = [];
afterEach(() => {
  for (const dir of temps.splice(0)) rmSync(dir, { recursive: true, force: true });
});

/** Resolve the tables chunk that owns pluginOptions + RedteamConfigSchema. */
function resolveTablesFile(): { tablesDir: string; tablesFile: string } {
  const indexSrc = readFileSync(INDEX_PATH, 'utf8');
  const candidates = [...indexSrc.matchAll(/from "\.\/(tables-[^"]+\.js)"/g)].map((m) => m[1]);
  expect(candidates.length).toBeGreaterThan(0);
  const tablesDir = path.join(PROMPTFOO_ROOT, 'dist', 'src');
  const tablesFile = candidates.find((file) =>
    readFileSync(path.join(tablesDir, file), 'utf8').includes('const pluginOptions = '),
  );
  expect(tablesFile, 'no tables chunk exposes pluginOptions — layout changed, re-measure').toBeTruthy();
  return { tablesDir, tablesFile: tablesFile as string };
}

/** Scratch copy = real installed chunk + export line (installed dist untouched). */
function writeScratchChunk(tablesDir: string, tablesFile: string, extraExport: string): string {
  const name = `__p28_step0_${process.pid}_${Date.now()}_${Math.random().toString(36).slice(2, 8)}.mjs`;
  const scratchPath = path.join(tablesDir, name);
  temps.push(scratchPath);
  const original = readFileSync(path.join(tablesDir, tablesFile), 'utf8');
  writeFileSync(scratchPath, `${original}\n${extraExport}\n`, 'utf8');
  return name;
}

function runChild(tablesDir: string, scriptBody: string): Record<string, any> {
  const scriptPath = path.join(tablesDir, `__p28_step0_${process.pid}_${Date.now()}.check.mjs`);
  temps.push(scriptPath);
  writeFileSync(scriptPath, scriptBody, 'utf8');
  const result = spawnSync(process.execPath, [scriptPath], { encoding: 'utf8' });
  expect(result.status, result.stderr).toBe(0);
  return JSON.parse(result.stdout.trim().split('\n').pop() as string);
}

describe('Step-0 A: runtime push into pluginOptions (zod 4.6.5 snapshot)', () => {
  it('rejects a runtime-pushed plain id but honors a runtime splice', () => {
    const { tablesDir, tablesFile } = resolveTablesFile();
    const scratch = writeScratchChunk(tablesDir, tablesFile, 'export { pluginOptions };');
    const verdict = runChild(
      tablesDir,
      `
import * as tables from ${JSON.stringify('./' + scratch)};
const schema = tables.Y; // RedteamConfigSchema
const base = { numTests: 1, strategies: [], purpose: 'probe', plugins: [] };
const NEW_ID = '__p28_hot_push__';
const pushRejectedBefore = !schema.safeParse({ ...base, plugins: [NEW_ID] }).success;
let pushAcceptedAfter = false;
let pushError = '';
try {
  tables.pluginOptions.push(NEW_ID);
  pushAcceptedAfter = schema.safeParse({ ...base, plugins: [NEW_ID] }).success;
} catch (err) { pushError = String(err && err.message ? err.message : err); }
const victim = tables.pluginOptions[0];
const removalAcceptedBefore = !!victim && schema.safeParse({ ...base, plugins: [victim] }).success;
let removalRejectedAfter = false;
if (victim) {
  tables.pluginOptions.splice(tables.pluginOptions.indexOf(victim), 1);
  removalRejectedAfter = !schema.safeParse({ ...base, plugins: [victim] }).success;
}
console.log(JSON.stringify({ pushRejectedBefore, pushAcceptedAfter, pushError, removalAcceptedBefore, removalRejectedAfter }));
`,
    );

    expect(verdict.pushRejectedBefore, 'a never-pushed id must be rejected BEFORE the push').toBe(true);
    // MEASURED FACT pinned BOTH ways: if an upgrade ever flips this to true,
    // plain-id hot-add becomes possible and the file:// rewrite design
    // (probe B) could be simplified — the red here is the tripwire.
    expect(
      verdict.pushAcceptedAfter,
      `zod must keep snapshot semantics (pushError=${verdict.pushError}) — if this flips to true, plain-id hot-add is possible: re-evaluate the E-4d file:// rewrite design`,
    ).toBe(false);
    expect(verdict.removalAcceptedBefore).toBe(true);
    expect(verdict.removalRejectedAfter, 'a spliced-out id must be rejected (superRefine reads the live array)').toBe(true);
  });
});

describe('Step-0 B: file:// synthetic-key path (schema + resolution order)', () => {
  it('schema accepts any file:// prefix and still rejects plain new ids', () => {
    const { tablesDir, tablesFile } = resolveTablesFile();
    const scratch = writeScratchChunk(tablesDir, tablesFile, 'export { RedteamConfigSchema as RCS };');
    const verdict = runChild(
      tablesDir,
      `
import * as tables from ${JSON.stringify('./' + scratch)};
const s = tables.RCS;
const base = { numTests: 1, strategies: [], purpose: 'p', plugins: [] };
const out = {
  plainNew: s.safeParse({ ...base, plugins: ['__p28_never_in_list__'] }).success,
  fileSynthetic: s.safeParse({ ...base, plugins: ['file://p28-hot/p28-new-plugin'] }).success,
  fileYaml: s.safeParse({ ...base, plugins: ['file:///app/embedded-plugins/p28-new/plugin.yaml'] }).success,
  builtin: s.safeParse({ ...base, plugins: ['harmful'] }).success,
};
console.log(JSON.stringify(out));
`,
    );
    expect(verdict.plainNew, 'plain new id must stay rejected (probe A premise)').toBe(false);
    expect(verdict.fileSynthetic, 'ANY file:// prefix must pass — this is the hot-add door').toBe(true);
    expect(verdict.fileYaml).toBe(true);
    expect(verdict.builtin).toBe(true);
  });

  // Source-order anchor: the load-bearing fact is that the registry lookup wins
  // BEFORE the file:// YAML door. If a promptfoo upgrade reorders resolution
  // (CustomPlugin first, or a different keying), hot plugins would hit the
  // file-loader instead of our action — this must go red at upgrade time, not
  // at a user's run.
  it('keeps registry-first resolution (Plugins.find before the CustomPlugin else-branch)', () => {
    const src = readFileSync(INDEX_PATH, 'utf8');
    // Generation loop: exact-key registry hit, then our action, else CustomPlugin.
    const lookup = 'const { action } = Plugins.find((p) => p.key === plugin.id) || {};';
    expect(src.includes(lookup), 'generation-loop registry lookup moved — re-measure resolution order').toBe(true);
    const elseIdx = src.indexOf('new CustomPlugin(');
    expect(elseIdx, 'CustomPlugin else-branch disappeared — re-measure resolution order').toBeGreaterThanOrEqual(0);
    const lookupIdx = src.indexOf(lookup);
    expect(lookupIdx).toBeLessThan(elseIdx);
    // The `if (action)` gate sits between them (registry hit short-circuits).
    const gateIdx = src.indexOf('if (action)', lookupIdx);
    expect(gateIdx).toBeGreaterThan(lookupIdx);
    expect(gateIdx).toBeLessThan(elseIdx);
    // Alias expansion is short-circuited by an exact-key registry hit.
    expect(
      src.includes('if (Plugins.some((p) => p.key === plugin.id))'),
      'alias-expansion short-circuit moved — re-measure',
    ).toBe(true);
    // Schema's file:// branch still exists (tables chunk).
    const { tablesDir, tablesFile } = resolveTablesFile();
    const tables = readFileSync(path.join(tablesDir, tablesFile), 'utf8');
    expect(
      tables.includes('Custom plugins must start with file://'),
      'schema file:// branch disappeared — hot-add door closed, re-measure',
    ).toBe(true);
  });
});
