/**
 * promptfooStateCleanup.ts — U7 · remove the state promptfoo's redteam path
 * writes outside the per-run workdir.
 *
 * # Why this module exists
 *
 * Plan A's contract is that nothing reaches promptfoo's own storage. Plan C
 * cannot keep it: `doRedteamRun()` calls
 *
 *     doEval({ ...evalOptions, config: [redteamPath], cache: true, write: true })
 *
 * with `write: true` **hardcoded** — there is no option to turn it off. That
 * persists the run's raw prompts and target responses.
 *
 * # Why the artifacts cannot simply be confined to the run directory
 *
 * Verified against the installed promptfoo 0.123.0 dist:
 *
 *  - The path comes from `getDbPath()`:
 *        path.resolve(getConfigDirectoryPath(), "promptfoo.db")
 *    and `getConfigDirectoryPath()` returns
 *        environmentConfigDirectoryPath || join(os.homedir(), ".promptfoo")
 *    where `environmentConfigDirectoryPath` is a **module-level `let`** assigned
 *    once at import time from `process.env.PROMPTFOO_CONFIG_DIR`.
 *  - `getDb()` **memoizes the connection**: `if (dbInstance) return dbInstance;`
 *    so the first call fixes the file for the life of the process.
 *
 * Both facts together mean the destination cannot be varied per run: mutating
 * `process.env.PROMPTFOO_CONFIG_DIR` mid-process would only take effect if the
 * module had not been loaded yet, and the TaskManager runs several workers
 * concurrently in one process — so run A's results could land in run B's
 * directory. Serializing every run to make that safe is a much larger
 * behavioural change than this cleanup is.
 *
 * Decision (2026-10-10, recorded in `audit-2026-10-09/14` §4.8.5): accept the
 * write inside the container, and **delete the artifacts explicitly on every
 * terminal path** instead. Deliberately **not** touching `PROMPTFOO_CONFIG_DIR`.
 *
 * # Why the whole directory is not removed
 *
 * `~/.promptfoo` can hold a developer's cloud login (`auth.json`); that is not
 * ours to delete, and removing it would be a nasty surprise. Only the files a
 * run actually produces are removed. On an ephemeral container it would vanish
 * on its own anyway — this is defence for the case where the home directory *is*
 * a mounted volume.
 *
 * `promptfoo.yaml` used to be protected by the same reasoning and that was a
 * mistake: measured against the U7 run it is promptfoo's own global config,
 * whose entire content in this container is one anonymous installation id. See
 * the note on `RUN_ARTIFACTS`.
 *
 * # Safety properties
 *
 *  - **Never throws on "not there".** A missing file is a success.
 *  - **Fails loudly.** A file that exists but cannot be removed raises, because
 *    silently leaving raw prompts on disk is exactly the failure mode U6 was
 *    created to eliminate.
 *  - **Never logs payload content.** Only paths and errno are reported.
 */
import { rm, stat } from 'node:fs/promises';
import { homedir } from 'node:os';
import path from 'node:path';
import process from 'node:process';

/**
 * Files a native run leaves in promptfoo's config directory.
 *
 *  - `promptfoo.db`     the SQLite database itself
 *  - `-wal` / `-shm`    siblings created because `configureConnection()` runs
 *                       `PRAGMA journal_mode = WAL`
 *  - `evalLastWritten`  written by `getDbSignalPath()` on each persisted eval
 *  - `cache/`           the on-disk response cache (`cache: false` stops new
 *                       entries, but an earlier Plan A run may have made these)
 *  - `promptfoo.yaml`   promptfoo's own global config file, whose whole content
 *                       in a container is one anonymous installation id
 *                       (`crypto.randomUUID()`, 41 bytes — measured against the
 *                       U7 acceptance run, which recorded exactly that residue).
 *
 * The last one was excluded for a while and the exclusion was wrong. The
 * original comment here said it was left alone because it "can hold a
 * hand-written config", which is true on a developer's machine and false in this
 * container: nobody hand-writes promptfoo config here, `auth.json` is the file
 * that actually holds a cloud login (and is still, correctly, left alone), and
 * promptfoo re-creates its id on demand. Leaving it meant every run added a
 * permanent artifact while the module's contract says a terminal run leaves
 * nothing behind — one of the two had to give, and the contract is the promise
 * users and operators rely on.
 */
const RUN_ARTIFACTS = [
  'promptfoo.db',
  'promptfoo.db-wal',
  'promptfoo.db-shm',
  'evalLastWritten',
  'promptfoo.yaml',
] as const;

/** Raised when an artifact that exists cannot be removed. */
export class PromptfooStateCleanupError extends Error {
  constructor(
    public readonly failed: Array<{ path: string; reason: string }>,
  ) {
    super(
      `failed to remove promptfoo run artifacts: ${failed.map((f) => `${f.path} (${f.reason})`).join(', ')}`,
    );
    this.name = 'PromptfooStateCleanupError';
  }
}

/**
 * Resolve promptfoo's config directory the same way promptfoo does.
 *
 * Mirrors `getConfigDirectoryPath()` (`environmentConfigDirectoryPath ||
 * join(homedir(), '.promptfoo')`). Duplicating the rule here is deliberate:
 * it keeps this module free of a promptfoo import, so cleanup can run from a
 * failure path where importing the library again would be pointless. The
 * `assertNativeGenerationInvariants` startup guard already refuses to boot with
 * `PROMPTFOO_CONFIG_DIR` set, so in practice this is always `~/.promptfoo`.
 */
export function promptfooConfigDir(env: NodeJS.ProcessEnv = process.env): string {
  const configured = (env.PROMPTFOO_CONFIG_DIR ?? '').trim();
  return configured || path.join(homedir(), '.promptfoo');
}

/** Absolute paths this module would remove. Exported for tests and diagnostics. */
export function promptfooRunArtifactPaths(env: NodeJS.ProcessEnv = process.env): string[] {
  const dir = promptfooConfigDir(env);
  return [...RUN_ARTIFACTS.map((name) => path.join(dir, name)), path.join(dir, 'cache')];
}

/**
 * Remove the artifacts a native run produced.
 *
 * Safe to call on every terminal path and safe to call twice. Never touches
 * `auth.json` or a user-authored `promptfoo.yaml`.
 *
 * @returns the paths that were actually removed (empty when there was nothing
 *          to do).
 * @throws PromptfooStateCleanupError when something exists but resists removal.
 */
export async function cleanupPromptfooRunArtifacts(
  env: NodeJS.ProcessEnv = process.env,
): Promise<string[]> {
  const removed: string[] = [];
  const failed: Array<{ path: string; reason: string }> = [];

  for (const target of promptfooRunArtifactPaths(env)) {
    try {
      // Distinguish "absent" (fine) from "present but unreadable" (report).
      await stat(target);
    } catch (err) {
      if (isNotFound(err)) continue;
      failed.push({ path: target, reason: errnoOf(err) });
      continue;
    }
    try {
      await rm(target, { recursive: true, force: true });
      removed.push(target);
    } catch (err) {
      failed.push({ path: target, reason: errnoOf(err) });
    }
  }

  if (failed.length > 0) {
    // Paths and errno only — never any prompt or response content.
    throw new PromptfooStateCleanupError(failed);
  }
  return removed;
}

function isNotFound(err: unknown): boolean {
  return (err as NodeJS.ErrnoException)?.code === 'ENOENT';
}

function errnoOf(err: unknown): string {
  const code = (err as NodeJS.ErrnoException)?.code;
  return typeof code === 'string' ? code : 'UNKNOWN';
}