import { describe, expect, it } from 'vitest'

import { addableStrategyOptions } from './ChatCards'
import { ENGINE_STRATEGY_OPTIONS, isStrategyRemoteGenerationGated, isStrategyRetired } from '../../services/engineEval'

/**
 * The plan card is the only place a user can correct a plan that arrived
 * without the strategy they asked for in chat. These assertions pin the two
 * things that make that correction usable: the dropdown never offers a strategy
 * the server would refuse, and it never offers the same strategy twice.
 */
describe('addableStrategyOptions', () => {
  it('offers the runnable catalog', () => {
    const options = addableStrategyOptions([], [])
    expect(options.length).toBeGreaterThan(0)
    expect(options.some((option) => option.value === 'promptfoo_strategy:base64')).toBe(true)
  })

  it('never offers a strategy the server refuses', () => {
    const values = addableStrategyOptions([], []).map((option) => option.value)
    for (const option of ENGINE_STRATEGY_OPTIONS) {
      const value = `promptfoo_strategy:${option.id}`
      const runnable = !isStrategyRetired(option.id) && !isStrategyRemoteGenerationGated(option.id)
      expect(values.includes(value)).toBe(runnable)
    }
    // The two classes exist in this catalog, so the filter is doing work.
    expect(ENGINE_STRATEGY_OPTIONS.some((option) => isStrategyRemoteGenerationGated(option.id))).toBe(true)
  })

  it('drops what the plan already selected and what was already added', () => {
    const planned = addableStrategyOptions([], []).map((option) => option.value)
    const values = addableStrategyOptions([planned[0]], [planned[1]]).map((option) => option.value)
    expect(values.includes(planned[0])).toBe(false)
    expect(values.includes(planned[1])).toBe(false)
  })
})
