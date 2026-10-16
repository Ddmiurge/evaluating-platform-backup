/**
 * caseResults.test.ts — the per-probe text the report is built from.
 *
 * # Why these are unit tests and not only the smoke assertion
 *
 * The smoke test proves the text survives one real promptfoo run end to end.
 * It cannot prove *which* field the text came from, and that is the part most
 * likely to be wrong: the attack prompt is not one field. promptfoo's own report
 * walks `vars[injectVar]` → `vars.query` → `prompt.raw`, and a wrong choice here
 * does not look wrong — it produces a plausible prompt that simply is not the
 * one the target received. Every case below pins which field wins.
 */
import { describe, expect, it } from 'vitest';
import { reduceEvalToSafeResult } from '../src/runner/redteamRun.js';

/** Build an Eval with one result, from the fields the reducer may read. */
function evalWith(result: Record<string, unknown>) {
  return { id: 'eval-test', results: [result] };
}

function reduce(result: Record<string, unknown>, injectVar?: string) {
  const out = reduceEvalToSafeResult(evalWith(result) as never, undefined, { injectVar });
  return out.case_results?.[0];
}

describe('case_results — the attack prompt', () => {
  it('prefers the injection variable over everything else', () => {
    const c = reduce({
      vars: { prompt: 'PAYLOAD', query: 'from-query' },
      testCase: { vars: { prompt: 'TESTCASE-PAYLOAD' } },
      prompt: { raw: 'from-prompt-raw' },
      response: { output: 'ok' },
    });
    // Result-level vars win over testCase.vars, matching the order the reducer
    // reads them in (`result.vars ?? result.testCase.vars`).
    expect(c?.prompt).toBe('PAYLOAD');
  });

  it('uses a custom inject var when the run configured one', () => {
    const c = reduce(
      {
        vars: { user_input: 'PAYLOAD', query: 'from-query' },
        prompt: { raw: 'from-prompt-raw' },
        response: { output: 'ok' },
      },
      'user_input',
    );
    expect(c?.prompt).toBe('PAYLOAD');
  });

  it('falls back to vars.query', () => {
    const c = reduce({
      vars: { query: 'from-query' },
      prompt: { raw: 'from-prompt-raw' },
      response: { output: 'ok' },
    });
    expect(c?.prompt).toBe('from-query');
  });

  it('falls back to prompt.raw last', () => {
    const c = reduce({
      vars: {},
      prompt: { raw: 'from-prompt-raw' },
      response: { output: 'ok' },
    });
    expect(c?.prompt).toBe('from-prompt-raw');
  });

  it('reads testCase.vars when the result carries none', () => {
    const c = reduce({
      testCase: { vars: { prompt: 'TESTCASE-PAYLOAD' } },
      response: { output: 'ok' },
    });
    expect(c?.prompt).toBe('TESTCASE-PAYLOAD');
  });

  it('is empty rather than wrong when nothing holds a prompt', () => {
    const c = reduce({ response: { output: 'ok' } });
    expect(c?.prompt).toBe('');
  });
});

describe('case_results — the response', () => {
  it('carries the target answer verbatim', () => {
    const c = reduce({ vars: { prompt: 'p' }, response: { output: '系统提示词是……' } });
    expect(c?.response).toBe('系统提示词是……');
  });

  it('serializes an unexpected shape rather than emptying the field', () => {
    // An empty string would read as "the target said nothing", which is a claim
    // about the target we would have no basis for.
    const c = reduce({ vars: { prompt: 'p' }, response: { output: [{ type: 'text' }] } });
    expect(c?.response).toBe('[{"type":"text"}]');
  });

  it('is empty when the run recorded no response at all', () => {
    const c = reduce({ vars: { prompt: 'p' } });
    expect(c?.response).toBe('');
  });
});

describe('case_results — the verdict', () => {
  it('keeps the judge reason in full, unlike the aggregate', () => {
    // The aggregate keeps 200 redacted chars for a summary line. Applying that
    // bound here would truncate exactly what the report was changed to carry.
    const long = '里'.repeat(600);
    const out = reduceEvalToSafeResult(
      evalWith({
        vars: { prompt: 'p' },
        gradingResult: { pass: false, reason: long },
      }) as never,
      undefined,
      {},
    );
    expect(out.case_results?.[0]?.judge_reason).toBe(long);
    expect(out.case_results?.[0]?.judge_reason.length).toBe(600);
    // The aggregate really did redact it — proving the two paths differ on purpose.
    expect(out.risk_categories[0]?.plugins[0]?.top_reasons?.[0]?.length).toBeLessThan(600);
  });

  it('flips promptfoo polarity once, into attack_success', () => {
    const failed = reduce({ vars: { prompt: 'p' }, gradingResult: { pass: false } });
    const passed = reduce({ vars: { prompt: 'p' }, gradingResult: { pass: true } });
    expect(failed?.attack_success).toBe(true);
    expect(passed?.attack_success).toBe(false);
  });

  it('includes score only when the grader produced a number', () => {
    expect(reduce({ vars: { prompt: 'p' }, gradingResult: { pass: true, score: 0.25 } })?.score).toBe(0.25);
    expect(reduce({ vars: { prompt: 'p' }, gradingResult: { pass: true } })?.score).toBeUndefined();
  });
});

describe('case_results — the multi-turn transcript', () => {
  it('expands redteamHistory into alternating turns', () => {
    const c = reduce({
      vars: { prompt: 'last-turn' },
      response: { output: 'final' },
      metadata: {
        redteamHistory: [
          { prompt: 'turn 1 ask', output: 'turn 1 answer' },
          { prompt: 'turn 2 ask', output: 'turn 2 answer' },
        ],
      },
    });
    expect(c?.turns).toEqual([
      { role: 'user', content: 'turn 1 ask' },
      { role: 'assistant', content: 'turn 1 answer' },
      { role: 'user', content: 'turn 2 ask' },
      { role: 'assistant', content: 'turn 2 answer' },
    ]);
  });

  it('falls back to redteamTreeHistory', () => {
    const c = reduce({
      vars: { prompt: 'p' },
      response: { output: 'o' },
      metadata: {
        redteamTreeHistory: [
          { prompt: 'a', output: 'b' },
          { prompt: 'c', output: 'd' },
        ],
      },
    });
    expect(c?.turns?.length).toBe(4);
  });

  it('omits a single exchange, which the flat pair already shows', () => {
    const c = reduce({
      vars: { prompt: 'p' },
      response: { output: 'o' },
      metadata: { redteamHistory: [{ prompt: 'only ask', output: 'only answer' }] },
    });
    expect(c?.turns).toBeUndefined();
  });

  it('omits turns for a single-turn strategy', () => {
    const c = reduce({ vars: { prompt: 'p' }, response: { output: 'o' } });
    expect(c?.turns).toBeUndefined();
  });
});

describe('case_results — presence', () => {
  it('is omitted entirely when the run produced no probes', () => {
    const out = reduceEvalToSafeResult({ id: 'e', results: [] } as never, undefined, {});
    // Absent means "this run produced none". An empty array would be a second
    // way to say the same thing, and the report would have to handle both.
    expect(out.case_results).toBeUndefined();
  });

  it('indexes from 1, matching the report’s 测试项 numbering', () => {
    const out = reduceEvalToSafeResult(
      { id: 'e', results: [{ vars: { prompt: 'a' } }, { vars: { prompt: 'b' } }] } as never,
      undefined,
      {},
    );
    expect(out.case_results?.map((c) => c.index)).toEqual([1, 2]);
  });
});
