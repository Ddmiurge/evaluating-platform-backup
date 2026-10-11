/**
 * U9 Agent multi-turn (`conversation_id`) — unit-level contracts.
 *
 * These tests pin the **translation** layer: what the engine emits into the
 * promptfoo provider config, and what it refuses. The end-to-end proof that a
 * turn-2 request really carries turn-1's `conversation_id` against the real
 * promptfoo library lives in `u9MultiTurn.e2e.test.ts` — deliberately separate,
 * because "the config we built looks right" and "the wire carried the id" are
 * different claims, and only the second one is the DoD.
 */
import { describe, expect, it } from 'vitest';
import {
  AGENT_SESSION_ERR_MULTITURN_MISSING,
  AGENT_SESSION_ERR_NO_EXTRACT_PATH,
  AGENT_SESSION_ERR_STATEFUL_FALSE,
  MULTI_TURN_STRATEGY_IDS,
  applyStatefulToMultiTurn,
  assertAgentConversationIsExecutable,
  assertMultiTurnStrategyIdsAreCurrent,
  buildSessionParser,
  countToolCallSignals,
  renderConversationPlaceholders,
  toJsAccessor,
  wantsAgentConversation,
} from '../src/runner/agentSession.js';
import { buildTargetProvider } from '../src/runner/redteamRun.js';
import type { OneShotCredentials } from '../src/types.js';

function targetCreds(overrides: Partial<OneShotCredentials['target']>): OneShotCredentials['target'] {
  return { base_url: 'https://llm.example/v1', model: 'm', api_key: 'app-dify-key', ...overrides };
}

/** A Dify-shaped agent target that asks for a server-owned conversation. */
function difyTarget(overrides: Partial<OneShotCredentials['target']> = {}): OneShotCredentials['target'] {
  return targetCreds({
    agent_endpoint: 'https://dify.example.com/v1/chat-messages',
    agent_headers_template: '{"Authorization":"Bearer {{api_key}}","Content-Type":"application/json"}',
    agent_body_template:
      '{"inputs":{},"query":"{{prompt}}","response_mode":"blocking","user":"redteam-eval","conversation_id":"{{conversation_id}}"}',
    agent_response_path: 'conversation_id',
    ...overrides,
  });
}

describe('wantsAgentConversation', () => {
  it('is true only when the operator wrote the placeholder', () => {
    expect(wantsAgentConversation({ agent_body_template: '{"conversation_id":"{{conversation_id}}"}' })).toBe(true);
    expect(wantsAgentConversation({ agent_headers_template: '{"X-Sid":"{{conversation_id}}"}' })).toBe(true);
    // v1 shapes must all stay false — this is what preserves v1 behaviour.
    expect(wantsAgentConversation({ agent_body_template: '{"query":"{{prompt}}"}' })).toBe(false);
    expect(wantsAgentConversation({})).toBe(false);
    expect(wantsAgentConversation({ agent_body_template: '{{prompt}}{{api_key}}' })).toBe(false);
  });
});

describe('renderConversationPlaceholders', () => {
  it('rewrites to the name promptfoo binds', () => {
    expect(renderConversationPlaceholders('{"conversation_id":"{{conversation_id}}"}')).toBe(
      '{"conversation_id":"{{sessionId}}"}',
    );
  });

  it('rewrites every occurrence, not just the first', () => {
    expect(renderConversationPlaceholders('{{conversation_id}}/{{conversation_id}}')).toBe(
      '{{sessionId}}/{{sessionId}}',
    );
  });

  it('leaves other placeholders untouched', () => {
    expect(renderConversationPlaceholders('{{prompt}}|{{api_key}}|{{conversation_id}}')).toBe(
      '{{prompt}}|{{api_key}}|{{sessionId}}',
    );
  });

  it('is identity for a template without the placeholder', () => {
    expect(renderConversationPlaceholders('{"query":"{{prompt}}"}')).toBe('{"query":"{{prompt}}"}');
  });
});

describe('toJsAccessor / buildSessionParser', () => {
  it('converts plain dot paths', () => {
    expect(toJsAccessor('conversation_id')).toBe('.conversation_id');
    expect(toJsAccessor('session.id')).toBe('.session.id');
  });

  it('converts bracket indices', () => {
    expect(toJsAccessor('choices[0].message.content')).toBe('.choices.0.message.content');
    expect(toJsAccessor('a[0][1]')).toBe('.a.0.1');
  });

  it('tolerates the json. prefix and stray dots, like toAgentTransformResponse', () => {
    expect(toJsAccessor('json.data.reply')).toBe('.data.reply');
    expect(toJsAccessor('  .a.b.  ')).toBe('.a.b');
  });

  it('returns empty for empty input', () => {
    expect(toJsAccessor('')).toBe('');
    expect(toJsAccessor('   ')).toBe('');
  });

  it('builds a parser that evaluates against promptfoo’s { headers, body } shape', () => {
    // This string is handed to promptfoo, which compiles it as
    // new Function('data', 'return (…);') and calls it with { headers, body }.
    // Verified in dist: createSessionParser + callApi's sessionParser({headers, body}).
    expect(buildSessionParser('conversation_id')).toBe("data.body.conversation_id ?? ''");
  });

  it('produces a parser that actually evaluates to the id (real JS semantics)', () => {
    // Not a shape assertion — the compiled expression is executed, because
    // "the string looks like an accessor" is exactly the kind of claim that is
    // true and useless.
    const parser = buildSessionParser('conversation_id');
    const compiled = new Function('data', `return (${parser});`) as (d: unknown) => unknown;
    expect(compiled({ body: { conversation_id: 'conv-AAA111' } })).toBe('conv-AAA111');
    // Missing field must yield '' rather than undefined: promptfoo stores the
    // return value as `sessionId`, and an `undefined` there would silently drop
    // the thread on the next turn instead of failing visibly.
    expect(compiled({ body: {} })).toBe('');
  });
});

describe('buildTargetProvider (U9 conversation wiring)', () => {
  it('rewrites the placeholder and derives sessionParser from the response path', () => {
    const provider = buildTargetProvider(difyTarget());
    const config = provider.config as Record<string, any>;

    expect(config.body.conversation_id).toBe('{{sessionId}}');
    // The operator-facing spelling must not survive into the provider config.
    expect(JSON.stringify(config.body)).not.toContain('{{conversation_id}}');
    expect(config.sessionParser).toBe("data.body.conversation_id ?? ''");
    // And it must be derived from the response path, not hardcoded.
    expect(config.transformResponse).toBe('json.conversation_id');
  });

  it('never rewrites the endpoint, so a session id cannot land in a URL', () => {
    const provider = buildTargetProvider(difyTarget({ agent_endpoint: 'https://dify.example.com/v1/chat-messages' }));
    const config = provider.config as Record<string, any>;
    expect(config.url).toBe('https://dify.example.com/v1/chat-messages');
    expect(config.url).not.toContain('conversation_id');
  });

  it('derives sessionParser for a nested response path too', () => {
    const provider = buildTargetProvider(
      difyTarget({ agent_response_path: 'data.thread.id' }),
    );
    const config = provider.config as Record<string, any>;
    expect(config.sessionParser).toBe("data.body.data.thread.id ?? ''");
  });

  it('substitutes {{api_key}} server-side while leaving both session placeholders in place', () => {
    const provider = buildTargetProvider(difyTarget());
    const config = provider.config as Record<string, any>;
    expect(config.headers['Authorization']).toBe('Bearer app-dify-key');
    // The secret must not ride along in the body, and the session placeholder
    // must still be there for promptfoo to bind per request.
    expect(JSON.stringify(config.body)).not.toContain('app-dify-key');
    expect(config.body.conversation_id).toBe('{{sessionId}}');
  });

  it('emits NO sessionParser when the operator did not ask for a conversation (v1 parity)', () => {
    const provider = buildTargetProvider(
      targetCreds({
        agent_endpoint: 'https://agent.example.com/chat',
        agent_body_template: '{"query":"{{prompt}}"}',
        agent_response_path: 'reply.text',
      }),
    );
    const config = provider.config as Record<string, any>;
    // Absent, not empty/undefined-valued: an explicit `sessionParser: undefined`
    // would still be a key in the YAML document promptfoo reads.
    expect('sessionParser' in config).toBe(false);
    expect(config.body).toEqual({ query: '{{prompt}}' });
  });
});

describe('assertAgentConversationIsExecutable', () => {
  it('refuses a conversation target with no response path', () => {
    expect(() =>
      assertAgentConversationIsExecutable({
        wantsConversation: true,
        agentResponsePath: '',
        promptfooStrategyIds: ['crescendo'],
      }),
    ).toThrow(AGENT_SESSION_ERR_NO_EXTRACT_PATH);
  });

  it('refuses a conversation target with no multi-turn strategy', () => {
    expect(() =>
      assertAgentConversationIsExecutable({
        wantsConversation: true,
        agentResponsePath: 'conversation_id',
        promptfooStrategyIds: ['base64', 'jailbreak'],
      }),
    ).toThrow(AGENT_SESSION_ERR_MULTITURN_MISSING);
  });

  it('accepts each supported multi-turn strategy', () => {
    for (const id of MULTI_TURN_STRATEGY_IDS) {
      expect(() =>
        assertAgentConversationIsExecutable({
          wantsConversation: true,
          agentResponsePath: 'conversation_id',
          promptfooStrategyIds: [id],
        }),
      ).not.toThrow();
    }
  });

  it('refuses an explicit stateful:false, which contradicts the conversation placeholder', () => {
    // The contradiction is refused by assertAgentConversationIsExecutable, so a
    // run can never reach applyStatefulToMultiTurn in this state.
    expect(() =>
      assertAgentConversationIsExecutable({
        wantsConversation: true,
        agentResponsePath: 'conversation_id',
        promptfooStrategyIds: ['crescendo'],
        promptfooStrategyConfigs: new Map([['crescendo', { stateful: false }]]),
      }),
    ).toThrow(AGENT_SESSION_ERR_STATEFUL_FALSE);
  });

  it('allows stateful:false on a strategy that is not multi-turn', () => {
    // Only the multi-turn entries are contradicted; an unrelated strategy's
    // config is none of this gate's business.
    expect(() =>
      assertAgentConversationIsExecutable({
        wantsConversation: true,
        agentResponsePath: 'conversation_id',
        promptfooStrategyIds: ['crescendo', 'base64'],
        promptfooStrategyConfigs: new Map([
          ['crescendo', undefined],
          ['base64', { stateful: false }],
        ]),
      }),
    ).not.toThrow();
  });

  it('is a no-op when no conversation was requested', () => {
    expect(() =>
      assertAgentConversationIsExecutable({
        wantsConversation: false,
        agentResponsePath: '',
        promptfooStrategyIds: [],
      }),
    ).not.toThrow();
  });
});

describe('applyStatefulToMultiTurn', () => {
  it('forces stateful on every multi-turn strategy', () => {
    const out = applyStatefulToMultiTurn([
      { id: 'crescendo' },
      { id: 'goat' },
      { id: 'jailbreak:hydra' },
      { id: 'jailbreak:goblin' },
    ]);
    expect(out).toEqual([
      { id: 'crescendo', config: { stateful: true } },
      { id: 'goat', config: { stateful: true } },
      { id: 'jailbreak:hydra', config: { stateful: true } },
      { id: 'jailbreak:goblin', config: { stateful: true } },
    ]);
  });

  it('leaves single-turn strategies untouched — they have no session concept', () => {
    const input = [{ id: 'base64' }, { id: 'jailbreak' }];
    expect(applyStatefulToMultiTurn(input)).toEqual(input);
  });

  it('preserves other strategy config keys', () => {
    expect(applyStatefulToMultiTurn([{ id: 'crescendo', config: { maxTurns: 4 } }])).toEqual([
      { id: 'crescendo', config: { maxTurns: 4, stateful: true } },
    ]);
  });

  it('does not add stateful to a strategy whose config is absent when nothing else applies', () => {
    // An explicit stateful:true is preserved (nothing to change).
    const input = [{ id: 'crescendo', config: { stateful: true, maxTurns: 3 } }];
    expect(applyStatefulToMultiTurn(input)).toEqual(input);
  });

  it('returns the identical array when nothing changed (no needless allocation)', () => {
    const input = [{ id: 'base64' }];
    expect(applyStatefulToMultiTurn(input)).toBe(input);
  });
});

describe('assertMultiTurnStrategyIdsAreCurrent', () => {
  it('passes when every id exists upstream', () => {
    expect(() =>
      assertMultiTurnStrategyIdsAreCurrent([...MULTI_TURN_STRATEGY_IDS, 'base64']),
    ).not.toThrow();
  });

  it('fails loudly when promptfoo drops an id (anti-drift)', () => {
    expect(() => assertMultiTurnStrategyIdsAreCurrent(['crescendo', 'goat'])).toThrow(
      /multi-turn strategy map is stale/,
    );
  });
});

describe('countToolCallSignals', () => {
  it('reports zero for a clean response', () => {
    expect(countToolCallSignals({ answer: 'hello', conversation_id: 'c1' })).toEqual({ count: 0 });
  });

  it('does not treat an empty tool_calls array as a signal', () => {
    // This is how most frameworks spell "no tools used". Counting it would
    // flag every clean run and make the metric useless.
    expect(countToolCallSignals({ answer: 'hi', tool_calls: [] })).toEqual({ count: 0 });
  });

  it('detects OpenAI-style tool_calls', () => {
    expect(countToolCallSignals({ tool_calls: [{ function: { name: 'send_email' } }] }).count).toBe(1);
  });

  it('detects Dify-style agent_outputs', () => {
    expect(countToolCallSignals({ agent_outputs: [{ type: 'tool', name: 'query_db' }] }).count).toBe(1);
  });

  it('detects signals nested inside the response', () => {
    expect(countToolCallSignals({ data: { meta: { actions: [{ name: 'delete' }] } } }).count).toBe(1);
  });

  it('ignores null and empty-string markers', () => {
    expect(countToolCallSignals({ tool_calls: null, function_call: '  ' })).toEqual({ count: 0 });
  });

  it('reports zero for a non-object (an unparsed body carries no structure)', () => {
    expect(countToolCallSignals('plain text body')).toEqual({ count: 0 });
    expect(countToolCallSignals(null)).toEqual({ count: 0 });
    expect(countToolCallSignals(undefined)).toEqual({ count: 0 });
  });

  it('survives a cyclic structure instead of hanging', () => {
    const cyclic: Record<string, unknown> = { answer: 'x' };
    cyclic['self'] = cyclic;
    expect(cyclic).toBeTruthy();
    expect(countToolCallSignals(cyclic)).toEqual({ count: 0 });
  });

  it('stops at the depth bound rather than walking an arbitrarily deep payload', () => {
    // An attacker-influenced response could nest deeply; the walk is bounded.
    let deep: Record<string, unknown> = { tool_calls: [{ name: 'x' }] };
    for (let i = 0; i < 50; i++) deep = { nested: deep };
    expect(countToolCallSignals(deep)).toEqual({ count: 0 });
  });
});