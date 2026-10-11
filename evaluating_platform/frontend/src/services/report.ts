import api from './api'

/** 报告 finding（后端 sanitizeRedteamArtifactText 已脱敏：密钥/路径/超长截断）。 */
export interface MaclawReportFinding {
  id?: string
  title?: string
  severity?: string
  category?: string
  description?: string
  suggestion?: string
}

export const JUDGE_TRACK_PLATFORM = 'platform' as const
export const JUDGE_TRACK_ENGINE = 'engine' as const

export interface MaclawReport {
  id?: string
  report_id?: string
  title?: string
  summary?: string
  risk_level?: string
  safety_score?: number
  /**
   * U4 判定口径：本份报告的分数由哪条判定链路算出。
   * `platform` = 平台侧 Judge，`engine` = promptfoo 引擎侧 Judge。
   * 两者口径不同、分数不可横向比较。旧报告无此字段（undefined），
   * 此时应按 `metadata.engine` 推断而不是当作 platform 直接显示。
   * 取值须与后端 `RedteamJudgeTrackPlatform` / `RedteamJudgeTrackEngine` 一致。
   */
  judge_track?: typeof JUDGE_TRACK_PLATFORM | typeof JUDGE_TRACK_ENGINE
  /**
   * U8 判定**标准**：本份报告的分数是按哪条 rubric 算出的。
   * `plugin:<id>` = 高危插件的专属标准，`category:<key>` = 族级标准，
   * `fallback:unmapped` = 未匹配到插件族标准、已回落到通用兜底标准。
   *
   * 与 `judge_track` **正交且不可合并**：track 回答「谁判的」，
   * source 回答「按什么标准判」。合并后「换了链路」会覆盖「换了标准」。
   * 旧报告无此字段（undefined），此时应显示「未记录」而不是替它选一个标准。
   */
  judge_source?: string
  /**
   * U8 判定标准分布，格式 `档位=条数,档位=条数`（后端稳定排序）。
   * 只有单值键时，「99 条按族判 + 1 条落兜底」会显示成「按族判」，
   * 降级就被抹掉了 —— 所以两个字段并存。
   */
  judge_sources?: string
  findings?: MaclawReportFinding[]
  metadata?: Record<string, string>
}

export const reportService = {
  async getMaclawReport(reportId: string): Promise<MaclawReport> {
    const res = await api.get<MaclawReport>(`/maclaw/evaluation/reports/${encodeURIComponent(reportId)}`)
    return res.data
  },
  async downloadMaclawReport(reportId: string, format: 'pdf' | 'markdown' | 'json' = 'pdf'): Promise<Blob> {
    const res = await api.get(`/maclaw/evaluation/reports/${encodeURIComponent(reportId)}/export`, {
      params: { format },
      responseType: 'blob',
    })
    return res.data
  },
}

export function triggerBrowserDownload(blob: Blob, filename: string, fallbackType = 'application/pdf') {
  const typedBlob = blob.type ? blob : new Blob([blob], { type: fallbackType })
  const url = window.URL.createObjectURL(typedBlob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.rel = 'noopener'
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  window.setTimeout(() => window.URL.revokeObjectURL(url), 60_000)
}
