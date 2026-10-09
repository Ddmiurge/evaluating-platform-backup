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
 */
import process from 'node:process';

export interface EngineEnv {
  port: number;
  bearerToken: string;
  dataRoot: string;
  workerCount: number;
  maxQueueDepth: number;
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
