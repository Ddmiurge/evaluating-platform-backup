import { expect, test } from 'vitest'
import type { ChatMessage } from '../../services/chat'
import type { EvaluationJob, EvaluationJobRecovery } from '../../services/maclawRuntime'
import { applyEvaluationJobRecovery, buildEvaluationJobProgressMessage, jobRunToStream, markEvaluationJobRetryStarted } from './jobProgressMessages'

test('job progress messages track retry, steps, counts, recovery and streaming', () => {
  const failedProgress: ChatMessage = {
    id: 'job-old-failed',
    session_id: 'session-1',
    role: 'assistant',
    content: '',
    metadata: {
      card_type: 'progress',
      job_id: 'job-old',
      phase: 'failed',
      recovery_action: 'retry',
    },
    created_at: '2026-05-13T00:00:00Z',
  }

  const untouchedProgress: ChatMessage = {
    ...failedProgress,
    id: 'job-other-failed',
    metadata: {
      ...failedProgress.metadata,
      job_id: 'job-other',
    },
  }

  const updated = markEvaluationJobRetryStarted([failedProgress, untouchedProgress], 'job-old', 'job-new')

  expect(updated[0].metadata.retry_started).toBe(true)
  expect(updated[0].metadata.retry_job_id).toBe('job-new')
  expect(updated[1].metadata.retry_started).toBe(undefined)

  const jobWithSteps: EvaluationJob = {
    id: 'job-steps',
    kind: 'evaluation.run',
    status: 'running',
    progress: {
      phase: 'target_call',
      step: 'calling_target',
      step_status: 'running',
      steps: [
        { step: 'materializing_resources', status: 'succeeded', retryable_on_restart: true },
        { step: 'calling_target', status: 'running', started_at: '2026-05-13T01:00:00Z' },
        { step: 'saving_results', status: 'pending' },
      ],
    },
  }
  const progressMessage = buildEvaluationJobProgressMessage(jobWithSteps, 'session-1', 'queued')
  const steps = progressMessage.metadata.steps as Array<{ step: string; status: string }> | undefined
  expect(steps?.[1].step).toBe('calling_target')
  expect(steps?.[1].status).toBe('running')
  expect((steps?.[1] as { started_at?: string } | undefined)?.started_at).toBe('2026-05-13T01:00:00Z')
  expect(progressMessage.metadata.phase).toBe('target_call')
  expect(progressMessage.metadata.status_text).toBe('正在调用被测模型。')

  const jobWithCounts: EvaluationJob = {
    id: 'job-counts',
    kind: 'evaluation.run',
    status: 'running',
    progress: {
      phase: 'target_calls',
      status_text: '正在调用被测模型。',
      planned_count: 20,
      executed_count: 7,
      current_stage: 'target_calls',
    },
  }
  const countProgressMessage = buildEvaluationJobProgressMessage(jobWithCounts, 'session-1', 'queued')
  expect(countProgressMessage.metadata.planned_count).toBe(20)
  expect(countProgressMessage.metadata.executed_count).toBe(7)
  expect(countProgressMessage.metadata.current_stage).toBe('target_calls')

  const pendingQueuedJob: EvaluationJob = {
    id: 'job-pending',
    kind: 'evaluation.run',
    status: 'pending',
  }
  const queuedMessage = buildEvaluationJobProgressMessage(pendingQueuedJob, 'session-1', 'queued')
  expect(queuedMessage.metadata.status_text).toBe('评测任务已提交，正在排队执行。')

  const jobWithoutRecoveryAction: EvaluationJob = {
    id: 'job-recovery',
    kind: 'evaluation.run',
    status: 'failed',
    progress: {
      phase: 'running',
      step: 'materializing_resources',
      status_text: 'interrupted',
    },
  }

  const retryRecovery: EvaluationJobRecovery = {
    job_id: 'job-recovery',
    run_id: 'run-recovery',
    action: 'retry',
    strategy: 'retry_safe',
    step: 'materializing_resources',
    completed_step_ids: ['materializing_resources'],
    can_retry: true,
    manual_review_required: false,
    reason: 'interrupted before target call',
    recovery_indexed: true,
    replacement_job_endpoint: '/api/v1/jobs/job-recovery/retry',
  }

  const jobWithRecovery = applyEvaluationJobRecovery(jobWithoutRecoveryAction, retryRecovery)
  const recoveryMessage = buildEvaluationJobProgressMessage(jobWithRecovery, 'session-1', 'failed')

  expect(jobWithRecovery.progress?.recovery_action).toBe('retry')
  expect(jobWithRecovery.progress?.recovery_strategy).toBe('retry_safe')
  expect(jobWithRecovery.progress?.restart_interrupted).toBe(true)
  expect(jobWithRecovery.progress?.run_id).toBe('run-recovery')
  expect(recoveryMessage.metadata.recovery_action).toBe('retry')

  const resumeRecovery: EvaluationJobRecovery = {
    job_id: 'job-recovery',
    run_id: 'run-recovery',
    action: 'manual_review',
    strategy: 'manual_review',
    step: 'saving_results',
    can_resume: true,
    resume_step: 'saving_results',
    resume_job_endpoint: '/api/v1/jobs/job-recovery/resume',
    can_retry: false,
    manual_review_required: false,
    reason: 'saved artifacts can be finalized safely',
    recovery_indexed: true,
  }

  const resumeJob = applyEvaluationJobRecovery(jobWithoutRecoveryAction, resumeRecovery)
  const resumeMessage = buildEvaluationJobProgressMessage(resumeJob, 'session-1', 'failed')

  expect(resumeJob.progress?.recovery_action).toBe('resume')
  expect(resumeJob.progress?.can_resume).toBe(true)
  expect(resumeJob.progress?.resume_step).toBe('saving_results')
  expect(resumeMessage.metadata.recovery_action).toBe('resume')
  expect(resumeMessage.metadata.can_resume).toBe(true)

  const succeededConfirmJob: EvaluationJob = {
    id: 'run-confirmed',
    kind: 'evaluation.run',
    status: 'succeeded',
    progress: {
      run_id: 'run-confirmed',
      session_id: 'session-1',
    },
  }

  expect(jobRunToStream(succeededConfirmJob, 'session-1')).toBe(undefined)

  const runningConfirmJob: EvaluationJob = {
    ...succeededConfirmJob,
    status: 'running',
  }

  expect(jobRunToStream(runningConfirmJob, 'session-1')?.id).toBe('run-confirmed')
})
