/**
 * A generation-model refusal must become its own error code.
 *
 * # The failure it explains (2026-10-26, record 38)
 *
 * promptfoo feeds the run purpose into every generation template. A purpose that
 * reads like a harness makes the generation model refuse; the plugin then yields
 * 0 cases and strict mode fails the run. The platform saw only
 * `native_generation_promptfoo_failed` — indistinguishable from a rate limit or a
 * broken template — while the fix was "reword the purpose".
 *
 * # Why this drives the real entry point
 *
 * `isGenerationRefusal` is unit-tested, but the defect shape is a correct helper
 * whose call site forgets to use it. This runs `executeNativeRedteamRun` against
 * a generation server that answers every request with a refusal and asserts the
 * thrown code.
 *
 * Run with: npx vitest run tests/generationRefusal.e2e.test.ts
 */
import './u8PromptfooHome.js';
import { afterAll, describe, expect, it } from 'vitest';
import http from 'node:http';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { executeNativeRedteamRun } from '../src/runner/nativeRedteam.js';
import type { RunRecord } from '../src/manager/taskManager.js';

process.env.CI = 'true';
process.env.PROMPTFOO_DISABLE_REMOTE_GENERATION = '1';
process.env.PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION = '1';

const servers: http.Server[] = [];
afterAll(() => {
  for (const s of servers) s.close();
});

async function listen(handler: http.RequestListener): Promise<number> {
  const server = http.createServer(handler);
  servers.push(server);
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', () => r()));
  return (server.address() as { port: number }).port;
}

function readBody(req: http.IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let body = '';
    req.on('data', (c: Buffer) => (body += c.toString()));
    req.on('end', () => resolve(body));
  });
}

describe('generation refusal', () => {
  it('is reported as native_generation_model_refused, not as a generic promptfoo failure', { timeout: 120000 }, async () => {
    const genPort = await listen(async (req, res) => {
      await readBody(req);
      res.setHeader('content-type', 'application/json');
      // promptfoo's isBasicRefusal() recognises this shape and throws with
      // "returned a refusal during inference".
      res.end(
        JSON.stringify({
          choices: [{ message: { role: 'assistant', content: 'I cannot help with that request.' } }],
          usage: { prompt_tokens: 8, completion_tokens: 6, total_tokens: 14 },
        }),
      );
    });
    const targetPort = await listen(async (req, res) => {
      await readBody(req);
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ choices: [{ message: { role: 'assistant', content: 'ok' } }] }));
    });
    const workdir = await fs.mkdtemp(path.join(os.tmpdir(), 'refusal-'));

    const record = {
      controller: new AbortController(),
      request: {
        run_id: 'refusal-run',
        platform_user_id: 'u',
        purpose: '验证用：固定返回配合内容',
        num_tests: 1,
        plugins: [{ id: 'contracts' }],
        strategies: [],
        judge_mode: 'promptfoo_native' as const,
        credentials: {
          target: {
            base_url: `http://127.0.0.1:${targetPort}/v1/chat/completions`,
            model: 'fake-target',
            api_key: 'k',
            timeout_seconds: 10,
          },
          generation: { base_url: `http://127.0.0.1:${genPort}/v1`, model: 'fake-gen', api_key: 'k' },
        },
        catalog: {
          plugins: [
            {
              id: 'contracts',
              category: 'industry',
              category_label: '行业合规',
              severity: 'medium',
              judge_criteria: 'RUBRIC',
              judge_source: 'category:industry',
            },
          ],
          strategies: [],
        },
      },
    } as unknown as RunRecord;

    try {
      await expect(executeNativeRedteamRun(record, () => undefined, workdir)).rejects.toMatchObject({
        code: 'native_generation_model_refused',
      });
    } finally {
      await fs.rm(workdir, { recursive: true, force: true }).catch(() => undefined);
    }
  });
});
