import { describe, expect, it } from 'vitest'

import { ENGINE_RUN_ERROR_MODEL_REFUSED, explainEngineRunErrorCode } from './engineEval'

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

  it('says nothing it does not know', () => {
    // An unknown code must stay unexplained rather than get a plausible-sounding
    // sentence that would stop the reader from investigating.
    expect(explainEngineRunErrorCode('some_future_code')).toBe('')
    expect(explainEngineRunErrorCode('')).toBe('')
    expect(explainEngineRunErrorCode(undefined)).toBe('')
    expect(explainEngineRunErrorCode(null)).toBe('')
  })
})
