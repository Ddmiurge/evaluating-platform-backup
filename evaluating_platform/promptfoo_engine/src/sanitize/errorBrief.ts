/**
 * errorBrief.ts — P2-5 · the engine's half of the `error_brief` channel.
 *
 * # The gap this closes
 *
 * `engine_runs.error_brief` has a column, a Go struct field and a `$23`
 * placeholder — and, until now, no writer anywhere in the chain. `ApplyStatus`
 * never copied it and the engine's status response had no field to carry it, so
 * every diagnosis of a failed run meant reproducing the failure inside the
 * container. That is why the U7 acceptance run could not locate item8 ("multi-turn
 * strategies all blocked") or item12: the underlying promptfoo exception lived
 * only in the engine process's memory and died with the run.
 *
 * The engine already produced those messages (`nativeRedteam.ts` builds
 * `promptfoo native generation failed: ${errorMessage(err)}`) — nothing carried
 * them out.
 *
 * # Redaction is not optional
 *
 * promptfoo exceptions are written for operators, not for a tenant-facing API:
 * they can contain provider endpoints (internal topology), the model name, and
 * occasionally a fragment of the request. So the brief is redacted **before** it
 * leaves the engine, and redacted again on the platform side before it is
 * persisted — two independent implementations, because a single missed channel is
 * how secrets reach storage. Neither side may treat the other's output as already
 * safe.
 *
 * Rules, mirroring `internal/maclaw/redteam_artifact_service.go`'s shape:
 *
 *   - payload-looking markers → `[redacted]` wholesale (conservative by design)
 *   - credentials: `sk-…`, `Bearer …`, `api_key`/`token`/`password` assignments,
 *     and long opaque base64-ish runs
 *   - URL hosts replaced with `[host]`, keeping the scheme and path SHAPE (the path
 *     often names which step failed; the host leaks where it lives)
 *   - filesystem paths reduced to their basename when they look like a file
 *   - truncated to a fixed budget, because an error message must not become a
 *     storage-growth vector
 *
 * # What the brief is NOT
 *
 * It is not a prompt, a response, or a payload. The markers below are the ones the
 * platform already treats as "content", and a message that trips one is replaced
 * entirely rather than filtered — a partial redaction of a payload would be worse
 * than none, because it would look trustworthy.
 */

/** Hard cap on the stored brief. Long enough for a real cause, short enough to bound storage. */
export const ENGINE_ERROR_BRIEF_MAX_CHARS = 500;

/** Phrases that mean "this string is carrying content, not a diagnosis". */
const CONTENT_MARKERS = [
  'raw payload',
  'payload body',
  'raw prompt',
  'prompt body',
  'raw response',
  'target response body',
  'evidence content',
  'test case body',
];

/**
 * Credential-shaped substrings.
 *
 * The "opaque run" rule is the delicate one, and an earlier version of it was
 * wrong in a way worth recording: matching any 32+ character run of
 * `[A-Za-z0-9+/_-]` also matched **the error codes this module exists to carry**.
 * `agent_session_requires_multiturn_strategy` is 41 characters of exactly that
 * alphabet, so the brief came out as `[[redacted]] nope` — the redactor destroyed
 * the diagnosis. Redaction that removes the cause is not conservative, it is
 * broken.
 *
 * So a long run only counts as a token when it LOOKS like one:
 *
 *   - mixed case (real tokens mix; snake_case identifiers and error codes are
 *     uniformly lower) or base64 punctuation (`+`, `/`, `=`),
 *   - long hex, which is all lower by definition but has no separators and is
 *     almost never prose,
 *   - or very long (64+) with no separators at all, as a backstop.
 */
const SECRET_PATTERNS: RegExp[] = [
  // Vendor-style keys (`sk-…`) and explicit credential assignments.
  /\bsk-[A-Za-z0-9_-]{6,}/g,
  /\b(?:authorization|bearer|api[_-]?key|apikey|access[_-]?token|password|secret)\b\s*[:=]?\s*["']?[^\s"',;)]+/gi,
  // Mixed-case or base64-punctuated runs.
  /\b(?=[A-Za-z0-9+/=_-]{32,}\b)(?=[A-Za-z0-9+/=_-]*[A-Z])(?=[A-Za-z0-9+/=_-]*[a-z])[A-Za-z0-9+/=_-]+/g,
  /\b(?=[A-Za-z0-9+/=_-]{32,}\b)[A-Za-z0-9+/=_-]*[+/=][A-Za-z0-9+/=_-]*/g,
  // Long hex blobs (keys, digests).
  /\b[a-f0-9]{32,}\b/g,
  // Backstop: a very long unbroken run is not prose, whatever its case.
  /\b[A-Za-z0-9]{64,}\b/g,
];

/** `https://internal.host:8443/v1/chat/completions` -> `https://[host]/v1/chat/completions` */
const URL_PATTERN = /\b([a-z][a-z0-9+.-]*):\/\/[^/\s"']+(\/[^\s"']*)?/gi;

/**
 * Filesystem paths, matched only when they end in a file extension.
 *
 * The extension requirement is what keeps this from chewing up URL paths (handled
 * above) and prose. Keeping the basename is deliberate: `.../runs/<id>/redteam.yaml`
 * tells a reader which artifact was involved without naming the deployment's
 * directory layout.
 */
const ABSOLUTE_PATH_PATTERN = /(?:^|[\s("'=])(?:\/[\w.@+-]+)+\/([\w.@+-]+\.[A-Za-z0-9]{1,8})\b/g;

/**
 * Redact an engine error message for the `error_brief` field.
 *
 * Returns `''` for empty input so the stored value is an empty string rather than
 * `"undefined"` / `"<nil>"` — the field's readers treat emptiness as "no error
 * recorded" and a literal would read as one.
 */
export function redactEngineErrorBrief(value: string | undefined | null): string {
  const trimmed = (value ?? '').trim();
  if (trimmed === '') return '';

  const lower = trimmed.toLowerCase();
  if (CONTENT_MARKERS.some((marker) => lower.includes(marker))) {
    return '[redacted: message carried content]';
  }

  let out = trimmed;
  // Order matters. URLs first (so their host is replaced), then paths, and only
  // then the credential patterns — because the "long opaque run" rule admits `/`
  // and `+` (the base64 alphabet) and would otherwise swallow a whole filesystem
  // path as if it were one token. Rewriting the path to a bare basename first
  // takes it out of that rule's reach.
  out = out.replace(URL_PATTERN, (_match, scheme: string, path: string | undefined) =>
    `${scheme}://[host]${path ?? ''}`,
  );
  out = out.replace(ABSOLUTE_PATH_PATTERN, (_match, basename: string) => ` ${basename}`);
  for (const pattern of SECRET_PATTERNS) {
    out = out.replace(pattern, '[redacted]');
  }

  if (out.length > ENGINE_ERROR_BRIEF_MAX_CHARS) {
    out = `${out.slice(0, ENGINE_ERROR_BRIEF_MAX_CHARS)}...`;
  }
  return out;
}
