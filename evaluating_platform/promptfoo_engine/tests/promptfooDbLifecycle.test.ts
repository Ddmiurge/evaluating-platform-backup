import { P2_PROMPTFOO_HOME } from './p2LifecycleHome.js'; // must stay first

import { existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { sql } from 'drizzle-orm';
import { describe, expect, it } from 'vitest';

import {
  assertPromptfooDbLifecycleResolvable,
  closePromptfooDbIfOpen,
  ensurePromptfooSchema,
  hasPromptfooSchema,
  loadPromptfooChunkExport,
  resolvePromptfooDbPath,
  resolvePromptfooMigrationsFolder,
  runPromptfooDbMigrations,
  withDbLifecycleLock,
} from '../src/runner/promptfooDbLifecycle.js';
import { cleanupPromptfooRunArtifacts } from '../src/runner/promptfooStateCleanup.js';

/**
 * promptfoo's own memoized database handle.
 *
 * Reached through the lifecycle module's semantic-name resolver rather than a
 * minified literal, so this file does not pin a build artifact either. Tests
 * need the *real* handle: the defect being fixed is precisely that the real
 * handle keeps working after its file is deleted.
 */
interface PromptfooDb {
  run: (query: unknown) => Promise<unknown>;
  all: (query: unknown) => Promise<Array<Record<string, unknown>>>;
}

async function promptfooDb(): Promise<PromptfooDb> {
  const getDb = await loadPromptfooChunkExport('getDb');
  if (typeof getDb !== 'function') throw new Error('promptfoo getDb is not a function');
  return (await (getDb as () => Promise<unknown>)()) as PromptfooDb;
}

const MARKER_TABLE = 'p2_lifecycle_marker';

async function createMarkerTable(): Promise<void> {
  const db = await promptfooDb();
  await db.run(sql.raw(`CREATE TABLE IF NOT EXISTS ${MARKER_TABLE} (marker TEXT)`));
}

async function writeMarker(marker: string): Promise<void> {
  const db = await promptfooDb();
  await db.run(sql.raw(`INSERT INTO ${MARKER_TABLE} (marker) VALUES ('${marker}')`));
}

/** Markers visible through promptfoo's own (possibly stale) connection. */
async function markersViaPromptfoo(): Promise<string[]> {
  const db = await promptfooDb();
  const rows = await db.all(sql.raw(`SELECT marker FROM ${MARKER_TABLE}`));
  return rows.map((row) => String(row['marker'])).sort();
}

/**
 * Markers visible through a **fresh** connection opened at the path.
 *
 * This is the connection that decides whether the run's data actually reached
 * disk. `null` means the table is not there at all, which is itself a verdict.
 */
async function markersViaFreshConnection(): Promise<string[] | null> {
  const { createClient } = await import('@libsql/client/node');
  const client = createClient({ url: `file:${resolvePromptfooDbPath()}` });
  try {
    const result = await client.execute(`SELECT marker FROM ${MARKER_TABLE}`);
    return result.rows.map((row) => String(row['marker'])).sort();
  } catch {
    return null;
  } finally {
    client.close();
  }
}

/** Restore a clean home between cases: close first, then delete. */
async function resetHome(): Promise<void> {
  await closePromptfooDbIfOpen();
  await cleanupPromptfooRunArtifacts();
}

describe('promptfooDbLifecycle — paths and resolvability', () => {
  it('resolves the database path with promptfoo’s own rule', () => {
    expect(resolvePromptfooDbPath()).toBe(join(P2_PROMPTFOO_HOME, 'promptfoo.db'));
  });

  it('resolves the migrations folder next to promptfoo’s entrypoint', () => {
    // Derived from promptfoo's own resolution, not from a hardcoded node_modules
    // path, so a hoisted or deduped install still resolves. `require.resolve`
    // honours the `require` condition and lands on index.cjs, so the ESM entry
    // is appended the same way the production resolver does it.
    const packageEntry = createRequire(import.meta.url).resolve('promptfoo');
    const expected = join(dirname(dirname(packageEntry)), 'drizzle');
    expect(resolvePromptfooMigrationsFolder()).toBe(expected);
    expect(existsSync(expected)).toBe(true);
  });

  it('assertPromptfooDbLifecycleResolvable finds both internals', () => {
    // The counter-proof for this assertion is an upgrade that renames the export
    // or moves `dist/drizzle`: boot then fails with a message naming what is
    // missing, instead of every run failing with `no such table` or — worse —
    // cleanup silently no longer closing the connection.
    expect(() => assertPromptfooDbLifecycleResolvable()).not.toThrow();
  });
});

describe('promptfooDbLifecycle — schema preparation', () => {
  it('creates the schema when none exists and is idempotent', async () => {
    await resetHome();
    expect(existsSync(resolvePromptfooDbPath())).toBe(false);
    expect(await hasPromptfooSchema()).toBe(false);

    await ensurePromptfooSchema();
    expect(await hasPromptfooSchema()).toBe(true);

    // Repeated calls must not throw and must not duplicate anything: the native
    // runner calls this on every run, and drizzle's migrator skips what is
    // already applied.
    await ensurePromptfooSchema();
    await ensurePromptfooSchema();
    expect(await hasPromptfooSchema()).toBe(true);
  });

  it('is what stops `no such table: eval_results`', async () => {
    // The failure this replaces, measured directly: on a home with no schema,
    // promptfoo's own probe-limit query is what dies. Preparing the schema first
    // makes the same query succeed.
    await resetHome();
    await expect(probeLimitQuery()).rejects.toThrow();

    await ensurePromptfooSchema();
    await expect(probeLimitQuery()).resolves.toBeDefined();
  });
});

describe('promptfooDbLifecycle — close before delete (P2-3 core)', () => {
  it('makes the next run write to the path, not to an unlinked inode', async () => {
    await resetHome();

    // --- run 1 ---
    await ensurePromptfooSchema();
    await createMarkerTable();
    await writeMarker('r1');
    expect(await markersViaFreshConnection()).toEqual(['r1']);

    // --- terminal: close FIRST, then delete ---
    await closePromptfooDbIfOpen();
    const removed = await cleanupPromptfooRunArtifacts();
    expect(removed.length).toBeGreaterThan(0);
    expect(existsSync(resolvePromptfooDbPath())).toBe(false);

    // --- run 2 ---
    await ensurePromptfooSchema();
    await createMarkerTable();
    await writeMarker('r2');

    // The judgement criterion (plan 18 §5.4 / R-3-c): run 2's data must be on
    // disk and run 1's must be gone. Without the close, promptfoo's memoized
    // handle keeps writing into the old, unlinked inode and this returns null —
    // the table is not at the path at all — while `markersViaPromptfoo()` would
    // show ["r1","r2"].
    expect(await markersViaFreshConnection()).toEqual(['r2']);
    expect(await markersViaPromptfoo()).toEqual(['r2']);

    await resetHome();
  });

  it('without the close the data lands in the unlinked inode (counter-proof)', async () => {
    // The reverse of the test above, kept as an executable measurement of the
    // defect rather than as prose: migrate-only (no close) reproduces exactly the
    // shape the plan recorded — the path and promptfoo's handle disagree.
    await resetHome();

    await ensurePromptfooSchema();
    await createMarkerTable();
    await writeMarker('r1');

    // Terminal WITHOUT the close: delete the path, then re-migrate it (this is
    // the U7 operator workaround: "migrate before every run", which created a
    // new file while promptfoo kept using the old inode).
    await cleanupPromptfooRunArtifacts();
    await runPromptfooDbMigrations();

    await createMarkerTable();
    await writeMarker('r2');

    // promptfoo's connection sees both runs — run 1's row was never discarded.
    expect(await markersViaPromptfoo()).toEqual(['r1', 'r2']);
    // And the file on disk has neither: it is a different database.
    expect(await markersViaFreshConnection()).toBeNull();

    // Leave the process in the good state for any later case.
    await resetHome();
  });
});

/**
 * promptfoo's redteam probe-limit query — the call that fails with
 * `no such table: eval_results` when the schema was never migrated.
 *
 * Expressed in the same shape promptfoo uses (`SELECT … FROM eval_results`), so
 * the assertion measures the real dependency rather than a proxy for it.
 */
async function probeLimitQuery(): Promise<unknown> {
  const db = await promptfooDb();
  return db.all(sql.raw('SELECT COUNT(*) AS n FROM eval_results'));
}

describe('promptfooDbLifecycle — lifecycle operations are serialized', () => {
  it('never interleaves two critical sections', async () => {
    // Close, migrate and delete all act on one shared file. Interleaving them
    // would let one run's close land between another's migrate and delete, so the
    // operations are chained. P2-3 also serializes whole native runs (see
    // effectiveWorkerCount); this is the second line of defence, kept so the
    // operations cannot interleave even if that policy changes.
    const events: string[] = [];
    const section = (name: string) => async (): Promise<void> => {
      events.push(`${name}:enter`);
      await new Promise((resolve) => setTimeout(resolve, 5));
      events.push(`${name}:exit`);
    };

    await Promise.all([
      withDbLifecycleLock(section('a')),
      withDbLifecycleLock(section('b')),
      withDbLifecycleLock(section('c')),
    ]);

    // Each section's exit immediately follows its own enter: no `a:exit` before
    // `b:enter`, and no two enters adjacent.
    expect(events).toEqual(['a:enter', 'a:exit', 'b:enter', 'b:exit', 'c:enter', 'c:exit']);
  });

  it('releases the lock when a section throws', async () => {
    // A rejected section must not wedge the chain, or one failed run would stop
    // every later run from preparing its schema.
    await expect(
      withDbLifecycleLock(async () => {
        throw new Error('boom');
      }),
    ).rejects.toThrow('boom');
    await expect(withDbLifecycleLock(async () => 'ok')).resolves.toBe('ok');
  });
});
