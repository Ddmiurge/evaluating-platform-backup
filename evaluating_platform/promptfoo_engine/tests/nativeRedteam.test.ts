import { describe, expect, it } from 'vitest';
import {
  assertNativeSlots,
  assertSafeProviderUrl,
  buildEffectivePurpose,
  buildNativeRedteamConfig,
  DEFAULT_INJECT_VAR,
  NATIVE_ERR_INVALID_PROVIDER,
  NATIVE_ERR_MISSING_PROVIDER,
  resolveNativeStrategies,
} from '../src/runner/nativeRedteam.js';
import {
  assertNativeStrategyMapIsCurrent,
  knownNativeStrategyIds,
  mappedPromptfooStrategyIds,
  nativeStrategyMapping,
  planAOnlyStrategyIds,
} from '../src/runner/nativeStrategyMap.js';
import { EngineRunError } from '../src/runner/generateTests.js';
import type { OneShotCredentials } from '../src/types.js';

/** Credentials with both slots filled — the shape a real U7 run has. */
function creds(overrides: Partial<OneShotCredentials> = {}): OneShotCredentials {
  return {
    target: { base_url: 'https://llm.corp.example/v1', model: 'target-model', api_key: 'target-key' },
    generation: { base_url: 'https://llm.corp.example/v1', model: 'gen-model', api_key: 'gen-key' },
    ...overrides,
  };
}

describe('assertNativeSlots — the two slots are independent (U7 DoD 反证)', () => {
  // This is the DoD's explicit counter-proof: filling `target` without
  // `provider` must FAIL LOUDLY. promptfoo itself does not: its
  // loadRedteamProvider() falls back to `openai:gpt-5.5-...` (api.openai.com),
  // and when that call fails the run still "succeeds" reporting zero cases.
  // Measured on promptfoo 0.123.0: "Using default redteam provider:
  // gpt-5.5-2026-04-23" → "No test cases generated. Skipping scan." → returns
  // undefined without throwing. assertNativeSlots is what prevents that.
  it('rejects a request that fills target but omits provider', () => {
    const request = { credentials: creds({ generation: undefined }) };
    try {
      assertNativeSlots(request);
      expect.unreachable('a target-only request must not be accepted');
    } catch (err) {
      expect(err).toBeInstanceOf(EngineRunError);
      expect((err as EngineRunError).code).toBe(NATIVE_ERR_MISSING_PROVIDER);
      expect((err as EngineRunError).message).toMatch(/OpenAI/i);
    }
  });

  it('rejects a request that fills provider but omits the target endpoint', () => {
    const request = {
      credentials: creds({
        target: { base_url: '   ', model: 'm', api_key: 'k' },
      }),
    };
    expect(() => assertNativeSlots(request)).toThrow(/target endpoint/);
  });

  it('accepts a fully-populated request', () => {
    expect(() => assertNativeSlots({ credentials: creds() })).not.toThrow();
  });

  it('rejects an agent target that only supplies agent_endpoint', () => {
    const request = {
      credentials: creds({
        target: {
          base_url: '',
          model: '',
          api_key: 'k',
          agent_endpoint: 'https://agent.corp.example/chat',
        },
      }),
    };
    expect(() => assertNativeSlots(request)).not.toThrow();
  });

  // Counter-proof for the "incomplete provider" branch: a half-filled slot must
  // not degrade into promptfoo's default-provider fallback.
  it.each([
    ['base_url', { base_url: '', model: 'm', api_key: 'k' }],
    ['model', { base_url: 'https://llm.corp.example/v1', model: '  ', api_key: 'k' }],
    ['api_key', { base_url: 'https://llm.corp.example/v1', model: 'm', api_key: '' }],
  ])('rejects a provider missing %s', (field, generation) => {
    try {
      assertNativeSlots({ credentials: creds({ generation }) });
      expect.unreachable('incomplete provider must be rejected');
    } catch (err) {
      expect((err as EngineRunError).code).toBe(NATIVE_ERR_INVALID_PROVIDER);
      expect((err as EngineRunError).message).toContain(field);
    }
  });
});

describe('assertSafeProviderUrl — no vendor endpoints, ever', () => {
  // promptfoo treats these as "its own" channel. Accepting one would put attack
  // payloads and the purpose string on a third-party endpoint, which is exactly
  // what the four fail-closed switches exist to prevent.
  it.each([
    'https://api.openai.com/v1',
    'https://api.promptfoo.app',
    'https://api.anthropic.com/v1',
    'https://generativelanguage.googleapis.com/v1beta',
  ])('rejects %s', (url) => {
    expect(() => assertSafeProviderUrl(url, 'provider')).toThrow(/vendor default/);
  });

  it('rejects a subdomain of a vendor host', () => {
    expect(() => assertSafeProviderUrl('https://eu.api.openai.com/v1', 'provider')).toThrow(/vendor default/);
  });

  it('rejects a non-http scheme', () => {
    expect(() => assertSafeProviderUrl('file:///etc/passwd', 'target')).toThrow(/http/);
  });

  it('rejects a malformed URL', () => {
    expect(() => assertSafeProviderUrl('not a url', 'provider')).toThrow(/not a valid URL/);
  });

  it('accepts a platform-operated endpoint', () => {
    expect(() => assertSafeProviderUrl('https://llm.corp.example/v1', 'provider')).not.toThrow();
  });

  it('does not false-positive on a host that merely contains a vendor name', () => {
    expect(() => assertSafeProviderUrl('https://openai-proxy.corp.example/v1', 'provider')).not.toThrow();
  });
});

describe('resolveNativeStrategies — platform ids → promptfoo ids', () => {
  it('maps the renamed multi-turn strategies onto promptfoo ids', () => {
    const { native, planAOnly } = resolveNativeStrategies([
      { id: 'iterative' },
      { id: 'iterative:tree' },
      { id: 'hydra' },
      { id: 'mischievousUser' },
      { id: 'authoritativeMarkupInjection' },
      { id: 'mathPrompt' },
    ]);
    expect(native.map((s) => s.id)).toEqual([
      'jailbreak:tree',
      'jailbreak:hydra',
      'mischievous-user',
      'authoritative-markup-injection',
      'math-prompt',
    ]);
    expect(planAOnly).toEqual([]);
  });

  // promptfoo throws "Invalid strategy(s)" for ids it does not know, and two
  // platform ids collapsing to the same promptfoo id would be a duplicate.
  it('de-duplicates platform ids that collapse to one promptfoo id', () => {
    const { native } = resolveNativeStrategies([{ id: 'iterative' }, { id: 'iterative:tree' }]);
    expect(native).toEqual([{ id: 'jailbreak:tree' }]);
  });

  // §4.3: direct/role-play/encoding are platform-invented wrapper dimensions.
  // Forwarding them would make promptfoo throw; dropping them silently would
  // understate coverage. They must be reported as Plan-A-only.
  it('keeps platform-invented dimensions out of promptfoo strategies', () => {
    const { native, planAOnly } = resolveNativeStrategies([
      { id: 'direct' },
      { id: 'role-play' },
      { id: 'encoding' },
      { id: 'base64' },
    ]);
    expect(native).toEqual([{ id: 'base64' }]);
    expect(planAOnly).toEqual(['direct', 'role-play', 'encoding']);
  });

  it('treats an unknown platform id as Plan-A-only rather than forwarding it', () => {
    const { native, planAOnly } = resolveNativeStrategies([{ id: 'no-such-strategy' }]);
    expect(native).toEqual([]);
    expect(planAOnly).toEqual(['no-such-strategy']);
  });

  it('ignores blank ids', () => {
    expect(resolveNativeStrategies([{ id: '  ' }, { id: '' }])).toEqual({ native: [], planAOnly: [] });
  });
});

describe('nativeStrategyMap — single source of truth, no overlap with U6', () => {
  // The mapping table is validated against the *installed* promptfoo, so an
  // upgrade that renames a strategy fails loudly here instead of aborting a run
  // mid-generation.
  it('is current with the installed promptfoo', () => {
    expect(() => assertNativeStrategyMapIsCurrent()).not.toThrow();
  });

  // U6's table owns "does this need remote generation?". Listing a gated
  // strategy here as `native` would let a remote-gated capability reach
  // promptfoo and produce the silent 0-coverage run U6 exists to prevent. The
  // four gated ids must therefore be absent from the native map entirely.
  it.each(['bestOfN', 'likert', 'citation', 'singleTurnComposite'])(
    'does not map the remote-gated strategy %s onto promptfoo',
    (gated) => {
      expect(nativeStrategyMapping(gated)).toBeUndefined();
    },
  );

  it('classifies exactly the platform-invented dimensions as Plan-A-only', () => {
    expect(planAOnlyStrategyIds()).toEqual([
      'assumed-knowledge',
      'direct',
      'encoding',
      'few-shot',
      'many-shot',
      'multi-turn',
      'otherEncodings',
      'role-play',
      'translate',
    ]);
  });

  it('gives every Plan-A-only entry a human-readable reason', () => {
    for (const id of planAOnlyStrategyIds()) {
      expect(nativeStrategyMapping(id)?.reason).toBeTruthy();
    }
  });

  it('maps every non-Plan-A-only id to a promptfoo id', () => {
    for (const id of knownNativeStrategyIds()) {
      const mapping = nativeStrategyMapping(id);
      if (mapping?.disposition === 'native') expect(mapping.promptfooId).toBeTruthy();
    }
  });

  it('has no duplicate promptfoo targets other than the documented collapse', () => {
    const counts = new Map<string, number>();
    for (const id of mappedPromptfooStrategyIds()) counts.set(id, (counts.get(id) ?? 0) + 1);
    const dupes = [...counts.entries()].filter(([, n]) => n > 1);
    // Only iterative + iterative:tree legitimately share jailbreak:tree.
    expect(dupes.map(([id]) => id)).toEqual(['jailbreak:tree']);
  });
});

describe('buildEffectivePurpose — application definition reaches the generator', () => {
  it('returns the bare purpose when no application fields are supplied', () => {
    expect(buildEffectivePurpose({ purpose: 'test a bank chatbot' })).toBe('test a bank chatbot');
  });

  it('appends every supplied application field', () => {
    const out = buildEffectivePurpose({
      purpose: 'test a bank chatbot',
      applicationDefinition: {
        purpose: 'retail banking assistant',
        features: 'balance inquiry, transfers',
        has_access_to: 'customer balances',
        does_not_have_access_to: 'the ledgers database',
        user_types: 'retail customers, branch staff',
        security_requirements: 'PCI scope, never disclose full account numbers',
      },
    });
    expect(out).toContain('test a bank chatbot');
    expect(out).toContain('应用目的：retail banking assistant');
    expect(out).toContain('功能范围：balance inquiry, transfers');
    expect(out).toContain('可访问的数据/系统：customer balances');
    expect(out).toContain('不可访问的数据/系统：the ledgers database');
    expect(out).toContain('用户类型：retail customers, branch staff');
    expect(out).toContain('安全要求：PCI scope, never disclose full account numbers');
  });

  // Partial input must not produce dangling labels.
  it('omits fields that are blank', () => {
    const out = buildEffectivePurpose({
      purpose: 'p',
      applicationDefinition: { features: '   ', user_types: 'admins' },
    });
    expect(out).toContain('用户类型：admins');
    expect(out).not.toContain('功能范围');
  });
});

describe('buildNativeRedteamConfig — the document handed to promptfoo', () => {
  const base = {
    runId: 'run-1',
    purpose: 'test a bank chatbot',
    injectVar: DEFAULT_INJECT_VAR,
    numTests: 3,
    plugins: [{ id: 'contracts' }],
    strategies: [{ id: 'base64' }],
    credentials: creds(),
  };

  it('fills both slots with the platform endpoints', () => {
    const cfg = buildNativeRedteamConfig(base);
    // slot 1: the target under test
    expect(cfg.targets).toHaveLength(1);
    expect(cfg.targets[0]?.config.url).toBe('https://llm.corp.example/v1/chat/completions');
    // slot 2: the generation model
    const gen = cfg.redteam.provider.config as Record<string, string>;
    expect(gen.apiBaseUrl).toBe('https://llm.corp.example/v1');
    expect(gen.apiKey).toBe('gen-key');
    expect(cfg.redteam.provider.id).toBe('openai:chat:gen-model');
  });

  // Without this, grading would fall back to the same vendor default that
  // generation does — the target-side twin of the provider-missing hole.
  it('also pins the grading provider so it cannot fall back', () => {
    const cfg = buildNativeRedteamConfig(base);
    expect(cfg.defaultTest.options.provider.id).toBe('openai:chat:gen-model');
  });

  it('uses the requested inject var in the prompt template', () => {
    const cfg = buildNativeRedteamConfig({ ...base, injectVar: 'user_input' });
    expect(cfg.prompts).toEqual(['{{user_input}}']);
    expect(cfg.redteam.injectVar).toBe('user_input');
  });

  it('includes testGenerationInstructions only when non-empty', () => {
    expect(buildNativeRedteamConfig(base).redteam.testGenerationInstructions).toBeUndefined();
    const withText = buildNativeRedteamConfig({ ...base, testGenerationInstructions: ' 只用中文 ' });
    expect(withText.redteam.testGenerationInstructions).toBe('只用中文');
  });

  it('preserves per-plugin config', () => {
    const cfg = buildNativeRedteamConfig({
      ...base,
      plugins: [{ id: 'policy', config: { policy: { text: 'no refunds' } } }],
    });
    expect(cfg.redteam.plugins[0]?.config).toEqual({ policy: { text: 'no refunds' } });
  });
});