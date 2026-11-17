/**
 * E-4d-5 · hot-load run e2e (design §14.4/14.5, local half).
 *
 * Boots the REAL engine server against fake in-process target + generation
 * LLMs — same harness as smoke.e2e — with PROMPTFOO_EMBEDDED_PLUGINS_DIR
 * pointed at a temp catalog we control, then drives the full hot path:
 *
 *   boot with batch-1 → rewrite index.json → SIGHUP → receipt batch-2
 *   → submit a run selecting the HOT plugin by its PUBLIC id
 *   → run succeeds → result carries no file:// synthetic key anywhere
 *   → busy refusal: SIGHUP during the run is refused, run unaffected,
 *     SIGHUP after the run applies.
 *
 * Requires the installed promptfoo dist to carry the Route B + E-4d patch
 * (the Docker build patches it; a fresh checkout is patched on demand here —
 * the same idempotent script the image build runs).
 */
import { afterEach, describe, expect, it } from 'vitest';
import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { readFileSync } from 'node:fs';
import http from 'node:http';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const TOKEN = 'hot-load-e2e-token-0123456789abcdef';
const PORT = 18107;
const ENGINE_DIR = resolve(fileURLToPath(import.meta.url), '../..');
const INDEX_PATH = join(ENGINE_DIR, 'node_modules/promptfoo/dist/src/index.js');
const HOT_ID = 'p28-hot-e2e';
const BAKED_ID = 'p28-baked-e2e';

let engineProc: ChildProcess | undefined;
let fakeTarget: http.Server | undefined;
let fakeGeneration: http.Server | undefined;
let engineRoot = '';
let promptfooHome = '';
let embeddedDir = '';

const temps: string[] = [];
afterEach(async () => {
  engineProc?.kill('SIGKILL');
  engineProc = undefined;
  await Promise.all([
    new Promise((r) => (fakeTarget ? fakeTarget.close(() => r(undefined)) : r(undefined))),
    new Promise((r) => (fakeGeneration ? fakeGeneration.close(() => r(undefined)) : r(undefined))),
  ]);
  fakeTarget = undefined;
  fakeGeneration = undefined;
  for (const dir of temps.splice(0)) {
    await rm(dir, { recursive: true, force: true });
  }
});

/**
 * The image build runs this script; a fresh checkout needs it too — and so
 * does a checkout patched by an OLDER revision of the script: the marker
 * alone is not enough, the reload hook itself must be present (a stale block
 * would boot with embedded_batch=null and every receipt would read
 * not_patched).
 */
function ensurePatched(): void {
  const src = readFileSync(INDEX_PATH, 'utf8');
  if (src.includes('globalThis.__p28Embedded') && src.includes('__p28SchemaApi')) return;
  const res = spawnSync(process.execPath, ['scripts/patch-embedded-plugins.mjs'], {
    cwd: ENGINE_DIR,
    encoding: 'utf8',
  });
  expect(res.status, res.stderr).toBe(0);
}

async function writeCatalog(batchID: string, withHot: boolean): Promise<void> {
  const packages: Array<{ id: string; entry: string }> = [{ id: BAKED_ID, entry: 'src/plugin.mjs' }];
  if (withHot) packages.push({ id: HOT_ID, entry: 'src/plugin.mjs' });
  await writeFile(join(embeddedDir, 'index.json'), JSON.stringify({ batch_id: batchID, packages }), 'utf8');
}

async function writePlugin(id: string, payload: string): Promise<void> {
  const dir = join(embeddedDir, id, 'src');
  await mkdir(dir, { recursive: true });
  await writeFile(
    join(dir, 'plugin.mjs'),
    [
      'export default {',
      '  async action(params) {',
      `    return [{ vars: { [params.injectVar]: ${JSON.stringify(payload)} }, assert: [{ type: "contains", value: "抱歉" }] }];`,
      '  },',
      '};',
    ].join('\n'),
    'utf8',
  );
}

/** Delayed target: keeps a run in flight long enough to probe busy refusal. */
async function startFakeTarget(delayMs: number): Promise<number> {
  return new Promise((resolvePort) => {
    fakeTarget = http.createServer((req, res) => {
      let body = '';
      req.on('data', (c: Buffer) => (body += c.toString()));
      req.on('end', () => {
        setTimeout(() => {
          res.setHeader('content-type', 'application/json');
          res.end(
            JSON.stringify({
              choices: [{ message: { role: 'assistant', content: '抱歉，我不能提供这类内容。' } }],
            }),
          );
        }, delayMs);
      });
    });
    fakeTarget.listen(0, '127.0.0.1', () => resolvePort((fakeTarget!.address() as AddressInfo).port));
  });
}

/** Fake generation LLM: entity extraction + rubric grading shapes (from smoke). */
async function startFakeGeneration(): Promise<number> {
  return new Promise((resolvePort) => {
    fakeGeneration = http.createServer((req, res) => {
      let body = '';
      req.on('data', (c: Buffer) => (body += c.toString()));
      req.on('end', () => {
        res.setHeader('content-type', 'application/json');
        if (body.includes('Extract only real-world entities')) {
          res.end(
            JSON.stringify({
              choices: [{ message: { role: 'assistant', content: JSON.stringify({ entities: [], purpose: 'hot-load' }) } }],
            }),
          );
          return;
        }
        res.end(
          JSON.stringify({
            choices: [
              {
                message: {
                  role: 'assistant',
                  content: JSON.stringify({ pass: true, score: 1, reason: 'fake grader' }),
                },
              },
            ],
          }),
        );
      });
    });
    fakeGeneration.listen(0, '127.0.0.1', () => resolvePort((fakeGeneration!.address() as AddressInfo).port));
  });
}

function startEngine(): Promise<void> {
  return new Promise((resolveBoot, reject) => {
    engineProc = spawn(process.execPath, ['--import', 'tsx', 'src/server.ts'], {
      cwd: ENGINE_DIR,
      env: {
        ...process.env,
        PROMPTFOO_DISABLE_REMOTE_GENERATION: '1',
        PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION: '1',
        PROMPTFOO_DISABLE_TELEMETRY: '1',
        PROMPTFOO_DISABLE_SHARE: '1',
        PROMPTFOO_CACHE_ENABLED: '0',
        PROMPTFOO_ENGINE_BEARER_TOKEN: TOKEN,
        PROMPTFOO_ENGINE_DATA_ROOT: engineRoot,
        PROMPTFOO_ENGINE_PORT: String(PORT),
        PROMPTFOO_ENGINE_WORKERS: '1',
        PROMPTFOO_ENGINE_DEBUG: '1',
        PROMPTFOO_CONFIG_DIR: promptfooHome,
        // The catalog this test owns: boot reads batch-1, reload reads batch-2/3.
        PROMPTFOO_EMBEDDED_PLUGINS_DIR: embeddedDir,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    engineProc.stdout?.on('data', (d: Buffer) => process.stdout.write('[engine] ' + d.toString()));
    engineProc.stderr?.on('data', (d: Buffer) => process.stderr.write('[engine-err] ' + d.toString()));
    const started = Date.now();
    const tryConnect = async (): Promise<void> => {
      if (Date.now() - started > 90000) {
        reject(new Error('engine did not become healthy within 90s'));
        return;
      }
      try {
        const res = await fetch(`http://127.0.0.1:${PORT}/health`, { signal: AbortSignal.timeout(2000) });
        if (res.ok) {
          resolveBoot();
          return;
        }
      } catch {
        // not up yet
      }
      setTimeout(() => void tryConnect(), 500);
    };
    void tryConnect();
  });
}

async function health(): Promise<{
  running?: number;
  embedded_batch?: string | null;
  embedded_reload?: { ok: boolean; reason: string; batch: string | null } | null;
}> {
  const res = await fetch(`http://127.0.0.1:${PORT}/health`);
  expect(res.status).toBe(200);
  return (await res.json()) as never;
}

async function api(path: string, init?: RequestInit): Promise<Response> {
  return fetch(`http://127.0.0.1:${PORT}${path}`, {
    ...init,
    headers: {
      authorization: `Bearer ${TOKEN}`,
      'content-type': 'application/json',
      ...(init?.headers ?? {}),
    },
  });
}

async function waitForHealth(predicate: (h: Awaited<ReturnType<typeof health>>) => boolean, ms: number): Promise<Awaited<ReturnType<typeof health>>> {
  const deadline = Date.now() + ms;
  let last: Awaited<ReturnType<typeof health>> = {};
  while (Date.now() < deadline) {
    try {
      last = await health();
      if (predicate(last)) return last;
    } catch {
      // engine briefly unavailable — keep polling
    }
    await new Promise((r) => setTimeout(r, 400));
  }
  return last;
}

describe('hot-load run e2e (E-4d-5 local)', () => {
  it(
    'reloads over SIGHUP, runs a hot plugin by its public id, refuses busy, normalizes the report',
    { timeout: 300000 },
    async () => {
      ensurePatched();

      engineRoot = await mkdtemp(join(tmpdir(), 'engine-hot-'));
      promptfooHome = await mkdtemp(join(tmpdir(), 'engine-hot-pfhome-'));
      embeddedDir = await mkdtemp(join(tmpdir(), 'engine-hot-catalog-'));
      temps.push(engineRoot, promptfooHome, embeddedDir);

      await writeCatalog('batch-e2e-1', false);
      await writePlugin(BAKED_ID, '烘焙插件载荷');
      await writePlugin(HOT_ID, '热加载探针攻击提示');

      const targetPort = await startFakeTarget(6000); // slow target → run stays in flight
      const generationPort = await startFakeGeneration();
      await startEngine();

      // ── boot: batch-1 loaded from the temp catalog ──
      const boot = await health();
      expect(boot.embedded_batch).toBe('batch-e2e-1');

      // ── hot add: rewrite the marker, SIGHUP, receipt must echo batch-2 ──
      await writeCatalog('batch-e2e-2', true);
      engineProc!.kill('SIGHUP');
      const reloaded = await waitForHealth((h) => h.embedded_batch === 'batch-e2e-2', 15000);
      expect(reloaded.embedded_batch).toBe('batch-e2e-2');
      expect(reloaded.embedded_reload?.ok).toBe(true);

      // ── run the HOT plugin by its PUBLIC id ──
      const create = await api('/runs', {
        method: 'POST',
        body: JSON.stringify({
          run_id: 'hot-1',
          platform_user_id: 'hot-user',
          purpose: '热加载插件端到端验证',
          num_tests: 1,
          plugins: [{ id: HOT_ID }],
          strategies: [],
          judge_mode: 'promptfoo_native',
          credentials: {
            target: {
              base_url: `http://127.0.0.1:${targetPort}/v1/chat/completions`,
              model: 'fake-model',
              api_key: 'sk-fake',
              timeout_seconds: 20,
            },
            generation: {
              base_url: `http://127.0.0.1:${generationPort}/v1`,
              model: 'fake-gen-model',
              api_key: 'sk-gen',
            },
          },
        }),
      });
      expect([202, 409]).toContain(create.status);

      // ── busy refusal (§14.5-3): while the run is in flight, a reload is
      //    refused with the static reason and the registry is untouched ──
      const inFlight = await waitForHealth((h) => (h.running ?? 0) > 0, 30000);
      expect(inFlight.running ?? 0).toBeGreaterThan(0);
      await writeCatalog('batch-e2e-3', true);
      engineProc!.kill('SIGHUP');
      const busy = await waitForHealth((h) => h.embedded_reload?.reason === 'busy', 10000);
      expect(busy.embedded_reload?.reason).toBe('busy');
      expect(busy.embedded_batch, 'busy refusal must not advance the loaded batch').toBe('batch-e2e-2');

      // ── terminal: run succeeded; hot key never leaked into the result ──
      let finalStatus: Record<string, unknown> | undefined;
      const deadline = Date.now() + 180000;
      while (Date.now() < deadline) {
        const st = await api('/runs/hot-1');
        expect(st.status).toBe(200);
        const j = (await st.json()) as Record<string, unknown>;
        finalStatus = j;
        if (['succeeded', 'failed', 'canceled'].includes(String(j.phase))) break;
        await new Promise((r) => setTimeout(r, 2000));
      }
      expect(finalStatus?.phase).toBe('succeeded');

      const dumped = JSON.stringify(finalStatus);
      // §14.5-1: the synthetic key is engine-internal — no report field may
      // carry it (this is the normalization assertion, not a nicety).
      expect(dumped).not.toContain('file://p28-hot');
      expect(dumped).not.toContain('sk-fake');
      expect(dumped).not.toContain('sk-gen');

      const result = (finalStatus ?? {}) as {
        planned_count?: number;
        executed_count?: number;
        result?: {
          totals?: { probes?: number };
          plugin_stats?: Array<{ plugin_id: string }>;
          case_results?: Array<{ prompt?: string }>;
        };
      };
      expect(result.result?.totals?.probes).toBe(1);
      const statsIds = (result.result?.plugin_stats ?? []).map((s) => s.plugin_id);
      expect(statsIds).toContain(HOT_ID);
      expect(result.result?.case_results?.[0]?.prompt).toContain('热加载探针攻击提示');

      // ── after the run: the pending batch-3 applies (busy → retry) ──
      engineProc!.kill('SIGHUP');
      const after = await waitForHealth((h) => h.embedded_batch === 'batch-e2e-3', 15000);
      expect(after.embedded_batch).toBe('batch-e2e-3');
      expect(after.embedded_reload?.ok).toBe(true);
    },
  );
});
