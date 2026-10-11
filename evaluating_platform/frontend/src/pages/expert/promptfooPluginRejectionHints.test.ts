/**
 * U10 · 前端「拒绝原因」映射测试。
 *
 * # 这层测试在守什么
 *
 * DoD 要求「JS 形态被拒绝并有**清晰提示**」。后端已经返回 code + 中文原文，
 * 但专家看到的是前端渲染的那一句**解读**。解读一旦与后端 code 脱节，就会出现
 * 「后端拒了，前端说别的」——那正是清晰提示的反面，而且不会让任何其他测试变红。
 *
 * 表与组件共享同一个模块（`promptfooPluginRejectionHints.ts`），所以这里断言的
 * 就是组件真正使用的映射，而不是一份副本。
 */
import { describe, expect, it } from 'vitest'
import {
  BACKEND_REJECTION_CODES,
  PROMPTFOO_PLUGIN_REJECTION_HINTS,
  promptfooPluginRejectionHint,
} from './promptfooPluginRejectionHints'

describe('U10 · 拒绝原因映射', () => {
  it('covers every rejection code the backend can emit', () => {
    const missingCodes = BACKEND_REJECTION_CODES.filter(
      (code) => !(code in PROMPTFOO_PLUGIN_REJECTION_HINTS),
    )
    expect(missingCodes).toEqual([])
  })

  it('has no hint for a code the backend does not emit', () => {
    // 多余的解读意味着有一段永远不会展示的文案，而它会悄悄过期。
    const extraCodes = Object.keys(PROMPTFOO_PLUGIN_REJECTION_HINTS).filter(
      (code) => !(BACKEND_REJECTION_CODES as readonly string[]).includes(code),
    )
    expect(extraCodes).toEqual([])
  })

  it('tells the expert what to do, not just what happened', () => {
    for (const [code, hint] of Object.entries(PROMPTFOO_PLUGIN_REJECTION_HINTS)) {
      expect(hint.length, `${code} 的解读为空`).toBeGreaterThan(0)
      // 每条解读都要给出下一步，而不是只描述失败。
      expect(/请|不允许|改用|需要/.test(hint), `${code} 的解读没有给出可执行的下一步：${hint}`).toBe(true)
    }
  })

  it('states the reason for the JS rejection rather than only the rule', () => {
    // DoD：JS/脚本形态被拒绝并有清晰提示。只说「不支持 JS」而不说为什么，
    // 专家会去猜是不是自己去错了；而真实原因是任意代码执行。
    expect(PROMPTFOO_PLUGIN_REJECTION_HINTS.pf_plugin_form_unsupported).toContain('任意代码')
  })

  it('explains that the remote-dependency rejection is a platform policy', () => {
    expect(PROMPTFOO_PLUGIN_REJECTION_HINTS.plugin_requires_remote_generation).toContain('远程生成')
  })

  it('returns undefined for an unknown code so the caller can show the backend text', () => {
    // 后端原文（如「自定义插件 id "harmful" 与内置目录中的插件同名…」）比任何
    // 通用提示都具体，兜底会把它替换掉。
    expect(promptfooPluginRejectionHint('some_future_code')).toBeUndefined()
    expect(promptfooPluginRejectionHint(undefined)).toBeUndefined()
    expect(promptfooPluginRejectionHint('pf_plugin_form_unsupported')).toContain('YAML')
  })
})