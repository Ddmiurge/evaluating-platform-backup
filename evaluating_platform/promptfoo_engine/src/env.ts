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
 * P3-3 removed the `PROMPTFOO_ENGINE_NATIVE_GENERATION` switch together with Plan
 * A, whose only purpose it served. promptfoo's own redteam generation is now the
 * single path, so the invariants that used to apply "when native generation is
 * on" apply always — see `assertNoRemoteGenerationConsent`, which is why it is
 * called unconditionally rather than gated behind a switch.
 */
import { existsSync } from 'node:fs';
import path from 'node:path';
import process from 'node:process';

export interface EngineEnv {
  port: number;
  bearerToken: string;
  dataRoot: string;
  /**
   * The CONFIGURED worker count. It is reported (and ignored) rather than removed:
   * the engine runs one worker by construction (see `ENGINE_WORKER_COUNT`), and an
   * operator who set this variable deserves to be told so in the boot log instead
   * of wondering why throughput does not change.
   */
  workerCount: number;
  maxQueueDepth: number;
}

/**
 * Workers the engine runs with.
 *
 * One, and not as a preference: promptfoo keeps a single database file per process
 * (`PROMPTFOO_CONFIG_DIR` is latched at import, `getDb()` memoizes the connection)
 * and the terminal cleanup deletes it — so two concurrent runs delete each other's
 * results and one of them loses its schema mid-run. Measured on the U7 probe: after
 * a cleanup and a re-migrate, run 2 still read run 1's rows through the memoized
 * handle while the path held a different database.
 */
export const ENGINE_WORKER_COUNT = 1;

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
  assertNoRemoteGenerationConsent(env);

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
  };
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

export function assertNoRemoteGenerationConsent(env: NodeJS.ProcessEnv = process.env): void {
  const offending: string[] = [];
  if ((env.OPENAI_API_KEY ?? '').trim()) offending.push('OPENAI_API_KEY');
  if ((env.PROMPTFOO_REMOTE_GENERATION_URL ?? '').trim()) offending.push('PROMPTFOO_REMOTE_GENERATION_URL');
  // P3-3：这一条原本禁止设置 `PROMPTFOO_CONFIG_DIR`。禁止的是一个**代理指标**，而不是
  // 危害本身：真正的危害是「库能看到一个 Promptfoo Cloud 登录」，而把配置目录指向隔离
  // 目录恰恰是测试与挂载卷部署用来把引擎状态挡在开发者 home 之外的手段（P2-3 的
  // 生命周期模块也正是按这个变量解析库路径的）。所以改为只拒绝**登录实际存在**的情形。
  const configDir = (env.PROMPTFOO_CONFIG_DIR ?? '').trim();
  if (configDir) {
    if (existsSync(path.join(configDir, 'auth.json'))) {
      offending.push(`PROMPTFOO_CONFIG_DIR (contains auth.json — a Promptfoo Cloud login) at ${configDir}`);
    }
  }
  if (offending.length > 0) {
    throw new Error(
      `[FATAL] the engine talks only to the platform-supplied endpoints, but ${offending.join(', ')} ` +
        `${offending.length > 1 ? 'are' : 'is'} set. promptfoo reads those variables as consent to use its own ` +
        'remote generation service, which this engine must never do. Refusing to start rather than booting with ' +
        `an exfiltration channel open: unset ${offending.join(', ')}.`,
    );
  }
}
