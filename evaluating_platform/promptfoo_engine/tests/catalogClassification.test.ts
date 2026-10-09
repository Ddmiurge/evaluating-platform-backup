/**
 * L1 catalog expansion coverage: the engine must bucket every catalog plugin
 * id into a real risk category (not 'other') and infer a sensible display
 * severity for successful attacks.
 */
import { describe, expect, it } from 'vitest';
import { reduceEvalToSafeResult } from '../src/runner/redteamRun.js';
import type { EvalLike } from '../src/runner/redteamRun.js';

const CATALOG_PLUGIN_IDS = [
  'harmful', 'harmful:cybercrime', 'harmful:illegal-activities', 'harmful:violent-crime',
  'pii', 'prompt-injection', 'indirect-prompt-injection', 'ascii-smuggling', 'excessive-agency',
  'jailbreak', 'bias', 'security-exploit', 'hallucination', 'off-topic', 'competitors',
  'medical:hallucination', 'financial:sox-compliance', 'telecom:cpni-disclosure',
  'realestate:steering', 'ecommerce:pci-dss', 'beavertails', 'harmbench', 'pliny',
  'politics', 'religion', 'teen-safety:age-restricted-goods-and-services',
  'mcp', 'agentic:memory-poisoning', 'hijacking', 'system-prompt-override',
  'prompt-extraction', 'cross-session-leak', 'overreliance', 'divergent-repetition',
  'wordplay', 'xstest', 'reasoning-dos', 'imitation', 'contracts', 'policy',
] as const;

function evalWithOneSuccessPerPlugin(pluginIds: readonly string[]): EvalLike {
  return {
    results: pluginIds.map((id) => ({
      gradingResult: { pass: false, reason: `被测模型遵从了 ${id} 攻击指令并输出有害内容` },
      testCase: { metadata: { pluginId: id, strategyId: 'direct' } },
    })),
  } as unknown as EvalLike;
}

describe('reduceEvalToSafeResult L1 catalog coverage', () => {
  it('buckets every catalog plugin id into a named risk category', () => {
    const result = reduceEvalToSafeResult(evalWithOneSuccessPerPlugin(CATALOG_PLUGIN_IDS));
    const categoryKeys = result.risk_categories.map((c) => c.key);
    expect(categoryKeys).not.toContain('other');
    // All fifteen plugin families appear across the category groups.
    const grouped = result.risk_categories.flatMap((c) => c.plugins.map((p) => p.plugin_id));
    expect(new Set(grouped).size).toBe(CATALOG_PLUGIN_IDS.length);
  });

  it('marks the most destructive families critical and grades the tail', () => {
    const result = reduceEvalToSafeResult(
      evalWithOneSuccessPerPlugin([
        'harmful:cybercrime', 'harmful:child-exploitation', 'medical:hallucination',
        'pharmacy:dosage-calculation', 'harmful', 'telecom:cpni-disclosure', 'beavertails',
        'hallucination', 'competitors', 'wordplay', 'xstest',
      ]),
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
    expect(byPlugin.get('competitors')?.max_severity).toBe('medium');
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
    );
    const labels = result.risk_categories.map((c) => c.label);
    expect(labels).toContain('品牌风险');
    expect(labels).toContain('离题边界');
    expect(labels).toContain('幻觉与事实性');
  });
});
