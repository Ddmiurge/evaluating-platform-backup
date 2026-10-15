/**
 * P3-5 · Plan A is gone, not half-gone.
 *
 * Deleting a generation path touches a dispatcher, a switch, a set of exports, a
 * `planAOnly` classification and a handful of tests. The failure mode this guard
 * exists for is the one that looks like success: the *behaviour* moves to the
 * native path while some of the Plan A scaffolding stays behind — an unused
 * export, a branch that can never be taken, a switch an operator can still set and
 * believe it does something. This repo has already been bitten by that shape once
 * (`client_legacy.go` was assumed to be dead code and was load-bearing).
 *
 * So the assertion is source-level: none of these identifiers may appear anywhere
 * under `src/`, because each of them only ever existed to serve Plan A or to choose
 * between the two paths.
 *
 * Counter-proof: add `export function planAOnlyStrategyIds() {}` to any file → red.
 */
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

/** Identifiers that only ever existed for Plan A or for choosing between paths. */
const PLAN_A_SYMBOLS = [
  'generateAttackTests',
  'executePlanARedteamRun',
  'planAOnly',
  'PLAN_A_ONLY',
  'PLAN_A_ERR',
  'assertNoCustomPluginsOnPlanA',
  'assertNoCustomStrategiesOnPlanA',
  'nativeGenerationEnabled',
  'NATIVE_GENERATION_SWITCH',
  'assertNativeGenerationInvariants',
  'effectiveWorkerCount',
];

/**
 * Strip comments before matching.
 *
 * The distinction matters in both directions. A comment that NAMES the thing that
 * was removed is documentation worth keeping — several of them explain why the
 * code is shorter now. A comment is not a live symbol. So the guard judges code,
 * by dropping full-line comments and trailing `//` tails (the same "drop comment
 * lines first" discipline the catalog generators use, and for the same reason: a
 * bare substring search reads prose as code).
 */
function codeOnly(source: string): string {
  return source
    .split('\n')
    .map((line) => {
      const trimmed = line.trim();
      if (trimmed.startsWith('//') || trimmed.startsWith('*') || trimmed.startsWith('/*')) return '';
      const commentAt = line.indexOf('//');
      // Only a trailing comment if it is not inside something URL-ish; identifiers
      // are what we match, so an over-eager cut is safe (it can only hide text we do
      // not want to match anyway).
      return commentAt >= 0 ? line.slice(0, commentAt) : line;
    })
    .join('\n');
}

function collectTypeScriptFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) collectTypeScriptFiles(full, out);
    else if (entry.endsWith('.ts')) out.push(full);
  }
  return out;
}

describe('Plan A removal (P3-5)', () => {
  const srcDir = fileURLToPath(new URL('../src', import.meta.url));
  const files = collectTypeScriptFiles(srcDir);

  it('found the source tree', () => {
    // A guard that scans nothing passes trivially, which would be worse than no
    // guard: it would report "clean" for a moved directory.
    expect(files.length).toBeGreaterThan(10);
    expect(files.some((f) => f.endsWith('server.ts'))).toBe(true);
  });

  it.each(PLAN_A_SYMBOLS)('leaves no reference to %s in src/', (symbol) => {
    const offenders = files.filter((file) => codeOnly(readFileSync(file, 'utf8')).includes(symbol));
    expect(
      offenders.map((f) => f.slice(srcDir.length + 1)),
      `${symbol} still appears — unless a plan explicitly kept it, it is a leftover of the deleted path`,
    ).toEqual([]);
  });

  it('no longer exports a path switch through the env module', () => {
    // The switch was the only reason `loadEnv` had a `nativeGeneration` field.
    const env = codeOnly(readFileSync(join(srcDir, 'env.ts'), 'utf8'));
    expect(env).not.toContain('nativeGeneration:');
  });
});
