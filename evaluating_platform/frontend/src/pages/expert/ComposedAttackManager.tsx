import { useEffect, useState } from 'react'
import {
  Button,
  Card,
  Col,
  Empty,
  Form,
  Input,
  Modal,
  Popconfirm,
  Row,
  Space,
  Tag,
  Typography,
  Upload,
  message,
} from 'antd'
import {
  DeleteOutlined,
  EyeOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { expertService } from '../../services/expert'
import { formatDate, formatSize } from '../../utils/format'

const { Paragraph, Text } = Typography
const { TextArea } = Input
const DEFAULT_COMPOSED_ATTACK_TYPE = 'ready_to_run'

interface ComposedAttackRecord {
  id: string
  sub_type: string
  name: string
  description: string
  sample_count: number
  file_hash: string
  file_size: number
  created_at: string
}

interface Payload {
  index: number
  data: string
}

export function ComposedAttackManager() {
  const [items, setItems] = useState<ComposedAttackRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [showUpload, setShowUpload] = useState(false)
  const [previewData, setPreviewData] = useState<Payload[] | null>(null)
  const [previewName, setPreviewName] = useState('')
  const [expandedRows, setExpandedRows] = useState<Record<number, boolean>>({})
  const [form] = Form.useForm()

  const loadItems = () => {
    setLoading(true)
    expertService
      .listComposedAttacks({ limit: 100 })
      .then((res) => setItems(res.items || []))
      .catch(() => message.error('加载已组合攻击失败'))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    loadItems()
  }, [])

  const handleUpload = async () => {
    try {
      const values = await form.validateFields()
      const formData = new FormData()
      formData.append('sub_type', DEFAULT_COMPOSED_ATTACK_TYPE)
      formData.append('name', values.name)
      formData.append('description', values.description || '')
      formData.append('file', values.file.file.originFileObj || values.file.file)
      await expertService.uploadComposedAttack(formData)
      message.success('已组合攻击上传成功')
      setShowUpload(false)
      form.resetFields()
      loadItems()
    } catch (error: unknown) {
      message.error((error as Error).message || '上传已组合攻击失败')
    }
  }

  const handlePreview = async (id: string, name: string, sampleCount: number) => {
    try {
      const res = await expertService.previewComposedAttack(id, sampleCount || 20)
      setPreviewData(res.preview || [])
      setPreviewName(name)
    } catch {
      message.error('预览已组合攻击失败')
    }
  }

  const handleDelete = async (id: string) => {
    try {
      await expertService.deleteComposedAttack(id)
      message.success('已组合攻击已删除')
      loadItems()
    } catch (error: unknown) {
      message.error((error as Error).message || '删除已组合攻击失败')
    }
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <Space orientation="vertical" size={2}>
          <Text style={{ color: 'var(--text-primary)', fontSize: 16, fontWeight: 600 }}>已组合攻击</Text>
          <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>
            这类资产已经是最终攻击载荷。编排命中后会直接执行，跳过模板拼接。
          </Text>
        </Space>
        <Space>
          <Button type="primary" icon={<UploadOutlined />} onClick={() => setShowUpload(true)}>
            上传已组合攻击
          </Button>
        </Space>
      </div>

      {items.length === 0 ? (
        <Card loading={loading}>
          <Empty description="当前还没有已组合攻击，上传 CSV 后会以卡片方式展示在这里。" />
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {items.map((item) => (
            <Col key={item.id} xs={24} md={12} xl={8}>
              <Card
                loading={loading}
                hoverable
                title={<Text style={{ color: 'var(--text-primary)' }}>{item.name}</Text>}
                extra={<Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>{item.sample_count} 条载荷</Text>}
                actions={[
                  <Button key="preview" type="link" icon={<EyeOutlined />} onClick={() => handlePreview(item.id, item.name, item.sample_count)}>
                    预览内容
                  </Button>,
                  <Popconfirm key="delete" title={`确认删除“${item.name}”吗？`} onConfirm={() => handleDelete(item.id)}>
                    <Button type="link" danger icon={<DeleteOutlined />}>
                      删除
                    </Button>
                  </Popconfirm>,
                ]}
                style={{ height: '100%', background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}
              >
                <Space size={[0, 8]} wrap style={{ marginBottom: 12 }}>
                  <Tag color="volcano">已组合攻击</Tag>
                  <Tag>{formatSize(item.file_size)}</Tag>
                </Space>

                <div style={{ marginBottom: 12 }}>
                  <Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>资源说明</Text>
                  <Paragraph style={{ color: 'var(--text-secondary)', minHeight: 44, marginBottom: 0 }} ellipsis={{ rows: 2 }}>
                    {item.description || `这个资源包含 ${item.sample_count} 条可直接执行的攻击载荷。`}
                  </Paragraph>
                </div>

                <div style={{ marginBottom: 12 }}>
                  <Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>上传时间</Text>
                  <div>
                    <Text style={{ color: 'var(--text-secondary)' }}>{formatDate(item.created_at)}</Text>
                  </div>
                </div>

                <div>
                  <Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>文件指纹</Text>
                  <Paragraph style={{ color: 'var(--text-secondary)', marginBottom: 0 }} ellipsis={{ rows: 1 }}>
                    {item.file_hash || '未记录'}
                  </Paragraph>
                </div>
              </Card>
            </Col>
          ))}
        </Row>
      )}

      <Modal
        title="上传已组合攻击"
        open={showUpload}
        onCancel={() => {
          setShowUpload(false)
          form.resetFields()
        }}
        onOk={handleUpload}
        okText="上传"
        width={540}
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="资源名称" rules={[{ required: true, message: '请输入资源名称' }]}>
            <Input placeholder="如：论文 A 预拼接越狱集" />
          </Form.Item>
          <Form.Item name="description" label="描述">
            <TextArea rows={2} placeholder="简要描述这组已组合攻击的来源或特点" />
          </Form.Item>
          <Form.Item
            name="file"
            label="CSV 文件"
            rules={[{ required: true, message: '请上传 CSV 文件' }]}
            extra="格式为两列：序号、最终攻击内容。上传后会直接作为可执行载荷使用。"
          >
            <Upload accept=".csv" maxCount={1} beforeUpload={() => false}>
              <Button icon={<UploadOutlined />}>选择 CSV 文件</Button>
            </Upload>
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={`已组合攻击预览：${previewName}`}
        open={!!previewData}
        onCancel={() => {
          setPreviewData(null)
          setExpandedRows({})
        }}
        footer={null}
        width={880}
      >
        {!previewData || previewData.length === 0 ? (
          <Empty description="暂无载荷数据" />
        ) : (
          <Space orientation="vertical" style={{ width: '100%' }} size="middle">
            {previewData.map((payload) => {
              const safeText = payload.data || ''
              const isExpanded = expandedRows[payload.index]
              const shouldTruncate = safeText.length > 500
              const displayText = shouldTruncate && !isExpanded ? `${safeText.slice(0, 500)}...` : safeText

              return (
                <Card
                  key={payload.index}
                  size="small"
                  title={<Text style={{ color: 'var(--text-primary)' }}>载荷 {payload.index}</Text>}
                  style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}
                >
                  <Paragraph style={{ color: 'var(--text-primary)', marginBottom: shouldTruncate ? 8 : 0, whiteSpace: 'pre-wrap' }}>
                    {displayText}
                  </Paragraph>
                  {shouldTruncate ? (
                    <Button
                      type="link"
                      size="small"
                      onClick={() => setExpandedRows((prev) => ({ ...prev, [payload.index]: !prev[payload.index] }))}
                      style={{ paddingLeft: 0 }}
                    >
                      {isExpanded ? '收起' : '展开'}
                    </Button>
                  ) : null}
                </Card>
              )
            })}
          </Space>
        )}
      </Modal>
    </div>
  )
}