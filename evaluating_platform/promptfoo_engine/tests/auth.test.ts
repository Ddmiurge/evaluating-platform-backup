import { describe, expect, it } from 'vitest';
import express from 'express';
import request from 'supertest';
import { bearerAuth } from '../src/auth.ts';

const TOKEN = 'unit-test-bearer-token-0123456789';

function app(): express.Express {
  const a = express();
  a.use(bearerAuth(TOKEN));
  a.get('/ping', (_req, res) => res.json({ ok: true }));
  return a;
}

describe('bearerAuth', () => {
  it('accepts the correct token', async () => {
    const res = await request(app()).get('/ping').set('Authorization', `Bearer ${TOKEN}`);
    expect(res.status).toBe(200);
  });

  it('rejects missing header with 401', async () => {
    const res = await request(app()).get('/ping');
    expect(res.status).toBe(401);
  });

  it('rejects wrong token with 401', async () => {
    const res = await request(app()).get('/ping').set('Authorization', 'Bearer wrong-token-value');
    expect(res.status).toBe(401);
  });

  it('rejects prefix-only header', async () => {
    const res = await request(app()).get('/ping').set('Authorization', 'Bearer ');
    expect(res.status).toBe(401);
  });

  it('rejects non-bearer schemes', async () => {
    const res = await request(app()).get('/ping').set('Authorization', `Basic ${TOKEN}`);
    expect(res.status).toBe(401);
  });
});
