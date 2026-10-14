/**
 * P2-3 lifecycle tests — promptfoo's own home directory, redirected.
 *
 * **Must be imported before anything that reaches promptfoo's tables chunk.**
 *
 * promptfoo latches `PROMPTFOO_CONFIG_DIR` into a module-level variable when its
 * config module is first evaluated, and it reads it again nowhere afterwards. So
 * the variable has to be set before the first import that pulls that module in,
 * which ESM's evaluate-imports-first rule makes impossible from inside a test
 * body. A separate side-effect module is the only construct that runs early
 * enough — the same remedy `u8PromptfooHome.ts` / `u9PromptfooHome.ts` use.
 *
 * Unlike those two this one does NOT pre-migrate: proving that the lifecycle
 * module migrates is the point of the suite, and a fixture that had already done
 * it would hide whether the code under test works.
 */
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const home = mkdtempSync(join(tmpdir(), 'p2-lifecycle-pfhome-'));
process.env['PROMPTFOO_CONFIG_DIR'] = home;

const cleanup = (): void => {
  try {
    rmSync(home, { recursive: true, force: true });
  } catch {
    // A leftover temp dir must never fail the suite.
  }
};
process.once('exit', cleanup);

export const P2_PROMPTFOO_HOME = home;
