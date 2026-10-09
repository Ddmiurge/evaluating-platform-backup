import api from './api'
import type { ChatMessage, ChatSession, PlanInfo, WelcomeCapability } from './chat'
import { parseRuntimePlan } from './maclawRuntimePlan'

export { isPlanConfirmRun, shouldStreamRuntimeRun } from './runtimeRunPolicy'

const PLAN_OUTPUT_TYPE = 'application/vnd.maclaw.plan-confirm+json'
const REPORT_OUTPUT_TYPE = 'application/vnd.maclaw.evaluation-report+json'

interface RuntimeSession {
  id: string
  tenant_id?: string
  user_id?: string
  instance_id?: string
  agent_id?: string
  title?: string
  metadata?: Record<string, string>
  archived?: boolean
  waiting_for_user?: boolean
  last_message_at?: string
  created_at?: string
  updated_at?: string
}

interface RuntimeMessage {
  id: string
  session_id?: string
  role: string
  input_type?: string
  output_type?: string
  content?: string
  metadata?: Record<string, string>
  created_at?: string
}

export interface RuntimeRun {
  id: string
  session_id?: string
  status: string
  error?: string
  response_source?: string
  waiting_for_user?: boolean
  metadata?: Record<string, string>
}

interface RuntimeMessageResponse {
  run?: RuntimeRun
  message?: RuntimeMessage
  error?: string
}

export interface EvaluationJob {
  id: string
  kind: 'evaluation.run' | string
  status: 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled' | string
  progress?: {
    phase?: string
    step?: string
    step_status?: string
    retryable_on_restart?: boolean
    steps?: Array<{
      step: string
      status: string
      retryable_on_restart?: boolean
      started_at?: string
      completed_at?: string
    }>
    restart_interrupted?: boolean
    recovery_strategy?: string
    recovery_action?: string
    can_resume?: boolean
    resume_step?: string
    resume_job_endpoint?: string
    run_id?: string
    instance_id?: string
    session_id?: string
    status_text?: string
    planned_count?: number
    executed_count?: number
    current_stage?: string
    duration_ms?: number
    stage_durations_json?: string
    updated_at?: string
  }
  result?: RuntimeMessageResponse
  error?: string
  created_at?: string
  started_at?: string
  completed_at?: string
}

export interface EvaluationTarget {
  id: string
  name: string
  kind: string
  provider?: string
  base_url?: string
  model?: string
  auth_type?: string
  status?: string
  health_status?: string
  credential_secret_set?: boolean
  metadata?: Record<string, string>
}

export interface EvaluationTargetInput {
  name: string
  kind: string
  provider?: string
  base_url?: string
  model?: string
  auth_type?: string
  credential_secret?: string
  status?: string
  metadata?: Record<string, string>
}

export interface EvaluationTargetProbeResult {
  target: EvaluationTarget
  status: string
  message?: string
  error?: string
}

export interface EvaluationJobRecovery {
  job_id: string
  run_id?: string
  action?: string
  strategy?: string
  step?: string
  completed_step_ids?: string[]
  can_resume?: boolean
  resume_step?: string
  resume_job_endpoint?: string
  can_retry?: boolean
  manual_review_required?: boolean
  reason?: string
  recovery_indexed?: boolean
  replacement_job_endpoint?: string
}

interface RuntimeSessionSnapshot {
  session: RuntimeSession
  messages: RuntimeMessage[]
}

export interface RuntimeSendResult {
  message: ChatMessage
  run?: RuntimeRun
  job?: EvaluationJob
}

interface EvaluationRunEvent {
  type: 'plan_confirm' | 'progress' | 'tool_call' | 'evidence' | 'report' | 'error' | 'cancelled'
  run_id?: string
  session_id?: string
  status?: string
  occurred_at?: string
  message?: string
  phase?: string
  status_text?: string
  planned_count?: number
  executed_count?: number
  current_stage?: string
  plan_confirm?: {
    message_id?: string
    output_type?: string
    summary?: string
    card?: unknown
  }
  report?: {
    message_id?: string
    report_id?: string
    summary?: string
    safety_score?: number
    risk_level?: string
    executed_count?: number
    success_count?: number
    failure_count?: number
  }
  tool_call?: {
    message_id?: string
    tool_name?: string
    summary?: string
  }
  evidence?: {
    message_id?: string
    evidence_id?: string
    kind?: string
    handle?: string
    summary?: string
  }
  error?: string
}

interface RuntimeStreamEnvelope {
  type?: string
  snapshot?: {
    run?: RuntimeRun
    session?: RuntimeSession
    assistant_message?: RuntimeMessage
    evaluation_event?: EvaluationRunEvent
  }
}

function apiURL(path: string) {
  const baseURL = String(api.defaults.baseURL || '/api/v1').replace(/\/$/, '')
  return `${baseURL}${path}`
}

function nowISO() {
  return new Date().toISOString()
}

function optionalNumber(value: unknown) {
  if (typeof value === 'number' && !Number.isNaN(value)) return value
  if (typeof value === 'string' && value.trim()) {
    const parsed = Number(value)
    if (!Number.isNaN(parsed)) return parsed
  }
  return undefined
}

function parsePlan(value: unknown): PlanInfo | undefined {
  return parseRuntimePlan(value)
}

function parseRuntimeJSON(value: string): Record<string, unknown> | undefined {
  const trimmed = value.trim()
  if (!trimmed) return undefined
  const candidates = [trimmed]
  const embeddedFence = trimmed.match(/```(?:json)?\s*([\s\S]*?)```/i)
  if (embeddedFence?.[1]?.trim()) {
    candidates.unshift(embeddedFence[1].trim())
  }
  if (trimmed.startsWith('```')) {
    const lines = trimmed.split('\n')
    let end = -1
    for (let index = lines.length - 1; index > 0; index -= 1) {
      if (lines[index].trim().startsWith('```')) {
        end = index
        break
      }
    }
    if (end > 1) {
      candidates.unshift(lines.slice(1, end).join('\n').trim())
    }
  }
  for (const candidate of candidates) {
    try {
      const parsed = JSON.parse(candidate) as unknown
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>
      }
    } catch {
      // Ignore non-JSON assistant text.
    }
  }
  return undefined
}

function runtimeSessionState(session: RuntimeSession): ChatSession['state'] {
  if (session.metadata?.pending_ask_user === 'true') return 'collecting_intent'
  if (
    session.waiting_for_user &&
    (session.metadata?.pending_plan_confirm === 'true' ||
      session.metadata?.response_source === 'plan_confirm' ||
      session.metadata?.evaluation_event_type === 'plan_confirm')
  ) {
    return 'confirming_plan'
  }
  const state = session.metadata?.evaluation_state
  if (state === 'running' || state === 'completed' || state === 'failed') return state
  return session.last_message_at ? 'collecting_intent' : 'idle'
}

function toChatSession(session: RuntimeSession): ChatSession {
  const createdAt = session.created_at || nowISO()
  const updatedAt = session.updated_at || session.last_message_at || createdAt
  return {
    id: session.id,
    user_id: session.user_id || '',
    title: session.title || '新评测会话',
    state: runtimeSessionState(session),
    assessment_id: session.metadata?.assessment_id,
    target_info: {},
    plan_info: {},
    created_at: createdAt,
    updated_at: updatedAt,
  }
}

function runtimeRole(role: string): ChatMessage['role'] {
  if (role === 'user') return 'user'
  if (role === 'system') return 'system'
  if (role === 'tool' || role === 'tool_event') return 'tool_event'
  return 'assistant'
}

function toChatMessage(message: RuntimeMessage): ChatMessage {
  const metadata: ChatMessage['metadata'] = { ...(message.metadata || {}) }
  const eventType = message.metadata?.evaluation_event_type
  let content = message.content || ''
  const runtimeJSON = parseRuntimeJSON(content)
  if (runtimeJSON?.response_source === 'ask_user') {
    metadata.response_source = 'ask_user'
    metadata.evaluation_event_type = 'ask_user'
    const question = runtimeJSON.question || runtimeJSON.message || runtimeJSON.content
    if (typeof question === 'string' && question.trim()) {
      metadata.ask_user_question = question
      content = question
    } else {
      content = '需要补充更多信息，请继续描述你的需求。'
    }
    if (Array.isArray(runtimeJSON.options)) {
      metadata.ask_user_options_json = JSON.stringify(runtimeJSON.options.filter(item => typeof item === 'string' && item.trim()))
    }
  }
  const isPlanConfirm = message.output_type === PLAN_OUTPUT_TYPE || eventType === 'plan_confirm' || runtimeJSON?.response_source === 'plan_confirm'
  const isReport = message.output_type === REPORT_OUTPUT_TYPE || Boolean(message.metadata?.report_id)
  if (isPlanConfirm) {
    const plan = parsePlan(content)
    metadata.card_type = 'plan_confirm'
    if (plan) {
      metadata.plan = plan
      content = '请确认下面的执行计划。'
    }
  }
  if (isReport) {
    metadata.card_type = 'report'
    metadata.report_id = message.metadata?.report_id
    metadata.report_source = 'maclaw'
    metadata.summary = message.content || message.metadata?.summary || 'Evaluation completed.'
    metadata.risk_level = message.metadata?.risk_level
    metadata.safety_score = optionalNumber(message.metadata?.safety_score)
    metadata.executed_count = optionalNumber(message.metadata?.executed_count || message.metadata?.total_cases)
    metadata.planned_count = optionalNumber(message.metadata?.planned_count || message.metadata?.total_cases)
    metadata.success_count = optionalNumber(message.metadata?.success_count)
    metadata.failure_count = optionalNumber(message.metadata?.failure_count)
    metadata.download_format = 'pdf'
    metadata.downloadable = Boolean(message.metadata?.report_id)
    content = ''
  }
  return {
    id: message.id,
    session_id: message.session_id || '',
    role: runtimeRole(message.role),
    content,
    metadata,
    created_at: message.created_at || nowISO(),
  }
}

export function sanitizeDirectMessage(message: ChatMessage, sessionId: string): ChatMessage {
  const eventType = String(message.metadata?.evaluation_event_type || '').toLowerCase()
  const cardType = message.metadata?.card_type
  const looksLikeProgress = cardType === 'progress' || eventType === 'progress' || eventType === 'tool_call'
  if (!looksLikeProgress) {
    return message
  }

  const {
    card_type: _cardType,
    phase: _phase,
    status_text: _statusText,
    job_id: _jobId,
    assessment_id: _assessmentId,
    evaluation_event_type: _eventType,
    progress_phase: _progressPhase,
    steps: _steps,
    ...safeMetadata
  } = message.metadata

  return {
    ...message,
    session_id: message.session_id || sessionId,
    content: message.content?.trim() || '正在生成回复，请稍候。',
    metadata: {
      ...safeMetadata,
      card_type: 'text',
      response_source: safeMetadata.response_source || 'chat',
    },
  }
}

function eventProgressPhase(event: EvaluationRunEvent) {
  if (event.status === 'queued' || event.status === 'pending') return 'starting'
  if (event.status === 'succeeded') return 'reporting'
  if (event.status === 'failed' || event.status === 'cancelled') return 'executed'
  return 'executing'
}

function toEventChatMessage(envelope: RuntimeStreamEnvelope): ChatMessage | null {
  const event = envelope.snapshot?.evaluation_event
  if (!event) return null
  const runID = event.run_id || envelope.snapshot?.run?.id || ''
  const sessionID = event.session_id || envelope.snapshot?.run?.session_id || envelope.snapshot?.session?.id || ''
  const createdAt = event.occurred_at || nowISO()
  const baseID = `${event.type}:${runID}:${event.status || ''}:${createdAt}`

  if (event.type === 'plan_confirm') {
    const plan = parsePlan(event.plan_confirm?.card) || parsePlan(envelope.snapshot?.assistant_message?.content || '')
    return {
      id: event.plan_confirm?.message_id || `maclaw-event:${baseID}`,
      session_id: sessionID,
      role: 'assistant',
      content: event.plan_confirm?.summary || '请确认下面的执行计划。',
      metadata: {
        card_type: 'plan_confirm',
        plan,
      },
      created_at: createdAt,
    }
  }

  if (event.type === 'report') {
    return {
      id: event.report?.message_id || `maclaw-event:${baseID}`,
      session_id: sessionID,
      role: 'assistant',
      content: '',
      metadata: {
        card_type: 'report',
        assessment_id: runID,
        report_id: event.report?.report_id,
        report_source: 'maclaw',
        summary: event.report?.summary || event.message || '评测已完成。',
        risk_level: event.report?.risk_level,
        safety_score: event.report?.safety_score,
        executed_count: event.report?.executed_count,
        planned_count: event.planned_count,
        success_count: event.report?.success_count,
        failure_count: event.report?.failure_count,
        download_format: 'pdf',
        downloadable: Boolean(event.report?.report_id),
      },
      created_at: createdAt,
    }
  }

  if (event.type === 'tool_call') {
    return {
      id: event.tool_call?.message_id || `maclaw-event:${baseID}`,
      session_id: sessionID,
      role: 'assistant',
      content: event.tool_call?.summary || `正在调用 ${event.tool_call?.tool_name || '工具'}。`,
      metadata: { card_type: 'text', tool_name: event.tool_call?.tool_name },
      created_at: createdAt,
    }
  }

  if (event.type === 'evidence') {
    return {
      id: event.evidence?.message_id || `maclaw-event:${baseID}`,
      session_id: sessionID,
      role: 'assistant',
      content: event.evidence?.summary || '已记录执行证据。',
      metadata: {
        card_type: 'text',
        evidence_id: event.evidence?.evidence_id,
        evidence_kind: event.evidence?.kind,
        evidence_handle: event.evidence?.handle,
      },
      created_at: createdAt,
    }
  }

  return {
    id: `maclaw-event:${baseID}`,
    session_id: sessionID,
    role: 'assistant',
    content: '',
    metadata: {
      card_type: 'progress',
      assessment_id: runID,
      phase: event.phase || envelope.snapshot?.run?.metadata?.phase || eventProgressPhase(event),
      status_text: event.status_text || event.error || event.message || envelope.snapshot?.run?.metadata?.status_text || envelope.snapshot?.run?.error || '评测任务执行中，请稍候。',
      planned_count: optionalNumber(event.planned_count ?? envelope.snapshot?.run?.metadata?.planned_count ?? envelope.snapshot?.run?.metadata?.total_count ?? envelope.snapshot?.run?.metadata?.test_count),
      executed_count: optionalNumber(event.executed_count ?? envelope.snapshot?.run?.metadata?.executed_count ?? envelope.snapshot?.run?.metadata?.completed_count),
      current_stage: event.current_stage || envelope.snapshot?.run?.metadata?.current_stage || envelope.snapshot?.run?.metadata?.progress_phase,
    },
    created_at: createdAt,
  }
}

function isTerminalEnvelope(envelope: RuntimeStreamEnvelope) {
  const status = envelope.snapshot?.run?.status
  const type = envelope.snapshot?.evaluation_event?.type
  return envelope.type === 'done' || status === 'succeeded' || status === 'failed' || status === 'cancelled' || type === 'report' || type === 'error' || type === 'cancelled'
}

function parseSSEBlock(block: string): RuntimeStreamEnvelope | null {
  const data = block
    .split(/\r?\n/)
    .filter(line => line.startsWith('data:'))
    .map(line => line.slice(5).trimStart())
    .join('\n')
  if (!data.trim()) return null
  return JSON.parse(data) as RuntimeStreamEnvelope
}

export const maclawRuntimeChatService = {
  async createSession(): Promise<ChatSession> {
    const res = await api.post<RuntimeSession>('/maclaw/evaluation/sessions', {
      title: '企业评测会话',
      metadata: { source: 'evaluating_platform' },
    })
    return toChatSession(res.data)
  },

  async listSessions(limit = 50): Promise<{ items: ChatSession[]; total: number }> {
    const res = await api.get<{ items: RuntimeSession[]; total: number }>('/maclaw/evaluation/sessions', { params: { limit } })
    const items = (res.data.items || []).map(toChatSession)
    return { items, total: res.data.total ?? items.length }
  },

  async getSession(id: string): Promise<{ session: ChatSession; messages: ChatMessage[] }> {
    const res = await api.get<RuntimeSessionSnapshot>(`/maclaw/evaluation/sessions/${id}`)
    const session = toChatSession(res.data.session)
    return { session, messages: (res.data.messages || []).map(toChatMessage) }
  },

  async sendMessage(sessionId: string, content: string): Promise<RuntimeSendResult> {
    const res = await api.post<RuntimeMessageResponse>(`/maclaw/evaluation/sessions/${sessionId}/messages`, { content }, { timeout: 180000 })
    return {
      message: res.data.message ? sanitizeDirectMessage(toChatMessage(res.data.message), sessionId) : {
        id: `maclaw-message-pending:${res.data.run?.id || Date.now()}`,
        session_id: sessionId,
        role: 'assistant',
        content: '正在生成回复，请稍候。',
        metadata: { card_type: 'text', response_source: res.data.run?.response_source || 'chat' },
        created_at: nowISO(),
      },
      run: res.data.run,
    }
  },

  async confirmPlan(sessionId: string, testCount?: number, planMessageId?: string): Promise<RuntimeSendResult> {
    const body: Record<string, unknown> = {}
    if (testCount) body.test_count = testCount
    if (planMessageId) body.plan_message_id = planMessageId
    const res = await api.post<EvaluationJob>(`/maclaw/evaluation/sessions/${sessionId}/confirm`, body, { timeout: 600000 })
    const job = res.data
    const hasRun = Boolean(job.progress?.run_id || job.result?.run?.id || job.status === 'running' || job.status === 'succeeded')
    const phase = hasRun ? (job.progress?.phase || (job.status === 'succeeded' ? 'reporting' : 'starting')) : 'queued'
    const statusText = job.progress?.status_text || (
      hasRun
        ? '评测任务已启动，正在连接执行进度。'
        : '评测任务已提交，正在排队执行。'
    )
    return {
      message: {
        id: job.id ? `job-${job.id}-queued` : `maclaw-job:${Date.now()}`,
        session_id: sessionId,
        role: 'assistant',
        content: '',
        metadata: {
          card_type: 'progress',
          job_id: job.id,
          assessment_id: job.progress?.run_id || job.result?.run?.id,
          phase,
          status_text: statusText,
          planned_count: job.progress?.planned_count || testCount,
          executed_count: job.progress?.executed_count,
          current_stage: job.progress?.current_stage,
          duration_ms: job.progress?.duration_ms,
          stage_durations_json: job.progress?.stage_durations_json,
        },
        created_at: job.created_at || nowISO(),
      },
      job,
    }
  },

  async getEvaluationJob(jobId: string): Promise<EvaluationJob> {
    const res = await api.get<EvaluationJob>(`/maclaw/evaluation/jobs/${encodeURIComponent(jobId)}`)
    return res.data
  },

  async getEvaluationJobRecovery(jobId: string): Promise<EvaluationJobRecovery> {
    const res = await api.get<EvaluationJobRecovery>(`/maclaw/evaluation/jobs/${encodeURIComponent(jobId)}/recovery`)
    return res.data
  },

  async retryEvaluationJob(jobId: string): Promise<EvaluationJob> {
    const res = await api.post<EvaluationJob>(`/maclaw/evaluation/jobs/${encodeURIComponent(jobId)}/retry`)
    return res.data
  },

  async resumeEvaluationJob(jobId: string): Promise<EvaluationJob> {
    const res = await api.post<EvaluationJob>(`/maclaw/evaluation/jobs/${encodeURIComponent(jobId)}/resume`)
    return res.data
  },

  async listEvaluationTargets(): Promise<{ items: EvaluationTarget[] }> {
    const res = await api.get<{ items: EvaluationTarget[] }>('/maclaw/evaluation/targets')
    return res.data
  },

  async saveEvaluationTarget(input: EvaluationTargetInput): Promise<EvaluationTarget> {
    const res = await api.post<EvaluationTarget>('/maclaw/evaluation/targets', input)
    return res.data
  },

  async probeEvaluationTarget(targetId: string): Promise<EvaluationTargetProbeResult> {
    const res = await api.post<EvaluationTargetProbeResult>(`/maclaw/evaluation/targets/${encodeURIComponent(targetId)}/health-check`)
    return res.data
  },

  async deleteSession(id: string): Promise<void> {
    await api.delete(`/maclaw/evaluation/sessions/${id}`)
  },

  async getWelcomeCapabilities(limit = 6, targetKind?: string): Promise<WelcomeCapability[]> {
    const params: Record<string, string | number> = { limit }
    if (targetKind === 'agent') params.target_kind = 'agent'
    const res = await api.get<{ items: WelcomeCapability[] }>('/enterprise/welcome-capabilities', { params })
    return (res.data.items || []).slice(0, limit)
  },

  streamRunEvents(
    runId: string,
    onMessage: (message: ChatMessage) => void,
    onDone: () => void,
    onError: (error: Error) => void,
  ): () => void {
    const controller = new AbortController()
    void (async () => {
      try {
        const token = localStorage.getItem('token') || ''
        const response = await fetch(apiURL(`/maclaw/evaluation/runs/${encodeURIComponent(runId)}/events`), {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
          signal: controller.signal,
        })
        if (!response.ok || !response.body) {
          throw new Error(`maclaw event stream failed: ${response.status}`)
        }
        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        let stopped = false
        while (!stopped) {
          const { done, value } = await reader.read()
          buffer += decoder.decode(value, { stream: !done })
          let boundary = buffer.indexOf('\n\n')
          while (boundary >= 0) {
            const block = buffer.slice(0, boundary)
            buffer = buffer.slice(boundary + 2)
            const envelope = parseSSEBlock(block)
            if (envelope) {
              const message = toEventChatMessage(envelope)
              if (message) onMessage(message)
              if (isTerminalEnvelope(envelope)) {
                stopped = true
                onDone()
                void reader.cancel().catch(() => {})
                break
              }
            }
            boundary = buffer.indexOf('\n\n')
          }
          if (done) {
            if (!stopped) onDone()
            break
          }
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          onError(error instanceof Error ? error : new Error(String(error)))
        }
      }
    })()
    return () => controller.abort()
  },
}