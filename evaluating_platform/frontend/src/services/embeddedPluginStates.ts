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

/**
 * Label for a stored review opinion. `review_reason` is the LATEST opinion on
 * the row; after a re-upload (state back to `pending_initial_review`, version+1)
 * it still belongs to the PREVIOUS version until the new one is reviewed
 * (record 53 §7.3 — unlabeled it reads as an opinion on the current version).
 * The audit trail lives in `maclaw_embedded_plugin_events`; this label exists so
 * the row is not misread.
 */
export function embeddedReviewReasonLabel(state: string): string {
  return state === 'pending_initial_review' ? '上一版意见' : '意见'
}

/** The upload's result tag for the auto-gate line. */
export function embeddedGateText(autoGate: Record<string, unknown> | undefined): string {
  if (!autoGate) return '—'
  if (autoGate.passed === true) {
    // files/source_bytes are the SAFE numeric summary the store stamps
    // (record 53 §7.1 — the line used to read 「0 个文件」 because they were
    // never computed). Only shown when present so old rows render as before.
    const files = typeof autoGate.files === 'number' && autoGate.files > 0 ? ` · ${autoGate.files} 个文件` : ''
    const bytes =
      typeof autoGate.source_bytes === 'number' && autoGate.source_bytes > 0
        ? ` · ${formatSourceBytes(autoGate.source_bytes)}`
        : ''
    const summary = typeof autoGate.test_summary === 'string' && autoGate.test_summary ? ` · ${autoGate.test_summary}` : ''
    return `✓ 通过${files}${bytes}${summary}`
  }
  const reason = typeof autoGate.reason === 'string' ? autoGate.reason : '未通过'
  return `✗ ${reason}`
}

function formatSourceBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

/**
 * Who/when requested the build batch (E-6 criterion 4).
 *
 * The row already stored `build_requested_by/at`, but the admin list omitted
 * them: 「已请求构建」 could sit for days with no visible owner or age, and the
 * deploy host is a separate machine. Only shown for the two states where the
 * marker is the actionable fact — once the batch finalizes, the history is in
 * the audit events, not the row.
 */
export function embeddedBuildRequest(row: {
  state: string
  build_requested_by?: string
  build_requested_at?: string
}): { by: string; at: string } | null {
  if (row.state !== 'build_requested' && row.state !== 'build_failed') return null
  if (!row.build_requested_at) return null
  return {
    by: row.build_requested_by ? `${row.build_requested_by.slice(0, 8)}…` : '未知',
    at: row.build_requested_at,
  }
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
