/**
 * P2-6 · 报告卡上的「生成路径」说明。
 *
 * 这一行是「不要把两份报告并列比较」的唯一依据：Plan A 与 Plan C 在相同输入下
 * 探针数量不同（阶段一实测 Plan A 50 条 vs Plan C 80 探针/6 子插件），而两份报告
 * 的外观完全一样（U7 item14）。
 */
import { describe, expect, it } from 'vitest'

import {
  GENERATION_PATH_PLAN_A,
  GENERATION_PATH_PLAN_C,
  reportGenerationPathCaption,
  resolveReportGenerationPath,
} from './chatDisplay'

describe('resolveReportGenerationPath', () => {
  it('accepts the two enum values, tolerating case and padding', () => {
    expect(resolveReportGenerationPath({ generation_path: 'plan_a' })).toBe(GENERATION_PATH_PLAN_A)
    expect(resolveReportGenerationPath({ generation_path: ' Plan_C ' })).toBe(GENERATION_PATH_PLAN_C)
  })

  it('reports an absent key as unrecorded rather than picking a path', () => {
    // Pre-P2-6 reports have no key. Guessing would publish a false statement about
    // which path produced the numbers.
    const candidates: Array<Record<string, string> | null | undefined> = [
      null,
      undefined,
      {},
      { generation_path: '   ' },
    ]
    for (const metadata of candidates) {
      expect(resolveReportGenerationPath(metadata)).toBe('unrecorded')
    }
  })

  it('flags an uninterpretable value instead of defaulting', () => {
    expect(resolveReportGenerationPath({ generation_path: 'plan_b' })).toBeNull()
  })
})

describe('reportGenerationPathCaption', () => {
  it('names plan_c, the configured probe count and the incomparability', () => {
    const caption = reportGenerationPathCaption({
      metadata: { generation_path: 'plan_c', num_tests_effective: '50' },
    })
    expect(caption).toContain('plan_c')
    expect(caption).toContain('实际配置探针数 50')
    expect(caption).toContain('不可直接比较')
  })

  it('labels plan_a too, and does not carry the plan_c comparison note', () => {
    // R-5-c: the field must have a value on BOTH paths. A caption that only exists
    // for one of them is the easiest shape to miss, because the reports without it
    // look fine.
    const caption = reportGenerationPathCaption({ metadata: { generation_path: 'plan_a' } })
    expect(caption).toContain('plan_a')
    expect(caption).not.toContain('不可直接比较')
  })

  it('omits the count rather than printing a nonsense one', () => {
    // A report whose count is missing or corrupt still gets the path — the path is
    // the part that prevents the wrong comparison.
    for (const raw of [undefined, '', 'many', '0', '-3']) {
      const caption = reportGenerationPathCaption({
        metadata: { generation_path: 'plan_a', num_tests_effective: raw as string },
      })
      expect(caption).toContain('plan_a')
      expect(caption).not.toContain('实际配置探针数')
    }
  })

  it('says "unrecorded" for a historical report instead of rendering nothing', () => {
    // Unlike the judge-track caption, an empty line here would be indistinguishable
    // from a legitimate historical report — so absence must be stated.
    const caption = reportGenerationPathCaption({ metadata: {} })
    expect(caption).toContain('未记录')
    expect(caption).not.toContain('plan_a')
    expect(caption).not.toContain('plan_c')
  })

  it('states invalid metadata rather than rendering nothing', () => {
    const caption = reportGenerationPathCaption({ metadata: { generation_path: 'plan_b' } })
    expect(caption).toContain('非法')
  })

  it('prefers a caption computed by the backend', () => {
    // The backend owns the sentence; using it verbatim is what keeps the two
    // renderings from drifting apart.
    expect(
      reportGenerationPathCaption({ metadata: { generation_path: 'plan_a' }, caption: '  后端整句  ' }),
    ).toBe('后端整句')
  })
})
