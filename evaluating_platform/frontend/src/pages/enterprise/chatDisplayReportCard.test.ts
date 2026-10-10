import { expect, test } from 'vitest'
import { reportJudgeTrackCaption, resolveReportSafetyScore, resolveReportJudgeTrack } from './chatDisplay.ts'

test('resolveReportSafetyScore prefers a direct safety score, else derives it', () => {
  expect(resolveReportSafetyScore({
    cardType: 'report',
    directSafetyScore: 62.4,
    successCount: 3,
    failureCount: 2,
    executedCount: 5,
  })).toBe(62)

  expect(resolveReportSafetyScore({
    cardType: 'report',
    successCount: 3,
    failureCount: 2,
    executedCount: 5,
  })).toBe(undefined)

  expect(resolveReportSafetyScore({
    cardType: 'progress',
    riskScore: 20,
  })).toBe(80)
})

// U4：判定轨道解析。三档优先级必须与后端
// internal/maclaw/redteam_judge_track.go 的 ResolveRedteamJudgeTrack 一致。
test('resolveReportJudgeTrack follows explicit > engine metadata > platform default', () => {
  expect(resolveReportJudgeTrack({ judgeTrack: 'platform' })).toBe('platform')
  expect(resolveReportJudgeTrack({ judgeTrack: 'engine' })).toBe('engine')

  // 显式声明优先于 engine 键：不能被推断覆盖。
  expect(resolveReportJudgeTrack({
    judgeTrack: 'platform',
    metadata: { engine: 'promptfoo' },
  })).toBe('platform')

  // 旧引擎报告：metadata 无 judge_track，但有 engine 键 → engine。
  expect(resolveReportJudgeTrack({ metadata: { engine: 'promptfoo' } })).toBe('engine')
  expect(resolveReportJudgeTrack({ judgeTrack: '', metadata: { engine: 'promptfoo' } })).toBe('engine')

  // 旧平台报告 / 完全无元数据 → platform 兜底。
  expect(resolveReportJudgeTrack({ metadata: { batch_tool: 'execute_redteam_evaluation_batch' } })).toBe('platform')
  expect(resolveReportJudgeTrack({})).toBe('platform')
  expect(resolveReportJudgeTrack({ metadata: null })).toBe('platform')

  // metadata 兜底读取（judge_track 传undefined 时应回落到 metadata）。
  expect(resolveReportJudgeTrack({
    judgeTrack: undefined,
    metadata: { judge_track: 'engine' },
  })).toBe('engine')
})

// 反证：非法轨道值必须返回 null，**不得**静默按 platform 显示。
// 静默兜底等于给一份来源不明的分数贴上确定的口径标签。
test('resolveReportJudgeTrack returns null for illegal values instead of silently defaulting', () => {
  for (const raw of ['both', 'promptfoo', 'unknown', '1', 'platforms', 'ENGINE_engine']) {
    expect(resolveReportJudgeTrack({ judgeTrack: raw })).toBe(null)
    expect(resolveReportJudgeTrack({ metadata: { judge_track: raw } })).toBe(null)
  }
})

// 大小写与空白属于归一化，不是非法值 —— 与后端 NormalizeRedteamJudgeTrack 对齐。
test('resolveReportJudgeTrack normalizes case and whitespace', () => {
  expect(resolveReportJudgeTrack({ judgeTrack: ' ENGINE ' })).toBe('engine')
  expect(resolveReportJudgeTrack({ judgeTrack: 'Platform' })).toBe('platform')
})

test('reportJudgeTrackCaption renders a track caption and stays empty for illegal input', () => {
  expect(reportJudgeTrackCaption({ judgeTrack: 'platform' })).toContain('平台 Judge')
  expect(reportJudgeTrackCaption({ judgeTrack: 'engine' })).toContain('promptfoo 引擎')
  // 两条链路的文案必须不同，否则分轨在 UI 上不可见。
  expect(reportJudgeTrackCaption({ judgeTrack: 'platform' }))
    .not.toBe(reportJudgeTrackCaption({ judgeTrack: 'engine' }))
  // 两条链路的分数不可横向比较 —— 这是分轨存在的理由，必须出现在文案里。
  expect(reportJudgeTrackCaption({ judgeTrack: 'engine' })).toContain('不可直接横向比较')

  // 口径不明时返回空串，由调用方整行不渲染，而不是渲染一句假的。
  expect(reportJudgeTrackCaption({ judgeTrack: 'both' })).toBe('')
  expect(reportJudgeTrackCaption({})).not.toBe('')
})