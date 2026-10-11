/**
 * U10 · sandbox verification tests.
 *
 * # What is and is not proven here
 *
 * The design doc calls the sandbox the batch's heaviest security item and names
 * three properties: read-only root, non-root, no external network. This file
 * tests **what can be tested without a container**, and is explicit about the
 * rest:
 *
 *  - `checkNonRoot` — fully testable, and the one that is *fatal* at boot.
 *  - `checkReadonlyRoot` / `checkNetworkEgress` in `probe: false` mode — tested
 *    for the "declared, not verified" wording. That wording is the point: a
 *    report that said "verified" without having probed anything would be the
 *    exact failure this module was written to prevent.
 *  - The live probes themselves — **not** exercised. A dev machine has a
 *    writable root and working internet, so asserting "the write fails" here
 *    would assert something false. `scripts/verify_u10_sandbox.sh` runs the real
 *    counter-proof inside the container; it exits 2 (SKIP), not 0, when Docker
 *    is absent, so "not run" never reads as "passed".
 */
import { describe, expect, it } from 'vitest';
import {
  checkNetworkEgress,
  checkNonRoot,
  checkReadonlyRoot,
  formatSandboxReport,
  READONLY_ROOT_ENV,
  SANDBOX_PROBE_ENV,
  verifySandbox,
  writableRoot,
} from '../src/sandbox.js';

describe('U10 · non-root', () => {
  it('reports the actual uid, and never claims verified for root', () => {
    const property = checkNonRoot();
    expect(property.property).toBe('non_root');
    // The test process may or may not be root depending on the machine; what
    // must hold is that the verdict follows the uid rather than being hardcoded.
    if (process.getuid?.() === 0) {
      expect(property.verdict).toBe('failed');
      expect(property.detail).toContain('root');
    } else {
      expect(property.verdict).toBe('verified');
      expect(property.detail).toContain('uid=');
    }
  });
});

describe('U10 · read-only root: declared is not verified', () => {
  it('reports "declared" when the contract is set but no probe ran', async () => {
    // Set the declaration explicitly rather than relying on ambient env: in a
    // dev shell it is legitimately absent, and a test that passed or failed
    // based on the developer's shell would be a test of the environment.
    const saved = process.env[READONLY_ROOT_ENV];
    process.env[READONLY_ROOT_ENV] = '1';
    try {
      const property = await checkReadonlyRoot(false);
      expect(property.property).toBe('readonly_root');
      expect(property.verdict).toBe('declared');
      expect(property.detail).toContain('未实际写入探测');
    } finally {
      if (saved === undefined) delete process.env[READONLY_ROOT_ENV];
      else process.env[READONLY_ROOT_ENV] = saved;
    }
  });

  it('reports "unavailable" when the contract is absent', async () => {
    // Temporarily unset the declaration so the "cannot tell" branch is real
    // rather than simulated by passing a flag.
    const saved = process.env[READONLY_ROOT_ENV];
    delete process.env[READONLY_ROOT_ENV];
    try {
      const property = await checkReadonlyRoot(false);
      expect(property.verdict).toBe('unavailable');
      expect(property.detail).toContain('read_only');
    } finally {
      if (saved !== undefined) process.env[READONLY_ROOT_ENV] = saved;
    }
  });
});

describe('U10 · network egress: declared is not verified', () => {
  it('reports "declared" when no probe ran, and names the real property', async () => {
    const property = await checkNetworkEgress(false);
    expect(property.property).toBe('no_unmanaged_egress');
    expect(property.verdict).toBe('declared');
    // The message must not imply the engine has no network at all — it must,
    // and does, call the model endpoint.
    expect(property.detail).toContain('平台下发的模型端点');
  });
});

describe('U10 · report shape', () => {
  it('separates "declared" from "verified" and exposes whether anything was probed', async () => {
    const report = await verifySandbox(false);
    expect(report.probed).toBe(false);
    expect(report.ok).toBe(true);
    // Every property must carry an explicit verdict word. A boolean here would
    // erase the distinction the whole module exists to preserve.
    for (const property of report.properties) {
      expect(['verified', 'declared', 'failed', 'unavailable']).toContain(property.verdict);
      expect(property.detail.length).toBeGreaterThan(0);
    }
    expect(report.properties.map((p) => p.property)).toEqual(
      expect.arrayContaining(['non_root', 'readonly_root', 'no_unmanaged_egress', 'writable_root']),
    );
  });

  it('names the writable root, which is the only write surface claimed', async () => {
    expect(writableRoot()).toBe(process.env['PROMPTFOO_ENGINE_DATA_ROOT'] ?? '/data/runs');
  });

  it('formats one line per property with the verdict inline', async () => {
    const lines = formatSandboxReport(await verifySandbox(false));
    expect(lines[0]).toContain(SANDBOX_PROBE_ENV);
    expect(lines.some((l) => l.includes('non_root='))).toBe(true);
    expect(lines[lines.length - 1]).toContain('ok=');
  });

  it('says "declared-only" up front when nothing was probed', async () => {
    const lines = formatSandboxReport(await verifySandbox(false));
    expect(lines[0]).toContain('declared-only');
  });
});