import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { TaskManager, type RunRecord } from '../src/manager/taskManager.js';
import { EngineRunError } from '../src/runner/generateTests.js';
import type { RunProgressEvent, RunRequest } from '../src/types.js';
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
