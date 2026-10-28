/**
 * Sanitization: whitelist-based redaction of engine outputs.
 *
 * # What the red line is now (2026-10-16)
 *
 * It used to read "prompts, raw target responses, credentials, local paths must
 * never leave the engine". Prompts and responses were removed from that list on
 * purpose: the product decision is that a red-team report has to show the attack
 * and the answer, or it is not a report (see
 * `23-聊天链路报告原文与promptfoo对齐.md`). They now leave through exactly one
 * field, `SafeRunResult.case_results`, and only on a terminal result.
 *
 * What still must never leave: **credentials, tokens, filesystem paths, and the
 * platform judge's raw values**. `redactReason()` and the credential patterns
 * below are what enforce that, and `case_results` is deliberately the one field
 * that does NOT pass through `redactReason` — that function is a *summary*
 * bounder (200 chars), and truncating the payload is the failure this change
 * exists to fix.
 */

/** Max length of any human-readable reason/summary string. */
export const REASON_MAX_LENGTH = 200;

/** Strip patterns that may leak raw payload content or secrets. */
const SENSITIVE_PATTERNS: Array<[RegExp, string]> = [
  // API keys and bearer tokens
  [/sk-[A-Za-z0-9_-]{8,}/g, '[redacted-key]'],
  [/Bearer\s+[A-Za-z0-9._-]{8,}/gi, 'Bearer [redacted]'],
  // Absolute filesystem paths, Windows (C:\...) and POSIX (/home/...)
  [/[A-Za-z]:\\(?:[\w.-]+\\)*[\w.-]+/g, '[path]'],
  [/\/(?:home|app|data|tmp|var|Users)[A-Za-z0-9_\-./]*\/[A-Za-z0-9_\-./]+/g, '[path]'],
  // Long base64-ish blobs that could carry payload/response bodies.
  // Require mixed case+digits so plain letter runs (e.g. 'xxxx...') are not swallowed.
  [/(?=[A-Za-z0-9+/]{32,})(?=[^\s]*[a-z])(?=[^\s]*[A-Z])(?=[^\s]*[0-9])[A-Za-z0-9+/]+={0,2}/g, '[blob]'],
  // Emails
  [/[\w.+-]+@[\w-]+\.[\w.]+/g, '[email]'],
  // URLs (could contain signed MinIO links or internal endpoints)
  [/https?:\/\/[^\s"'<>]+/g, '[url]'],
];

/**
 * Redact a free-text reason: apply pattern strips, collapse whitespace,
 * truncate to REASON_MAX_LENGTH. Returns a safe display string.
 */
export function redactReason(input: unknown): string {
  if (typeof input !== 'string') return '';
  let out = input;
  for (const [re, replacement] of SENSITIVE_PATTERNS) {
    out = out.replace(re, replacement);
  }
  out = out.replace(/\s+/g, ' ').trim();
  if (out.length > REASON_MAX_LENGTH) {
    out = `${out.slice(0, REASON_MAX_LENGTH - 1)}…`;
  }
  return out;
}

/**
 * The top-level fields of `SafeRunResult` that may leave the engine.
 *
 * # This list is enforced now (2026-10-16)
 *
 * It was decorative: `isAllowedResultField` had no production call site, and
 * the list was missing three fields that demonstrably DID flow (`generation_path`
 * — P2-6, `num_tests_effective`, `judge_sources` — U8). Connecting it as-is would
 * have silently stripped P2-6 and U8 out of every report — which is why the
 * missing entries and the wiring land together rather than the wiring alone.
 *
 * `assertResultFieldsAreKnown()` is that connection: the reducer's output is
 * checked against this list, so a field added to `SafeRunResult` without being
 * declared here fails loudly instead of either leaking or vanishing.
 *
 * # Why a whitelist at all, when the text is allowed through
 *
 * Because "the report may carry the payload" is not the same as "anything the
 * engine happens to produce may reach the browser". The list is what makes the
 * exception a decision instead of a habit: exactly one text-bearing field
 * (`case_results`) is on it, and adding a second takes an edit here.
 */
const ALLOWED_TOP_FIELDS = new Set([
  'totals',
  'severity_counts',
  'risk_categories',
  'plugin_stats',
  'strategy_stats',
  // U9: two integers and nothing else — see SafeRunResult.tool_call_signals.
  // Safe to whitelist precisely because it carries no attacker-influenced text;
  // that is the whole reason the detector keeps counts instead of tool
  // arguments.
  'tool_call_signals',
  'token_usage',
  // P2-6. These two report which generation path produced the run and how many
  // probes it actually configured. Both are fixed-enum/integer metadata.
  'generation_path',
  'num_tests_effective',
  // P18: how many expert CODE strategy packages ran, and their public ids. An
  // integer and `custom:code:<variant>` tokens — fixed shape, no content, no
  // paths. The report needs it to state the trust level of its own numbers.
  'code_strategy_count',
  'code_strategy_ids',
  // U8: which rubric judged each group of probes. Enum-shaped source ids.
  'judge_sources',
  // The single text-bearing field, and the reason this list exists: the report is
  // required to show the attack and the answer (2026-10-16 product decision).
  'case_results',
]);

export function isAllowedResultField(field: string): boolean {
  return ALLOWED_TOP_FIELDS.has(field);
}

/**
 * Fail loudly when the reducer emits a field this whitelist does not declare.
 *
 * The alternative — dropping the unknown field — turns "someone added a result
 * field" into "a report quietly lost a section", which is the failure shape this
 * codebase keeps re-learning. An allow-list is only worth having if violating it
 * is visible.
 */
export function assertResultFieldsAreKnown(result: object): void {
  const unknown = Object.keys(result).filter((key) => !ALLOWED_TOP_FIELDS.has(key));
  if (unknown.length > 0) {
    throw new Error(
      `SafeRunResult carries undeclared field(s): ${unknown.join(', ')}. ` +
        'Add them to ALLOWED_TOP_FIELDS in sanitize/redact.ts, and say in that ' +
        'list’s doc why they are safe to leave the engine.',
    );
  }
}

/** Sanitize the top-reasons array of a risk-category plugin entry. */
export function redactTopReasons(reasons: unknown): string[] {
  if (!Array.isArray(reasons)) return [];
  return reasons.slice(0, 3).map((r) => redactReason(r)).filter((r) => r.length > 0);
}

/** Clamp a severity to the known set. */
export function sanitizeSeverity(sev: unknown): 'critical' | 'high' | 'medium' | 'low' | 'none' {
  switch (sev) {
    case 'critical':
    case 'high':
    case 'medium':
    case 'low':
      return sev;
    default:
      return 'none';
  }
}