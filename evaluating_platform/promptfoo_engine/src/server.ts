/**
 * Engine service entrypoint.
 *
 * Boot order:
 *  1. loadEnv() — asserts security switches + bearer token (fail-closed)
 *  2. mount /health (no auth, for compose healthcheck) and authed routes
 *  3. graceful shutdown: stop accepting, cancel in-flight runs, wipe workdirs
 */
import express from 'express';
import { loadEnv } from './env.js';
import { bearerAuth } from './auth.js';
import { TaskManager } from './manager/taskManager.js';
import { WorkdirManager } from './manager/workdir.js';
import { createRunsRouter } from './routes/runs.js';
import { executeRedteamRun } from './runner/redteamRun.js';
import { version } from './version.js';

async function main(): Promise<void> {
  const env = loadEnv();

  const workdirs = new WorkdirManager(env.dataRoot);
  const manager = new TaskManager({
    workerCount: env.workerCount,
    maxQueueDepth: env.maxQueueDepth,
    workdirs,
    async execute(run, emit) {
      // Create the isolated workdir for this run; cleanup happens in the
      // task manager's terminal path.
      await workdirs.create(run.request.run_id);
      const { result } = await executeRedteamRun(run, emit);
      run.status.result = result;
    },
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
