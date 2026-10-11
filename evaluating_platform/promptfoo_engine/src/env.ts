/**
 * Environment loading with startup assertions.
 *
 * Security invariants (fail-closed, from the merge plan R2):
 *  - remote generation MUST be disabled
 *  - telemetry MUST be disabled
 *  - share/cloud export MUST be disabled
 * The service refuses to start if any of these switches is missing or not
 * set to a truthy "disabled" value, so a misconfigured container can never
 * silently exfiltrate data.
 *
 * U7 adds a fifth switch, `PROMPTFOO_ENGINE_NATIVE_GENERATION`, which is NOT a
 * security switch: it only chooses *which generation path* a run takes (Plan A
 * platform-synthesized prompts vs Plan C promptfoo's own redteam generation).
 * It is **off by default** and is deliberately independent of the four
 * fail-closed switches above — turning it on must never relax any of them.
 * See `assertNativeGenerationInvariants` for the (additive) invariants it does
 * enforce, and nativeRedteam.ts for why they are load-bearing rather than
 * decorative.
 */
import process from 'node:process';

export interface EngineEnv {
  port: number;
  bearerToken: string;
  dataRoot: string;
  workerCount: number;
  maxQueueDepth: number;
  /** U7: run promptfoo's own redteam generation instead of Plan A. Default false. */
  nativeGeneration: boolean;
}

function parseBool(v: string | undefined): boolean {
  if (!v) return false;
  return ['1', 'true', 'yes', 'on'].includes(v.trim().toLowerCase());
}

function parseIntEnv(v: string | undefined, fallback: number, min: number, max: number): number {
  const n = Number.parseInt(v ?? '', 10);
  if (Number.isNaN(n)) return fallback;
  return Math.min(max, Math.max(min, n));
}

/** Assert exfiltration switches are all disabled; throw otherwise. */
export function assertSecuritySwitches(env: NodeJS.ProcessEnv = process.env): void {
  const switches: Array<[string, string]> = [
    ['PROMPTFOO_DISABLE_REMOTE_GENERATION', 'remote generation'],
    ['PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION', 'redteam remote generation'],
    ['PROMPTFOO_DISABLE_TELEMETRY', 'telemetry'],
    ['PROMPTFOO_DISABLE_SHARE', 'share/cloud export'],
  ];
  const offending = switches.filter(([key]) => !parseBool(env[key])).map(([, label]) => label);
  if (offending.length > 0) {
    throw new Error(
      `[FATAL] promptfoo-engine refuses to start: the following exfiltration channels are not disabled: ${offending.join(', ')}. ` +
        'Set PROMPTFOO_DISABLE_REMOTE_GENERATION=1, PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION=1, ' +
        'PROMPTFOO_DISABLE_TELEMETRY=1, PROMPTFOO_DISABLE_SHARE=1.',
    );
  }
}

export function loadEnv(env: NodeJS.ProcessEnv = process.env): EngineEnv {
  assertSecuritySwitches(env);
  assertNativeGenerationInvariants(env);

  const bearerToken = env.PROMPTFOO_ENGINE_BEARER_TOKEN ?? '';
  if (bearerToken.length < 16) {
    throw new Error(
      '[FATAL] PROMPTFOO_ENGINE_BEARER_TOKEN must be set to a random secret of at least 16 characters.',
    );
  }

  const dataRoot = env.PROMPTFOO_ENGINE_DATA_ROOT ?? '/data/runs';
  if (!dataRoot) {
    throw new Error('[FATAL] PROMPTFOO_ENGINE_DATA_ROOT must not be empty.');
  }

  return {
    port: parseIntEnv(env.PROMPTFOO_ENGINE_PORT, 8090, 1, 65535),
    bearerToken,
    dataRoot,
    workerCount: parseIntEnv(env.PROMPTFOO_ENGINE_WORKERS, 2, 1, 8),
    maxQueueDepth: parseIntEnv(env.PROMPTFOO_ENGINE_MAX_QUEUE, 100, 1, 1000),
    nativeGeneration: nativeGenerationEnabled(env),
  };
}

/** The U7 path-selection switch. Anything other than a truthy value means Plan A. */
export const NATIVE_GENERATION_SWITCH = 'PROMPTFOO_ENGINE_NATIVE_GENERATION';

/**
 * Whether Plan C (promptfoo's own redteam generation) is selected.
 *
 * Default-off by construction: `parseBool` maps both "unset" and every
 * unrecognized spelling to false, so a typo in a compose file degrades to the
 * known-good Plan A rather than to the new path. Rolling back U7 is therefore
 * a one-variable operation — no code revert, no data migration.
 */
export function nativeGenerationEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return parseBool(env[NATIVE_GENERATION_SWITCH]);
}

/**
 * U10 · Sandbox environment keys.
 *
 * `PROMPTFOO_ENGINE_READONLY_ROOT=1` is the engine's assertion that the
 * container runs with a read-only root filesystem (compose: `read_only: true`).
 * It is declared rather than probed by default, because there is no portable
 * Node API for "is this filesystem read-only" — see `sandbox.ts` for the full
 * reasoning and for the live probe (`PROMPTFOO_ENGINE_SANDBOX_PROBE=1`) that
 * does test kernel behaviour.
 *
 * These are **additive**: nothing here relaxes any of the four fail-closed
 * switches above, and every one of them only ever refuses.
 */
export const READONLY_ROOT_SWITCH = 'PROMPTFOO_ENGINE_READONLY_ROOT';
export const SANDBOX_PROBE_SWITCH = 'PROMPTFOO_ENGINE_SANDBOX_PROBE';

/**
 * U10 · Fail-closed sandbox invariants.
 *
 * Only the **non-root** check is fatal, and that is a considered choice rather
 * than an omission:
 *
 *  - Running as root makes every other boundary meaningless — a read-only root
 *    filesystem and a dropped capability set are both bypassed by uid 0, so a
 *    root container must not start. This one is checked at boot.
 *  - The read-only and egress properties are asserted at boot **only when the
 *    operator has declared the live probe**, because outside a container a dev
 *    machine legitimately fails both. Refusing to boot there would make the
 *    engine untestable on a laptop, which is where a surprising share of its
 *    regressions are found.
 *
 * Every check's verdict is recorded distinctly (`verified` / `declared` /
 * `failed` / `unavailable`) so a reader can never mistake "declared" for
 * "verified". That distinction is the whole reason this layer exists.
 */
export async function assertSandboxInvariants(env: NodeJS.ProcessEnv = process.env): Promise<void> {
  const { verifySandbox } = await import('./sandbox.js');
  const liveProbe = parseBool(env[SANDBOX_PROBE_SWITCH]);
  const report = await verifySandbox(liveProbe);
  const failed = report.properties.filter((p) => p.verdict === 'failed');
  if (failed.length > 0) {
    throw new Error(
      `[FATAL] promptfoo-engine refuses to start: the execution sandbox is not intact: ` +
        failed.map((p) => `${p.property} (${p.detail})`).join('; '),
    );
  }
  const nonRoot = report.properties.find((p) => p.property === 'non_root');
  if (nonRoot?.verdict !== 'verified') {
    throw new Error(
      `[FATAL] promptfoo-engine refuses to start: ${nonRoot?.detail ?? 'uid unknown'}. ` +
        'The engine holds a materialized generation credential in its workdir; it must not run as root.',
    );
  }
}

/**
 * U7 · additive invariants for the native-generation path.
 *
 * These do NOT relax anything in `assertSecuritySwitches`; they are extra
 * refusals that only ever fire when the new switch is ON. Rationale, verified
 * against the installed promptfoo 0.123.0 dist:
 *
 *  1. `OPENAI_API_KEY` must be absent. promptfoo's `shouldGenerateRemote()`
 *     returns true when that variable is set even with both remote switches
 *     disabled, which would re-open the remote-generation channel this engine
 *     is fail-closed on. Its `loadRedteamProvider` would also silently fall
 *     back to `openai:gpt-5.5-...` (i.e. api.openai.com) for the generation
 *     slot. Refusing to boot is the only honest response.
 *
 *  2. `PROMPTFOO_REMOTE_GENERATION_URL` must be absent for the same reason:
 *     `shouldGenerateRemote()` short-circuits to true when it is present.
 *
 *  3. No Promptfoo Cloud login may be visible to the engine. `isLoggedIntoCloud()`
 *     is also a `shouldGenerateRemote()` trigger, and it reads a credentials
 *     file rather than the environment, so it can only be asserted, not proven
 *     absent. Pointing `PROMPTFOO_CONFIG_DIR` at a per-run-empty directory is
 *     how an operator states that intent; the variable being set at all is
 *     what we check, because a login is then the only cloud signal left.
 */
export function assertNativeGenerationInvariants(env: NodeJS.ProcessEnv = process.env): void {
  if (!nativeGenerationEnabled(env)) return;

  const offending: string[] = [];
  if ((env.OPENAI_API_KEY ?? '').trim()) offending.push('OPENAI_API_KEY');
  if ((env.PROMPTFOO_REMOTE_GENERATION_URL ?? '').trim()) offending.push('PROMPTFOO_REMOTE_GENERATION_URL');
  if ((env.PROMPTFOO_CONFIG_DIR ?? '').trim()) {
    offending.push('PROMPTFOO_CONFIG_DIR (would expose a Promptfoo Cloud login to the library)');
  }
  if (offending.length > 0) {
    throw new Error(
      `[FATAL] ${NATIVE_GENERATION_SWITCH} is on but ${offending.join(', ')} ${offending.length > 1 ? 'are' : 'is'} set. ` +
        'Native generation talks only to the platform-supplied provider endpoint; promptfoo treats those variables as ' +
        'consent to use its remote generation service, which this engine must never do. ' +
        `Unset ${offending.join(', ')} or turn ${NATIVE_GENERATION_SWITCH} off.`,
    );
  }
}
