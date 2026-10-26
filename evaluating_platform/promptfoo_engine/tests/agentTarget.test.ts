/**
 * Agent target (kind=agent) provider tests: the custom-HTTP template must
 * render into a promptfoo http provider config with {{prompt}} left for
 * nunjucks and {{api_key}} substituted server-side (never serialized).
 */
import { describe, expect, it } from 'vitest';
import { buildTargetProvider, toAgentTransformResponse } from '../src/runner/redteamRun.js';
import type { OneShotCredentials } from '../src/types.js';

function targetCreds(overrides: Partial<OneShotCredentials['target']>): OneShotCredentials['target'] {
  return { base_url: 'https://llm.example/v1', model: 'm', api_key: 'sk-agent-secret', ...overrides };
}

describe('buildTargetProvider (agent targets)', () => {
  it('renders endpoint/headers/body templates and substitutes {{api_key}} server-side', () => {
    const provider = buildTargetProvider(
      targetCreds({
        agent_endpoint: 'https://agent.example.com/chat?token={{api_key}}',
        agent_method: 'post',
        agent_headers_template: '{"X-Api-Key":"{{api_key}}"}',
        agent_body_template: '{"query":"{{prompt}}","session":"abc"}',
        agent_response_path: 'reply.text',
      }),
    );
    expect(provider.id).toBe('http');
    const config = provider.config as Record<string, any>;
    expect(config.url).toBe('https://agent.example.com/chat?token=sk-agent-secret');
    expect(config.method).toBe('POST');
    expect(config.headers['X-Api-Key']).toBe('sk-agent-secret');
    expect(config.headers['Content-Type']).toBe('application/json');
    // {{prompt}} must survive for promptfoo's per-test substitution.
    expect(config.body).toEqual({ query: '{{prompt}}', session: 'abc' });
    // The expression also carries the miss handling (see
    // toAgentTransformResponse): a bare path yields undefined, which the report
    // cannot tell from an empty answer.
    expect(config.transformResponse).toContain('json.reply.text');
    // Only a 2xx is a response. promptfoo's default accepts every status, which
    // made a 404 from the target indistinguishable from an empty answer.
    // Held as a string because the config is written to YAML; promptfoo turns it
    // into a predicate the same way.
    expect(typeof config.validateStatus).toBe('string');
    const validateStatus = new Function('status', `return ${config.validateStatus}`) as (s: number) => boolean;
    expect(validateStatus(200)).toBe(true);
    expect(validateStatus(404)).toBe(false);
    expect(validateStatus(500)).toBe(false);
    // The secret must never ride along as a literal anywhere else.
    expect(JSON.stringify(config.body)).not.toContain('sk-agent-secret');
  });

  it('falls back to defaults: POST, JSON content type, bearer auth, prompt body', () => {
    const provider = buildTargetProvider(
      targetCreds({ agent_endpoint: 'https://agent.example.com/chat' }),
    );
    const config = provider.config as Record<string, any>;
    expect(config.method).toBe('POST');
    expect(config.headers['Content-Type']).toBe('application/json');
    // Secret set but no {{api_key}} reference → default bearer header.
    expect(config.headers['Authorization']).toBe('Bearer sk-agent-secret');
    expect(config.body).toEqual({ prompt: '{{prompt}}' });
    expect(config.transformResponse).toBe('json');
  });

  it('keeps the LLM chat/completions provider for non-agent targets', () => {
    const provider = buildTargetProvider(targetCreds({}));
    const config = provider.config as Record<string, any>;
    expect(config.url).toBe('https://llm.example/v1/chat/completions');
    expect(config.body.model).toBe('m');
    expect(config.transformResponse).toBe('json.choices[0].message.content');
  });

  it('bounds retries and does not pretend to set a per-provider timeout', () => {
    // Measured 2026-10-26: we used to pass `timeout`, and before that `timeoutMs`
    // — promptfoo's http provider reads NEITHER. Its bound is the process-level
    // `REQUEST_TIMEOUT_MS` (fetch-*.js:47). A config key nobody reads is worse
    // than no key: it made the 15-minute hang look configured.
    const provider = buildTargetProvider(
      targetCreds({ agent_endpoint: 'https://agent.example.com/chat', timeout_seconds: 12 }),
    );
    const config = provider.config as Record<string, any>;
    expect(config.timeout).toBeUndefined();
    expect(config.timeoutMs).toBeUndefined();
    expect(config.maxRetries).toBe(0);
  });
});

describe('toAgentTransformResponse', () => {
  /** Evaluate the expression the way promptfoo does — the behaviour is the contract. */
  function run(expr: string, json: unknown): unknown {
    return new Function('json', 'text', 'context', 'process', `return (${expr});`)(json, '', undefined, undefined);
  }

  it('maps dot paths and openai-style paths', () => {
    expect(toAgentTransformResponse('reply.text')).toContain('json.reply.text');
    expect(toAgentTransformResponse('choices[0].message.content')).toContain(
      'json.choices[0].message.content',
    );
    expect(toAgentTransformResponse('json.data.reply')).toContain('json.data.reply');
  });

  it('falls back to the whole body when no path is configured', () => {
    expect(toAgentTransformResponse('  ')).toBe('json');
    expect(toAgentTransformResponse(undefined)).toBe('json');
    expect(run(toAgentTransformResponse(undefined), { any: 'thing' })).toEqual({ any: 'thing' });
  });

  it('returns the configured field when it is there', () => {
    expect(run(toAgentTransformResponse('answer'), { answer: '你好' })).toBe('你好');
    expect(run(toAgentTransformResponse('reply.text'), { reply: { text: 'ok' } })).toBe('ok');
  });

  // The regression this exists for. A body without the configured path used to
  // yield `undefined`; the report rendered that as an empty answer and
  // promptfoo's rubric graded an empty answer as a pass, so a misconfigured
  // extraction path read as 「目标没有泄露」 instead of 「没测到」.
  it('throws when the configured path matches nothing', () => {
    expect(() => run(toAgentTransformResponse('answer'), { output: '' })).toThrow();
    expect(() => run(toAgentTransformResponse('answer'), {})).toThrow();
    expect(() => run(toAgentTransformResponse('reply.text'), { reply: {} })).toThrow();
  });
});
