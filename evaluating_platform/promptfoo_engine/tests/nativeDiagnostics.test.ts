/**
 * P2-9 补批 · 生成失败时「模型到底做了什么」的诊断摘录。
 *
 * 2026-10-14 的验收撞在这堵墙上：5 个插件 id 在生成阶段产出 0 用例，运行失败，
 * 而引擎日志只有 promptfoo 的 "Failed to generate tests for X / Generated 0 tests"
 * —— 「模型是拒答了这些类别，还是没按模板输出」无法回答，除非进容器复现。
 * 这与 error_brief 当初要解决的那批 U7 未决项是同一形状。
 *
 * promptfoo 把真正的答案（模型的原话）通过 logger 发出来，而 `redteam.run` 接受公开的
 * `logCallback` 选项。这里钉住采集器的行为：只留诊断行、必须脱敏、必须有界。
 */
import { describe, expect, it } from 'vitest';

import { createDiagnosticLogCollector } from '../src/runner/nativeRedteam.js';

function collect(lines: string[], limit?: number): string {
  const collector = createDiagnosticLogCollector(limit === undefined ? undefined : { limit });
  for (const line of lines) collector.callback(line);
  return collector.excerpt();
}

describe('createDiagnosticLogCollector', () => {
  it('returns nothing when no line looks like a diagnosis', () => {
    // 正常运行的日志不该被塞进失败信息里 —— 摘录只在真有话说时出现。
    expect(collect(['starting run', 'loaded 6 test cases', 'scan complete'])).toBe('');
    expect(collect([])).toBe('');
  });

  it('keeps only the lines that name a cause', () => {
    const excerpt = collect([
      'starting run',
      'Failed to generate tests for harmful:cybercrime',
      'progress 3/6',
      'Generated 0 tests for bias:age',
    ]);
    expect(excerpt).toContain('harmful:cybercrime');
    expect(excerpt).toContain('bias:age');
    expect(excerpt).not.toContain('starting run');
    expect(excerpt).not.toContain('progress 3/6');
  });

  it('keeps the LAST few matching lines and is bounded by the ring limit', () => {
    // 最近的行离失败点最近，也最有信息量。
    const lines = Array.from({ length: 200 }, (_, i) => `error detail ${i}`);
    const excerpt = collect(lines, 10);
    expect(excerpt).toContain('error detail 199');
    expect(excerpt).not.toContain('error detail 150');
    // 只保留最后 3 条命中的行。
    expect(excerpt.split('｜')).toHaveLength(3);
  });

  it('redacts credentials before they reach the brief', () => {
    // 摘录会进入 error_brief（持久化的、面向租户的表），所以它必须过脱敏。
    const excerpt = collect([
      'provider failed with key sk-abc123DEF456ghi789 at https://llm.internal:8443/v1',
    ]);
    expect(excerpt).not.toContain('sk-abc123DEF456ghi789');
    expect(excerpt).not.toContain('llm.internal');
    expect(excerpt).toContain('provider failed');
  });

  it('replaces a content-shaped line wholesale rather than quoting it', () => {
    // 日志行可能带上用例或响应片段。少一条摘录可以接受，把响应正文带进 engine_runs
    // 不可以。
    const excerpt = collect(['the provider rejected the request: raw prompt body included']);
    expect(excerpt).not.toContain('raw prompt body');
    expect(excerpt).toContain('redacted');
  });

  it('caps the excerpt so the failure message stays readable', () => {
    // 自然文本：单个超长无分隔串会被脱敏规则整段替换，反而测不到截断分支。
    const excerpt = collect([`error the provider rejected the request because ${'it was too long '.repeat(20)}`]);
    expect(excerpt.length).toBeLessThanOrEqual(240);
    expect(excerpt.endsWith('...')).toBe(true);
  });

  it('ignores blank lines', () => {
    expect(collect(['', '   ', '\n'])).toBe('');
  });
});

describe('logCallback wiring', () => {
  it('is passed under the option name promptfoo declares', () => {
    // 选项名写错时，`as Parameters<...>[0]` 的整体转型会绕过多余属性检查，编译不会报错
    // —— 所以 nativeRedteam.ts 把回调单独绑到 promptfoo 自己声明的字段类型上。这条
    // 断言读源码确认那段绑定还在（删掉它，回退成裸属性就又会静默失效）。
    const source = require('node:fs').readFileSync(
      new URL('../src/runner/nativeRedteam.ts', import.meta.url),
      'utf8',
    ) as string;
    expect(source).toContain("NonNullable<Parameters<typeof promptfoo.redteam.run>[0]>['logCallback']");
    expect(source).toContain('logCallback: diagnosticLogCallback');
  });
});
