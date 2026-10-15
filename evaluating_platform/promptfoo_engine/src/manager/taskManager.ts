/**
 * Task manager: FIFO queue + N workers + per-run state machine.
 *
 * Design notes (merge plan P0-03, risk R1):
 *  - A run moves through: queued -> running(-> terminal)
 *  - Workers pull from the FIFO queue; each worker executes one run at a time.
 *  - Cancellation flips a per-run AbortController; the runner observes it.
 *  - All state is in-memory only. The platform (BFF) is the sole persistence
 *    layer; if the engine restarts, the BFF marks in-flight runs failed via
 *    its own timeout/recovery logic.
 */
import { EventEmitter } from 'node:events';
import type { RunProgressEvent, RunRequest, RunStatusResponse } from '../types.js';
import { EngineRunError } from '../runner/engineRunError.js';
import { redactEngineErrorBrief } from '../sanitize/errorBrief.js';
import type { WorkdirManager } from './workdir.js';

export interface RunRecord {
  request: RunRequest;
  status: RunStatusResponse;
  controller: AbortController;
  startedAt: number;
}

export interface TaskManagerOptions {
  workerCount: number;
  maxQueueDepth: number;
  workdirs: WorkdirManager;
  /** Execute one run. Resolves with a safe summary; throws on failure. */
  execute: (run: RunRecord, emit: (event: RunProgressEvent) => void) => Promise<void>;
  /**
   * U7 · optional terminal hook, awaited on **every** terminal path (success,
   * failure and cancellation) after the workdir is gone.
   *
   * Exists so cleanup of state the library writes *outside* the workdir can be
   * driven from the one place all three paths converge. A rejection here marks
   * the run failed rather than being swallowed: leaving raw prompts on disk is
   * precisely the silent-residue failure U6 was created to eliminate. The hook
   * must not log payload content.
   */
  onTerminal?: (run: RunRecord) => Promise<void>;
}

interface QueuedItem {
  runId: string;
}

const TERMINAL_PHASES = new Set(['succeeded', 'failed', 'canceled']);

export class TaskManager extends EventEmitter {
  private readonly runs = new Map<string, RunRecord>();
  private readonly queue: QueuedItem[] = [];
  private readonly workers: Array<Promise<void>> = [];
  private readonly workdirs: WorkdirManager;
  private readonly execute: TaskManagerOptions['execute'];
  private readonly onTerminal: TaskManagerOptions['onTerminal'];
  private readonly maxQueueDepth: number;
  private shutdownRequested = false;

  constructor(opts: TaskManagerOptions) {
    super();
    this.workerCount = opts.workerCount;
    this.maxQueueDepth = opts.maxQueueDepth;
    this.workdirs = opts.workdirs;
    this.execute = opts.execute;
    this.onTerminal = opts.onTerminal;
    for (let i = 0; i < opts.workerCount; i++) {
      this.workers.push(this.workerLoop(i));
    }
  }

  /**
   * Workers actually running. Public and read-only so `/health` can report the
   * effective count (P2-3): the native path clamps it to 1, and without this the
   * only way for an operator to confirm serialization is to read the logs.
   */
  readonly workerCount: number;

  get queuedCount(): number {
    return this.queue.length;
  }


  get runningCount(): number {
    let n = 0;
    for (const run of this.runs.values()) {
      if (run.status.phase === 'generating_tests' || run.status.phase === 'executing_probes' || run.status.phase === 'engine_judging' || run.status.phase === 'compiling_report') n++;
    }
    return n;
  }

  /** Enqueue a new run. Returns false when the queue is full. */
  submit(request: RunRequest): boolean {
    if (this.runs.has(request.run_id)) {
      return false;
    }
    if (this.queue.length >= this.maxQueueDepth) {
      return false;
    }
    const now = new Date().toISOString();
    const record: RunRecord = {
      request,
      status: {
        run_id: request.run_id,
        phase: 'queued',
        planned_count: 0,
        executed_count: 0,
        duration_ms: 0,
        created_at: now,
        updated_at: now,
      },
      controller: new AbortController(),
      startedAt: 0,
    };
    this.runs.set(request.run_id, record);
    this.queue.push({ runId: request.run_id });
    this.emitProgress(record, {});
    this.kick();
    return true;
  }

  getStatus(runId: string): RunStatusResponse | undefined {
    return this.runs.get(runId)?.status;
  }

  /** Cancel a queued or running run. Unknown/terminal runs resolve false. */
  cancel(runId: string): boolean {
    const run = this.runs.get(runId);
    if (!run) return false;
    if (TERMINAL_PHASES.has(run.status.phase)) return false;
    const idx = this.queue.findIndex((q) => q.runId === runId);
    if (idx >= 0) {
      this.queue.splice(idx, 1);
      this.terminate(run, 'canceled', 'canceled by user');
      return true;
    }
    run.controller.abort();
    return true;
  }

  private kick(): void {
    // workers are always polling via promise loop; nothing else needed.
  }

  private async workerLoop(index: number): Promise<void> {
    for (;;) {
      if (this.shutdownRequested) return;
      const item = this.queue.shift();
      if (!item) {
        await sleep(100);
        continue;
      }
      const run = this.runs.get(item.runId);
      if (!run) continue;
      if (TERMINAL_PHASES.has(run.status.phase)) continue;
      await this.runSafe(run, index);
    }
  }

  private async runSafe(run: RunRecord, workerIndex: number): Promise<void> {
    run.startedAt = Date.now();
    // Mark the run as executing before handing it to the runner so status
    // observers (tests, REST, SSE) see a non-queued phase immediately.
    this.emitProgress(run, { phase: 'executing_probes', status_text: '执行探针' });
    const emit = (event: RunProgressEvent): void => this.emitProgress(run, event);
    try {
      await this.execute(run, emit);
      // An aborted run must never be reported as succeeded, even when execute()
      // returns normally instead of throwing: a runner that swallows the abort
      // (or finishes at the same instant) would otherwise turn a cancellation
      // into a green run. Cheap to check, and a false "succeeded" is the exact
      // shape of silent failure this engine keeps working to eliminate.
      if (run.controller.signal.aborted) {
        this.terminate(run, 'canceled', 'canceled by user');
      } else if (!TERMINAL_PHASES.has(run.status.phase)) {
        this.terminate(run, 'succeeded');
      }
    } catch (err) {
      if (run.controller.signal.aborted) {
        this.terminate(run, 'canceled', 'canceled by user');
      } else {
        // P2-5: carry the cause out, redacted. `EngineRunError` codes are the API
        // contract; anything else is an internal fault whose text is still the only
        // clue anyone will get — so it is passed through the same redaction rather
        // than dropped. A bare `err.code` is read when present, because a coded
        // error that is not an EngineRunError instance (a re-thrown or wrapped one)
        // should still be classified.
        const code =
          err instanceof EngineRunError
            ? err.code
            : typeof (err as { code?: unknown })?.code === 'string'
              ? ((err as { code: string }).code)
              : 'engine_error';
        this.terminate(run, 'failed', code, redactEngineErrorBrief(err instanceof Error ? err.message : String(err)));
      }
    } finally {
      await this.cleanupTerminal(run);
      this.emit('run-terminal', run.status);
    }
  }

  /**
   * Terminal cleanup: workdir first, then the caller's hook.
   *
   * Workdir cleanup is best-effort (it is `rm -rf` on a path we created) while
   * hook failures are *not* swallowed — a run that reports success while its
   * raw prompts are still on disk is the exact failure shape U6 exists to
   * prevent, so it is downgraded to `failed` with a readable code.
   *
   * Only a still-successful run is downgraded. A run that already failed or was
   * canceled keeps its original phase and error code: the cleanup problem is
   * additional context, not a replacement for the real cause, and overwriting a
   * meaningful error code with a housekeeping one would hide it.
   */
  private async cleanupTerminal(run: RunRecord): Promise<void> {
    await this.workdirs.cleanup(run.request.run_id).catch(() => undefined);
    if (!this.onTerminal) return;
    try {
      await this.onTerminal(run);
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      // Path/errno only — the hook is responsible for never putting payload
      // content in the message it throws.
      console.error(`[engine] terminal cleanup failed for run ${run.request.run_id}: ${detail}`);
      if (run.status.phase === 'succeeded') {
        this.terminate(run, 'failed', 'terminal_cleanup_failed');
      }
    }
  }

  /** Update phase/counters of a run; used by the runner through emit(). */
  emitProgress(run: RunRecord, patch: Partial<RunProgressEvent>): void {
    const status = run.status;
    if (patch.phase && !TERMINAL_PHASES.has(status.phase)) {
      status.phase = patch.phase;
    }
    if (patch.status_text !== undefined) status.status_text = patch.status_text;
    if (patch.planned_count !== undefined) status.planned_count = patch.planned_count;
    if (patch.executed_count !== undefined) status.executed_count = patch.executed_count;
    if (patch.current_stage !== undefined) status.current_stage = patch.current_stage;
    status.duration_ms = run.startedAt ? Date.now() - run.startedAt : 0;
    status.updated_at = new Date().toISOString();
    const event: RunProgressEvent = {
      run_id: status.run_id,
      phase: status.phase,
      status_text: status.status_text,
      planned_count: status.planned_count,
      executed_count: status.executed_count,
      current_stage: status.current_stage,
      duration_ms: status.duration_ms,
    };
    this.emit('progress', event);
  }

  terminate(
    run: RunRecord,
    phase: 'succeeded' | 'failed' | 'canceled',
    errorCode?: string,
    errorMessage?: string,
  ): void {
    run.status.phase = phase;
    if (errorCode) run.status.error_code = errorCode;
    // P2-5: the brief is a TERMINAL field. A success clears it so a retried or
    // reused record cannot carry a stale diagnosis, and a cancellation records
    // nothing because "canceled by user" is not a failure to explain.
    if (phase === 'failed' && errorMessage) {
      run.status.error_message = errorMessage;
    } else if (phase !== 'failed') {
      delete run.status.error_message;
    }
    run.status.duration_ms = run.startedAt ? Date.now() - run.startedAt : 0;
    run.status.updated_at = new Date().toISOString();
    this.emit('progress', {
      run_id: run.status.run_id,
      phase,
      duration_ms: run.status.duration_ms,
    });
  }

  /** Attach the runner-provided result to the record (called by execute()). */
  attachResult(runId: string, result: RunStatusResponse['result']): void {
    const run = this.runs.get(runId);
    if (run) run.status.result = result;
  }

  async shutdown(): Promise<void> {
    this.shutdownRequested = true;
    for (const run of this.runs.values()) {
      if (!TERMINAL_PHASES.has(run.status.phase)) {
        run.controller.abort();
      }
    }
    await Promise.allSettled(this.workers);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}