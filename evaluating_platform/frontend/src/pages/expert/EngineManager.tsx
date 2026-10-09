import { useCallback, useEffect, useMemo, useState } from 'react'
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
  Select,
  Space,
  Tag,
  Tabs,
  Typography,
  Upload,
  message,
} from 'antd'
import {
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  FolderOutlined,
  PlusOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { expertService } from '../../services/expert'
import { ComposedAttackManager } from './ComposedAttackManager'
import { formatDate } from '../../utils/format'

const { Paragraph, Text, Title } = Typography
const { TextArea } = Input

interface TemplateRecord {
  id: string
  upload_batch_id?: string
  upload_batch_name?: string
  sub_type: string
  name: string
  description: string
  content: string
  created_at: string
  updated_at?: string
}

interface TemplateCategoryGroup {
  id: string
  name: string
  templates: TemplateRecord[]
  subTypes: string[]
  latestUpdatedAt: string
}

const templateSubTypeLabel: Record<string, string> = {
  角色身份扮演: '角色身份扮演',
  虚拟叙事保护: '虚拟叙事保护',
  系统指令注入: '系统指令注入',
  DAN模式: 'DAN模式',
  游戏化包装: '游戏化包装',
  双重人格回答: '双重人格回答',
  邪恶AI召唤: '邪恶AI召唤',
  '编码／格式混淆': '编码／格式混淆',
  role_play: '角色身份扮演',
  multilingual: '多语言增强（历史）',
  encoding_evasion: '编码／格式混淆（历史）',
}

const builtinTemplateCategories = [
  '角色身份扮演',
  '虚拟叙事保护',
  '系统指令注入',
  'DAN模式',
  '游戏化包装',
  '双重人格回答',
  '邪恶AI召唤',
  '编码／格式混淆',
]

function templateCategoryOptions(extra: string[] = []) {
  const seen = new Set<string>()
  return [...builtinTemplateCategories, ...extra]
    .filter((value) => {
      if (!value || seen.has(value)) {
        return false
      }
      seen.add(value)
      return true
    })
    .map((value) => ({ value, label: templateSubTypeLabel[value] || value }))
}

function toTimestamp(value?: string) {
  const time = new Date(value || '').getTime()
  return Number.isNaN(time) ? 0 : time
}

function buildTemplateCategoryGroups(templates: TemplateRecord[]) {
  const groupMap = new Map<string, TemplateCategoryGroup>()
  builtinTemplateCategories.forEach((category) => {
    groupMap.set(category, {
      id: `category-${category}`,
      name: templateSubTypeLabel[category] || category,
      templates: [],
      subTypes: [category],
      latestUpdatedAt: '',
    })
  })

  templates.forEach((template) => {
    const category = template.sub_type || '未分类'
    const existing = groupMap.get(category) || {
      id: `category-${category}`,
      name: templateSubTypeLabel[category] || category,
      templates: [],
      subTypes: [category],
      latestUpdatedAt: '',
    }
    existing.templates.push(template)
    const updatedAt = template.updated_at || template.created_at || ''
    if (toTimestamp(updatedAt) > toTimestamp(existing.latestUpdatedAt)) {
      existing.latestUpdatedAt = updatedAt
    }
    groupMap.set(category, existing)
  })

  return Array.from(groupMap.values())
    .map((group) => ({
      ...group,
      templates: [...group.templates].sort((a, b) => toTimestamp(b.updated_at || b.created_at) - toTimestamp(a.updated_at || a.created_at)),
    }))
    .sort((a, b) => {
      const aBuiltin = builtinTemplateCategories.indexOf(a.subTypes[0])
      const bBuiltin = builtinTemplateCategories.indexOf(b.subTypes[0])
      if (aBuiltin >= 0 && bBuiltin >= 0) return aBuiltin - bBuiltin
      if (aBuiltin >= 0) return -1
      if (bBuiltin >= 0) return 1
      return a.name.localeCompare(b.name, 'zh-CN')
    })
}

export function EngineManager() {
  const [templates, setTemplates] = useState<TemplateRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [filterType, setFilterType] = useState<string>('')
  const [showTemplateModal, setShowTemplateModal] = useState(false)
  const [showCSVUpload, setShowCSVUpload] = useState(false)
  const [showBatchModal, setShowBatchModal] = useState(false)
  const [selectedBatch, setSelectedBatch] = useState<TemplateCategoryGroup | null>(null)
  const [editingTemplate, setEditingTemplate] = useState<TemplateRecord | null>(null)
  const [templateForm] = Form.useForm()
  const [csvForm] = Form.useForm()

  const loadTemplates = useCallback(() => {
    setLoading(true)
    expertService
      .listTemplates({ sub_type: filterType || undefined, limit: 200 })
      .then((res) => setTemplates(res.items || []))
      .catch(() => message.error('加载模板失败'))
      .finally(() => setLoading(false))
  }, [filterType])

  useEffect(() => {
    loadTemplates()
  }, [loadTemplates])

  const templateBatches = useMemo(() => buildTemplateCategoryGroups(templates), [templates])
  const templateTypeOptions = useMemo(() => templateCategoryOptions(templates.map((item) => item.sub_type)), [templates])

  const refreshSelectedBatch = useCallback((batchID: string) => {
    const nextBatch = templateBatches.find((item) => item.id === batchID) || null
    setSelectedBatch(nextBatch)
    if (!nextBatch) {
      setShowBatchModal(false)
    }
  }, [templateBatches])

  useEffect(() => {
    if (selectedBatch) {
      refreshSelectedBatch(selectedBatch.id)
    }
  }, [templateBatches, refreshSelectedBatch, selectedBatch])

  const handleSaveTemplate = async () => {
    try {
      const values = await templateForm.validateFields()
      const payload = {
        sub_type: values.sub_type,
        name: values.name,
        description: values.description || '',
        content: values.content,
      }
      if (editingTemplate) {
        await expertService.updateTemplate(editingTemplate.id, payload)
        message.success('模板已更新')
      } else {
        await expertService.createTemplate(payload)
        message.success('模板已创建')
      }
      setShowTemplateModal(false)
      setEditingTemplate(null)
      templateForm.resetFields()
      loadTemplates()
    } catch (error: unknown) {
      message.error((error as Error).message || '保存模板失败')
    }
  }

  const handleEditTemplate = (record: TemplateRecord) => {
    setEditingTemplate(record)
    templateForm.setFieldsValue({
      sub_type: record.sub_type,
      name: record.name,
      description: record.description,
      content: record.content,
    })
    setShowTemplateModal(true)
  }

  const handleDeleteTemplate = async (id: string) => {
    try {
      await expertService.deleteTemplate(id)
      message.success('模板已删除')
      loadTemplates()
    } catch (error: unknown) {
      message.error((error as Error).message || '删除模板失败')
    }
  }

  const loadAllTemplatesForSubTypes = async (subTypes: string[]) => {
    const pageSize = 200
    const byID = new Map<string, TemplateRecord>()
    for (const subType of subTypes) {
      let offset = 0
      for (;;) {
        const res = await expertService.listTemplates({ sub_type: subType, limit: pageSize, offset })
        const pageItems = ((res.items || []) as TemplateRecord[]).filter((item) => item.id)
        pageItems.forEach((item) => byID.set(item.id, item))
        const total = Number(res.total || 0)
        offset += pageSize
        if (pageItems.length < pageSize || (total > 0 && offset >= total)) {
          break
        }
      }
    }
    return Array.from(byID.values())
  }

  const handleDeleteBatch = async (batch: TemplateCategoryGroup) => {
    try {
      const allTemplates = await loadAllTemplatesForSubTypes(batch.subTypes)
      if (allTemplates.length === 0) {
        message.info('该分类下暂无模板')
        return
      }
      await Promise.all(allTemplates.map((template) => expertService.deleteTemplate(template.id)))
      message.success(`分类“${batch.name}”下的模板已删除`)
      if (selectedBatch?.id === batch.id) {
        setShowBatchModal(false)
        setSelectedBatch(null)
      }
      loadTemplates()
    } catch (error: unknown) {
      message.error((error as Error).message || '删除分类模板失败')
    }
  }

  const handleCSVUpload = async () => {
    try {
      const values = await csvForm.validateFields()
      const formData = new FormData()
      formData.append('name', values.name)
      formData.append('file', values.file.file.originFileObj || values.file.file)
      await expertService.uploadTemplateCSV(formData)
      message.success('模板集合上传成功')
      setShowCSVUpload(false)
      csvForm.resetFields()
      loadTemplates()
    } catch (error: unknown) {
      message.error((error as Error).message || '上传模板失败')
    }
  }

  const templateTab = (
    <div>
      <Space style={{ marginBottom: 16 }} wrap>
        <Select
          value={filterType}
          onChange={setFilterType}
          style={{ width: 180 }}
          options={[
            { value: '', label: '全部类型' },
            ...templateTypeOptions,
          ]}
        />
        <Button
          icon={<UploadOutlined />}
          onClick={() => {
            csvForm.resetFields()
            setShowCSVUpload(true)
          }}
        >
          上传模板集合
        </Button>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditingTemplate(null)
            templateForm.resetFields()
            setShowTemplateModal(true)
          }}
        >
          新建单条模板
        </Button>
      </Space>

      {templateBatches.length === 0 ? (
        <Card loading={loading}>
          <Empty description="当前还没有模板分类。" />
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {templateBatches.map((batch) => (
            <Col key={batch.id} xs={24} sm={12} lg={8} xl={6}>
              <Card
                loading={loading}
                hoverable
                actions={[
                  <Button key="view" type="link" icon={<EyeOutlined />} onClick={() => { setSelectedBatch(batch); setShowBatchModal(true) }}>
                    查看详情
                  </Button>,
                  <Popconfirm
                    key="delete"
                    title={`确认删除分类“${batch.name}”下的全部模板吗？`}
                    description={batch.templates.length > 0 ? '会删除这个分类下的全部模板。' : '该分类当前没有模板。'}
                    onConfirm={() => handleDeleteBatch(batch)}
                  >
                    <Button type="link" danger icon={<DeleteOutlined />} disabled={batch.templates.length === 0}>
                      删除分类
                    </Button>
                  </Popconfirm>,
                ]}
                style={{ height: '100%', background: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: 8 }}
                styles={{ body: { minHeight: 130, padding: 28 } }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 18 }}>
                  <FolderOutlined style={{ color: 'var(--text-primary)', fontSize: 22 }} />
                  <Text style={{ color: 'var(--text-primary)', fontSize: 22, fontWeight: 700 }}>{batch.name}</Text>
                </div>
                <Text style={{ color: 'var(--text-muted)', fontSize: 14 }}>{batch.templates.length} 条模板</Text>
              </Card>
            </Col>
          ))}
        </Row>
      )}
    </div>
  )

  return (
    <div>
      <div style={{ marginBottom: 20 }}>
        <Title level={4} style={{ color: 'var(--text-primary)', margin: 0 }}>
          引擎管理
        </Title>
        <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>
          模板管理按分类展示，每个分类一个卡片；上传文件第二列的自定义分类会自动成为新的分类卡片。
        </Text>
      </div>

      <Tabs
        defaultActiveKey="templates"
        items={[
          { key: 'templates', label: '模板管理', children: templateTab },
          { key: 'composed', label: '已组合攻击', children: <ComposedAttackManager /> },
        ]}
      />

      <Modal
        title={editingTemplate ? '编辑模板' : '新建模板'}
        open={showTemplateModal}
        onCancel={() => {
          setShowTemplateModal(false)
          setEditingTemplate(null)
          templateForm.resetFields()
        }}
        onOk={handleSaveTemplate}
        okText="保存"
        width={720}
      >
        <Form form={templateForm} layout="vertical">
          <Form.Item name="sub_type" label="模板类型" rules={[{ required: true, message: '请选择模板类型' }]}> 
            <Select showSearch options={templateTypeOptions} />
          </Form.Item>
          <Form.Item name="name" label="模板名称" rules={[{ required: true, message: '请输入模板名称' }]}> 
            <Input placeholder="如：合规检测角色扮演模板" />
          </Form.Item>
          <Form.Item name="description" label="描述">
            <Input placeholder="简要说明这个模板的用途" />
          </Form.Item>
          <Form.Item
            name="content"
            label="模板内容"
            rules={[{ required: true, message: '请输入模板内容' }]}
            extra="模板内容会在内部与样本问题拼接后，再送往被测 LLM。"
          >
            <TextArea rows={8} placeholder="请输入模板内容，可使用 {{sample}} 或 {{question}} 表示样本问题插入位置。" />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="上传模板集合"
        open={showCSVUpload}
        onCancel={() => {
          setShowCSVUpload(false)
          csvForm.resetFields()
        }}
        onOk={handleCSVUpload}
        okText="上传"
        width={560}
      >
        <Form form={csvForm} layout="vertical">
          <Form.Item name="name" label="集合名称" rules={[{ required: true, message: '请输入集合名称' }]}> 
            <Input placeholder="如：2026-04 合规测试模板集" />
          </Form.Item>
          <Form.Item
            name="file"
            label="Excel / CSV 文件"
            rules={[{ required: true, message: '请上传 Excel 或 CSV 文件' }]}
            extra="格式：三列（序号, 分类名称, 模板内容），可带表头。分类名称支持内置分类或自定义扩展分类。"
          >
            <Upload accept=".xlsx,.csv" maxCount={1} beforeUpload={() => false}>
              <Button icon={<UploadOutlined />}>选择 Excel / CSV 文件</Button>
            </Upload>
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={selectedBatch ? `模板分类：${selectedBatch.name}` : '模板分类详情'}
        open={showBatchModal}
        onCancel={() => {
          setShowBatchModal(false)
          setSelectedBatch(null)
        }}
        footer={null}
        width={920}
      >
        {!selectedBatch ? null : (
          <Space orientation="vertical" style={{ width: '100%' }} size="middle">
            <Card size="small" style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-color)' }}>
              <Space orientation="vertical" size={4} style={{ width: '100%' }}>
                <Text style={{ color: 'var(--text-secondary)' }}>模板数量：{selectedBatch.templates.length}</Text>
                <Text style={{ color: 'var(--text-secondary)' }}>最近更新时间：{selectedBatch.latestUpdatedAt ? formatDate(selectedBatch.latestUpdatedAt) : '暂无模板'}</Text>
                <Space size={[0, 8]} wrap>
                  {selectedBatch.subTypes.map((subType) => (
                    <Tag key={subType} color="purple">
                      {templateSubTypeLabel[subType] || subType}
                    </Tag>
                  ))}
                </Space>
              </Space>
            </Card>

            {selectedBatch.templates.length === 0 ? (
              <Card style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}>
                <Empty description="该分类下暂无模板，上传或新建模板后会显示在这里。" />
              </Card>
            ) : (
              <Row gutter={[12, 12]}>
                {selectedBatch.templates.map((template, index) => (
                  <Col key={template.id} span={24}>
                    <Card
                      size="small"
                      title={<Text style={{ color: 'var(--text-primary)' }}>模板 {index + 1}</Text>}
                      extra={
                        <Space>
                          <Button type="link" size="small" icon={<EditOutlined />} onClick={() => handleEditTemplate(template)}>
                            编辑
                          </Button>
                          <Popconfirm title="确认删除这条模板吗？" onConfirm={() => handleDeleteTemplate(template.id)}>
                            <Button type="link" size="small" danger icon={<DeleteOutlined />}>
                              删除
                            </Button>
                          </Popconfirm>
                        </Space>
                      }
                      style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}
                    >
                      <Space orientation="vertical" size={6} style={{ width: '100%' }}>
                        <Text style={{ color: 'var(--text-secondary)' }}>
                          类型：{templateSubTypeLabel[template.sub_type] || template.sub_type}
                        </Text>
                        {template.description ? (
                          <Paragraph style={{ color: 'var(--text-secondary)', marginBottom: 0 }}>{template.description}</Paragraph>
                        ) : null}
                        <Paragraph
                          style={{ color: 'var(--text-primary)', marginBottom: 0, whiteSpace: 'pre-wrap' }}
                          ellipsis={{ rows: 4, expandable: true, symbol: '展开' }}
                        >
                          {template.content}
                        </Paragraph>
                      </Space>
                    </Card>
                  </Col>
                ))}
              </Row>
            )}
          </Space>
        )}
      </Modal>
    </div>
  )
}