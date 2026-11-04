import { useEffect, useState } from 'react'
import { Alert, Button, Card, Form, Input, InputNumber, Select, Space, Switch, Typography, message } from 'antd'
import { CheckCircleOutlined, ExperimentOutlined, SaveOutlined } from '@ant-design/icons'
import { adminService } from '../../services/admin'
import { configToForm, formToConfig } from './maclawModelConfigForm'

const { Text, Title } = Typography

interface Props {
  userId?: string
  title?: string
  mode?: 'default' | 'account'
}

type ResultStatus = 'success' | 'warning'

function formatTestResult(data: {
  success: boolean
  message?: string
  error?: string
  detail?: string
  endpoint?: string
  model?: string
  wire_api?: string
}) {
  if (data.success) {
    return data.message || '模型连通性测试通过'
  }
  const parts = [
    data.error,
    data.detail,
    data.endpoint ? `endpoint=${data.endpoint}` : '',
    data.model ? `model=${data.model}` : '',
    data.wire_api ? `wire_api=${data.wire_api}` : '',
  ].filter(Boolean)
  return parts.length > 0 ? parts.join('；') : (data.message || '模型连通性测试失败')
}

export function MaclawModelConfig({ userId, title = 'maclaw 模型配置', mode = 'default' }: Props) {
  const [form] = Form.useForm()
  const [loading, setLoading] = useState(false)
  const [checking, setChecking] = useState(false)
  const [result, setResult] = useState<string>('')
  const [resultStatus, setResultStatus] = useState<ResultStatus>('warning')

  const applyProviderDefaults = () => {
    const values = form.getFieldsValue()
    form.setFieldsValue(configToForm(formToConfig(values)))
  }

  const load = async () => {
    setLoading(true)
    try {
      const data = mode === 'account' && userId
        ? await adminService.getAccountModelConfig(userId)
        : await adminService.getDefaultModelConfig()
      form.setFieldsValue(configToForm(data.app_config))
    } catch (error) {
      message.error((error as Error).message || '加载模型配置失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [userId, mode])

  const save = async () => {
    const values = await form.validateFields()
    setLoading(true)
    try {
      const payload = formToConfig(values)
      const data = mode === 'account' && userId
        ? await adminService.updateAccountModelConfig(userId, payload)
        : await adminService.updateDefaultModelConfig(payload)
      form.setFieldsValue(configToForm(data.app_config))
      const sync = (data as { sync?: { attempted?: number; succeeded?: number; failed?: number } }).sync
      if (mode === 'default' && sync) {
        message.success(`默认模型配置已保存，已同步 ${sync.succeeded || 0}/${sync.attempted || 0} 个现有租户`)
      } else {
        message.success('模型配置已保存')
      }
      // P26b：runtime 已生效但平台镜像写失败 → 引擎生成 key 仍按旧配置解析，
      // 必须说出来而不是绿着过去（下次保存会幂等收敛）。
      if (mode === 'account' && (data as { mirror_saved?: boolean | null }).mirror_saved === false) {
        message.warning('runtime 已更新，但平台镜像写入失败：该租户的生成模型 key 暂未生效，请重试保存或联系维护者查看后端日志。', 8)
      }
    } catch (error) {
      message.error((error as Error).message || '保存失败')
    } finally {
      setLoading(false)
    }
  }

  const validate = async () => {
    const values = await form.validateFields()
    setChecking(true)
    try {
      const data = mode === 'account' && userId
        ? await adminService.validateAccountModelConfig(userId, formToConfig(values))
        : await adminService.validateDefaultModelConfig(formToConfig(values))
      setResultStatus(data.valid ? 'success' : 'warning')
      setResult(data.valid ? '配置校验通过' : `配置需要调整：${data.issues?.map((i) => i.message).join('；') || '未知问题'}`)
    } catch (error) {
      message.error((error as Error).message || '校验失败')
    } finally {
      setChecking(false)
    }
  }

  const test = async () => {
    const values = await form.validateFields()
    setChecking(true)
    try {
      const data = mode === 'account' && userId
        ? await adminService.testAccountModelConfig(userId, formToConfig(values))
        : await adminService.testDefaultModelConfig(formToConfig(values))
      setResultStatus(data.success ? 'success' : 'warning')
      setResult(formatTestResult(data))
    } catch (error) {
      message.error((error as Error).message || '测试失败')
    } finally {
      setChecking(false)
    }
  }

  return (
    <Card style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: 8 }}>
      <Space orientation="vertical" size={16} style={{ width: '100%' }}>
        <div>
          <Title level={4} style={{ margin: 0, color: 'var(--text-primary)' }}>{title}</Title>
          <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>
            API Key 只写入，不回显明文；保存后页面仅展示 masked key。
          </Text>
        </div>
        {result && <Alert type={resultStatus} message={result} showIcon />}
        <Form form={form} layout="vertical" disabled={loading}>
          <Form.Item
            label="配置别名"
            name="provider_name"
            extra="这不是模型厂商枚举，只是当前 MaClaw 租户内选择这条模型配置的名字；DeepSeek 可填 deepseek-prod，也可留空自动生成。"
          >
            <Input placeholder="deepseek-prod" />
          </Form.Item>
          <Form.Item label="Base URL" name="url" rules={[{ required: true }]}>
            <Input placeholder="https://api.deepseek.com" onBlur={applyProviderDefaults} />
          </Form.Item>
          <Form.Item label="API Key" name="key">
            <Input.Password placeholder="留空或 ****** 表示沿用已保存密钥" autoComplete="new-password" />
          </Form.Item>
          <Form.Item label="模型" name="model" rules={[{ required: true }]}>
            <Input placeholder="deepseek-v4-pro / deepseek-v4-flash" onBlur={applyProviderDefaults} />
          </Form.Item>
          <Space size={16} wrap>
            <Form.Item label="Wire API" name="wire_api">
              <Select style={{ width: 180 }} options={[
                { value: 'responses', label: 'OpenAI Responses' },
                { value: 'chat_completions', label: 'Chat Completions' },
                { value: 'anthropic_messages', label: 'Anthropic Messages' },
              ]} />
            </Form.Item>
            <Form.Item label="协议" name="protocol">
              <Input style={{ width: 160 }} placeholder="可选" />
            </Form.Item>
            <Form.Item label="上下文长度" name="context_length">
              <InputNumber min={0} style={{ width: 160 }} />
            </Form.Item>
            <Form.Item label="超时秒数" name="timeout_sec">
              <InputNumber min={1} style={{ width: 160 }} />
            </Form.Item>
            <Form.Item label="视觉能力" name="supports_vision" valuePropName="checked">
              <Switch />
            </Form.Item>
          </Space>
        </Form>
        <Space>
          <Button type="primary" icon={<SaveOutlined />} loading={loading} onClick={save}>保存</Button>
          <Button icon={<CheckCircleOutlined />} loading={checking} onClick={validate}>校验</Button>
          <Button icon={<ExperimentOutlined />} loading={checking} onClick={test}>测试连接</Button>
        </Space>
      </Space>
    </Card>
  )
}
