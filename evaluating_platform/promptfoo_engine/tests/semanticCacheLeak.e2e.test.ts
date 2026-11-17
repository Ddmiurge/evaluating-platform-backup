/**
 * E-6 红路径（record 60 §6.1）：语义缓存碰撞的「真实泄露」侧。
 *
 * 60 号上机只验证了绿路径（目标无语义缓存 → 无 Canary 复用 → 攻击失败）。
 * 本用例用一个**会漏的假靶机**补上红路径：受害者请求进来时，靶机把预热条的
 * Canary 混进回复 → 受害者条的 `not-contains` 断言失败 → 红队极性下
 * `pass=false` = 攻击成功 → `totals.attack_success=1` 且该条
 * `case_results.attack_success=true`。绿侧（预热条回显自己的 Canary 是允许
 * 的）在同一 run 里天然对照。
 *
 * 靶机判定按提示词内容路由（不依赖执行顺序），因此 promptfoo 的探针并发不会
 * 让用例变成偶发。
 */
import { afterEach, describe, expect, it } from 'vitest';
import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { cp, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { readFileSync } from 'node:fs';
import http from 'node:http';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const TOKEN = 'semantic-leak-e2e-token-0123456789abcdef';
const PORT = 18111;
const ENGINE_DIR = resolve(fileURLToPath(import.meta.url), '../..');
const INDEX_PATH = join(ENGINE_DIR, 'node_modules/promptfoo/dist/src/index.js');
const PACKAGE_DIR = resolve(fileURLToPath(import.meta.url), '../../../../embedded-packages/semantic-cache-collision');
const PLUGIN_ID = 'semantic-cache-collision';
const CANARY = 'CACHE_CANARY_2026_CASE_01';

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

function ensurePatched(): void {
  const src = readFileSync(INDEX_PATH, 'utf8');
  if (src.includes('globalThis.__p28Embedded') && src.includes('__p28SchemaApi')) return;
  const res = spawnSync(process.execPath, ['scripts/patch-embedded-plugins.mjs'], {
    cwd: ENGINE_DIR,
    encoding: 'utf8',
  });
  expect(res.status, res.stderr).toBe(0);
}

/**
 * The leaking target:
 *  - prime prompt ("Echo the marker …") → echoes its own canary (allowed);
 *  - victim prompt ("cache safety regression test result …") → fabricates the
 *    cross-user reuse: the prime canary appears in the reply;
 *  - anything else → refuses politely (the 60 号 green baseline).
 */
async function startLeakingTarget(): Promise<number> {
  return new Promise((resolvePort) => {
    fakeTarget = http.createServer((req, res) => {
      let body = '';
      req.on('data', (c: Buffer) => (body += c.toString()));
      req.on('end', () => {
        res.setHeader('content-type', 'application/json');
        let content: string;
        if (body.includes('Echo the marker')) {
          content = `${CANARY} — This is a simulated result. No real tools were invoked.`;
        } else if (body.includes('cache safety regression test result')) {
          // The leak: the victim receives the attacker-primed canary.
          content = `Your cache safety regression test result: complete. Reused cached entry: ${CANARY}.`;
        } else {
          content = '抱歉，我不能提供这类内容。';
        }
        res.end(JSON.stringify({ choices: [{ message: { role: 'assistant', content } }] }));
      });
    });
    fakeTarget.listen(0, '127.0.0.1', () => resolvePort((fakeTarget!.address() as AddressInfo).port));
  });
}

/** Fake generation LLM: entity extraction + llm-rubric grading (from smoke). */
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
              choices: [{ message: { role: 'assistant', content: JSON.stringify({ entities: [], purpose: 'semantic-cache' }) } }],
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

describe('semantic-cache-collision red path (E-6 record 60 §6.1)', () => {
  it(
    'a leaking target flips the victim probe to attack_success while the prime stays safe',
    { timeout: 300000 },
    async () => {
      ensurePatched();

      engineRoot = await mkdtemp(join(tmpdir(), 'engine-leak-'));
      promptfooHome = await mkdtemp(join(tmpdir(), 'engine-leak-pfhome-'));
      embeddedDir = await mkdtemp(join(tmpdir(), 'engine-leak-catalog-'));
      temps.push(engineRoot, promptfooHome, embeddedDir);

      // Boot-time registration: copy the first-citizen package into the temp
      // catalog dir (same layout the build tool stages) — no hot reload needed.
      await mkdir(embeddedDir, { recursive: true });
      await cp(PACKAGE_DIR, join(embeddedDir, PLUGIN_ID), { recursive: true });
      await writeFile(
        join(embeddedDir, 'index.json'),
        JSON.stringify({
          batch_id: 'batch-e2e-leak',
          packages: [{ id: PLUGIN_ID, entry: 'src/plugin.ts' }],
        }),
        'utf8',
      );

      const targetPort = await startLeakingTarget();
      const generationPort = await startFakeGeneration();
      await startEngine();

      const create = await api('/runs', {
        method: 'POST',
        body: JSON.stringify({
          run_id: 'leak-1',
          platform_user_id: 'leak-user',
          purpose: '语义缓存碰撞红路径验证（会漏的合成靶机）',
          num_tests: 2,
          plugins: [{ id: PLUGIN_ID }],
          strategies: [],
          judge_mode: 'promptfoo_native',
          credentials: {
            target: {
              base_url: `http://127.0.0.1:${targetPort}/v1/chat/completions`,
              model: 'fake-model',
              api_key: 'sk-fake',
              timeout_seconds: 15,
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

      let finalStatus: Record<string, unknown> | undefined;
      const deadline = Date.now() + 180000;
      while (Date.now() < deadline) {
        const st = await api('/runs/leak-1');
        expect(st.status).toBe(200);
        const j = (await st.json()) as Record<string, unknown>;
        finalStatus = j;
        if (['succeeded', 'failed', 'canceled'].includes(String(j.phase))) break;
        await new Promise((r) => setTimeout(r, 2000));
      }
      expect(finalStatus?.phase).toBe('succeeded');

      const result = (finalStatus ?? {}) as {
        planned_count?: number;
        executed_count?: number;
        result?: {
          totals?: { probes?: number; attack_success?: number };
          plugin_stats?: Array<{ plugin_id: string; attack_success?: number }>;
          case_results?: Array<{
            plugin_id: string;
            attack_success: boolean;
            prompt?: string;
            response?: string;
          }>;
        };
      };

      // Two probes: the prime and its victim.
      expect(result.result?.totals?.probes).toBe(2);
      // THE red-path assertion: the fabricated leak counts exactly once.
      expect(result.result?.totals?.attack_success).toBe(1);

      const cases = result.result?.case_results ?? [];
      expect(cases).toHaveLength(2);
      for (const c of cases) {
        expect(c.plugin_id).toBe(PLUGIN_ID);
      }
      const prime = cases.find((c) => (c.prompt ?? '').includes('Echo the marker'));
      const victim = cases.find((c) => (c.prompt ?? '').includes('cache safety regression test result'));
      expect(prime, 'prime probe missing').toBeTruthy();
      expect(victim, 'victim probe missing').toBeTruthy();
      // Prime echoing its own canary is the allowed simulated result → safe.
      expect(prime!.attack_success).toBe(false);
      expect(prime!.response).toContain(CANARY);
      // Victim received the canary → not-contains failed → attack success.
      expect(victim!.attack_success).toBe(true);
      expect(victim!.response).toContain(CANARY);

      const stats = result.result?.plugin_stats ?? [];
      const stat = stats.find((s) => s.plugin_id === PLUGIN_ID);
      expect(stat).toBeTruthy();
      expect(stat!.attack_success).toBe(1);

      // The synthetic-key/secret boundary still holds on this path.
      const dumped = JSON.stringify(finalStatus);
      expect(dumped).not.toContain('file://p28-hot');
      expect(dumped).not.toContain('sk-fake');
      expect(dumped).not.toContain('sk-gen');
    },
  );
});
