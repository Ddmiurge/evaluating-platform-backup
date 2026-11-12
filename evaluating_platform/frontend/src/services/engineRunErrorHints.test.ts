import { describe, expect, it } from 'vitest'

import {
  ENGINE_RUN_ERROR_EMBEDDED_PLUGIN_INVALID,
  ENGINE_RUN_ERROR_MODEL_REFUSED,
  engineRunFailureDisplay,
  explainEngineRunErrorCode,
} from './engineEval'

/**
 * The refusal code is the only generation failure the caller can act on, so it is
 * the only one that must always come with a readable next step (record 38).
 */
describe('explainEngineRunErrorCode', () => {
  it('explains the model-refusal code in terms of what to change', () => {
    const hint = explainEngineRunErrorCode(ENGINE_RUN_ERROR_MODEL_REFUSED)
    expect(hint).toContain('被测系统用途')
    expect(hint).toContain('中性')
    expect(hint).toContain('重试')
  })

  it('explains an embedded package contract failure as the author\'s fix', () => {
    // Record 54 §6.4: "0/2 tests" used to be indistinguishable from a runtime
    // failure. The hint must say the package is wrong AND that retrying the run
    // changes nothing.
    const hint = explainEngineRunErrorCode(ENGINE_RUN_ERROR_EMBEDDED_PLUGIN_INVALID)
    expect(hint).toContain('assert')
    expect(hint).toContain('重新上传')
    expect(hint).toContain('重试本次运行不会改变结果')
  })

  it('says nothing it does not know', () => {
    // An unknown code must stay unexplained rather than get a plausible-sounding
    // sentence that would stop the reader from investigating.
    expect(explainEngineRunErrorCode('some_future_code')).toBe('')
    expect(explainEngineRunErrorCode('')).toBe('')
    expect(explainEngineRunErrorCode(undefined)).toBe('')
    expect(explainEngineRunErrorCode(null)).toBe('')
  })
})

// engineRunFailureDisplay（P25）：错误解释与真实原因分离。
describe('engineRunFailureDisplay', () => {
  it('已知码给 hint，error_brief 给 reason，两者互不替代', () => {
    const view = engineRunFailureDisplay(ENGINE_RUN_ERROR_MODEL_REFUSED, '代码策略 custom:code:x 返回了空数组')
    expect(view.hint).not.toBe('')
    expect(view.reason).toBe('代码策略 custom:code:x 返回了空数组')
  })

  it('未知码 hint 为空串但不吞掉 reason —— brief 仍是要展示的唯一线索', () => {
    const view = engineRunFailureDisplay('brand_new_unknown_code', ' target call timed out')
    expect(view.hint).toBe('')
    expect(view.reason).toBe('target call timed out')
  })

  it('无码无 brief 时两行都不展示（空 trim，不编造「未知错误」）', () => {
    const view = engineRunFailureDisplay('', '   ')
    expect(view.hint).toBe('')
    expect(view.reason).toBe('')
  })
})
