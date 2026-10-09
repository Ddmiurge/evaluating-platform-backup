import { useEffect, useState } from 'react'
import { Avatar, Button, InputNumber, Progress, Spin, message } from 'antd'
import { ApiOutlined, BugOutlined, CheckCircleOutlined, DownloadOutlined, ExportOutlined, LoadingOutlined, PlayCircleOutlined, ReloadOutlined, RobotOutlined, SafetyOutlined, ThunderboltOutlined, UserOutlined } from '@ant-design/icons'

import type { ChatMessage, PlanInfo, WelcomeCapability } from '../../services/chat'
import { reportService, triggerBrowserDownload } from '../../services/report'
import { LABELS, RESOURCE_MODE_LABELS, normalizePlanForDisplay, resolveReportSafetyScore } from './chatDisplay'
import { parseChatMarkdown, type ChatMarkdownInline } from './chatMarkdown'

const RISK_COLORS: Record<string, string> = {
  critical: '#dc2626',
  high: '#ea580c',
  高风险: '#ea580c',
  medium: '#d97706',
  中风险: '#d97706',
  low: '#16a34a',
  低风险: '#16a34a',
  info: '#16a34a',
  safe: '#16a34a',
  最高安全: '#16a34a',
  安全: '#16a34a',
}

const RISK_LABELS: Record<string, string> = {
  critical: '严重风险',
  high: '高风险',
  medium: '中风险',
  low: '低风险',
  info: '未发现明显风险',
  safe: '最高安全',
  最高安全: '最高安全',
  安全: '最高安全',
  高风险: '高风险',
  中风险: '中风险',
  低风险: '低风险',
}

const SELECTION_STRATEGY_LABELS: Record<string, string> = {
  random: '随机抽取',
  sequential: '顺序抽取',
  risk_coverage: '按风险覆盖',
  maclaw_selected: 'MaClaw 自主选择',
}

const PROGRESS_STAGE_LABELS: Record<string, string> = {
  queued: '排队中',
  pending: '排队中',
  starting: '启动评估',
  prepare_skill_input: '准备专家样本',
  run_skill: '运行 Skill 生成载荷',
  register_skill_payloads: '登记 Skill 载荷',
  executing: '执行评估',
  running: '执行评估',
  execute_batch: '批量执行评估',
  compose_payloads: '组合评估载荷',
  composing_payloads: '组合评估载荷',
  target_call: '调用被测模型',
  target_calls: '调用被测模型',
  calling_target: '调用被测模型',
  judgement: '判定攻击结果',
  judging: '判定攻击结果',
  save_evidence: '保存证据摘要',
  reporting: '生成报告中',
  compile_report: '生成报告中',
  completed: '评估完成',
  executed: '测试已完成',
  failed: '评估失败',
  canceled: '已取消',
  cancelled: '已取消',
}

function normalizeProgressStage(phase?: string, currentStage?: string) {
  return String(currentStage || phase || '').trim().toLowerCase()
}

function progressStageLabel(phase?: string, currentStage?: string) {
  const key = normalizeProgressStage(phase, currentStage)
  return PROGRESS_STAGE_LABELS[key] || '执行评估'
}

function stageFallbackPercent(phase?: string, currentStage?: string) {
  switch (normalizeProgressStage(phase, currentStage)) {
    case 'queued':
    case 'pending':
      return 4
    case 'starting':
      return 8
    case 'prepare_skill_input':
      return 16
    case 'run_skill':
      return 28
    case 'register_skill_payloads':
      return 36
    case 'compose_payloads':
    case 'composing_payloads':
      return 18
    case 'execute_batch':
      return 42
    case 'target_call':
    case 'target_calls':
    case 'calling_target':
    case 'executing':
    case 'running':
      return 45
    case 'judgement':
    case 'judging':
      return 72
    case 'save_evidence':
      return 82
    case 'reporting':
    case 'compile_report':
      return 92
    case 'completed':
    case 'executed':
      return 100
    default:
      return 15
  }
}

function progressPercent(phase?: string, currentStage?: string, executedCount?: number, plannedCount?: number) {
  if (phase === 'failed' || phase === 'canceled' || phase === 'cancelled') return 100
  if (phase === 'reporting' || phase === 'completed' || phase === 'executed') return Math.max(stageFallbackPercent(phase, currentStage), 100)
  if (typeof plannedCount === 'number' && plannedCount > 0 && typeof executedCount === 'number') {
    const bounded = Math.max(0, Math.min(plannedCount, executedCount))
    return Math.max(stageFallbackPercent(phase, currentStage), Math.round((bounded / plannedCount) * 100))
  }
  return stageFallbackPercent(phase, currentStage)
}

function capabilityVisual(item: WelcomeCapability) {
  switch (item.tone) {
    case 'governance':
      return { icon: <SafetyOutlined />, color: '#16a34a' }
    case 'engine':
      return { icon: <ThunderboltOutlined />, color: '#d97706' }
    case 'tool':
      return { icon: <ApiOutlined />, color: '#2563eb' }
    default:
      return { icon: <BugOutlined />, color: '#dc2626' }
  }
}

function displayCapabilityName(ref: string) {
  const text = String(ref || '').trim()
  if (!text) return ''
  const lower = text.toLowerCase()
  if (lower.includes('ccbos') || lower.includes('classical-chinese')) return 'CCBOS 文言文改写 Skill'
  if (lower.startsWith('skillhub:')) return text.replace(/^skillhub:/i, 'Hub Skill: ')
  if (lower.startsWith('sample:')) return `专家样本 ${text.slice(0, 18)}`
  if (lower.startsWith('template:')) return `专家模板 ${text.slice(0, 20)}`
  if (lower.startsWith('composed_attack:')) return `已组合攻击 ${text.slice(0, 24)}`
  return text
}

function PlanConfirmCard({
  plan,
  onConfirm,
  confirmed,
  confirmedTestCount,
}: {
  plan: PlanInfo
  onConfirm: (count: number) => void
  confirmed: boolean
  confirmedTestCount?: number
}) {
  const normalizedPlan = normalizePlanForDisplay(plan)
  const [testCount, setTestCount] = useState(confirmedTestCount ?? normalizedPlan.test_count ?? 20)

  useEffect(() => {
    if (confirmed && confirmedTestCount) {
      setTestCount(confirmedTestCount)
      return
    }
    if (!confirmed) {
      setTestCount(normalizedPlan.test_count ?? 20)
    }
  }, [confirmed, confirmedTestCount, normalizedPlan.test_count])

  const rows: [string, string][] = [
    ['任务名称', normalizedPlan.name],
    ['评估目标', normalizedPlan.goal],
    ['目标类型', normalizedPlan.target_type],
  ]
  if (normalizedPlan.target_url) rows.push(['目标地址', normalizedPlan.target_url])
  if (normalizedPlan.target_model) rows.push(['模型', normalizedPlan.target_model])
  rows.push(['资源模式', RESOURCE_MODE_LABELS[normalizedPlan.resource_mode_preference || ''] || '自动选择'])
  if (normalizedPlan.selection_strategy) rows.push(['选择策略', SELECTION_STRATEGY_LABELS[normalizedPlan.selection_strategy] || normalizedPlan.selection_strategy])
  const selectedCapabilities = [...new Set([
    ...(normalizedPlan.selected_skills || []),
    ...(normalizedPlan.selected_capability_refs || []),
    ...(normalizedPlan.resource_handles || []),
  ].map(displayCapabilityName).filter(Boolean))]

  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--color-primary-border)', borderRadius: 12, padding: '16px 20px', maxWidth: 500, marginTop: 8 }}>
      <div style={{ color: 'var(--color-primary)', fontWeight: 600, marginBottom: 12, fontSize: 14 }}>执行确认</div>
      <div style={{ display: 'grid', gap: 8, marginBottom: 16 }}>
        {rows.map(([label, value]) => (
          <div key={label} style={{ display: 'flex', gap: 8, fontSize: 13 }}>
            <span style={{ color: 'var(--text-muted)', minWidth: 70 }}>{label}</span>
            <span style={{ color: 'var(--text-primary)', wordBreak: 'break-all' }}>{value}</span>
          </div>
        ))}
        <div style={{ display: 'flex', gap: 8, fontSize: 13, flexWrap: 'wrap' }}>
          <span style={{ color: 'var(--text-muted)', minWidth: 70 }}>评估类型</span>
          <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
            {(normalizedPlan.assessment_types ?? []).map(type => (
              <span key={type} style={{ background: 'var(--color-primary-light)', color: 'var(--color-primary)', padding: '2px 8px', borderRadius: 4, fontSize: 12 }}>
                {LABELS[type] || type}
              </span>
            ))}
          </div>
        </div>
        {selectedCapabilities.length > 0 && (
          <div style={{ display: 'flex', gap: 8, fontSize: 13, alignItems: 'flex-start' }}>
            <span style={{ color: 'var(--text-muted)', minWidth: 70 }}>使用能力</span>
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              {selectedCapabilities.map(item => (
                <span key={item} style={{ background: 'rgba(22, 163, 74, 0.10)', color: 'var(--color-low)', padding: '2px 8px', borderRadius: 4, fontSize: 12 }}>
                  {item}
                </span>
              ))}
            </div>
          </div>
        )}
        {(normalizedPlan.selection_reasons || []).length > 0 && (
          <div style={{ display: 'flex', gap: 8, fontSize: 13, alignItems: 'flex-start' }}>
            <span style={{ color: 'var(--text-muted)', minWidth: 70 }}>选择理由</span>
            <span style={{ color: 'var(--text-primary)', lineHeight: 1.6 }}>
              {(normalizedPlan.selection_reasons || []).slice(0, 3).join('；')}
            </span>
          </div>
        )}
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', fontSize: 13 }}>
          <span style={{ color: 'var(--text-muted)', minWidth: 70 }}>测试问题数</span>
          <InputNumber min={1} max={2000} value={testCount} onChange={(value) => setTestCount(typeof value === 'number' ? value : 20)} disabled={confirmed} size="small" />
          <span style={{ color: 'var(--text-muted)', fontSize: 12 }}>表示本次最多执行多少条最终测试问题</span>
        </div>
      </div>
      {confirmed ? (
        <div style={{ color: 'var(--color-low)', fontSize: 13, display: 'flex', alignItems: 'center', gap: 6 }}>
          <CheckCircleOutlined /> 已确认，正在按 {testCount} 条测试问题启动评估...
        </div>
      ) : (
        <Button type="primary" size="small" onClick={() => onConfirm(testCount)}>
          确认执行
        </Button>
      )}
    </div>
  )
}

function ReportCard({
  cardType,
  assessmentId,
  reportId,
  riskLevel,
  riskScore,
  safetyScore: directSafetyScore,
  downloadFormat = 'pdf',
  downloadable = true,
  isRunning,
  statusText,
  phase,
  currentStage,
  executedCount,
  plannedCount,
  successCount,
  failureCount,
  jobId,
  recoveryAction,
  recoveryStrategy,
  restartInterrupted,
  canResume,
  resumeStarted,
  resumeJobId,
  resuming,
  onResumeJob,
  retryStarted,
  retryJobId,
  retrying,
  onRetryJob,
}: {
  cardType?: string
  assessmentId?: string
  reportId?: string
  riskLevel?: string
  riskScore?: number
  safetyScore?: number
  downloadFormat?: 'pdf' | 'markdown' | 'json'
  downloadable?: boolean
  isRunning?: boolean
  statusText?: string
  phase?: string
  currentStage?: string
  executedCount?: number
  plannedCount?: number
  successCount?: number
  failureCount?: number
  jobId?: string
  recoveryAction?: string
  recoveryStrategy?: string
  restartInterrupted?: boolean
  canResume?: boolean
  resumeStarted?: boolean
  resumeJobId?: string
  resuming?: boolean
  onResumeJob?: (jobId: string) => void
  retryStarted?: boolean
  retryJobId?: string
  retrying?: boolean
  onRetryJob?: (jobId: string) => void
}) {
  const [downloading, setDownloading] = useState(false)
  const [findingsOpen, setFindingsOpen] = useState(false)
  const [findingsLoading, setFindingsLoading] = useState(false)
  const [successfulFindings, setSuccessfulFindings] = useState<Array<{ title?: string; severity?: string; category?: string; description?: string }> | null>(null)
  const safetyScore = resolveReportSafetyScore({
    cardType,
    directSafetyScore,
    riskScore,
  })

  // 攻击成功摘要：按需拉取报告详情，仅展示后端已脱敏的成功项摘要。
  // 完整攻击样例与目标回答仍在 PDF 报告内，这里只做安全摘要层。
  const toggleFindings = async () => {
    const next = !findingsOpen
    setFindingsOpen(next)
    if (!next || !reportId || successfulFindings || findingsLoading) return
    setFindingsLoading(true)
    try {
      const report = await reportService.getMaclawReport(reportId)
      const successful = (report.findings || []).filter(finding => {
        const judgeResult = String((finding as { metadata?: Record<string, string> }).metadata?.judge_result || '').toLowerCase()
        const severity = String(finding.severity || '').toLowerCase()
        const isFailureByJudge = judgeResult === 'failure' || judgeResult === 'fail'
        const looksSuccessful = severity === 'high' || severity === 'critical' || severity === '高' || judgeResult === 'success'
        return looksSuccessful && !isFailureByJudge
      })
      setSuccessfulFindings(successful.slice(0, Math.max(successCount ?? 0, 0) || 5).map(finding => ({
        title: finding.title,
        severity: finding.severity,
        category: finding.category,
        description: finding.description,
      })))
    } catch {
      setSuccessfulFindings([])
    } finally {
      setFindingsLoading(false)
    }
  }

  const handleDownload = async () => {
    if (!reportId) return
    setDownloading(true)
    try {
      const blob = await reportService.downloadMaclawReport(reportId, downloadFormat)
      const ext = downloadFormat === 'json' ? 'json' : downloadFormat === 'markdown' ? 'md' : 'pdf'
      const fallbackType = downloadFormat === 'json' ? 'application/json' : downloadFormat === 'markdown' ? 'text/markdown;charset=utf-8' : 'application/pdf'
      triggerBrowserDownload(blob, `maclaw-report-${reportId}.${ext}`, fallbackType)
    } catch {
      message.error('PDF 下载失败，请稍后重试')
    } finally {
      setDownloading(false)
    }
  }

  if (cardType === 'report') {
    const normalizedRiskLevel = (riskLevel || '').trim()
    const normalizedRiskKey = normalizedRiskLevel.toLowerCase()
    const color = RISK_COLORS[normalizedRiskLevel] || RISK_COLORS[normalizedRiskKey] || '#16a34a'
    const label = RISK_LABELS[normalizedRiskLevel] || RISK_LABELS[normalizedRiskKey] || riskLevel || '最高安全'
    const resolvedFailureCount = typeof failureCount === 'number'
      ? failureCount
      : typeof executedCount === 'number' && typeof successCount === 'number'
        ? Math.max(0, executedCount - successCount)
        : undefined
    const stats = [
      ['执行', executedCount],
      ['成功', successCount],
      ['失败', resolvedFailureCount],
    ].filter(([, value]) => typeof value === 'number')
    return (
      <div style={{ background: 'var(--bg-card)', border: `1px solid ${color}40`, borderRadius: 12, padding: '16px 20px', maxWidth: 420, marginTop: 8 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12 }}>
          <CheckCircleOutlined style={{ color: 'var(--color-low)', fontSize: 18 }} />
          <span style={{ color: 'var(--text-primary)', fontSize: 14, fontWeight: 600 }}>评估已完成</span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
          <span style={{ background: color, color: '#fff', padding: '2px 10px', borderRadius: 4, fontSize: 12, fontWeight: 500 }}>{label}</span>
          <span style={{ color: 'var(--text-primary)', fontSize: 20, fontWeight: 700 }}>
            {typeof safetyScore === 'number' ? safetyScore : '--'}
            <span style={{ fontSize: 12, color: 'var(--text-muted)', marginLeft: 2 }}>安全分</span>
          </span>
        </div>
        {stats.length > 0 && (
          <div style={{ display: 'grid', gridTemplateColumns: `repeat(${stats.length}, minmax(0, 1fr))`, gap: 6, marginBottom: 12 }}>
            {stats.map(([labelText, value]) => (
              <div key={labelText as string} style={{ border: '1px solid var(--border-color)', borderRadius: 6, padding: '6px 4px', textAlign: 'center', background: '#f8fafc' }}>
                <div style={{ color: 'var(--text-primary)', fontSize: 14, fontWeight: 700 }}>{value as number}</div>
                <div style={{ color: 'var(--text-muted)', fontSize: 11, marginTop: 2 }}>{labelText as string}</div>
              </div>
            ))}
          </div>
        )}
        {(assessmentId || reportId) && downloadable && (
          <Button type="primary" icon={<DownloadOutlined />} size="small" loading={downloading} onClick={handleDownload}>
            {reportId ? '下载报告' : '下载 PDF 报告'}
          </Button>
        )}
        {reportId && (
          <div style={{ marginTop: 12 }}>
            <Button type="link" size="small" style={{ padding: 0, color: 'var(--color-primary)' }} onClick={() => { void toggleFindings() }}>
              {findingsOpen ? '收起攻击成功摘要' : '展开攻击成功摘要（脱敏）'}
            </Button>
            {findingsOpen && (
              <div style={{ marginTop: 8 }}>
                {findingsLoading ? (
                  <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>加载中...</div>
                ) : successfulFindings && successfulFindings.length > 0 ? (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    {successfulFindings.map((finding, index) => (
                      <div key={index} style={{ border: '1px solid var(--border-color)', borderRadius: 8, padding: '8px 12px', background: '#fffbeb' }}>
                        <div style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap', marginBottom: 4 }}>
                          <span style={{ color: 'var(--text-primary)', fontSize: 12, fontWeight: 600 }}>#{index + 1}</span>
                          {finding.category && <span style={{ fontSize: 11, color: 'var(--text-muted)', border: '1px solid var(--border-color)', borderRadius: 4, padding: '0 6px' }}>{finding.category}</span>}
                          {finding.severity && <span style={{ fontSize: 11, color: '#b45309', border: '1px solid #f59e0b66', borderRadius: 4, padding: '0 6px' }}>{finding.severity}</span>}
                        </div>
                        {finding.description && (
                          <div style={{ color: 'var(--text-secondary, #475569)', fontSize: 12, lineHeight: 1.6 }}>{finding.description}</div>
                        )}
                      </div>
                    ))}
                  </div>
                ) : (
                  <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>未发现攻击成功（与报告“最高安全”一致）</div>
                )}
                <div style={{ color: 'var(--text-muted)', fontSize: 11, marginTop: 6 }}>以上为脱敏摘要，完整攻击样例与目标回答见 PDF 报告。</div>
              </div>
            )}
          </div>
        )}
      </div>
    )
  }

  if (cardType === 'progress') {
    const isFailed = phase === 'failed' || phase === 'canceled'
    const canResumeJob = isFailed && Boolean(jobId) && (canResume || recoveryAction === 'resume') && !resumeStarted
    const canRetry = isFailed && Boolean(jobId) && recoveryAction === 'retry' && !retryStarted && !resumeStarted
    const needsReview = isFailed && Boolean(restartInterrupted) && recoveryAction !== 'retry' && recoveryAction !== 'resume'
    const retryStartedText = retryJobId ? `已发起重试任务：${retryJobId}` : '已发起重试任务。'
    const resumeStartedText = resumeJobId ? `已发起恢复任务：${resumeJobId}` : '已发起恢复任务。'
    const phaseLabel = phase === 'failed'
      ? '评估任务未完成'
      : phase === 'canceled'
        ? '评估任务已取消'
        : phase === 'reporting'
          ? '正在生成报告'
          : phase === 'executed'
            ? '测试已完成'
            : phase === 'starting'
              ? '初始化评估'
              : '正在执行测试'
    const showSpinner = isRunning && phase !== 'executed' && !isFailed
    const borderColor = isFailed ? 'rgba(220,38,38,0.35)' : 'rgba(22,163,74,0.3)'
    const stageLabel = progressStageLabel(phase, currentStage)
    const percent = progressPercent(phase, currentStage, executedCount, plannedCount)
    const progressStatus = isFailed ? 'exception' : percent >= 100 ? 'success' : 'active'
    const countLabel = typeof plannedCount === 'number' && plannedCount > 0
      ? `执行轮次 ${typeof executedCount === 'number' ? Math.max(0, Math.min(plannedCount, executedCount)) : 0}/${plannedCount}`
      : stageLabel
    return (
      <div style={{ background: 'var(--bg-card)', border: `1px solid ${borderColor}`, borderRadius: 12, padding: '14px 18px', maxWidth: 420, marginTop: 8 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 10 }}>
          {showSpinner ? <Spin indicator={<LoadingOutlined style={{ color: 'var(--color-primary)' }} />} /> : <CheckCircleOutlined style={{ color: isFailed ? 'var(--color-critical)' : 'var(--color-low)', fontSize: 18 }} />}
          <div>
            <div style={{ color: 'var(--text-primary)', fontSize: 14, fontWeight: 600 }}>{phaseLabel}</div>
            <div style={{ color: 'var(--text-secondary)', fontSize: 13 }}>{statusText || '评估任务执行中，请稍候...'}</div>
          </div>
        </div>
        <div style={{ display: 'grid', gap: 6 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, color: 'var(--text-muted)', fontSize: 12 }}>
            <span style={{ color: 'var(--color-primary)', fontWeight: 500 }}>{countLabel}</span>
            <span>{stageLabel}</span>
          </div>
          <Progress percent={percent} size="small" status={progressStatus} showInfo={false} strokeColor={isFailed ? 'var(--color-critical)' : 'var(--color-primary)'} trailColor="#eef2f7" />
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--text-muted)', fontSize: 12 }}>
            {phase === 'reporting' && <span>测试已完成，报告生成后会自动提供下载卡片。</span>}
          </div>
        </div>
        {isFailed && (canResumeJob || canRetry || needsReview || retryStarted || resumeStarted) && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 12, flexWrap: 'wrap' }}>
            {(canResumeJob || resumeStarted) && (
              <span style={{ color: 'var(--text-muted)', fontSize: 12 }}>
                {resumeStarted ? resumeStartedText : '该任务可继续恢复。'}
              </span>
            )}
            {!(canResumeJob || resumeStarted) && (
            <span style={{ color: 'var(--text-muted)', fontSize: 12 }}>
              {retryStarted ? retryStartedText : canRetry ? '该任务可安全重试。' : `需要人工复核${recoveryStrategy ? `：${recoveryStrategy}` : '。'}`}
            </span>
            )}
            {canResumeJob && jobId && (
              <Button size="small" type="primary" icon={<PlayCircleOutlined />} loading={resuming} onClick={() => onResumeJob?.(jobId)}>
                继续恢复
              </Button>
            )}
            {canRetry && jobId && (
              <Button size="small" type="primary" icon={<ReloadOutlined />} loading={retrying} onClick={() => onRetryJob?.(jobId)}>
                重试
              </Button>
            )}
          </div>
        )}
      </div>
    )
  }

  return null
}

function SkillLaunchCard({ skillName, openUrl }: { skillName?: string; openUrl?: string }) {
  if (!openUrl) return null
  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--color-primary-border)', borderRadius: 12, padding: '16px 20px', maxWidth: 500, marginTop: 8 }}>
      <div style={{ color: 'var(--color-primary)', fontWeight: 600, marginBottom: 8, fontSize: 14 }}>互动 Skill</div>
      <div style={{ color: 'var(--text-secondary)', fontSize: 13, lineHeight: 1.7, marginBottom: 14 }}>
        {skillName ? `已匹配到「${skillName}」页面工具。` : '已匹配到一个可直接打开的页面工具。'}
        点击下面按钮会在新标签页打开，不会启动评测流程。
      </div>
      <Button type="primary" icon={<ExportOutlined />} size="small" onClick={() => window.open(openUrl, '_blank', 'noopener,noreferrer')}>
        新标签页打开
      </Button>
    </div>
  )
}

function renderMarkdownInline(parts: ChatMarkdownInline[], keyPrefix: string) {
  return parts.map((part, index) => {
    const key = `${keyPrefix}-${index}`
    if (part.code) {
      return (
        <code key={key} style={{ background: '#f1f5f9', border: '1px solid #e2e8f0', borderRadius: 4, padding: '1px 5px', fontSize: '0.92em' }}>
          {part.text}
        </code>
      )
    }
    if (part.strong) return <strong key={key}>{part.text}</strong>
    if (part.emphasis) return <em key={key}>{part.text}</em>
    return <span key={key}>{part.text}</span>
  })
}

function ChatMarkdown({ content, enabled }: { content: string; enabled: boolean }) {
  if (!enabled) return <>{content}</>
  const blocks = parseChatMarkdown(content)
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      {blocks.map((block, index) => {
        const key = `md-${index}`
        if (block.type === 'heading') {
          const fontSize = block.level === 1 ? 17 : block.level === 2 ? 16 : 15
          return (
            <div key={key} style={{ color: 'var(--text-primary)', fontSize, fontWeight: 700, lineHeight: 1.45, marginTop: index === 0 ? 0 : 4 }}>
              {renderMarkdownInline(block.children, key)}
            </div>
          )
        }
        if (block.type === 'list') {
          return (
            <ul key={key} style={{ margin: 0, paddingLeft: 20, display: 'grid', gap: 4 }}>
              {block.items.map((item, itemIndex) => (
                <li key={`${key}-${itemIndex}`} style={{ paddingLeft: 2 }}>
                  {renderMarkdownInline(item, `${key}-${itemIndex}`)}
                </li>
              ))}
            </ul>
          )
        }
        if (block.type === 'code_block') {
          return (
            <pre key={key} style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word', background: '#f1f5f9', border: '1px solid #e2e8f0', borderRadius: 8, padding: '8px 10px', fontSize: 12, lineHeight: 1.6 }}>
              <code>{block.text}</code>
            </pre>
          )
        }
        return (
          <div key={key} style={{ whiteSpace: 'pre-wrap' }}>
            {renderMarkdownInline(block.children, key)}
          </div>
        )
      })}
    </div>
  )
}

export function WelcomePanel({ items, onQuickPrompt }: { items: WelcomeCapability[]; onQuickPrompt: (text: string) => void }) {
  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 32, minHeight: '100%' }}>
      <div style={{ textAlign: 'center' }}>
        <div style={{ width: 64, height: 64, borderRadius: 16, background: 'var(--color-primary-light)', border: '1px solid var(--color-primary-border)', display: 'flex', alignItems: 'center', justifyContent: 'center', margin: '0 auto 16px' }}>
          <RobotOutlined style={{ fontSize: 32, color: 'var(--color-primary)' }} />
        </div>
        <div style={{ color: 'var(--text-primary)', fontSize: 22, fontWeight: 600 }}>AI 安全评估助手</div>
        <div style={{ color: 'var(--text-muted)', fontSize: 14, marginTop: 8 }}>描述你的评估需求，我来帮你制定评估方案</div>
      </div>
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', justifyContent: 'center', maxWidth: 560 }}>
        {items.map(item => {
          const visual = capabilityVisual(item)
          return (
            <div
              key={item.id}
              title={item.description || item.label}
              onClick={() => onQuickPrompt(item.prompt)}
              style={{ padding: '12px 18px', background: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: 10, cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: 'var(--text-primary)', transition: 'border-color 0.2s' }}
              onMouseEnter={(event) => { event.currentTarget.style.borderColor = visual.color }}
              onMouseLeave={(event) => { event.currentTarget.style.borderColor = 'var(--border-color)' }}
            >
              <span style={{ color: visual.color }}>{visual.icon}</span>
              {item.label}
            </div>
          )
        })}
      </div>
    </div>
  )
}

export function TypingIndicator() {
  return (
    <div style={{ display: 'flex', gap: 10, marginBottom: 20 }}>
      <Avatar size={34} icon={<RobotOutlined />} style={{ background: 'rgba(22,163,74,0.15)', border: '1px solid rgba(22,163,74,0.4)', flexShrink: 0 }} />
      <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: '4px 12px 12px 12px', padding: '12px 16px', display: 'flex', gap: 4, alignItems: 'center' }}>
        {[0, 1, 2].map(index => (
          <div key={index} style={{ width: 6, height: 6, borderRadius: '50%', background: 'var(--color-primary)', animation: `bounce 1.2s ${index * 0.2}s infinite` }} />
        ))}
      </div>
    </div>
  )
}

export function MessageBubble({
  msg,
  onPlanConfirm,
  onQuickReply,
  confirmedMsgs,
  confirmedTestCounts,
  isRunning,
  onResumeJob,
  resumingJobIds,
  onRetryJob,
  retryingJobIds,
}: {
  msg: ChatMessage
  onPlanConfirm: (count: number, planMessageId: string) => void
  onQuickReply?: (text: string) => void
  confirmedMsgs: Set<string>
  confirmedTestCounts: Record<string, number>
  isRunning: boolean
  onResumeJob: (jobId: string, sessionId: string) => void
  resumingJobIds: Set<string>
  onRetryJob: (jobId: string, sessionId: string) => void
  retryingJobIds: Set<string>
}) {
  const isUser = msg.role === 'user'
  const cardType = msg.metadata?.card_type
  const isStalePlan = msg.metadata?.plan_stale === true || msg.metadata?.plan_stale === 'true'
  const hideTextBubble = !isUser && (cardType === 'plan_confirm' || cardType === 'progress' || cardType === 'report' || cardType === 'skill_launch')
  const askUserOptions = (() => {
    const raw = msg.metadata?.ask_user_options_json
    if (typeof raw !== 'string' || !raw.trim()) return []
    try {
      const parsed = JSON.parse(raw)
      return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string' && item.trim().length > 0) : []
    } catch {
      return []
    }
  })()

  return (
    <div style={{ display: 'flex', flexDirection: isUser ? 'row-reverse' : 'row', gap: 10, marginBottom: 20, alignItems: 'flex-start' }}>
      <Avatar size={34} icon={isUser ? <UserOutlined /> : <RobotOutlined />} style={{ background: isUser ? 'var(--color-primary-light)' : 'rgba(22,163,74,0.15)', border: `1px solid ${isUser ? 'var(--color-primary-border)' : 'rgba(22,163,74,0.4)'}`, flexShrink: 0 }} />
      <div style={{ maxWidth: '72%' }}>
        {msg.content && !hideTextBubble && (
          <div style={{ background: isUser ? 'var(--color-primary-light)' : 'var(--bg-card)', border: `1px solid ${isUser ? 'var(--color-primary-border)' : 'var(--border-color)'}`, borderRadius: isUser ? '12px 4px 12px 12px' : '4px 12px 12px 12px', padding: '10px 14px', color: 'var(--text-primary)', fontSize: 14, lineHeight: 1.6, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
            <ChatMarkdown content={msg.content} enabled={!isUser} />
          </div>
        )}
        {cardType === 'plan_confirm' && !isUser && msg.metadata?.plan && (
          <PlanConfirmCard
            plan={msg.metadata.plan as PlanInfo}
            onConfirm={(count) => onPlanConfirm(count, msg.id)}
            confirmed={confirmedMsgs.has(msg.id) || isStalePlan}
            confirmedTestCount={confirmedTestCounts[msg.id]}
          />
        )}
        {msg.metadata?.response_source === 'ask_user' && askUserOptions.length > 0 && !isUser && (
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 8 }}>
            {askUserOptions.map(option => (
              <Button key={option} size="small" onClick={() => onQuickReply?.(option)} disabled={isRunning}>
                {option}
              </Button>
            ))}
          </div>
        )}
        {(cardType === 'progress' || cardType === 'report') && !isUser && (
          <ReportCard
            cardType={cardType}
            assessmentId={msg.metadata?.assessment_id as string | undefined}
            reportId={msg.metadata?.report_id as string | undefined}
            riskLevel={msg.metadata?.risk_level as string | undefined}
            riskScore={msg.metadata?.risk_score as number | undefined}
            safetyScore={msg.metadata?.safety_score as number | undefined}
            downloadFormat={msg.metadata?.download_format as 'pdf' | 'markdown' | 'json' | undefined}
            downloadable={msg.metadata?.downloadable as boolean | undefined}
            statusText={msg.metadata?.status_text as string | undefined}
            phase={msg.metadata?.phase as string | undefined}
            currentStage={msg.metadata?.current_stage as string | undefined}
            executedCount={msg.metadata?.executed_count as number | undefined}
            plannedCount={msg.metadata?.planned_count as number | undefined}
            successCount={msg.metadata?.success_count as number | undefined}
            failureCount={msg.metadata?.failure_count as number | undefined}
            isRunning={isRunning}
            jobId={msg.metadata?.job_id as string | undefined}
            recoveryAction={msg.metadata?.recovery_action as string | undefined}
            recoveryStrategy={msg.metadata?.recovery_strategy as string | undefined}
            restartInterrupted={msg.metadata?.restart_interrupted as boolean | undefined}
            canResume={msg.metadata?.can_resume as boolean | undefined}
            resumeStarted={msg.metadata?.resume_started as boolean | undefined}
            resumeJobId={msg.metadata?.resume_job_id as string | undefined}
            resuming={resumingJobIds.has(msg.metadata?.job_id as string)}
            onResumeJob={(jobId) => onResumeJob(jobId, msg.session_id)}
            retryStarted={msg.metadata?.retry_started as boolean | undefined}
            retryJobId={msg.metadata?.retry_job_id as string | undefined}
            retrying={retryingJobIds.has(msg.metadata?.job_id as string)}
            onRetryJob={(jobId) => onRetryJob(jobId, msg.session_id)}
          />
        )}
        {cardType === 'skill_launch' && !isUser && (
          <SkillLaunchCard skillName={msg.metadata?.skill_name as string | undefined} openUrl={msg.metadata?.open_url as string | undefined} />
        )}
        <div style={{ color: 'var(--text-muted)', fontSize: 11, marginTop: 4, textAlign: isUser ? 'right' : 'left' }}>
          {new Date(msg.created_at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}
        </div>
      </div>
    </div>
  )
}