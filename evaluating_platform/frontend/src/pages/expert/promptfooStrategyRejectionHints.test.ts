/**
 * Phase 3 开篇 · 任务 2 · 前端「拒绝原因」映射测试（自定义策略）。
 *
 * # 这层测试在守什么
 *
 * 与 U10 插件那张表同一件事：DoD 要求「JS 形态被拒绝且提示清晰」。后端返回
 * code + 中文原文，专家看到的是前端渲染的**解读**。解读一旦与后端 code 脱节，
 * 就会出现「后端拒了、前端说别的」——那是清晰提示的反面，而且不会让任何其他
 * 测试变红。
 *
 * 表与组件共享同一个模块，所以这里断言的就是组件真正使用的映射。
 */
import { describe, expect, it } from 'vitest'
import {
  BACKEND_STRATEGY_REJECTION_CODES,
  PROMPTFOO_STRATEGY_REJECTION_HINTS,
  promptfooStrategyRejectionHint,
} from './promptfooStrategyRejectionHints'

describe('Phase 3 · 策略拒绝原因映射', () => {
  it('covers every rejection code the backend can emit', () => {
    const missingCodes = BACKEND_STRATEGY_REJECTION_CODES.filter(
      (code) => !(code in PROMPTFOO_STRATEGY_REJECTION_HINTS),
    )
    expect(missingCodes).toEqual([])
  })

  it('has no hint for a code the backend does not emit', () => {
    const extraCodes = Object.keys(PROMPTFOO_STRATEGY_REJECTION_HINTS).filter(
      (code) => !(BACKEND_STRATEGY_REJECTION_CODES as readonly string[]).includes(code),
    )
    expect(extraCodes).toEqual([])
  })

  it('tells the expert what to do, not just what happened', () => {
    for (const [code, hint] of Object.entries(PROMPTFOO_STRATEGY_REJECTION_HINTS)) {
      expect(hint.length, `${code} 的解读为空`).toBeGreaterThan(0)
      expect(/请|不允许|必须|限/.test(hint), `${code} 的解读没有给出可执行的下一步：${hint}`).toBe(true)
    }
  })

  it('states the real reason the JS form is refused', () => {
    // 对策略而言，JS 形态不是「暂不支持」：promptfoo 的 file:// 策略**只**
    // 接受 .js/.ts（loadStrategy 校验 isJavascriptFile），所以这不是一个被
    // 限制的同类形态，而是一个更危险的不同形态。只说「不支持 JS」而不说
    // 为什么，专家会去猜是不是自己写错了。
    const hint = PROMPTFOO_STRATEGY_REJECTION_HINTS.pf_strategy_form_unsupported
    expect(hint).toContain('任意代码')
    expect(hint).toContain('YAML')
  })

  it('explains that the remote-dependency rejection is a platform policy', () => {
    expect(PROMPTFOO_STRATEGY_REJECTION_HINTS.plugin_requires_remote_generation).toContain('远程生成')
  })

  it('explains that the endpoint cannot be chosen from the package', () => {
    // redteamProvider 由引擎侧注入；包内指定等于让已发布内容改写攻击模型的
    // 调用目标，所以这条提示要说明「不是你不该填，而是不能填」。
    expect(PROMPTFOO_STRATEGY_REJECTION_HINTS.pf_strategy_remote_reference_forbidden).toContain(
      'redteamProvider',
    )
  })

  it('returns undefined for an unknown code so the caller can show the backend text', () => {
    expect(promptfooStrategyRejectionHint('some_future_code')).toBeUndefined()
    expect(promptfooStrategyRejectionHint(undefined)).toBeUndefined()
    expect(promptfooStrategyRejectionHint('pf_strategy_definition_invalid')).toContain('strategyText')
  })
})