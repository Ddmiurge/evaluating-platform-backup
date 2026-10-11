import { describe, expect, it } from 'vitest';
import {
  assertNativeGenerationInvariants,
  assertSecuritySwitches,
  loadEnv,
  nativeGenerationEnabled,
} from '../src/env.js';

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

/* ------------------------------------------------------------------ *
 * U7 · Plan A / Plan C path switch.
 *
 * The switch is NOT a security switch. It only chooses the generation
 * path, it is OFF by default, and it is fully independent of the four
 * fail-closed switches above — the tests below pin all three properties.
 * ------------------------------------------------------------------ */
describe('nativeGenerationEnabled (U7 path switch, default OFF)', () => {
  it('is off when the variable is absent — Plan A is the default', () => {
    expect(nativeGenerationEnabled(env({}))).toBe(false);
    expect(loadEnv(env({})).nativeGeneration).toBe(false);
  });

  // A typo in a compose file must degrade to the known-good Plan A rather than
  // silently enabling the new path.
  it.each(['0', 'false', 'no', 'off', '', '  ', 'enabled', 'yes-please', '2'])(
    'treats %j as off',
    (value) => {
      expect(nativeGenerationEnabled(env({ PROMPTFOO_ENGINE_NATIVE_GENERATION: value }))).toBe(false);
    },
  );

  it.each(['1', 'true', 'yes', 'on', 'ON', ' True '])('treats %j as on', (value) => {
    expect(nativeGenerationEnabled(env({ PROMPTFOO_ENGINE_NATIVE_GENERATION: value }))).toBe(true);
  });
});

describe('assertNativeGenerationInvariants (additive, never relaxing)', () => {
  it('does nothing while the switch is off', () => {
    // Even with the dangerous variables present: Plan A never reaches
    // promptfoo's generation path, so it must not block startup.
    expect(() =>
      assertNativeGenerationInvariants(
        env({ OPENAI_API_KEY: 'sk-present', PROMPTFOO_REMOTE_GENERATION_URL: 'https://x' }),
      ),
    ).not.toThrow();
  });

  it('passes when the switch is on and no vendor variable is set', () => {
    expect(() =>
      assertNativeGenerationInvariants(env({ PROMPTFOO_ENGINE_NATIVE_GENERATION: '1' })),
    ).not.toThrow();
  });

  // Counter-proof: with the switch ON, a stray OPENAI_API_KEY would make
  // promptfoo's shouldGenerateRemote() return true even with both remote
  // switches disabled — re-opening the exact channel this engine is fail-closed
  // on. Refusing to boot is the only honest response.
  it('refuses to boot with OPENAI_API_KEY set', () => {
    expect(() =>
      assertNativeGenerationInvariants(
        env({ PROMPTFOO_ENGINE_NATIVE_GENERATION: '1', OPENAI_API_KEY: 'sk-whatever' }),
      ),
    ).toThrow(/OPENAI_API_KEY/);
  });

  it('refuses to boot with PROMPTFOO_REMOTE_GENERATION_URL set', () => {
    expect(() =>
      assertNativeGenerationInvariants(
        env({
          PROMPTFOO_ENGINE_NATIVE_GENERATION: '1',
          PROMPTFOO_REMOTE_GENERATION_URL: 'https://selfhosted.example',
        }),
      ),
    ).toThrow(/PROMPTFOO_REMOTE_GENERATION_URL/);
  });

  it('refuses to boot when a Promptfoo Cloud login could be visible', () => {
    expect(() =>
      assertNativeGenerationInvariants(
        env({ PROMPTFOO_ENGINE_NATIVE_GENERATION: '1', PROMPTFOO_CONFIG_DIR: '/home/.promptfoo' }),
      ),
    ).toThrow(/PROMPTFOO_CONFIG_DIR/);
  });

  it('lists every offending variable at once', () => {
    try {
      assertNativeGenerationInvariants(
        env({
          PROMPTFOO_ENGINE_NATIVE_GENERATION: '1',
          OPENAI_API_KEY: 'sk-x',
          PROMPTFOO_REMOTE_GENERATION_URL: 'https://x',
        }),
      );
      expect.unreachable('should have thrown');
    } catch (err) {
      const msg = (err as Error).message;
      expect(msg).toContain('OPENAI_API_KEY');
      expect(msg).toContain('PROMPTFOO_REMOTE_GENERATION_URL');
    }
  });

  // The independence property itself: turning the new path on must never let a
  // fail-closed switch be relaxed.
  it('still enforces the four fail-closed switches with native generation ON', () => {
    expect(() =>
      assertSecuritySwitches(
        env({ PROMPTFOO_ENGINE_NATIVE_GENERATION: '1', PROMPTFOO_DISABLE_TELEMETRY: '0' }),
      ),
    ).toThrow(/telemetry/);
    expect(() => loadEnv(env({ PROMPTFOO_ENGINE_NATIVE_GENERATION: '1', PROMPTFOO_DISABLE_SHARE: '0' }))).toThrow(
      /share\/cloud export/,
    );
  });
});
