import type { PlanInfo } from './chat'

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function firstString(...values: unknown[]) {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return ''
}

function stringArray(value: unknown) {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
}

function optionalNumber(value: unknown) {
  if (typeof value === 'number' && !Number.isNaN(value)) return value
  if (typeof value === 'string' && value.trim()) {
    const parsed = Number(value)
    if (!Number.isNaN(parsed)) return parsed
  }
  return undefined
}

function stripMarkdownJSONFence(value: string) {
  const text = value.trim()
  const embeddedFence = text.match(/```(?:json)?\s*([\s\S]*?)```/i)
  if (embeddedFence?.[1]?.trim()) return embeddedFence[1].trim()
  if (!text.startsWith('```')) return text
  const lines = text.split(/\r?\n/)
  if (lines.length < 3) return text
  const end = [...lines].reverse().findIndex(line => line.trim().startsWith('```'))
  if (end < 0) return text
  const endIndex = lines.length - 1 - end
  if (endIndex <= 0) return text
  return lines.slice(1, endIndex).join('\n').trim()
}

function parseJSONRecord(value: unknown): Record<string, unknown> | undefined {
  if (isRecord(value)) return value
  if (typeof value !== 'string') return undefined
  try {
    const parsed = JSON.parse(stripMarkdownJSONFence(value))
    return isRecord(parsed) ? parsed : undefined
  } catch {
    return undefined
  }
}

function riskTypesFrom(value: unknown) {
  if (!Array.isArray(value)) return []
  return value
    .map(item => {
      if (typeof item === 'string') return item.trim()
      if (isRecord(item)) return firstString(item.risk, item.type, item.name)
      return ''
    })
    .filter(Boolean)
}

function selectedCapabilityRefs(source: Record<string, unknown>) {
  const refs = new Set<string>()
  const add = (value: unknown) => {
    const ref = firstString(value)
    if (ref) refs.add(ref)
  }
  stringArray(source.resource_handles ?? source.resourceHandles).forEach(add)
  for (const item of Array.isArray(source.selected_capability_refs) ? source.selected_capability_refs : []) {
    if (typeof item === 'string') {
      add(item)
    } else if (isRecord(item)) {
      add(item.source_ref ?? item.ref ?? item.handle ?? item.name ?? item.id)
    }
  }
  for (const item of Array.isArray(source.selected_capabilities) ? source.selected_capabilities : []) {
    if (isRecord(item)) add(item.source_ref ?? item.ref ?? item.handle ?? item.name ?? item.id)
  }
  return [...refs]
}

function selectedSkillNames(source: Record<string, unknown>) {
  const names = new Set<string>()
  const add = (value: unknown) => {
    const name = firstString(value)
    if (name) names.add(name)
  }
  add(source.skill_name)
  for (const item of Array.isArray(source.selected_skills) ? source.selected_skills : []) {
    if (typeof item === 'string') {
      add(item)
    } else if (isRecord(item)) {
      add(item.skill_name ?? item.source_ref ?? item.name ?? item.ref ?? item.id)
    }
  }
  return [...names]
}

function selectionReasons(source: Record<string, unknown>) {
  const reasons: string[] = []
  const add = (value: unknown) => {
    const reason = firstString(value)
    if (reason) reasons.push(reason)
  }
  for (const item of Array.isArray(source.selection_reasons) ? source.selection_reasons : []) {
    if (typeof item === 'string') {
      add(item)
    } else if (isRecord(item)) {
      add(item.reason ?? item.summary ?? item.description)
    }
  }
  return [...new Set(reasons)]
}

export function runtimeContentHasPlanConfirm(value: unknown) {
  const body = parseJSONRecord(value)
  if (!body) return false
  return firstString(body.response_source) === 'plan_confirm'
}

export function parseRuntimePlan(value: unknown): PlanInfo | undefined {
  const raw = parseJSONRecord(value)
  if (!raw) return undefined
  const source = isRecord(raw.plan) ? raw.plan : isRecord(raw.card) ? raw.card : raw
  const targetSummary = isRecord(source.target_summary) ? source.target_summary : {}
  const assessmentTypes = [
    ...stringArray(source.assessment_types ?? source.types ?? source.assessmentTypes),
    ...riskTypesFrom(source.risk_types),
  ]
  const resourceHandles = selectedCapabilityRefs(source)
  const selectedSkills = selectedSkillNames(source)
  const reasons = selectionReasons(source)
  const resourceMode = firstString(source.resource_mode_preference, source.resourceMode, source.resource_mode)
    || (selectedSkills.length > 0 || resourceHandles.some(ref => ref.toLowerCase().startsWith('skillhub:')) ? 'skill_generated' : '')

  return {
    name: firstString(source.name, source.title, source.task_name) || 'AI 安全评测计划',
    goal: firstString(source.goal, source.summary, source.description, targetSummary.assessment_scope) || '围绕目标系统开展安全评测。',
    target_type: firstString(source.target_type, source.targetType, source.provider, targetSummary.target_type) || 'llm',
    target_url: firstString(source.target_url, source.targetUrl, source.base_url),
    target_key: firstString(source.target_key, source.targetKey),
    target_model: firstString(source.target_model, source.targetModel, source.model),
    target_id: firstString(source.target_id, source.targetId),
    assessment_types: assessmentTypes.length > 0 ? [...new Set(assessmentTypes)] : ['llm_safety'],
    resource_mode_preference: resourceMode || undefined,
    resource_handles: resourceHandles,
    selected_skills: selectedSkills,
    selected_capability_refs: [...new Set([...resourceHandles, ...selectedSkills])],
    selection_reasons: reasons,
    selection_strategy: firstString(source.selection_strategy, source.selectionStrategy),
    test_count: optionalNumber(source.test_count ?? source.testCount),
  }
}
