import { useCallback, useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Drawer,
  Input,
  Modal,
  Popconfirm,
  Segmented,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { adminService, type EmbeddedPluginSummary, type EmbeddedSourceFile } from '../../services/admin'
import { embeddedAdminActions, embeddedGateText, embeddedReviewReasonLabel, embeddedStateTag, type EmbeddedAdminAction } from '../../services/embeddedPluginStates'
import { formatDate } from '../../utils/format'

const { Text, Title, Paragraph } = Typography

const ACTION_LABELS: Record<EmbeddedAdminAction, string> = {
  approve: '通过 → 待构建',
  changes: '需修改',
  'request-build': '请求构建',
  'final-approve': '最终通过',
  disable: '停用',
  enable: '启用',
  remove: '标记移除',
  unmark: '撤销移除',
}

const FILTERS = [
  { value: '', label: '全部' },
  { value: 'pending_initial_review', label: '待初审' },
  { value: 'approved_for_build', label: '待构建' },
  { value: 'build_requested', label: '已请求构建' },
  { value: 'testing', label: '测试中' },
  { value: 'approved', label: '已通过' },
  { value: 'rejected', label: '已驳回' },
]

/**
 * 管理员「内嵌插件审批」页（P28）。
 *
 * 顶栏四计数 = 状态机的实时投影（批了没人建一眼可见）；行操作严格随状态
 * （embeddedAdminActions 与 Go 转移表同源）；「需修改」意见必填；源码抽屉是
 * R-5 窄例外的落点（全站唯一可读包正文处，带截断上限由后端强制）。
 */
export function EmbeddedPluginReview() {
  const [items, setItems] = useState<EmbeddedPluginSummary[]>([])
  const [loading, setLoading] = useState(false)
  const [filter, setFilter] = useState('')
  const [changesTarget, setChangesTarget] = useState<EmbeddedPluginSummary | null>(null)
  const [changesReason, setChangesReason] = useState('')
  const [sourceTarget, setSourceTarget] = useState<EmbeddedPluginSummary | null>(null)
  const [sourceFiles, setSourceFiles] = useState<EmbeddedSourceFile[] | null>(null)
  const [testers, setTesters] = useState<string[]>([])
  const [newTester, setNewTester] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [list, testerList] = await Promise.all([
        adminService.listEmbeddedPlugins(),
        adminService.listEmbeddedTesters().catch(() => ({ items: [] })),
      ])
      setItems(list.items ?? [])
      setTesters(testerList.items ?? [])
    } catch (error) {
      message.error((error as Error).message || '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const runAction = async (row: EmbeddedPluginSummary, action: EmbeddedAdminAction, reason?: string) => {
    try {
      if (action === 'approve' || action === 'changes') {
        await adminService.reviewEmbeddedPlugin(row.package_id, action, reason ?? '')
      } else {
        await adminService.embeddedPluginAction(row.package_id, action, reason)
      }
      message.success(`已执行：${ACTION_LABELS[action]}`)
      setChangesTarget(null)
      setChangesReason('')
      void load()
    } catch (error) {
      const resp = (error as { response?: { data?: { error?: string } } })?.response
      message.error(resp?.data?.error || (error as Error).message || '操作失败')
    }
  }

  const openSource = async (row: EmbeddedPluginSummary) => {
    setSourceTarget(row)
    setSourceFiles(null)
    try {
      const out = await adminService.getEmbeddedPluginSource(row.package_id)
      setSourceFiles(out.files)
    } catch (error) {
      const resp = (error as { response?: { data?: { error?: string } } })?.response
      message.error(resp?.data?.error || '读取源码失败')
      setSourceTarget(null)
    }
  }

  const addTester = async () => {
    const id = newTester.trim()
    if (!/^[0-9a-f-]{36}$/i.test(id)) {
      message.error('请输入完整的账号 UUID')
      return
    }
    try {
      await adminService.addEmbeddedTester(id)
      message.success('已加入测试白名单')
      setNewTester('')
      void load()
    } catch (error) {
      message.error((error as Error).message || '添加失败')
    }
  }

  const counts = items.reduce<Record<string, number>>((acc, item) => {
    acc[item.state] = (acc[item.state] ?? 0) + 1
    return acc
  }, {})

  const filtered = filter ? items.filter((item) => item.state === filter) : items

  const renderActions = (row: EmbeddedPluginSummary) => {
    const actions = embeddedAdminActions(row.state)
    const buttons = actions.map((action) => {
      const label = ACTION_LABELS[action]
      const needsReason = action === 'changes'
      const confirmText =
        action === 'request-build'
          ? '将标记「已请求构建」，部署机工具消费后执行构建'
          : action === 'final-approve'
            ? '确认测试通过，放行给全员？'
            : undefined
      if (needsReason) {
        return (
          <Button key={action} size="small" onClick={() => setChangesTarget(row)}>
            {label}
          </Button>
        )
      }
      if (confirmText) {
        return (
          <Popconfirm key={action} title={confirmText} okText="执行" cancelText="取消" onConfirm={() => void runAction(row, action)}>
            <Button size="small" type={action === 'final-approve' ? 'primary' : 'link'}>
              {label}
            </Button>
          </Popconfirm>
        )
      }
      return (
        <Popconfirm
          key={action}
          title={`确认「${label}」？`}
          okText="执行"
          cancelText="取消"
          onConfirm={() => void runAction(row, action)}
        >
          <Button size="small" type="link" danger={action === 'disable' || action === 'remove'}>
            {label}
          </Button>
        </Popconfirm>
      )
    })
    return (
      <Space size={0} wrap>
        {buttons}
        {row.resource_id && (
          <Button size="small" type="link" onClick={() => void openSource(row)}>
            查看源码
          </Button>
        )}
      </Space>
    )
  }

  const columns = [
    {
      title: '包 / 提交人',
      render: (_: unknown, row: EmbeddedPluginSummary) => (
        <Space orientation="vertical" size={0}>
          <Text>{row.name}</Text>
          <Text type="secondary">
            {row.package_id} · {row.owner_user_id.slice(0, 8)}…
          </Text>
        </Space>
      ),
    },
    { title: '版本', dataIndex: 'version', width: 70 },
    {
      title: '状态',
      dataIndex: 'state',
      width: 130,
      render: (value: string) => {
        const tag = embeddedStateTag(value)
        return <Tag color={tag.color}>{tag.label}</Tag>
      },
    },
    {
      title: '自动关卡 / 意见',
      render: (_: unknown, row: EmbeddedPluginSummary) => (
        <Space orientation="vertical" size={0}>
          <Text type={row.auto_gate?.passed === false ? 'danger' : 'secondary'}>{embeddedGateText(row.auto_gate)}</Text>
          {row.review_reason && (
            <Text type="warning">
              {embeddedReviewReasonLabel(row.state)}：{row.review_reason}
            </Text>
          )}
        </Space>
      ),
    },
    { title: '更新时间', dataIndex: 'updated_at', width: 150, render: (v?: string) => (v ? formatDate(v) : '-') },
    { title: '操作', render: (_: unknown, row: EmbeddedPluginSummary) => renderActions(row) },
  ]

  return (
    <div>
      <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 16 }}>
        <div>
          <Title level={4} style={{ color: 'var(--text-primary)', margin: 0 }}>
            内嵌插件审批
          </Title>
          <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>
            待初审 {counts.pending_initial_review ?? 0} · 待构建 {(counts.approved_for_build ?? 0) + (counts.build_requested ?? 0) + (counts.build_failed ?? 0)} · 测试中 {counts.testing ?? 0} · 已通过 {counts.approved ?? 0} · 已驳回 {counts.rejected ?? 0}
          </Text>
        </div>
        <Space>
          <Segmented value={filter} onChange={(v) => setFilter(String(v))} options={FILTERS} />
          <Button icon={<ReloadOutlined />} onClick={() => void load()}>
            刷新
          </Button>
        </Space>
      </Space>

      <Table
        rowKey="package_id"
        loading={loading}
        dataSource={filtered}
        pagination={{ pageSize: 15 }}
        columns={columns}
        locale={{ emptyText: '暂无内嵌插件' }}
      />

      <Alert
        type="info"
        showIcon
        style={{ marginTop: 16 }}
        message="两段审批：初审=可构建去测；最终通过=看过测试 run 后放行全员。「请求构建」只写标记，部署机工具消费标记才执行（守卫不绿不部署）。"
      />

      <div style={{ marginTop: 24 }}>
        <Title level={5} style={{ color: 'var(--text-primary)' }}>
          测试白名单（「测试中」阶段可见可选）
        </Title>
        <Space wrap style={{ marginBottom: 8 }}>
          {testers.length === 0 && <Text type="secondary">暂无测试账号</Text>}
          {testers.map((id) => (
            <Tag key={id} closable onClose={() => void adminService.removeEmbeddedTester(id).then(() => load())}>
              {id.slice(0, 8)}…
            </Tag>
          ))}
        </Space>
        <Space>
          <Input
            placeholder="账号 UUID"
            style={{ width: 320 }}
            value={newTester}
            onChange={(e) => setNewTester(e.target.value)}
          />
          <Button size="small" onClick={() => void addTester()}>
            添加
          </Button>
        </Space>
      </div>

      <Modal
        title={changesTarget ? `需修改 - ${changesTarget.name}` : '需修改'}
        open={!!changesTarget}
        onCancel={() => {
          setChangesTarget(null)
          setChangesReason('')
        }}
        onOk={() => changesTarget && void runAction(changesTarget, 'changes', changesReason)}
        okText="发送给专家"
        okButtonProps={{ disabled: changesReason.trim().length === 0 }}
      >
        <Paragraph type="secondary">意见必填，指向具体的文件或问题——专家照着它改后同 id 重传。</Paragraph>
        <Input.TextArea
          rows={4}
          value={changesReason}
          onChange={(e) => setChangesReason(e.target.value)}
          placeholder="例如：src/plugin.ts 第 12 行读取了 process.env，请移除后重传"
        />
        {changesReason.trim().length === 0 && (
          <Text type="danger" style={{ fontSize: 12 }}>
            意见不能为空（状态机同样强制）
          </Text>
        )}
      </Modal>

      <Drawer
        title={sourceTarget ? `源码审阅 - ${sourceTarget.name} v${sourceTarget.version}` : '源码审阅'}
        open={!!sourceTarget}
        width={720}
        onClose={() => {
          setSourceTarget(null)
          setSourceFiles(null)
        }}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message="本抽屉是全站唯一可读包正文的位置（审批即治理）；其余管理页面仍禁止回显任何 payload。"
        />
        {sourceFiles === null && <Text type="secondary">加载中…</Text>}
        {sourceFiles?.length === 0 && <Text type="secondary">无可读文本文件（包内只有二进制？）</Text>}
        {sourceFiles?.map((file) => (
          <div key={file.name} style={{ marginBottom: 12 }}>
            <Space>
              <Text strong>{file.name}</Text>
              {file.truncated && <Tag color="orange">已截断</Tag>}
            </Space>
            <pre
              style={{
                background: 'var(--bg-secondary, #f6f8fa)',
                padding: 8,
                fontSize: 12,
                maxHeight: 320,
                overflow: 'auto',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-all',
              }}
            >
              {file.content}
            </pre>
          </div>
        ))}
        {sourceTarget && (
          <Space>
            {embeddedAdminActions(sourceTarget.state).includes('changes') && (
              <Button
                type="primary"
                onClick={() => {
                  setChangesTarget(sourceTarget)
                  setSourceTarget(null)
                }}
              >
                需修改
              </Button>
            )}
            {embeddedAdminActions(sourceTarget.state).includes('approve') && (
              <Button onClick={() => void runAction(sourceTarget, 'approve')}>通过 → 待构建</Button>
            )}
          </Space>
        )}
      </Drawer>
    </div>
  )
}
