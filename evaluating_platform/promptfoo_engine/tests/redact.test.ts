import { describe, expect, it } from 'vitest';
import {
  REASON_MAX_LENGTH,
  isAllowedResultField,
  redactReason,
  redactTopReasons,
  sanitizeSeverity,
} from '../src/sanitize/redact.js';

describe('redactReason', () => {
  it('strips API keys', () => {
    const out = redactReason('called with sk-abcdefghijklmnop failed because output refused');
    expect(out).not.toContain('sk-abcdefghijklmnop');
    expect(out).toContain('[redacted-key]');
  });

  it('strips bearer tokens', () => {
    const out = redactReason('Authorization: Bearer abcdefgh12345678 was rejected');
    expect(out).toContain('[redacted]');
    expect(out).not.toContain('abcdefgh12345678');
  });

  it('strips absolute filesystem paths', () => {
    const out = redactReason('artifact written to /app/data/runs/run-123/out.json then read');
    expect(out).toContain('[path]');
    expect(out).not.toContain('/app/data/runs');
  });

  it('strips emails', () => {
    const out = redactReason('leaked contact someone@example.com in response');
    expect(out).not.toContain('someone@example.com');
    expect(out).toContain('[email]');
  });

  it('strips URLs', () => {
    const out = redactReason('fetched https://internal.minio.local/report.pdf?sig=1 failed');
    expect(out).not.toContain('https://internal.minio.local');
    expect(out).toContain('[url]');
  });

  it('strips long base64-like blobs but keeps ordinary words', () => {
    const out = redactReason('body contained q9Zx2Pm8Lk4Vb7Nc3Qw1Rt5Yh6Df0Gj2Ki9Mq8Lw3Er1Ty6Ui5Op0As7Df8Gh9 payload');
    expect(out).toContain('[blob]');
    expect(out).not.toContain('q9Zx2Pm8Lk4Vb7Nc3Qw1Rt5Yh6Df0Gj2Ki9Mq8Lw3Er1Ty6Ui5Op0As7Df8Gh9');
    // plain prose is untouched
    expect(redactReason('the model refused the request')).toBe('the model refused the request');
  });

  it('truncates to REASON_MAX_LENGTH', () => {
    const long = 'a'.repeat(500);
    const out = redactReason(long);
    expect(out.length).toBeLessThanOrEqual(REASON_MAX_LENGTH);
  });

  it('returns empty string for non-string input', () => {
    expect(redactReason(undefined)).toBe('');
    expect(redactReason(123)).toBe('');
    expect(redactReason({})).toBe('');
  });

  it('collapses whitespace', () => {
    expect(redactReason('too   much\t\nwhitespace here')).toBe('too much whitespace here');
  });
});

describe('isAllowedResultField (whitelist)', () => {
  it('allows only the six safe aggregate fields', () => {
    for (const f of [
      'totals',
      'severity_counts',
      'risk_categories',
      'plugin_stats',
      'strategy_stats',
      'token_usage',
    ]) {
      expect(isAllowedResultField(f)).toBe(true);
    }
    for (const f of ['prompts', 'responses', 'credentials', 'api_key', '__proto__', 'path']) {
      expect(isAllowedResultField(f)).toBe(false);
    }
  });
});

describe('redactTopReasons', () => {
  it('keeps at most three entries and filters empties', () => {
    const out = redactTopReasons(['ok', '', 'also ok', 'third', 'fourth']);
    // slice(0,3) happens before the empty filter: the first three entries
    // are ['ok', '', 'also ok'] -> empties dropped -> two survive.
    expect(out).toEqual(['ok', 'also ok']);
    // A third non-empty entry inside the first three is kept.
    expect(redactTopReasons(['a', 'b', 'c', 'd'])).toEqual(['a', 'b', 'c']);
  });

  it('returns [] for non-array input', () => {
    expect(redactTopReasons('nope')).toEqual([]);
    expect(redactTopReasons(null)).toEqual([]);
  });
});

describe('sanitizeSeverity', () => {
  it('maps unknown severities to none', () => {
    expect(sanitizeSeverity('critical')).toBe('critical');
    expect(sanitizeSeverity('high')).toBe('high');
    expect(sanitizeSeverity('medium')).toBe('medium');
    expect(sanitizeSeverity('low')).toBe('low');
    expect(sanitizeSeverity('informational')).toBe('none');
    expect(sanitizeSeverity(undefined)).toBe('none');
    expect(sanitizeSeverity('HIGH')).toBe('none'); // strict match
  });
});