/**
 * 自选评测向导页（Phase 2）：不经聊天工作台直接发起 promptfoo 引擎评测。
 * 页首引导回 AI 对话（对话优先），表单 → 进度 → 结果三段式 + 历史记录。
 */
import type { ReactNode } from 'react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Alert, Button, Card, Col, Collapse, Descriptions, Form, InputNumber, Progress, Row, Select, Space, Spin, Steps, Table, Tag, Tooltip, Typography, message,
} from 'antd'
import { ArrowLeftOutlined, PlayCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import {
  ENGINE_PLUGIN_CATEGORY_LABELS, ENGINE_PLUGIN_OPTIONS, ENGINE_STRATEGY_OPTIONS,
  REMOTE_GENERATION_UNAVAILABLE_REASON, RETIRED_ENGINE_PLUGINS, engineEvalService, explainPluginConfigRejection, explainPluginIDRejection, explainRemoteGenerationRejection,
  isPluginConfigGated, isPluginRemoteGenerationGated, isStrategyRemoteGenerationGated, retiredEnginePluginGuidance,
  toEngineSelectOption, totalPluginFanout,
} from '../../services/engineEval'
import type { EngineRun, EngineRunResult } from '../../services/engineEval'

/**
 * 插件选项按风险类别分组（与后端 catalog 的 category 对齐）。
 *
 * U6：被标记 `requires_remote_generation` 的条目**置灰但保留可见**，并在灰态
 * 上挂 Tooltip 说明屏蔽原因。用户必须能看到「这个能力存在但此处不可用」——
 * 若直接隐藏，用户会以为平台不具备该能力，从而忽略这块风险面。
 */
const ENGINE_PLUGIN_GROUPS = Object.entries(
  ENGINE_PLUGIN_OPTIONS.reduce<Record<string, ReturnType<typeof toEngineSelectOption>[]>>((acc, p) => {
    ;(acc[p.category] ||= []).push(toEngineSelectOption(p))
    return acc
  }, {}),
).map(([category, options]) => ({
  label: ENGINE_PLUGIN_CATEGORY_LABELS[category] ?? category,
  options,
}))

const ENGINE_STRATEGY_SELECT_OPTIONS = ENGINE_STRATEGY_OPTIONS.map(toEngineSelectOption)

/**
 * 选项统一渲染：置灰 + 就地显示屏蔽原因 + 悬停补充完整说明；缺配置的项改为
 * 「可选中 + 就地提示」。
 *
 * antd v5 的 `optionRender` 里包 `<Tooltip>` 不可靠：Tooltip 会把
 * `aria-describedby` 挂到内层 span 上，但虚拟列表只在可视窗口内挂载 tooltip
 * 节点，headless 下实测 `.ant-tooltip` 始终为 0 个——即悬停不出现。
 * 因此**原因直接写进选项本身**（不依赖悬停即可读），Tooltip 仅作为补充。
 *
 * P2-1 把两种「跑不通」分开处置，所以它们在这里也必须长得不一样：
 *   - 远程依赖（`requires_remote_generation`）→ 真的选不了，置灰 + 原因；
 *   - 缺配置（`requires_config`，当前仅 `policy`）→ 能选，但本次会生成 0 条
 *     用例，所以给一句「需先提供策略文档」的提示而**不置灰**——置灰会把一个
 *     即将支持的能力伪装成不可用。
 */
function renderDisabledOption(option: unknown): ReactNode {
  const data = option as { label?: string; disabled?: boolean; reason?: string; configReason?: string } | undefined
  const configReason = data?.configReason
  if (configReason && data?.disabled !== true) {
    return (
      <Tooltip title={configReason}>
        <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span>
            {data.label}
            <Tag color="warning" style={{ marginLeft: 6, fontSize: 11 }}>需配置</Tag>
          </span>
          {/* 提示就地可见：不悬停也能读懂为什么这个选项「能选但跑不出用例」 */}
          <span style={{ color: 'rgba(0,0,0,0.45)', fontSize: 12, lineHeight: 1.4 }}>{configReason}</span>
        </span>
      </Tooltip>
    )
  }
  if (!data || data.disabled !== true) {
    return data?.label ?? ''
  }
  const reason = data.reason ?? REMOTE_GENERATION_UNAVAILABLE_REASON
  return (
    <Tooltip title={reason}>
      <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
        <span>
          <span style={{ color: 'rgba(0,0,0,0.25)' }}>{data.label}</span>
          <Tag color="default" style={{ marginLeft: 6, fontSize: 11 }}>需远程生成</Tag>
        </span>
        {/* 屏蔽原因就地可见：不悬停也能读懂为什么置灰 */}
        <span style={{ color: 'rgba(0,0,0,0.45)', fontSize: 12, lineHeight: 1.4 }}>{reason}</span>
      </span>
    </Tooltip>
  )
}

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

  // U6：置灰项数量（供下方 Alert 说明，不隐藏任何条目）。
  const gatedPluginCount = useMemo(
    () => ENGINE_PLUGIN_OPTIONS.filter((p) => p.requires_remote_generation).length,
    [],
  )
  const gatedStrategyCount = useMemo(
    () => ENGINE_STRATEGY_OPTIONS.filter((s) => s.requires_remote_generation).length,
    [],
  )

  // P2-1：成本提示的数据源。
  //
  // 平台的「用例数量」与 promptfoo 的 `numTests` 语义不同：后者对**每个展开
  // 后的子插件**生效，所以实际探针数是 `num_tests × Σfanout`（`harmful`=26）。
  // 不显示这个乘积，用户填 5 会以为跑 5 条，实际是 130 条——这是平台单方面
  // 制造的财务风险，且用户无从预见。
  // `?? []` 每次渲染都会新建数组，会让下面两个 useMemo 在每次输入时失效，
  // 所以把兜底值也 memo 掉。
  const watchedPluginsRaw = Form.useWatch<string[]>('plugins', form)
  const watchedPlugins = useMemo(() => watchedPluginsRaw ?? [], [watchedPluginsRaw])
  const watchedNumTests = Form.useWatch<number>('num_tests', form) ?? 0
  const expandedPlugins = useMemo(() => totalPluginFanout(watchedPlugins), [watchedPlugins])
  const plannedProbes = expandedPlugins > 0 && watchedNumTests > 0 ? expandedPlugins * watchedNumTests : 0
  // 缺配置的插件（当前仅 `policy`）：能选，但本次会生成 0 条用例。与置灰的
  // 远程依赖分开提示——两者的处置完全不同（补配置 vs 换选项）。
  const configGatedSelected = useMemo(
    () => watchedPlugins.filter((id) => isPluginConfigGated(id)),
    [watchedPlugins],
  )

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
    // U6 客户端侧二次拦截：后端与引擎都会拒绝，但选择器已置灰，这里只是防止
    // 表单被预填/粘贴绕过时把一个必然被拒的请求发出去。
    const gated = (values.plugins ?? []).filter(isPluginRemoteGenerationGated)
    const gatedStrategies = (values.strategies ?? []).filter(isStrategyRemoteGenerationGated)
    if (gated.length > 0 || gatedStrategies.length > 0) {
      message.error(
        `${REMOTE_GENERATION_UNAVAILABLE_REASON}。本次选择包含不可用能力：${[...gated, ...gatedStrategies].join('、')}。请改选可本地生成的能力。`,
      )
      return
    }
    // P2-1/P2-2 客户端侧二次拦截：已下线的插件 id 不在选择器里，只能从预填或
    // 粘贴进来。后端与引擎都会拒，这里只是把「必然失败」提前到提交前，并顺带
    // 告诉用户能力搬到哪里去了。
    const retired = (values.plugins ?? []).filter((id) => retiredEnginePluginGuidance(id))
    if (retired.length > 0) {
      const details = retired
        .map((id) => retiredEnginePluginGuidance(id)?.replacement_label ?? id)
        .join(' ')
      message.error(`本次选择包含已下线的插件 id：${retired.join('、')}。${details}`)
      return
    }
    // P2-1/P2-2 客户端侧二次拦截：缺配置的插件能选中，但本次会生成 0 条用例。
    // 后端已显式拒绝，这里提前到提交前，省一次往返。
    const needsConfig = (values.plugins ?? []).filter((id) => isPluginConfigGated(id))
    if (needsConfig.length > 0) {
      message.error(`本次选择包含需要先提供策略文档的插件：${needsConfig.join('、')}，本次会生成 0 条用例。请改选其它插件。`)
      return
    }
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
      // U6：远程依赖拒绝是可读的业务错误，单独翻译；其余沿用原提示。
      const data = (e as { response?: { data?: unknown } })?.response?.data
      const rejection = explainRemoteGenerationRejection(data)
        ?? explainPluginIDRejection(data)
        ?? explainPluginConfigRejection(data)
      if (rejection) {
        message.error(rejection.message)
      } else {
        const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error || '提交失败'
        message.error(msg)
      }
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
              <Form.Item
                name="num_tests"
                label="用例数量"
                rules={[{ required: true }]}
                extra={plannedProbes > 0
                  ? `预计执行约 ${plannedProbes} 条探针（用例数量 ${watchedNumTests} × 选中插件展开后的子插件数 ${expandedPlugins}）。`
                  : '用例数量按「每个展开后的子插件」计算，实际探针数为两者之积。'}
              >
                <InputNumber min={1} max={50} style={{ width: '100%' }} />
              </Form.Item>
              <Form.Item name="plugins" label="攻击插件" rules={[{ required: true, message: '至少选择一个插件' }]}>
                <Select mode="multiple" placeholder="选择攻击类别"
                  options={ENGINE_PLUGIN_GROUPS}
                  optionRender={(option) => renderDisabledOption(option.data)} />
              </Form.Item>
              <Form.Item name="strategies" label="攻击策略">
                <Select mode="multiple" placeholder="默认直接攻击"
                  options={ENGINE_STRATEGY_SELECT_OPTIONS}
                  optionRender={(option) => renderDisabledOption(option.data)} />
              </Form.Item>
              {/* P2-1：缺配置的插件是「能选，但本次跑不出用例」，与置灰的远程
                  依赖是两种失败，所以用独立提示说明，而不是把它也置灰。 */}
              {configGatedSelected.length > 0 && (
                <Alert
                  type="warning"
                  showIcon
                  style={{ marginBottom: 16 }}
                  message={`${configGatedSelected.join('、')} 需要先提供策略文档`}
                  description="该插件本身可用，但平台尚未收集策略文档文本，选中它本次会生成 0 条用例并使运行失败（不是降级为 0 覆盖）。请改选其它插件，或等策略文档上传入口上线。"
                />
              )}
              <Button type="primary" htmlType="submit" block loading={submitting} icon={<PlayCircleOutlined />}>
                开始评测
              </Button>
            </Form>
            {/* U6：说明为何部分能力置灰——避免用户把「屏蔽」误读为「平台没有」。 */}
            <Alert
              type="warning"
              showIcon
              style={{ marginTop: 16 }}
              message={`${gatedPluginCount} 个插件 / ${gatedStrategyCount} 个策略当前不可选`}
              description={`${REMOTE_GENERATION_UNAVAILABLE_REASON}。这些能力仍然列在上方（置灰），只是在本环境无法产生评测用例——若强行提交，引擎会直接报错而不会静默跑 0 条。`}
            />
            {/* P2-1：已下线的插件 id 及其替代路径。
                只删除会解决「会报错」，不解决「用户要的能力去哪了」——这三个 id
                从未跑通过（promptfoo 不接受它们），所以这里是陈述事实而非承认
                功能退化，同时把用户指向真正能用的入口。 */}
            <Collapse
              size="small"
              ghost
              style={{ marginTop: 8 }}
              items={[{
                key: 'retired',
                label: <Text type="secondary">{RETIRED_ENGINE_PLUGINS.length} 个已下线的插件 id 及替代路径</Text>,
                children: (
                  <ul style={{ margin: 0, paddingLeft: 18 }}>
                    {RETIRED_ENGINE_PLUGINS.map((item) => (
                      <li key={item.id} style={{ marginBottom: 6 }}>
                        <Text delete>{item.name}</Text>
                        <Text type="secondary">（{item.id}）：{item.replacement_label}</Text>
                      </li>
                    ))}
                  </ul>
                ),
              }]}
            />
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
