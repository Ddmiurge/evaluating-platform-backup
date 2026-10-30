/**
 * 专家门户「能力目录」的纯分组逻辑（P23，2026-10-30）。
 *
 * 需求：专家在发布页里就能看见**当前所有的插件和策略**——promptfoo 内置目录
 * 按原生 category 分组 + 全体专家已发布的包（`include_published_catalog`），
 * 本账号发布的特殊标注，**不显示发布者是谁**。
 *
 * 单独成模块（无 JSX）是因为本仓库的 vitest 跑在 node 环境：页面组件测不了，
 * 数据推导必须可测。内置目录 import 的是 `engineEval.ts` 的同源镜像，
 * 与后端集合的一致性已由 `TestFrontendOptionsConsistency` 在 CI 钉死，
 * 这里不再维护第二份清单。
 */
import {
  ENGINE_PLUGIN_CATEGORY_LABELS,
  ENGINE_PLUGIN_OPTIONS,
  ENGINE_STRATEGY_OPTIONS,
  RETIRED_ENGINE_PLUGINS,
  RETIRED_ENGINE_STRATEGIES,
  toEngineSelectOption,
} from '../../services/engineEval'

export type CatalogRowSource = 'builtin' | 'package' | 'retired'

/** 目录里一行已发布包（由页面从后端行映射而来；派生字段已在 service 层做完）。 */
export interface CatalogPackageRow {
  id: string
  handle: string
  name: string
  version?: string
  status: string
  enabled: boolean
  /** 'yaml' | 'js'（策略包形态；插件包通常缺省）。 */
  form?: string
  /** metadata.is_own === 'true'：仅专家目录请求时后端才给。 */
  isOwn?: boolean
  /** 包声明的引擎 id（pf_plugin_id / pf_strategy_id）。 */
  declaredId?: string
  updatedAt?: string
}

export interface CatalogRow {
  key: string
  source: CatalogRowSource
  name: string
  /** 内置行：promptfoo id；包行：声明 id，未声明则 handle。 */
  id: string
  fanout?: number
  /** 被远程生成闸门拦住：本环境永远跑不了（灰态而非隐藏，与向导口径一致）。 */
  disabled?: boolean
  /** disabled 的原因说明。 */
  reason?: string
  /** 可选但缺配置（如 `policy`）：不是灰态，只是提醒。 */
  configReason?: string
  /** 已下线项：原因 + 替代路径。 */
  retired?: { reason: string; replacementLabel: string; replacementIds: string[] }
  /** 仅包行：页面据此渲染操作（重新发布/下架）与状态标签。 */
  package?: CatalogPackageRow
}

export interface CatalogGroup {
  key: string
  label: string
  rows: CatalogRow[]
}

export const PLUGIN_PACKAGE_GROUP_KEY = 'published-packages'
export const STRATEGY_PACKAGE_GROUP_KEY = PLUGIN_PACKAGE_GROUP_KEY
export const RETIRED_GROUP_KEY = 'retired'

function packageRow(item: CatalogPackageRow): CatalogRow {
  return {
    key: `package:${item.id}`,
    source: 'package',
    name: item.name,
    id: item.declaredId || item.handle,
    package: item,
  }
}

function packageGroup(packages: CatalogPackageRow[]): CatalogGroup {
  return {
    key: PLUGIN_PACKAGE_GROUP_KEY,
    label: '专家发布包',
    rows: packages.map(packageRow),
  }
}

/** 内置插件按 promptfoo 原生 category 分组，顺序 = 目录里类别首次出现的顺序（与向导 optGroup 同口径）。 */
export function buildPluginCatalogGroups(packages: CatalogPackageRow[]): CatalogGroup[] {
  const byCategory = new Map<string, CatalogRow[]>()
  for (const option of ENGINE_PLUGIN_OPTIONS) {
    const select = toEngineSelectOption(option)
    const row: CatalogRow = {
      key: `builtin:${option.id}`,
      source: 'builtin',
      name: option.name,
      id: option.id,
      fanout: option.fanout,
      disabled: select.disabled || undefined,
      reason: select.reason,
      configReason: select.configReason,
    }
    const bucket = byCategory.get(option.category)
    if (bucket) {
      bucket.push(row)
    } else {
      byCategory.set(option.category, [row])
    }
  }
  const groups: CatalogGroup[] = []
  for (const [category, rows] of byCategory) {
    groups.push({
      key: `builtin-${category}`,
      label: `内置 · ${ENGINE_PLUGIN_CATEGORY_LABELS[category] ?? category}`,
      rows,
    })
  }
  groups.push(packageGroup(packages))
  if (RETIRED_ENGINE_PLUGINS.length > 0) {
    groups.push({
      key: RETIRED_GROUP_KEY,
      label: `已下线（${RETIRED_ENGINE_PLUGINS.length}）`,
      rows: RETIRED_ENGINE_PLUGINS.map((item) => ({
        key: `retired:${item.id}`,
        source: 'retired' as const,
        name: item.name,
        id: item.id,
        retired: {
          reason: item.reason,
          replacementLabel: item.replacement_label,
          replacementIds: [...item.replacement_ids],
        },
      })),
    })
  }
  return groups
}

/** 策略轴没有原生分类：内置一条平组 + 专家发布包 + 已下线（与向导「扁平策略 + 专家发布殿后」一致）。 */
export function buildStrategyCatalogGroups(packages: CatalogPackageRow[]): CatalogGroup[] {
  const groups: CatalogGroup[] = [
    {
      key: 'builtin-strategies',
      label: '内置策略',
      rows: ENGINE_STRATEGY_OPTIONS.map((option) => {
        const select = toEngineSelectOption(option)
        return {
          key: `builtin:${option.id}`,
          source: 'builtin' as const,
          name: option.name,
          id: option.id,
          disabled: select.disabled || undefined,
          reason: select.reason,
          configReason: select.configReason,
        }
      }),
    },
    packageGroup(packages),
  ]
  if (RETIRED_ENGINE_STRATEGIES.length > 0) {
    groups.push({
      key: RETIRED_GROUP_KEY,
      label: `已下线（${RETIRED_ENGINE_STRATEGIES.length}）`,
      rows: RETIRED_ENGINE_STRATEGIES.map((item) => ({
        key: `retired:${item.id}`,
        source: 'retired' as const,
        name: item.name,
        id: item.id,
        retired: {
          reason: item.reason,
          replacementLabel: item.replacement_label,
          replacementIds: [...item.replacement_ids],
        },
      })),
    })
  }
  return groups
}

/** 本账号 / 他人 / 形态 三个标签的措辞集中一处，页面与测试不再各写各的。 */
export function packageRowTags(item: CatalogPackageRow): string[] {
  const tags: string[] = []
  if (item.isOwn) {
    tags.push('本账号发布')
  }
  if (item.form === 'js') {
    tags.push('代码策略')
  } else if (item.form === 'yaml') {
    tags.push('文本策略')
  }
  return tags
}
