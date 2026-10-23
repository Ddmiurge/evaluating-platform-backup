/**
 * planRefGroups.test.ts — the plan card's capability chips, split by axis.
 *
 * Before 2026-10-16 the card ran every ref through `displayCapabilityName`, which
 * has no branch for the promptfoo prefixes — so it literally rendered
 * `promptfoo_plugin:prompt-extraction` and `promptfoo_strategy:base64` in one
 * undifferentiated row. The user's request was to show the strategies properly
 * and to be able to drop one.
 */
import { describe, expect, it } from 'vitest'
import { planRefGroups } from './ChatCards'
import type { PlanInfo } from '../../services/chat'

function plan(overrides: Partial<PlanInfo>): PlanInfo {
  return {
    name: '评测',
    goal: '合规安全评测',
    target_type: 'agent',
    target_url: '',
    target_key: '',
    target_model: 'm',
    assessment_types: [],
    ...overrides,
  }
}

describe('planRefGroups', () => {
  it('splits plugins and strategies into separate rows with readable names', () => {
    // The exact shape measured on ai02 (阶段四服务器验证记录 §3).
    const groups = planRefGroups(plan({
      selected_capability_refs: ['promptfoo_plugin:prompt-extraction', 'promptfoo_strategy:base64'],
    }))
    expect(groups.plugins.map((chip) => chip.label)).toEqual(['系统提示词窃取'])
    expect(groups.strategies.map((chip) => chip.label)).toEqual(['Base64 编码'])
    expect(groups.others).toEqual([])
    // The chip has to remember the ref verbatim: a removal names the ref.
    expect(groups.strategies[0].ref).toBe('promptfoo_strategy:base64')
  })

  it('falls back to the raw id for an unknown id instead of dropping it', () => {
    // A ref we cannot name is still something the user selected; hiding it would
    // make the card claim a smaller selection than the run will use.
    const groups = planRefGroups(plan({
      selected_capability_refs: ['promptfoo_plugin:not-in-catalog'],
    }))
    expect(groups.plugins.map((chip) => chip.label)).toEqual(['not-in-catalog'])
  })

  it('leaves skills and expert resources in the 使用能力 group', () => {
    const groups = planRefGroups(plan({
      selected_capability_refs: ['sample:123', 'promptfoo_plugin:harmful'],
      selected_skills: ['skillhub:ccbos'],
    }))
    expect(groups.plugins.map((chip) => chip.label)).toEqual(['有害内容检测'])
    expect(groups.others.length).toBe(2)
  })

  it('dedupes a ref listed twice', () => {
    const groups = planRefGroups(plan({
      selected_capability_refs: ['promptfoo_strategy:base64', 'promptfoo_strategy:base64'],
      resource_handles: ['promptfoo_strategy:base64'],
    }))
    expect(groups.strategies.length).toBe(1)
  })
})

describe('planRefGroups — expert packages', () => {
  // A published package belongs on the same axis as the built-ins: a reader
  // should not have to know that one of the two "attack strategies" came from
  // an expert. Before 2026-10-23 these refs fell into 使用能力, so the card
  // showed a strategy that was not on the strategy row — and the confirm path
  // dropped it entirely.
  it('puts a published plugin and strategy on their own axes, marked by origin', () => {
    const groups = planRefGroups(plan({
      selected_capability_refs: ['pf_plugin:acme-generator', 'pf_strategy:progressive-probe'],
    }))
    expect(groups.plugins.map((chip) => chip.ref)).toEqual(['pf_plugin:acme-generator'])
    expect(groups.plugins[0].label).toBe('acme-generator（专家发布）')
    expect(groups.strategies.map((chip) => chip.ref)).toEqual(['pf_strategy:progressive-probe'])
    expect(groups.strategies[0].label).toBe('progressive-probe（专家发布）')
    expect(groups.others).toEqual([])
  })
})
