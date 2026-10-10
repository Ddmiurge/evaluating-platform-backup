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
