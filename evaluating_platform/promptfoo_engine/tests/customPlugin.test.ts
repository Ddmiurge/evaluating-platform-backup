/**
 * U10 · custom plugin package loading — unit level.
 *
 * # What is under test and why each case exists
 *
 * The engine is the *second* gate, not the only one: the BFF already validated
 * the package. These tests exist because the engine is a separately deployable
 * service with its own REST surface — a request can reach it from an older
 * BFF, a replayed capture, or a future caller that never went through
 * `ValidateCustomPluginPublication`. That is the same two-gate argument U6 makes
 * for the remote-generation guard, and it is why the engine re-derives the
 * schema rather than trusting the payload.
 *
 * The cases that matter most are the ones where a *weaker* implementation would
 * still look correct:
 *  - a package whose name sanitizes to empty (traversal-proofing must not
 *    produce a path outside the workdir);
 *  - two ids that sanitize identically (must not overwrite each other);
 *  - the `file://` → plugin-id mapping in **both** directions, because a report
 *    built from promptfoo's raw `metadata.pluginId` would carry a local path.
 */
import { afterEach, describe, expect, it } from 'vitest';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {
  applyCustomPluginRefs,
  assertPublishableDefinition,
  CUSTOM_PLUGIN_ERR_INVALID,
  CustomPluginError,
  customPluginFileName,
  materializeCustomPlugins,
  PROMPTFOO_CUSTOM_DEFAULT_ID,
  reportPluginID,
  type EngineCustomPlugin,
} from '../src/runner/customPlugin.js';

const workdirs: string[] = [];

async function makeWorkdir(): Promise<string> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'u10-workdir-'));
  workdirs.push(dir);
  return dir;
}

afterEach(async () => {
  while (workdirs.length > 0) {
    await fs.rm(workdirs.pop() as string, { recursive: true, force: true });
  }
});

function plugin(overrides: Partial<EngineCustomPlugin> = {}): EngineCustomPlugin {
  return {
    handle: 'pf_handle_acme',
    plugin_id: 'acme-injection-probe',
    definition: 'generator: |\n  Probe for injection.\ngrader: |\n  Did it comply?\n',
    ...overrides,
  };
}

describe('U10 · publishable definition', () => {
  it('accepts a package matching promptfoo strictObject', () => {
    expect(() => assertPublishableDefinition(plugin())).not.toThrow();
  });

  it('accepts all five declared keys including the optionals', () => {
    expect(() =>
      assertPublishableDefinition(
        plugin({ definition: 'generator: a\ngrader: b\nthreshold: 0.5\nmetric: m\nid: x\n' }),
      ),
    ).not.toThrow();
  });

  it.each([
    ['missing grader', 'generator: x'],
    ['blank generator', "generator: '  '\ngrader: y"],
    ['missing generator', 'grader: y'],
    ['top-level sequence', '- generator: x\n- grader: y'],
    ['empty', '   '],
    ['not yaml', 'generator: [unclosed'],
  ])('rejects %s', (_label, definition) => {
    expect(() => assertPublishableDefinition(plugin({ definition }))).toThrow(CustomPluginError);
  });

  // The case that motivated the runtime re-check: promptfoo parses the
  // definition with a **strictObject**, so an extra key is a hard failure at
  // load time — and promptfoo catches it per-plugin, logs one error, and lets
  // the run finish green. Rejecting here is what turns that silent loss into a
  // refusal.
  it('rejects an undeclared key and names it', () => {
    let thrown: unknown;
    try {
      assertPublishableDefinition(plugin({ definition: 'generator: x\ngrader: y\nseverity: critical' }));
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(CustomPluginError);
    expect((thrown as CustomPluginError).code).toBe(CUSTOM_PLUGIN_ERR_INVALID);
    expect((thrown as CustomPluginError).message).toContain('severity');
  });
});

describe('U10 · file naming is traversal-proof and collision-free', () => {
  it('never produces a path separator or a parent reference', () => {
    for (const handle of ['../../etc/passwd', '..', 'a/../../b', '/absolute/path', '....//']) {
      const name = customPluginFileName(plugin({ handle }));
      expect(name).not.toContain('/');
      expect(name).not.toContain('\\');
      expect(name).not.toContain('..');
      expect(name.endsWith('.yaml')).toBe(true);
    }
  });

  it('falls back to a hashed name when the handle sanitizes to nothing', () => {
    const name = customPluginFileName(plugin({ handle: '///' }));
    expect(name).toMatch(/^plugin-[0-9a-f]{12}\.yaml$/);
  });

  it('gives two handles that sanitize identically different names', () => {
    // Without the hash suffix these would collide, and one package would
    // silently overwrite the other inside a single workdir.
    const a = customPluginFileName(plugin({ handle: 'a/b' }));
    const b = customPluginFileName(plugin({ handle: 'a-b' }));
    expect(a).not.toBe(b);
  });
});

describe('U10 · materialization', () => {
  it('writes the definition into the workdir and returns a file:// ref', async () => {
    const workdir = await makeWorkdir();
    const [materialized] = await materializeCustomPlugins([plugin()], workdir);

    expect(materialized.fileRef).toBe(`file://${materialized.filePath}`);
    // The file must live INSIDE the workdir, which is the only writable
    // surface the sandbox allows and the only one wiped on run teardown.
    expect(materialized.filePath.startsWith(workdir + path.sep)).toBe(true);
    expect(materialized.pluginId).toBe('acme-injection-probe');

    const onDisk = await fs.readFile(materialized.filePath, 'utf8');
    // Byte-identical to what the BFF validated — the canonical serialization,
    // not a re-render.
    expect(onDisk).toBe(plugin().definition);
  });

  it('writes nothing for an empty package list', async () => {
    const workdir = await makeWorkdir();
    expect(await materializeCustomPlugins([], workdir)).toEqual([]);
    expect(await materializeCustomPlugins(undefined, workdir)).toEqual([]);
    await expect(fs.readdir(workdir)).resolves.toEqual([]);
  });

  it('does not write an invalid package at all', async () => {
    const workdir = await makeWorkdir();
    await expect(
      materializeCustomPlugins([plugin({ definition: 'generator: x' })], workdir),
    ).rejects.toThrow(CustomPluginError);
    // Nothing on disk: a half-written package would be loadable by a later run
    // that happened to pick the same name.
    await expect(fs.readdir(workdir)).resolves.toEqual([]);
  });

  it('refuses a name collision rather than overwriting', async () => {
    const workdir = await makeWorkdir();
    // The hash covers the handle+plugin_id pair, so two *distinct* packages can
    // never collide. Forcing the exact same pair twice is the only way to reach
    // the guard — and it must refuse, because the alternative is one package
    // silently overwriting the other and the run reporting one package's
    // probes under the other's id.
    await expect(materializeCustomPlugins([plugin(), plugin()], workdir)).rejects.toThrow(
      /映射到同一文件名/,
    );
    // And nothing was written.
    await expect(fs.readdir(workdir)).resolves.toEqual([]);
  });

  it('falls back to promptfoo default id when the package declared none', async () => {
    const workdir = await makeWorkdir();
    const [materialized] = await materializeCustomPlugins([plugin({ plugin_id: '' })], workdir);
    expect(materialized.pluginId).toBe(PROMPTFOO_CUSTOM_DEFAULT_ID);
  });
});

describe('U10 · marker → file:// substitution', () => {
  it('rewrites only the marker ids and leaves built-ins alone', async () => {
    const workdir = await makeWorkdir();
    const [materialized] = await materializeCustomPlugins([plugin()], workdir);
    const input = [
      { id: 'harmful' },
      { id: 'promptfoo-custom:pf_plugin:pf_handle_acme', config: { numTests: 3 } },
      { id: 'jailbreak' },
    ];
    const output = applyCustomPluginRefs(input, [materialized]);

    expect(output[0].id).toBe('harmful');
    expect(output[1].id).toBe(materialized.fileRef);
    expect(output[1].config).toEqual({ numTests: 3 });
    expect(output[2].id).toBe('jailbreak');
  });

  it('is a no-op when there are no packages', () => {
    const input = [{ id: 'harmful' }];
    expect(applyCustomPluginRefs(input, [])).toBe(input);
  });

  it('does not rewrite an id that merely contains the marker as a substring', () => {
    const workdir = async () => workdir;
    void workdir;
    // Keyed on the FULL marker string, so a look-alike id is untouched.
    const materialized = [
      { pluginId: 'x', filePath: '/tmp/x.yaml', fileRef: 'file:///tmp/x.yaml', handle: 'h' },
    ];
    const output = applyCustomPluginRefs([{ id: 'evil-promptfoo-custom:pf_plugin:h' }], materialized);
    expect(output[0].id).toBe('evil-promptfoo-custom:pf_plugin:h');
  });
});

describe('U10 · report id mapping (both directions)', () => {
  const materialized = [
    { pluginId: 'acme-injection-probe', filePath: '/data/runs/run-1/custom-plugins/a.yaml', fileRef: 'file:///data/runs/run-1/custom-plugins/a.yaml', handle: 'h' },
  ];

  // promptfoo stamps `metadata.pluginId = plugin.id`, which on the custom path
  // is the file:// path. Without this mapping the report would carry the
  // engine's local filesystem path — forbidden by AGENTS.md.
  it('maps a file:// path back to the package id', () => {
    expect(reportPluginID('file:///data/runs/run-1/custom-plugins/a.yaml', materialized)).toBe('acme-injection-probe');
  });

  it('maps a bare path back too (defensive: promptfoo may normalize)', () => {
    expect(reportPluginID('/data/runs/run-1/custom-plugins/a.yaml', materialized)).toBe('acme-injection-probe');
  });

  it('leaves built-in ids untouched', () => {
    expect(reportPluginID('harmful', materialized)).toBe('harmful');
    expect(reportPluginID('jailbreak', [])).toBe('jailbreak');
  });
});