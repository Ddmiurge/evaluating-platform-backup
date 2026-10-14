import { describe, expect, it } from 'vitest';

import { ENGINE_ERROR_BRIEF_MAX_CHARS, redactEngineErrorBrief } from '../src/sanitize/errorBrief.js';

/**
 * P2-5 · the redaction rules that make `error_brief` storable.
 *
 * The field is new, but the reason for these assertions is not: `engine_runs` is
 * a tenant-facing table and a promptfoo exception is written for an operator. It
 * can carry the provider endpoint (internal topology), the model name, and
 * occasionally a fragment of the request. Each rule below is a counter-proof —
 * remove one and the matching case goes red.
 */
describe('redactEngineErrorBrief — credentials and topology', () => {
  it('removes vendor-style keys', () => {
    const out = redactEngineErrorBrief('provider rejected key sk-abc123DEF456ghi789 at startup');
    expect(out).not.toContain('sk-abc123DEF456ghi789');
    expect(out).toContain('[redacted]');
    expect(out).toContain('provider rejected key');
  });

  it('removes credential assignments in every common spelling', () => {
    for (const raw of [
      'api_key: super-secret-value',
      'apiKey=super-secret-value',
      'Authorization: Bearer super-secret-value',
      'password="super-secret-value"',
      'access_token: super-secret-value',
    ]) {
      const out = redactEngineErrorBrief(`request failed (${raw})`);
      expect(out, raw).not.toContain('super-secret-value');
    }
  });

  it('removes long opaque runs that are almost never prose', () => {
    // Mixed case is what distinguishes a token from an identifier — see the
    // `keeps an error code readable` case below for the other side of the rule.
    const mixed = 'QWxhZGRpbjpvcGVuIHNlc2FtZQabcdefgh';
    expect(redactEngineErrorBrief(`signature mismatch: ${mixed}`)).not.toContain(mixed);

    const hex = 'a3f9c1d07b2e4856fa90cd13be7450f2';
    expect(redactEngineErrorBrief(`digest mismatch: ${hex}`)).not.toContain(hex);
  });

  it('keeps an error code readable, because that is the point of the field', () => {
    // The regression this pins: `agent_session_requires_multiturn_strategy` is 41
    // characters of the same alphabet the opaque-run rule matched, so an earlier
    // version redacted the code out of its own message.
    const message =
      '[agent_session_requires_multiturn_strategy] 请求体模板里用了 {{conversation_id}}，但本次运行没有选择多轮策略';
    expect(redactEngineErrorBrief(message)).toContain('agent_session_requires_multiturn_strategy');
  });

  it('replaces the URL host but keeps the scheme and path shape', () => {
    // The path usually names the step that failed; the host says where the
    // deployment lives, which is not the reader's business.
    const out = redactEngineErrorBrief('POST https://llm.internal.corp:8443/v1/chat/completions failed with 401');
    expect(out).not.toContain('llm.internal.corp');
    expect(out).not.toContain('8443');
    expect(out).toContain('https://[host]/v1/chat/completions');
  });

  it('reduces filesystem paths to their basename', () => {
    const out = redactEngineErrorBrief('cannot read /home/node/.promptfoo/runs/run-1/redteam-config.yaml');
    expect(out).not.toContain('/home/node');
    expect(out).toContain('redteam-config.yaml');
  });

  it('leaves a URL path alone rather than treating it as a filesystem path', () => {
    // The extension rule is what separates the two; without it a URL path would
    // be mangled into a bare filename and the reader would lose the step name.
    const out = redactEngineErrorBrief('GET https://target.internal/v1/models.json timed out');
    expect(out).toContain('https://[host]/v1/models.json');
  });
});

describe('redactEngineErrorBrief — payload-shaped messages', () => {
  it('replaces wholesale when the message says it carries content', () => {
    // Conservative on purpose: a partially redacted payload looks trustworthy,
    // which is worse than an obviously missing one.
    for (const raw of [
      'raw payload: {"prompt": "..."}',
      'raw prompt was rejected',
      'target response body exceeded the limit',
      'evidence content could not be parsed',
    ]) {
      expect(redactEngineErrorBrief(raw), raw).toBe('[redacted: message carried content]');
    }
  });
});

describe('redactEngineErrorBrief — shape and boundaries', () => {
  it('bounds the length so the field cannot become a growth vector', () => {
    // Natural text on purpose: a single long token would be redacted wholesale by
    // the opaque-run rule, so it would never reach the truncation branch.
    const out = redactEngineErrorBrief('the provider rejected the request because '.repeat(40));
    expect(out.length).toBeLessThanOrEqual(ENGINE_ERROR_BRIEF_MAX_CHARS + 3);
    expect(out.endsWith('...')).toBe(true);
  });

  it('returns an empty string, never a literal, for absent input', () => {
    // Readers treat emptiness as "no error recorded" and `"undefined"` as one.
    for (const raw of [undefined, null, '', '   ']) {
      expect(redactEngineErrorBrief(raw)).toBe('');
    }
  });

  it('keeps an ordinary diagnosis readable', () => {
    // The point of the field is diagnosis; over-redaction would defeat it.
    const out = redactEngineErrorBrief('jailbreak:meta strategy requires remote generation');
    expect(out).toBe('jailbreak:meta strategy requires remote generation');
  });

  it('is idempotent, so the platform can redact again without damaging the text', () => {
    const once = redactEngineErrorBrief('POST https://llm.internal:8443/v1 failed with key sk-abc123DEF456ghi789');
    expect(redactEngineErrorBrief(once)).toBe(once);
  });
});
