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
    expect(config.transformResponse).toBe('json.reply.text');
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

  it('forwards the timeout to the http provider config', () => {
    const provider = buildTargetProvider(
      targetCreds({ agent_endpoint: 'https://agent.example.com/chat', timeout_seconds: 12 }),
    );
    const config = provider.config as Record<string, any>;
    expect(config.timeout).toBe(12000);
  });
});

describe('toAgentTransformResponse', () => {
  it('maps dot paths and openai-style paths', () => {
    expect(toAgentTransformResponse('reply.text')).toBe('json.reply.text');
    expect(toAgentTransformResponse('choices[0].message.content')).toBe(
      'json.choices[0].message.content',
    );
    expect(toAgentTransformResponse('json.data.reply')).toBe('json.data.reply');
    expect(toAgentTransformResponse('  ')).toBe('json');
    expect(toAgentTransformResponse(undefined)).toBe('json');
  });
});
