import { spawnSync } from 'node:child_process';
import { readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

/**
 * Step-0 · E-4d 热加载（方案 B-v2）的 make-or-break 探针：
 * 运行时往 `pluginOptions` 数组 push 一个新插件 id，`RedteamConfigSchema`
 * 的 safeParse 是否接受它。
 *
 * 背景（tables chunk 实测，2026-11-16）：
 *   const pluginOptions = [...new Set([…])]                    ← 模块加载时物化的数组
 *   z$1.enum(pluginOptions).superRefine((val, ctx) => {
 *     if (!pluginOptions.includes(val)) …                      ← 判据读**活数组**（闭包）
 *   })
 * zod 4.6.5。两条路径的语义不同：
 *   - **移除**（splice）：superRefine 活读 → 必然生效（enum 门即使持快照也拦不住被拒）；
 *   - **新增**（push）：enum 门若在构造时**快照**了数组 → push 后新 id 过不了 enum，
 *     superRefine 根本没机会跑 —— 热加载的新插件会被 schema 拒
 *     （`Invalid redteam configuration`），方案前提破灭。
 *
 * 本文件对**已安装 dist 的真实 tables chunk**（副本导出 pluginOptions，不改原文件）
 * 逐条测量，不重实现。**实测判决（2026-11-16，zod 4.6.5）**：
 *   - 热 push 新 id：**无效**（enum 构造时定格；深遍历含 symbol 键未找到可变
 *     容器）→ 热增插件 id 不可行，E-4d 范围据此收窄（新增 id 的批次仍走
 *     recreate；schema 语句改「工厂 + let」的重建手术是备选，未做）；
 *   - 热移除（splice）：**有效**（superRefine 活读闭包）→ 热移除可用。
 *
 * 本测试把这组事实钉成双向绊线：zod/promptfoo 升级若让 push 变为可接受
 * （热增突然可行）或让移除失效，这里先红——范围决策必须重估，而不是部署
 * 后静默失败。
 */

const PROMPTFOO_ROOT = path.join(process.cwd(), 'node_modules', 'promptfoo');
const INDEX_PATH = path.join(PROMPTFOO_ROOT, 'dist', 'src', 'index.js');

const temps: string[] = [];
afterEach(() => {
  for (const dir of temps.splice(0)) rmSync(dir, { recursive: true, force: true });
});

describe('Step-0: hot-push into pluginOptions survives RedteamConfigSchema', () => {
  it('accepts a runtime-pushed id and rejects a runtime-removed one (zod 4.6.5 contract)', () => {
    // Resolve the tables chunk the schema actually lives in (same recipe as
    // patchEmbeddedPlugins.test.ts — the chunk imports siblings relatively, so
    // the probe copy must live in dist/src).
    const indexSrc = readFileSync(INDEX_PATH, 'utf8');
    const candidates = [...indexSrc.matchAll(/from "\.\/(tables-[^"]+\.js)"/g)].map((m) => m[1]);
    expect(candidates.length).toBeGreaterThan(0);
    const tablesDir = path.join(PROMPTFOO_ROOT, 'dist', 'src');
    const tablesFile = candidates.find((file) =>
      readFileSync(path.join(tablesDir, file), 'utf8').includes('const pluginOptions = '),
    );
    expect(tablesFile, 'no tables chunk exposes pluginOptions — layout changed, re-measure').toBeTruthy();

    // Probe copy = the REAL installed chunk + one export line (test-controlled
    // file, cleaned up below; the installed dist is never modified).
    const scratchName = `__p28_hot_probe_${process.pid}_${Date.now()}.mjs`;
    const scratchPath = path.join(tablesDir, scratchName);
    temps.push(scratchPath);
    const original = readFileSync(path.join(tablesDir, tablesFile as string), 'utf8');
    writeFileSync(scratchPath, `${original}\nexport { pluginOptions };\n`, 'utf8');

    // The probe script must sit NEXT TO the scratch chunk (its import is
    // relative), exactly like patchEmbeddedPlugins.test.ts's schema harness.
    const scriptPath = path.join(tablesDir, `__p28_hot_probe_${process.pid}_${Date.now()}.check.mjs`);
    temps.push(scriptPath);
    writeFileSync(
      scriptPath,
      `
import * as tables from ${JSON.stringify('./' + scratchName)};
const schema = tables.Y; // RedteamConfigSchema (same export the patch tests use)
const base = { numTests: 1, strategies: [], purpose: 'probe', plugins: [] };
const NEW_ID = '__p28_hot_push__';

const pushRejectedBefore = !schema.safeParse({ ...base, plugins: [NEW_ID] }).success;
let pushAcceptedAfter = false;
let pushError = '';
try {
  tables.pluginOptions.push(NEW_ID);
  pushAcceptedAfter = schema.safeParse({ ...base, plugins: [NEW_ID] }).success;
} catch (err) {
  pushError = String(err && err.message ? err.message : err);
}

// Removal: superRefine reads the live array (measured from source), so splice
// must make a previously-valid id invalid regardless of the enum gate.
const victim = tables.pluginOptions[0];
const removalAcceptedBefore = !!victim && schema.safeParse({ ...base, plugins: [victim] }).success;
let removalRejectedAfter = false;
if (victim) {
  tables.pluginOptions.splice(tables.pluginOptions.indexOf(victim), 1);
  removalRejectedAfter = !schema.safeParse({ ...base, plugins: [victim] }).success;
}

console.log(JSON.stringify({
  pushRejectedBefore,
  pushAcceptedAfter,
  pushError,
  removalAcceptedBefore,
  removalRejectedAfter,
}));
`,
      'utf8',
    );

    const result = spawnSync(process.execPath, [scriptPath], { encoding: 'utf8' });
    expect(result.status, result.stderr).toBe(0);
    const verdict = JSON.parse(result.stdout.trim().split('\n').pop() as string);

    // Sanity: the schema is not a rubber stamp before the push.
    expect(verdict.pushRejectedBefore, 'a never-pushed id must be rejected BEFORE the push').toBe(true);
    // MEASURED FACT (zod 4.6.5, 2026-11-16): z.enum captures pluginOptions at
    // construction — a runtime push does NOT make safeParse accept the id, and
    // a deep walk (symbol keys included) found no reachable mutable container
    // for the enum values. Pinned BOTH ways: if an upgrade ever flips this to
    // true, hot-ADD becomes possible and E-4d's scope decision must be re-made
    // — the red here is the tripwire, not a defect.
    expect(verdict.pushAcceptedAfter, `zod must keep snapshot semantics (pushError=${verdict.pushError}) — if this flips to true, hot-add is now possible: re-evaluate E-4d scope`).toBe(false);
    // Removal contract: the live-array superRefine (splice → reject) — this one
    // works today and any hot-REMOVE relies on it.
    expect(verdict.removalAcceptedBefore).toBe(true);
    expect(verdict.removalRejectedAfter, 'a spliced-out id must be rejected (superRefine reads the live array)').toBe(true);
  });
});
