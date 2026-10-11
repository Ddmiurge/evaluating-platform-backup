/**
 * judgeRubric.test.ts — U8 · per-plugin rubric resolution.
 *
 * Three things are guarded here:
 *  1. the catalog is the only source of standards (no id-guessing);
 *  2. the fallback tier is **explicit and named**, never a silent reuse of the
 *     old generic rubric — that reuse is exactly "U8 didn't happen";
 *  3. reduction reports which rubric judged what, per plugin and per run.
 */
import { describe, expect, it } from 'vitest';
import {
  buildJudgeRubricMap,
  PLUGIN_JUDGE_SOURCE_METADATA_KEY,
  resolveJudgeRubric,
  UNMAPPED_JUDGE_CRITERIA,
  UNMAPPED_JUDGE_RUBRIC,
  UNMAPPED_JUDGE_SOURCE,
} from '../src/runner/judgeRubric.js';
import { reduceEvalToSafeResult } from '../src/runner/redteamRun.js';
import type { EngineCatalog } from '../src/types.js';

/** A catalog shaped like the one the BFF pushes: two families, one override. */
function catalog(): EngineCatalog {
  return {
    plugins: [
      { id: 'pii:direct', category: 'privacy', category_label: '隐私泄露', severity: 'high', judge_criteria: 'RUBRIC_PII', judge_source: 'plugin:pii' },
      { id: 'harmful:hate', category: 'harmful', category_label: '有害内容', severity: 'high', judge_criteria: 'RUBRIC_HARMFUL', judge_source: 'category:harmful' },
      // Blank criteria: must NOT become an empty rubric (an empty llm-rubric
      // still returns a verdict — an unjudged probe that counts as judged).
      { id: 'blank:rubric', category: 'harmful', category_label: '有害内容', severity: 'high', judge_criteria: '   ', judge_source: 'category:harmful' },
      // Criteria with no identity: untraceable, so it degrades to the named tier.
      { id: 'anon:rubric', category: 'harmful', category_label: '有害内容', severity: 'high', judge_criteria: 'RUBRIC_ANON', judge_source: '' },
    ],
    strategies: [],
  };
}

describe('buildJudgeRubricMap — standards come from the catalog, nothing else', () => {
  it('indexes each plugin by the rubric the BFF supplied', () => {
    const map = buildJudgeRubricMap(catalog());
    expect(map.get('pii:direct')?.criteria).toBe('RUBRIC_PII');
    expect(map.get('pii:direct')?.source).toBe('plugin:pii');
    expect(map.get('harmful:hate')?.source).toBe('category:harmful');
  });

  it('gives plugins from different families different rubrics', () => {
    // The whole point of U8: two plugins must not be judged by one string.
    const map = buildJudgeRubricMap(catalog());
    expect(map.get('pii:direct')?.criteria).not.toBe(map.get('harmful:hate')?.criteria);
  });

  // Guardrail counter-proof #1 — "a plugin with no rubric".
  it('excludes a plugin whose rubric text is blank, so it lands on the fallback', () => {
    const map = buildJudgeRubricMap(catalog());
    expect(map.has('blank:rubric')).toBe(false);
    const resolved = resolveJudgeRubric('blank:rubric', map);
    expect(resolved.isFallback).toBe(true);
    expect(resolved.criteria.length).toBeGreaterThan(0);
  });

  // Guardrail counter-proof #2 — an untraceable rubric is not accepted as-is.
  it('refuses criteria with no judge_source rather than shipping an anonymous rubric', () => {
    const map = buildJudgeRubricMap(catalog());
    expect(map.has('anon:rubric')).toBe(false);
    expect(resolveJudgeRubric('anon:rubric', map).source).toBe(UNMAPPED_JUDGE_SOURCE);
  });

  it('returns an empty map for a missing catalog, so every plugin degrades explicitly', () => {
    const map = buildJudgeRubricMap(undefined);
    expect(map.size).toBe(0);
    expect(resolveJudgeRubric('anything', map).isFallback).toBe(true);
  });

  it('never invents a family from the plugin id', () => {
    // The id *looks* like a privacy plugin but is not in the catalog. U2
    // already ruled that ids are never pattern-matched into families.
    const resolved = resolveJudgeRubric('pii:direct', buildJudgeRubricMap({ plugins: [], strategies: [] }));
    expect(resolved.isFallback).toBe(true);
    expect(resolved.source).toBe(UNMAPPED_JUDGE_SOURCE);
  });
});

describe('the fallback tier is explicit, not a silent generic rubric', () => {
  it('is named and flags itself as unmatched', () => {
    expect(UNMAPPED_JUDGE_SOURCE).toBe('fallback:unmapped');
    expect(UNMAPPED_JUDGE_CRITERIA).toContain('未匹配到插件族标准');
  });

  // The polarity contract. reduceEvalToSafeResult flips promptfoo's verdict, so
  // an inverted rubric inverts every conclusion in the report.
  it('still states the PASS/FAIL polarity', () => {
    expect(UNMAPPED_JUDGE_CRITERIA).toContain('PASS');
    expect(UNMAPPED_JUDGE_CRITERIA).toContain('FAIL');
  });

  it('is not the pre-U8 generic rubric', () => {
    // Reusing SAFETY_RUBRIC here would make "judged by a family standard" and
    // "not judged by any standard" look identical in the report.
    expect(UNMAPPED_JUDGE_CRITERIA).not.toContain('泄露了敏感信息');
  });

  it('is what resolveJudgeRubric returns for an unknown plugin', () => {
    expect(resolveJudgeRubric('nope', buildJudgeRubricMap(catalog()))).toEqual(UNMAPPED_JUDGE_RUBRIC);
  });
});

describe('reduceEvalToSafeResult — the rubric that judged is traceable', () => {
  const evalWith = (pluginId: string, judgeSource?: string, pass = false) => ({
    results: [
      {
        gradingResult: { pass, reason: 'r' },
        testCase: {
          metadata: {
            pluginId,
            strategyId: 'direct',
            ...(judgeSource ? { [PLUGIN_JUDGE_SOURCE_METADATA_KEY]: judgeSource } : {}),
          },
        },
      },
    ],
  });

  it('reports the rubric per plugin', () => {
    const result = reduceEvalToSafeResult(evalWith('pii:direct', 'plugin:pii'), catalog());
    expect(result.plugin_stats[0]?.judge_source).toBe('plugin:pii');
  });

  it('counts probes per rubric for the whole run', () => {
    const result = reduceEvalToSafeResult(
      {
        results: [
          ...evalWith('pii:direct', 'plugin:pii').results,
          ...evalWith('harmful:hate', 'category:harmful').results,
        ],
      },
      catalog(),
    );
    const bySource = Object.fromEntries((result.judge_sources ?? []).map((s) => [s.source, s.probes]));
    expect(bySource['plugin:pii']).toBe(1);
    expect(bySource['category:harmful']).toBe(1);
    expect(bySource[UNMAPPED_JUDGE_SOURCE]).toBeUndefined();
  });

  // Guardrail counter-proof: the fallback must be *countable*, otherwise a
  // degraded run is indistinguishable from a fully-specified one.
  it('flags fallback probes with is_fallback so a degraded run is visible', () => {
    const result = reduceEvalToSafeResult(evalWith('ghost:plugin', UNMAPPED_JUDGE_SOURCE), catalog());
    expect(result.judge_sources).toEqual([{ source: UNMAPPED_JUDGE_SOURCE, probes: 1, is_fallback: true }]);
    expect(result.plugin_stats[0]?.judge_source).toBe(UNMAPPED_JUDGE_SOURCE);
  });

  // Plan C results carry no stamp, so the reducer resolves from the catalog.
  // Without this the native-generation path would report every probe as
  // fallback and the count would be meaningless.
  it('resolves the rubric from the catalog when the metadata stamp is absent', () => {
    const result = reduceEvalToSafeResult(evalWith('harmful:hate'), catalog());
    expect(result.plugin_stats[0]?.judge_source).toBe('category:harmful');
    expect(result.judge_sources?.every((s) => !s.is_fallback)).toBe(true);
  });

  it('still judges a plugin absent from the catalog, marking the degradation', () => {
    const result = reduceEvalToSafeResult(evalWith('ghost:plugin'), catalog());
    expect(result.totals.probes).toBe(1);
    expect(result.plugin_stats[0]?.judge_source).toBe(UNMAPPED_JUDGE_SOURCE);
  });

  it('keeps classification behaviour unchanged', () => {
    // U8 must not alter what counts as an attack success.
    const result = reduceEvalToSafeResult(evalWith('harmful:hate', 'category:harmful', false), catalog());
    expect(result.totals.attack_success).toBe(1);
    expect(result.severity_counts.high).toBe(1);
  });
});