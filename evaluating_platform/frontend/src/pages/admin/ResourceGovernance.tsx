import { useEffect, useMemo, useState } from 'react'
import { Button, Segmented, Space, Switch, Table, Tag, Tooltip, Typography, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { adminService, type AdminMaclawResource } from '../../services/admin'

const { Text, Title } = Typography

/**
 * 管理员资源治理（U10 扩展）。
 *
 * 自定义插件（kind=pf_plugin）与其它专家资源同表同治理：启停 / 归档。
 * 设计文档 `06` D-03 取消了「管理员审核」作为发布前置门，所以这里的动作全部是
 * **事后**的，且**即时生效** —— 停用后引擎下一次准备 run 时就会拒绝该插件引用
 * （`CustomPluginResolver` 每次都查 `enabled = true AND status = 'published'`，
 * 不缓存），不存在需要重启或等待同步的窗口。
 */
export function ResourceGovernance() {
  const [items, setItems] = useState<AdminMaclawResource[]>([])
  const [loading, setLoading] = useState(false)
  // 默认聚焦自定义包，因为它们是「会被引擎执行」的资源类别，治理风险最高。
  // 2026-10-23：策略包（pf_strategy）与插件包同权 —— 它同样会被引擎加载执行，
  // 之前只能通过 API 停用。
  const [scope, setScope] = useState<'all' | 'pf_plugin' | 'pf_strategy'>('pf_plugin')

  const load = async () => {
    setLoading(true)
    try {
      const data = await adminService.listMaclawResources({ include_inactive: true, limit: 300 })
      setItems(data.items)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const visible = useMemo(
    () => (scope === 'all' ? items : items.filter((row) => row.kind === scope)),
    [items, scope],
  )

  const updateEnabled = async (row: AdminMaclawResource, enabled: boolean) => {
    try {
      await adminService.updateMaclawResource(row.source_resource_id, { enabled })
      message.success(
        enabled
          ? '插件已启用，后续评测可使用'
          : '插件已停用，引擎已不可用（对下一次评测即时生效，无需重启）',
      )
      load()
    } catch (error) {
      message.error((error as Error).message || '更新失败')
    }
  }

  const archive = async (row: AdminMaclawResource) => {
    try {
      await adminService.updateMaclawResource(row.source_resource_id, { enabled: false, status: 'archived' })
      message.success('资源已归档')
      load()
    } catch (error) {
      message.error((error as Error).message || '归档失败')
    }
  }

  return (
    <div>
      <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 16 }}>
        <div>
          <Title level={4} style={{ color: 'var(--text-primary)', margin: 0 }}>资源治理</Title>
          <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>统一查看专家发布资源目录，不展示资源正文。</Text>
        </div>
        <Space>
          <Segmented
            value={scope}
            onChange={(value) => setScope(value as 'all' | 'pf_plugin' | 'pf_strategy')}
            options={[
              { label: '自定义插件', value: 'pf_plugin' },
              { label: '自定义策略', value: 'pf_strategy' },
              { label: '全部', value: 'all' },
            ]}
          />
          <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        </Space>
      </Space>
      <Table
        rowKey="source_resource_id"
        loading={loading}
        dataSource={visible}
        pagination={{ pageSize: 12 }}
        columns={[
          { title: '资源', dataIndex: 'name', render: (value, row) => <Space orientation="vertical" size={0}><Text>{value}</Text><Text type="secondary">{row.summary || row.source_resource_handle}</Text></Space> },
          { title: '专家', dataIndex: 'source_expert_email', render: (value, row) => <Space orientation="vertical" size={0}><Text>{row.source_expert_name || '-'}</Text><Text type="secondary">{value}</Text></Space> },
          {
            title: '类型',
            dataIndex: 'kind',
            render: (value) => (
              // U10：把「会被引擎执行」的资源类别标出来，避免管理员把自定义插件
              // 当成普通样本看待而忽略其治理含义。
              <Tag color={value === 'pf_plugin' || value === 'pf_strategy' ? 'orange' : undefined}>{value}</Tag>
            ),
          },
          // 包 id 是报告里显示的名字，也是与内置目录冲突的唯一依据，所以治理视图
          // 要能看到（无需解密正文即可展示）。插件与策略各用各的 metadata 键。
          ...(scope === 'all'
            ? []
            : [{
                title: scope === 'pf_strategy' ? '策略标识' : '插件 id',
                dataIndex: ['metadata', scope === 'pf_strategy' ? 'pf_strategy_id' : 'pf_plugin_id'],
                render: (_: unknown, row: AdminMaclawResource) => (
                  <Text type="secondary">
                    {(scope === 'pf_strategy' ? row.metadata?.pf_strategy_id : row.metadata?.pf_plugin_id) || '未声明'}
                  </Text>
                ),
              }]),
          { title: '版本', dataIndex: 'source_version' },
          { title: '状态', dataIndex: 'status', render: (value) => <Tag color={value === 'published' ? 'green' : value === 'archived' ? 'default' : 'blue'}>{value}</Tag> },
          {
            title: '企业可见',
            dataIndex: 'enabled',
            render: (value, row) => (
              <Tooltip title="停用后引擎立即不可用：对下一次评测即时生效，无需重启引擎或等待同步">
                <Switch checked={value} onChange={(checked) => updateEnabled(row, checked)} />
              </Tooltip>
            ),
          },
          { title: '标签', dataIndex: 'tags', render: (tags?: string[]) => <Space wrap>{(tags || []).map((tag) => <Tag key={tag}>{tag}</Tag>)}</Space> },
          { title: '操作', render: (_, row) => <Button size="small" danger disabled={row.status === 'archived'} onClick={() => archive(row)}>归档</Button> },
        ]}
      />
    </div>
  )
}
