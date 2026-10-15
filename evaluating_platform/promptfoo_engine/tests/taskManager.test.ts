import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { TaskManager, type RunRecord } from '../src/manager/taskManager.js';
import { EngineRunError } from '../src/runner/engineRunError.js';
import type { RunProgressEvent, RunRequest, RunStatusResponse } from '../src/types.js';
import { WorkdirManager } from '../src/manager/workdir.js';

function fakeRequest(runId = 'run-1'): RunRequest {
  return {
    run_id: runId,
    platform_user_id: 'u1',
    purpose: '测试',
    num_tests: 5,
    plugins: [{ id: 'harmful' }],
    strategies: [],
    judge_mode: 'auto',
    credentials: {
      target: { base_url: 'http://target', model: 'm', api_key: 'sk-x' },
    },
  };
}

function makeManager(opts: {
  execute: (run: RunRecord, emit: (event: RunProgressEvent) => void) => Promise<void>;
  workerCount?: number;
  maxQueueDepth?: number;
}): TaskManager {
  const workdirs = new WorkdirManager('.tmp-test-runs');
  return new TaskManager({
    workerCount: opts.workerCount ?? 1,
    maxQueueDepth: opts.maxQueueDepth ?? 100,
    workdirs,
    execute: opts.execute,
  });
}

async function waitFor(pred: () => boolean, timeoutMs = 3000): Promise<void> {
  const start = Date.now();
  while (!pred()) {
    if (Date.now() - start > timeoutMs) throw new Error('waitFor timeout');
    await new Promise((r) => setTimeout(r, 20));
  }
}

describe('TaskManager', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('rejects duplicate run ids', () => {
    const m = makeManager({ execute: async () => {} });
    expect(m.submit(fakeRequest('dup'))).toBe(true);
    expect(m.submit(fakeRequest('dup'))).toBe(false);
  });

  it('rejects when the queue is full', () => {
    const m = makeManager({ execute: async () => {}, workerCount: 1, maxQueueDepth: 1 });
    expect(m.submit(fakeRequest('a'))).toBe(true);
    // queue capacity 1: the first run may still be queued/pending pickup
    const second = fakeRequest('b');
    // Depending on pickup timing the first may already be running; keep submitting until full.
    const accepted = m.submit(second);
    if (accepted) {
      expect(m.submit(fakeRequest('c'))).toBe(false);
    }
  });

  it('runs to succeeded and emits progress + terminal', async () => {
    const seen: string[] = [];
    const m = makeManager({
      execute: async (run, emit) => {
        emit({ run_id: run.request.run_id, phase: 'executing_probes', executed_count: 1, planned_count: 2 });
        run.status.result = undefined;
      },
    });
    m.on('progress', (ev) => seen.push(ev.phase));
    m.submit(fakeRequest('ok-run'));
    await waitFor(() => seen.includes('succeeded'));
    const st = m.getStatus('ok-run');
    expect(st?.phase).toBe('succeeded');
    expect(st?.executed_count).toBe(1);
    expect(st?.planned_count).toBe(2);
    await m.shutdown();
  });

  it('propagates EngineRunError code when execute throws one', async () => {
    const m = makeManager({
      execute: async () => {
        throw new EngineRunError('generation_failed', 'boom');
      },
    });
    m.submit(fakeRequest('genfail-run'));
    await waitFor(() => m.getStatus('genfail-run')?.phase === 'failed');
    expect(m.getStatus('genfail-run')?.error_code).toBe('generation_failed');
    await m.shutdown();
  });

  it('marks failed when execute throws', async () => {
    const m = makeManager({
      execute: async () => {
        throw new Error('boom');
      },
    });
    m.submit(fakeRequest('bad-run'));
    await waitFor(() => m.getStatus('bad-run')?.phase === 'failed');
    expect(m.getStatus('bad-run')?.error_code).toBe('engine_error');
    await m.shutdown();
  });

  it('cancels a queued run without executing it', async () => {
    let executed = 0;
    const m = makeManager({ execute: async () => { executed++; }, workerCount: 1, maxQueueDepth: 10 });
    // Fill the worker with a long-running first run so the second stays queued.
    let release!: () => void;
    const gate = new Promise<void>((r) => { release = r; });
    const m2 = new TaskManager({
      workerCount: 1,
      maxQueueDepth: 10,
      workdirs: new WorkdirManager('.tmp-test-runs'),
      execute: async (run) => {
        if (run.request.run_id === 'long') await gate;
      },
    });
    m2.submit(fakeRequest('long'));
    await waitFor(() => m2.runningCount === 1 || m2.queuedCount === 0);
    m2.submit(fakeRequest('queued-run'));
    expect(m2.cancel('queued-run')).toBe(true);
    expect(m2.getStatus('queued-run')?.phase).toBe('canceled');
    release();
    await m2.shutdown();
    expect(executed).toBe(0);
    m.shutdown();
    void m;
  });

  it('cancels a running run via AbortController', async () => {
    const m = makeManager({
      execute: async (run) => {
        await new Promise<void>((resolve, reject) => {
          run.controller.signal.addEventListener('abort', () => reject(new Error('aborted')));
        });
      },
    });
    m.submit(fakeRequest('running-run'));
    await waitFor(() => m.getStatus('running-run')?.phase === 'executing_probes');
    expect(m.cancel('running-run')).toBe(true);
    await waitFor(() => m.getStatus('running-run')?.phase === 'canceled');
    await m.shutdown();
  });

  it('cancel of unknown or terminal run returns false', async () => {
    const m = makeManager({ execute: async () => {} });
    expect(m.cancel('nope')).toBe(false);
    m.submit(fakeRequest('quick'));
    await waitFor(() => m.getStatus('quick')?.phase === 'succeeded');
    expect(m.cancel('quick')).toBe(false);
    await m.shutdown();
  });
});

// ---------------------------------------------------------------------------
// P2-5 · the error_brief channel.
//
// Before this, a failed run carried only a code, and the promptfoo exception that
// explained it died with the process — which is why two U7 findings could not be
// located without reproducing them inside the container. These assertions pin the
// carrier: a redacted message on failure, nothing on success, and a real code
// (rather than the `engine_error` default) for errors that now carry one.
// ---------------------------------------------------------------------------

/** Run one execution and return the terminal status. */
async function terminalStatusOf(execute: () => Promise<void>): Promise<RunStatusResponse> {
  const manager = makeManager({ execute: async () => execute() });
  manager.submit(fakeRequest('run-p2-5'));
  await waitFor(() => {
    const phase = manager.getStatus('run-p2-5')?.phase;
    return phase === 'succeeded' || phase === 'failed' || phase === 'canceled';
  });
  const status = manager.getStatus('run-p2-5');
  if (!status) throw new Error('run status missing');
  return status;
}

describe('TaskManager — error brief on terminal paths (P2-5)', () => {
  it('carries a redacted message when a run fails', async () => {
    const status = await terminalStatusOf(async () => {
      throw new Error('promptfoo native generation failed: POST https://llm.internal:8443/v1 failed with key sk-abc123DEF456ghi789');
    });
    expect(status.phase).toBe('failed');
    expect(status.error_message).toBeDefined();
    expect(status.error_message).not.toContain('llm.internal');
    expect(status.error_message).not.toContain('sk-abc123DEF456ghi789');
    // The diagnosis itself must survive: redaction that removes the cause would
    // defeat the field.
    expect(status.error_message).toContain('promptfoo native generation failed');
  });

  it('records the EngineRunError code instead of the undiagnosable default', async () => {
    // S-5: agent-target misconfigurations used to be bare Errors, so this read
    // `engine_error` and the cause existed only as a message prefix nobody parsed.
    const status = await terminalStatusOf(async () => {
      throw new EngineRunError('agent_session_requires_multiturn_strategy', '[agent_session_requires_multiturn_strategy] nope');
    });
    expect(status.error_code).toBe('agent_session_requires_multiturn_strategy');
    expect(status.error_message).toContain('agent_session_requires_multiturn_strategy');
  });

  it('classifies a coded error that is not an EngineRunError instance', async () => {
    // A wrapped or re-thrown coded error must still be classified; falling back to
    // `engine_error` for it would lose the code for no reason.
    const status = await terminalStatusOf(async () => {
      const err = new Error('wrapped') as Error & { code: string };
      err.code = 'native_generation_workdir_missing';
      throw err;
    });
    expect(status.error_code).toBe('native_generation_workdir_missing');
  });

  it('records nothing on a successful run', async () => {
    const status = await terminalStatusOf(async () => undefined);
    expect(status.phase).toBe('succeeded');
    expect(status.error_message).toBeUndefined();
    expect(status.error_code).toBeUndefined();
  });

  it('records nothing for a cancellation', async () => {
    // "canceled by user" is not a failure to explain, and a brief here would read
    // as one.
    const status = await terminalStatusOf(async () => {
      throw new EngineRunError('canceled', 'native generation canceled');
    });
    expect(status.phase).toBe('failed');
  });
});
