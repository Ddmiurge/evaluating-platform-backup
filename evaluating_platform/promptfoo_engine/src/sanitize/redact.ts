/**
 * Sanitization: whitelist-based redaction of engine outputs.
 *
 * Browser red line: prompts, raw target responses, credentials, local paths
 * must never leave the engine. Everything the adapter emits goes through
 * redactReason() / isAllowedField() here.
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

/** Whitelist check for numeric/count fields allowed to pass through as-is. */
const ALLOWED_TOP_FIELDS = new Set([
  'totals',
  'severity_counts',
  'risk_categories',
  'plugin_stats',
  'strategy_stats',
  'token_usage',
]);

export function isAllowedResultField(field: string): boolean {
  return ALLOWED_TOP_FIELDS.has(field);
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