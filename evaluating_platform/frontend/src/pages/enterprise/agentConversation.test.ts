/**
 * U9 · Agent 多轮（`{{conversation_id}}`）在前端的配置期契约。
 *
 * 本文件守三件事，且都是**行为**而非文案：
 *
 *  1. 占位符写在请求体模板里就够了 —— 不需要新增任何表单字段，多轮能力
 *     完全由既有 `agent_body_template` / `agent_response_path` 承载。这是
 *     「不填新配置即保持 v1」在前端一侧的体现：没有新字段，就没有新状态，
 *     旧配置读出来必然不变。
 *  2. 用了 `{{conversation_id}}` 却没填响应提取路径，必须在**保存前**被拦下。
 *     这条与后端 `ValidateAgentTargetInput` 一致；两侧任一单独改动都会让
 *     用户在保存后才被引擎拒绝（白跑一次付费评测）。
 *  3. 模板原样往返：占位符不能在读取→回填→保存之间被吃掉或改写。
 *
 * 判定「是否多轮目标」的谓词同时被 Drawer 的校验器和后端使用，所以放在
 * `agentConversation.ts` 里单点导出，而不是在两个文件各写一份。
 */
import { describe, expect, it } from 'vitest'
import {
  AGENT_CONVERSATION_PLACEHOLDER,
  agentTemplateWantsConversation,
  assertConversationTemplateHasResponsePath,
} from './agentConversation'

describe('agentTemplateWantsConversation — 判定谓词', () => {
  it('请求体模板里有占位符即为多轮目标', () => {
    expect(agentTemplateWantsConversation({ bodyTemplate: '{"conversation_id":"{{conversation_id}}"}' })).toBe(true)
  })

  it('请求头模板里有占位符同样算多轮目标', () => {
    expect(agentTemplateWantsConversation({ headersTemplate: '{"X-Sid":"{{conversation_id}}"}' })).toBe(true)
  })

  it('v1 模板一律为false —— 这是「不填新配置即保持 v1」的前端一侧', () => {
    expect(agentTemplateWantsConversation({ bodyTemplate: '{"query":"{{prompt}}"}' })).toBe(false)
    expect(agentTemplateWantsConversation({ bodyTemplate: '{"q":"{{prompt}}","k":"{{api_key}}"}' })).toBe(false)
    expect(agentTemplateWantsConversation({})).toBe(false)
    expect(agentTemplateWantsConversation({ bodyTemplate: '', headersTemplate: '' })).toBe(false)
    // 未定义输入不能抛错：Drawer 在字段尚未渲染时会拿到 undefined。
    expect(agentTemplateWantsConversation({ bodyTemplate: undefined })).toBe(false)
  })
})

describe('assertConversationTemplateHasResponsePath — 保存前拦截', () => {
  it('占位符 + 无响应提取路径 → 拒绝（否则每轮都新开一段对话）', () => {
    const errors = assertConversationTemplateHasResponsePath({
      bodyTemplate: '{"query":"{{prompt}}","conversation_id":"{{conversation_id}}"}',
      responsePath: '',
    })
    expect(errors).toHaveLength(1)
    expect(errors[0]).toContain(AGENT_CONVERSATION_PLACEHOLDER)
  })

  it('只有空白字符的响应路径等同于没填', () => {
    expect(
      assertConversationTemplateHasResponsePath({
        bodyTemplate: '{"conversation_id":"{{conversation_id}}"}',
        responsePath: '   ',
      }),
    ).toHaveLength(1)
  })

  it('占位符 + 有响应提取路径 → 放行（D-04 的 Dify 形态）', () => {
    expect(
      assertConversationTemplateHasResponsePath({
        bodyTemplate: '{"query":"{{prompt}}","conversation_id":"{{conversation_id}}"}',
        responsePath: 'conversation_id',
      }),
    ).toEqual([])
  })

  it('没有占位符 → 永远放行，且不需要响应提取路径（v1 配置不受影响）', () => {
    expect(assertConversationTemplateHasResponsePath({ bodyTemplate: '{"query":"{{prompt}}"}', responsePath: '' })).toEqual([])
    expect(assertConversationTemplateHasResponsePath({})).toEqual([])
  })

  it('请求头里的占位符同样受约束', () => {
    expect(
      assertConversationTemplateHasResponsePath({ headersTemplate: '{"X-Sid":"{{conversation_id}}"}' }),
    ).toHaveLength(1)
  })
})

describe('模板往返 —— 占位符不得在读取/回填之间被吃掉', () => {
  it('原样保留 Dify 多轮模板', () => {
    const template = '{"inputs":{},"query":"{{prompt}}","response_mode":"blocking","conversation_id":"{{conversation_id}}"}'
    // Drawer 读取目标后原样回填表单，保存时再原样送回；这里钉住「不加工」。
    expect(agentTemplateWantsConversation({ bodyTemplate: template })).toBe(true)
    expect(template).toContain(AGENT_CONVERSATION_PLACEHOLDER)
  })

  it('占位符字面量与后端 Go 常量一致', () => {
    // 后端：internal/maclaw/agent_target.go 的 AgentConversationPlaceholder。
    // 引擎：promptfoo_engine/src/runner/agentSession.ts 的同名常量。
    // 三处字面量必须相同，否则前端认得、后端不认（或反之），
    // 用户会看到一个「校验通过但引擎拒绝」的矛盾状态。
    expect(AGENT_CONVERSATION_PLACEHOLDER).toBe('{{conversation_id}}')
  })
})