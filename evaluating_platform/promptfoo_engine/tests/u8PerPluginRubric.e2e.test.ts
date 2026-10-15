/**
 * U8 E2E — proves the run judges by the plugin's own rubric, against the real
 * promptfoo library.
 *
 * # It used to test both generation paths
 *
 * U8 landed while Plan A and Plan C coexisted, and the point of this file was
 * that the per-plugin rubric reached the grader on BOTH — the risk being that
 * turning the switch on would silently regress judging to promptfoo's one-size
 * rubric. P3-3 deleted Plan A, so the Plan A half is gone. The remaining half is
 * the one that matters and it is unchanged: the rubric text must reach the
 * grader on the live path. The FILE NAME is kept for one reason — the u8 report
 * and the docs cite it — but nothing here tests two paths any more.
 *
 * Method (the U7 lesson: never assert a config field is consumed — measure it):
 *   - a fake target server records every probe it receives;
 *   - a fake generation+grading LLM records every request body, so we can see
 *     the actual grading prompt promptfoo built;
 *   - the run goes through `buildNativeRedteamConfig` fed to
 *     `promptfoo.redteam.run()`;
 *   - and we assert the per-plugin rubric text reached the grader, and that two
 *     plugins in one run got two different rubrics.
 *
 * Run with: npx vitest run tests/u8PerPluginRubric.e2e.test.ts
 */
// MUST stay the first import: it redirects promptfoo's config dir before
// promptfoo is imported by anything below (ESM evaluates imports in order, and
// promptfoo latches the dir at import time). See the module's own header.
import './u8PromptfooHome.js';
import { afterAll, describe, expect, it } from 'vitest';
import http from 'node:http';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { stringify } from 'yaml';
import { reduceEvalToSafeResult } from '../src/runner/redteamRun.js';
import { buildNativeRedteamConfig, executeNativeRedteamRun, loadEvalResults } from '../src/runner/nativeRedteam.js';
import type { RunRecord } from '../src/manager/taskManager.js';
import type { EngineCatalog } from '../src/types.js';

/** Distinct, greppable rubric bodies — one per plugin family in this test. */
const RUBRIC_INJECTION = 'U8_E2E_RUBRIC_INJECTION_提示注入专用判据';
const RUBRIC_HARMFUL = 'U8_E2E_RUBRIC_HARMFUL_有害内容专用判据';

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

/** Catalog shaped like the BFF's: two families, each with its own rubric. */
function catalog(): EngineCatalog {
  return {
    plugins: [
      {
        id: 'prompt-injection',
        category: 'injection',
        category_label: '提示注入',
        severity: 'high',
        judge_criteria: RUBRIC_INJECTION,
        judge_source: 'plugin:prompt-injection',
      },
      {
        id: 'harmful:hate',
        category: 'harmful',
        category_label: '有害内容',
        severity: 'high',
        judge_criteria: RUBRIC_HARMFUL,
        judge_source: 'category:harmful',
      },
    ],
    strategies: [],
  };
}

describe('U8 E2E — the native path judges per plugin', { timeout: 180000 }, () => {
  // promptfoo's config dir is redirected by the `./u8PromptfooHome.js` import at
  // the very top of this file — it has to happen before promptfoo is imported,
  // and that module also removes the temp dir on process exit.
  it('Plan C: promptfoo grades with the per-plugin rubric we injected', async () => {
    const gradingBodies: string[] = [];
    const genPort = await listen(async (req, res) => {
      const body = await readBody(req);
      res.setHeader('content-type', 'application/json');
      // Entity extraction (promptfoo's own first call — Plan A never makes it).
      if (body.includes('Extract only real-world entities')) {
        res.end(
          JSON.stringify({
            choices: [{ message: { role: 'assistant', content: JSON.stringify({ entities: [], purpose: 'e2e' }) } }],
            usage: { prompt_tokens: 5, completion_tokens: 2, total_tokens: 7 },
          }),
        );
        return;
      }
      // Generation templates differ per plugin: contracts-style asks for
      // "Prompt:" lines, others ask for a JSON array of {__prompt}.
      if (body.includes('Each line must begin with the string')) {
        res.end(
          JSON.stringify({
            choices: [{ message: { role: 'assistant', content: 'Prompt: probe attack one' } }],
            usage: { prompt_tokens: 8, completion_tokens: 4, total_tokens: 12 },
          }),
        );
        return;
      }
      if (body.includes('"__prompt"')) {
        res.end(
          JSON.stringify({
            choices: [{ message: { role: 'assistant', content: JSON.stringify([{ __prompt: 'probe attack one' }]) } }],
            usage: { prompt_tokens: 8, completion_tokens: 4, total_tokens: 12 },
          }),
        );
        return;
      }
      gradingBodies.push(body);
      res.end(
        JSON.stringify({
          choices: [
            {
              message: {
                role: 'assistant',
                content: JSON.stringify({ pass: false, score: 0, reason: 'probe verdict' }),
              },
            },
          ],
          usage: { prompt_tokens: 20, completion_tokens: 5, total_tokens: 25 },
        }),
      );
    });
    const targetPort = await listen(async (req, res) => {
      await readBody(req);
      res.setHeader('content-type', 'application/json');
      res.end(
        JSON.stringify({
          choices: [{ message: { role: 'assistant', content: 'Sure — here is the content you asked for, step by step.' } }],
        }),
      );
    });

    const workdir = await fs.mkdtemp(path.join(os.tmpdir(), 'u8-e2e-'));
    try {
      const config = await buildNativeRedteamConfig({
        runId: 'u8-plan-c',
        purpose: 'U8 E2E',
        injectVar: 'prompt',
        // P2-4: the platform figure is a TOTAL and each selected plugin needs at
        // least one probe, so 2 covers these two.
        numTests: 2,
        plugins: [{ id: 'contracts' }, { id: 'hallucination' }],
        strategies: [],
        credentials: {
          target: {
            base_url: `http://127.0.0.1:${targetPort}/v1/chat/completions`,
            model: 'fake-target',
            api_key: 'sk-target',
          },
          generation: {
            base_url: `http://127.0.0.1:${genPort}/v1`,
            model: 'fake-gen',
            api_key: 'sk-gen',
          },
        },
        catalog: {
          plugins: [
            {
              id: 'contracts',
              category: 'industry',
              category_label: '行业合规',
              severity: 'medium',
              judge_criteria: RUBRIC_INJECTION,
              judge_source: 'category:industry',
            },
            {
              id: 'hallucination',
              category: 'hallucination',
              category_label: '幻觉与事实性',
              severity: 'medium',
              judge_criteria: RUBRIC_HARMFUL,
              judge_source: 'category:hallucination',
            },
          ],
          strategies: [],
        },
      });

      // The document really does carry two different rubrics.
      expect(config.redteam.plugins[0]?.config?.graderGuidance).toBe(RUBRIC_INJECTION);
      expect(config.redteam.plugins[1]?.config?.graderGuidance).toBe(RUBRIC_HARMFUL);

      const configPath = path.join(workdir, 'redteam-config.yaml');
      await fs.writeFile(configPath, stringify(config, { lineWidth: 0 }), { encoding: 'utf8', mode: 0o600 });

      process.env.PROMPTFOO_DISABLE_TELEMETRY = '1';
      process.env.PROMPTFOO_DISABLE_SHARE = '1';
      process.env.PROMPTFOO_DISABLE_REMOTE_GENERATION = '1';
      process.env.PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION = '1';

      // Dynamic import, not a static one at the top of this file: promptfoo
      // captures PROMPTFOO_CONFIG_DIR into a module-level variable at import
      // time, so a static import would latch the real ~/.promptfoo before
      // beforeAll ever runs. Same ESM hoisting trap U7 documented in §4.8.5,
      // and the reason U7's cleanup exists at all.
      const promptfoo = (await import('promptfoo')).default;
      const evalResult = (await promptfoo.redteam.run({
        config: configPath,
        force: true,
        strict: false,
        cache: false,
        verbose: false,
        progressBar: false,
      } as Parameters<typeof promptfoo.redteam.run>[0])) as unknown as {
        results?: Array<{ testCase?: { metadata?: Record<string, unknown> } }>;
      };
      // --- the actual proof: promptfoo's own grader saw our rubric ---
      expect(gradingBodies.length).toBeGreaterThan(0);
      const usedRubrics = gradingBodies.filter(
        (b) => b.includes(RUBRIC_INJECTION) || b.includes(RUBRIC_HARMFUL),
      );
      expect(usedRubrics.length).toBeGreaterThan(0);
      // Both families were present in the run, so both rubrics must appear.
      expect(gradingBodies.some((b) => b.includes(RUBRIC_INJECTION))).toBe(true);
      expect(gradingBodies.some((b) => b.includes(RUBRIC_HARMFUL))).toBe(true);

      // --- the fix that makes Plan C's judging reach the report at all ---
      //
      // Before U8's loadEvalResults, this path reduced an Eval whose `results`
      // array was empty and reported 0 probes / 0 coverage for every plugin,
      // even though 2 probes had been generated and graded. Assert through the
      // *production* entry point (executeNativeRedteamRun), not by hand-wiring
      // getResults() here — a test that loads results itself proves nothing about
      // whether the run does.
      const planCCatalog = {
        plugins: [
          {
            id: 'contracts',
            category: 'industry',
            category_label: '行业合规',
            severity: 'medium',
            judge_criteria: RUBRIC_INJECTION,
            judge_source: 'category:industry',
          },
          {
            id: 'hallucination',
            category: 'hallucination',
            category_label: '幻觉与事实性',
            severity: 'medium',
            judge_criteria: RUBRIC_HARMFUL,
            judge_source: 'category:hallucination',
          },
        ],
        strategies: [],
      };
      const loaded = await loadEvalResults(evalResult);
      // Measured, not asserted as a fixed precondition: on a bare process the
      // library leaves `Eval.results` empty (it persists rows via doEval's
      // `write: true` and only calls `setResults` from its server/CLI paths), but
      // in a process that has already evaluated, it can come back populated.
      // loadEvalResults handles both; the substantive claim is the one below.
      expect(loaded.results?.length ?? 0).toBeGreaterThan(0);

      const reduced = reduceEvalToSafeResult(loaded, planCCatalog);
      const sources = Object.fromEntries((reduced.judge_sources ?? []).map((s) => [s.source, s.probes]));
      expect(Object.keys(sources).length).toBeGreaterThan(0);
      // Every probe resolved to a named standard — none fell back silently.
      expect(reduced.judge_sources?.every((s) => !s.is_fallback)).toBe(true);
      // Two different families in one run → two different standards recorded.
      expect(new Set(Object.keys(sources)).size).toBe(2);
    } finally {
      await fs.rm(workdir, { recursive: true, force: true });
    }
  });

  // The claim that matters, asserted through the **production entry point**:
  // executeNativeRedteamRun used to report 0 probes / 0 coverage for every plugin
  // even after generating and grading two probes. A test that calls
  // loadEvalResults by hand would keep passing even if the run stopped calling
  // it — which is exactly the trap that made U8's counter-proof #3 green on the
  // first attempt. So this goes through executeNativeRedteamRun and asserts on
  // its SafeRunResult.
  it('Plan C run reports non-zero probes through executeNativeRedteamRun', { timeout: 180000 }, async () => {
    // promptfoo's policy plugin path calls promptForEmailUnverified(), which
    // BLOCKS on interactive stdin when no email is configured. CI mode uses a
    // placeholder and proceeds. Without this the run hangs on '? Work email:'.
    process.env.CI = 'true';
    const genPort = await listen(async (req, res) => {
      const body = await readBody(req);
      res.setHeader('content-type', 'application/json');
      if (body.includes('Extract only real-world entities')) {
        res.end(JSON.stringify({ choices: [{ message: { role: 'assistant', content: JSON.stringify({ entities: [], purpose: 'e2e' }) } }], usage: { prompt_tokens: 5, completion_tokens: 2, total_tokens: 7 } }));
        return;
      }
      // PolicyPlugin's template asks for `"Prompt: "` lines ("One prompt per
      // line"), NOT the contracts phrasing. Missing this made `policy` yield 0
      // cases in this harness and looked like an upstream dead end — it was our
      // fake LLM's gap, not promptfoo's.
      if (body.includes('Each line must begin with the string') || body.includes('One prompt per line')) {
        res.end(JSON.stringify({ choices: [{ message: { role: 'assistant', content: 'Prompt: probe attack one' } }], usage: { prompt_tokens: 8, completion_tokens: 4, total_tokens: 12 } }));
        return;
      }
      if (body.includes('"__prompt"')) {
        res.end(JSON.stringify({ choices: [{ message: { role: 'assistant', content: JSON.stringify([{ __prompt: 'probe attack one' }]) } }], usage: { prompt_tokens: 8, completion_tokens: 4, total_tokens: 12 } }));
        return;
      }
      res.end(JSON.stringify({ choices: [{ message: { role: 'assistant', content: JSON.stringify({ pass: false, score: 0, reason: 'probe verdict' }) } }], usage: { prompt_tokens: 20, completion_tokens: 5, total_tokens: 25 } }));
    });
    const targetPort = await listen(async (req, res) => {
      await readBody(req);
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ choices: [{ message: { role: 'assistant', content: 'Sure — here it is, step by step.' } }] }));
    });

    const workdir = await fs.mkdtemp(path.join(os.tmpdir(), 'u8-e2e-run-'));
    try {
      const controller = new AbortController();
      const record = {
        controller,
        request: {
          run_id: 'u8-plan-c-run',
          platform_user_id: 'u8-user',
          purpose: 'U8 E2E',
          // P2-4: platform total, and each of the three plugins needs >= 1 probe.
          num_tests: 3,
          plugins: [
            { id: 'contracts' },
            { id: 'hallucination' },
            // `policy` carries a caller-supplied policy document. Without one
            // PolicyPlugin has nothing to test against and yields 0 cases
            // (measured) — which is why it needs a doc here, and also why the
            // platform must treat "policy selected without a document" as a
            // real gap rather than a silent 0-coverage run.
            { id: 'policy', config: { policy: '不得给出退款承诺' } },
          ],
          strategies: [],
          judge_mode: 'promptfoo_native' as const,
          credentials: {
            target: { base_url: `http://127.0.0.1:${targetPort}/v1/chat/completions`, model: 'fake', api_key: 'k', timeout_seconds: 10 },
            generation: { base_url: `http://127.0.0.1:${genPort}/v1`, model: 'fake-gen', api_key: 'k' },
          },
          catalog: {
            plugins: [
              { id: 'contracts', category: 'industry', category_label: '行业合规', severity: 'medium', judge_criteria: RUBRIC_INJECTION, judge_source: 'category:industry' },
              { id: 'hallucination', category: 'hallucination', category_label: '幻觉与事实性', severity: 'medium', judge_criteria: RUBRIC_HARMFUL, judge_source: 'category:hallucination' },
              // Same family as `contracts` on purpose: without the override the
              // two would be indistinguishable in the report.
              { id: 'policy', category: 'industry', category_label: '行业合规', severity: 'medium', judge_criteria: RUBRIC_INJECTION, judge_source: 'category:industry' },
            ],
            strategies: [],
          },
        },
      } as unknown as RunRecord;

      const { result } = await executeNativeRedteamRun(record, () => undefined, workdir);

      // The regression this pins: all of these were 0 before loadEvalResults.
      expect(result.totals.probes).toBeGreaterThan(0);
      // P2-6: a Plan C run is stamped as one, so its report cannot be mistaken for
      // a Plan A report — the two are not comparable and, before this field, looked
      // identical. The effective count is the number the allocator configured (3
      // plugins, 3 probes requested), read off the document that ran.
      expect(result.generation_path).toBe('plan_c');
      expect(result.num_tests_effective).toBe(3);
      expect(result.totals.attack_success).toBeGreaterThan(0);
      expect(result.plugin_stats.length).toBeGreaterThan(0);
      // And the per-plugin standards still came through, non-degraded.
      const byPlugin = Object.fromEntries((result.plugin_stats ?? []).map((s) => [s.plugin_id, s.judge_source]));
      expect(byPlugin['contracts']).toBe('category:industry');
      expect(byPlugin['hallucination']).toBe('category:hallucination');
      expect(result.judge_sources?.every((s) => !s.is_fallback)).toBe(true);

      // U8 ①: `policy` must NOT claim the platform family standard — on this path
      // promptfoo's own grader judged it. Asserted through the run (the unique
      // exit), NOT through the helper: the previous version tested only the
      // helper and deleting the wiring left the suite green — the exact trap
      // this batch's §2.10.1 criterion is about.
      //
      // Driving `policy` at all required two measured fixes: the fake LLM must
      // answer PolicyPlugin's `"Prompt: "` line format, and the plugin needs a
      // policy document (PolicyPlugin yields 0 cases without one).
      expect(byPlugin['policy']).toBe('promptfoo:policy');
      // It is not the fallback tier: the platform does have a standard here, it
      // just could not be delivered.
      expect(result.judge_sources?.every((s) => !s.is_fallback)).toBe(true);
      expect(result.judge_sources?.map((s) => s.source).sort()).toEqual([
        'category:hallucination',
        'category:industry',
        'promptfoo:policy',
      ]);
    } finally {
      delete process.env.CI;
      await fs.rm(workdir, { recursive: true, force: true });
    }
  });
});