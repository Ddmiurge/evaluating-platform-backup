import { useCallback, useEffect, useRef, useState, type Dispatch, type MouseEvent, type SetStateAction } from 'react'
import { Alert, Button, Drawer, Form, Input, Segmented, Select, Space, Switch, message } from 'antd'
import { ApiOutlined, DeleteOutlined, LoadingOutlined, PlusOutlined, SendOutlined } from '@ant-design/icons'

import type { ChatMessage, ChatSession, WelcomeCapability } from '../../services/chat'
import { maclawRuntimeChatService as chatService } from '../../services/maclawRuntime'
import type { EvaluationJob, EvaluationTarget, RuntimeRun } from '../../services/maclawRuntime'
import { MessageBubble, TypingIndicator, WelcomePanel } from './ChatCards'
import { AGENT_WELCOME_CAPABILITIES, DEFAULT_WELCOME_CAPABILITIES, confirmedPlanStateFromMessages, formatSessionDate, messagesForSession, normalizeSessionTimestamps, pendingAssistantUserMessageId, prepareMessagesForDisplay } from './chatDisplay'
import { applyEvaluationJobRecovery, buildEvaluationJobProgressMessage, jobRunToStream, markEvaluationJobRecoveryStarted, markEvaluationJobRetryStarted } from './jobProgressMessages'

const { TextArea } = Input

type SessionSnapshot = {
  session: ChatSession
  messages: ChatMessage[]
}

function buildTemporaryUserMessage(sessionId: string, content: string): ChatMessage {
  return {
    id: `tmp-${Date.now()}`,
    session_id: sessionId,
    role: 'user',
    content,
    metadata: { card_type: 'text' },
    created_at: new Date().toISOString(),
  }
}

function buildConfirmProgressMessage(sessionId: string, planMessageId: string, plannedCount: number): ChatMessage {
  return {
    id: `confirm-progress-${planMessageId}`,
    session_id: sessionId,
    role: 'assistant',
    content: '',
    metadata: {
      card_type: 'progress',
      phase: 'starting',
      current_stage: 'starting',
      status_text: '已确认执行，正在启动 MaClaw 评估任务...',
      planned_count: plannedCount,
      executed_count: 0,
    },
    created_at: new Date().toISOString(),
  }
}

function findRestorableProgressJobId(messages: ChatMessage[]): string | undefined {
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const metadata = messages[i].metadata || {}
    if (metadata.card_type !== 'progress') continue
    const phase = String(metadata.phase || '').toLowerCase()
    if (phase === 'failed' || phase === 'canceled' || phase === 'cancelled' || phase === 'completed' || phase === 'report') {
      continue
    }
    const jobId = typeof metadata.job_id === 'string' ? metadata.job_id.trim() : ''
    if (jobId) return jobId
  }
  return undefined
}

function mostRecentSession(sessions: ChatSession[]): ChatSession | undefined {
  return [...sessions].sort((left, right) => {
    const leftTime = Date.parse(left.updated_at || left.created_at || '')
    const rightTime = Date.parse(right.updated_at || right.created_at || '')
    return (Number.isFinite(rightTime) ? rightTime : 0) - (Number.isFinite(leftTime) ? leftTime : 0)
  })[0]
}

function extractSendErrorMessage(error: unknown) {
  if (error instanceof Error && error.message.trim()) {
    return error.message
  }
  if (typeof error === 'object' && error !== null) {
    const response = (error as { response?: { data?: { error?: unknown } } }).response
    const detail = response?.data?.error
    if (typeof detail === 'string' && detail.trim()) {
      return detail
    }
  }
  return '发送失败，请检查 maclaw 模型配置、被测模型连接或稍后重试'
}

async function sendPrompt(
  sessionId: string,
  text: string,
  append: boolean,
  setMessages: Dispatch<SetStateAction<ChatMessage[]>>,
  setSendingSessionId: Dispatch<SetStateAction<string | null>>,
  onSessionUpdated: () => void,
  getActiveSessionId: () => string | null,
) {
  setSendingSessionId(sessionId)
  const tempUserMessage = buildTemporaryUserMessage(sessionId, text)
  if (getActiveSessionId() === sessionId) {
    setMessages(previous => (append ? [...messagesForSession(previous, sessionId), tempUserMessage] : [tempUserMessage]))
  }

  try {
    const result = await chatService.sendMessage(sessionId, text)
    const assistantMessage = result.message
    if (getActiveSessionId() === sessionId) {
      setMessages(previous => {
        const withoutTemp = messagesForSession(previous, sessionId).filter(message => message.id !== tempUserMessage.id)
        return append ? [...withoutTemp, tempUserMessage, assistantMessage] : [tempUserMessage, assistantMessage]
      })
    }
    onSessionUpdated()
  } catch (error) {
    if (getActiveSessionId() === sessionId) {
      setMessages(previous => messagesForSession(previous, sessionId).filter(message => message.id !== tempUserMessage.id))
      message.error(extractSendErrorMessage(error))
    }
  } finally {
    setSendingSessionId(current => (current === sessionId ? null : current))
  }
}

export function ChatPage() {
  const [targetForm] = Form.useForm()
  const [sessions, setSessions] = useState<ChatSession[]>([])
  const [activeId, setActiveId] = useState<string | null>(null)
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [welcomeCapabilities, setWelcomeCapabilities] = useState<WelcomeCapability[]>(DEFAULT_WELCOME_CAPABILITIES)
  const [input, setInput] = useState('')
  const [sendingSessionId, setSendingSessionId] = useState<string | null>(null)
  const [loadingSessionId, setLoadingSessionId] = useState<string | null>(null)
  const [confirmedMsgs, setConfirmedMsgs] = useState<Set<string>>(new Set())
  const [confirmedTestCounts, setConfirmedTestCounts] = useState<Record<string, number>>({})
  const [resumingJobIds, setResumingJobIds] = useState<Set<string>>(new Set())
  const [retryingJobIds, setRetryingJobIds] = useState<Set<string>>(new Set())
  const [runningSessionIds, setRunningSessionIds] = useState<Set<string>>(new Set())
  const [targetDrawerOpen, setTargetDrawerOpen] = useState(false)
  const [savingTarget, setSavingTarget] = useState(false)
  const [currentTarget, setCurrentTarget] = useState<EvaluationTarget | null>(null)

  const messageViewportRef = useRef<HTMLDivElement>(null)
  const bottomRef = useRef<HTMLDivElement>(null)
  const shouldStickToBottomRef = useRef(true)
  const activeIdRef = useRef<string | null>(null)
  const streamStopRef = useRef<(() => void) | null>(null)
  const mountedRef = useRef(true)

  const markSessionRunning = useCallback((sessionId: string, running: boolean) => {
    if (!sessionId) return
    setRunningSessionIds(previous => {
      const next = new Set(previous)
      if (running) {
        next.add(sessionId)
      } else {
        next.delete(sessionId)
      }
      return next
    })
  }, [])

  const updateScrollStickiness = useCallback(() => {
    const viewport = messageViewportRef.current
    if (!viewport) {
      shouldStickToBottomRef.current = true
      return
    }
    const distanceFromBottom = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight
    shouldStickToBottomRef.current = distanceFromBottom < 96
  }, [])

  const applySessionSnapshot = useCallback((snapshot: SessionSnapshot) => {
    const rawMessages = snapshot.messages || []
    const confirmedState = confirmedPlanStateFromMessages(rawMessages)
    setConfirmedMsgs(confirmedState.confirmedIds)
    setConfirmedTestCounts(confirmedState.testCounts)
    setMessages(prepareMessagesForDisplay(rawMessages, snapshot.session))
    markSessionRunning(snapshot.session.id, snapshot.session.state === 'running')
  }, [markSessionRunning])

  useEffect(() => {
    activeIdRef.current = activeId
  }, [activeId])

  const loadSessions = useCallback(async () => {
    try {
      const res = await chatService.listSessions()
      setSessions((res.items || []).map(normalizeSessionTimestamps))
    } catch {
      // Ignore sidebar refresh failures and preserve current UI.
    }
  }, [])

  const loadWelcomeCapabilities = useCallback(async (targetKind: 'llm' | 'agent' = 'llm') => {
    const fallback = targetKind === 'agent' ? AGENT_WELCOME_CAPABILITIES : DEFAULT_WELCOME_CAPABILITIES
    try {
      const items = (await chatService.getWelcomeCapabilities(6, targetKind)).slice(0, 6)
      setWelcomeCapabilities(items.length > 0 ? items : fallback)
    } catch {
      setWelcomeCapabilities(fallback)
    }
  }, [])

  const refreshCurrentTarget = useCallback(async () => {
    try {
      const res = await chatService.listEvaluationTargets()
      setCurrentTarget((res.items || [])[0] || null)
    } catch {
      setCurrentTarget(null)
    }
  }, [])

  const startRunStream = useCallback((run: RuntimeRun, sessionId: string) => {
    if (!run.id) return
    streamStopRef.current?.()
    markSessionRunning(sessionId, true)
    streamStopRef.current = chatService.streamRunEvents(
      run.id,
      (eventMessage) => {
        if (activeIdRef.current !== sessionId) return
        setMessages(previous => prepareMessagesForDisplay([
          ...previous.filter(message => message.id !== eventMessage.id),
          eventMessage,
        ]))
      },
      async () => {
        streamStopRef.current = null
        markSessionRunning(sessionId, false)
        await loadSessions()
        if (activeIdRef.current !== sessionId) return
        try {
          const snapshot = await chatService.getSession(sessionId)
          applySessionSnapshot(snapshot)
        } catch {
          // Keep streamed cards visible if final snapshot refresh fails.
        }
      },
      () => {
        streamStopRef.current = null
        markSessionRunning(sessionId, false)
        message.error('maclaw 事件流连接失败，请稍后刷新会话。')
      },
    )
  }, [applySessionSnapshot, loadSessions, markSessionRunning])

  const waitForEvaluationJob = useCallback(async (jobId: string, sessionId: string) => {
    const MAX_CONSECUTIVE_POLL_FAILURES = 5
    let consecutivePollFailures = 0
    for (let attempt = 0; attempt < 650; attempt += 1) {
      if (activeIdRef.current !== sessionId || !mountedRef.current) return
      let job: EvaluationJob
      try {
        job = await chatService.getEvaluationJob(jobId)
        consecutivePollFailures = 0
      } catch {
        consecutivePollFailures += 1
        if (consecutivePollFailures < MAX_CONSECUTIVE_POLL_FAILURES) {
          await new Promise(resolve => window.setTimeout(resolve, 1000))
          continue
        }
        if (activeIdRef.current !== sessionId || !mountedRef.current) return
        const pollFailureCard = buildEvaluationJobProgressMessage({
          id: jobId,
          kind: 'evaluation.run',
          status: 'failed',
          error: '评测任务状态查询连续失败，已停止自动跟踪。请刷新会话后重试。',
        }, sessionId, 'failed')
        setMessages(previous => prepareMessagesForDisplay([
          ...previous.filter(message => message.id !== pollFailureCard.id),
          pollFailureCard,
        ]))
        markSessionRunning(sessionId, false)
        message.error('评测任务状态查询连续失败，请稍后刷新会话重试。')
        return
      }
      if (job.status === 'succeeded') {
        setMessages(previous => prepareMessagesForDisplay(
          previous.filter(message => message.id !== `job-${jobId}-queued`),
        ))
        markSessionRunning(sessionId, false)
        await loadSessions()
        if (activeIdRef.current !== sessionId) return
        try {
          const snapshot = await chatService.getSession(sessionId)
          applySessionSnapshot(snapshot)
        } catch {
          // Keep the progress card visible if final snapshot refresh fails.
        }
        return
      }
      if (job.status === 'failed' || job.status === 'canceled') {
        const phase = job.status === 'canceled' ? 'canceled' : 'failed'
        let cardJob = job
        if (!job.progress?.recovery_action) {
          try {
            const recovery = await chatService.getEvaluationJobRecovery(job.id)
            cardJob = applyEvaluationJobRecovery(job, recovery)
          } catch {
            cardJob = job
          }
        }
        const card = buildEvaluationJobProgressMessage(cardJob, sessionId, phase)
        setMessages(previous => prepareMessagesForDisplay([
          ...previous.filter(message => message.id !== card.id),
          card,
        ]))
        markSessionRunning(sessionId, false)
        if (mountedRef.current) {
          if (cardJob.progress?.recovery_action === 'resume') {
            message.warning('评测任务中断，可在卡片中继续恢复。')
          } else if (cardJob.progress?.recovery_action === 'retry') {
            message.warning('评测任务中断，可在卡片中重试。')
          } else {
            message.error(cardJob.error || '评测任务未能完成，请稍后重试。')
          }
        }
        return
      }
      const run = jobRunToStream(job, sessionId)
      if (run?.id) {
        // Single source of truth for progress: once the SSE stream is active it
        // owns every progress card for this run. The job poll only seeds the
        // initial "queued" card before the stream connects, then stays out of
        // the way. If both channels wrote the same assessment_id concurrently
        // they would alternate as the "latest" progress card (collapseProgress
        // keeps one per assessment_id) and the visible card would flicker.
        // When the SSE stream errors out, streamStopRef is cleared and this
        // branch resumes writing, so the job poll remains the fallback.
        if (!streamStopRef.current) {
          const card = buildEvaluationJobProgressMessage(job, sessionId, 'queued')
          setMessages(previous => prepareMessagesForDisplay(
            [
              ...previous.filter(message => message.id !== card.id),
              card,
            ],
          ))
          startRunStream(run, sessionId)
        }
        await new Promise(resolve => window.setTimeout(resolve, 1000))
        continue
      }
      // Jobs without a runtime run stream (platform promptfoo engine jobs,
      // pfj-…): refresh the progress card straight from the job poll. Never
      // fall back to session snapshots here — each snapshot rewrites the
      // whole message list and confirmed state, which made the plan card and
      // progress cards flicker in and out for the whole run.
      const jobCard = buildEvaluationJobProgressMessage(job, sessionId, 'queued')
      setMessages(previous => prepareMessagesForDisplay([
        ...previous.filter(message => message.id !== jobCard.id),
        jobCard,
      ]))
      await new Promise(resolve => window.setTimeout(resolve, 1000))
    }
    markSessionRunning(sessionId, false)
    if (mountedRef.current && activeIdRef.current === sessionId) {
      message.error('评测任务排队超时，请稍后刷新会话。')
    }
  }, [applySessionSnapshot, loadSessions, markSessionRunning, startRunStream])

  const handleRetryJob = useCallback(async (jobId: string, sessionId: string) => {
    if (activeIdRef.current !== sessionId) return
    setRetryingJobIds(previous => new Set([...previous, jobId]))
    markSessionRunning(sessionId, true)
    shouldStickToBottomRef.current = true
    try {
      const retryJob = await chatService.retryEvaluationJob(jobId)
      const queued = buildEvaluationJobProgressMessage(retryJob, sessionId, 'queued', { retryOfJobId: jobId })
      setMessages(previous => prepareMessagesForDisplay([
        ...markEvaluationJobRetryStarted(previous, jobId, retryJob.id).filter(message => message.id !== queued.id),
        queued,
      ]))
      void waitForEvaluationJob(retryJob.id, sessionId)
    } catch {
      markSessionRunning(sessionId, false)
      message.error('重试评测任务失败，请稍后再试。')
    } finally {
      setRetryingJobIds(previous => {
        const next = new Set(previous)
        next.delete(jobId)
        return next
      })
    }
  }, [markSessionRunning, waitForEvaluationJob])

  const handleResumeJob = useCallback(async (jobId: string, sessionId: string) => {
    if (activeIdRef.current !== sessionId) return
    setResumingJobIds(previous => new Set([...previous, jobId]))
    markSessionRunning(sessionId, true)
    shouldStickToBottomRef.current = true
    try {
      const resumeJob = await chatService.resumeEvaluationJob(jobId)
      const queued = buildEvaluationJobProgressMessage(resumeJob, sessionId, 'queued', { resumeOfJobId: jobId })
      setMessages(previous => prepareMessagesForDisplay([
        ...markEvaluationJobRecoveryStarted(previous, jobId, resumeJob.id, 'resume').filter(message => message.id !== queued.id),
        queued,
      ]))
      void waitForEvaluationJob(resumeJob.id, sessionId)
    } catch {
      markSessionRunning(sessionId, false)
      message.error('恢复评测任务失败，请稍后再试。')
    } finally {
      setResumingJobIds(previous => {
        const next = new Set(previous)
        next.delete(jobId)
        return next
      })
    }
  }, [markSessionRunning, waitForEvaluationJob])

  const waitForAssistantReply = useCallback(async (sessionId: string, userMessageId: string) => {
    if (!sessionId || !userMessageId) return
    setSendingSessionId(sessionId)
    try {
      for (let attempt = 0; attempt < 60; attempt += 1) {
        await new Promise(resolve => window.setTimeout(resolve, 1500))
        if (activeIdRef.current !== sessionId || !mountedRef.current) return
        const snapshot = await chatService.getSession(sessionId)
        applySessionSnapshot(snapshot)
        if (pendingAssistantUserMessageId(snapshot.messages) !== userMessageId) {
          await loadSessions()
          return
        }
      }
      if (activeIdRef.current === sessionId && mountedRef.current) {
        message.warning('MaClaw 仍在生成回复，请稍后刷新会话。')
      }
    } catch {
      if (activeIdRef.current === sessionId && mountedRef.current) {
        message.error('刷新 MaClaw 回复状态失败，请稍后重试。')
      }
    } finally {
      setSendingSessionId(current => (current === sessionId ? null : current))
    }
  }, [applySessionSnapshot, loadSessions])

  const openSession = useCallback(async (id: string) => {
    streamStopRef.current?.()
    streamStopRef.current = null
    shouldStickToBottomRef.current = true
    activeIdRef.current = id
    setActiveId(id)
    setLoadingSessionId(id)
    try {
      const snapshot = await chatService.getSession(id)
      applySessionSnapshot(snapshot)
      const jobId = findRestorableProgressJobId(snapshot.messages || [])
      if (jobId) {
        markSessionRunning(id, true)
        void waitForEvaluationJob(jobId, id)
      } else {
        const pendingUserMessageId = pendingAssistantUserMessageId(snapshot.messages || [])
        if (pendingUserMessageId) {
          void waitForAssistantReply(id, pendingUserMessageId)
        }
      }
    } catch {
      // Ignore open failure and keep the previous content rendered.
    } finally {
      setLoadingSessionId(current => (current === id ? null : current))
    }
  }, [applySessionSnapshot, markSessionRunning, waitForAssistantReply, waitForEvaluationJob])

  const createSession = useCallback(async () => {
    streamStopRef.current?.()
    streamStopRef.current = null
    const session = normalizeSessionTimestamps(await chatService.createSession())
    setSessions(previous => [session, ...previous])
    activeIdRef.current = session.id
    setActiveId(session.id)
    setMessages([])
    markSessionRunning(session.id, false)
    setConfirmedMsgs(new Set())
    setConfirmedTestCounts({})
    shouldStickToBottomRef.current = true
    return session
  }, [markSessionRunning])

  const deleteSession = useCallback(async (id: string, event: MouseEvent) => {
    event.stopPropagation()
    await chatService.deleteSession(id)
    streamStopRef.current?.()
    streamStopRef.current = null
    setSessions(previous => previous.filter(session => session.id !== id))
    if (activeId === id) {
      activeIdRef.current = null
      setActiveId(null)
      setMessages([])
      setSendingSessionId(current => (current === id ? null : current))
      markSessionRunning(id, false)
      setConfirmedMsgs(new Set())
      setConfirmedTestCounts({})
    }
  }, [activeId, markSessionRunning])

  const sendToSession = useCallback(async (sessionId: string, text: string, append: boolean) => {
    await sendPrompt(sessionId, text, append, setMessages, setSendingSessionId, loadSessions, () => activeIdRef.current)
  }, [loadSessions])

  const submitPrompt = useCallback(async (text: string) => {
    const trimmed = text.trim()
    const activeSending = Boolean(activeId && sendingSessionId === activeId)
    const activeRunning = Boolean(activeId && runningSessionIds.has(activeId))
    if (!trimmed || activeSending || activeRunning) return

    if (activeId && messagesForSession(messages, activeId).length === 0) {
      await sendToSession(activeId, trimmed, false)
      return
    }

    const session = await createSession()
    await sendToSession(session.id, trimmed, false)
  }, [activeId, createSession, messages, runningSessionIds, sendToSession, sendingSessionId])

  const handleConfirmPlan = useCallback(async (msgId: string, testCount: number) => {
    if (!activeId) return
    const sessionId = activeId
    const progressMessage = buildConfirmProgressMessage(sessionId, msgId, testCount)
    try {
      shouldStickToBottomRef.current = true
      setConfirmedMsgs(previous => new Set([...previous, msgId]))
      setConfirmedTestCounts(previous => ({ ...previous, [msgId]: testCount }))
      markSessionRunning(sessionId, true)
      setMessages(previous => prepareMessagesForDisplay([
        ...messagesForSession(previous, sessionId).filter(message => message.id !== progressMessage.id),
        progressMessage,
      ]))
      const result = await chatService.confirmPlan(sessionId, testCount, msgId)
      if (activeIdRef.current !== sessionId) {
        if (result.job?.status === 'succeeded' || (!result.run && !result.job)) {
          markSessionRunning(sessionId, false)
        }
        return
      }
      if (result.job?.status === 'succeeded') {
        const snapshot = await chatService.getSession(sessionId)
        if (activeIdRef.current !== sessionId) return
        applySessionSnapshot(snapshot)
        await loadSessions()
        markSessionRunning(sessionId, false)
        return
      }
      setMessages(previous => prepareMessagesForDisplay([
        ...messagesForSession(previous, sessionId).filter(message => message.id !== progressMessage.id),
        result.message,
      ]))
      if (result.run?.id) {
        startRunStream(result.run, sessionId)
        if (result.job?.id) {
          void waitForEvaluationJob(result.job.id, sessionId)
        }
      } else if (result.job?.id) {
        const run = jobRunToStream(result.job, sessionId)
        if (run?.id) {
          startRunStream(run, sessionId)
          void waitForEvaluationJob(result.job.id, sessionId)
        } else {
          void waitForEvaluationJob(result.job.id, sessionId)
        }
      } else {
        const snapshot = await chatService.getSession(sessionId)
        if (activeIdRef.current !== sessionId) return
        applySessionSnapshot(snapshot)
      }
    } catch (error) {
      const errorMessage = extractSendErrorMessage(error)
      markSessionRunning(sessionId, false)
      setConfirmedMsgs(previous => {
        const next = new Set(previous)
        next.delete(msgId)
        return next
      })
      setConfirmedTestCounts(previous => {
        const next = { ...previous }
        delete next[msgId]
        return next
      })
      if (activeIdRef.current === sessionId) {
        setMessages(previous => prepareMessagesForDisplay(messagesForSession(previous, sessionId).filter(message => message.id !== progressMessage.id)))
        message.error(errorMessage)
      }
    }
  }, [activeId, applySessionSnapshot, loadSessions, markSessionRunning, startRunStream, waitForEvaluationJob])

  const submitCurrentInput = useCallback(async () => {
    const trimmed = input.trim()
    const activeSending = Boolean(activeId && sendingSessionId === activeId)
    const activeRunning = Boolean(activeId && runningSessionIds.has(activeId))
    if (!trimmed || activeSending || activeRunning) return
    setInput('')
    if (activeId) {
      await sendToSession(activeId, trimmed, true)
      return
    }
    await submitPrompt(trimmed)
  }, [activeId, input, runningSessionIds, sendToSession, sendingSessionId, submitPrompt])

  const openTargetDrawer = useCallback(async () => {
    setTargetDrawerOpen(true)
    try {
      const res = await chatService.listEvaluationTargets()
      const target = (res.items || [])[0]
      if (target) {
        setCurrentTarget(target)
        const isAgent = target.kind === 'agent'
        targetForm.setFieldsValue({
          kind: isAgent ? 'agent' : 'llm',
          name: target.name,
          provider: target.provider || 'openai',
          base_url: target.base_url,
          model: target.model,
          credential_secret: '',
          supports_vision: target.metadata?.supports_vision === 'true',
          agent_endpoint: target.metadata?.agent_endpoint,
          agent_method: target.metadata?.agent_method || 'POST',
          agent_headers_template: target.metadata?.agent_headers_template,
          agent_body_template: target.metadata?.agent_body_template,
          agent_response_path: target.metadata?.agent_response_path,
        })
      } else {
        setCurrentTarget(null)
        targetForm.setFieldsValue({ kind: 'llm', name: '默认被测模型', provider: 'openai', auth_type: 'bearer', agent_method: 'POST' })
      }
    } catch {
      targetForm.setFieldsValue({ kind: 'llm', name: '默认被测模型', provider: 'openai', auth_type: 'bearer', agent_method: 'POST' })
    }
  }, [targetForm])

  const saveAndProbeTarget = useCallback(async () => {
    const values = await targetForm.validateFields()
    setSavingTarget(true)
    try {
      const isAgent = values.kind === 'agent'
      const target = await chatService.saveEvaluationTarget(isAgent ? {
        name: values.name,
        kind: 'agent',
        auth_type: 'bearer',
        credential_secret: values.credential_secret,
        status: 'published',
        metadata: {
          agent_endpoint: values.agent_endpoint,
          agent_method: values.agent_method || 'POST',
          agent_headers_template: values.agent_headers_template || '',
          agent_body_template: values.agent_body_template || '',
          agent_response_path: values.agent_response_path || '',
        },
      } : {
        name: values.name,
        kind: 'llm',
        provider: values.provider,
        base_url: values.base_url,
        model: values.model,
        auth_type: 'bearer',
        credential_secret: values.credential_secret,
        status: 'published',
        metadata: {
          health_url: `${String(values.base_url || '').replace(/\/+$/, '')}/models`,
          supports_vision: values.supports_vision ? 'true' : 'false',
        },
      })
      setCurrentTarget(target)
      const probe = await chatService.probeEvaluationTarget(target.id)
      if (probe.status === 'healthy') {
        message.success(isAgent ? '智能体连接正常，后续评测会调用该智能体端点' : '被测模型连接正常，后续计划会优先使用该企业 target')
      } else {
        message.warning(probe.message || probe.error || (isAgent ? '智能体已保存，但连通性检查未通过' : '被测模型已保存，但连通性检查未通过'))
      }
      targetForm.setFieldValue('credential_secret', '')
    } catch (error) {
      message.error((error as Error).message || '保存被测目标失败')
    } finally {
      setSavingTarget(false)
    }
  }, [targetForm])

  useEffect(() => {
    loadSessions()
  }, [loadSessions])

  useEffect(() => {
    if (activeId || sessions.length === 0 || loadingSessionId) return
    const latest = mostRecentSession(sessions)
    if (latest) {
      void openSession(latest.id)
    }
  }, [activeId, loadingSessionId, openSession, sessions])

  useEffect(() => {
    void refreshCurrentTarget()
  }, [refreshCurrentTarget])

  useEffect(() => {
    const targetKind: 'llm' | 'agent' = currentTarget?.kind === 'agent' ? 'agent' : 'llm'
    loadWelcomeCapabilities(targetKind)
    const handleFocus = () => { loadWelcomeCapabilities(targetKind) }
    window.addEventListener('focus', handleFocus)
    return () => window.removeEventListener('focus', handleFocus)
  }, [loadWelcomeCapabilities, currentTarget?.kind])

  useEffect(() => {
    if (!shouldStickToBottomRef.current) return
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  useEffect(() => () => {
    mountedRef.current = false
    streamStopRef.current?.()
  }, [])

  const activeLoading = Boolean(activeId && loadingSessionId === activeId)
  const activeSending = Boolean(activeId && sendingSessionId === activeId)
  const activeRunning = Boolean(activeId && runningSessionIds.has(activeId))
  const visibleMessages = messagesForSession(messages, activeId)
  const showWelcomeState = !activeLoading && (!activeId || visibleMessages.length === 0)
  const canSubmit = input.trim() !== '' && !activeSending && !activeRunning

  return (
    <div style={{ display: 'flex', height: 'calc(100vh - 64px)', overflow: 'hidden' }}>
      <div style={{ width: 260, borderRight: '1px solid var(--border-color)', display: 'flex', flexDirection: 'column', background: 'var(--bg-surface)', flexShrink: 0 }}>
        <div style={{ padding: '12px 12px 8px' }}>
          <Button type="primary" icon={<PlusOutlined />} block onClick={createSession} style={{ borderRadius: 8 }}>
            新建对话
          </Button>
        </div>
        <div style={{ flex: 1, overflowY: 'auto', padding: '0 8px' }}>
          {sessions.length === 0 && (
            <div style={{ color: 'var(--text-muted)', fontSize: 12, textAlign: 'center', marginTop: 32 }}>
              暂无对话记录
            </div>
          )}
          {sessions.map(session => (
            <div
              key={session.id}
              onClick={() => openSession(session.id)}
              style={{ padding: '10px 12px', borderRadius: 8, cursor: 'pointer', marginBottom: 2, background: activeId === session.id ? 'var(--color-primary-light)' : 'transparent', border: `1px solid ${activeId === session.id ? 'var(--color-primary-border)' : 'transparent'}`, display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}
            >
              <div style={{ flex: 1, overflow: 'hidden' }}>
                <div style={{ color: 'var(--text-primary)', fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {session.title}
                </div>
                <div style={{ color: 'var(--text-muted)', fontSize: 11, marginTop: 2 }}>
                  {formatSessionDate(session.updated_at)}
                </div>
              </div>
              <DeleteOutlined style={{ color: 'var(--text-muted)', fontSize: 13, flexShrink: 0 }} onClick={(event) => deleteSession(session.id, event)} />
            </div>
          ))}
        </div>
      </div>

      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <div ref={messageViewportRef} onScroll={updateScrollStickiness} style={{ flex: 1, overflowY: 'auto' }}>
          {activeLoading ? (
            <div style={{ textAlign: 'center', paddingTop: 60 }}>
              <LoadingOutlined style={{ fontSize: 24, color: 'var(--color-primary)' }} />
            </div>
          ) : showWelcomeState ? (
            <WelcomePanel items={welcomeCapabilities} onQuickPrompt={submitPrompt} />
          ) : (
            <div style={{ padding: '24px 32px' }}>
              {visibleMessages.map(msg => (
                <MessageBubble
                  key={msg.id}
                  msg={msg}
                  onPlanConfirm={(rounds, planMessageId) => handleConfirmPlan(planMessageId || msg.id, rounds)}
                  onQuickReply={(text) => {
                    if (!activeId || activeSending || activeRunning) return
                    void sendToSession(activeId, text, true)
                  }}
                  confirmedMsgs={confirmedMsgs}
                  confirmedTestCounts={confirmedTestCounts}
                  isRunning={activeRunning}
                  onResumeJob={handleResumeJob}
                  resumingJobIds={resumingJobIds}
                  onRetryJob={handleRetryJob}
                  retryingJobIds={retryingJobIds}
                />
              ))}
              {activeSending && <TypingIndicator />}
              <div ref={bottomRef} />
            </div>
          )}
        </div>

        <div style={{ padding: '12px 24px 20px', borderTop: '1px solid var(--border-color)', background: 'var(--bg-surface)' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
            <span style={{ color: 'var(--text-muted)', fontSize: 12 }}>
              {currentTarget
                ? currentTarget.kind === 'agent'
                  ? `当前被测智能体：${currentTarget.name}`
                  : `当前被测模型：${currentTarget.name}${currentTarget.model ? ` / ${currentTarget.model}` : ''}`
                : '请先配置本企业被测模型或智能体'}
            </span>
            <Button size="small" icon={<ApiOutlined />} onClick={() => { void openTargetDrawer() }}>
              被测模型连接
            </Button>
          </div>
          <div style={{ display: 'flex', gap: 10, alignItems: 'flex-end' }}>
            <TextArea
              value={input}
              onChange={(event) => setInput(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && !event.shiftKey) {
                  event.preventDefault()
                  if (!canSubmit) return
                  void submitCurrentInput()
                }
              }}
              placeholder={activeRunning ? '评估任务执行中，请稍候...' : '描述你的评估需求，按 Enter 发送，Shift+Enter 换行...'}
              autoSize={{ minRows: 1, maxRows: 5 }}
              disabled={activeRunning}
              style={{ flex: 1, background: 'var(--bg-card)', borderColor: 'var(--border-color)', color: 'var(--text-primary)', borderRadius: 10, resize: 'none' }}
            />
            <Button
              type="primary"
              icon={<SendOutlined />}
              onClick={() => { void submitCurrentInput() }}
              disabled={!canSubmit}
              style={{ height: 40, borderRadius: 10 }}
            />
          </div>
        </div>
      </div>

      <Drawer
        title="企业被测对象连接"
        open={targetDrawerOpen}
        onClose={() => setTargetDrawerOpen(false)}
        size="large"
        footer={(
          <Space style={{ float: 'right' }}>
            <Button onClick={() => setTargetDrawerOpen(false)}>取消</Button>
            <Button type="primary" loading={savingTarget} onClick={() => { void saveAndProbeTarget() }}>保存并测试</Button>
          </Space>
        )}
      >
        <Form form={targetForm} layout="vertical" initialValues={{ kind: 'llm', name: '默认被测模型', provider: 'openai', auth_type: 'bearer', agent_method: 'POST', supports_vision: false }}>
          <Form.Item label="被测对象类型" name="kind">
            <Segmented
              options={[
                { value: 'llm', label: '大模型（LLM）' },
                { value: 'agent', label: '智能体（Agent）' },
              ]}
              onChange={() => targetForm.setFieldValue('credential_secret', '')}
            />
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(prev, next) => prev.kind !== next.kind}>
            {({ getFieldValue }) => {
              const kind = getFieldValue('kind') || 'llm'
              return kind === 'agent' ? (
                <>
                  <Form.Item label="名称" name="name" rules={[{ required: true, message: '请输入名称' }]}>
                    <Input placeholder="如：客服 Agent" />
                  </Form.Item>
                  <Form.Item
                    label="对话端点 URL"
                    name="agent_endpoint"
                    rules={[
                      { required: true, message: '请输入智能体对话端点 URL' },
                      { type: 'url', message: '请输入合法的 http(s) URL' },
                    ]}
                    extra="智能体的对话 API 地址。评测时每个攻击用例都会向该端点发起一次请求。建议使用沙箱/mock 端点，避免触发真实副作用。"
                  >
                    <Input placeholder="https://agent.example.com/chat" />
                  </Form.Item>
                  <Form.Item label="HTTP 方法" name="agent_method" initialValue="POST">
                    <Select options={[
                      { value: 'POST', label: 'POST' },
                      { value: 'PUT', label: 'PUT' },
                      { value: 'PATCH', label: 'PATCH' },
                      { value: 'GET', label: 'GET' },
                    ]} />
                  </Form.Item>
                  <Form.Item
                    label="请求头模板（JSON）"
                    name="agent_headers_template"
                    rules={[
                      {
                        validator: (_rule, value: string) => {
                          const raw = String(value || '').trim()
                          if (!raw) return Promise.resolve()
                          try {
                            const parsed = JSON.parse(raw)
                            if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
                              return Promise.reject(new Error('必须是 JSON 对象，如 {"X-Api-Key":"{{api_key}}"}'))
                            }
                            return Promise.resolve()
                          } catch {
                            return Promise.reject(new Error('必须是合法的 JSON 对象'))
                          }
                        },
                      },
                    ]}
                    extra={'头部值可包含 {{api_key}} 占位符（服务端替换为下方密钥，不会回显）。示例：{"X-Api-Key":"{{api_key}}"}'}
                  >
                    <Input.TextArea rows={2} placeholder='{"X-Api-Key":"{{api_key}}"}' autoComplete="off" />
                  </Form.Item>
                  <Form.Item
                    label="请求体模板（JSON）"
                    name="agent_body_template"
                    rules={[
                      {
                        validator: (_rule, value: string) => {
                          const raw = String(value || '').trim()
                          if (!raw) return Promise.resolve()
                          try {
                            JSON.parse(raw)
                            return Promise.resolve()
                          } catch {
                            return Promise.reject(new Error('必须是合法的 JSON'))
                          }
                        },
                      },
                    ]}
                    extra={'用 {{prompt}} 占位攻击提示（每条用例替换一次），{{api_key}} 占位密钥。留空默认 {"prompt":"{{prompt}}"}。示例：{"query":"{{prompt}}","session":"fixed"}'}
                  >
                    <Input.TextArea rows={3} placeholder='{"query":"{{prompt}}"}' autoComplete="off" />
                  </Form.Item>
                  <Form.Item
                    label="响应提取路径"
                    name="agent_response_path"
                    extra="从 JSON 响应中提取智能体回复文本的点分路径。示例：reply.text、choices[0].message.content。留空表示提取不到时回退整包。"
                  >
                    <Input placeholder="reply.text" />
                  </Form.Item>
                  <Form.Item label="API Key / Token" name="credential_secret" extra="密钥只写入加密存储，不会回显明文。用于替换 {{api_key}} 占位符；若模板未使用占位符，默认以 Bearer 方式发送 Authorization 头。留空表示沿用已保存密钥。">
                    <Input.Password placeholder="sk-..." autoComplete="new-password" />
                  </Form.Item>
                  <Alert
                    type="warning"
                    showIcon
                    style={{ marginBottom: 16 }}
                    message="智能体评测为单轮调用（v1）：每个攻击用例独立发起一次请求，不维护多轮会话状态。请确保端点无副作用或使用沙箱环境。"
                  />
                </>
              ) : (
                <>
                  <Form.Item label="名称" name="name" rules={[{ required: true, message: '请输入名称' }]}>
                    <Input placeholder="默认被测模型" />
                  </Form.Item>
                  <Form.Item label="Provider" name="provider" rules={[{ required: true, message: '请选择 provider' }]}>
                    <Select options={[
                      { value: 'openai', label: 'OpenAI Compatible' },
                      { value: 'anthropic', label: 'Anthropic Compatible' },
                      { value: 'custom', label: 'Custom HTTP' },
                    ]} />
                  </Form.Item>
                  <Form.Item label="Base URL" name="base_url" rules={[{ required: true, message: '请输入被测模型 Base URL' }]}>
                    <Input placeholder="https://api.example.com/v1" />
                  </Form.Item>
                  <Form.Item label="Model" name="model" rules={[{ required: true, message: '请输入模型名' }]}>
                    <Input placeholder="gpt-4.1 或本地模型名" />
                  </Form.Item>
                  <Form.Item label="API Key" name="credential_secret" extra="密钥只写入 maclaw，不会回显明文。留空表示沿用已保存密钥。">
                    <Input.Password placeholder="sk-..." autoComplete="new-password" />
                  </Form.Item>
                  <Form.Item
                    label="支持图片输入"
                    name="supports_vision"
                    valuePropName="checked"
                    extra="仅在被测模型支持 OpenAI-compatible 图文输入时开启；DeepSeek 文本模型请保持关闭。"
                  >
                    <Switch />
                  </Form.Item>
                </>
              )
            }}
          </Form.Item>
        </Form>
      </Drawer>

      <style>{'@keyframes bounce{0%,80%,100%{transform:translateY(0);opacity:0.4}40%{transform:translateY(-6px);opacity:1}}'}</style>
    </div>
  )
}