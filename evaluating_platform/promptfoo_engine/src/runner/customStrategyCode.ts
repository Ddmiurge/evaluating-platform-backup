/**
 * customStrategyCode.ts — expert-published **code** strategy packages
 * (`pf_strategy_form=js`) and the `file://` strategy refs they produce.
 *
 * # What Step 0 measured (2026-10-28, tests/strategyCodePackages.step0.test.ts)
 *
 *   - promptfoo's strategy schema accepts `file://…js` and `validateStrategies()`
 *     skips it explicitly (`if (strategy.id.startsWith("file://")) continue`);
 *   - `applyStrategies()` imports the module and calls its `action`;
 *   - **the module's exported `id` is never used.** The per-case metadata is
 *     `strategyId: t?.metadata?.strategyId || strategy.id`, i.e. the file://
 *     PATH. So `case_results[].strategy_id` and `strategy_stats[].id` carry the
 *     workdir path unless this module rewrites them — and AGENTS.md forbids local
 *     paths in run records and reports.
 *
 * That is why both halves live here: the file is written under the run workdir
 * (so the path is bounded), and the ref is normalised back to the package's
 * public id before anything is aggregated.
 *
 * # Why the file name is generated, never taken from the package
 *
 * Same rule as the plugin materializer: promptfoo joins the ref with
 * `safeJoin(state.basePath || cwd, filePath)`, and `safeJoin` returns an
 * ABSOLUTE path unchanged. A name from package content would therefore be a path
 * injection primitive. The name is derived from the validated variant only.
 */
import fs from 'node:fs/promises';
import path from 'node:path';

import type { EngineCustomStrategyCode } from '../types.js';

export type { EngineCustomStrategyCode };

/** Directory inside the per-run workdir. Mirrors `custom-plugins/`. */
export const CUSTOM_STRATEGY_DIR = 'custom-strategies';

/** Marker the engine uses to recognise a file-based strategy ref. */
export const FILE_STRATEGY_PREFIX = 'file://';

/** Errors a code package can be rejected with. */
export const CODE_STRATEGY_ERR_INVALID = 'custom_strategy_code_invalid';
export const CODE_STRATEGY_ERR_UNSAFE = 'custom_strategy_code_unsafe';
export const CODE_STRATEGY_ERR_TIMEOUT = 'custom_strategy_code_timeout';

/** Cap on a code package. Algorithmic code; anything larger is data. */
export const CODE_STRATEGY_MAX_BYTES = 256 * 1024;

/**
 * Tokens that make a package refuse at load time.
 *
 * This is a THRESHOLD, not a boundary — see the design doc §4.3. It stops the
 * obvious cases from ever running inside the engine process; the boundary is the
 * tenant isolation the package is released behind.
 */
const FORBIDDEN_TOKENS: Array<[RegExp, string]> = [
  [/\bchild_process\b/, 'child_process'],
  [/\bprocess\s*\.\s*env\b/, 'process.env'],
  [/\brequire\s*\(/, 'require('],
  [/\bimport\s*\(\s*['"`]/, 'dynamic import()'],
  [/\bfetch\s*\(/, 'fetch('],
  [/\bhttps?\s*\.\s*request\b/, 'http.request'],
  [/\bnew\s+Function\b/, 'new Function'],
  [/\beval\s*\(/, 'eval('],
  [/\bglobalThis\b/, 'globalThis'],
];

export class CodeStrategyError extends Error {
  constructor(
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = 'CodeStrategyError';
  }
}

/**
 * Swap each code package's resolved id for the file:// ref this run materialized.
 *
 * # Why a REPLACEMENT and not an append (record 42, measured)
 *
 * The first version appended the file:// refs alongside whatever the request
 * named. For a code package the request names its resolved `custom:<variant>`
 * (the BFF no longer sends the `pf_strategy:` marker, because no table swaps it
 * on this channel). That id is a promptfoo `custom` strategy with **no
 * strategyText**, so leaving it in the config makes `validateStrategies()` throw
 * mid-run — and the run that reached it had two entries for one package.
 *
 * So the id is the package's NAME in the request; the ref is what promptfoo
 * loads. This function turns one into the other, exactly as
 * `applyCustomStrategyRefs` does for the text channel.
 *
 * A `custom:<variant>` that matches no materialized package is a refusal, not a
 * pass-through: it would either fail the run (no strategyText) or, worse, run as
 * some other package's strategy.
 */
export function applyCodeStrategyRefs(
  strategies: Array<{ id: string; config?: Record<string, unknown> }>,
  materialized: MaterializedCodeStrategy[],
): Array<{ id: string; config?: Record<string, unknown> }> {
  if (materialized.length === 0) return strategies;
  const byId = new Map<string, MaterializedCodeStrategy>();
  for (const item of materialized) byId.set(item.strategyId, item);
  // Any `custom:<variant>` whose variant matches a materialized package — the
  // request may carry either spelling (`custom:<variant>` or the public
  // `custom:code:<variant>`).
  const match = (id: string): MaterializedCodeStrategy | undefined => {
    for (const item of materialized) {
      if (item.strategyId === id || item.publicId === id) return item;
    }
    return undefined;
  };
  return strategies.map((ref) => {
    const id = (ref?.id ?? '').trim();
    if (!id || id.toLowerCase().startsWith(FILE_STRATEGY_PREFIX)) return ref;
    const item = match(id);
    if (!item) return ref;
    return { id: item.ref };
  });
}

/** `custom:code:<variant>` for a `custom:<variant>` strategy id. */
export function defaultPublicId(strategyId: string): string {
  const variant = codeStrategyVariant(strategyId);
  return `custom:code:${variant}`;
}

/** The variant a validated `custom:<variant>` id carries. */
export function codeStrategyVariant(strategyId: string): string {
  const id = (strategyId ?? '').trim();
  if (!id.startsWith('custom:')) {
    throw new CodeStrategyError(CODE_STRATEGY_ERR_INVALID, `代码策略的 id 必须是 custom:<variant>，收到 ${id || '<空>'}。`);
  }
  const variant = id.slice('custom:'.length);
  // The same character class the BFF enforces; re-checked here because the file
  // name is derived from it. The lookahead rejects dot-only variants ("..",
  // "..."), which pass the character class but name a confusing file.
  if (!/^(?=.*[A-Za-z0-9])[A-Za-z0-9._-]{1,64}$/.test(variant)) {
    throw new CodeStrategyError(
      CODE_STRATEGY_ERR_INVALID,
      `代码策略 ${id} 的 variant 只能包含字母、数字、点、下划线与连字符。`,
    );
  }
  return variant;
}

/**
 * Reject a package before anything is written.
 *
 * Order matters: shape and safety first, disk second — a refused package must
 * leave no file behind for a later run to import.
 */
export function assertPublishableCodeStrategy(pkg: EngineCustomStrategyCode): string {
  const handle = (pkg?.handle ?? '').trim() || '<unknown>';
  const source = pkg?.definition ?? '';
  if (!source.trim()) {
    throw new CodeStrategyError(CODE_STRATEGY_ERR_INVALID, `代码策略 ${handle} 的内容为空，拒绝加载。`);
  }
  if (Buffer.byteLength(source, 'utf8') > CODE_STRATEGY_MAX_BYTES) {
    throw new CodeStrategyError(
      CODE_STRATEGY_ERR_INVALID,
      `代码策略 ${handle} 超过 ${CODE_STRATEGY_MAX_BYTES} 字节上限。`,
    );
  }
  for (const [pattern, label] of FORBIDDEN_TOKENS) {
    if (pattern.test(source)) {
      throw new CodeStrategyError(
        CODE_STRATEGY_ERR_UNSAFE,
        `代码策略 ${handle} 命中禁止项 ${label}：策略代码必须是纯函数，不得读取环境/文件、不得发起网络请求。`,
      );
    }
  }
  // The module must export `{ id, action }`; a package that does not would fail
  // inside the run, after the user paid for the probes.
  if (!/export\s+default\s*\{/.test(source) && !/export\s+const\s+/.test(source)) {
    throw new CodeStrategyError(
      CODE_STRATEGY_ERR_INVALID,
      `代码策略 ${handle} 必须使用 ESM 导出（export default { id, action }）。`,
    );
  }
  if (!/\baction\b/.test(source)) {
    throw new CodeStrategyError(CODE_STRATEGY_ERR_INVALID, `代码策略 ${handle} 缺少 action 函数。`);
  }
  codeStrategyVariant(pkg.strategy_id);
  const publicId = (pkg.public_id ?? '').trim() || defaultPublicId(pkg.strategy_id);
  if (!publicId.startsWith('custom:')) {
    throw new CodeStrategyError(
      CODE_STRATEGY_ERR_INVALID,
      `代码策略 ${handle} 的 public_id 必须是 custom:<variant>，收到 ${publicId || '<空>'}。`,
    );
  }
  return handle;
}

/** Deterministic file name for a package; derived from the variant only. */
export function codeStrategyFileName(strategyId: string): string {
  return `${codeStrategyVariant(strategyId)}.js`;
}

/** Output limits from design §4.2. */
export const CODE_STRATEGY_MAX_OUTPUT_ITEMS = 2;
export const CODE_STRATEGY_MAX_PAYLOAD_BYTES = 64 * 1024;
export const CODE_STRATEGY_MAX_TOTAL_BYTES = 2 * 1024 * 1024;

/**
 * Validate what a code strategy returned, before promptfoo grades anything.
 *
 * # Why this exists rather than "let promptfoo deal with it"
 *
 * promptfoo accepts any array of test cases and happily grades whatever comes
 * back. A strategy that returns 5000 cases, or empties `vars[injectVar]`, would
 * therefore produce a run whose report claims coverage it does not have — the
 * exact silent-narrowing shape this project keeps removing. The limits are the
 * ones in the design doc; they are deliberately generous (a transform may
 * duplicate a case) and deliberately finite.
 */
export function assertCodeStrategyOutput(
  input: Array<{ vars?: Record<string, unknown>; metadata?: Record<string, unknown> }>,
  output: unknown,
  injectVar: string,
  publicId: string,
): void {
  if (!Array.isArray(output)) {
    throw new CodeStrategyError(CODE_STRATEGY_ERR_INVALID, `代码策略 ${publicId} 的 action 必须返回用例数组。`);
  }
  const limit = Math.max(input.length * CODE_STRATEGY_MAX_OUTPUT_ITEMS, 1);
  if (output.length === 0) {
    throw new CodeStrategyError(CODE_STRATEGY_ERR_INVALID, `代码策略 ${publicId} 返回了空数组：这会让报告声称覆盖了实际没跑的用例。`);
  }
  if (output.length > limit) {
    throw new CodeStrategyError(
      CODE_STRATEGY_ERR_INVALID,
      `代码策略 ${publicId} 返回 ${output.length} 条用例，超过输入 ${input.length} 条的两倍上限。`,
    );
  }
  let total = 0;
  for (const [index, item] of output.entries()) {
    const row = item as { vars?: Record<string, unknown> } | null;
    const value = row?.vars?.[injectVar];
    if (typeof value !== 'string' || value.trim() === '') {
      throw new CodeStrategyError(
        CODE_STRATEGY_ERR_INVALID,
        `代码策略 ${publicId} 的第 ${index + 1} 条输出缺少 vars.${injectVar}。`,
      );
    }
    const bytes = Buffer.byteLength(value, 'utf8');
    if (bytes > CODE_STRATEGY_MAX_PAYLOAD_BYTES) {
      throw new CodeStrategyError(
        CODE_STRATEGY_ERR_INVALID,
        `代码策略 ${publicId} 的第 ${index + 1} 条输出 ${bytes} 字节，超过单条上限 ${CODE_STRATEGY_MAX_PAYLOAD_BYTES}。`,
      );
    }
    total += bytes;
  }
  if (total > CODE_STRATEGY_MAX_TOTAL_BYTES) {
    throw new CodeStrategyError(CODE_STRATEGY_ERR_INVALID, `代码策略 ${publicId} 的输出总量超过 ${CODE_STRATEGY_MAX_TOTAL_BYTES} 字节上限。`);
  }
}

/**
 * Write the GUARDED module the engine actually hands to promptfoo.
 *
 * # Why a wrapper file rather than the module itself
 *
 * promptfoo owns the strategy call (`applyStrategies`), so the only place the
 * engine can interpose on a code package is the file it points at. The package
 * source is therefore wrapped, not imported directly:
 *
 *     import pkg from '<module>';
 *     export default { id: '<public id>', action: guarded(pkg.action) };
 *
 * The wrapper adds the two things the platform must own and the package cannot:
 *
 *   - a wall-clock bound, because a hung `action` holds the run's only worker;
 *   - an output schema check, because promptfoo grades whatever array comes back.
 *
 * # What the bound does NOT do
 *
 * `Promise.race` cannot interrupt *synchronous* JS. A `while (true) {}` in the
 * package still hangs the worker; the timeout covers promises that never settle.
 * That limit is the price of executing in-process, and it is exactly why the
 * design refuses network/fs tokens at publication and requires tenant isolation
 * before the feature ships.
 *
 * # Why the package is copied into the workdir
 *
 * The wrapper imports a relative path, and a relative import cannot escape the
 * wrapper's directory without `../`. Copying the module next to the wrapper keeps
 * the import bare (`./<variant>.module.js`) while the original stays the path
 * recorded for diagnostics.
 */
function guardedWrapperSource(moduleFileName: string, publicId: string, injectVar: string, timeoutMs: number): string {
  return `// Generated by the evaluating platform. Do not edit.
import pkg from './${moduleFileName}';

const PUBLIC_ID = ${JSON.stringify(publicId)};
const INJECT_VAR = ${JSON.stringify(injectVar)};
const TIMEOUT_MS = ${timeoutMs};
const MAX_ITEMS_FACTOR = 2;
const MAX_PAYLOAD_BYTES = 64 * 1024;
const MAX_TOTAL_BYTES = 2 * 1024 * 1024;

export default {
  id: PUBLIC_ID,
  async action(testCases, ...rest) {
    const input = Array.isArray(testCases) ? testCases : [];
    let timer;
    const timeout = new Promise((_resolve, reject) => {
      timer = setTimeout(() => reject(new Error('代码策略 ' + PUBLIC_ID + ' 执行超过 ' + TIMEOUT_MS + 'ms')), TIMEOUT_MS);
    });
    let output;
    try {
      output = await Promise.race([Promise.resolve(pkg.action(testCases, ...rest)), timeout]);
    } finally {
      if (timer) clearTimeout(timer);
    }
    if (!Array.isArray(output)) throw new Error('代码策略 ' + PUBLIC_ID + ' 必须返回用例数组');
    if (output.length === 0) throw new Error('代码策略 ' + PUBLIC_ID + ' 返回了空数组');
    const limit = Math.max(input.length * MAX_ITEMS_FACTOR, 1);
    if (output.length > limit) throw new Error('代码策略 ' + PUBLIC_ID + ' 返回用例过多（' + output.length + ' > ' + limit + '）');
    let total = 0;
    for (let i = 0; i < output.length; i += 1) {
      const value = output[i] && output[i].vars ? output[i].vars[INJECT_VAR] : undefined;
      if (typeof value !== 'string' || value.trim() === '') {
        throw new Error('代码策略 ' + PUBLIC_ID + ' 的第 ' + (i + 1) + ' 条输出缺少 vars.' + INJECT_VAR);
      }
      const bytes = Buffer.byteLength(value, 'utf8');
      if (bytes > MAX_PAYLOAD_BYTES) throw new Error('代码策略 ' + PUBLIC_ID + ' 的第 ' + (i + 1) + ' 条输出超过单条上限');
      total += bytes;
    }
    if (total > MAX_TOTAL_BYTES) throw new Error('代码策略 ' + PUBLIC_ID + ' 的输出总量超过上限');
    return output;
  },
};
`;
}

export interface MaterializedCodeStrategy {
  /** `file://<absolute path>` — what promptfoo loads. */
  ref: string;
  /** The package's resolved `custom:<variant>` — what the request names it by. */
  strategyId: string;
  /** Absolute path, needed to map the ref back after the run. */
  filePath: string;
  /** Public id the report must show. */
  publicId: string;
}

/**
 * Write every code package into this run's workdir.
 *
 * Validates all of them first (a refused batch leaves nothing on disk), then
 * writes with 0600 — the workdir also holds the materialized generation key.
 */
export async function materializeCustomStrategyCode(
  packages: EngineCustomStrategyCode[] | undefined,
  workdir: string,
  options?: { timeoutMs?: number; injectVar?: string },
): Promise<MaterializedCodeStrategy[]> {
  if (!packages || packages.length === 0) return [];
  for (const pkg of packages) assertPublishableCodeStrategy(pkg);

  const names = new Set<string>();
  for (const pkg of packages) {
    const name = codeStrategyFileName(pkg.strategy_id);
    if (names.has(name)) {
      throw new CodeStrategyError(
        CODE_STRATEGY_ERR_INVALID,
        `代码策略 ${pkg.handle} 与本次运行中的另一个策略映射到同一文件名，拒绝加载以免互相覆盖。`,
      );
    }
    names.add(name);
  }

  const dir = path.join(workdir, CUSTOM_STRATEGY_DIR);
  await fs.mkdir(dir, { recursive: true, mode: 0o700 });
  const out: MaterializedCodeStrategy[] = [];
  const timeoutMs = options?.timeoutMs ?? 20_000;
  const injectVar = options?.injectVar ?? 'prompt';
  for (const pkg of packages) {
    // `codeStrategyFileName` already ends in `.js`; strip it for the stem so the
    // wrapper is `<variant>.js` and the package `<variant>.module.js`.
    const stem = codeStrategyFileName(pkg.strategy_id).replace(/\.js$/, '');
    const moduleFileName = `${stem}.module.js`;
    const publicId = (pkg.public_id ?? '').trim() || defaultPublicId(pkg.strategy_id);
    // 0600: the workdir also holds the materialized generation API key.
    await fs.writeFile(path.join(dir, moduleFileName), pkg.definition, { encoding: 'utf8', mode: 0o600 });
    // The wrapper is what promptfoo imports; it references the module by a bare
    // relative specifier so the package cannot walk out of the directory.
    const wrapperPath = path.join(dir, `${stem}.js`);
    await fs.writeFile(wrapperPath, guardedWrapperSource(moduleFileName, publicId, injectVar, timeoutMs), {
      encoding: 'utf8',
      mode: 0o600,
    });
    out.push({
      ref: `${FILE_STRATEGY_PREFIX}${wrapperPath}`,
      strategyId: pkg.strategy_id.trim(),
      filePath: wrapperPath,
      publicId,
    });
  }
  return out;
}

/**
 * Replace promptfoo's file:// strategy ids with the package's public id.
 *
 * `applyStrategies()` stamps the ref (the path) into `metadata.strategyId`, which
 * is what the reducer turns into `case_results[].strategy_id` and
 * `strategy_stats[].id`. Left alone, every report of a code-strategy run would
 * carry a workdir path — forbidden in run records and reports alike.
 *
 * Unknown file:// ids are mapped to `strategy_code` rather than passed through:
 * a ref this run did not materialize is not something a report should name.
 */
export function normalizeCodeStrategyIds<T extends { results?: Array<Record<string, unknown>> }>(
  evalLike: T,
  materialized: MaterializedCodeStrategy[],
): T {
  if (!evalLike?.results?.length || materialized.length === 0) return evalLike;
  const byRef = new Map<string, string>();
  for (const item of materialized) byRef.set(item.ref, item.publicId);
  for (const row of evalLike.results) {
    const testCase = row?.testCase as Record<string, unknown> | undefined;
    const metadata = (testCase?.metadata ?? row?.metadata) as Record<string, unknown> | undefined;
    if (!metadata) continue;
    const strategyId = typeof metadata.strategyId === 'string' ? metadata.strategyId.trim() : '';
    if (!strategyId.startsWith(FILE_STRATEGY_PREFIX)) continue;
    metadata.strategyId = byRef.get(strategyId) ?? 'strategy_code';
    if (testCase) testCase.metadata = metadata;
    else row.metadata = metadata;
  }
  return evalLike;
}
