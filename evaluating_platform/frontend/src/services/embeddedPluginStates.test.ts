import { describe, expect, it } from 'vitest'

import {
  EMBEDDED_STATE_LABELS,
  arrayBufferToBase64,
  embeddedAdminActions,
  embeddedBuildRequest,
  embeddedExpertCanRevise,
  embeddedGateText,
  embeddedReviewReasonLabel,
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

  it('a stored opinion is labelled 上一版 when the row is back to pending', () => {
    // Record 53 §7.3: after a re-upload the row carries the PREVIOUS version's
    // opinion; the label must say so instead of reading as a current opinion.
    expect(embeddedReviewReasonLabel('pending_initial_review')).toBe('上一版意见')
    expect(embeddedReviewReasonLabel('rejected')).toBe('意见')
    expect(embeddedReviewReasonLabel('approved')).toBe('意见')
  })

  it('gate text shows pass with summary and failure with reason', () => {
    expect(embeddedGateText({ passed: true, test_summary: '12 passed · 40ms' })).toContain('✓')
    expect(embeddedGateText({ passed: true, test_summary: '12 passed · 40ms' })).toContain('12 passed')
    expect(embeddedGateText({ passed: false, reason: '含 process.env' })).toContain('process.env')
    expect(embeddedGateText(undefined)).toBe('—')
  })

  it('gate text includes the file count when the store stamped it (record 53 §7.1)', () => {
    expect(embeddedGateText({ passed: true, files: 3, test_summary: 'tests passed · 65ms' })).toBe(
      '✓ 通过 · 3 个文件 · tests passed · 65ms',
    )
    // old rows without the stamped count render exactly as before
    expect(embeddedGateText({ passed: true, files: 0 })).toBe('✓ 通过')
    expect(embeddedGateText({ passed: true })).toBe('✓ 通过')
  })

  it('gate text includes the source size when the store stamped it', () => {
    expect(embeddedGateText({ passed: true, files: 3, source_bytes: 2048 })).toContain('2.0 KB')
    expect(embeddedGateText({ passed: true, files: 3, source_bytes: 300 })).toContain('300 B')
    expect(embeddedGateText({ passed: true, files: 3 })).not.toContain('KB')
  })

  it('shows who/when requested the build only while the marker is actionable', () => {
    const at = '2026-11-12T10:00:00Z'
    expect(
      embeddedBuildRequest({
        state: 'build_requested',
        build_requested_by: '12345678-90ab-cdef-1234-567890abcdef',
        build_requested_at: at,
      }),
    ).toEqual({ by: '12345678…', at })
    // build_failed keeps the marker visible so the operator knows whom to ask.
    expect(
      embeddedBuildRequest({ state: 'build_failed', build_requested_at: at }),
    ).toEqual({ by: '未知', at })
    // A finalized row's marker belongs to history, not to the row.
    expect(
      embeddedBuildRequest({ state: 'testing', build_requested_at: at }),
    ).toBeNull()
    expect(embeddedBuildRequest({ state: 'build_requested' })).toBeNull()
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
