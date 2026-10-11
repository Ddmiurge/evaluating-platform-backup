/**
 * U9 Agent home redirection — **must be imported before anything that imports
 * promptfoo**.
 *
 * # Why a separate module
 *
 * Identical hazard, and identical remedy, to `u8PromptfooHome.ts`: promptfoo
 * latches `PROMPTFOO_CONFIG_DIR` into a module-level variable at first import,
 * and ESM evaluates every import of a file before that file's body. A separate
 * side-effect module is the only construct that runs early enough.
 *
 * U9 needs its own because the schema has to be migrated before *this* file's
 * promptfoo import, and because two e2e files latching different homes in one
 * process would leave the first one's database pointing at the second's
 * directory. Both e2e files set the variable, but whichever promptfoo import
 * wins is the one that latches — so U9's e2e keeps its own copy and must stay
 * self-contained.
 *
 * See `u8PromptfooHome.ts` for the full explanation of why a temp directory is
 * used instead of `IS_TESTING` (the in-memory database has no schema either,
 * and `checkRedteamProbeLimit()` queries `eval_results` before promptfoo runs
 * its own migrations).
 */
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const home = mkdtempSync(join(tmpdir(), 'u9-e2e-pfhome-'));
process.env['PROMPTFOO_CONFIG_DIR'] = home;

const migrations = await import('drizzle-orm/libsql/migrator');
const { createClient } = await import('@libsql/client/node');
const { drizzle } = await import('drizzle-orm/libsql/node');
const client = createClient({ url: `file:${join(home, 'promptfoo.db')}` });
await migrations.migrate(drizzle(client), {
  migrationsFolder: new URL('../node_modules/promptfoo/dist/drizzle', import.meta.url).pathname,
});
client.close();

const cleanup = (): void => {
  try {
    rmSync(home, { recursive: true, force: true });
  } catch {
    // Best effort: a leftover temp dir must never fail the suite.
  }
};
process.once('exit', cleanup);

export const U9_PROMPTFOO_HOME = home;