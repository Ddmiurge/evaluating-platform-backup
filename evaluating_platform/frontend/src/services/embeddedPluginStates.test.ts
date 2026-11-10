import { describe, expect, it } from 'vitest'

import {
  EMBEDDED_STATE_LABELS,
  arrayBufferToBase64,
  embeddedAdminActions,
  embeddedExpertCanRevise,
  embeddedGateText,
  embeddedStateTag,
} from './embeddedPluginStates'

const ALL_STATES = [
  'pending_initial_review',
  'rejected',
  'approved_for_build',
  'build_requested',
  'build_failed',
  'testing',
  'approved',
  'disabled',
  'remove_requested',
  'removed',
]

describe('embedded states', () => {
  it('covers every machine state with a label and color', () => {
    for (const state of ALL_STATES) {
      const tag = embeddedStateTag(state)
      expect(tag.label).toBeTruthy()
      expect(tag.color).toBeTruthy()
    }
    expect(EMBEDDED_STATE_LABELS.testing).toBe('测试中')
  })

  it('admin actions mirror the Go transition table', () => {
    expect(embeddedAdminActions('pending_initial_review')).toEqual(['approve', 'changes'])
    expect(embeddedAdminActions('approved_for_build')).toEqual(['request-build'])
    expect(embeddedAdminActions('build_failed')).toEqual(['request-build'])
    expect(embeddedAdminActions('build_requested')).toEqual([]) // the tool owns it
    expect(embeddedAdminActions('testing')).toEqual(['final-approve'])
    expect(embeddedAdminActions('approved')).toEqual(['disable', 'remove'])
    expect(embeddedAdminActions('disabled')).toEqual(['enable', 'remove'])
    expect(embeddedAdminActions('remove_requested')).toEqual(['unmark'])
    expect(embeddedAdminActions('rejected')).toEqual([]) // the expert re-uploads
    expect(embeddedAdminActions('removed')).toEqual([]) // terminal
  })

  it('experts can revise anything except the terminal removed state', () => {
    expect(embeddedExpertCanRevise('rejected')).toBe(true)
    expect(embeddedExpertCanRevise('testing')).toBe(true)
    expect(embeddedExpertCanRevise('removed')).toBe(false)
  })

  it('gate text shows pass with summary and failure with reason', () => {
    expect(embeddedGateText({ passed: true, test_summary: '12 passed · 40ms' })).toContain('✓')
    expect(embeddedGateText({ passed: true, test_summary: '12 passed · 40ms' })).toContain('12 passed')
    expect(embeddedGateText({ passed: false, reason: '含 process.env' })).toContain('process.env')
    expect(embeddedGateText(undefined)).toBe('—')
  })

  it('base64 encodes binary chunks correctly (including multi-byte)', () => {
    const bytes = new Uint8Array([0, 1, 2, 250, 251, 252, 253, 254, 255])
    const b64 = arrayBufferToBase64(bytes.buffer)
    const decoded = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))
    expect(Array.from(decoded)).toEqual(Array.from(bytes))
    // large buffer exercises the chunk path (> 0x8000 bytes)
    const big = new Uint8Array(0x8001).fill(7)
    expect(atob(arrayBufferToBase64(big.buffer)).length).toBe(0x8001)
  })
})
