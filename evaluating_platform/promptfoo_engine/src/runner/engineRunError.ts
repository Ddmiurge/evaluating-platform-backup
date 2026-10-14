/**
 * engineRunError.ts — the one error type whose `code` reaches the platform.
 *
 * # Why it lives in its own module
 *
 * Every other error the engine throws is an implementation detail: the task
 * manager converts it to `engine_error` and the platform never sees the cause. A
 * `code` here is different — it is copied onto the run record, returned over the
 * status endpoint, stored on the engine_runs row and shown to the user, which
 * makes it part of the API contract.
 *
 * It used to be declared inside `generateTests.ts`, which meant
 * `agentSession.ts` — a module with no other dependency on the Plan A generation
 * path — had to import that file to produce a coded error, and instead threw bare
 * `Error`s whose code existed only as a message prefix. A prefix is not a code:
 * `taskManager.runSafe` reads `err.code`, found none, and filed every agent-target
 * misconfiguration under the undiagnosable default. Moving the class here lets
 * every module that needs a CODE use one, regardless of which generation path it
 * belongs to.
 *
 * `generateTests.ts` re-exports it, so existing imports keep working.
 */
export class EngineRunError extends Error {
  constructor(
    /**
     * Machine-readable code. Part of the engine/BFF API contract: the platform
     * copies it verbatim onto the run record and surfaces it to the user.
     */
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = 'EngineRunError';
  }
}
