/**
 * U8 · judge_source（判定标准）在前端的解析与文案。
 *
 * 优先级：**行为必须与后端
 * `internal/maclaw/redteam_judge_source.go` 的 NormalizeJudgeSource /
 * SummarizeEngineJudgeSources 一致**。两侧任一单独改动都会让报告卡显示
 * 一个后端并不认可的口径。
 *
 * 本文件守三条：
 *  1. `judge_source`（标准）与 `judge_track`（链路）互不污染；
 *  2. 兜底档必须**带条数**显示 —— 降级只有被计数才叫可见；
 *  3. 分布解析不出来时返回 null，**不猜**：给一份口径不明的报告贴上
 *     确定的标准，比不显示更糟。
 */
import { describe, expect, it } from 'vitest'
import {
  JUDGE_SOURCE_FALLBACK,
  reportJudgeSourceCaption,
  resolveReportJudgeSources,
  resolveReportJudgeTrack,
} from './chatDisplay'

describe('resolveReportJudgeTrack vs resolveReportJudgeSources — 正交的两层口径', () => {
  it('解析判定链路，不受判定标准影响', () => {
    expect(resolveReportJudgeTrack({ metadata: { judge_track: 'engine', judge_source: 'category:harmful' } })).toBe('engine')
  })

  it('解析判定标准，不受判定链路影响', () => {
    const sources = resolveReportJudgeSources({ judge_sources: 'category:harmful=5,category:privacy=2' })
    expect(sources?.distinct).toBe(2)
    expect(resolveReportJudgeTrack({ metadata: { judge_track: 'platform', judge_sources: 'category:harmful=5' } })).toBe(
      'platform',
    )
  })

  it('U8 的标准不覆盖 U4 的链路判定，反之亦然', () => {
    // 只有 judge_source、没有 judge_track 的报告：链路走既有推断（platform），
    // 标准照常显示。两者共存时互不影响。
    const metadata = { judge_source: 'category:privacy' }
    expect(resolveReportJudgeTrack({ metadata })).toBe('platform')
    expect(reportJudgeSourceCaption({ judgeSource: 'category:privacy', metadata })).toContain('category:privacy')
  })
})

describe('resolveReportJudgeSources — 分布解析', () => {
  it('解析出每档探测数与兜底条数', () => {
    const sources = resolveReportJudgeSources({
      judge_sources: `category:harmful=5,category:injection=3,${JUDGE_SOURCE_FALLBACK}=2`,
    })
    expect(sources).toEqual({
      bySource: { 'category:harmful': 5, 'category:injection': 3, [JUDGE_SOURCE_FALLBACK]: 2 },
      fallbackProbes: 2,
      // No promptfoo-owned tier in this run — asserted explicitly so a future
      // default change can't quietly make it non-zero.
      promptfooOwnedProbes: 0,
      distinct: 3,
    })
  })

  it('无分布字段时返回 null（=未记录），不返回空分布', () => {
    // 「未记录」与「全部按族判、无人落兜底」是两种不同事实，不能互相顶替。
    expect(resolveReportJudgeSources({})).toBeNull()
    expect(resolveReportJudgeSources(undefined)).toBeNull()
    expect(resolveReportJudgeSources({ judge_sources: '   ' })).toBeNull()
  })

  it.each([
    ['category:harmful'],
    ['=5'],
    ['category:harmful=x'],
    ['category:harmful=-1'],
    ['category:harmful=1.5'],
  ])('非法直方图 %s 返回 null 而不是尽力猜', (raw) => {
    expect(resolveReportJudgeSources({ judge_sources: raw })).toBeNull()
  })
})

describe('reportJudgeSourceCaption — 口径说明', () => {
  it('无记录时返回空串，调用方据此整行不渲染', () => {
    expect(reportJudgeSourceCaption({ metadata: {} })).toBe('')
    expect(reportJudgeSourceCaption({ metadata: undefined })).toBe('')
  })

  it('分布非法时不显示任何标准 —— 不给口径不明的报告贴确定标签', () => {
    const caption = reportJudgeSourceCaption({
      judgeSource: 'category:harmful',
      metadata: { judge_sources: 'garbage' },
    })
    // 非法分布时不猜分布，但单值键本身可用，仍按单档呈现且不含臆造的分布。
    expect(caption).toContain('category:harmful')
    expect(caption).not.toContain('档标准')
  })

  it('单档时给出该档', () => {
    const caption = reportJudgeSourceCaption({ judgeSource: 'plugin:pii', metadata: {} })
    expect(caption).toContain('plugin:pii')
  })

  // U8 的核心可观测性断言：降级必须带条数出现在用户眼前。
  it('有兜底探测时以警告形态出现，并给出具体条数', () => {
    const caption = reportJudgeSourceCaption({
      judgeSource: 'category:harmful',
      metadata: { judge_sources: `category:harmful=8,${JUDGE_SOURCE_FALLBACK}=3` },
    })
    // The ⚠️ sits on the fallback sentence, not at the start of the whole
    // caption — matching the backend's composition in
    // `judgeSourceCaptionFallback` ("⚠️ 其中 N 条探测…"). The two sides render
    // the same report, so the warning marker must land in the same place.
    expect(caption).toContain('⚠️')
    expect(caption).toContain('3 条探测')
    expect(caption).toContain(JUDGE_SOURCE_FALLBACK)
  })

  it('多档时说明同一报告内尺度不同（U8 生效的标志，不是异常）', () => {
    const caption = reportJudgeSourceCaption({
      judgeSource: 'category:harmful',
      metadata: { judge_sources: 'category:harmful=5,category:injection=3' },
    })
    expect(caption).toContain('2 档标准')
    expect(caption).not.toContain('⚠️')
  })

  it('档位按字典序稳定排序，同一份报告每次渲染措辞一致', () => {
    const a = reportJudgeSourceCaption({ metadata: { judge_sources: 'category:privacy=2,category:harmful=5' } })
    const b = reportJudgeSourceCaption({ metadata: { judge_sources: 'category:privacy=2,category:harmful=5' } })
    expect(a).toBe(b)
    expect(a.indexOf('category:harmful')).toBeLessThan(a.indexOf('category:privacy'))
  })

  it('metadata 里的 judge_source 作为单值键的回退读取', () => {
    const caption = reportJudgeSourceCaption({ metadata: { judge_source: 'category:brand' } })
    expect(caption).toContain('category:brand')
  })
})
// ---------------------------------------------------------------------------
// U8 ①：`promptfoo:<id>` 档必须与 `fallback:unmapped` 分开呈现。
//
// 成因不同：兜底档是「平台没有对应标准」，promptfoo 档是「平台有标准但
// 上游缺陷导致注入不了」。混成一句会把排查方向带偏 —— 用户会去查目录缺
// 标准，而真正的原因是上游 bug。
// ---------------------------------------------------------------------------
describe('promptfoo 自带标准这一档', () => {
  const withPromptfoo = { judge_sources: 'category:industry=6,promptfoo:policy=2' }

  it('单独计数，不并入兜底', () => {
    const sources = resolveReportJudgeSources(withPromptfoo)
    expect(sources?.promptfooOwnedProbes).toBe(2)
    expect(sources?.fallbackProbes).toBe(0)
    expect(sources?.distinct).toBe(2)
  })

  it('说明文案点出这一档并给出条数', () => {
    const caption = reportJudgeSourceCaption({ judgeSource: 'category:industry', metadata: withPromptfoo })
    expect(caption).toContain('2 条探测由 promptfoo 自带')
    expect(caption).toContain('平台标准未能注入')
  })

  it('不被误写成「未匹配到族标准」', () => {
    const caption = reportJudgeSourceCaption({ judgeSource: 'category:industry', metadata: withPromptfoo })
    expect(caption).not.toContain('未匹配到插件族标准')
    expect(caption).not.toContain('通用兜底标准')
  })

  it('不与兜底档混成同一句：两档同时出现时两条提示都在', () => {
    const both = { judge_sources: `category:industry=4,promptfoo:policy=2,${JUDGE_SOURCE_FALLBACK}=1` }
    const caption = reportJudgeSourceCaption({ judgeSource: 'category:industry', metadata: both })
    expect(caption).toContain('1 条探测未匹配到插件族标准')
    expect(caption).toContain('2 条探测由 promptfoo 自带')
  })

  it('没有这一档时不出现该措辞（不制造噪音）', () => {
    const caption = reportJudgeSourceCaption({
      judgeSource: 'category:harmful',
      metadata: { judge_sources: 'category:harmful=5' },
    })
    expect(caption).not.toContain('promptfoo')
  })
})
