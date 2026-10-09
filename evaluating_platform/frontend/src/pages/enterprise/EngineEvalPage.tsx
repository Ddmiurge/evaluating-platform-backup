/**
 * 自选评测向导页（Phase 2）：不经聊天工作台直接发起 promptfoo 引擎评测。
 * 页首引导回 AI 对话（对话优先），表单 → 进度 → 结果三段式 + 历史记录。
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Alert, Button, Card, Col, Collapse, Descriptions, Form, InputNumber, Progress, Row, Select, Space, Spin, Steps, Table, Tag, Typography, message,
} from 'antd'
import { ArrowLeftOutlined, PlayCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import {
  ENGINE_PLUGIN_CATEGORY_LABELS, ENGINE_PLUGIN_OPTIONS, ENGINE_STRATEGY_OPTIONS, engineEvalService,
} from '../../services/engineEval'
import type { EngineRun, EngineRunResult } from '../../services/engineEval'

/** 插件选项按风险类别分组（与后端 catalog 的 category 对齐）。 */
const ENGINE_PLUGIN_GROUPS = Object.entries(
  ENGINE_PLUGIN_OPTIONS.reduce<Record<string, Array<{ value: string; label: string }>>>((acc, p) => {
    ;(acc[p.category] ||= []).push({ value: p.id, label: p.name })
    return acc
  }, {}),
).map(([category, options]) => ({
  label: ENGINE_PLUGIN_CATEGORY_LABELS[category] ?? category,
  options,
}))

const { Paragraph, Text, Title } = Typography

const PHASE_TEXT: Record<string, string> = {
  queued: '排队中',
  generating_tests: '生成攻击用例',
  executing_probes: '执行探针',
  engine_judging: '引擎判定',
  compiling_report: '生成报告',
  succeeded: '评测完成',
  failed: '评测失败',
  canceled: '已取消',
}

const SEVERITY_COLOR: Record<string, string> = {
  critical: 'magenta', high: 'red', medium: 'orange', low: 'green', none: 'default',
}

function phaseStepIndex(phase: string): number {
  switch (phase) {
    case 'queued': return 0
    case 'generating_tests': return 1
    case 'executing_probes': return 2
    case 'engine_judging': case 'compiling_report': return 3
    default: return 3
  }
}

/** 攻击成功明细：聚合插件/策略维度 attack_success>0 的条目（脱敏摘要来自 top_reasons）。 */
function collectSuccessDetails(result: EngineRunResult) {
  const rows: Array<{ key: string; dimension: string; id: string; probes: number; attack_success: number; success_rate: number; max_severity?: string; top_reasons: string[] }> = []
  const labelById = new Map<string, string>()
  for (const p of result.plugin_stats || []) labelById.set(p.plugin_id, p.label)
  for (const cat of result.risk_categories || []) {
    for (const p of cat.plugins || []) {
      if (p.attack_success > 0) {
        rows.push({
          key: `cat-${cat.key}-${p.plugin_id}`,
          dimension: `插件 · ${cat.label}`,
          id: labelById.get(p.plugin_id) || p.plugin_id,
          probes: p.probes,
          attack_success: p.attack_success,
          success_rate: p.probes > 0 ? p.attack_success / p.probes : 0,
          top_reasons: (p.top_reasons || []).slice(0, 3),
        })
      }
    }
  }
  for (const s of result.strategy_stats || []) {
    if (s.attack_success > 0) {
      rows.push({
        key: `strategy-${s.id}`,
        dimension: '策略',
        id: s.label || s.id,
        probes: s.probes,
        attack_success: s.attack_success,
        success_rate: s.success_rate,
        max_severity: s.max_severity,
        top_reasons: [],
      })
    }
  }
  return rows
}

function SeverityTags({ counts }: { counts?: Record<string, number> }) {
  const entries = Object.entries(counts || {}).filter(([, n]) => n > 0)
  if (entries.length === 0) return <Text type="secondary">-</Text>
  return <Space size={4} wrap>{entries.map(([k, n]) => <Tag key={k} color={SEVERITY_COLOR[k] ?? 'default'}>{k}: {n}</Tag>)}</Space>
}

/** 结果明细区：风险类别分组 + 脱敏原因摘要 + 攻击成功明细（均为脱敏数据，完整内容见报告）。 */
function ResultDetails({ result }: { result: EngineRunResult }) {
  const successRows = collectSuccessDetails(result)
  const categories = result.risk_categories || []
  return (
    <>
      <Alert
        type="info"
        showIcon
        style={{ margin: '12px 0' }}
        message="以下为脱敏摘要，完整攻击样例与目标响应见正式报告。"
      />
      {categories.length > 0 && (
        <Collapse
          size="small"
          style={{ marginBottom: 12 }}
          items={categories.map(cat => ({
            key: cat.key,
            label: (
              <Space size={8} wrap>
                <Text strong>{cat.label}</Text>
                <Tag>{cat.count} 探针</Tag>
                <SeverityTags counts={cat.severity_counts} />
              </Space>
            ),
            children: (
              <Table
                size="small"
                pagination={false}
                rowKey="plugin_id"
                dataSource={cat.plugins || []}
                columns={[
                  { title: '插件', dataIndex: 'plugin_id', key: 'plugin_id', width: 180 },
                  { title: '探针', dataIndex: 'probes', key: 'probes', width: 70 },
                  { title: '攻击成功', dataIndex: 'attack_success', key: 'attack_success', width: 90,
                    render: (v: number) => <span style={{ color: v > 0 ? '#dc2626' : undefined }}>{v}</span> },
                  { title: '脱敏原因摘要（≤3 条）', dataIndex: 'top_reasons', key: 'top_reasons',
                    render: (reasons: string[]) => (reasons?.length
                      ? <ul style={{ margin: 0, paddingLeft: 18 }}>{reasons.map((r, i) => <li key={i}>{r}</li>)}</ul>
                      : <Text type="secondary">-</Text>) },
                ]}
              />
            ),
          }))}
        />
      )}
      <div style={{ marginBottom: 8 }}>
        {successRows.length > 0
          ? <Text strong>攻击成功明细（{successRows.length} 项）</Text>
          : <Text strong>未发现攻击成功（与报告“最高安全”一致）</Text>}
      </div>
      {successRows.length > 0 && (
        <Table size="small" pagination={false} rowKey="key" dataSource={successRows}
          columns={[
            { title: '维度', dataIndex: 'dimension', key: 'dimension', width: 160 },
            { title: '插件/策略', dataIndex: 'id', key: 'id', width: 160 },
            { title: '探针', dataIndex: 'probes', key: 'probes', width: 70 },
            { title: '攻击成功', dataIndex: 'attack_success', key: 'attack_success', width: 90,
              render: (v: number) => <span style={{ color: '#dc2626' }}>{v}</span> },
            { title: '成功率', dataIndex: 'success_rate', key: 'success_rate', width: 80,
              render: (v: number) => `${(v * 100).toFixed(0)}%` },
            { title: '最高严重度', dataIndex: 'max_severity', key: 'max_severity', width: 110,
              render: (v?: string) => (v ? <Tag color={SEVERITY_COLOR[v] ?? 'default'}>{v}</Tag> : <Text type="secondary">-</Text>) },
            { title: '脱敏原因摘要', dataIndex: 'top_reasons', key: 'top_reasons',
              render: (reasons: string[]) => (reasons?.length
                ? <ul style={{ margin: 0, paddingLeft: 18 }}>{reasons.map((r, i) => <li key={i}>{r}</li>)}</ul>
                : <Text type="secondary">-</Text>) },
          ]} />
      )}
    </>
  )
}

export function EngineEvalPage() {
  const [form] = Form.useForm()
  const [submitting, setSubmitting] = useState(false)
  const [activeRun, setActiveRun] = useState<EngineRun | null>(null)
  const [history, setHistory] = useState<EngineRun[]>([])
  const [loadingHistory, setLoadingHistory] = useState(false)
  const pollRef = useRef<number | null>(null)

  const loadHistory = useCallback(async () => {
    setLoadingHistory(true)
    try {
      const items = await engineEvalService.listRuns(20)
      setHistory(items)
    } catch {
      /* 静默：历史加载失败不打断向导 */
    } finally {
      setLoadingHistory(false)
    }
  }, [])

  useEffect(() => {
    void loadHistory()
    return () => { if (pollRef.current) window.clearInterval(pollRef.current) }
  }, [loadHistory])

  const startPolling = useCallback((runId: string) => {
    if (pollRef.current) window.clearInterval(pollRef.current)
    pollRef.current = window.setInterval(async () => {
      try {
        const run = await engineEvalService.getRun(runId)
        setActiveRun(run)
        if (['succeeded', 'failed', 'canceled'].includes(run.phase)) {
          if (pollRef.current) window.clearInterval(pollRef.current)
          void loadHistory()
        }
      } catch {
        /* 轮询失败保留下次重试 */
      }
    }, 3000)
  }, [loadHistory])

  const onSubmit = async (values: { purpose: string; num_tests: number; plugins: string[]; strategies?: string[] }) => {
    setSubmitting(true)
    try {
      const run = await engineEvalService.createRun({
        purpose: values.purpose,
        num_tests: values.num_tests,
        plugins: values.plugins.map(id => ({ id })),
        strategies: (values.strategies ?? ['direct']).map(id => ({ id })),
        judge_mode: 'auto',
      })
      setActiveRun(run)
      startPolling(run.id)
      message.success('评测任务已提交')
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error || '提交失败'
      message.error(msg)
    } finally {
      setSubmitting(false)
    }
  }

  const result = activeRun?.result
  const running = activeRun && !['succeeded', 'failed', 'canceled'].includes(activeRun.phase)

  return (
    <div style={{ padding: 24, background: 'var(--bg-base, #f6f7f9)', minHeight: '100%' }}>
      {/* 对话优先引导 */}
      <Alert
        type="info"
        showIcon
        message="推荐使用 AI 对话发起评测"
        description={
          <Space>
            <span>AI 对话可自动推荐攻击插件、生成确认卡片并沉淀正式报告。</span>
            <Link to="/enterprise"><Button type="primary" size="small" icon={<ArrowLeftOutlined />}>返回 AI 对话</Button></Link>
          </Space>
        }
        style={{ marginBottom: 16 }}
      />

      <Row gutter={16}>
        {/* 左：向导表单 */}
        <Col xs={24} lg={10}>
          <Card>
            <Title level={5}>自选评测（promptfoo 引擎）</Title>
            <Paragraph type="secondary">
              平台将使用默认生成模型合成攻击用例，对当前已连接的被测模型执行安全评测。
            </Paragraph>
            <Form form={form} layout="vertical" onFinish={onSubmit}
              initialValues={{ num_tests: 5, plugins: ['harmful'], strategies: ['direct'] }}>
              <Form.Item name="purpose" label="评测目的" rules={[{ required: true, message: '请描述评测目的' }]}>
                <Select mode="tags" placeholder="例如：企业客服助手的合规安全评测" maxTagCount={1} />
              </Form.Item>
              <Form.Item name="num_tests" label="用例数量" rules={[{ required: true }]}>
                <InputNumber min={1} max={50} style={{ width: '100%' }} />
              </Form.Item>
              <Form.Item name="plugins" label="攻击插件" rules={[{ required: true, message: '至少选择一个插件' }]}>
                <Select mode="multiple" placeholder="选择攻击类别"
                  options={ENGINE_PLUGIN_GROUPS} />
              </Form.Item>
              <Form.Item name="strategies" label="攻击策略">
                <Select mode="multiple" placeholder="默认直接攻击"
                  options={ENGINE_STRATEGY_OPTIONS.map(s => ({ value: s.id, label: s.name }))} />
              </Form.Item>
              <Button type="primary" htmlType="submit" block loading={submitting} icon={<PlayCircleOutlined />}>
                开始评测
              </Button>
            </Form>
          </Card>
        </Col>

        {/* 右：进度与结果 */}
        <Col xs={24} lg={14}>
          <Card>
            {!activeRun ? (
              <div style={{ padding: 40, textAlign: 'center', color: '#94a3b8' }}>
                提交评测后，此处展示实时进度与结果。
              </div>
            ) : (
              <>
                <Steps size="small" current={phaseStepIndex(activeRun.phase)} status={activeRun.phase === 'failed' ? 'error' : undefined}
                  items={[
                    { title: '排队' }, { title: '生成用例' }, { title: '执行探针' }, { title: '判定汇总' },
                  ]} />
                <div style={{ margin: '16px 0' }}>
                  <Progress
                    percent={activeRun.planned_count > 0 ? Math.round((activeRun.executed_count / activeRun.planned_count) * 100) : (activeRun.phase === 'succeeded' ? 100 : 5)}
                    status={activeRun.phase === 'failed' ? 'exception' : activeRun.phase === 'succeeded' ? 'success' : 'active'}
                  />
                  <Text type="secondary">
                    {PHASE_TEXT[activeRun.phase] ?? activeRun.phase}
                    {activeRun.current_stage ? ` · ${activeRun.current_stage}` : ''}
                    {activeRun.planned_count > 0 ? ` · ${activeRun.executed_count}/${activeRun.planned_count}` : ''}
                    {activeRun.duration_ms > 0 ? ` · ${(activeRun.duration_ms / 1000).toFixed(1)}s` : ''}
                  </Text>
                  {activeRun.error_code ? <div><Tag color="red">{activeRun.error_code}</Tag></div> : null}
                </div>

                {result ? (
                  <>
                    <Row gutter={12} style={{ marginBottom: 16 }}>
                      <Col span={8}><Card size="small"><Text type="secondary">探针总数</Text><Title level={4} style={{ margin: 0 }}>{result.totals.probes}</Title></Card></Col>
                      <Col span={8}><Card size="small"><Text type="secondary">攻击成功</Text><Title level={4} style={{ margin: 0, color: result.totals.attack_success > 0 ? '#dc2626' : '#16a34a' }}>{result.totals.attack_success}</Title></Card></Col>
                      <Col span={8}><Card size="small"><Text type="secondary">拦截率</Text><Title level={4} style={{ margin: 0 }}>{(result.totals.pass_rate * 100).toFixed(0)}%</Title></Card></Col>
                    </Row>
                    <Table size="small" pagination={false} dataSource={result.plugin_stats}
                      rowKey="plugin_id"
                      columns={[
                        { title: '插件', dataIndex: 'plugin_id', key: 'plugin_id' },
                        { title: '探针', dataIndex: 'probes', key: 'probes', width: 70 },
                        { title: '攻击成功', dataIndex: 'attack_success', key: 'attack_success', width: 90,
                          render: (v: number) => <span style={{ color: v > 0 ? '#dc2626' : undefined }}>{v}</span> },
                        { title: '成功率', dataIndex: 'success_rate', key: 'success_rate', width: 80,
                          render: (v: number) => `${(v * 100).toFixed(0)}%` },
                        { title: '最高严重度', dataIndex: 'max_severity', key: 'max_severity', width: 110,
                          render: (v: string) => <Tag color={SEVERITY_COLOR[v] ?? 'default'}>{v}</Tag> },
                      ]} />
                    <ResultDetails result={result} />
                  </>
                ) : running ? <Spin /> : null}
              </>
            )}
          </Card>
        </Col>
      </Row>

      {/* 历史 run 列表 */}
      <Card title="评测历史" style={{ marginTop: 16 }} extra={<Button size="small" icon={<ReloadOutlined />} onClick={() => void loadHistory()} loading={loadingHistory}>刷新</Button>}>
        <Table size="small" rowKey="id" dataSource={history} pagination={{ pageSize: 5 }}
          columns={[
            { title: '时间', dataIndex: 'created_at', key: 'created_at', width: 170,
              render: (v: string) => new Date(v).toLocaleString('zh-CN') },
            { title: '状态', dataIndex: 'phase', key: 'phase', width: 100,
              render: (v: string) => <Tag color={v === 'succeeded' ? 'green' : v === 'failed' ? 'red' : 'blue'}>{PHASE_TEXT[v] ?? v}</Tag> },
            { title: '探针', key: 'probes', width: 70,
              render: (_: unknown, r: EngineRun) => r.result?.totals.probes ?? r.executed_count },
            { title: '攻击成功', key: 'success', width: 90,
              render: (_: unknown, r: EngineRun) => r.result?.totals.attack_success ?? '-' },
            { title: '插件', key: 'plugins', ellipsis: true,
              render: (_: unknown, r: EngineRun) => (r.plugins ?? []).join('、') },
          ]}
          expandable={{
            expandedRowRender: (r: EngineRun) => (
              <div>
                <Descriptions size="small" column={3}>
                  <Descriptions.Item label="评测目的" span={3}>{r.purpose}</Descriptions.Item>
                  <Descriptions.Item label="用例数">{r.num_tests}</Descriptions.Item>
                  <Descriptions.Item label="耗时">{(r.duration_ms / 1000).toFixed(1)}s</Descriptions.Item>
                  <Descriptions.Item label="错误码">{r.error_code || '-'}</Descriptions.Item>
                </Descriptions>
                {r.result ? (
                  <>
                    <div style={{ margin: '8px 0 4px' }}>
                      {Object.entries(r.result.severity_counts || {}).filter(([, n]) => n > 0).map(([k, n]) => (
                        <Tag key={k} color={SEVERITY_COLOR[k] ?? 'default'} style={{ marginRight: 8 }}>{k}: {n}</Tag>
                      ))}
                      {r.result.totals ? <Tag color="blue">拦截率 {(r.result.totals.pass_rate * 100).toFixed(0)}%</Tag> : null}
                    </div>
                    <Table size="small" pagination={false} rowKey="plugin_id" dataSource={r.result.plugin_stats || []}
                      columns={[
                        { title: '插件', dataIndex: 'plugin_id' },
                        { title: '探针', dataIndex: 'probes', width: 70 },
                        { title: '攻击成功', dataIndex: 'attack_success', width: 90 },
                        { title: '成功率', dataIndex: 'success_rate', width: 80, render: (v: number) => `${(v * 100).toFixed(0)}%` },
                        { title: '最高严重度', dataIndex: 'max_severity', width: 110, render: (v: string) => <Tag color={SEVERITY_COLOR[v] ?? 'default'}>{v}</Tag> },
                      ]} />
                    <ResultDetails result={r.result} />
                  </>
                ) : null}
              </div>
            ),
          }} />
      </Card>
    </div>
  )
}
