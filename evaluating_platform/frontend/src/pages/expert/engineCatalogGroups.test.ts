import { describe, expect, it } from 'vitest'

import {
  PLUGIN_PACKAGE_GROUP_KEY,
  RETIRED_GROUP_KEY,
  buildPluginCatalogGroups,
  buildStrategyCatalogGroups,
  packageRowTags,
  type CatalogPackageRow,
} from './engineCatalogGroups'

const ownPublished: CatalogPackageRow = {
  id: 'res_own',
  handle: 'platform_resource_own',
  name: '我的代码策略',
  version: 'v2',
  status: 'published',
  enabled: true,
  form: 'js',
  isOwn: true,
  declaredId: 'custom:p18upper',
}

const foreignPublished: CatalogPackageRow = {
  id: 'res_foreign',
  handle: 'platform_resource_foreign',
  name: '别人发布的插件',
  version: 'v1',
  status: 'published',
  enabled: true,
  isOwn: false,
}

const ownArchived: CatalogPackageRow = {
  id: 'res_archived',
  handle: 'platform_resource_old',
  name: '我下架过的包',
  version: 'v1',
  status: 'archived',
  enabled: false,
  form: 'yaml',
  isOwn: true,
}

describe('buildPluginCatalogGroups', () => {
  const groups = buildPluginCatalogGroups([ownPublished, foreignPublished, ownArchived])

  it('第一组按 promptfoo 原生类别（有害内容），并携带 fanout', () => {
    expect(groups[0].label).toBe('内置 · 有害内容')
    const harmful = groups[0].rows.find((row) => row.id === 'harmful')
    expect(harmful?.fanout).toBe(26)
    expect(harmful?.source).toBe('builtin')
  })

  it('被远程生成闸拦住的内置项是灰态而非消失，并带原因', () => {
    const all = groups.flatMap((group) => group.rows)
    const gated = all.find((row) => row.id === 'harmful:child-exploitation')
    expect(gated?.disabled).toBe(true)
    expect(gated?.reason).toBeTruthy()
  })

  it('`policy` 类缺配置项保持可选，只挂 configReason 提醒', () => {
    const all = groups.flatMap((group) => group.rows)
    const policy = all.find((row) => row.id === 'policy')
    expect(policy?.disabled).toBeUndefined()
    expect(policy?.configReason).toBeTruthy()
  })

  it('专家发布包组在内置类别之后、已下线之前；全部包行（含本账号 archived）都在里面', () => {
    const keys = groups.map((group) => group.key)
    expect(keys.indexOf(PLUGIN_PACKAGE_GROUP_KEY)).toBeGreaterThan(0)
    expect(keys[keys.length - 1]).toBe(RETIRED_GROUP_KEY)
    const packageGroup = groups.find((group) => group.key === PLUGIN_PACKAGE_GROUP_KEY)
    expect(packageGroup?.rows).toHaveLength(3)
    expect(packageGroup?.rows.map((row) => row.key)).toEqual([
      'package:res_own',
      'package:res_foreign',
      'package:res_archived',
    ])
  })

  it('已下线组带替代路径引导', () => {
    const retired = groups.find((group) => group.key === RETIRED_GROUP_KEY)
    expect(retired?.label).toContain('已下线')
    expect(retired?.rows.length).toBeGreaterThan(0)
    const row = retired!.rows[0]
    expect(row.retired?.reason).toBeTruthy()
    expect(row.retired?.replacementLabel).toBeTruthy()
  })
})

describe('buildStrategyCatalogGroups', () => {
  const groups = buildStrategyCatalogGroups([ownPublished])

  it('策略页 = 内置策略平组 + 专家发布包 + 已下线（9 条口径与后端同源）', () => {
    expect(groups.map((group) => group.key)).toEqual(['builtin-strategies', PLUGIN_PACKAGE_GROUP_KEY, RETIRED_GROUP_KEY])
    expect(groups[0].rows.length).toBeGreaterThan(0)
    const retiredCount = groups[2].rows.length
    expect(retiredCount).toBe(9)
  })

  it('策略的已下线项把后端引导文案原样透出（含 4 个永久移除项的「不是等价项」措辞）', () => {
    const noEquivalent = groups[2].rows.filter((row) => row.id === 'role-play' || row.id === 'assumed-knowledge' || row.id === 'few-shot' || row.id === 'many-shot')
    expect(noEquivalent).toHaveLength(4)
    expect(noEquivalent[0].retired?.replacementLabel).toContain('不是等价项')
    expect(noEquivalent[0].retired?.reason).toContain('永久移除')
  })
})

describe('packageRowTags', () => {
  it('本账号发布与代码/文本形态是两个独立标注', () => {
    expect(packageRowTags(ownPublished)).toEqual(['本账号发布', '代码策略'])
    expect(packageRowTags(ownArchived)).toEqual(['本账号发布', '文本策略'])
    expect(packageRowTags(foreignPublished)).toEqual([])
  })
})
