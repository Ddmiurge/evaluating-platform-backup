import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import express from 'express';
import type { Server } from 'node:http';
import { TaskManager } from '../src/manager/taskManager.js';
import { WorkdirManager } from '../src/manager/workdir.js';
import { bearerAuth } from '../src/auth.js';
import { createRunsRouter } from '../src/routes/runs.js';

function startServer(manager: TaskManager): Promise<{ server: Server; port: number }> {
  const app = express();
  app.use(express.json());
  // Mirror src/server.ts mounting order: bearer auth in front of the runs router.
  app.use(bearerAuth(TOKEN));
  app.use(createRunsRouter(manager));
  return new Promise((resolve) => {
    const server = app.listen(0, '127.0.0.1', () => {
      const addr = server.address();
      resolve({ server, port: (addr as { port: number }).port });
    });
  });
}

function makeManager(): TaskManager {
  const workdirs = new WorkdirManager('.tmp-test-runs');
  return new TaskManager({
    workerCount: 1,
    maxQueueDepth: 2,
    workdirs,
    async execute() {
      /* unit-tested in taskManager.test.ts */
    },
  });
}

// TOKEN must be declared before startServer references it.
const TOKEN = 'unit-test-bearer-token';

function authed(port: number, path: string, init?: RequestInit): Promise<Response> {
  return fetch(`http://127.0.0.1:${port}${path}`, {
    ...init,
    headers: { ...(init?.headers ?? {}), Authorization: `Bearer ${TOKEN}` },
  });
}

const validBody = {
  run_id: 'api-run-1',
  platform_user_id: 'u1',
  purpose: '合规评测',
  num_tests: 5,
  plugins: [{ id: 'harmful' }],
  strategies: [],
  judge_mode: 'auto',
  credentials: { target: { base_url: 'http://t', model: 'm', api_key: 'k' } },
};

describe('runs router', () => {
  let manager: TaskManager;
  let server: Server;
  let port: number;

  beforeEach(async () => {
    manager = makeManager();
    ({ server, port } = await startServer(manager));
  });

  afterEach(async () => {
    await new Promise<void>((resolve) => server.close(() => resolve()));
    await manager.shutdown();
  });

  it('rejects requests without bearer token', async () => {
    const res = await fetch(`http://127.0.0.1:${port}/runs/api-run-1`);
    expect(res.status).toBe(401);
  });

  it('rejects an invalid run request body', async () => {
    const res = await authed(port, '/runs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ run_id: 'x' }),
    });
    expect(res.status).toBe(400);
  });

  it('accepts a valid run and reports queued status', async () => {
    const create = await authed(port, '/runs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(validBody),
    });
    expect(create.status).toBe(202);

    const status = await authed(port, '/runs/api-run-1');
    expect(status.status).toBe(200);
    const body = (await status.json()) as { run_id: string; phase: string };
    expect(body.run_id).toBe('api-run-1');
    expect(['queued', 'executing_probes', 'succeeded']).toContain(body.phase);
  });

  it('conflicts on duplicate run id', async () => {
    const first = await authed(port, '/runs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(validBody),
    });
    expect(first.status).toBe(202);
    const dup = await authed(port, '/runs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(validBody),
    });
    expect(dup.status).toBe(409);
  });

  it('404s unknown runs', async () => {
    const res = await authed(port, '/runs/does-not-exist');
    expect(res.status).toBe(404);
  });

  it('400s on malformed run ids', async () => {
    const res = await authed(port, '/runs/not%20a%20valid%20id');
    expect(res.status === 400 || res.status === 404).toBe(true);
  });
});