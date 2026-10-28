/**
 * Publication-time probe for a CODE strategy module.
 *
 * # Why the engine has to answer this and the BFF cannot
 *
 * The BFF can check size, shape and the capability tokens (cheap, deterministic,
 * and worth doing). It cannot answer "does this module load and does its action
 * accept a test case" — only a JS runtime can. Left unanswered until the run,
 * a module that fails to load fails AFTER the user has paid for the probes, and
 * the operator sees it as a mid-run engine failure.
 *
 * # Why a child process
 *
 * Importing a module runs its top-level code. Doing that in the server process
 * would let a bad package take the engine down at publication time — the very
 * thing the run-time guards exist to contain. The probe forks a short-lived
 * child, imports there, calls `action` once with a synthetic case, and returns
 * ONLY a verdict. Nothing from inside the child (log lines, stack, the module's
 * output text) is forwarded: the caller is an expert portal and must not learn
 * anything the module happened to print.
 */
import { fork } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

export interface CodeStrategyProbeInput {
  source: string;
  /** Timeout for the whole probe (import + one action call). */
  timeoutMs?: number;
}

export interface CodeStrategyProbeResult {
  ok: boolean;
  /** Machine-readable reason; empty when ok. */
  code?: string;
  /** A short, content-free explanation for the submitter. */
  message?: string;
}

const PROBE_TIMEOUT_MS = 5_000;

const CHILD_SOURCE = [
  "const target = process.argv[2];",
  "const respond = (payload) => { console.log(JSON.stringify(payload)); process.exit(0); };",
  "try {",
  "  const mod = (await import(target)).default;",
  "  if (!mod || typeof mod.action !== 'function') {",
  "    respond({ ok: false, code: 'code_strategy_bad_shape', message: '模块必须默认导出 { id, action }，且 action 是函数。' });",
  "  }",
  "  const out = await Promise.race([",
  "    Promise.resolve(mod.action([{ vars: { prompt: 'probe' }, metadata: { pluginId: 'probe' } }], 'prompt', {})),",
  "    new Promise((_r, reject) => setTimeout(() => reject(new Error('timeout')), " + (PROBE_TIMEOUT_MS - 500) + ")),",
  "  ]);",
  "  if (!Array.isArray(out) || out.length === 0) {",
  "    respond({ ok: false, code: 'code_strategy_bad_output', message: 'action 必须返回非空的用例数组。' });",
  "  }",
  "  const first = out[0];",
  "  if (!first || !first.vars || typeof first.vars.prompt !== 'string' || first.vars.prompt.trim() === '') {",
  "    respond({ ok: false, code: 'code_strategy_bad_output', message: '返回的用例必须保留 vars.prompt。' });",
  "  }",
  "  respond({ ok: true });",
  "} catch (err) {",
  "  const name = err && err.name ? String(err.name) : 'Error';",
  "  respond({ ok: false, code: 'code_strategy_load_failed', message: '模块加载或执行失败（' + name + '）。' });",
  "}",
].join('\n');


/**
 * Probe one module in a throwaway child process.
 *
 * The module is written into a private temp directory that is removed on every
 * path, so a rejected package leaves nothing behind.
 */
export async function probeCodeStrategyModule(input: CodeStrategyProbeInput): Promise<CodeStrategyProbeResult> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'strategy-probe-'));
  const modulePath = path.join(dir, 'probe.mjs');
  const runnerPath = path.join(dir, 'runner.mjs');
  try {
    await fs.writeFile(modulePath, input.source, { encoding: 'utf8', mode: 0o600 });
    await fs.writeFile(runnerPath, CHILD_SOURCE, { encoding: 'utf8', mode: 0o600 });
    return await new Promise<CodeStrategyProbeResult>((resolve) => {
      // `fork` requires an IPC channel in stdio; 'ignore' for it is enough —
      // the parent never uses IPC, the child reports over stdout.
      const child = fork(runnerPath, [modulePath], {
        stdio: ['ignore', 'pipe', 'pipe', 'ipc'],
        // No inherited environment beyond what Node needs to start: a package
        // that reads process.env would find nothing useful even if it got past
        // the publication token scan.
        env: { PATH: process.env.PATH ?? '', NODE_ENV: 'production' },
        execArgv: [],
      });
      let buffer = '';
      let settled = false;
      const finish = (result: CodeStrategyProbeResult): void => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        child.kill('SIGKILL');
        resolve(result);
      };
      const timer = setTimeout(
        () => finish({ ok: false, code: 'code_strategy_timeout', message: `模块在 ${PROBE_TIMEOUT_MS}ms 内未完成。` }),
        input.timeoutMs ?? PROBE_TIMEOUT_MS,
      );
      child.stdout?.on('data', (chunk: Buffer) => {
        buffer += chunk.toString();
        const line = buffer.split('\n').find((item) => item.trim() !== '');
        if (!line) return;
        try {
          const parsed = JSON.parse(line) as CodeStrategyProbeResult;
          finish(parsed);
        } catch {
          finish({ ok: false, code: 'code_strategy_probe_failed', message: '校验进程返回了无法解析的结果。' });
        }
      });
      child.on('exit', () => {
        finish({ ok: false, code: 'code_strategy_probe_failed', message: '模块在返回结果前退出。' });
      });
      child.on('error', () => {
        finish({ ok: false, code: 'code_strategy_probe_failed', message: '无法启动校验进程。' });
      });
    });
  } finally {
    await fs.rm(dir, { recursive: true, force: true }).catch(() => undefined);
  }
}
