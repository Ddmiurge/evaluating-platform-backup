/**
 * Catalog-driven classification coverage (U2/T2.2).
 *
 * The engine no longer infers a plugin's category/severity from its id with
 * regex tables — it looks every plugin_id up in the catalog the BFF ships with
 * the run (from promptfoo_plugin_catalog.go). These tests assert that lookup
 * behaviour: catalog values win for known ids, and an unknown id falls back to
 * other/medium (never a regex guess). The fixture table mirrors the backend
 * catalog; the Go side asserts the same values in promptfoo_plugin_catalog_test.go.
 */
import { describe, expect, it } from 'vitest';
import { reduceEvalToSafeResult } from '../src/runner/redteamRun.js';
import type { EvalLike } from '../src/runner/redteamRun.js';
import type { EngineCatalog, EngineCatalogPlugin } from '../src/types.js';

/** Chinese category labels — mirrors the backend's promptfooCategoryLabels. */
const CATEGORY_LABELS: Record<string, string> = {
  privacy: '隐私泄露',
  harmful: '有害内容',
  injection: '提示注入',
  jailbreak: '越狱对抗',
  bias: '偏见歧视',
  hallucination: '幻觉与事实性',
  'off-topic': '离题边界',
  brand: '品牌风险',
  sensitive: '敏感话题',
  dataset: '数据集基准',
  industry: '行业合规',
  'security-exploit': '安全利用',
  other: '其他',
};

/** id -> [category, severity], matching promptfoo_plugin_catalog.go. */
const PLUGIN_TABLE: Record<string, [category: string, severity: string]> = {
  harmful: ['harmful', 'high'],
  'harmful:cybercrime': ['harmful', 'critical'],
  'harmful:illegal-activities': ['harmful', 'critical'],
  'harmful:violent-crime': ['harmful', 'critical'],
  'harmful:child-exploitation': ['harmful', 'critical'],
  pii: ['privacy', 'high'],
  'cross-session-leak': ['privacy', 'high'],
  'prompt-injection': ['injection', 'high'],
  'indirect-prompt-injection': ['injection', 'high'],
  'ascii-smuggling': ['injection', 'high'],
  'excessive-agency': ['injection', 'high'],
  hijacking: ['injection', 'high'],
  'system-prompt-override': ['injection', 'high'],
  'prompt-extraction': ['injection', 'high'],
  mcp: ['injection', 'high'],
  'agentic:memory-poisoning': ['injection', 'high'],
  jailbreak: ['jailbreak', 'high'],
  bias: ['bias', 'medium'],
  'security-exploit': ['security-exploit', 'high'],
  'reasoning-dos': ['security-exploit', 'medium'],
  hallucination: ['hallucination', 'medium'],
  overreliance: ['hallucination', 'medium'],
  'divergent-repetition': ['hallucination', 'low'],
  'off-topic': ['off-topic', 'low'],
  wordplay: ['off-topic', 'low'],
  competitors: ['brand', 'low'],
  imitation: ['brand', 'medium'],
  politics: ['sensitive', 'medium'],
  religion: ['sensitive', 'medium'],
  'teen-safety:age-restricted-goods-and-services': ['sensitive', 'high'],
  contracts: ['industry', 'medium'],
  policy: ['industry', 'medium'],
  'medical:hallucination': ['industry', 'critical'],
  'pharmacy:dosage-calculation': ['industry', 'critical'],
  'financial:sox-compliance': ['industry', 'high'],
  'telecom:cpni-disclosure': ['industry', 'high'],
  'realestate:steering': ['industry', 'high'],
  'ecommerce:pci-dss': ['industry', 'high'],
  beavertails: ['dataset', 'high'],
  harmbench: ['dataset', 'high'],
  pliny: ['dataset', 'high'],
  donotanswer: ['dataset', 'high'],
  cyberseceval: ['dataset', 'high'],
  xstest: ['dataset', 'low'],
};

const PLUGIN_IDS = Object.keys(PLUGIN_TABLE);

function fixtureCatalog(): EngineCatalog {
  const plugins: EngineCatalogPlugin[] = PLUGIN_IDS.map((id) => {
    const [category, severity] = PLUGIN_TABLE[id]!;
    return { id, category, category_label: CATEGORY_LABELS[category] ?? category, severity };
  });
  return { plugins, strategies: [] };
}

function evalWithOneSuccessPerPlugin(
  pluginIds: readonly string[],
  severity?: string,
): EvalLike {
  return {
    results: pluginIds.map((id) => ({
      gradingResult: { pass: false, reason: `被测模型遵从了 ${id} 攻击指令并输出有害内容`, ...(severity ? { severity } : {}) },
      testCase: { metadata: { pluginId: id, strategyId: 'direct' } },
    })),
  } as unknown as EvalLike;
}

describe('reduceEvalToSafeResult catalog-driven classification', () => {
  const catalog = fixtureCatalog();

  it('buckets every catalog plugin id into a named risk category', () => {
    const result = reduceEvalToSafeResult(evalWithOneSuccessPerPlugin(PLUGIN_IDS), catalog);
    const categoryKeys = result.risk_categories.map((c) => c.key);
    expect(categoryKeys).not.toContain('other');
    const grouped = result.risk_categories.flatMap((c) => c.plugins.map((p) => p.plugin_id));
    expect(new Set(grouped).size).toBe(PLUGIN_IDS.length);
  });

  it('takes severity from the catalog for known ids', () => {
    const result = reduceEvalToSafeResult(
      evalWithOneSuccessPerPlugin([
        'harmful:cybercrime', 'harmful:child-exploitation', 'medical:hallucination',
        'pharmacy:dosage-calculation', 'harmful', 'telecom:cpni-disclosure', 'beavertails',
        'hallucination', 'competitors', 'wordplay', 'xstest',
      ]),
      catalog,
    );
    const byPlugin = new Map(result.plugin_stats.map((p) => [p.plugin_id, p]));
    expect(byPlugin.get('harmful:cybercrime')?.max_severity).toBe('critical');
    expect(byPlugin.get('harmful:child-exploitation')?.max_severity).toBe('critical');
    expect(byPlugin.get('medical:hallucination')?.max_severity).toBe('critical');
    expect(byPlugin.get('pharmacy:dosage-calculation')?.max_severity).toBe('critical');
    expect(byPlugin.get('harmful')?.max_severity).toBe('high');
    expect(byPlugin.get('telecom:cpni-disclosure')?.max_severity).toBe('high');
    expect(byPlugin.get('beavertails')?.max_severity).toBe('high');
    expect(byPlugin.get('hallucination')?.max_severity).toBe('medium');
    // catalog says 'low' for competitors (the old regex guess said 'medium').
    expect(byPlugin.get('competitors')?.max_severity).toBe('low');
    expect(byPlugin.get('wordplay')?.max_severity).toBe('low');
    expect(byPlugin.get('xstest')?.max_severity).toBe('low');
  });

  it('groups industry suites under industry and datasets under dataset', () => {
    const result = reduceEvalToSafeResult(
      evalWithOneSuccessPerPlugin([
        'medical:hallucination', 'financial:sox-compliance', 'telecom:cpni-disclosure',
        'realestate:steering', 'ecommerce:pci-dss', 'contracts', 'policy',
        'beavertails', 'harmbench', 'pliny', 'donotanswer', 'cyberseceval', 'xstest',
        'politics', 'religion', 'teen-safety:age-restricted-goods-and-services',
      ]),
      catalog,
    );
    const byCategory = new Map(result.risk_categories.map((c) => [c.key, c]));
    expect(byCategory.get('industry')?.plugins.length).toBe(7);
    expect(byCategory.get('dataset')?.plugins.length).toBe(6);
    expect(byCategory.get('sensitive')?.plugins.length).toBe(3);
    expect(byCategory.get('industry')?.label).toBe('行业合规');
    expect(byCategory.get('dataset')?.label).toBe('数据集基准');
    expect(byCategory.get('sensitive')?.label).toBe('敏感话题');
  });

  it('groups injection-adjacent plugins (ascii-smuggling / excessive-agency) under injection', () => {
    const result = reduceEvalToSafeResult(
      evalWithOneSuccessPerPlugin(['ascii-smuggling', 'excessive-agency', 'prompt-injection']),
      catalog,
    );
    const injection = result.risk_categories.find((c) => c.key === 'injection');
    expect(injection).toBeDefined();
    expect(injection?.plugins.map((p) => p.plugin_id).sort()).toEqual([
      'ascii-smuggling',
      'excessive-agency',
      'prompt-injection',
    ]);
  });

  it('labels brand / off-topic / hallucination categories in Chinese', () => {
    const result = reduceEvalToSafeResult(
      evalWithOneSuccessPerPlugin(['competitors', 'off-topic', 'hallucination']),
      catalog,
    );
    const labels = result.risk_categories.map((c) => c.label);
    expect(labels).toContain('品牌风险');
    expect(labels).toContain('离题边界');
    expect(labels).toContain('幻觉与事实性');
  });

  it('falls back to other/medium for an id absent from the catalog', () => {
    const result = reduceEvalToSafeResult(
      evalWithOneSuccessPerPlugin(['totally-made-up-plugin']),
      catalog,
    );
    const other = result.risk_categories.find((c) => c.key === 'other');
    expect(other).toBeDefined();
    expect(other?.label).toBe('其他');
    expect(result.plugin_stats[0]?.plugin_id).toBe('totally-made-up-plugin');
    expect(result.plugin_stats[0]?.max_severity).toBe('medium');
  });

  it('falls back to other/medium when no catalog is supplied', () => {
    const result = reduceEvalToSafeResult(evalWithOneSuccessPerPlugin(['harmful:cybercrime']));
    const other = result.risk_categories.find((c) => c.key === 'other');
    expect(other).toBeDefined();
    expect(result.plugin_stats[0]?.max_severity).toBe('medium');
  });

  it('lets a grader-provided severity win over the catalog value', () => {
    const result = reduceEvalToSafeResult(
      evalWithOneSuccessPerPlugin(['wordplay'], 'critical'),
      catalog,
    );
    expect(result.plugin_stats[0]?.max_severity).toBe('critical');
  });
});
