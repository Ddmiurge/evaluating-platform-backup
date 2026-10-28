/**
 * Publication-time probe for CODE strategy modules (design §6.1).
 *
 * The verdict is what the expert portal shows at publish time; it must be
 * content-free (a code and a short message), and it must be produced in a child
 * process so a hostile package cannot take the engine down while being checked.
 */
import { describe, expect, it } from 'vitest';

import { probeCodeStrategyModule } from '../src/runner/codeStrategyProbe.js';

const GOOD = `export default { id: 'x', action(t, injectVar) { return t.map((tc) => ({ ...tc, vars: { ...tc.vars, [injectVar]: 'q' } })); } };`;

describe('probeCodeStrategyModule', () => {
  it('accepts a module that loads and transforms a case', async () => {
    const result = await probeCodeStrategyModule({ source: GOOD });
    expect(result.ok).toBe(true);
  });

  it('rejects a module without an action', async () => {
    const result = await probeCodeStrategyModule({ source: `export default { id: 'x' };` });
    expect(result).toMatchObject({ ok: false, code: 'code_strategy_bad_shape' });
  });

  it('rejects an action that returns an empty array', async () => {
    const result = await probeCodeStrategyModule({
      source: `export default { id: 'x', action() { return []; } };`,
    });
    expect(result).toMatchObject({ ok: false, code: 'code_strategy_bad_output' });
  });

  it('rejects an action that drops the injection variable', async () => {
    const result = await probeCodeStrategyModule({
      source: `export default { id: 'x', action(t) { return t.map(() => ({ vars: {} })); } };`,
    });
    expect(result).toMatchObject({ ok: false, code: 'code_strategy_bad_output' });
  });

  it('rejects a module that does not parse', async () => {
    const result = await probeCodeStrategyModule({ source: `export default { oops` });
    expect(result).toMatchObject({ ok: false, code: 'code_strategy_load_failed' });
  });

  it('rejects a module that throws at import time', async () => {
    const result = await probeCodeStrategyModule({ source: `throw new Error('boom');` });
    expect(result).toMatchObject({ ok: false, code: 'code_strategy_load_failed' });
  });

  it('bounds a hung action', async () => {
    const result = await probeCodeStrategyModule({
      source: `export default { id: 'x', async action() { await new Promise(() => {}); } };`,
      timeoutMs: 1_500,
    });
    expect(result).toMatchObject({ ok: false });
    expect(['code_strategy_timeout', 'code_strategy_probe_failed']).toContain(result.code);
  });

  it('never forwards what the module printed', async () => {
    const result = await probeCodeStrategyModule({
      source: `console.log('ZZ_MODULE_NOISE 内部数据'); export default { id: 'x', action(t) { return []; } };`,
    });
    const serialized = JSON.stringify(result);
    expect(serialized).not.toContain('ZZ_MODULE_NOISE');
    expect(serialized).not.toContain('内部数据');
  });
});
