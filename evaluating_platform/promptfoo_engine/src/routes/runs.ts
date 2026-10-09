/**
 * REST routes for run management.
 *
 *   POST /runs                  create + enqueue a run (202; 409 when queue full / dup)
 *   GET  /runs/:id              current status (safe fields only)
 *   GET  /runs/:id/events       SSE stream of safe progress events
 *   POST /runs/:id/cancel       cancel queued/running run
 *
 * All handlers assume bearer auth has already run.
 */
import { Router, type Request, type Response } from 'express';
import type { RunRequest, RunProgressEvent } from '../types.js';
import type { TaskManager } from '../manager/taskManager.js';

export function createRunsRouter(manager: TaskManager): Router {
  const router = Router();

  const RUN_ID_RE = /^[A-Za-z0-9_-]{1,64}$/;

  // P3-05: 提取重复的 runId 校验与 404 短路样板（4 处 × ~14 行）。
  const runIdOr400 = (req: Request, res: Response): string | null => {
    const runId = req.params.id ?? '';
    if (!RUN_ID_RE.test(runId)) {
      res.status(400).json({ error: 'invalid_run_id' });
      return null;
    }
    return runId;
  };

  const statusOr404 = (runId: string, res: Response) => {
    const status = manager.getStatus(runId);
    if (!status) {
      res.status(404).json({ error: 'run_not_found' });
      return null;
    }
    return status;
  };

  router.post('/runs', (req: Request, res: Response) => {
    const body = req.body as Partial<RunRequest>;
    if (!isRunRequest(body)) {
      res.status(400).json({ error: 'invalid_run_request' });
      return;
    }
    if (manager.getStatus(body.run_id)) {
      res.status(409).json({ error: 'run_already_exists' });
      return;
    }
    const accepted = manager.submit(body as RunRequest);
    if (!accepted) {
      res.status(409).json({ error: 'queue_full' });
      return;
    }
    res.status(202).json({ run_id: body.run_id, status: 'queued' });
  });

  router.get('/runs/:id', (req: Request, res: Response) => {
    const runId = runIdOr400(req, res);
    if (!runId) return;
    const status = statusOr404(runId, res);
    if (!status) return;
    res.json(status);
  });

  router.post('/runs/:id/cancel', (req: Request, res: Response) => {
    const runId = runIdOr400(req, res);
    if (!runId) return;
    const ok = manager.cancel(runId);
    if (!ok) {
      const status = statusOr404(runId, res);
      if (!status) return;
      res.status(409).json({ error: 'run_not_cancelable', phase: status.phase });
      return;
    }
    res.json({ run_id: runId, status: 'canceling' });
  });

  router.get('/runs/:id/events', (req: Request, res: Response) => {
    const runId = runIdOr400(req, res);
    if (!runId) return;
    const status = statusOr404(runId, res);
    if (!status) return;

    res.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
      'X-Accel-Buffering': 'no',
    });

    // Initial snapshot so late subscribers see current state.
    sendEvent(res, 'progress', snapshotEvent(status));

    const onProgress = (event: RunProgressEvent): void => {
      if (event.run_id !== runId) return;
      sendEvent(res, 'progress', event);
    };
    const onTerminal = (): void => {
      const final = manager.getStatus(runId);
      if (final) sendEvent(res, 'progress', snapshotEvent(final));
      res.end();
    };

    manager.on('progress', onProgress);
    manager.once('run-terminal', onTerminal);

    req.on('close', () => {
      manager.off('progress', onProgress);
      manager.off('run-terminal', onTerminal);
    });
  });

  return router;
}

interface Snapshot {
  run_id: string;
  phase: string;
  status_text?: string;
  planned_count: number;
  executed_count: number;
  current_stage?: string;
  duration_ms: number;
  error_code?: string;
  result?: unknown;
}

function snapshotEvent(status: Snapshot): RunProgressEvent {
  return {
    run_id: status.run_id,
    phase: status.phase as RunProgressEvent['phase'],
    status_text: status.status_text,
    planned_count: status.planned_count,
    executed_count: status.executed_count,
    current_stage: status.current_stage,
    duration_ms: status.duration_ms,
  };
}

function sendEvent(res: Response, event: string, data: unknown): void {
  if (res.writableEnded) return;
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
}

const TERMINAL = new Set(['succeeded', 'failed', 'canceled']);

function isRunRequest(body: unknown): body is RunRequest {
  if (typeof body !== 'object' || body === null) return false;
  const b = body as Record<string, unknown>;
  return (
    typeof b.run_id === 'string' &&
    /^[A-Za-z0-9_-]{1,64}$/.test(b.run_id) &&
    typeof b.platform_user_id === 'string' &&
    b.platform_user_id.length > 0 &&
    typeof b.purpose === 'string' &&
    b.purpose.length > 0 &&
    typeof b.num_tests === 'number' &&
    Array.isArray(b.plugins) &&
    b.plugins.length > 0 &&
    Array.isArray(b.strategies) &&
    typeof b.judge_mode === 'string' &&
    typeof b.credentials === 'object' &&
    b.credentials !== null &&
    typeof (b.credentials as Record<string, { target?: unknown }>).target === 'object' &&
    (b.credentials as Record<string, { target?: unknown }>).target !== null
  );
}
