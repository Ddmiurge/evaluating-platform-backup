/**
 * remoteDepGuard.ts — U6 · refuse remote-generation-gated runs before starting.
 *
 * # Why this module exists
 *
 * promptfoo builds its remote-testcase plugins like this
 * (`dist/src/index.js:16099`, `createRemotePlugin`):
 *
 *     if (neverGenerateRemote()) {
 *         logger.error(getRemoteGenerationExplicitlyDisabledError(`${key} plugin`));
 *         return [];              // no throw, no abort, exit code still 0
 *     }
 *
 * This engine is fail-closed on that channel — `assertSecuritySwitches()` in
 * env.ts refuses to start unless PROMPTFOO_DISABLE_REMOTE_GENERATION=1 — so the
 * branch is taken for EVERY remote-dependent plugin. The run then "succeeds"
 * with 0 generated cases for that plugin, and the user sees a finished
 * evaluation and a report that silently omits a capability they selected. That
 * is the failure mode U6 exists to eliminate.
 *
 * # Why the gate lives in the engine too, not only in the BFF
 *
 * The backend already rejects gated requests (internal/maclaw/
 * promptfoo_remote_dep.go), but this engine is a separately deployable service
 * with its own REST surface. A request that reaches it directly — an older
 * BFF, a replayed capture, a future caller — must not be able to produce the
 * silent-0-coverage run. Two independent gates, one shared source of truth: the
 * BFF pushes the flag per catalog entry, so the engine needs no second copy of
 * the mapping table to stay in sync.
 *
 * This module is pure: no I/O, no promptfoo calls, no state. It decides, the
 * route decides how to report.
 */
import type { EngineCatalog } from './types.js';

/** Machine-readable error codes (mirrored by the BFF; part of the API contract). */
export const PLUGIN_REQUIRES_REMOTE_GENERATION = 'plugin_requires_remote_generation';
export const STRATEGY_REQUIRES_REMOTE_GENERATION = 'strategy_requires_remote_generation';

/**
 * Fallback explanation for a request that arrives without a catalog. The BFF
 * always ships one, so this only fires for a direct caller; the message still
 * has to name the real constraint rather than implying the plugin is unknown.
 */
export const DEFAULT_UNAVAILABLE_REASON =
  '需远程生成 · 当前环境已屏蔽（平台强制关闭 promptfoo 远程生成以防数据外泄）';

/** Capability ids requested by a run. */
export interface CapabilityRef {
  id: string;
}

/** A rejection describing every gated capability the request named. */
export class RemoteGenerationRequiredError extends Error {
  constructor(
    public readonly code: string,
    public readonly blockedPlugins: string[],
    public readonly blockedStrategies: string[],
    public readonly reason: string,
  ) {
    super(
      `${reason}：${[
        blockedPlugins.length > 0 ? `插件 ${blockedPlugins.join(', ')}` : '',
        blockedStrategies.length > 0 ? `策略 ${blockedStrategies.join(', ')}` : '',
      ]
        .filter(Boolean)
        .join('；')}`,
    );
    this.name = 'RemoteGenerationRequiredError';
  }
}

/** Capability refs as they arrive on RunRequest. */
function refsOf(list: Array<{ id: string }> | undefined): string[] {
  return (list ?? []).map((r) => (r?.id ?? '').trim()).filter((id) => id.length > 0);
}

/**
 * Reject the request when it names any capability the backend catalog marks as
 * requiring remote generation.
 *
 * Returns undefined when the run may proceed. Two deliberate decisions:
 *
 *  - **No catalog ⇒ no rejection.** The catalog is the single source of truth
 *    for the flag; inventing a local copy here would be a second table to keep
 *    in sync, and guessing would either block valid runs or miss gated ones.
 *    The BFF gate covers the catalog-less path in practice.
 *  - **Unknown ids pass.** An id absent from the catalog is not known to be
 *    gated, and rejecting it would break the existing "unknown plugin degrades to
 *    defaults" contract (U2/T2.2).
 */
export function assertNoRemoteGenerationRequired(
  plugins: Array<{ id: string }> | undefined,
  strategies: Array<{ id: string }> | undefined,
  catalog?: EngineCatalog,
): RemoteGenerationRequiredError | undefined {
  if (!catalog) return undefined;

  const gatedPlugins = new Map<string, string>();
  for (const entry of catalog.plugins ?? []) {
    if (entry?.requires_remote_generation === true) {
      gatedPlugins.set(entry.id, entry.unavailable_reason || DEFAULT_UNAVAILABLE_REASON);
    }
  }
  const gatedStrategies = new Map<string, string>();
  for (const entry of catalog.strategies ?? []) {
    if (entry?.requires_remote_generation === true) {
      gatedStrategies.set(entry.id, entry.unavailable_reason || DEFAULT_UNAVAILABLE_REASON);
    }
  }
  if (gatedPlugins.size === 0 && gatedStrategies.size === 0) return undefined;

  // De-duplicate so a repeated id is reported once.
  const blockedPlugins = [...new Set(refsOf(plugins).filter((id) => gatedPlugins.has(id)))].sort();
  const blockedStrategies = [...new Set(refsOf(strategies).filter((id) => gatedStrategies.has(id)))].sort();
  if (blockedPlugins.length === 0 && blockedStrategies.length === 0) return undefined;

  const reason =
    gatedPlugins.get(blockedPlugins[0] ?? '') ??
    gatedStrategies.get(blockedStrategies[0] ?? '') ??
    DEFAULT_UNAVAILABLE_REASON;
  const code = blockedPlugins.length > 0 ? PLUGIN_REQUIRES_REMOTE_GENERATION : STRATEGY_REQUIRES_REMOTE_GENERATION;
  return new RemoteGenerationRequiredError(code, blockedPlugins, blockedStrategies, reason);
}
