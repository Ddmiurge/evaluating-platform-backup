import { expect, test } from 'vitest'
import type { ChatMessage } from '../../services/chat'
import { messagesForSession, pendingAssistantUserMessageId } from './chatDisplay.ts'

test('messagesForSession keeps shared orphan text while filtering scoped cards', () => {
  const messages: ChatMessage[] = [
    {
      id: 'session-a-progress',
      session_id: 'session-a',
      role: 'assistant',
      content: '',
      metadata: { card_type: 'progress' },
      created_at: '2026-05-26T01:00:00Z',
    },
    {
      id: 'session-b-user',
      session_id: 'session-b',
      role: 'user',
      content: 'hello',
      metadata: { card_type: 'text' },
      created_at: '2026-05-26T01:01:00Z',
    },
    {
      id: 'orphan-progress',
      session_id: '',
      role: 'assistant',
      content: '',
      metadata: { card_type: 'progress' },
      created_at: '2026-05-26T01:02:00Z',
    },
    {
      id: 'orphan-plan',
      session_id: '',
      role: 'assistant',
      content: '',
      metadata: { card_type: 'plan_confirm' },
      created_at: '2026-05-26T01:03:00Z',
    },
    {
      id: 'orphan-text',
      session_id: '',
      role: 'assistant',
      content: 'safe shared text',
      metadata: { card_type: 'text' },
      created_at: '2026-05-26T01:04:00Z',
    },
  ]

  const sessionBMessages = messagesForSession(messages, 'session-b')

  expect(sessionBMessages.length).toBe(2)
  expect(sessionBMessages[0].id).toBe('session-b-user')
  expect(sessionBMessages[1].id).toBe('orphan-text')
})

test('pendingAssistantUserMessageId detects an unanswered user prompt', () => {
  expect(pendingAssistantUserMessageId([
    {
      id: 'user-pending',
      session_id: 'session-a',
      role: 'user',
      content: 'clicked quick prompt before refresh',
      metadata: { card_type: 'text' },
      created_at: '2026-05-26T01:05:00Z',
    },
  ])).toBe('user-pending')

  expect(pendingAssistantUserMessageId([
    {
      id: 'user-confirm',
      session_id: 'session-a',
      role: 'user',
      content: 'confirm',
      metadata: { evaluation_action: 'confirm_plan' },
      created_at: '2026-05-26T01:06:00Z',
    },
  ])).toBe('')

  expect(pendingAssistantUserMessageId([
    {
      id: 'user-answered',
      session_id: 'session-a',
      role: 'user',
      content: 'hello',
      metadata: { card_type: 'text' },
      created_at: '2026-05-26T01:07:00Z',
    },
    {
      id: 'assistant-answer',
      session_id: 'session-a',
      role: 'assistant',
      content: 'hi',
      metadata: { card_type: 'text' },
      created_at: '2026-05-26T01:08:00Z',
    },
  ])).toBe('')
})
