import { useEffect, useMemo, useState } from 'react'
import {
  Button,
  Drawer,
  Form,
  Input,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import {
  ApiOutlined,
  DeleteOutlined,
  EditOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  StopOutlined,
  ToolOutlined,
} from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import {
  expertService,
  type MaclawMCPServerInput,
  type MaclawMCPServerSummary,
  type MaclawMCPToolSummary,
} from '../../services/expert'

const { Paragraph, Text, Title } = Typography

interface MCPFormValues {
  name: string
  endpoint_url: string
  auth_type?: string
  auth_secret?: string
  headers_text?: string
  auto_start?: boolean
  disabled?: boolean
}

function healthColor(status?: string) {
  switch ((status || '').toLowerCase()) {
    case 'healthy':
      return 'green'
    case 'unhealthy':
    case 'failed':
    case 'error':
      return 'red'
    case 'checking':
      return 'blue'
    default:
      return 'default'
  }
}

function healthLabel(status?: string) {
  switch ((status || '').toLowerCase()) {
    case 'healthy':
      return '健康'
    case 'unhealthy':
      return '异常'
    case 'failed':
      return '检查失败'
    case 'error':
      return '错误'
    case 'checking':
      return '检查中'
    default:
      return '未检查'
  }
}

function isRemoteServer(item: MaclawMCPServerSummary) {
  return (item.kind || 'remote').toLowerCase() === 'remote'
}

function runtimeStatus(item: MaclawMCPServerSummary) {
  if (item.disabled) {
    return { color: 'default', label: '已禁用' }
  }

  const health = (item.health_status || '').toLowerCase()
  if (isRemoteServer(item)) {
    if (health === 'healthy') return { color: 'green', label: '连接健康' }
    if (health === 'checking') return { color: 'blue', label: '检查中' }
    if (health === 'unhealthy' || health === 'failed' || health === 'error') {
      return { color: 'red', label: '连接异常' }
    }
    if (item.auto_start) return { color: 'geekblue', label: '已配置' }
    return { color: 'default', label: '已配置' }
  }

  return item.running ? { color: 'blue', label: '运行中' } : { color: 'default', label: '已停止' }
}

function parseHeaders(value?: string): Record<string, string> | undefined {
  const headers: Record<string, string> = {}
  for (const line of (value || '').split('\n')) {
    const trimmed = line.trim()
    if (!trimmed) continue
    const index = trimmed.indexOf(':')
    if (index <= 0) continue
    const key = trimmed.slice(0, index).trim()
    const headerValue = trimmed.slice(index + 1).trim()
    if (key && headerValue) {
      headers[key] = headerValue
    }
  }
  return Object.keys(headers).length > 0 ? headers : undefined
}

function toInput(values: MCPFormValues): MaclawMCPServerInput {
  return {
    kind: 'remote',
    name: values.name.trim(),
    endpoint_url: values.endpoint_url.trim(),
    auth_type: values.auth_type,
    auth_secret: values.auth_secret?.trim() || undefined,
    headers: parseHeaders(values.headers_text),
    auto_start: Boolean(values.auto_start),
    disabled: Boolean(values.disabled),
  }
}

export function MaclawMCPServerManager() {
  const [form] = Form.useForm<MCPFormValues>()
  const [items, setItems] = useState<MaclawMCPServerSummary[]>([])
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [editing, setEditing] = useState<MaclawMCPServerSummary | null>(null)
  const [toolsOpen, setToolsOpen] = useState(false)
  const [toolsLoading, setToolsLoading] = useState(false)
  const [tools, setTools] = useState<MaclawMCPToolSummary[]>([])
  const [toolsServerName, setToolsServerName] = useState('')
  const [busyAction, setBusyAction] = useState('')

  const activeCount = useMemo(() => items.filter((item) => !item.disabled).length, [items])

  const loadServers = async () => {
    setLoading(true)
    try {
      const res = await expertService.listMaclawMCPServers(100)
      setItems(res.items || [])
    } catch (error) {
      message.error((error as Error).message || '加载 MCP 服务失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadServers()
  }, [])

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ auth_type: 'bearer', auto_start: true, disabled: false })
    setDrawerOpen(true)
  }

  const openEdit = (item: MaclawMCPServerSummary) => {
    setEditing(item)
    form.setFieldsValue({
      name: item.name,
      endpoint_url: item.endpoint_url || '',
      auth_type: item.auth_type || 'bearer',
      auth_secret: '',
      headers_text: (item.header_names || []).map((name) => `${name}: `).join('\n'),
      auto_start: item.auto_start,
      disabled: item.disabled,
    })
    setDrawerOpen(true)
  }

  const saveServer = async () => {
    const values = await form.validateFields()
    setSaving(true)
    try {
      if (editing) {
        await expertService.updateMaclawMCPServer(editing.id, toInput(values))
        message.success('MCP 服务已更新')
      } else {
        await expertService.createMaclawMCPServer(toInput(values))
        message.success('MCP 服务已创建')
      }
      setDrawerOpen(false)
      await loadServers()
    } catch (error) {
      message.error((error as Error).message || '保存 MCP 服务失败')
    } finally {
      setSaving(false)
    }
  }

  const mutateServer = async (item: MaclawMCPServerSummary, action: 'start' | 'stop' | 'health' | 'delete') => {
    const actionKey = `${action}:${item.id}`
    setBusyAction(actionKey)
    try {
      if (action === 'start') await expertService.startMaclawMCPServer(item.id)
      if (action === 'stop') await expertService.stopMaclawMCPServer(item.id)
      if (action === 'health') await expertService.healthCheckMaclawMCPServer(item.id)
      if (action === 'delete') await expertService.deleteMaclawMCPServer(item.id)
      await loadServers()
    } catch (error) {
      message.error((error as Error).message || '操作 MCP 服务失败')
    } finally {
      setBusyAction('')
    }
  }

  const showTools = async (item: MaclawMCPServerSummary) => {
    setToolsServerName(item.name)
    setToolsOpen(true)
    setToolsLoading(true)
    try {
      const res = await expertService.listMaclawMCPServerTools(item.id)
      setTools(res.items || [])
    } catch (error) {
      setTools([])
      message.error((error as Error).message || '加载 MCP 工具失败')
    } finally {
      setToolsLoading(false)
    }
  }

  const columns: ColumnsType<MaclawMCPServerSummary> = [
    {
      title: '服务',
      dataIndex: 'name',
      render: (_, item) => (
        <Space orientation="vertical" size={2}>
          <Text strong>{item.name}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>{item.endpoint_url || '未配置远程地址'}</Text>
        </Space>
      ),
    },
    {
      title: '状态',
      width: 220,
      render: (_, item) => {
        const status = runtimeStatus(item)
        return (
          <Space wrap>
            <Tag color={item.disabled ? 'default' : 'green'}>{item.disabled ? '禁用' : '启用'}</Tag>
            <Tag color={status.color}>{status.label}</Tag>
            <Tag color={healthColor(item.health_status)}>{healthLabel(item.health_status)}</Tag>
          </Space>
        )
      },
    },
    {
      title: '认证',
      width: 180,
      render: (_, item) => (
        <Space wrap>
          {item.auth_type ? <Tag>{item.auth_type}</Tag> : null}
          {item.has_auth_secret ? <Tag color="gold">密钥已保存</Tag> : null}
          {item.header_names?.slice(0, 2).map((name) => <Tag key={name}>{name}</Tag>)}
        </Space>
      ),
    },
    {
      title: '操作',
      width: 330,
      render: (_, item) => (
        <Space wrap>
          <Button size="small" icon={<EditOutlined />} disabled={Boolean(busyAction)} onClick={() => openEdit(item)}>编辑</Button>
          <Button
            size="small"
            icon={<PlayCircleOutlined />}
            loading={busyAction === `start:${item.id}`}
            disabled={Boolean(busyAction) && busyAction !== `start:${item.id}`}
            onClick={() => void mutateServer(item, 'start')}
          >
            启动/重连
          </Button>
          {item.running ? (
            <Popconfirm title={`确认停止 ${item.name} 吗？`} onConfirm={() => void mutateServer(item, 'stop')}>
              <Button
                size="small"
                danger
                icon={<StopOutlined />}
                loading={busyAction === `stop:${item.id}`}
                disabled={Boolean(busyAction) && busyAction !== `stop:${item.id}`}
              >
                停止
              </Button>
            </Popconfirm>
          ) : null}
          <Button
            size="small"
            icon={<ReloadOutlined />}
            loading={busyAction === `health:${item.id}`}
            disabled={Boolean(busyAction) && busyAction !== `health:${item.id}`}
            onClick={() => void mutateServer(item, 'health')}
          >
            检查
          </Button>
          <Button size="small" icon={<ToolOutlined />} disabled={Boolean(busyAction)} onClick={() => void showTools(item)}>工具</Button>
          <Popconfirm title={`确认删除 ${item.name} 吗？`} onConfirm={() => void mutateServer(item, 'delete')}>
            <Button
              size="small"
              danger
              icon={<DeleteOutlined />}
              loading={busyAction === `delete:${item.id}`}
              disabled={Boolean(busyAction) && busyAction !== `delete:${item.id}`}
            />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <Space orientation="vertical" size="large" style={{ width: '100%' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 16 }}>
        <div>
          <Title level={3} style={{ margin: 0, color: 'var(--text-primary)' }}>MCP 服务</Title>
          <Text style={{ color: 'var(--text-secondary)' }}>已配置 {items.length} 个，启用 {activeCount} 个</Text>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => void loadServers()} loading={loading}>刷新</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建远程 MCP</Button>
        </Space>
      </div>

      <Table
        rowKey="id"
        loading={loading}
        columns={columns}
        dataSource={items}
        pagination={{ pageSize: 10 }}
        style={{ background: 'var(--bg-surface)' }}
      />

      <Drawer
        title={editing ? '编辑 MCP 服务' : '新建远程 MCP 服务'}
        open={drawerOpen}
        width={520}
        onClose={() => setDrawerOpen(false)}
        footer={(
          <Space style={{ float: 'right' }}>
            <Button onClick={() => setDrawerOpen(false)}>取消</Button>
            <Button type="primary" icon={<ApiOutlined />} loading={saving} onClick={() => void saveServer()}>保存</Button>
          </Space>
        )}
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入 MCP 服务名称' }]}>
            <Input placeholder="例如：专家样本检索 MCP" />
          </Form.Item>
          <Form.Item name="endpoint_url" label="远程 MCP URL" rules={[{ required: true, message: '请输入远程 MCP URL' }]}>
            <Input placeholder="https://mcp.example.com/mcp" />
          </Form.Item>
          <Form.Item name="auth_type" label="认证方式">
            <Select
              options={[
                { label: 'Bearer', value: 'bearer' },
                { label: '无认证', value: 'none' },
              ]}
            />
          </Form.Item>
          <Form.Item name="auth_secret" label="认证密钥">
            <Input.Password placeholder={editing?.has_auth_secret ? '留空则保留已保存密钥' : 'Bearer token'} />
          </Form.Item>
          <Form.Item name="headers_text" label="附加请求头">
            <Input.TextArea rows={4} placeholder={'X-Header: value\nX-Trace: demo'} />
          </Form.Item>
          <Form.Item name="auto_start" label="自动启用" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name="disabled" label="禁用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Drawer>

      <Drawer title={`${toolsServerName} 工具`} open={toolsOpen} width={520} onClose={() => setToolsOpen(false)}>
        <Table
          rowKey="name"
          size="small"
          loading={toolsLoading}
          dataSource={tools}
          pagination={false}
          columns={[
            { title: '工具', dataIndex: 'name' },
            {
              title: '描述',
              dataIndex: 'description',
              render: (value?: string) => <Paragraph style={{ margin: 0 }}>{value || '-'}</Paragraph>,
            },
          ]}
        />
      </Drawer>
    </Space>
  )
}