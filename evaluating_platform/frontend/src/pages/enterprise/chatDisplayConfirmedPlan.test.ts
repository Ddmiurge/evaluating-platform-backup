import { expect, test } from 'vitest'
import type { ChatMessage } from '../../services/chat'
import { confirmedPlanStateFromMessages } from './chatDisplay.ts'

test('confirmedPlanStateFromMessages maps confirmed plans to their test counts', () => {
  const messages: ChatMessage[] = [
    {
      id: 'msg_plan_old',
      session_id: 'session-a',
      role: 'assistant',
      content: '',
      metadata: { card_type: 'plan_confirm' },
      created_at: '2026-05-27T01:00:00Z',
    },
    {
      id: 'msg_confirm_old',
      session_id: 'session-a',
      role: 'user',
      content: '确认执行',
      metadata: { evaluation_action: 'confirm_plan', test_count: '5' },
      created_at: '2026-05-27T01:01:00Z',
    },
    {
      id: 'msg_plan_new',
      session_id: 'session-a',
      role: 'assistant',
      content: '',
      metadata: { card_type: 'plan_confirm' },
      created_at: '2026-05-27T01:02:00Z',
    },
    {
      id: 'msg_confirm_new',
      session_id: 'session-a',
      role: 'user',
      content: '确认执行',
      metadata: { evaluation_action: 'confirm_plan', plan_message_id: 'msg_plan_new', test_count: '10' },
      created_at: '2026-05-27T01:03:00Z',
    },
  ]

  const state = confirmedPlanStateFromMessages(messages)

  expect(state.confirmedIds.has('msg_plan_old')).toBe(true)
  expect(state.confirmedIds.has('msg_plan_new')).toBe(true)
  expect(state.testCounts.msg_plan_old).toBe(5)
  expect(state.testCounts.msg_plan_new).toBe(10)
})
