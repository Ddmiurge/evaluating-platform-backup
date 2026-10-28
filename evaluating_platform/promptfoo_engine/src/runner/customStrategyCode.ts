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

export interface MaterializedCodeStrategy {
  /** `file://<absolute path>` — what promptfoo loads. */
  ref: string;
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
  for (const pkg of packages) {
    const filePath = path.join(dir, codeStrategyFileName(pkg.strategy_id));
    await fs.writeFile(filePath, pkg.definition, { encoding: 'utf8', mode: 0o600 });
    out.push({ ref: `${FILE_STRATEGY_PREFIX}${filePath}`, filePath, publicId: (pkg.public_id ?? '').trim() || defaultPublicId(pkg.strategy_id) });
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
