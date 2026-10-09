import { useEffect, useState } from 'react'
import { Button, Drawer, Select, Space, Table, Tag, Typography } from 'antd'
import { SettingOutlined, ReloadOutlined } from '@ant-design/icons'
import { adminService, type AdminMaclawAccount } from '../../services/admin'
import { MaclawModelConfig } from './MaclawModelConfig'

const { Text, Title } = Typography

export function AccountTenantManagement() {
  const [items, setItems] = useState<AdminMaclawAccount[]>([])
  const [loading, setLoading] = useState(false)
  const [role, setRole] = useState<string>()
  const [selected, setSelected] = useState<AdminMaclawAccount | null>(null)

  const load = async () => {
    setLoading(true)
    try {
      const data = await adminService.listMaclawAccounts({ role, limit: 100 })
      setItems(data.items)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [role])

  return (
    <div>
      <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 16 }}>
        <div>
          <Title level={4} style={{ color: 'var(--text-primary)', margin: 0 }}>用户与租户</Title>
          <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>查看平台账号和 maclaw tenant/user/instance 映射。</Text>
        </div>
        <Space>
          <Select
            allowClear
            placeholder="角色"
            style={{ width: 140 }}
            value={role}
            onChange={setRole}
            options={[
              { value: 'enterprise', label: '企业' },
              { value: 'expert', label: '专家' },
              { value: 'admin', label: '管理员' },
            ]}
          />
          <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        </Space>
      </Space>
      <Table
        rowKey="platform_user_id"
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 12 }}
        columns={[
          { title: '账号', dataIndex: 'email', render: (value, row) => <Space orientation="vertical" size={0}><Text>{value}</Text><Text type="secondary">{row.name}</Text></Space> },
          { title: '角色', dataIndex: 'role', render: (value) => <Tag color={value === 'expert' ? 'orange' : value === 'admin' ? 'purple' : 'blue'}>{value}</Tag> },
          { title: 'maclaw tenant', dataIndex: 'maclaw_tenant_id', ellipsis: true, render: (value) => value || <Text type="secondary">未 provision</Text> },
          { title: 'instance', dataIndex: 'maclaw_instance_id', ellipsis: true },
          { title: '状态', dataIndex: 'provisioning_status', render: (value) => <Tag color={value === 'ready' ? 'green' : 'default'}>{value || 'not_provisioned'}</Tag> },
          {
            title: '模型',
            render: (_, row) => (
              <Button size="small" icon={<SettingOutlined />} onClick={() => setSelected(row)}>
                配置
              </Button>
            ),
          },
        ]}
      />
      <Drawer
        title={selected ? `租户模型配置 - ${selected.email}` : '租户模型配置'}
        open={!!selected}
        size="large"
        onClose={() => setSelected(null)}
        destroyOnHidden
      >
        {selected && <MaclawModelConfig userId={selected.platform_user_id} mode="account" title="单租户 maclaw 模型配置" />}
      </Drawer>
    </div>
  )
}