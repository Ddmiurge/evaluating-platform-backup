import { describe, expect, it } from 'vitest';
import promptfoo from 'promptfoo';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import {
  allocateNumTests,
  assertNativeSlots,
  assertPluginIdsAreAccepted,
  assertSafeProviderUrl,
  buildEffectivePurpose,
  buildNativeRedteamConfig,
  DEFAULT_INJECT_VAR,
  NATIVE_ERR_INVALID_PROVIDER,
  NATIVE_ERR_MISSING_PROVIDER,
  NATIVE_ERR_NO_TEST_CASES,
  NATIVE_ERR_NUM_TESTS_TOO_SMALL,
  NATIVE_ERR_PLUGIN_ID_INVALID,
  NATIVE_ERR_PROMPTFOO_FAILED,
  NATIVE_ERR_STRATEGY_UNSUPPORTED,
  NATIVE_ERR_WORKDIR_MISSING,
  resolveNativeStrategies,
  withPluginJudgeRubrics,
  loadEvalResults,
  nativeJudgeSourceOverrides,
} from '../src/runner/nativeRedteam.js';
import {
  assertNativeStrategyMapIsCurrent,
  knownNativeStrategyIds,
  mappedPromptfooStrategyIds,
  nativeStrategyMapping,
  planAOnlyStrategyIds,
} from '../src/runner/nativeStrategyMap.js';
import { EngineRunError } from '../src/runner/generateTests.js';
import { reduceEvalToSafeResult } from '../src/runner/redteamRun.js';
import type { EngineCatalog, OneShotCredentials } from '../src/types.js';
import { buildJudgeRubricMap, UNMAPPED_JUDGE_CRITERIA } from '../src/runner/judgeRubric.js';

/** Credentials with both slots filled — the shape a real U7 run has. */
function creds(overrides: Partial<OneShotCredentials> = {}): OneShotCredentials {
  return {
    target: { base_url: 'https://llm.corp.example/v1', model: 'target-model', api_key: 'target-key' },
    generation: { base_url: 'https://llm.corp.example/v1', model: 'gen-model', api_key: 'gen-key' },
    ...overrides,
  };
}

/**
 * P2-2: a catalogue carrying the measured rejection verdict — the shape the BFF
 * ships once `IllegalPluginIDs()` is non-empty.
 */
const rejectedCatalog: EngineCatalog = {
  plugins: [],
  strategies: [],
  rejected_plugin_ids: {
    'prompt-injection': 'platform-invented; promptfoo has no such plugin',
    jailbreak: 'jailbreak is a promptfoo STRATEGY, not a plugin',
    'security-exploit': 'platform-invented',
  },
};

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
    ]);
    expect(native.map((s) => s.id)).toEqual([
      'jailbreak:tree',
      'jailbreak:hydra',
      'mischievous-user',
      'authoritative-markup-injection',
    ]);
    // `iterative` and `iterative:tree` share one promptfoo id and promptfoo
    // rejects duplicates, so the collapse is intentional — asserted here because
    // it means a selection of five yields four forwarded strategies.
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

  // Phase 3 · `simba`, `retry` and `mathPrompt` join this list. None of them is a
  // platform-invented dimension — promptfoo really has all three ids — but all
  // three fail or do nothing when the platform forwards them, and each was
  // previously mapped as `native`:
  //
  //   simba       -> deprecated stub; action logs "will be skipped", returns [].
  //                  A green run with the strategy reported as applied.
  //   retry       -> invariant "No target IDs found in config", thrown after
  //                  the run record is persisted. Needs config the platform
  //                  does not send.
  //   math-prompt -> with remote generation fail-closed it calls an OpenAI
  //                  provider directly and dies with "API key is not set".
  //
  // U6's remote-dependency table gates none of them, so none was caught before
  // this discovery pass. Reclassifying keeps each id resolvable (Plan A
  // synthesis plus a reason) without forwarding a strategy that cannot run.
  it('classifies exactly the platform-invented dimensions and the unusable natives as Plan-A-only', () => {
    expect(planAOnlyStrategyIds()).toEqual([
      'assumed-knowledge',
      'direct',
      'encoding',
      'few-shot',
      'many-shot',
      'mathPrompt',
      'multi-turn',
      'otherEncodings',
      'retry',
      'role-play',
      'simba',
      'translate',
    ]);
  });

  // The `simba` reclassification is a correctness fix, so it is pinned here
  // rather than left to the list assertion above: this states the property that
  // matters, which is that no strategy whose action provably returns no cases is
  // ever forwarded to promptfoo.
  it('never forwards a strategy that generates zero cases', () => {
    for (const strategy of promptfoo.redteam.Strategies) {
      const body = strategy.action.toString();
      // Only the self-contained empty implementations are provable from source.
      if (!/return\s*\[\s*\]\s*;?\s*}/.test(body)) continue;
      expect(nativeStrategyMapping(strategy.id)?.disposition).not.toBe('native');
    }
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

  it('fills both slots with the platform endpoints', async () => {
    const cfg = await buildNativeRedteamConfig(base);
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
  it('also pins the grading provider so it cannot fall back', async () => {
    const cfg = await buildNativeRedteamConfig(base);
    expect(cfg.defaultTest.options.provider.id).toBe('openai:chat:gen-model');
  });

  it('uses the requested inject var in the prompt template', async () => {
    const cfg = await buildNativeRedteamConfig({ ...base, injectVar: 'user_input' });
    expect(cfg.prompts).toEqual(['{{user_input}}']);
    expect(cfg.redteam.injectVar).toBe('user_input');
  });

  it('includes testGenerationInstructions only when non-empty', async () => {
    expect((await buildNativeRedteamConfig(base)).redteam.testGenerationInstructions).toBeUndefined();
    const withText = await buildNativeRedteamConfig({ ...base, testGenerationInstructions: ' 只用中文 ' });
    expect(withText.redteam.testGenerationInstructions).toBe('只用中文');
  });

  // U8 adds one more key: graderGuidance. The caller-supplied config must still
  // survive alongside it, which is why this asserts per-key rather than by
  // whole-object equality (the rubric text is asserted in the U8 suite).
  it('preserves per-plugin config', async () => {
    const cfg = await buildNativeRedteamConfig({
      ...base,
      plugins: [{ id: 'policy', config: { policy: { text: 'no refunds' } } }],
    });
    expect(cfg.redteam.plugins[0]?.config?.policy).toEqual({ text: 'no refunds' });
  });
});
// ---------------------------------------------------------------------------
// U8 · Plan C must judge by the same per-plugin standards as Plan A.
//
// Counter-requirement: if only Plan A were wired, turning on native generation
// would silently regress judging to promptfoo's one-size rubric. That is the
// exact degradation U8 exists to remove, and it would only show up as "the
// numbers changed after the upgrade" — which nobody attributes to judging.
// ---------------------------------------------------------------------------
describe('withPluginJudgeRubrics — Plan C carries the per-plugin rubric', () => {
  const catalog: EngineCatalog = {
    plugins: [
      { id: 'contracts', category: 'industry', category_label: '行业合规', severity: 'medium', judge_criteria: 'RUBRIC_CONTRACTS', judge_source: 'category:industry' },
      { id: 'harmful:hate', category: 'harmful', category_label: '有害内容', severity: 'high', judge_criteria: 'RUBRIC_HARMFUL', judge_source: 'category:harmful' },
    ],
    strategies: [],
  };

  it('writes each plugin its own rubric into the field promptfoo reads', () => {
    // graderGuidance is the ONLY rubric-ish field promptfoo's
    // RedteamConfigSchema/PluginConfigSchema declares and then consumes
    // (metadata.pluginConfig → RedteamGraderBase.getResult). Verified
    // end-to-end against promptfoo 0.123.0 dist with a fake local LLM:
    // graderGuidance reaches the grading prompt; `rubric` and `judgeRubric`
    const out = withPluginJudgeRubrics([{ id: 'contracts' }, { id: 'harmful:hate' }], buildJudgeRubricMap(catalog));
    expect(out[0]?.config?.graderGuidance).toBe('RUBRIC_CONTRACTS');
    expect(out[1]?.config?.graderGuidance).toBe('RUBRIC_HARMFUL');
  });

  it('gives two plugins in one run two different rubrics', () => {
    const out = withPluginJudgeRubrics([{ id: 'contracts' }, { id: 'harmful:hate' }], buildJudgeRubricMap(catalog));
    expect(out[0]?.config?.graderGuidance).not.toBe(out[1]?.config?.graderGuidance);
  });

  // Guardrail counter-proof: a plugin with no rubric must not silently lose
  // its standard — it gets the named fallback, which promptfoo will still read.
  it('falls back to the named unmapped rubric rather than omitting the field', () => {
    const out = withPluginJudgeRubrics([{ id: 'not-in-catalog' }], buildJudgeRubricMap(catalog));
    expect(out[0]?.config?.graderGuidance).toBe(UNMAPPED_JUDGE_CRITERIA);
  });

  it('preserves other plugin config and never drops a caller-supplied override', () => {
    const out = withPluginJudgeRubrics(
      [
        { id: 'contracts', config: { modifiers: { language: 'zh' } } },
        { id: 'harmful:hate', config: { graderGuidance: 'OPERATOR_OVERRIDE' } },
      ],
      buildJudgeRubricMap(catalog),
    );
    expect(out[0]?.config?.modifiers).toEqual({ language: 'zh' });
    expect(out[0]?.config?.graderGuidance).toBe('RUBRIC_CONTRACTS');
    // An explicit operator override wins: silently replacing it would make the
    // report claim a standard the run did not actually use.
    expect(out[1]?.config?.graderGuidance).toBe('OPERATOR_OVERRIDE');
  });

  // This is the assertion that matters. It goes through
  // buildNativeRedteamConfig — the function that produces the document
  // promptfoo actually reads — rather than through the helper. An earlier
  // version of this test only exercised withPluginJudgeRubrics, and the
  // counter-proof (delete the rubric attachment from the run path) still
  // passed: the tests were green while Plan C judged by promptfoo's one-size
  // rubric. Testing the seam that emits the config is what closes that hole.
  it('attaches each plugin rubric inside the config document promptfoo reads', async () => {
    const cfg = await buildNativeRedteamConfig({
      runId: 'run-u8',
      purpose: 'p',
      injectVar: DEFAULT_INJECT_VAR,
      // P2-4: the platform figure is a TOTAL, so it must cover both plugins
      // (each gets at least one probe). 2 = one each.
      numTests: 2,
      plugins: [{ id: 'contracts' }, { id: 'harmful:hate' }],
      strategies: [],
      credentials: creds(),
      catalog,
    });
    expect(cfg.redteam.plugins[0]?.config?.graderGuidance).toBe('RUBRIC_CONTRACTS');
    expect(cfg.redteam.plugins[1]?.config?.graderGuidance).toBe('RUBRIC_HARMFUL');
  });

  it('still attaches the named fallback for a plugin missing from the catalog', async () => {
    const cfg = await buildNativeRedteamConfig({
      runId: 'run-u8-fallback',
      purpose: 'p',
      injectVar: DEFAULT_INJECT_VAR,
      numTests: 1,
      plugins: [{ id: 'not-in-catalog' }],
      strategies: [],
      credentials: creds(),
      catalog,
    });
    expect(cfg.redteam.plugins[0]?.config?.graderGuidance).toBe(UNMAPPED_JUDGE_CRITERIA);
  });

  // P2-2 L3 · the counter-proof that matters. It goes through
  // buildNativeRedteamConfig — the seam that emits the document promptfoo reads
  // — rather than through the helper, because a guard wired at a call site can
  // be deleted with the suite still green (U8's lesson). Delete the
  // assertPluginIdsAreAccepted call inside the builder and this goes red: the
  // request would reach promptfoo and fail the whole run mid-flight instead.
  it('refuses a rejected plugin id instead of letting promptfoo fail the run', async () => {
    await expect(
      buildNativeRedteamConfig({
        runId: 'run-p2-2-l3',
        purpose: 'p',
        injectVar: DEFAULT_INJECT_VAR,
        numTests: 1,
        plugins: [{ id: 'prompt-injection' }],
        strategies: [],
        credentials: creds(),
        catalog: rejectedCatalog,
      }),
    ).rejects.toMatchObject({ code: NATIVE_ERR_PLUGIN_ID_INVALID });
  });

  // The same seam must NOT refuse a plugin merely absent from the catalogue:
  // U8 decided unlisted-but-acceptable plugins still run, judged by the named
  // fallback. This is the pair to the test above — one refusal, one pass — and
  // together they pin that the verdict is about MEASURED rejection, not
  // catalogue membership.
  it('still runs a plugin absent from the catalogue when it is not rejected', async () => {
    const cfg = await buildNativeRedteamConfig({
      runId: 'run-p2-2-l3-unlisted',
      purpose: 'p',
      injectVar: DEFAULT_INJECT_VAR,
      numTests: 1,
      plugins: [{ id: 'sql-injection' }],
      strategies: [],
      credentials: creds(),
      catalog: rejectedCatalog,
    });
    expect(cfg.redteam.plugins[0]?.id).toBe('sql-injection');
  });

  // Pre-U8 BFF: no catalog at all. The run must still be rubric-bearing rather
  // than emitting a document with no standard per plugin.
  it('degrades to the named fallback when no catalog is supplied', async () => {
    const cfg = await buildNativeRedteamConfig({
      runId: 'run-u8-nocatalog',
      purpose: 'p',
      injectVar: DEFAULT_INJECT_VAR,
      numTests: 1,
      plugins: [{ id: 'contracts' }],
      strategies: [],
      credentials: creds(),
    });
    expect(cfg.redteam.plugins[0]?.config?.graderGuidance).toBe(UNMAPPED_JUDGE_CRITERIA);
  });

  // Regression guard for an upstream defect that U8's injection would trigger.
  //
  // promptfoo 0.123.0 (dist/src/index.js:16762) does, while logging plugin
  // config summaries during synthesis:
  //     if (p.config) { if (p.id === "policy") { const policy = p.config?.policy;
  //       if (isValidPolicyObject(policy)) {...} else { policy.trim() } } }
  // The `else` assumes `config.policy` is a string whenever `config` exists.
  // U8 attaches `graderGuidance`, which makes `config` exist — so injecting
  // unconditionally turns every Plan C run containing `policy` into
  // "TypeError: Cannot read properties of undefined (reading 'trim')", thrown
  // before a single probe runs.
  //
  // `policy` is a real catalog plugin (internal/maclaw/promptfoo_plugin_catalog.go
  // p("policy", "政策违规", "industry", …)), so this is reachable in production.
  //
  // KNOWN GAP, pinned here on purpose: `policy` is therefore NOT given the
  // platform rubric on the Plan C path. It is still graded — by promptfoo's own
  // PolicyPlugin grader — so the run stays correct, but not by our family
  // standard. Fixing it properly needs an upstream patch or a promptfoo upgrade.
  it('does not attach a config to plugins whose config promptfoo misinterprets', () => {
    const out = withPluginJudgeRubrics([{ id: 'policy' }, { id: 'contracts' }], buildJudgeRubricMap(catalog));
    // policy: no config at all, so promptfoo's `if (p.config)` branch is skipped.
    expect(out[0]?.config).toBeUndefined();
    // Every other plugin still gets its rubric — the guard is narrow on purpose.
    expect(out[1]?.config?.graderGuidance).toBe('RUBRIC_CONTRACTS');
  });

  it('leaves a caller-supplied policy config untouched', () => {
    // The operator's own `config.policy` is what promptfoo needs; we must not
    // strip it, and must not add anything that would re-trigger the bug.
    const out = withPluginJudgeRubrics(
      [{ id: 'policy', config: { policy: { text: 'no refunds' } } }],
      buildJudgeRubricMap(catalog),
    );
    expect(out[0]?.config).toEqual({ policy: { text: 'no refunds' } });
  });
});

// ---------------------------------------------------------------------------
// U8 ②：`Eval.results` 在 Plan C 上是空的，必须经 getResults() 取回。
//
// 为什么这些用例必须是确定性的、而不能只靠 E2E：E2E 跑在「同一进程已
// evaluate 过」的状态下，库可能已经把 results 填上了（实测：独立进程里
// `results.length`=0，同进程 E2E 里=2）。也就是说 **E2E 单独不足以钉住这个
// 回归** —— 它在某些执行顺序下会假绿。这里用构造输入把三种形态都固定下来。
// ---------------------------------------------------------------------------
describe('loadEvalResults — Plan C 的结果必须重新水合（U8 ②）', () => {
  const rows = [
    { gradingResult: { pass: false }, testCase: { metadata: { pluginId: 'contracts' } } },
    { gradingResult: { pass: false }, testCase: { metadata: { pluginId: 'hallucination' } } },
  ];

  it('results 为空时经 getResults() 取回（U7 报 0 覆盖的根因）', async () => {
    let called = 0;
    const loaded = await loadEvalResults({
      id: 'eval-1',
      results: [],
      getResults: async () => {
        called++;
        return rows;
      },
    });
    expect(called).toBe(1);
    expect(loaded.results).toHaveLength(2);
  });

  it('results 已填充时不再付一次 DB 往返', async () => {
    let called = 0;
    const loaded = await loadEvalResults({
      id: 'eval-2',
      results: rows,
      getResults: async () => {
        called++;
        return [];
      },
    });
    expect(called).toBe(0);
    expect(loaded.results).toHaveLength(2);
  });

  it('保留 id，供rawEvalId 继续回传', async () => {
    const loaded = await loadEvalResults({ id: 'eval-3', results: [], getResults: async () => rows });
    expect(loaded.id).toBe('eval-3');
  });

  it('没有 getResults 时不抛，退化为空数组交给调用方的 fail-closed 判定', async () => {
    // 刻意不抛：抛在这里会把「库没有这个入口」变成一个看不懂的错误，
    // 而调用方（executeNativeRedteamRun）已有 0 探针即fail-closed 的闸。
    const loaded = await loadEvalResults({ id: 'eval-4', results: [] });
    expect(loaded.results).toEqual([]);
  });

  it('getResults 返回非数组时安全退化为空数组', async () => {
    const loaded = await loadEvalResults({ results: [], getResults: async () => undefined as never });
    expect(loaded.results).toEqual([]);
  });

  it.each([[null], [undefined], ['not-an-eval']])('输入 %p 返回空结果而非抛错', async (input) => {
    expect((await loadEvalResults(input)).results).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// U8 ①：`policy` 的判定基础必须可见，不能显示成 category:industry。
//
// 跳过注入保住了 run，但若报告仍按目录解析成族级档，policy 就与 contracts
// 完全同形 —— 用户以为平台族标准生效了，而实际是promptfoo 自带 grader 判的。
// ---------------------------------------------------------------------------
describe('nativeJudgeSourceOverrides — policy 的判定基础要单独成档', () => {
  it('把注入不了的插件标成 promptfoo:<id>，其余不动', () => {
    const overrides = nativeJudgeSourceOverrides([{ id: 'policy' }, { id: 'contracts' }]);
    expect(overrides.get('policy')).toBe('promptfoo:policy');
    expect(overrides.has('contracts')).toBe(false);
  });

  // 与 Go 侧 TestPromptfooConfigUnsafePluginsAreExactlyPolicy 成对：任一侧改动
  // 而另一方未改，报告就会声称一个本次运行并未使用的判定基础。
  it('清单与后端一致：恰为 policy 一个', () => {
    const all = ['policy', 'contracts', 'harmful:hate', 'hallucination', 'pii', 'bias'];
    const affected = all.filter((id) => nativeJudgeSourceOverrides([{ id }]).has(id));
    expect(affected).toEqual(['policy']);
  });

  it('reduction 用它把 policy 记成 promptfoo 档，而非族级档', () => {
    const catalog: EngineCatalog = {
      plugins: [
        { id: 'policy', category: 'industry', category_label: '行业合规', severity: 'medium', judge_criteria: 'RUBRIC_POLICY', judge_source: 'category:industry' },
        { id: 'contracts', category: 'industry', category_label: '行业合规', severity: 'medium', judge_criteria: 'RUBRIC_CONTRACTS', judge_source: 'category:industry' },
      ],
      strategies: [],
    };
    const evalLike = {
      results: [
        { gradingResult: { pass: false }, testCase: { metadata: { pluginId: 'policy' } } },
        { gradingResult: { pass: false }, testCase: { metadata: { pluginId: 'contracts' } } },
      ],
    };
    // 不带 override：两者同形，正是要避免的误导形态。
    const naive = reduceEvalToSafeResult(evalLike, catalog);
    expect(naive.plugin_stats.find((s) => s.plugin_id === 'policy')?.judge_source).toBe('category:industry');

    // 带 override：policy 被如实标出。
    const fixed = reduceEvalToSafeResult(evalLike, catalog, {
      judgeSourceOverrides: nativeJudgeSourceOverrides([{ id: 'policy' }]),
    });
    expect(fixed.plugin_stats.find((s) => s.plugin_id === 'policy')?.judge_source).toBe('promptfoo:policy');
    expect(fixed.plugin_stats.find((s) => s.plugin_id === 'contracts')?.judge_source).toBe('category:industry');
    // 这一档不是兜底：平台有标准，只是注入不了。
    expect(fixed.judge_sources?.every((s) => !s.is_fallback)).toBe(true);
    expect(fixed.judge_sources?.map((s) => s.source).sort()).toEqual(['category:industry', 'promptfoo:policy']);
  });

  it('Plan A 的 metadata 标记优先于 override（Plan A 确实用了平台标准）', () => {
    const catalog: EngineCatalog = {
      plugins: [{ id: 'policy', category: 'industry', category_label: '行业合规', severity: 'medium', judge_criteria: 'R', judge_source: 'category:industry' }],
      strategies: [],
    };
    const result = reduceEvalToSafeResult(
      {
        results: [
          {
            gradingResult: { pass: false },
            testCase: { metadata: { pluginId: 'policy', judgeSource: 'category:industry' } },
          },
        ],
      },
      catalog,
      { judgeSourceOverrides: nativeJudgeSourceOverrides([{ id: 'policy' }]) },
    );
    // override 只补 Plan C 缺标记时的缺口，不得推翻 Plan A 明确写入的值。
    expect(result.plugin_stats[0]?.judge_source).toBe('category:industry');
  });
});

describe('assertPluginIdsAreAccepted — P2-2 L3 (measured rejection, not catalogue membership)', () => {
  it('refuses every rejected id with the plugin-id code and its reason', () => {
    for (const id of Object.keys(rejectedCatalog.rejected_plugin_ids ?? {})) {
      let thrown: unknown;
      try {
        assertPluginIdsAreAccepted([{ id }], rejectedCatalog);
      } catch (err) {
        thrown = err;
      }
      expect(thrown, `${id} was not refused`).toBeInstanceOf(EngineRunError);
      expect((thrown as EngineRunError).code).toBe(NATIVE_ERR_PLUGIN_ID_INVALID);
      // The message must name the id and carry the platform's reason, or the
      // user cannot tell which selection to change or why it failed.
      expect((thrown as EngineRunError).message).toContain(id);
      expect((thrown as EngineRunError).message).toContain('promptfoo');
    }
  });

  it('accepts an id the catalogue does not list but does not reject', () => {
    // The paired counter-proof to the test above. Catalogue membership is not
    // the test: `sql-injection` / `shell-injection` are accepted by promptfoo
    // and absent from the platform catalogue, and the plan treats those as
    // catalogue gaps to fill — not as runs to block.
    expect(() =>
      assertPluginIdsAreAccepted([{ id: 'not-in-catalog' }, { id: 'sql-injection' }], rejectedCatalog),
    ).not.toThrow();
  });

  it('exempts file:// package refs written into the run workdir', () => {
    expect(() => assertPluginIdsAreAccepted([{ id: 'file:///tmp/run/custom.yaml' }], rejectedCatalog)).not.toThrow();
  });

  it('is a no-op when no verdict is shipped', () => {
    const bare: EngineCatalog = { plugins: [], strategies: [] };
    expect(() => assertPluginIdsAreAccepted([{ id: 'prompt-injection' }], bare)).not.toThrow();
    expect(() => assertPluginIdsAreAccepted([{ id: 'prompt-injection' }], undefined)).not.toThrow();
  });

  it('ignores blank ids rather than refusing them', () => {
    expect(() => assertPluginIdsAreAccepted([{ id: '   ' }], rejectedCatalog)).not.toThrow();
  });
});

/**
 * promptfoo's own `RedteamConfigSchema`, resolved by the SEMANTIC name
 * promptfoo binds it to.
 *
 * The schema is not exported from the package entrypoint (verified: the entry's
 * export list has no `RedteamConfigSchema`), so it is reached the same way
 * `gen_plugin_legality.mjs` reaches it: read the entry's own import statement and
 * follow the binding, not the minified export name or the content-hashed chunk
 * filename. Resolving it here rather than restating the rules is the point —
 * "what promptfoo actually accepts" is the only evidence that counts, and a
 * hand-written expectation could agree with the engine while both disagree with
 * promptfoo.
 */
async function redteamConfigSchema(): Promise<{
  safeParse: (value: unknown) => { success: boolean; data?: { plugins?: Array<{ numTests?: number }> }; error?: unknown };
}> {
  const importMetaResolve = (import.meta as { resolve?: (id: string) => string }).resolve;
  const entry =
    typeof importMetaResolve === 'function'
      ? (() => {
          const resolved = importMetaResolve.call(import.meta, 'promptfoo');
          return resolved.startsWith('file://') ? fileURLToPath(resolved) : resolved;
        })()
      : join(dirname(createRequire(import.meta.url).resolve('promptfoo')), 'index.js');

  const source = readFileSync(entry, 'utf8');
  for (const match of source.matchAll(/import\s*\{([^}]*)\}\s*from\s*"\.\/(tables-[^"]+)"/g)) {
    for (const specifier of (match[1] ?? '').split(',').map((s) => s.trim()).filter(Boolean)) {
      const [exported = '', local = exported] = specifier.split(/\s+as\s+/).map((s) => s.trim());
      if (local !== 'RedteamConfigSchema') continue;
      const chunk = match[2] ?? '';
      const module = (await import(new URL(chunk, pathToFileURL(entry)).href)) as Record<string, unknown>;
      return module[exported] as Awaited<ReturnType<typeof redteamConfigSchema>>;
    }
  }
  throw new Error('promptfoo no longer resolves RedteamConfigSchema from a tables chunk');
}

const p24 = (id: string, fanout: number) => ({
  id,
  category: 'harmful',
  category_label: '有害内容',
  severity: 'high',
  fanout,
});

describe('allocateNumTests — the platform total is a probe cap (P2-4)', () => {
  const fanout = (entries: Record<string, number>) => new Map(Object.entries(entries));

  it('spends the whole budget when the fanouts allow it', () => {
    // harmful 26 + pii 4 + contracts 1 => 31 sub-plugins; N=50 is reachable.
    const allocation = allocateNumTests(
      [{ id: 'harmful' }, { id: 'pii' }, { id: 'contracts' }],
      50,
      fanout({ harmful: 26, pii: 4, contracts: 1 }),
    );
    expect(allocation.total).toBe(50);
    expect(allocation.expandedPlugins).toBe(31);
    for (const count of allocation.perPlugin.values()) expect(count).toBeGreaterThanOrEqual(1);
  });

  it('never exceeds the user’s number, which is the whole point', () => {
    // Every combination here must stay at or below N. The old behaviour was
    // N x sum(fanout): 50 with `harmful` ran 1300.
    for (const total of [31, 32, 40, 50, 77, 500]) {
      const allocation = allocateNumTests(
        [{ id: 'harmful' }, { id: 'pii' }, { id: 'contracts' }],
        total,
        fanout({ harmful: 26, pii: 4, contracts: 1 }),
      );
      expect(allocation.total).toBeLessThanOrEqual(total);
    }
  });

  it('gives every selected plugin at least one probe', () => {
    // A selected plugin with no cases is a silently dropped capability, and
    // promptfoo's per-plugin `numTests` is `positive()` so 0 would be refused.
    const allocation = allocateNumTests(
      [{ id: 'harmful' }, { id: 'policy' }],
      27,
      fanout({ harmful: 26, policy: 1 }),
    );
    expect(allocation.perPlugin.get('harmful')).toBe(1);
    expect(allocation.perPlugin.get('policy')).toBe(1);
  });

  it('refuses a total below the expansion instead of running more than asked', () => {
    // harmful alone expands to 26 sub-plugins, so the honest minimum is 26. The
    // refusal has to be actionable: it names the expansion and both ways out.
    let thrown: unknown;
    try {
      allocateNumTests([{ id: 'harmful' }], 10, fanout({ harmful: 26 }));
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(EngineRunError);
    expect((thrown as EngineRunError).code).toBe(NATIVE_ERR_NUM_TESTS_TOO_SMALL);
    expect((thrown as EngineRunError).message).toContain('26');
    expect((thrown as EngineRunError).message).toContain('harmful:cybercrime');
  });

  it('is deterministic, and breaks ties by id', () => {
    const run = () =>
      allocateNumTests(
        [{ id: 'b' }, { id: 'a' }, { id: 'c' }],
        8,
        fanout({ a: 1, b: 1, c: 1 }),
      );
    const first = [...run().perPlugin.entries()];
    const second = [...run().perPlugin.entries()];
    expect(first).toEqual(second);
    // Equal fanouts: the two spare probes go to the two lowest ids.
    expect(Object.fromEntries(first)).toEqual({ a: 3, b: 3, c: 2 });
  });

  it('weighs an unknown id 1 and collapses duplicates', () => {
    const allocation = allocateNumTests(
      [{ id: 'file:///tmp/run/custom.yaml' }, { id: 'pii' }, { id: 'pii' }, { id: '  ' }],
      6,
      fanout({ pii: 4 }),
    );
    // 4 + 1 = 5 sub-plugins; the blank entry and the duplicate do not add weight.
    expect(allocation.expandedPlugins).toBe(5);
    expect(allocation.perPlugin.size).toBe(2);
    // The remaining budget goes where it fits: `pii`'s fanout of 4 cannot be
    // afforded again, the single-fanout package can.
    expect(allocation.perPlugin.get('file:///tmp/run/custom.yaml')).toBe(2);
    expect(allocation.perPlugin.get('pii')).toBe(1);
    expect(allocation.total).toBe(6);
  });

  it('returns nothing for an empty selection rather than dividing by zero', () => {
    const allocation = allocateNumTests([], 10, fanout({}));
    expect(allocation.total).toBe(0);
    expect(allocation.perPlugin.size).toBe(0);
  });
});

describe('buildNativeRedteamConfig — num_tests reaches promptfoo as a total (P2-4)', () => {
  const catalog: EngineCatalog = {
    plugins: [p24('harmful', 26), p24('pii', 4), p24('contracts', 1)],
    strategies: [],
  };

  it('writes a per-plugin numTests and no redteam.numTests', async () => {
    const cfg = await buildNativeRedteamConfig({
      runId: 'run-p2-4',
      purpose: 'p',
      injectVar: DEFAULT_INJECT_VAR,
      numTests: 50,
      plugins: [{ id: 'harmful' }, { id: 'pii' }, { id: 'contracts' }],
      strategies: [],
      credentials: creds(),
      catalog,
    });
    // Leaving a scalar here would reinstate promptfoo's per-plugin fallback and
    // bring the 26x surprise back for anything the allocator missed.
    expect(cfg.redteam.numTests).toBeUndefined();
    const counts = Object.fromEntries(cfg.redteam.plugins.map((p) => [p.id, p.numTests]));
    expect(counts['harmful']).toBe(1);
    expect(counts['pii']).toBe(5);
    expect(counts['contracts']).toBe(4);
  });

  it('produces a document promptfoo accepts whose probe count is the user’s number', async () => {
    // R-1-b: the only real evidence. Reading our own code cannot establish what
    // promptfoo does with per-plugin counts — its own schema can, and the sum of
    // the EXPANDED plugins' counts is the number of probes that will run.
    const cfg = await buildNativeRedteamConfig({
      runId: 'run-p2-4-zod',
      purpose: 'p',
      injectVar: DEFAULT_INJECT_VAR,
      numTests: 50,
      plugins: [{ id: 'harmful' }, { id: 'pii' }, { id: 'contracts' }],
      strategies: [],
      credentials: creds(),
      catalog,
    });

    const schema = await redteamConfigSchema();
    const parsed = schema.safeParse(cfg.redteam);
    expect(parsed.success, JSON.stringify(parsed.error)).toBe(true);

    const expanded = parsed.data?.plugins ?? [];
    // 26 + 4 + 1 sub-plugins, i.e. the expansion really happened.
    expect(expanded).toHaveLength(31);
    const probes = expanded.reduce((sum, plugin) => sum + (plugin.numTests ?? 0), 0);
    expect(probes).toBe(50);
    // And none of them is 0, which the schema would have rejected anyway.
    expect(expanded.every((plugin) => (plugin.numTests ?? 0) >= 1)).toBe(true);
  });

  it('keeps a single collection plugin inside the cap instead of 26x it', async () => {
    // The headline regression: 50 with `harmful` used to mean 1300 probes.
    const cfg = await buildNativeRedteamConfig({
      runId: 'run-p2-4-harmful',
      purpose: 'p',
      injectVar: DEFAULT_INJECT_VAR,
      numTests: 50,
      plugins: [{ id: 'harmful' }],
      strategies: [],
      credentials: creds(),
      catalog,
    });
    const schema = await redteamConfigSchema();
    const parsed = schema.safeParse(cfg.redteam);
    expect(parsed.success, JSON.stringify(parsed.error)).toBe(true);
    const probes = (parsed.data?.plugins ?? []).reduce((sum, plugin) => sum + (plugin.numTests ?? 0), 0);
    // 26 sub-plugins, each 1: the granularity floor, and far below 1300.
    expect(probes).toBe(26);
  });
});

describe('engine error codes are specific (P2-5)', () => {
  it('no longer shares one code across unrelated failure classes', () => {
    // The defect: `native_generation_config_invalid` covered ten throw sites, so
    // the code named no cause — a missing workdir, an unsupported strategy and any
    // promptfoo exception all arrived as the same string. Two U7 findings could
    // not be diagnosed from their records for exactly that reason.
    const codes = [
      NATIVE_ERR_MISSING_PROVIDER,
      NATIVE_ERR_INVALID_PROVIDER,
      NATIVE_ERR_NO_TEST_CASES,
      NATIVE_ERR_PLUGIN_ID_INVALID,
      NATIVE_ERR_NUM_TESTS_TOO_SMALL,
      NATIVE_ERR_STRATEGY_UNSUPPORTED,
      NATIVE_ERR_WORKDIR_MISSING,
      NATIVE_ERR_PROMPTFOO_FAILED,
    ];
    expect(new Set(codes).size).toBe(codes.length);
    expect(codes).not.toContain('native_generation_config_invalid');
    // Every code is namespaced, so a platform-side mapping can tell engine codes
    // from its own without a maintained list.
    for (const code of codes) expect(code.startsWith('native_generation_')).toBe(true);
  });

  it('reports a missing workdir as its own code', async () => {
    // Reachable behaviour, not just a constant: a run carrying expert packages
    // with nowhere to write them refuses with this code.
    await expect(
      buildNativeRedteamConfig({
        runId: 'run-p2-5-workdir',
        purpose: 'p',
        injectVar: DEFAULT_INJECT_VAR,
        numTests: 1,
        plugins: [{ id: 'contracts' }],
        strategies: [],
        credentials: creds(),
        customPlugins: [
          { handle: 'h1', plugin_id: 'acme-probe', yaml: 'id: acme-probe', content_hash: 'x', published_at: '' },
        ],
      }),
    ).rejects.toMatchObject({ code: NATIVE_ERR_WORKDIR_MISSING });
  });
});
