import { expect, test } from 'vitest'
import { shouldStreamRuntimeRun } from './runtimeRunPolicy.ts'

test('shouldStreamRuntimeRun only streams confirmation evaluation runs', () => {
  expect(shouldStreamRuntimeRun({
    id: 'chat-run',
    status: 'running',
    response_source: 'chat',
  })).toBe(false)

  expect(shouldStreamRuntimeRun({
    id: 'ask-run',
    status: 'queued',
    response_source: 'ask_user',
  })).toBe(false)

  expect(shouldStreamRuntimeRun({
    id: 'plan-run',
    status: 'running',
    response_source: 'plan_confirm',
  })).toBe(false)

  expect(shouldStreamRuntimeRun({
    id: 'eval-run',
    status: 'running',
    metadata: { evaluation_action: 'confirm_plan' },
  })).toBe(true)
})
