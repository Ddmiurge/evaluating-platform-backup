/**
 * Smoke test: boot the real engine server against fake in-process HTTP
 * target + generation LLM, submit a real run through promptfoo (Plan A:
 * engine-side generation feeding promptfoo evaluate), and watch it reach a
 * terminal state.
 *
 * Validates: env assertions, bearer auth, task manager, REST + status polling,
 * generation + evaluate wiring, reduceEvalToSafeResult — all against the real
 * promptfoo library, without any external network LLM access.
 */
import { afterEach, describe, expect, it } from 'vitest';
import { spawn, type ChildProcess } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import http from 'node:http';
import type { AddressInfo } from 'node:net';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const TOKEN = 'smoke-test-bearer-token-0123456789abcdef';
const PORT = 18099;
const ENGINE_DIR = resolve(fileURLToPath(import.meta.url), '../..');

let engineProc: ChildProcess | undefined;
let fakeTarget: http.Server | undefined;
let fakeGeneration: http.Server | undefined;
let engineRoot = '';
let promptfooHome = '';

async function startFakeTarget(): Promise<number> {
  return new Promise((resolve) => {
    fakeTarget = http.createServer((req, res) => {
      let body = '';
      req.on('data', (c: Buffer) => (body += c.toString()));
      req.on('end', () => {
        // Always refuse politely — model never complies with attacks.
        res.setHeader('content-type', 'application/json');
        res.end(
          JSON.stringify({
            choices: [{ message: { role: 'assistant', content: '抱歉，我不能提供这类内容。' } }],
          }),
        );
      });
    });
    fakeTarget.listen(0, '127.0.0.1', () => {
      resolve((fakeTarget!.address() as AddressInfo).port);
    });
  });
}

/**
 * Fake OpenAI-compatible generation LLM. Serves two roles routed by body:
 *  1. engine-side attack generation (our system prompt contains 红队专家)
 *     → returns a JSON array of attack test cases;
 *  2. llm-rubric grading (promptfoo's grading prompt)
 *     → returns {pass:true} so the run stays safe-by-default.
 */
async function startFakeGeneration(): Promise<number> {
  return new Promise((resolve) => {
    fakeGeneration = http.createServer((req, res) => {
      let body = '';
      req.on('data', (c: Buffer) => (body += c.toString()));
      req.on('end', () => {
        res.setHeader('content-type', 'application/json');
        // P3-3：假生成 LLM 现在必须回答 **promptfoo 原生生成**的请求形状（Plan A 的
        // 「红队专家」提示词已随 Plan A 删除）。三种形状由 promptfoo 自己决定，实测抓取：
        //   1. 实体提取 → 返回 {entities, purpose}
        //   2. 「Each line must begin with…」类模板 → 返回 `Prompt:` 行
        //   3. 要求 JSON 数组的模板 → 返回 [{__prompt}]
        if (body.includes('Extract only real-world entities')) {
          res.end(
            JSON.stringify({
              choices: [{ message: { role: 'assistant', content: JSON.stringify({ entities: [], purpose: 'smoke' }) } }],
              usage: { prompt_tokens: 20, completion_tokens: 10, total_tokens: 30 },
            }),
          );
          return;
        }
        if (body.includes('Each line must begin with the string')) {
          res.end(
            JSON.stringify({
              choices: [{ message: { role: 'assistant', content: 'Prompt: 冒烟测试攻击提示 one' } }],
              usage: { prompt_tokens: 30, completion_tokens: 12, total_tokens: 42 },
            }),
          );
          return;
        }
        if (body.includes('"__prompt"')) {
          res.end(
            JSON.stringify({
              choices: [
                {
                  message: {
                    role: 'assistant',
                    content: JSON.stringify([
                      { __prompt: '冒烟测试攻击提示 one' },
                      { __prompt: '冒烟测试攻击提示 two' },
                    ]),
                  },
                },
              ],
              usage: { prompt_tokens: 30, completion_tokens: 12, total_tokens: 42 },
            }),
          );
          return;
        }
        // llm-rubric grading → the fake target always refuses, so PASS.
        res.end(
          JSON.stringify({
            choices: [
              {
                message: {
                  role: 'assistant',
                  content: JSON.stringify({ pass: true, score: 1, reason: 'fake grader: output is safe' }),
                },
              },
            ],
            usage: { prompt_tokens: 200, completion_tokens: 20, total_tokens: 220 },
          }),
        );
      });
    });
    fakeGeneration.listen(0, '127.0.0.1', () => {
      resolve((fakeGeneration!.address() as AddressInfo).port);
    });
  });
}

function startEngine(): Promise<void> {
  return new Promise((resolve, reject) => {
    engineProc = spawn(process.execPath, ['--import', 'tsx', 'src/server.ts'], {
      cwd: ENGINE_DIR,
      env: {
        ...process.env,
        PROMPTFOO_DISABLE_REMOTE_GENERATION: '1',
        PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION: '1',
        PROMPTFOO_DISABLE_TELEMETRY: '1',
        PROMPTFOO_DISABLE_SHARE: '1',
        PROMPTFOO_ENGINE_BEARER_TOKEN: TOKEN,
        PROMPTFOO_ENGINE_DATA_ROOT: engineRoot,
        PROMPTFOO_ENGINE_PORT: String(PORT),
        PROMPTFOO_ENGINE_WORKERS: '1',
        PROMPTFOO_ENGINE_DEBUG: '1',
        // P3-3：native 路径会把迁移落在 promptfoo 的配置目录、并在终态清空它。把子进程
        // 指向临时目录，这样冒烟测试既不会污染开发者自己的 `~/.promptfoo`，也不会删掉
        // 不是它创建的 state。`assertNoRemoteGenerationConsent` 允许这么做（它拒绝的
        // 是目录里存在 auth.json，即 Promptfoo Cloud 登录）。
        PROMPTFOO_CONFIG_DIR: promptfooHome,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    engineProc.stdout?.on('data', (d: Buffer) => {
      process.stdout.write('[engine] ' + d.toString());
    });
    engineProc.stderr?.on('data', (d: Buffer) => {
      process.stderr.write('[engine-err] ' + d.toString());
    });
    // Poll /health until the engine is up (tsx cold-load can take a while).
    const started = Date.now();
    const tryConnect = async (): Promise<void> => {
      if (Date.now() - started > 90000) {
        reject(new Error('engine did not become healthy within 90s'));
        return;
      }
      try {
        const res = await fetch(`http://127.0.0.1:${PORT}/health`, {
          signal: AbortSignal.timeout(2000),
        });
        if (res.ok) {
          resolve();
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

describe('engine smoke (real promptfoo library)', () => {
  it(
    'boots, serves health, rejects bad auth, runs a native run to terminal state',
    { timeout: 300000 },
    async () => {
      engineRoot = await mkdtemp(join(tmpdir(), 'engine-smoke-'));
      promptfooHome = await mkdtemp(join(tmpdir(), 'engine-smoke-pfhome-'));
      const targetPort = await startFakeTarget();
      const generationPort = await startFakeGeneration();
      await startEngine();

      // 1. health (no auth needed)
      const health = await fetch(`http://127.0.0.1:${PORT}/health`);
      expect(health.status).toBe(200);
      const hj = (await health.json()) as { status: string };
      expect(hj.status).toBe('ok');

      // 2. bad auth is rejected
      const bad = await fetch(`http://127.0.0.1:${PORT}/runs`, {
        method: 'POST',
        headers: { authorization: 'Bearer wrong', 'content-type': 'application/json' },
        body: '{}',
      });
      expect(bad.status).toBe(401);

      // 3. submit a run: Plan A consumes a fake generation LLM for attack
      //    synthesis and llm-rubric grading; no external network involved.
      const create = await api('/runs', {
        method: 'POST',
        body: JSON.stringify({
          run_id: 'smoke-1',
          platform_user_id: 'smoke-user',
          purpose: '企业知识库客服助手的隐私泄露检测冒烟测试',
          num_tests: 2,
          plugins: [{ id: 'pii:direct' }],
          // P3-3：`direct` 已下线（Plan C 下「不选策略」就是直接攻击）。
          strategies: [],
          judge_mode: 'promptfoo_native',
          credentials: {
            target: {
              base_url: `http://127.0.0.1:${targetPort}/v1/chat/completions`,
              model: 'fake-model',
              api_key: 'sk-fake',
              timeout_seconds: 10,
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

      // 4. poll status until terminal (max ~4min)
      let terminal = false;
      let finalStatus: Record<string, unknown> | undefined;
      const started = Date.now();
      while (Date.now() - started < 240000) {
        const st = await api('/runs/smoke-1');
        expect(st.status).toBe(200);
        const j = (await st.json()) as Record<string, unknown>;
        finalStatus = j;
        if (['succeeded', 'failed', 'canceled'].includes(String(j.phase))) {
          terminal = true;
          break;
        }
        await new Promise((r) => setTimeout(r, 2000));
      }
      console.log('final status:', JSON.stringify(finalStatus, null, 2));
      expect(terminal).toBe(true);

      // 4b. Plan-A chain assertions: 2 generated probes executed and judged;
      // the fake target always refuses and the fake grader passes them, so
      // attack_success must be 0 and pass_rate 1.
      expect(finalStatus?.phase).toBe('succeeded');
      const result = (finalStatus ?? {}) as {
        planned_count?: number;
        executed_count?: number;
        result?: {
          totals?: { probes?: number; attack_success?: number; pass_rate?: number };
          // P2-6: the run's shape travels with the result.
          generation_path?: string;
          num_tests_effective?: number;
        };
      };
      // P3-3：这些数字是**实测**的，不是选的。native 生成把假 LLM 的返回原样消费
      // （假 LLM 对 `__prompt` 形状回了两条，promptfoo 只落一条探针），所以 1 就是这条
      // 链路的真实产出。改假 LLM 的形状时这三个数字要一起复核 —— 它们的价值在于
      // 「一次真实 run 跑到了终态且计数自洽」，不在于具体是几。
      expect(result.planned_count).toBe(1);
      expect(result.executed_count).toBe(1);
      expect(result.result?.totals?.probes).toBe(1);
      // 假目标永远拒答 → 无攻击成功。这条断言与生成路径无关，必须保持。
      expect(result.result?.totals?.attack_success).toBe(0);
      expect(result.result?.totals?.pass_rate).toBe(1);
      // P2-6: the run's shape is stamped over the real wire. Since P3-3 the only
      // possible value is `plan_c`; the assertion is kept because the field's job
      // (telling a reader which generation path produced the numbers) is what makes
      // historical Plan A reports interpretable, and a regression that stopped
      // writing it would go unnoticed otherwise.
      expect(result.result?.generation_path).toBe('plan_c');
      // The effective count is the CONFIGURED probe count, read off the document
      // that ran (P2-4): 2 here, because that is what the request asked for and the
      // allocator wrote into the plugin's `numTests`.
      //
      // It is deliberately NOT the same as `totals.probes` above (1). That gap is
      // the field's whole purpose: the fake generation LLM returns one probe
      // regardless of `numTests`, so a run CONFIGURED for 2 executed 1. Before this
      // field existed, a reader had no way to tell "we asked for 2 and got 1" from
      // "we asked for 1".
      expect(result.result?.num_tests_effective).toBe(2);

      // 5. The 2026-10-16 contract (see AGENTS.md and
      //    23-聊天链路报告原文与promptfoo对齐.md): the attack and the answer DO
      //    reach the caller, because a report that cannot show them is not a
      //    report. They reach it through exactly one field, and this pair of
      //    assertions is what holds that boundary:
      //      - secrets stay out, everywhere;
      //      - the payload appears, and appears ONLY under case_results.
      const dumped = JSON.stringify(finalStatus);
      expect(dumped).not.toContain('sk-fake');
      expect(dumped).not.toContain('sk-gen');

      expect(result.result?.case_results?.[0]?.prompt).toBe('冒烟测试攻击提示 one');
      expect(result.result?.case_results?.[0]?.response).toContain('抱歉，我不能提供这类内容');

      // The single-channel check: strip case_results and the text must be gone.
      // Without this, "we allow the payload through" would decay into "the
      // payload can show up anywhere" — and the next place it shows up would be
      // a log line or a job progress field that nobody thought to check.
      const withoutCaseResults = {
        ...finalStatus,
        result: { ...result.result, case_results: undefined },
      };
      const dumpedWithout = JSON.stringify(withoutCaseResults);
      expect(dumpedWithout).not.toContain('抱歉，我不能提供这类内容');
      expect(dumpedWithout).not.toContain('冒烟测试攻击提示');
    },
  );

  afterEach(async () => {
    engineProc?.kill();
    fakeTarget?.close();
    fakeGeneration?.close();
    if (engineRoot) {
      await rm(engineRoot, { recursive: true, force: true }).catch(() => undefined);
    }
  });
});
