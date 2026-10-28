/**
 * `POST /strategy-packages/validate` — the HTTP surface of the publication probe.
 *
 * The probe itself is unit-tested; this pins the route ADAPTER: the capability
 * switch, the input guards, and the verdict → status-code mapping the BFF
 * depends on (200 ok / 422 rejected / 409 disabled).
 */
import './u8PromptfooHome.js';

// The capability is off by default; the route tests exercise it.
process.env.MACLAW_STRATEGY_CODE_PACKAGES = '1';
import { describe, expect, it } from 'vitest';
import express from 'express';
import http from 'node:http';

import { createStrategyPackagesRouter } from '../src/routes/strategyPackages.js';

async function call(
  body: unknown,
): Promise<{ status: number; json: Record<string, unknown> }> {
  const app = express();
  app.use(express.json());
  app.use(createStrategyPackagesRouter());
  const server = http.createServer(app);
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', () => r()));
  const port = (server.address() as { port: number }).port;
  try {
    const res = await fetch(`http://127.0.0.1:${port}/strategy-packages/validate`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    });
    return { status: res.status, json: (await res.json()) as Record<string, unknown> };
  } finally {
    server.close();
  }
}

describe('POST /strategy-packages/validate', () => {
  it('returns the verdict for a loadable module', async () => {
    const { status, json } = await call({
      source: `export default { id: 'x', action(t, v) { return t.map((tc) => ({ ...tc, vars: { ...tc.vars, [v]: 'q' } })); } };`,
    });
    expect(status).toBe(200);
    expect(json.ok).toBe(true);
  });

  it('maps a rejected module to 422 with its code', async () => {
    const { status, json } = await call({ source: `export default { id: 'x' };` });
    expect(status).toBe(422);
    expect(json.ok).toBe(false);
    expect(json.code).toBe('code_strategy_bad_shape');
  });

  it('rejects a missing body without forking anything', async () => {
    const { status, json } = await call({});
    expect(status).toBe(400);
    expect(json.code).toBe('code_strategy_empty');
  });

  it('refuses when the capability is switched off', async () => {
    const key = 'MACLAW_STRATEGY_CODE_PACKAGES';
    const previous = process.env[key];
    delete process.env[key];
    try {
      const { status, json } = await call({ source: `export default { id: 'x', action() {} };` });
      expect(status).toBe(409);
      expect(json.code).toBe('code_strategy_disabled');
    } finally {
      process.env[key] = previous;
    }
  });
});
