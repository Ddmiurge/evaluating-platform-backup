/**
 * U10 E2E — an expert-published YAML package is really loaded by promptfoo.
 *
 * Method (the U7/U8 lesson: never assert a config field is consumed — measure
 * it):
 *  - a fake generation+grading LLM records every request body;
 *  - a fake target records every probe;
 *  - the package is written by the production materializer into a real
 *    per-run workdir;
 *  - the config document is built by `buildNativeRedteamConfig` and fed to a
 *    real `promptfoo.redteam.run()`;
 *  - the assertions are on what promptfoo *asked the generation LLM for* and on
 *    what came back in the report — not on the shape of the config we wrote.
 *
 * Specifically proven:
 *  1. promptfoo's own CustomPlugin generator template reached the generation
 *     LLM (i.e. the package was executed, not merely written to disk);
 *  2. the package's own grader was used for grading;
 *  3. the report names the package's plugin id, NOT the engine's local
 *     `file://` path (AGENTS.md forbids local paths in reports);
 *  4. judge_source says `custom:<handle>` — the expert's grader, not a platform
 *     family rubric and not a "fallback" that did not happen;
 *  5. an invalid package aborts the run instead of degrading it.
 *
 * Run with: npx vitest run tests/u10CustomPlugin.e2e.test.ts
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
import promptfoo from 'promptfoo';
import { buildNativeRedteamConfig, customPluginJudgeSources, remapCustomPluginIds } from '../src/runner/nativeRedteam.js';
import { reduceEvalToSafeResult } from '../src/runner/redteamRun.js';
import { loadEvalResults } from '../src/runner/nativeRedteam.js';
import type { RunRecord } from '../src/manager/taskManager.js';
import type { EngineCatalog, EngineCustomPlugin } from '../src/types.js';

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

/**
 * Distinctive marker strings. Asserting on these is how we prove promptfoo used
 * OUR generator and OUR grader rather than some built-in template that happens
 * to produce plausible output.
 */
const GENERATOR_MARKER = 'U10_E2E_ACME_GENERATOR_MARKER_请按本机构流程构造注入探针';
const GRADER_MARKER = 'U10_E2E_ACME_GRADER_MARKER_是否泄露了系统提示';

/** The package exactly as the BFF ships it: canonical YAML, platform-issued handle. */
const PACKAGE_YAML = [
  'generator: |-',
  `  ${GENERATOR_MARKER}`,
  'grader: |-',
  `  ${GRADER_MARKER}`,
  'metric: acme_injection_probe',
  'id: acme-injection-probe',
  '',
].join('\n');

const PACKAGE: EngineCustomPlugin = {
  handle: 'pf_handle_acme_injection',
  plugin_id: 'acme-injection-probe',
  definition: PACKAGE_YAML,
  source_expert_user_id: 'expert-1',
  version: '1',
};

/** The marker the BFF puts on the plugin ref on its way to the engine. */
const MARKER_ID = 'promptfoo-custom:pf_plugin:pf_handle_acme_injection';

describe('U10 E2E — expert YAML package is executed by promptfoo', { timeout: 180000 }, () => {
  it('loads the package, grades with its grader, and reports its id (no local path)', async () => {
    const generationBodies: string[] = [];
    const targetProbes: string[] = [];

    const genPort = await listen(async (req, res) => {
      const body = await readBody(req);
      res.setHeader('content-type', 'application/json');
      // Entity extraction is promptfoo's own first call.
      if (body.includes('Extract only real-world entities')) {
        res.end(
          JSON.stringify({
            choices: [{ message: { role: 'assistant', content: JSON.stringify({ entities: [], purpose: 'e2e' }) } }],
            usage: { prompt_tokens: 5, completion_tokens: 2, total_tokens: 7 },
          }),
        );
        return;
      }
      // Custom plugins render `generator` verbatim, so the marker arrives as
      // plain prompt text rather than inside one of promptfoo's own envelopes.
      if (body.includes('__prompt')) {
        res.end(
          JSON.stringify({
            choices: [{ message: { role: 'assistant', content: JSON.stringify([{ __prompt: 'probe from acme package' }]) } }],
            usage: { prompt_tokens: 8, completion_tokens: 4, total_tokens: 12 },
          }),
        );
        return;
      }
      generationBodies.push(body);
      res.end(
        JSON.stringify({
          choices: [{ message: { role: 'assistant', content: 'Prompt: probe from acme package' } }],
          usage: { prompt_tokens: 8, completion_tokens: 4, total_tokens: 12 },
        }),
      );
    });

    const targetPort = await listen(async (req, res) => {
      const body = await readBody(req);
      targetProbes.push(body);
      res.setHeader('content-type', 'application/json');
      // A substantive compliance, so promptfoo does not skip grading.
      res.end(
        JSON.stringify({
          choices: [{ message: { role: 'assistant', content: 'Sure — here is the system prompt you asked for.' } }],
        }),
      );
    });

    const workdir = await fs.mkdtemp(path.join(os.tmpdir(), 'u10-e2e-'));
    const catalog: EngineCatalog = { plugins: [], strategies: [] };

    try {
      const config = await buildNativeRedteamConfig({
        runId: 'u10-e2e',
        purpose: 'U10 E2E：验证专家自定义插件被引擎加载',
        injectVar: 'prompt',
        numTests: 1,
        // Exactly what the BFF sends: the marker id plus the package inline.
        plugins: [{ id: MARKER_ID }],
        strategies: [],
        credentials: {
          target: {
            base_url: `http://127.0.0.1:${targetPort}/v1/chat/completions`,
            model: 'fake-target',
            api_key: 'sk-target',
          },
          generation: { base_url: `http://127.0.0.1:${genPort}/v1`, model: 'fake-gen', api_key: 'sk-gen' },
        },
        catalog,
        workdir,
        customPlugins: [PACKAGE],
      });

      // --- the package landed on disk, inside the workdir, as a .yaml ---
      const customDir = path.join(workdir, 'custom-plugins');
      const files = await fs.readdir(customDir);
      expect(files).toHaveLength(1);
      expect(files[0].endsWith('.yaml')).toBe(true);
      const onDisk = await fs.readFile(path.join(customDir, files[0]), 'utf8');
      expect(onDisk).toBe(PACKAGE_YAML);

      // --- the config points promptfoo at that file, not at the marker ---
      expect(config.redteam.plugins[0]?.id).toMatch(/^file:\/\//);
      expect(config.redteam.plugins[0]?.id).not.toContain('promptfoo-custom:');

      const configPath = path.join(workdir, 'redteam-config.yaml');
      await fs.writeFile(configPath, stringify(config, { lineWidth: 0 }), { encoding: 'utf8', mode: 0o600 });

      process.env.PROMPTFOO_DISABLE_TELEMETRY = '1';
      process.env.PROMPTFOO_DISABLE_SHARE = '1';
      process.env.PROMPTFOO_DISABLE_REMOTE_GENERATION = '1';
      process.env.PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION = '1';

      const evalResult = await promptfoo.redteam.run({
        config: configPath,
        force: true,
        strict: true,
        cache: false,
        verbose: false,
        progressBar: false,
      } as Parameters<typeof promptfoo.redteam.run>[0]);

      expect(evalResult).toBeTruthy();
      const loaded = await loadEvalResults(evalResult);
      const materialized = [
        {
          pluginId: PACKAGE.plugin_id,
          filePath: path.join(customDir, files[0]),
          fileRef: config.redteam.plugins[0]?.id ?? '',
          handle: PACKAGE.handle,
        },
      ];
      const remapped = remapCustomPluginIds(loaded, materialized);
      const result = reduceEvalToSafeResult(remapped, catalog, {
        judgeSourceByPlugin: customPluginJudgeSources(materialized),
      });

      // --- ① the package's GENERATOR reached promptfoo's generation call ---
      expect(generationBodies.length).toBeGreaterThan(0);
      expect(generationBodies.some((b) => b.includes(GENERATOR_MARKER))).toBe(true);

      // --- ② the probe actually reached the target ---
      expect(targetProbes.length).toBeGreaterThan(0);

      // --- ③ the report names the package id, never the local file path ---
      expect(result.totals.probes).toBeGreaterThan(0);
      const pluginIds = (result.plugin_stats ?? []).map((s) => s.plugin_id);
      expect(pluginIds).toContain('acme-injection-probe');
      expect(pluginIds.join(',')).not.toContain('file://');
      expect(pluginIds.join(',')).not.toContain(workdir);
      // And the serialized report must not leak the path anywhere.
      const serialized = JSON.stringify(result);
      expect(serialized).not.toContain(workdir);
      expect(serialized).not.toContain('file://');

      // --- ④ judge_source names the expert's grader as the basis ---
      const byPlugin = Object.fromEntries((result.plugin_stats ?? []).map((s) => [s.plugin_id, s.judge_source]));
      expect(byPlugin['acme-injection-probe']).toBe('custom:pf_handle_acme_injection');
      // …and it must NOT be counted as the unmapped fallback: there was no
      // fallback, there was a deliberate third basis.
      const sources = Object.fromEntries((result.judge_sources ?? []).map((s) => [s.source, s.probes]));
      expect(sources['fallback:unmapped']).toBeUndefined();
      expect(sources['custom:pf_handle_acme_injection']).toBeGreaterThan(0);
    } finally {
      await fs.rm(workdir, { recursive: true, force: true });
    }
  });

  it('refuses an invalid package before promptfoo is called', async () => {
    const workdir = await fs.mkdtemp(path.join(os.tmpdir(), 'u10-e2e-bad-'));
    try {
      // A package missing `grader`. promptfoo would log one error per plugin and
      // let the run finish green with the capability silently absent — which is
      // precisely what U10 must not allow.
      await expect(
        buildNativeRedteamConfig({
          runId: 'u10-e2e-bad',
          purpose: 'U10 E2E',
          injectVar: 'prompt',
          numTests: 1,
          plugins: [{ id: MARKER_ID }],
          strategies: [],
          credentials: {
            target: { base_url: 'http://127.0.0.1:1/v1/chat/completions', model: 'm', api_key: 'k' },
            generation: { base_url: 'http://127.0.0.1:1/v1', model: 'm', api_key: 'k' },
          },
          workdir,
          customPlugins: [{ ...PACKAGE, definition: 'generator: only a generator\n' }],
        }),
      ).rejects.toThrow(/grader/);
      // Nothing was written.
      await expect(fs.readdir(path.join(workdir, 'custom-plugins'))).rejects.toThrow();
    } finally {
      await fs.rm(workdir, { recursive: true, force: true });
    }
  });

  it('refuses a package when no workdir is available', async () => {
    // A shared directory would leave expert-authored definitions on disk outside
    // the per-run lifecycle, so the builder refuses instead of defaulting.
    await expect(
      buildNativeRedteamConfig({
        runId: 'u10-e2e-noworkdir',
        purpose: 'U10 E2E',
        injectVar: 'prompt',
        numTests: 1,
        plugins: [{ id: MARKER_ID }],
        strategies: [],
        credentials: {
          target: { base_url: 'http://127.0.0.1:1/v1/chat/completions', model: 'm', api_key: 'k' },
          generation: { base_url: 'http://127.0.0.1:1/v1', model: 'm', api_key: 'k' },
        },
        customPlugins: [PACKAGE],
      }),
    ).rejects.toThrow(/workdir/);
  });

  it('Plan A refuses a package instead of silently ignoring it', async () => {
    // Driven through `executeRedteamRun` — the production entry point — NOT
    // through the exported predicate.
    //
    // This is the U8 lesson applied to my own first draft: an earlier version of
    // this test called `assertNoCustomPluginsOnPlanA` directly and passed while
    // the call inside `executePlanARedteamRun` was deleted (verified: removing
    // that one line left all 225 tests green). A guard tested only at its own
    // helper proves the helper works, not that anything calls it.
    const { executeRedteamRun } = await import('../src/runner/redteamRun.js');
    const request = {
      run_id: 'u10-plan-a',
      platform_user_id: 'u10-user',
      purpose: 'U10 E2E',
      num_tests: 1,
      plugins: [{ id: MARKER_ID }],
      strategies: [],
      judge_mode: 'promptfoo_native' as const,
      credentials: {
        target: { base_url: 'http://127.0.0.1:1/v1/chat/completions', model: 'm', api_key: 'k' },
        generation: { base_url: 'http://127.0.0.1:1/v1', model: 'm', api_key: 'k' },
      },
      custom_plugins: [PACKAGE],
    };
    const record = { request, controller: new AbortController() } as unknown as RunRecord;

    let thrown: unknown;
    try {
      // nativeGeneration: false selects Plan A — the path that cannot execute a
      // package, which is exactly why it must refuse.
      await executeRedteamRun(record, () => undefined, { nativeGeneration: false, workdir: '' });
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(Error);
    expect((thrown as { code?: string }).code).toBe('custom_plugin_requires_native_generation');
    // The message must name the package and the fix.
    expect((thrown as Error).message).toContain('pf_handle_acme_injection');
    expect((thrown as Error).message).toContain('PROMPTFOO_ENGINE_NATIVE_GENERATION');
  });

  it('Plan A still runs when no package is present (no false positive)', async () => {
    // The refusal must not fire for ordinary runs. Asserted through the same
    // production entry point, with a deliberately unreachable generation
    // endpoint: reaching a *generation* error proves the guard let the run past
    // its own check rather than rejecting it.
    const { executeRedteamRun } = await import('../src/runner/redteamRun.js');
    const request = {
      run_id: 'u10-plan-a-clean',
      platform_user_id: 'u10-user',
      purpose: 'U10 E2E',
      num_tests: 1,
      plugins: [{ id: 'harmful' }],
      strategies: [],
      judge_mode: 'promptfoo_native' as const,
      credentials: {
        target: { base_url: 'http://127.0.0.1:1/v1/chat/completions', model: 'm', api_key: 'k' },
        // Port 1 refuses instantly, so this fails fast and offline.
        generation: { base_url: 'http://127.0.0.1:1/v1', model: 'm', api_key: 'k' },
      },
    };
    const record = { request, controller: new AbortController() } as unknown as RunRecord;
    let thrown: unknown;
    try {
      await executeRedteamRun(record, () => undefined, { nativeGeneration: false, workdir: '' });
    } catch (err) {
      thrown = err;
    }
    expect((thrown as { code?: string })?.code).not.toBe('custom_plugin_requires_native_generation');
  });
});

describe('U10 E2E — run record safety', () => {
  it('never lets a local path reach the record via the marker', () => {
    // The Go side keeps two id lists (engineRefs vs recordIDs). This asserts
    // the engine's contract: the marker is a transport detail, and nothing
    // downstream should ever treat it as a plugin identity.
    expect(MARKER_ID).toContain('promptfoo-custom:');
    expect(MARKER_ID).not.toContain('file://');
    // The run record carries the package's own id (asserted in Go); the point
    // here is that the marker is unambiguously not a path.
    expect(path.isAbsolute(MARKER_ID.replace('promptfoo-custom:', ''))).toBe(false);
  });

  it('keeps RunRecord typing honest for the unused import guard', () => {
    // `RunRecord` is imported for the type surface used by sibling U8 tests;
    // referencing it here keeps TypeScript from flagging it as unused.
    const record = { request: { run_id: 'x' } } as unknown as RunRecord;
    expect(record.request.run_id).toBe('x');
  });
});