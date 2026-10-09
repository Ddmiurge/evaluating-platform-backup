/**
 * Per-run working directory management.
 *
 * promptfoo keeps process-global state (cliState, base paths, caches), so the
 * adapter serializes runs inside isolated working directories (R1 mitigation)
 * and wipes them on terminal states. Nothing in here touches the promptfoo
 * library itself.
 */
import { mkdtemp, rm } from 'node:fs/promises';
import { join } from 'node:path';

export class WorkdirManager {
  private readonly dataRoot: string;
  private readonly paths = new Map<string, string>();

  constructor(dataRoot: string) {
    this.dataRoot = dataRoot;
  }

  /** Create an isolated working directory for a run: <dataRoot>/run-<id>-<rand>. */
  async create(runId: string): Promise<string> {
    if (this.paths.has(runId)) {
      throw new Error(`workdir for run ${runId} already exists`);
    }
    const dir = await mkdtemp(join(this.dataRoot, `run-${sanitize(runId)}-`));
    this.paths.set(runId, dir);
    return dir;
  }

  /** Remove the working directory (terminal states only). Idempotent. */
  async cleanup(runId: string): Promise<void> {
    const dir = this.paths.get(runId);
    if (!dir) return;
    this.paths.delete(runId);
    await rm(dir, { recursive: true, force: true });
  }

  has(runId: string): boolean {
    return this.paths.has(runId);
  }

  /** Current number of live workdirs (for health reporting). */
  get size(): number {
    return this.paths.size;
  }
}

function sanitize(s: string): string {
  // run ids come from the platform (uuid-ish); defense in depth anyway.
  return s.replace(/[^a-zA-Z0-9_-]/g, '').slice(0, 64);
}
