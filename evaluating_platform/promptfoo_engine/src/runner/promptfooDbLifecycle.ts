/**
 * promptfooDbLifecycle.ts — P2-3 · make promptfoo's SQLite file usable and
 * actually disposable.
 *
 * # The two defects this module closes
 *
 * **1. The schema was never migrated.** promptfoo's `checkRedteamProbeLimit()`
 * runs at the very start of `doGenerateRedteamInternal` and queries
 * `eval_results`, but library mode never migrates: `runDbMigrations()` is called
 * inside `doEval({write: true})`, which happens *after* generation. The order is
 * reversed. The result is that every Plan C run dies with
 * `LibSQL/DrizzleQueryError: no such table: eval_results` unless an operator
 * migrates by hand inside the container — which is exactly what the U7
 * acceptance run had to do.
 *
 * **2. Deleting the file did not delete the data.** `getDb()` memoizes its
 * connection (`if (dbInstance) return dbInstance`), so after
 * `cleanupPromptfooRunArtifacts()` unlinks `promptfoo.db`, promptfoo keeps
 * writing through the same connection into the **already-unlinked inode**. The
 * consequences are worse than "leftover files": the data never reaches the path,
 * the disk space is not released for the life of the process, and the
 * "read-then-burn" promise U7 makes is not actually kept. Measured on the U7
 * probe: run 1 writes `r1`; cleanup removes the path; migrating creates a new
 * empty DB at the path; run 2 writes `r2` — and promptfoo's connection reads
 * `["r1","r2"]` while a fresh connection at the path reports "no such table".
 *
 * # The corrected order (measured, not assumed)
 *
 *   boot            resolvePromptfooDbPath() -> runPromptfooDbMigrations()
 *   each native run ensurePromptfooSchema()   (close, then migrate)
 *   terminal        closePromptfooDbIfOpen()  **before** the file is removed
 *                   then cleanupPromptfooRunArtifacts()
 *
 * The close is the load-bearing step: it drops the memoized connection so the
 * next `getDb()` re-opens **the path**, which is what makes the subsequent
 * delete both complete and durable.
 *
 * # Why the internals are resolved by name rather than by literal
 *
 * promptfoo exports no migration or connection API — verified against the
 * installed dist: none of its exports is `migrate`, `getDb` or `closeDbIfOpen`.
 * `closeDbIfOpen` is only reachable through the tables chunk, under a minified
 * name that changes whenever the build is re-minified (`p` today), inside a
 * content-hashed file (`tables-BboCJNxW.js` today). So the binding is read out
 * of `dist/src/index.js`'s own import statement, keyed on the **semantic name**
 * promptfoo itself uses. A re-minify cannot silently break it, and a genuine
 * removal fails loudly at boot instead of leaving the close as a no-op — which
 * would re-create defect 2 while looking healthy.
 *
 * # Dependencies
 *
 * `drizzle-orm` and `@libsql/client` are promptfoo's own dependencies. They are
 * declared in package.json explicitly (with the versions the installed promptfoo
 * resolves to) because production code importing them is otherwise a phantom
 * dependency that survives on npm flattening.
 */
import { existsSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { promptfooConfigDir } from './promptfooStateCleanup.js';

/** promptfoo's own name for the connection-closing export. */
const CLOSE_DB_SEMANTIC_NAME = 'closeDbIfOpen';

/** The table whose absence is the exact failure this module exists to prevent. */
const SCHEMA_SENTINEL_TABLE = 'eval_results';

/** Raised when the lifecycle cannot be established. Never contains payloads. */
export class PromptfooDbLifecycleError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'PromptfooDbLifecycleError';
  }
}

/**
 * The SQLite path promptfoo will use, resolved by promptfoo's own rule.
 *
 * Mirrors `path.resolve(getConfigDirectoryPath(), 'promptfoo.db')`. Kept
 * identical to `promptfooStateCleanup.promptfooConfigDir()` on purpose: the
 * migration/close must target the same file the cleanup deletes, and two
 * independent readings of that rule could drift apart without anything noticing.
 */
export function resolvePromptfooDbPath(env: NodeJS.ProcessEnv = process.env): string {
  return path.join(promptfooConfigDir(env), 'promptfoo.db');
}

/** Absolute path of promptfoo's drizzle migrations folder. */
export function resolvePromptfooMigrationsFolder(): string {
  const entry = resolvePromptfooEntry();
  // `dist/src/index.js` -> `dist/drizzle`. Derived from the resolved entrypoint
  // rather than from `PROMPTFOO_CONFIG_DIR`-based heuristics, which are internal
  // to promptfoo and move between releases.
  const folder = path.join(path.dirname(path.dirname(entry)), 'drizzle');
  if (!existsSync(folder)) {
    throw new PromptfooDbLifecycleError(
      `promptfoo's drizzle migrations folder is missing at ${folder} (resolved from ${entry}). ` +
        'The upstream layout changed; the schema cannot be prepared, and a run would fail with ' +
        '"no such table: eval_results".',
    );
  }
  return folder;
}

/**
 * Absolute path of promptfoo's built entrypoint, via its own export map.
 *
 * Prefers `import.meta.resolve`, which is what production Node uses and what
 * honours the `import` condition. Falls back to `createRequire` for runtimes
 * whose module transform does not implement it (Vitest's SSR transform throws
 * `__vite_ssr_import_meta__.resolve is not a function`). `require.resolve` lands
 * on `index.cjs` because it honours the `require` condition, so the ESM entry is
 * appended explicitly: that is the file production loads, and the only one whose
 * chunk imports the parsers below can read.
 */
function resolvePromptfooEntry(): string {
  const importMetaResolve = (import.meta as { resolve?: (id: string) => string }).resolve;
  if (typeof importMetaResolve === 'function') {
    const resolved = importMetaResolve.call(import.meta, 'promptfoo');
    return resolved.startsWith('file://') ? fileURLToPath(resolved) : resolved;
  }
  const packageEntry = createRequire(import.meta.url).resolve('promptfoo');
  return path.join(path.dirname(packageEntry), 'index.js');
}

/**
 * Locate a promptfoo chunk export by the **semantic name** promptfoo itself
 * binds it to.
 *
 * The import statement is read from `dist/src/index.js` and the binding matched
 * on its local name; the exported (minified) name and the content-hashed chunk
 * filename are build artifacts that change on every re-minify. Exported because
 * the lifecycle needs two of these (`closeDbIfOpen` here, and tests drive
 * promptfoo's own `getDb` to prove the close has an effect), and one parser is
 * better than two that can disagree.
 */
export function resolvePromptfooChunkExport(semanticName: string): { chunk: string; exported: string } {
  const entry = resolvePromptfooEntry();
  const source = readFileSync(entry, 'utf8');
  const pattern = /import\s*\{([^}]*)\}\s*from\s*"\.\/(tables-[^"]+)"/g;
  for (const match of source.matchAll(pattern)) {
    const specifiers = (match[1] ?? '').split(',').map((s) => s.trim()).filter(Boolean);
    for (const specifier of specifiers) {
      const [exported = '', local = exported] = specifier.split(/\s+as\s+/).map((s) => s.trim());
      if (local === semanticName) {
        return { chunk: match[2] ?? '', exported };
      }
    }
  }
  throw new PromptfooDbLifecycleError(
    `promptfoo's dist no longer imports ${semanticName} from a tables chunk. ` +
      'For closeDbIfOpen this is fatal: without it the connection cannot be closed, the memoized ' +
      'handle would keep writing to an unlinked inode after cleanup, and "read-then-burn" would ' +
      'silently stop holding. Refusing to continue rather than degrading into a no-op.',
  );
}

/** Import the module a chunk export lives in and return that export. */
export async function loadPromptfooChunkExport(semanticName: string): Promise<unknown> {
  const { chunk, exported } = resolvePromptfooChunkExport(semanticName);
  const chunkUrl = new URL(chunk, pathToFileURL(resolvePromptfooEntry()));
  const module = (await import(chunkUrl.href)) as Record<string, unknown>;
  return module[exported];
}

/**
 * Close promptfoo's memoized database connection, if one is open.
 *
 * Safe to call when nothing is open (promptfoo's own implementation is
 * `if (sqliteInstance) await closeDb()`), safe to call twice, and safe to call
 * when the file has already been removed. This is what makes the terminal
 * delete meaningful — see the module header.
 */
export async function closePromptfooDbIfOpen(): Promise<void> {
  const close = await loadPromptfooChunkExport(CLOSE_DB_SEMANTIC_NAME);
  if (typeof close !== 'function') {
    throw new PromptfooDbLifecycleError(
      `promptfoo's ${CLOSE_DB_SEMANTIC_NAME} binding is not a function; refusing to treat the ` +
        'connection as closed.',
    );
  }
  await (close as () => Promise<void> | void)();
}

/**
 * Run promptfoo's drizzle migrations against the database path.
 *
 * Idempotent — drizzle's migrator skips migrations already applied, verified by
 * calling it repeatedly. Fail-closed: a failure throws rather than leaving a run
 * to discover the missing schema later, because "no such table" mid-run is the
 * failure this module exists to remove.
 */
export async function runPromptfooDbMigrations(env: NodeJS.ProcessEnv = process.env): Promise<void> {
  const dbPath = resolvePromptfooDbPath(env);
  const migrationsFolder = resolvePromptfooMigrationsFolder();

  const { createClient } = (await import('@libsql/client/node')) as typeof import('@libsql/client/node');
  const { drizzle } = (await import('drizzle-orm/libsql/node')) as typeof import('drizzle-orm/libsql/node');
  const { migrate } = (await import('drizzle-orm/libsql/migrator')) as typeof import('drizzle-orm/libsql/migrator');

  const client = createClient({ url: `file:${dbPath}` });
  try {
    await migrate(drizzle(client), { migrationsFolder });
  } catch (err) {
    throw new PromptfooDbLifecycleError(
      `failed to migrate promptfoo's database at ${dbPath}: ${err instanceof Error ? err.message : String(err)}`,
    );
  } finally {
    client.close();
  }
}

/**
 * Ensure the schema exists before a native run touches promptfoo.
 *
 * Closes the connection first. That order is the point: migrating while a
 * memoized handle is open would create a *new* file at the path while promptfoo
 * kept writing to the old, unlinked one — the state measured on the U7 probe,
 * where the path and the connection reported different contents.
 *
 * The migration itself is unconditional rather than gated on a "is the table
 * there" check: drizzle skips what is already applied, so the cost is a few
 * milliseconds and the branch that could get the answer wrong disappears. A
 * crashed run leaving a partially-migrated file is exactly the case a
 * presence check would handle inconsistently.
 */
export async function ensurePromptfooSchema(env: NodeJS.ProcessEnv = process.env): Promise<void> {
  await withDbLifecycleLock(async () => {
    await closePromptfooDbIfOpen();
    await runPromptfooDbMigrations(env);
  });
}

/**
 * Verify the lifecycle is resolvable, without touching the database.
 *
 * Called at boot so a promptfoo upgrade that renames the export or moves the
 * migrations folder fails the container instead of surfacing as "every run dies
 * with no such table" or, worse, as a cleanup that quietly stops working.
 */
export function assertPromptfooDbLifecycleResolvable(): void {
  resolvePromptfooChunkExport(CLOSE_DB_SEMANTIC_NAME);
  resolvePromptfooMigrationsFolder();
}

/**
 * Serialize database lifecycle operations.
 *
 * The engine's TaskManager runs several workers in one process. Close, migrate
 * and delete all act on one shared file, so two workers interleaving them would
 * let one run's close land between another's migrate and delete. Serializing
 * the *operations* is not sufficient on its own — P2-3 therefore also runs
 * native runs one at a time (see server.ts) — but it keeps these steps from
 * interleaving even if that changes.
 *
 * Implemented as a promise chain rather than a mutex library: no new dependency,
 * and the ordering guarantee is the same.
 */
let lifecycleChain: Promise<unknown> = Promise.resolve();

export async function withDbLifecycleLock<T>(fn: () => Promise<T>): Promise<T> {
  const previous = lifecycleChain;
  let release: () => void = () => {};
  lifecycleChain = new Promise<void>((resolve) => {
    release = resolve;
  });
  await previous.catch(() => undefined);
  try {
    return await fn();
  } finally {
    release();
  }
}

/**
 * Count the sentinel table's presence, for diagnostics and tests.
 *
 * Deliberately a *read* on a fresh connection rather than a check of the path's
 * existence: after the unlinked-inode defect, "the file is there" and "the schema
 * is there" are different claims, and only the second one is what a run needs.
 */
export async function hasPromptfooSchema(env: NodeJS.ProcessEnv = process.env): Promise<boolean> {
  const dbPath = resolvePromptfooDbPath(env);
  if (!existsSync(dbPath)) return false;
  const { createClient } = (await import('@libsql/client/node')) as typeof import('@libsql/client/node');
  const client = createClient({ url: `file:${dbPath}` });
  try {
    const result = await client.execute({
      sql: 'SELECT name FROM sqlite_master WHERE type = ? AND name = ?',
      args: ['table', SCHEMA_SENTINEL_TABLE],
    });
    return result.rows.length > 0;
  } finally {
    client.close();
  }
}
