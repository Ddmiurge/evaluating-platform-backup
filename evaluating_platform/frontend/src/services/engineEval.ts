/**
 * Promptfoo 引擎评测 API 客户端（Phase 2 自选评测向导）。
 * 后端契约见 internal/api/handler/engine_run.go。
 */
import api from './api'

export interface EngineRunPluginStat {
  plugin_id: string
  label: string
  probes: number
  attack_success: number
  success_rate: number
  max_severity: string
}

/** 风险类别内的插件明细（top_reasons 已由引擎 redactReason 脱敏，≤200 字）。 */
export interface EngineRunCategoryPlugin {
  plugin_id: string
  probes: number
  attack_success: number
  top_reasons: string[]
}

export interface EngineRunResult {
  totals: { probes: number; attack_success: number; pass_rate: number }
  severity_counts: Record<string, number>
  risk_categories?: Array<{
    key: string
    label: string
    count: number
    severity_counts: Record<string, number>
    plugins?: EngineRunCategoryPlugin[]
  }>
  plugin_stats: EngineRunPluginStat[]
  strategy_stats?: Array<{ id: string; label?: string; probes: number; attack_success: number; success_rate: number; max_severity?: string }>
  token_usage?: { generation: number; judging: number; target: number }
}

export interface EngineRun {
  id: string
  purpose: string
  phase: string
  planned_count: number
  executed_count: number
  current_stage?: string
  duration_ms: number
  error_code?: string
  created_at: string
  updated_at?: string
  num_tests: number
  plugins: string[]
  strategies: string[]
  result?: EngineRunResult
}

export interface EngineRunCreateInput {
  purpose: string
  num_tests: number
  plugins: Array<{ id: string }>
  strategies?: Array<{ id: string }>
  judge_mode?: string
}

export const engineEvalService = {
  async createRun(input: EngineRunCreateInput): Promise<EngineRun> {
    const res = await api.post<EngineRun>('/maclaw/engine/runs', input)
    return res.data
  },
  async getRun(id: string): Promise<EngineRun> {
    const res = await api.get<EngineRun>(`/maclaw/engine/runs/${encodeURIComponent(id)}`)
    return res.data
  },
  async listRuns(limit = 20): Promise<EngineRun[]> {
    const res = await api.get<{ items: EngineRun[] }>('/maclaw/engine/runs', { params: { limit } })
    return res.data.items ?? []
  },
  async cancelRun(id: string): Promise<EngineRun> {
    const res = await api.post<EngineRun>(`/maclaw/engine/runs/${encodeURIComponent(id)}/cancel`)
    return res.data
  },
}

/** 内置引擎插件目录（由后端 promptfoo_plugin_catalog.go 生成的同源清单，勿手改 id）。 */
export const ENGINE_PLUGIN_OPTIONS: ReadonlyArray<EnginePluginCatalogOption> = [
{ id: 'harmful', name: '有害内容检测', category: 'harmful' },
{ id: 'harmful:child-exploitation', name: '儿童剥削内容', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:chemical-biological-weapons', name: '化学生物武器', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:indiscriminate-weapons', name: '大规模杀伤武器', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:weapons:ied', name: '简易爆炸装置', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:cybercrime', name: '网络犯罪协助检测', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:cybercrime:malicious-code', name: '恶意代码编写', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:violent-crime', name: '暴力犯罪协助检测', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:illegal-activities', name: '违法活动协助检测', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:illegal-drugs', name: '违禁药物', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:illegal-drugs:meth', name: '冰毒制造', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:hate', name: '仇恨言论', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:harassment-bullying', name: '骚扰霸凌', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:insults', name: '人身攻击辱骂', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:profanity', name: '脏话粗口', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:radicalization', name: '极端化煽动', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:self-harm', name: '自残自杀', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:sex-crime', name: '性犯罪', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:sexual-content', name: '色情内容', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:graphic-content', name: '血腥暴力内容', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:misinformation-disinformation', name: '虚假信息有害宣传', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:privacy', name: '隐私侵犯协助', category: 'harmful' },
{ id: 'harmful:copyright-violations', name: '版权侵犯', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:intellectual-property', name: '知识产权侵犯', category: 'harmful' },
{ id: 'harmful:specialized-advice', name: '无资质专业建议', category: 'harmful', requires_remote_generation: true },
{ id: 'harmful:unsafe-practices', name: '危险行为教唆', category: 'harmful', requires_remote_generation: true },
{ id: 'pii', name: '隐私信息泄露检测', category: 'privacy' },
{ id: 'pii:direct', name: '直接索要隐私', category: 'privacy' },
{ id: 'pii:social', name: '社工套取隐私', category: 'privacy' },
{ id: 'pii:api-db', name: 'API/数据库隐私外泄', category: 'privacy' },
{ id: 'pii:session', name: '会话数据泄露', category: 'privacy' },
{ id: 'cross-session-leak', name: '跨会话数据泄漏', category: 'privacy' },
{ id: 'prompt-injection', name: '提示注入检测', category: 'injection' },
{ id: 'indirect-prompt-injection', name: '间接提示注入检测', category: 'injection', requires_remote_generation: true },
{ id: 'ascii-smuggling', name: 'ASCII 走私检测', category: 'injection', requires_remote_generation: true },
{ id: 'excessive-agency', name: '过度代理行为检测', category: 'injection' },
{ id: 'hijacking', name: '对话劫持', category: 'injection', requires_remote_generation: true },
{ id: 'system-prompt-override', name: '系统提示覆盖', category: 'injection', requires_remote_generation: true },
{ id: 'prompt-extraction', name: '系统提示词窃取', category: 'injection' },
{ id: 'mcp', name: 'MCP 协议攻击', category: 'injection', requires_remote_generation: true },
{ id: 'agentic:memory-poisoning', name: 'Agent 记忆投毒', category: 'injection', requires_remote_generation: true },
{ id: 'jailbreak', name: '越狱对抗检测', category: 'jailbreak' },
{ id: 'bias', name: '偏见与歧视检测', category: 'bias' },
{ id: 'bias:age', name: '年龄偏见', category: 'bias', requires_remote_generation: true },
{ id: 'bias:gender', name: '性别偏见', category: 'bias', requires_remote_generation: true },
{ id: 'bias:race', name: '种族偏见', category: 'bias', requires_remote_generation: true },
{ id: 'bias:disability', name: '残障偏见', category: 'bias', requires_remote_generation: true },
{ id: 'security-exploit', name: '安全利用类检测', category: 'security-exploit' },
{ id: 'reasoning-dos', name: '推理资源耗尽', category: 'security-exploit', requires_remote_generation: true },
{ id: 'hallucination', name: '幻觉与事实性检测', category: 'hallucination' },
{ id: 'overreliance', name: '过度信赖', category: 'hallucination' },
{ id: 'divergent-repetition', name: '重复发散输出', category: 'hallucination' },
{ id: 'off-topic', name: '离题与边界检测', category: 'off-topic', requires_remote_generation: true },
{ id: 'wordplay', name: '文字游戏', category: 'off-topic', requires_remote_generation: true },
{ id: 'competitors', name: '竞品与品牌风险检测', category: 'brand', requires_remote_generation: true },
{ id: 'imitation', name: '名人模仿', category: 'brand' },
{ id: 'politics', name: '政治敏感话题', category: 'sensitive' },
{ id: 'religion', name: '宗教敏感话题', category: 'sensitive', requires_remote_generation: true },
{ id: 'teen-safety:age-restricted-goods-and-services', name: '未成年人保护', category: 'sensitive' },
{ id: 'contracts', name: '合同法律错误', category: 'industry' },
{ id: 'policy', name: '政策违规', category: 'industry' },
{ id: 'beavertails', name: 'BeaverTails 有害问答基准', category: 'dataset' },
{ id: 'harmbench', name: 'HarmBench 攻击技术基准', category: 'dataset' },
{ id: 'pliny', name: 'Pliny 越狱库基准', category: 'dataset' },
{ id: 'donotanswer', name: 'DoNotAnswer 拒答基准', category: 'dataset' },
{ id: 'cyberseceval', name: 'CyberSecEval 网络安全基准', category: 'dataset' },
{ id: 'xstest', name: 'XSTest 过度拒答基准', category: 'dataset' },
{ id: 'medical:hallucination', name: '医疗幻觉', category: 'industry', requires_remote_generation: true },
{ id: 'medical:incorrect-knowledge', name: '医疗错误知识', category: 'industry', requires_remote_generation: true },
{ id: 'medical:off-label-use', name: '超说明书用药', category: 'industry', requires_remote_generation: true },
{ id: 'medical:prioritization-error', name: '分诊优先级错误', category: 'industry', requires_remote_generation: true },
{ id: 'medical:sycophancy', name: '医疗迎合', category: 'industry', requires_remote_generation: true },
{ id: 'medical:anchoring-bias', name: '医疗锚定偏差', category: 'industry', requires_remote_generation: true },
{ id: 'medical:fda:ai-disclosure', name: 'FDA AI 披露', category: 'industry', requires_remote_generation: true },
{ id: 'medical:fda:cyber-access-control', name: 'FDA 访问控制', category: 'industry', requires_remote_generation: true },
{ id: 'medical:fda:cyber-audit-tampering', name: 'FDA 审计篡改', category: 'industry', requires_remote_generation: true },
{ id: 'pharmacy:controlled-substance-compliance', name: '管制药品合规', category: 'industry', requires_remote_generation: true },
{ id: 'pharmacy:dosage-calculation', name: '剂量计算', category: 'industry', requires_remote_generation: true },
{ id: 'pharmacy:drug-interaction', name: '药物相互作用', category: 'industry', requires_remote_generation: true },
{ id: 'financial:hallucination', name: '金融幻觉', category: 'industry', requires_remote_generation: true },
{ id: 'financial:calculation-error', name: '金融计算错误', category: 'industry', requires_remote_generation: true },
{ id: 'financial:compliance-violation', name: '金融合规违规', category: 'industry', requires_remote_generation: true },
{ id: 'financial:confidential-disclosure', name: 'MNPI 泄露', category: 'industry', requires_remote_generation: true },
{ id: 'financial:counterfactual', name: '虚构金融叙事', category: 'industry', requires_remote_generation: true },
{ id: 'financial:data-leakage', name: '金融数据泄露', category: 'industry', requires_remote_generation: true },
{ id: 'financial:defamation', name: '金融诽谤', category: 'industry', requires_remote_generation: true },
{ id: 'financial:impartiality', name: '无资质投资建议', category: 'industry', requires_remote_generation: true },
{ id: 'financial:japan-fiea-suitability', name: '日本 FIEA 适当性', category: 'industry', requires_remote_generation: true },
{ id: 'financial:misconduct', name: '金融犯罪协助', category: 'industry', requires_remote_generation: true },
{ id: 'financial:sox-compliance', name: 'SOX 合规', category: 'industry', requires_remote_generation: true },
{ id: 'financial:sycophancy', name: '金融迎合', category: 'industry', requires_remote_generation: true },
{ id: 'insurance:phi-disclosure', name: 'PHI 医疗信息泄露', category: 'industry', requires_remote_generation: true },
{ id: 'insurance:coverage-discrimination', name: '理赔歧视', category: 'industry', requires_remote_generation: true },
{ id: 'insurance:data-disclosure', name: '保单数据泄露', category: 'industry', requires_remote_generation: true },
{ id: 'insurance:network-misinformation', name: '网络信息错误', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:account-takeover', name: '账户接管', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:cpni-disclosure', name: 'CPNI 泄露', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:location-disclosure', name: '位置数据泄露', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:fraud-enablement', name: '电信诈骗协助', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:billing-misinformation', name: '账单误导', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:coverage-misinformation', name: '覆盖误导', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:e911-misinformation', name: 'E911 错误信息', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:porting-misinformation', name: '携号转网误导', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:tcpa-violation', name: 'TCPA 违规', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:accessibility-violation', name: '无障碍合规', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:unauthorized-changes', name: '擅自变更', category: 'industry', requires_remote_generation: true },
{ id: 'telecom:law-enforcement-request-handling', name: '执法请求处理', category: 'industry', requires_remote_generation: true },
{ id: 'realestate:fair-housing-discrimination', name: '公平住房歧视', category: 'industry', requires_remote_generation: true },
{ id: 'realestate:lending-discrimination', name: '贷款歧视', category: 'industry', requires_remote_generation: true },
{ id: 'realestate:steering', name: '引导看房歧视', category: 'industry', requires_remote_generation: true },
{ id: 'realestate:advertising-discrimination', name: '广告歧视', category: 'industry', requires_remote_generation: true },
{ id: 'realestate:discriminatory-listings', name: '歧视性房源', category: 'industry', requires_remote_generation: true },
{ id: 'realestate:accessibility-discrimination', name: '无障碍歧视', category: 'industry', requires_remote_generation: true },
{ id: 'realestate:source-of-income', name: '收入来源歧视', category: 'industry', requires_remote_generation: true },
{ id: 'realestate:valuation-bias', name: '估值偏差', category: 'industry', requires_remote_generation: true },
{ id: 'ecommerce:pci-dss', name: '支付卡数据泄露', category: 'industry', requires_remote_generation: true },
{ id: 'ecommerce:order-fraud', name: '订单欺诈', category: 'industry', requires_remote_generation: true },
{ id: 'ecommerce:price-manipulation', name: '价格操纵', category: 'industry', requires_remote_generation: true },
{ id: 'ecommerce:compliance-bypass', name: '合规绕过', category: 'industry', requires_remote_generation: true },
 ]

export const ENGINE_PLUGIN_CATEGORY_LABELS: Record<string, string> = {
  harmful: "有害内容",
  privacy: "隐私泄露",
  injection: "提示注入",
  jailbreak: "越狱对抗",
  bias: "偏见歧视",
  "security-exploit": "安全利用",
  hallucination: "幻觉与事实性",
  "off-topic": "离题边界",
  brand: "品牌风险",
  sensitive: "敏感话题",
  industry: "行业合规",
  dataset: "数据集基准",
}

export const ENGINE_STRATEGY_OPTIONS: ReadonlyArray<EngineCatalogOption> = [
{ id: 'direct', name: '直接攻击' },
{ id: 'role-play', name: '角色扮演' },
{ id: 'encoding', name: '编码混淆' },
{ id: 'multi-turn', name: '多轮诱导' },
{ id: 'assumed-knowledge', name: '假设已知' },
{ id: 'few-shot', name: '少样本诱导' },
{ id: 'many-shot', name: '多样本诱导' },
{ id: 'translate', name: '翻译绕过' },
{ id: 'base64', name: 'Base64 编码' },
{ id: 'rot13', name: 'ROT13 编码' },
{ id: 'hex', name: '十六进制编码' },
{ id: 'leetspeak', name: 'Leet 字符替换' },
{ id: 'otherEncodings', name: '其他编码变体' },
{ id: 'homoglyph', name: '同形字符混淆' },
{ id: 'crescendo', name: '渐进升级' },
{ id: 'goat', name: 'GOAT 多轮越狱' },
{ id: 'hydra', name: 'Hydra 多分支越狱' },
{ id: 'goblin', name: 'Goblin 多轮攻击' },
{ id: 'simba', name: 'Simba 攻击链' },
{ id: 'iterative', name: '迭代越狱' },
{ id: 'iterative:tree', name: '树搜索越狱' },
{ id: 'iterative:meta', name: '元提示越狱' },
{ id: 'bestOfN', name: 'Best-of-N 多次采样', requires_remote_generation: true },
{ id: 'retry', name: '重试攻击' },
{ id: 'likert', name: 'Likert 量表包装', requires_remote_generation: true },
{ id: 'citation', name: '引用包装', requires_remote_generation: true },
{ id: 'mathPrompt', name: '数学题包装' },
{ id: 'mischievousUser', name: '顽劣用户' },
{ id: 'authoritativeMarkupInjection', name: '权威标记注入' },
{ id: 'singleTurnComposite', name: '单轮组合攻击', requires_remote_generation: true },
]

/* ------------------------------------------------------------------ *
 * U6 · 远程依赖能力护栏（前端侧）
 *
 * promptfoo 在 fail-closed 关闭远程生成时，对依赖远程生成的插件只 `return []`
 * ——不抛错、不中断、退出码仍为 0。用户会看到「评测跑完了、报告出来了」，但该
 * 插件覆盖为 0，界面上一无所知。
 *
 * 本批**不改任何生成逻辑**，只把这层静默失败显式化：目录条目带
 * `requires_remote_generation` 标记（由后端映射表生成，勿手改），选择器对被
 * 标记项**置灰但保留可见**——用户要看到「这个能力存在但此处不可用」，而不是
 * 以为平台没有。屏蔽原因见 REMOTE_GENERATION_UNAVAILABLE_REASON，与后端
 * maclaw.RemoteGenerationUnavailableMessage 保持同一句文案。
 * ------------------------------------------------------------------ */

/** 被远程依赖屏蔽时的原因文案（与后端常量逐字一致，勿单独改）。 */
export const REMOTE_GENERATION_UNAVAILABLE_REASON =
  '需远程生成 · 当前环境已屏蔽（平台强制关闭 promptfoo 远程生成以防数据外泄）'

/** 引擎在请求含被标记项时返回的 error_code（与后端常量一致）。 */
export const REMOTE_GENERATION_ERROR_CODES = {
  plugin: 'plugin_requires_remote_generation',
  strategy: 'strategy_requires_remote_generation',
} as const

/** 一条插件/策略选项。requires_remote_generation 由后端目录生成。 */
export interface EngineCatalogOption {
  id: string
  name: string
  requires_remote_generation?: boolean
}

/** 插件选项额外带风险类别（与后端 catalog 的 category 对齐）。 */
export interface EnginePluginCatalogOption extends EngineCatalogOption {
  category: string
}

/** 灰态选项：保留可见 + 置灰 + 附屏蔽原因，供 Select 的 options 使用。 */
export interface EngineSelectOption {
  value: string
  label: string
  disabled: boolean
  /** 灰态原因；非灰态为 undefined。 */
  reason?: string
}

/**
 * 把目录条目映射为 Select 选项：被远程依赖标记的条目**置灰而非隐藏**。
 *
 * 为什么不隐藏：隐藏会让用户以为平台不具备该能力，从而转而用别的方式覆盖或
 * 干脆忽略这块风险面。置灰 + 说明才是准确表达——能力存在，当前环境不可用。
 */
export function toEngineSelectOption(option: EngineCatalogOption): EngineSelectOption {
  if (option.requires_remote_generation) {
    return {
      value: option.id,
      label: option.name,
      disabled: true,
      reason: REMOTE_GENERATION_UNAVAILABLE_REASON,
    }
  }
  return { value: option.id, label: option.name, disabled: false }
}

/** 判断某个插件 id 是否被远程依赖屏蔽（用于提交前的二次拦截与提示）。 */
export function isPluginRemoteGenerationGated(id: string): boolean {
  return ENGINE_PLUGIN_OPTIONS.some((o) => o.id === id && o.requires_remote_generation === true)
}

/** 判断某个策略 id 是否被远程依赖屏蔽。 */
export function isStrategyRemoteGenerationGated(id: string): boolean {
  return ENGINE_STRATEGY_OPTIONS.some((o) => o.id === id && o.requires_remote_generation === true)
}

/**
 * 把后端 409 拒绝响应翻译成可读提示。返回 null 表示不是远程依赖拒绝，
 * 调用方应按原逻辑处理。
 */
export function explainRemoteGenerationRejection(
  data: unknown,
): { message: string; code: string } | null {
  const body = data as { code?: string; error?: string; blocked_plugins?: string[]; blocked_strategies?: string[] } | null
  if (!body || typeof body.code !== 'string') return null
  if (
    body.code !== REMOTE_GENERATION_ERROR_CODES.plugin &&
    body.code !== REMOTE_GENERATION_ERROR_CODES.strategy
  ) {
    return null
  }
  const blocked = [...(body.blocked_plugins ?? []), ...(body.blocked_strategies ?? [])]
  return {
    code: body.code,
    message: `${REMOTE_GENERATION_UNAVAILABLE_REASON}。${blocked.length > 0 ? `本次请求包含：${blocked.join('、')}。` : ''}请改选可本地生成的能力后重试。`,
  }
}
