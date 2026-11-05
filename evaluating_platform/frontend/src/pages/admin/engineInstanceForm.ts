/**
 * 专属引擎登记的前端预检（P26c.1）。
 *
 * 与后端 `maclaw.ValidateEngineInstanceBaseURL` 同规则（后端仍是权威——这里只是
 * 把注定 400 的输入挡在点击之前）。镜像测试：
 * `TestFrontendEngineInstanceValidationMirrorsBackend`。
 */

export const ENGINE_URL_MAX_LEN = 200

export function validateEngineBaseUrl(raw: string): string | null {
  const v = String(raw ?? '').trim()
  if (v === '' || v.length > ENGINE_URL_MAX_LEN) {
    return '地址不能为空且不超过 200 字符'
  }
  let u: URL
  try {
    u = new URL(v)
  } catch {
    return '必须是完整的 http(s) 地址，如 http://promptfoo-engine-acme:8090'
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    return '仅支持 http(s)'
  }
  if (!u.hostname) {
    return '缺少主机名'
  }
  if (u.username || u.password) {
    return '地址不允许携带凭据'
  }
  if (u.search) {
    return '地址不允许携带查询参数'
  }
  if (u.hash) {
    return '地址不允许携带片段'
  }
  return null
}

export function normalizeEngineBaseUrl(raw: string): string {
  return String(raw ?? '').trim().replace(/\/+$/, '')
}

/** 登记成功后的固定提示（token 一次性语义，与后端 note 同旨，UI 再钉一遍）。 */
export const ENGINE_TOKEN_ONCE_NOTICE = '该 token 只显示这一次：写进部署目录 .env 并创建该租户的引擎服务块后，才可开启 MACLAW_TENANT_ENGINES。丢失只能重新登记轮换。'

/** P26c.2 探活状态 → 面板标签。后端 `maclaw.EngineInstanceHealth` 同一组字面量。 */
export interface EngineInstanceHealthTag {
  color: string
  label: string
  hint?: string
}

export function engineInstanceHealthTag(health?: string): EngineInstanceHealthTag | null {
  switch (health) {
    case 'ok':
      return { color: 'green', label: '可用' }
    case 'token_mismatch':
      return {
        color: 'orange',
        label: 'token 未部署',
        hint: '登记已轮换 token，但容器还在用旧的：把新 token 写进 .env 并 recreate 该引擎服务，否则该租户的每次评测都会 401。',
      }
    case 'unreachable':
      return { color: 'red', label: '实例不可达', hint: '容器没起或已挂：docker compose up -d promptfoo-engine-p8test。' }
    case 'incomplete':
      return { color: 'orange', label: '半登记', hint: '有地址但缺 token 密文：重新登记一次。' }
    default:
      return null // 未探活 / 探活失败：不显示，宁缺毋滥
  }
}
