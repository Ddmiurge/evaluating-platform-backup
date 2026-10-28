import { describe, expect, it } from 'vitest'

import { normalizePlanForDisplay } from './chatDisplay'
import type { PlanInfo } from '../../services/chat'

function plan(overrides: Partial<PlanInfo>): PlanInfo {
  return {
    name: '评测',
    goal: '合规安全评测',
    target_type: '',
    target_url: '',
    target_key: '',
    target_model: 'm',
    assessment_types: [],
    ...overrides,
  }
}

/**
 * The card's 目标类型 row shows the platform's target KIND, not whatever the
 * planner wrote. The planner used to write the provider name (`openai`), which
 * read as a target type and hid the fact that the target was an agent
 * (reported 2026-10-28).
 */
describe('normalizePlanForDisplay · target type', () => {
  it('labels the two platform kinds', () => {
    expect(normalizePlanForDisplay(plan({ target_type: 'agent' })).target_type).toBe('智能体（Agent）')
    expect(normalizePlanForDisplay(plan({ target_type: 'llm' })).target_type).toBe('大模型（LLM）')
    expect(normalizePlanForDisplay(plan({ target_type: 'AGENT' })).target_type).toBe('智能体（Agent）')
  })

  it('does not dress a provider name up as a target kind', () => {
    // Not "openai", not a blank that silently becomes 未确认: the card says it
    // does not know, which is the truth when the backend has no target.
    expect(normalizePlanForDisplay(plan({ target_type: 'openai' })).target_type).toBe('未确认（目标类型未识别）')
    expect(normalizePlanForDisplay(plan({ target_type: '' })).target_type).toBe('未确认（目标类型未识别）')
  })
})
