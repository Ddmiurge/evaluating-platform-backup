/**
 * Engine service entrypoint.
 *
 * Boot order:
 *  1. loadEnv() — asserts security switches + bearer token (fail-closed)
 *  2. verifySandbox() + assertSandboxInvariants() — refuse to boot in an unsafe
 *     sandbox (U10)
 *  3. P2-3 · native path only: assert the promptfoo DB lifecycle is resolvable
 *     and migrate the schema, before anything can need it
 *  4. mount /health (no auth, for compose healthcheck) and authed routes
 *  5. graceful shutdown: stop accepting, cancel in-flight runs, wipe workdirs
 */
import express from 'express';
import { assertSandboxInvariants, effectiveWorkerCount, loadEnv } from './env.js';
import { bearerAuth } from './auth.js';
import { TaskManager } from './manager/taskManager.js';
import { WorkdirManager } from './manager/workdir.js';
import { createRunsRouter } from './routes/runs.js';
import { executeRedteamRun } from './runner/redteamRun.js';
import { cleanupPromptfooRunArtifacts } from './runner/promptfooStateCleanup.js';
import {
  assertPromptfooDbLifecycleResolvable,
  closePromptfooDbIfOpen,
  runPromptfooDbMigrations,
} from './runner/promptfooDbLifecycle.js';
import { formatSandboxReport, verifySandbox } from './sandbox.js';
import { version } from './version.js';

async function main(): Promise<void> {
  const env = loadEnv();

  // U10: assert the execution sandbox before anything can run a customer's
  // code. `assertSandboxInvariants` refuses to boot when the process is root or
  // a declared-and-probed property has failed; the report is logged either way
  // so a reader can tell `verified` from `declared` — the distinction the
  // design doc's "最重安全项" demands.
  const sandbox = await verifySandbox();
  for (const line of formatSandboxReport(sandbox)) {
    console.log(line);
  }
  await assertSandboxInvariants();

  // P2-3 · prepare promptfoo's SQLite schema before the first run can need it.
  //
  // Both steps are fail-closed, and both are only meaningful on the native path
  // (Plan A never touches promptfoo's storage). Failing to boot is the right
  // answer for either: a missing schema turns every run into
  // `no such table: eval_results`, and an unresolvable close would silently
  // restore the unlinked-inode problem — a cleanup that looks like it works.
  if (env.nativeGeneration) {
    assertPromptfooDbLifecycleResolvable();
    await runPromptfooDbMigrations();
  }

  // P2-3 · native runs are serialized, and this is where that is enforced.
  //
  // promptfoo keeps ONE database file for the whole process (`getDb()` memoizes
  // the path, `PROMPTFOO_CONFIG_DIR` is latched at import), and the terminal
  // cleanup deletes that file. With two workers, worker A's terminal cleanup
  // therefore deletes worker B's in-flight results, and B's schema can vanish
  // mid-run. Measured on the U7 probe: run 2 read `["r1","r2"]` — run 1's rows
  // were still there, in an inode the cleanup could not reach.
  //
  // The trade is throughput for correctness, and it is the trade the plan
  // recorded the user choosing ("排队比偶发失败 + 数据丢失好"). It is clamped
  // here rather than left to `PROMPTFOO_ENGINE_WORKERS` because a forgotten
  // setting would silently corrupt runs — the failure would not look like a
  // configuration mistake, it would look like flaky data. A queued run stays
  // `queued` (visible on /health and through the BFF), so the wait is honest.
  const workerCount = effectiveWorkerCount(env.workerCount, env.nativeGeneration);
  if (env.nativeGeneration && env.workerCount !== 1) {
    console.log(
      `[engine] native generation serializes runs: clamping workers ${env.workerCount} -> 1 ` +
        '(one shared promptfoo database; concurrent runs would delete each other\'s results)',
    );
  }

  const workdirs = new WorkdirManager(env.dataRoot);
  const manager = new TaskManager({
    workerCount,
    maxQueueDepth: env.maxQueueDepth,
    workdirs,
    async execute(run, emit) {
      // Create the isolated workdir for this run; cleanup happens in the
      // task manager's terminal path. Plan C writes promptfoo's config document
      // (which embeds the materialized generation credential) in here, so the
      // path has to be handed to the runner.
      const workdir = await workdirs.create(run.request.run_id);
      const { result } = await executeRedteamRun(run, emit, {
        nativeGeneration: env.nativeGeneration,
        workdir,
      });
      run.status.result = result;
    },
    // U7 · Plan C makes promptfoo persist the run to its own SQLite database —
    // `doRedteamRun()` hardcodes `doEval({ write: true })`, so it cannot be
    // turned off. The destination is a module-level decision inside promptfoo
    // (see promptfooStateCleanup.ts for why it cannot be confined per run), so
    // the artifacts are removed here instead, on every terminal path.
    //
    // P2-3 · the close must come FIRST, and the order is the whole point. The
    // delete removes a path; promptfoo's memoized connection would keep writing
    // to the now-unlinked inode, so the data would not reach disk, the space
    // would not be freed, and the run's prompts would still be readable through
    // that handle. Closing drops the memo so the next run's `getDb()` re-opens
    // the path — which is what makes the delete complete and durable.
    //
    // The close lives here rather than inside `cleanupPromptfooRunArtifacts`
    // because that module deliberately imports nothing from promptfoo (so it can
    // run from a failure path without loading the library again); composing the
    // two steps at the one place both paths converge keeps that property.
    //
    // Only wired when the native path is selected: Plan A keeps its original
    // promise of writing nothing, and deleting a developer's `~/.promptfoo`
    // state that this service never created would be wrong.
    ...(env.nativeGeneration
      ? {
          onTerminal: async (): Promise<void> => {
            await closePromptfooDbIfOpen();
            await cleanupPromptfooRunArtifacts();
          },
        }
      : {}),
  });

  const app = express();
  app.disable('x-powered-by');
  app.use(express.json({ limit: '1mb' }));

  // Health endpoint: no auth, minimal info (compose healthcheck target).
  app.get('/health', (_req, res) => {
    res.json({
      status: 'ok',
      version,
      queued: manager.queuedCount,
      running: manager.runningCount,
      workdirs: workdirs.size,
    });
  });

  app.use(bearerAuth(env.bearerToken));
  app.use(createRunsRouter(manager));

  // U10: the sandbox report, behind the same bearer auth as the run routes.
  //
  // Deliberately NOT folded into `/health`. `/health` is unauthenticated (it is
  // the compose healthcheck target), so publishing "is the sandbox intact"
  // there would hand an unauthenticated caller a map of the engine's internals
  // — writable root, uid, whether egress is blocked. Operators need the report;
  // anonymous callers do not.
  //
  // The report distinguishes `verified` from `declared`, and `probed` says
  // whether anything was actually probed at all, so an operator reading it can
  // never mistake a declaration for a measurement.
  app.get('/sandbox', async (_req: express.Request, res: express.Response) => {
    const report = await verifySandbox();
    res.json(report);
  });

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  app.use((err: Error, _req: express.Request, res: express.Response, _next: express.NextFunction) => {
    // Never leak stack traces or message details to the caller.
    res.status(500).json({ error: 'internal_error' });
    if (process.env.PROMPTFOO_ENGINE_DEBUG === '1') {
      console.error('[engine] unhandled error:', err.message);
    }
  });

  const server = app.listen(env.port, () => {
    console.log(`[engine] promptfoo-engine v${version} listening on :${env.port} (workers=${env.workerCount})`);
  });

  const shutdown = async (signal: string): Promise<void> => {
    console.log(`[engine] ${signal} received, shutting down`);
    server.close();
    await manager.shutdown();
    process.exit(0);
  };
  process.on('SIGTERM', () => void shutdown('SIGTERM'));
  process.on('SIGINT', () => void shutdown('SIGINT'));
}

main().catch((err: unknown) => {
  console.error('[engine] fatal:', err instanceof Error ? err.message : err);
  process.exit(1);
});
