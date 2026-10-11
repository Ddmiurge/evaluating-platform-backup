/**
 * agentConversation.ts — U9 · 多轮会话配置的前端侧谓词与校验。
 *
 * # 为什么单独一个模块
 *
 * 「这个 Agent 目标是不是多轮目标」这个判定必须和后端
 * （`internal/maclaw/agent_target.go` 的 `AgentTargetWantsConversation`）
 * 以及引擎（`promptfoo_engine/src/runner/agentSession.ts` 的
 * `wantsAgentConversation`）**完全一致**。三处各写一份，迟早漂移；漂移之后
 * 用户会看到一个自相矛盾的状态：前端放行 → 后端保存成功 → 引擎在跑了之后
 * 才拒绝，或者反过来。多轮是否可用不是 UI 层的自由裁量。
 *
 * # 为什么没有新增表单字段
 *
 * 多轮能力完全由既有的 `agent_body_template`（写 `{{conversation_id}}`）与
 * `agent_response_path`（填会话 id 所在字段）承载，**没有新字段**。
 * 这是「不填新配置时行为与 v1 完全一致」在前端一侧的兑现方式：没有新状态，
 * 旧配置读出来必然与从前逐字节相同，不需要任何迁移或兼容分支。
 */

/** 操作员书写的会话 id 占位符，字面量必须与 Go/引擎侧一致。 */
export const AGENT_CONVERSATION_PLACEHOLDER = '{{conversation_id}}'

/** Drawer 读到的 Agent 模板字段（其余字段与判定无关）。 */
export interface AgentConversationTemplates {
  bodyTemplate?: string
  headersTemplate?: string
}

/**
 * 该 Agent 目标是否要求服务端托管的多轮会话。
 *
 * 与 Go 侧 `AgentTargetWantsConversation` 同义：只要在请求体**或**请求头模板
 * 里写了占位符即为真。请求头也算，是因为有些 Agent 网关把会话 id 放在
 * 自定义头里而不是 body 字段。
 */
export function agentTemplateWantsConversation(templates: AgentConversationTemplates): boolean {
  return (
    containsPlaceholder(templates.bodyTemplate) || containsPlaceholder(templates.headersTemplate)
  )
}

function containsPlaceholder(template?: string): boolean {
  return typeof template === 'string' && template.includes(AGENT_CONVERSATION_PLACEHOLDER)
}

/**
 * 保存前的拦截：用了会话占位符就必须能取回会话 id。
 *
 * 返回错误文案数组（空数组 = 放行），而不是抛异常或返回布尔 —— 这样它能直接
 * 喂给 antd Form 的 `validator`，调用方不必再包一层。
 *
 * 错误文案里带上占位符本身：用户看到的是「你写的这个东西还缺一个字段」，
 * 而不是一句「响应提取路径必填」—— 后者在只关心多轮的语境下会让人误以为
 * v1 单轮配置也必须填。
 */
export function assertConversationTemplateHasResponsePath(input: {
  bodyTemplate?: string
  headersTemplate?: string
  responsePath?: string
}): string[] {
  if (!agentTemplateWantsConversation(input)) return []
  if (String(input.responsePath ?? '').trim()) return []
  return [
    `用了 ${AGENT_CONVERSATION_PLACEHOLDER} 就必须填写下方「响应提取路径」——` +
      '平台需要知道从响应的哪个字段取回会话 id，否则每轮都会新开一段对话，' +
      '报告却显示多轮。',
  ]
}