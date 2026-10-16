/**
 * strategyBaseCases.test.ts — a selected strategy must not ALSO run the
 * unwrapped probes.
 *
 * # The defect this pins (2026-10-16)
 *
 * promptfoo applies strategies ADDITIVELY (`dist/src/index.js:17097`):
 *
 *     const finalTestCases = [...includeBasicTests ? pluginTestCases : [], ...strategyTestCases];
 *
 * So `numTests=6` with `base64` produced **12** probes — 6 with no attack
 * technique applied and 6 base64-wrapped. Measured on ai02: the report's
 * `strategy_stats` read `none: 6` beside `base64: 6` for a run the user asked to
 * be 6. Their decision: 「我不要再跑一遍没策略的」.
 *
 * The single switch is the `basic` strategy's `enabled` flag. There is no
 * `baseline` / `control` / `noStrategy` option, and `enabled:false` on any other
 * strategy is inert (both reads are gated on `strategy.id === 'basic'`).
 */
import { describe, expect, it } from 'vitest';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { OneShotCredentials } from '../src/types.js';
import {
  buildNativeRedteamConfig,
  productiveStrategyCount,
  withBasicCasesDisabled,
} from '../src/runner/nativeRedteam.js';

const BASIC = { id: 'basic', config: { enabled: false } };

function credentials(): OneShotCredentials {
  return {
    target: { base_url: 'http://127.0.0.1:1/v1', model: 'stub-model', api_key: 'sk-stub' },
    generation: { base_url: 'http://127.0.0.1:1/v1', model: 'stub-model', api_key: 'sk-gen' },
  };
}

async function tmpDir(): Promise<string> {
  return mkdtemp(join(tmpdir(), 'strategy-base-'));
}

describe('withBasicCasesDisabled', () => {
  it('disables the control group when a strategy is selected', () => {
    expect(withBasicCasesDisabled([{ id: 'base64' }])).toEqual([{ id: 'base64' }, BASIC]);
  });

  it('leaves the request untouched when no strategy is selected', () => {
    // The base cases ARE the run here. Adding `basic: false` would produce an
    // empty test set and a `no_test_cases` failure instead of an evaluation.
    expect(withBasicCasesDisabled([])).toEqual([]);
  });

  it('does not add a second entry when the caller named basic itself', () => {
    // Two entries for one id would leave promptfoo to reconcile them, and the
    // caller's own choice is the one to honour.
    expect(withBasicCasesDisabled([{ id: 'basic' }])).toEqual([{ id: 'basic' }]);
  });

  it('keeps the requested strategies, in order, ahead of the control entry', () => {
    const out = withBasicCasesDisabled([{ id: 'base64' }, { id: 'crescendo' }]);
    expect(out).toEqual([{ id: 'base64' }, { id: 'crescendo' }, BASIC]);
  });
});

describe('productiveStrategyCount', () => {
  it('counts the strategies that produce probes, not the control entry', () => {
    expect(productiveStrategyCount([{ id: 'base64' }])).toBe(1);
    expect(productiveStrategyCount([{ id: 'base64' }, { id: 'rot13' }])).toBe(2);
    // `basic` names the UNWRAPPED cases: counting it would report double the
    // probes for exactly the run where the doubling was removed.
    expect(productiveStrategyCount([{ id: 'base64' }, BASIC])).toBe(1);
    expect(productiveStrategyCount([BASIC])).toBe(1);
    expect(productiveStrategyCount([])).toBe(1);
  });
});

describe('the built config document', () => {
  it('carries the control entry when a strategy is selected', async () => {
    // The wiring has to be in the BUILDER, not at a call site: the builder is the
    // single seam that produces a config document, so a call-site wiring could be
    // deleted with the suite still green (U8's lesson).
    //
    // Counter-proof: drop the withBasicCasesDisabled call in
    // buildNativeRedteamConfig → this fails with `[{id:'base64'}]`.
    const dir = await tmpDir();
    try {
      const config = await buildNativeRedteamConfig({
        runId: 'base-cases-1',
        purpose: 'control group off',
        injectVar: 'prompt',
        numTests: 6,
        plugins: [{ id: 'contracts' }],
        strategies: [{ id: 'base64' }],
        credentials: credentials(),
        workdir: dir,
      });
      expect(config.redteam.strategies).toEqual([{ id: 'base64' }, BASIC]);
    } finally {
      await rm(dir, { recursive: true, force: true }).catch(() => undefined);
    }
  });

  it('carries no control entry when no strategy is selected', async () => {
    const dir = await tmpDir();
    try {
      const config = await buildNativeRedteamConfig({
        runId: 'base-cases-2',
        purpose: 'no strategy',
        injectVar: 'prompt',
        numTests: 6,
        plugins: [{ id: 'contracts' }],
        strategies: [],
        credentials: credentials(),
        workdir: dir,
      });
      expect(config.redteam.strategies).toEqual([]);
    } finally {
      await rm(dir, { recursive: true, force: true }).catch(() => undefined);
    }
  });
});

describe('num_tests_effective', () => {
  // The field's job (P2-4) is to say how many probes the document CONFIGURED,
  // so a reader can tell "we asked for 6 and got 12" from "we asked for 12".
  // With the control group off, one strategy configures one pass over the
  // plugin cases; two strategies configure two.
  it('multiplies by the number of productive strategies', () => {
    const single = productiveStrategyCount([{ id: 'base64' }, BASIC]);
    const double = productiveStrategyCount([{ id: 'base64' }, { id: 'rot13' }, BASIC]);
    expect(single).toBe(1);
    expect(double).toBe(2);
  });
});
