/**
 * U8 E2E · promptfoo home redirection — **must be imported before anything
 * that imports promptfoo**.
 *
 * # Why this is a separate module
 *
 * promptfoo reads `PROMPTFOO_CONFIG_DIR` into a **module-level** variable when
 * it is first imported (`getConfigDirectoryPath()` uses
 * `environmentConfigDirectoryPath || ~/.promptfoo`). ESM evaluates all of a
 * file's imports before that file's body, so setting `process.env` at the top of
 * the test body is already too late — the real `~/.promptfoo` has been latched.
 * This is the same hoisting trap U7 hit in production (§4.8.5) and had to add
 * `promptfooStateCleanup` for; here it is simply avoided by ordering.
 *
 * A side-effect module imported *first* is the one construct that runs early
 * enough. `tests/u8BothPaths.e2e.test.ts` therefore has this import above its
 * `promptfoo`-importing ones — do not reorder it.
 *
 * # Why a temp dir rather than IS_TESTING
 *
 * `IS_TESTING=true` gives promptfoo an **in-memory** database that is created
 * without its schema; the first query then dies with
 * `SQLITE_ERROR: no such table: eval_results`. A real temp directory gets a
 * migrated database, and it is removed on process exit so nothing is left in
 * the developer's home directory.
 */
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const home = mkdtempSync(join(tmpdir(), 'u8-e2e-pfhome-'));
process.env['PROMPTFOO_CONFIG_DIR'] = home;

/**
 * Seed the fresh database with promptfoo's own schema, before any test imports
 * promptfoo (see the header for why that ordering matters).
 *
 * # Why this is needed
 *
 * `checkRedteamProbeLimit()` — which Plan C's `doRedteamRun` calls before
 * generating anything — queries the `eval_results` table. On a brand-new
 * database that table does not exist yet, because promptfoo only runs its
 * migrations at `runDbMigrations()` call sites (evaluate with
 * `writeLatestResults`, CLI with `--write`), **neither of which has happened by
 * the time the probe limit is checked**. The result is
 * `SQLITE_ERROR: no such table: eval_results`.
 *
 * `IS_TESTING=true` avoids that error but swaps in an in-memory database with
 * no schema either, so it fails the same way. Running promptfoo's shipped
 * drizzle migrations against the temp file is what an installed deployment
 * would already have.
 *
 * Imported dynamically rather than statically because drizzle pulls in native
 * bindings, and we want this to run after `PROMPTFOO_CONFIG_DIR` is set.
 */
const migrations = await import('drizzle-orm/libsql/migrator');
const { createClient } = await import('@libsql/client/node');
const { drizzle } = await import('drizzle-orm/libsql/node');
const client = createClient({ url: `file:${join(home, 'promptfoo.db')}` });
await migrations.migrate(drizzle(client), {
  migrationsFolder: new URL('../node_modules/promptfoo/dist/drizzle', import.meta.url).pathname,
});
client.close();

// Clean up even on an abnormal exit, so a failed run does not leave a
// promptfoo.db in the temp directory for the next run to trip over.
const cleanup = (): void => {
  try {
    rmSync(home, { recursive: true, force: true });
  } catch {
    // Best effort: a leftover temp dir must never fail the test suite.
  }
};
process.once('exit', cleanup);

export const U8_PROMPTFOO_HOME = home;