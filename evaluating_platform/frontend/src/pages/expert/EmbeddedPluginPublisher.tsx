import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Modal, Space, Table, Tag, Typography, Upload, message } from 'antd'
import { PlusOutlined, ReloadOutlined, UploadOutlined } from '@ant-design/icons'
import { expertService, type EmbeddedPluginSummary } from '../../services/expert'
import {
  arrayBufferToBase64,
  embeddedGateText,
  embeddedExpertCanRevise,
  embeddedStateTag,
} from '../../services/embeddedPluginStates'
import { formatDate } from '../../utils/format'

const { Text, Title } = Typography

/**
 * 专家门户「内嵌插件」页（P28）。
 *
 * 生命周期是评审流而非发布流：上传 → 自动关卡 → 管理员初审 → 构建入镜像 →
 * 测试中（仅白名单）→ 管理员最终通过。所以这页的主角是**状态**：
 * 驳回原因行内可读（改什么一目了然），「重新上传」= 同 id 更新（版本由状态机
 * 递增，页面不问版本号）。
 *
 * 上传契约（与后端存储出口一致）：.zip ≤512KB；正文以 base64 走 JSON
 * （与其它资源同一加密存储出口）；manifest 解析是后端的权威，前端只做
 * 扩展名/大小两道快检，把注定被拒的文件挡在提交前。
 */
const MAX_ZIP_BYTES = 512 * 1024

export function EmbeddedPluginPublisher() {
  const [items, setItems] = useState<EmbeddedPluginSummary[]>([])
  const [loading, setLoading] = useState(false)
  const [uploadOpen, setUploadOpen] = useState(false)
  const [reviseOf, setReviseOf] = useState<EmbeddedPluginSummary | null>(null)
  const [pendingFile, setPendingFile] = useState<File | null>(null)
  const [uploading, setUploading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await expertService.listEmbeddedPlugins()
      setItems(data.items ?? [])
    } catch (error) {
      message.error((error as Error).message || '加载内嵌插件失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const openUpload = (row?: EmbeddedPluginSummary) => {
    setReviseOf(row ?? null)
    setPendingFile(null)
    setUploadOpen(true)
  }

  const onPickFile = (file: File) => {
    if (!file.name.toLowerCase().endsWith('.zip')) {
      message.error('只接受 .zip 包（结构：manifest.yaml + src/ + tests/）')
      return false
    }
    if (file.size > MAX_ZIP_BYTES) {
      message.error(`压缩包 ${Math.round(file.size / 1024)} KB 超过 512KB 上限`)
      return false
    }
    setPendingFile(file)
    return false // never auto-upload; the user confirms below
  }

  const submit = async () => {
    if (!pendingFile) {
      message.error('请先选择 .zip 文件')
      return
    }
    setUploading(true)
    try {
      const base64 = arrayBufferToBase64(await pendingFile.arrayBuffer())
      const out = await expertService.publishEmbeddedPlugin({
        name: pendingFile.name.replace(/\.zip$/i, ''),
        payload: base64,
        // 同 id 重传：带已有资源 id 走 P10 的「更新同一行」；被拒过的包没有
        // 资源行，留空由状态机按 manifest.id 归并。
        ...(reviseOf?.resource_id ? { id: reviseOf.resource_id } : {}),
      })
      message.success(`已入库：「${out.name}」状态=待初审（同 id 重传版本会自动递增）`)
      setUploadOpen(false)
      setPendingFile(null)
      setReviseOf(null)
      void load()
    } catch (error) {
      const resp = (error as { response?: { data?: { error?: string } } })?.response
      message.error(resp?.data?.error || (error as Error).message || '上传失败')
    } finally {
      setUploading(false)
    }
  }

  const columns = [
    {
      title: '包 / id',
      render: (_: unknown, row: EmbeddedPluginSummary) => (
        <Space orientation="vertical" size={0}>
          <Text>{row.name}</Text>
          <Text type="secondary">{row.package_id}</Text>
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
      title: '关卡 / 意见',
      render: (_: unknown, row: EmbeddedPluginSummary) => {
        if (row.state === 'rejected' && row.review_reason) {
          return <Text type="danger">{row.review_reason}</Text>
        }
        if (row.state === 'rejected') {
          return <Text type="secondary">{embeddedGateText(row.auto_gate)}</Text>
        }
        if (row.auto_gate && row.auto_gate.passed === true) {
          return <Text type="secondary">{embeddedGateText(row.auto_gate)}</Text>
        }
        return <Text type="secondary">—</Text>
      },
    },
    {
      title: '更新时间',
      dataIndex: 'updated_at',
      width: 160,
      render: (value?: string) => (value ? formatDate(value) : '-'),
    },
    {
      title: '操作',
      width: 110,
      render: (_: unknown, row: EmbeddedPluginSummary) =>
        embeddedExpertCanRevise(row.state) ? (
          <Button type="link" size="small" onClick={() => openUpload(row)}>
            重新上传
          </Button>
        ) : (
          <Text type="secondary">终态</Text>
        ),
    },
  ]

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 20 }}>
        <div>
          <Title level={4} style={{ color: 'var(--text-primary)', margin: 0 }}>
            内嵌插件
          </Title>
          <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>
            上传「编译进引擎」的插件包（zip：manifest.yaml + src/ + tests/）。流程：自动关卡 → 管理员初审 → 构建入镜像 → 测试中 → 管理员最终通过；通过后对企业全员可用。
          </Text>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => void load()}>
            刷新
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => openUpload()}>
            上传包
          </Button>
        </Space>
      </div>

      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="上传 ≠ 生效；同 id 重传 = 更新此包（版本自动递增），驳回后按原因修改重传即可"
        description="包须零依赖、离线可跑：tests/run.mjs 在引擎子进程内秒级执行，禁止 npm install 与联网。测试白名单账号在「测试中」阶段可选测，管理员最终通过后全员可见。"
      />

      <Table
        rowKey="package_id"
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 20 }}
        columns={columns}
        locale={{ emptyText: '还没有上传过内嵌插件。点右上「上传包」开始。' }}
      />

      <Modal
        title={reviseOf ? `重新上传 - ${reviseOf.name}` : '上传内嵌插件包'}
        open={uploadOpen}
        onCancel={() => {
          setUploadOpen(false)
          setPendingFile(null)
          setReviseOf(null)
        }}
        onOk={() => void submit()}
        confirmLoading={uploading}
        okText="上传"
        width={560}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message="包结构：manifest.yaml + src/ + tests/（tests/run.mjs 必须存在）"
          description="仅 .zip，≤512KB。manifest 以后端解析为准；上传后进入自动关卡，被拒会在此列表给出可操作原因。"
        />
        {reviseOf && (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 12 }}
            message={`同 id 重传：更新「${reviseOf.package_id}」（版本 +1），不会产生第二个包`}
          />
        )}
        <Upload accept=".zip" showUploadList={false} beforeUpload={onPickFile}>
          <Button icon={<UploadOutlined />}>{pendingFile ? '重新选择' : '选择 .zip 文件'}</Button>
        </Upload>
        {pendingFile && (
          <div style={{ marginTop: 8 }}>
            <Text>
              {pendingFile.name}（{Math.ceil(pendingFile.size / 1024)} KB）
            </Text>
          </div>
        )}
      </Modal>
    </div>
  )
}
