/**
 * Embedded-plugin state presentation (P28) — shared by the expert portal's
 * 「内嵌插件」 page and the admin's 「内嵌插件审批」 page.
 *
 * The labels mirror `maclaw.EmbeddedStateLabel` (Go is the source of truth for
 * the machine states; this file owns only presentation: tag colors, which
 * actions each state offers). The mirror is enforced by
 * `TestEmbeddedStateLabelsMatchFrontendMirror` — a one-sided rename fails the
 * backend suite.
 */

export interface EmbeddedStateTag {
  color: string
  label: string
}

export const EMBEDDED_STATE_LABELS: Record<string, string> = {
  pending_initial_review: '待初审',
  rejected: '已驳回',
  approved_for_build: '已批准·待构建',
  build_requested: '已请求构建',
  build_failed: '构建失败',
  testing: '测试中',
  approved: '已通过',
  disabled: '已停用',
  remove_requested: '标记移除',
  removed: '已移除',
}

const STATE_COLORS: Record<string, string> = {
  pending_initial_review: 'gold',
  rejected: 'red',
  approved_for_build: 'blue',
  build_requested: 'geekblue',
  build_failed: 'volcano',
  testing: 'purple',
  approved: 'green',
  disabled: 'default',
  remove_requested: 'orange',
  removed: 'default',
}

export function embeddedStateTag(state: string): EmbeddedStateTag {
  return {
    label: EMBEDDED_STATE_LABELS[state] ?? state,
    color: STATE_COLORS[state] ?? 'default',
  }
}

export type EmbeddedAdminAction =
  | 'approve'
  | 'changes'
  | 'request-build'
  | 'final-approve'
  | 'disable'
  | 'enable'
  | 'remove'
  | 'unmark'

/** Admin actions legal for a state — mirrors the Go transition table. */
export function embeddedAdminActions(state: string): EmbeddedAdminAction[] {
  switch (state) {
    case 'pending_initial_review':
      return ['approve', 'changes']
    case 'approved_for_build':
    case 'build_failed':
      return ['request-build']
    case 'build_requested':
      return [] // the build tool owns this state
    case 'testing':
      return ['final-approve']
    case 'approved':
      return ['disable', 'remove']
    case 'disabled':
      return ['enable', 'remove']
    case 'remove_requested':
      return ['unmark']
    default:
      return [] // rejected (re-upload is the expert's move) / removed (terminal)
  }
}

/** What the expert may do: revise anything not yet removed. */
export function embeddedExpertCanRevise(state: string): boolean {
  return state !== 'removed'
}

/** The upload's result tag for the auto-gate line. */
export function embeddedGateText(autoGate: Record<string, unknown> | undefined): string {
  if (!autoGate) return '—'
  if (autoGate.passed === true) {
    const summary = typeof autoGate.test_summary === 'string' && autoGate.test_summary ? ` · ${autoGate.test_summary}` : ''
    return `✓ 通过${summary}`
  }
  const reason = typeof autoGate.reason === 'string' ? autoGate.reason : '未通过'
  return `✗ ${reason}`
}

/** ArrayBuffer → base64 (chunked; File.arrayBuffer() at the call site). */
export function arrayBufferToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  const chunk = 0x8000
  let binary = ''
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(binary)
}
