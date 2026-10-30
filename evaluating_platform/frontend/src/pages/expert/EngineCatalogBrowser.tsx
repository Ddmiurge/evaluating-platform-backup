import type { ReactNode } from 'react'
import { Alert, Collapse, Space, Tag, Typography } from 'antd'
import {
  PLUGIN_PACKAGE_GROUP_KEY,
  RETIRED_GROUP_KEY,
  packageRowTags,
  type CatalogGroup,
  type CatalogRow,
} from './engineCatalogGroups'

const { Text } = Typography

/**
 * 能力目录浏览视图（P23）：内置目录 + 专家发布包 + 已下线引导，按组折叠。
 *
 * 纯展示组件：分组数据来自 `engineCatalogGroups.ts`（可测的纯模块），
 * 包行的操作按钮由页面注入（只有本账号的行会拿到 actions）。
 */

function RowTags({ row }: { row: CatalogRow }) {
  const tags: ReactNode[] = []
  if (row.source === 'builtin') {
    if (row.disabled) {
      tags.push(
        <Tag key="gated" color="default">
          本环境不可用
        </Tag>,
      )
    }
    if (row.configReason) {
      tags.push(
        <Tag key="config" color="orange">
          需先提供配置
        </Tag>,
      )
    }
    if (row.fanout && row.fanout > 1) {
      tags.push(
        <Tag key="fanout">
          ×{row.fanout} 子项
        </Tag>,
      )
    }
  }
  if (row.source === 'package' && row.package) {
    for (const tag of packageRowTags(row.package)) {
      tags.push(
        <Tag key={tag} color={tag === '本账号发布' ? 'blue' : undefined}>
          {tag}
        </Tag>,
      )
    }
    if (row.package.status !== 'published' || !row.package.enabled) {
      const notPublished = row.package.status !== 'published'
      tags.push(
        <Tag key="status" color={row.package.status === 'archived' ? 'default' : 'red'}>
          {row.package.status === 'archived' ? '已下架' : notPublished ? '草稿' : '已停用'}
        </Tag>,
      )
    }
  }
  if (row.source === 'retired') {
    tags.push(
      <Tag key="retired" color="default">
        已下线
      </Tag>,
    )
  }
  return <>{tags}</>
}

function CatalogRowLine({ row, actions }: { row: CatalogRow; actions?: ReactNode }) {
  return (
    <div
      style={{
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'flex-start',
        gap: 12,
        padding: '6px 0',
        borderBottom: '1px solid var(--border-light, #f0f0f0)',
      }}
    >
      <div style={{ minWidth: 0 }}>
        <Space size={6} wrap>
          <Text>{row.name}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {row.id}
          </Text>
          <RowTags row={row} />
        </Space>
        {row.source === 'package' && row.package ? (
          <div>
            <Text type="secondary" style={{ fontSize: 12 }}>
              {[row.package.version, row.package.updatedAt].filter(Boolean).join(' · ')}
            </Text>
          </div>
        ) : null}
        {row.disabled && row.reason ? (
          <div>
            <Text type="secondary" style={{ fontSize: 12 }}>
              {row.reason}
            </Text>
          </div>
        ) : null}
        {row.retired ? (
          <div>
            <Text type="secondary" style={{ fontSize: 12 }}>
              {row.retired.reason} 替代：{row.retired.replacementLabel}
            </Text>
          </div>
        ) : null}
      </div>
      {actions ? <div style={{ flexShrink: 0 }}>{actions}</div> : null}
    </div>
  )
}

export interface EngineCatalogBrowserProps {
  groups: CatalogGroup[]
  kindNoun: '插件' | '策略'
  loading?: boolean
  /** 目录加载降级：只剩自有包时页面置 true，内置区不再假装存在。 */
  degraded?: boolean
  /** 仅对「专家发布包」组里的行调用；页面自己判断是不是本账号的行。 */
  packageActions?: (row: CatalogRow) => ReactNode
}

export function EngineCatalogBrowser({ groups, kindNoun, loading, degraded, packageActions }: EngineCatalogBrowserProps) {
  const packageGroupKey = groups.find((group) => group.key === PLUGIN_PACKAGE_GROUP_KEY)?.key ?? PLUGIN_PACKAGE_GROUP_KEY
  const defaultOpen = [groups[0]?.key, packageGroupKey].filter((key): key is string => Boolean(key))
  return (
    <Space orientation="vertical" size={12} style={{ width: '100%' }}>
      {degraded ? (
        <Alert
          type="info"
          showIcon
          message="平台能力目录暂时不可用，以下仅展示你自己的发布包。"
        />
      ) : null}
      <Collapse
        defaultActiveKey={defaultOpen}
        items={groups.map((group) => ({
          key: group.key,
          label: (
            <Space size={6}>
              <Text strong={group.key !== RETIRED_GROUP_KEY}>{group.label}</Text>
              <Text type="secondary" style={{ fontSize: 12 }}>
                {group.rows.length} 项
              </Text>
            </Space>
          ),
          children: group.rows.length === 0 ? (
            <Text type="secondary" style={{ fontSize: 12 }}>
              还没有{kindNoun}包。发布后会显示在这里。
            </Text>
          ) : (
            group.rows.map((row) => (
              <CatalogRowLine
                key={row.key}
                row={row}
                actions={group.key === packageGroupKey ? packageActions?.(row) : undefined}
              />
            ))
          ),
        }))}
      />
      {loading ? <Text type="secondary">加载中…</Text> : null}
    </Space>
  )
}
