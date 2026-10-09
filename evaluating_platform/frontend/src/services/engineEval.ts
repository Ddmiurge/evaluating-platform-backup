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
export const ENGINE_PLUGIN_OPTIONS = [
{ id: 'harmful', name: '有害内容检测', category: 'harmful' },
{ id: 'harmful:child-exploitation', name: '儿童剥削内容', category: 'harmful' },
{ id: 'harmful:chemical-biological-weapons', name: '化学生物武器', category: 'harmful' },
{ id: 'harmful:indiscriminate-weapons', name: '大规模杀伤武器', category: 'harmful' },
{ id: 'harmful:weapons:ied', name: '简易爆炸装置', category: 'harmful' },
{ id: 'harmful:cybercrime', name: '网络犯罪协助检测', category: 'harmful' },
{ id: 'harmful:cybercrime:malicious-code', name: '恶意代码编写', category: 'harmful' },
{ id: 'harmful:violent-crime', name: '暴力犯罪协助检测', category: 'harmful' },
{ id: 'harmful:illegal-activities', name: '违法活动协助检测', category: 'harmful' },
{ id: 'harmful:illegal-drugs', name: '违禁药物', category: 'harmful' },
{ id: 'harmful:illegal-drugs:meth', name: '冰毒制造', category: 'harmful' },
{ id: 'harmful:hate', name: '仇恨言论', category: 'harmful' },
{ id: 'harmful:harassment-bullying', name: '骚扰霸凌', category: 'harmful' },
{ id: 'harmful:insults', name: '人身攻击辱骂', category: 'harmful' },
{ id: 'harmful:profanity', name: '脏话粗口', category: 'harmful' },
{ id: 'harmful:radicalization', name: '极端化煽动', category: 'harmful' },
{ id: 'harmful:self-harm', name: '自残自杀', category: 'harmful' },
{ id: 'harmful:sex-crime', name: '性犯罪', category: 'harmful' },
{ id: 'harmful:sexual-content', name: '色情内容', category: 'harmful' },
{ id: 'harmful:graphic-content', name: '血腥暴力内容', category: 'harmful' },
{ id: 'harmful:misinformation-disinformation', name: '虚假信息有害宣传', category: 'harmful' },
{ id: 'harmful:privacy', name: '隐私侵犯协助', category: 'harmful' },
{ id: 'harmful:copyright-violations', name: '版权侵犯', category: 'harmful' },
{ id: 'harmful:intellectual-property', name: '知识产权侵犯', category: 'harmful' },
{ id: 'harmful:specialized-advice', name: '无资质专业建议', category: 'harmful' },
{ id: 'harmful:unsafe-practices', name: '危险行为教唆', category: 'harmful' },
{ id: 'pii', name: '隐私信息泄露检测', category: 'privacy' },
{ id: 'pii:direct', name: '直接索要隐私', category: 'privacy' },
{ id: 'pii:social', name: '社工套取隐私', category: 'privacy' },
{ id: 'pii:api-db', name: 'API/数据库隐私外泄', category: 'privacy' },
{ id: 'pii:session', name: '会话数据泄露', category: 'privacy' },
{ id: 'cross-session-leak', name: '跨会话数据泄漏', category: 'privacy' },
{ id: 'prompt-injection', name: '提示注入检测', category: 'injection' },
{ id: 'indirect-prompt-injection', name: '间接提示注入检测', category: 'injection' },
{ id: 'ascii-smuggling', name: 'ASCII 走私检测', category: 'injection' },
{ id: 'excessive-agency', name: '过度代理行为检测', category: 'injection' },
{ id: 'hijacking', name: '对话劫持', category: 'injection' },
{ id: 'system-prompt-override', name: '系统提示覆盖', category: 'injection' },
{ id: 'prompt-extraction', name: '系统提示词窃取', category: 'injection' },
{ id: 'mcp', name: 'MCP 协议攻击', category: 'injection' },
{ id: 'agentic:memory-poisoning', name: 'Agent 记忆投毒', category: 'injection' },
{ id: 'jailbreak', name: '越狱对抗检测', category: 'jailbreak' },
{ id: 'bias', name: '偏见与歧视检测', category: 'bias' },
{ id: 'bias:age', name: '年龄偏见', category: 'bias' },
{ id: 'bias:gender', name: '性别偏见', category: 'bias' },
{ id: 'bias:race', name: '种族偏见', category: 'bias' },
{ id: 'bias:disability', name: '残障偏见', category: 'bias' },
{ id: 'security-exploit', name: '安全利用类检测', category: 'security-exploit' },
{ id: 'reasoning-dos', name: '推理资源耗尽', category: 'security-exploit' },
{ id: 'hallucination', name: '幻觉与事实性检测', category: 'hallucination' },
{ id: 'overreliance', name: '过度信赖', category: 'hallucination' },
{ id: 'divergent-repetition', name: '重复发散输出', category: 'hallucination' },
{ id: 'off-topic', name: '离题与边界检测', category: 'off-topic' },
{ id: 'wordplay', name: '文字游戏', category: 'off-topic' },
{ id: 'competitors', name: '竞品与品牌风险检测', category: 'brand' },
{ id: 'imitation', name: '名人模仿', category: 'brand' },
{ id: 'politics', name: '政治敏感话题', category: 'sensitive' },
{ id: 'religion', name: '宗教敏感话题', category: 'sensitive' },
{ id: 'teen-safety:age-restricted-goods-and-services', name: '未成年人保护', category: 'sensitive' },
{ id: 'contracts', name: '合同法律错误', category: 'industry' },
{ id: 'policy', name: '政策违规', category: 'industry' },
{ id: 'beavertails', name: 'BeaverTails 有害问答基准', category: 'dataset' },
{ id: 'harmbench', name: 'HarmBench 攻击技术基准', category: 'dataset' },
{ id: 'pliny', name: 'Pliny 越狱库基准', category: 'dataset' },
{ id: 'donotanswer', name: 'DoNotAnswer 拒答基准', category: 'dataset' },
{ id: 'cyberseceval', name: 'CyberSecEval 网络安全基准', category: 'dataset' },
{ id: 'xstest', name: 'XSTest 过度拒答基准', category: 'dataset' },
{ id: 'medical:hallucination', name: '医疗幻觉', category: 'industry' },
{ id: 'medical:incorrect-knowledge', name: '医疗错误知识', category: 'industry' },
{ id: 'medical:off-label-use', name: '超说明书用药', category: 'industry' },
{ id: 'medical:prioritization-error', name: '分诊优先级错误', category: 'industry' },
{ id: 'medical:sycophancy', name: '医疗迎合', category: 'industry' },
{ id: 'medical:anchoring-bias', name: '医疗锚定偏差', category: 'industry' },
{ id: 'medical:fda:ai-disclosure', name: 'FDA AI 披露', category: 'industry' },
{ id: 'medical:fda:cyber-access-control', name: 'FDA 访问控制', category: 'industry' },
{ id: 'medical:fda:cyber-audit-tampering', name: 'FDA 审计篡改', category: 'industry' },
{ id: 'pharmacy:controlled-substance-compliance', name: '管制药品合规', category: 'industry' },
{ id: 'pharmacy:dosage-calculation', name: '剂量计算', category: 'industry' },
{ id: 'pharmacy:drug-interaction', name: '药物相互作用', category: 'industry' },
{ id: 'financial:hallucination', name: '金融幻觉', category: 'industry' },
{ id: 'financial:calculation-error', name: '金融计算错误', category: 'industry' },
{ id: 'financial:compliance-violation', name: '金融合规违规', category: 'industry' },
{ id: 'financial:confidential-disclosure', name: 'MNPI 泄露', category: 'industry' },
{ id: 'financial:counterfactual', name: '虚构金融叙事', category: 'industry' },
{ id: 'financial:data-leakage', name: '金融数据泄露', category: 'industry' },
{ id: 'financial:defamation', name: '金融诽谤', category: 'industry' },
{ id: 'financial:impartiality', name: '无资质投资建议', category: 'industry' },
{ id: 'financial:japan-fiea-suitability', name: '日本 FIEA 适当性', category: 'industry' },
{ id: 'financial:misconduct', name: '金融犯罪协助', category: 'industry' },
{ id: 'financial:sox-compliance', name: 'SOX 合规', category: 'industry' },
{ id: 'financial:sycophancy', name: '金融迎合', category: 'industry' },
{ id: 'insurance:phi-disclosure', name: 'PHI 医疗信息泄露', category: 'industry' },
{ id: 'insurance:coverage-discrimination', name: '理赔歧视', category: 'industry' },
{ id: 'insurance:data-disclosure', name: '保单数据泄露', category: 'industry' },
{ id: 'insurance:network-misinformation', name: '网络信息错误', category: 'industry' },
{ id: 'telecom:account-takeover', name: '账户接管', category: 'industry' },
{ id: 'telecom:cpni-disclosure', name: 'CPNI 泄露', category: 'industry' },
{ id: 'telecom:location-disclosure', name: '位置数据泄露', category: 'industry' },
{ id: 'telecom:fraud-enablement', name: '电信诈骗协助', category: 'industry' },
{ id: 'telecom:billing-misinformation', name: '账单误导', category: 'industry' },
{ id: 'telecom:coverage-misinformation', name: '覆盖误导', category: 'industry' },
{ id: 'telecom:e911-misinformation', name: 'E911 错误信息', category: 'industry' },
{ id: 'telecom:porting-misinformation', name: '携号转网误导', category: 'industry' },
{ id: 'telecom:tcpa-violation', name: 'TCPA 违规', category: 'industry' },
{ id: 'telecom:accessibility-violation', name: '无障碍合规', category: 'industry' },
{ id: 'telecom:unauthorized-changes', name: '擅自变更', category: 'industry' },
{ id: 'telecom:law-enforcement-request-handling', name: '执法请求处理', category: 'industry' },
{ id: 'realestate:fair-housing-discrimination', name: '公平住房歧视', category: 'industry' },
{ id: 'realestate:lending-discrimination', name: '贷款歧视', category: 'industry' },
{ id: 'realestate:steering', name: '引导看房歧视', category: 'industry' },
{ id: 'realestate:advertising-discrimination', name: '广告歧视', category: 'industry' },
{ id: 'realestate:discriminatory-listings', name: '歧视性房源', category: 'industry' },
{ id: 'realestate:accessibility-discrimination', name: '无障碍歧视', category: 'industry' },
{ id: 'realestate:source-of-income', name: '收入来源歧视', category: 'industry' },
{ id: 'realestate:valuation-bias', name: '估值偏差', category: 'industry' },
{ id: 'ecommerce:pci-dss', name: '支付卡数据泄露', category: 'industry' },
{ id: 'ecommerce:order-fraud', name: '订单欺诈', category: 'industry' },
{ id: 'ecommerce:price-manipulation', name: '价格操纵', category: 'industry' },
{ id: 'ecommerce:compliance-bypass', name: '合规绕过', category: 'industry' },
] as const

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

export const ENGINE_STRATEGY_OPTIONS = [
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
{ id: 'bestOfN', name: 'Best-of-N 多次采样' },
{ id: 'retry', name: '重试攻击' },
{ id: 'likert', name: 'Likert 量表包装' },
{ id: 'citation', name: '引用包装' },
{ id: 'mathPrompt', name: '数学题包装' },
{ id: 'mischievousUser', name: '顽劣用户' },
{ id: 'authoritativeMarkupInjection', name: '权威标记注入' },
{ id: 'singleTurnComposite', name: '单轮组合攻击' },
] as const
