import { useEffect, useState } from 'react'
import { Alert, Button, Drawer, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography, message } from 'antd'
import { SettingOutlined, ReloadOutlined } from '@ant-design/icons'
import { adminService, type AdminMaclawAccount } from '../../services/admin'
import { MaclawModelConfig } from './MaclawModelConfig'
import { ENGINE_TOKEN_ONCE_NOTICE, normalizeEngineBaseUrl, validateEngineBaseUrl } from './engineInstanceForm'

const { Text, Title, Paragraph } = Typography

export function AccountTenantManagement() {
  const [items, setItems] = useState<AdminMaclawAccount[]>([])
  const [loading, setLoading] = useState(false)
  const [role, setRole] = useState<string>()
  const [selected, setSelected] = useState<AdminMaclawAccount | null>(null)
  const [regTarget, setRegTarget] = useState<AdminMaclawAccount | null>(null)
  const [regUrl, setRegUrl] = useState('')
  const [regBusy, setRegBusy] = useState(false)
  const [tokenResult, setTokenResult] = useState<{ account: string; token: string } | null>(null)

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

  const openRegister = (row: AdminMaclawAccount) => {
    setRegTarget(row)
    setRegUrl(row.engine_instance?.base_url || '')
  }

  const submitRegister = async () => {
    if (!regTarget) return
    const problem = validateEngineBaseUrl(regUrl)
    if (problem) {
      message.error(problem)
      return
    }
    setRegBusy(true)
    try {
      const out = await adminService.registerAccountEngine(regTarget.platform_user_id, normalizeEngineBaseUrl(regUrl))
      setRegTarget(null)
      setTokenResult({ account: regTarget.email, token: out.token })
      void load()
    } catch (error) {
      const resp = (error as { response?: { data?: { error?: string } } })?.response
      message.error(resp?.data?.error || '登记失败')
    } finally {
      setRegBusy(false)
    }
  }

  const unregister = async (row: AdminMaclawAccount) => {
    try {
      await adminService.unregisterAccountEngine(row.platform_user_id)
      message.success('已摘除：该账号回落到共享引擎（代码策略仍会被双条件闸拒绝）。')
      void load()
    } catch (error) {
      const resp = (error as { response?: { data?: { error?: string } } })?.response
      message.error(resp?.data?.error || '摘除失败')
    }
  }

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
          { title: '状态', dataIndex: 'provisioning_status', render: (value, row) => (
            <Space size={4}>
              <Tag color={value === 'ready' ? 'green' : 'default'}>{value || 'not_provisioned'}</Tag>
              {row.model_override && <Tag color="geekblue">模型镜像</Tag>}
            </Space>
          ) },
          {
            title: '专属引擎',
            render: (_, row) => {
              if (!row.maclaw_tenant_id) return <Text type="secondary">-</Text>
              if (row.engine_instance?.registered) {
                return (
                  <Space size={4}>
                    <TooltiplessUrl url={row.engine_instance.base_url} />
                    <Popconfirm title="摘除后该账号回落到共享引擎（代码策略将被双条件闸拒绝）。" okText="摘除" cancelText="取消" onConfirm={() => void unregister(row)}>
                      <Button size="small" danger type="link">摘除</Button>
                    </Popconfirm>
                  </Space>
                )
              }
              return (
                <Space size={4}>
                  <Text type="secondary">共享</Text>
                  <Button size="small" type="link" onClick={() => openRegister(row)}>登记</Button>
                </Space>
              )
            },
          },
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
      <Modal
        title={regTarget ? `登记专属引擎 - ${regTarget.email}` : '登记专属引擎'}
        open={!!regTarget}
        onCancel={() => setRegTarget(null)}
        onOk={() => void submitRegister()}
        confirmLoading={regBusy}
        okText="登记（先探活）"
      >
        <Paragraph type="secondary">
          地址必须已在编排中起出（模板 `docker-compose.tenant-engines.yml`）。登记会先探活该地址的 /health，探活失败不会写入。
        </Paragraph>
        <Input
          placeholder="http://promptfoo-engine-acme:8090"
          value={regUrl}
          onChange={(e) => setRegUrl(e.target.value)}
          status={regUrl && validateEngineBaseUrl(regUrl) ? 'error' : undefined}
        />
        {regUrl && validateEngineBaseUrl(regUrl) && (
          <Text type="danger" style={{ fontSize: 12 }}>{validateEngineBaseUrl(regUrl)}</Text>
        )}
      </Modal>
      <Modal
        title="登记成功 · token 仅此一次"
        open={!!tokenResult}
        onCancel={() => setTokenResult(null)}
        onOk={() => setTokenResult(null)}
        okText="我已保存"
        cancelText={null}
      >
        {tokenResult && (
          <>
            <Alert type="warning" showIcon style={{ marginBottom: 12 }} message={ENGINE_TOKEN_ONCE_NOTICE} />
            <Paragraph copyable code style={{ wordBreak: 'break-all' }}>{tokenResult.token}</Paragraph>
            <Text type="secondary">关闭后无法找回，只能重新登记轮换。</Text>
          </>
        )}
      </Modal>
    </div>
  )
}

function TooltiplessUrl({ url }: { url?: string }) {
  return <Text code style={{ fontSize: 12, wordBreak: 'break-all' }}>{url || '-'}</Text>
}
