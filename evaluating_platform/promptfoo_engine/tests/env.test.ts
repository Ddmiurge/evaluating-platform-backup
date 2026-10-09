import { describe, expect, it } from 'vitest';
import { assertSecuritySwitches, loadEnv } from '../src/env.js';

function env(overrides: Record<string, string | undefined>): NodeJS.ProcessEnv {
  return {
    PROMPTFOO_DISABLE_REMOTE_GENERATION: '1',
    PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION: '1',
    PROMPTFOO_DISABLE_TELEMETRY: '1',
    PROMPTFOO_DISABLE_SHARE: '1',
    PROMPTFOO_ENGINE_BEARER_TOKEN: 'x'.repeat(24),
    PROMPTFOO_ENGINE_PORT: '8090',
    PROMPTFOO_ENGINE_DATA_ROOT: '/data/runs',
    PROMPTFOO_ENGINE_WORKERS: '2',
    PROMPTFOO_ENGINE_MAX_QUEUE: '100',
    ...overrides,
  } as NodeJS.ProcessEnv;
}

describe('assertSecuritySwitches (fail-closed)', () => {
  it('passes when all four switches are disabled', () => {
    expect(() => assertSecuritySwitches(env({}))).not.toThrow();
  });

  it('accepts alternative truthy spellings', () => {
    expect(() =>
      assertSecuritySwitches(
        env({
          PROMPTFOO_DISABLE_REMOTE_GENERATION: 'true',
          PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION: 'yes',
          PROMPTFOO_DISABLE_TELEMETRY: 'on',
          PROMPTFOO_DISABLE_SHARE: 'TRUE',
        }),
      ),
    ).not.toThrow();
  });

  it('throws when remote generation is enabled', () => {
    expect(() => assertSecuritySwitches(env({ PROMPTFOO_DISABLE_REMOTE_GENERATION: '0' }))).toThrow(
      /remote generation/,
    );
  });

  it('throws when a switch is missing entirely', () => {
    expect(() =>
      assertSecuritySwitches(env({ PROMPTFOO_DISABLE_TELEMETRY: undefined })),
    ).toThrow(/telemetry/);
  });

  it('lists every offending channel at once', () => {
    try {
      assertSecuritySwitches(
        env({
          PROMPTFOO_DISABLE_TELEMETRY: '0',
          PROMPTFOO_DISABLE_SHARE: 'false',
        }),
      );
      expect.unreachable('should have thrown');
    } catch (err) {
      const msg = (err as Error).message;
      expect(msg).toContain('telemetry');
      expect(msg).toContain('share/cloud export');
    }
  });
});

describe('loadEnv', () => {
  it('rejects a short bearer token', () => {
    expect(() => loadEnv(env({ PROMPTFOO_ENGINE_BEARER_TOKEN: 'short' }))).toThrow(
      /PROMPTFOO_ENGINE_BEARER_TOKEN/,
    );
  });

  it('clamps worker count and queue depth into range', () => {
    const out = loadEnv(
      env({
        PROMPTFOO_ENGINE_WORKERS: '99',
        PROMPTFOO_ENGINE_MAX_QUEUE: '0',
        PROMPTFOO_ENGINE_PORT: '70000',
      }),
    );
    expect(out.workerCount).toBe(8);
    expect(out.maxQueueDepth).toBe(1);
    expect(out.port).toBe(65535);
  });

  it('falls back on defaults for missing numeric values', () => {
    const out = loadEnv(
      env({
        PROMPTFOO_ENGINE_WORKERS: undefined,
        PROMPTFOO_ENGINE_MAX_QUEUE: undefined,
        PROMPTFOO_ENGINE_PORT: undefined,
      }),
    );
    expect(out.workerCount).toBe(2);
    expect(out.maxQueueDepth).toBe(100);
    expect(out.port).toBe(8090);
  });
});
