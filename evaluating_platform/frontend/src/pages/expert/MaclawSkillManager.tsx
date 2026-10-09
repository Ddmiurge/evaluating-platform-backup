import { useEffect, useMemo, useState } from 'react'
import {
  Button,
  Card,
  Col,
  Drawer,
  Empty,
  Form,
  Input,
  Row,
  Space,
  Switch,
  Tag,
  Typography,
  Upload,
  message,
} from 'antd'
import { CloudDownloadOutlined, CloudUploadOutlined, ReloadOutlined, SearchOutlined, UploadOutlined } from '@ant-design/icons'
import { expertService } from '../../services/expert'

const { Paragraph, Text, Title } = Typography

interface MaclawSkillSummary {
  name: string
  description?: string
  triggers?: string[]
  status?: string
  source?: string
  type?: string
  mode?: string
  platforms?: string[]
  requires_gui?: boolean
  required_args?: string[]
  required_env?: string[]
  required_credential_files?: string[]
}

interface MaclawSkillSearchResult {
  source: string
  id?: string
  name: string
  description?: string
  version?: string
  author?: string
  trust_level?: string
  tags?: string[]
  downloads?: number
  avg_rating?: number
  price?: number
  repo_url?: string
  raw_url?: string
  repo_full_name?: string
  file_path?: string
  branch?: string
  definition_type?: string
  installed?: boolean
}

function tagColor(value?: string) {
  switch ((value || '').toLowerCase()) {
    case 'active':
    case 'installed':
    case 'enabled':
    case 'healthy':
      return 'green'
    case 'disabled':
    case 'archived':
      return 'default'
    case 'draft':
    case 'pending':
      return 'gold'
    default:
      return 'blue'
  }
}

function asFile(value: unknown): File | null {
  if (value instanceof File) return value
  if (value && typeof value === 'object' && 'originFileObj' in value) {
    const raw = (value as { originFileObj?: unknown }).originFileObj
    return raw instanceof File ? raw : null
  }
  return null
}

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error || new Error('read file failed'))
    reader.onload = () => {
      const result = String(reader.result || '')
      const comma = result.indexOf(',')
      resolve(comma >= 0 ? result.slice(comma + 1) : result)
    }
    reader.readAsDataURL(file)
  })
}

function SkillCard({ item }: { item: MaclawSkillSummary }) {
  const tags = [
    item.status ? <Tag key="status" color={tagColor(item.status)}>{item.status}</Tag> : null,
    item.source ? <Tag key="source">{item.source}</Tag> : null,
    item.type ? <Tag key="type">{item.type}</Tag> : null,
    item.mode ? <Tag key="mode">{item.mode}</Tag> : null,
    item.requires_gui ? <Tag key="gui" color="orange">GUI</Tag> : null,
  ].filter(Boolean)

  return (
    <Card size="small" style={{ height: '100%', background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}>
      <Space orientation="vertical" size={10} style={{ width: '100%' }}>
        <Space orientation="vertical" size={2} style={{ width: '100%' }}>
          <Text strong style={{ color: 'var(--text-primary)' }}>{item.name}</Text>
          <Paragraph ellipsis={{ rows: 2 }} style={{ margin: 0, color: 'var(--text-secondary)' }}>
            {item.description || '未填写说明'}
          </Paragraph>
        </Space>
        {tags.length > 0 ? <Space size={[0, 6]} wrap>{tags}</Space> : null}
        {item.triggers?.length ? (
          <Space size={[0, 6]} wrap>
            {item.triggers.slice(0, 4).map((trigger) => <Tag key={trigger}>{trigger}</Tag>)}
          </Space>
        ) : null}
        {item.platforms?.length ? (
          <Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>平台：{item.platforms.join(' / ')}</Text>
        ) : null}
        {item.required_env?.length ? (
          <Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>环境变量：{item.required_env.join(' / ')}</Text>
        ) : null}
      </Space>
    </Card>
  )
}

function SearchResultCard({
  item,
  installing,
  onInstall,
}: {
  item: MaclawSkillSearchResult
  installing?: boolean
  onInstall: (item: MaclawSkillSearchResult) => void
}) {
  return (
    <Card size="small" style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}>
      <Space orientation="vertical" size={8} style={{ width: '100%' }}>
        <Space wrap style={{ width: '100%', justifyContent: 'space-between' }}>
          <Space wrap>
            <Text strong style={{ color: 'var(--text-primary)' }}>{item.name}</Text>
            <Tag>{item.source}</Tag>
            {item.version ? <Tag>{item.version}</Tag> : null}
            {item.installed ? <Tag color="green">installed</Tag> : null}
          </Space>
          <Button
            size="small"
            icon={<CloudDownloadOutlined />}
            disabled={item.installed}
            loading={installing}
            onClick={() => onInstall(item)}
          >
            安装
          </Button>
        </Space>
        <Paragraph ellipsis={{ rows: 2 }} style={{ margin: 0, color: 'var(--text-secondary)' }}>
          {item.description || '未填写说明'}
        </Paragraph>
        <Space size={[0, 6]} wrap>
          {item.author ? <Tag>{item.author}</Tag> : null}
          {item.trust_level ? <Tag color={tagColor(item.trust_level)}>{item.trust_level}</Tag> : null}
          {item.tags?.slice(0, 5).map((tag) => <Tag key={tag}>{tag}</Tag>)}
        </Space>
      </Space>
    </Card>
  )
}

export function MaclawSkillManager() {
  const [form] = Form.useForm()
  const [importForm] = Form.useForm()
  const [items, setItems] = useState<MaclawSkillSummary[]>([])
  const [searchResults, setSearchResults] = useState<MaclawSkillSearchResult[]>([])
  const [loading, setLoading] = useState(false)
  const [searching, setSearching] = useState(false)
  const [importing, setImporting] = useState(false)
  const [installingKey, setInstallingKey] = useState('')
  const [showImportDrawer, setShowImportDrawer] = useState(false)

  const activeCount = useMemo(() => items.filter((item) => item.status !== 'disabled').length, [items])

  const loadSkills = async () => {
    setLoading(true)
    try {
      const res = await expertService.listMaclawSkills(100)
      setItems((res.items || []) as MaclawSkillSummary[])
    } catch (error) {
      message.error((error as Error).message || '加载 maclaw Skill 失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadSkills()
  }, [])

  const handleSearch = async () => {
    const values = await form.validateFields()
    setSearching(true)
    try {
      const res = await expertService.searchMaclawSkills({
        query: values.query,
        top_n: 10,
        include_installed: true,
      })
      setSearchResults((res.items || []) as MaclawSkillSearchResult[])
    } catch (error) {
      message.error((error as Error).message || '搜索 maclaw Skill 失败')
    } finally {
      setSearching(false)
    }
  }

  const handleImport = async () => {
    const values = await importForm.validateFields()
    const uploadFile = Array.isArray(values.archive) ? values.archive[0] : values.archive?.file
    const file = asFile(uploadFile)
    if (!file) {
      message.error('请选择 Skill ZIP')
      return
    }
    setImporting(true)
    try {
      const zipBase64 = await fileToBase64(file)
      await expertService.importMaclawSkill({
        zip_base64: zipBase64,
        archive_name: file.name,
        overwrite: Boolean(values.overwrite),
      })
      message.success('Skill 已发布到 Hub 并安装')
      setShowImportDrawer(false)
      importForm.resetFields()
      await loadSkills()
    } catch (error) {
      message.error((error as Error).message || '发布并安装 Skill 失败')
    } finally {
      setImporting(false)
    }
  }

  const handleInstallFromSearch = async (item: MaclawSkillSearchResult) => {
    const source = item.source || 'skillhub'
    const key = `${source}:${item.id || item.name}`
    setInstallingKey(key)
    try {
      await expertService.installMaclawSkill({
        source,
        skill_id: item.id || item.name,
        repo_url: item.repo_url,
        raw_url: item.raw_url,
        repo_full_name: item.repo_full_name,
        file_path: item.file_path,
        branch: item.branch,
        definition_type: item.definition_type,
        overwrite: true,
      })
      message.success('Skill 已安装')
      setSearchResults((prev) => prev.map((candidate) => (
        `${candidate.source}:${candidate.id || candidate.name}` === key ? { ...candidate, installed: true } : candidate
      )))
      await loadSkills()
    } catch (error) {
      message.error((error as Error).message || '安装 maclaw Skill 失败')
    } finally {
      setInstallingKey('')
    }
  }

  return (
    <Space orientation="vertical" size="large" style={{ width: '100%' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 16 }}>
        <div>
          <Title level={3} style={{ margin: 0, color: 'var(--text-primary)' }}>maclaw Skill 管理</Title>
          <Text style={{ color: 'var(--text-secondary)' }}>已安装 {items.length} 个，可用 {activeCount} 个</Text>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => void loadSkills()} loading={loading}>
            刷新
          </Button>
          <Button type="primary" icon={<CloudUploadOutlined />} onClick={() => setShowImportDrawer(true)}>
            发布 ZIP
          </Button>
        </Space>
      </div>

      <Card style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-color)' }}>
        <Form form={form} layout="inline" style={{ rowGap: 12 }}>
          <Form.Item name="query" rules={[{ required: true, message: '请输入搜索词' }]}>
            <Input placeholder="搜索 SkillHub / SkillMarket" style={{ width: 320 }} />
          </Form.Item>
          <Form.Item>
            <Button type="primary" icon={<SearchOutlined />} loading={searching} onClick={() => void handleSearch()}>
              搜索
            </Button>
          </Form.Item>
        </Form>
      </Card>

      {searchResults.length > 0 ? (
        <Row gutter={[16, 16]}>
          {searchResults.map((item) => (
            <Col xs={24} lg={12} xl={8} key={`${item.source}:${item.id || item.name}`}>
              <SearchResultCard
                item={item}
                installing={installingKey === `${item.source}:${item.id || item.name}`}
                onInstall={handleInstallFromSearch}
              />
            </Col>
          ))}
        </Row>
      ) : null}

      <Card loading={loading} style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-color)' }}>
        {items.length === 0 ? (
          <Empty description="当前没有 maclaw Skill" />
        ) : (
          <Row gutter={[16, 16]}>
            {items.map((item) => (
              <Col xs={24} lg={12} xl={8} key={item.name}>
                <SkillCard item={item} />
              </Col>
            ))}
          </Row>
        )}
      </Card>

      <Drawer
        title="发布 Skill 到私有 Hub"
        open={showImportDrawer}
        width={480}
        onClose={() => setShowImportDrawer(false)}
        footer={(
          <Space style={{ float: 'right' }}>
            <Button onClick={() => setShowImportDrawer(false)}>取消</Button>
            <Button type="primary" icon={<UploadOutlined />} loading={importing} onClick={() => void handleImport()}>
              发布并安装
            </Button>
          </Space>
        )}
      >
        <Form form={importForm} layout="vertical" initialValues={{ overwrite: false }}>
          <Form.Item
            name="archive"
            label="Skill ZIP"
            valuePropName="fileList"
            getValueFromEvent={(event) => Array.isArray(event) ? event : event?.fileList}
            rules={[{ required: true, message: '请选择 Skill ZIP' }]}
          >
            <Upload accept=".zip" maxCount={1} beforeUpload={() => false}>
              <Button icon={<UploadOutlined />}>选择 ZIP</Button>
            </Upload>
          </Form.Item>
          <Form.Item name="overwrite" label="覆盖同名 Skill" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Drawer>
    </Space>
  )
}