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

/**
 * U7 · 被测应用的结构化描述。
 *
 * 原生生成（promptfoo 自己生成攻击用例）需要知道「被测的是什么」，
 * 否则攻击载荷泛化而无针对性 —— 不知道系统能做什么、不能做什么。
 *
 * 六字段沿用 promptfoo `applicationDefinition` 的命名。注意：promptfoo 的
 * **库入口**（本平台调用的 `redteam.run`）并不读取该键，它只存在于 CLI 路径，
 * 且 `RedteamConfigSchema` 未声明它；引擎因此把这些字段渲染进 promptfoo 真正
 * 生效的 `purpose` 通道。字段名保持一致，便于上游将来原生支持时识别。
 */
export interface EngineApplicationDefinition {
  purpose?: string
  features?: string
  has_access_to?: string
  does_not_have_access_to?: string
  user_types?: string
  security_requirements?: string
}

export interface EngineRunCreateInput {
  purpose: string
  num_tests: number
  plugins: Array<{ id: string }>
  strategies?: Array<{ id: string }>
  judge_mode?: string
  /** U7：全部可选。留空则沿用目标配置里的 redteam_* 元数据。 */
  application_definition?: EngineApplicationDefinition
  /** U7：额外的生成约束（语言、领域、禁忌），作用于每个插件。 */
  test_generation_instructions?: string
  /** U7：promptfoo 注入攻击载荷的 prompt 变量名；留空用引擎默认值 `prompt`。 */
  inject_var?: string
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
{ id: 'harmful', name: '有害内容检测', category: 'harmful', fanout: 26 },
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
{ id: 'pii', name: '隐私信息泄露检测', category: 'privacy', fanout: 4 },
{ id: 'pii:direct', name: '直接索要隐私', category: 'privacy' },
{ id: 'pii:social', name: '社工套取隐私', category: 'privacy' },
{ id: 'pii:api-db', name: 'API/数据库隐私外泄', category: 'privacy' },
{ id: 'pii:session', name: '会话数据泄露', category: 'privacy' },
{ id: 'cross-session-leak', name: '跨会话数据泄漏', category: 'privacy' },
{ id: 'indirect-prompt-injection', name: '间接提示注入检测', category: 'injection', requires_remote_generation: true },
{ id: 'ascii-smuggling', name: 'ASCII 走私检测', category: 'injection', requires_remote_generation: true },
{ id: 'excessive-agency', name: '过度代理行为检测', category: 'injection' },
{ id: 'hijacking', name: '对话劫持', category: 'injection', requires_remote_generation: true },
{ id: 'system-prompt-override', name: '系统提示覆盖', category: 'injection', requires_remote_generation: true },
{ id: 'prompt-extraction', name: '系统提示词窃取', category: 'injection' },
{ id: 'mcp', name: 'MCP 协议攻击', category: 'injection', requires_remote_generation: true },
{ id: 'agentic:memory-poisoning', name: 'Agent 记忆投毒', category: 'injection', requires_remote_generation: true },
{ id: 'bias', name: '偏见与歧视检测', category: 'bias', fanout: 6 },
{ id: 'bias:age', name: '年龄偏见', category: 'bias', requires_remote_generation: true },
{ id: 'bias:gender', name: '性别偏见', category: 'bias', requires_remote_generation: true },
{ id: 'bias:race', name: '种族偏见', category: 'bias', requires_remote_generation: true },
{ id: 'bias:disability', name: '残障偏见', category: 'bias', requires_remote_generation: true },
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
{ id: 'policy', name: '政策违规', category: 'industry', requires_config: true },
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
{ id: 'iterative', name: '迭代越狱' },
{ id: 'iterative:tree', name: '树搜索越狱' },
{ id: 'iterative:meta', name: '元提示越狱' },
{ id: 'bestOfN', name: 'Best-of-N 多次采样', requires_remote_generation: true },
{ id: 'likert', name: 'Likert 量表包装', requires_remote_generation: true },
{ id: 'citation', name: '引用包装', requires_remote_generation: true },
{ id: 'mischievousUser', name: '顽劣用户' },
{ id: 'authoritativeMarkupInjection', name: '权威标记注入' },
{ id: 'singleTurnComposite', name: '单轮组合攻击', requires_remote_generation: true },
/* ---- Phase 3 开篇 · 任务 1 · 由 promptfoo 动态发现得到的原生策略 ----
 *
 * 这 7 项此前用户根本选不到：平台的策略清单是手写的 30 条，而 promptfoo
 * 0.123.0 有 35 条原生策略。手写清单没有漂移机制，漏掉的就是不可用。
 * 清单本体由 `scripts/gen_remote_dep.mjs` 同级脚本 gen_strategy_catalog.mjs
 * 从 promptfoo 运行时求得（不是 grep 字面量），前端这一份必须与后端
 * `promptfooStrategyCatalog` 逐条一致 —— 由 gen_frontend_options_test.go 断言。
 *
 * 同时移除了三条**看似可选、实则跑不通**的旧项（Phase 3 实测发现）：
 *   simba      —— promptfoo 已废弃，执行体 log 一句就返回空用例数组，
 *                 运行成功但覆盖为 0（U6 要消除的静默 0 覆盖）
 *   retry      —— 需要 config.targetIds，平台只下发 id 字段，
 *                 promptfoo 在评测进行中抛 "No target IDs found in config"
 *   mathPrompt —— 远程生成屏蔽后回落到直连 OpenAI 的分支，报 "API key is not set"
 * 这三条都不在 U6 的远程依赖表里，所以此前一直没被拦住。
 */
{ id: 'morse', name: '摩斯电码' },
{ id: 'piglatin', name: '猪拉丁文' },
{ id: 'camelcase', name: '驼峰拆分' },
{ id: 'emoji', name: 'Emoji 替换' },
{ id: 'jailbreak-templates', name: '越狱模板库' },
{ id: 'layer', name: '多层组合' },
{ id: 'indirect-web-pwn', name: '间接网页注入' },
{ id: 'custom', name: '自定义策略' },
/* ---- 已废弃但仍可用的上游别名 ----
 *
 * promptfoo 只 log 一句 deprecated 警告、并不拒绝，所以它们仍然能跑。
 * 保留但明确标注，避免与它们真正委派到的目标（越狱模板库 / 元提示越狱）
 * 看起来像两种不同手法。
 */
{ id: 'jailbreak', name: '元提示越狱（旧别名）' },
{ id: 'prompt-injection', name: '提示注入模板（旧别名）' },
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

/**
 * P2-1：`requires_config` 的提示文案。
 *
 * 与上一句刻意不同：那句说的是「本环境永远跑不了」，这句说的是「补一份配置就
 * 能跑」。把两者写成一句会让用户以为 `policy` 是个坏掉的选项，而不是一个平台
 * 还没做上传入口的能力。
 */
export const PLUGIN_REQUIRES_CONFIG_REASON =
  '需先提供策略文档 · 已支持该插件，但平台尚未收集 config.policy 文本，选中它本次会生成 0 条用例'

/** 引擎在请求含被标记项时返回的 error_code（与后端常量一致）。 */
export const REMOTE_GENERATION_ERROR_CODES = {
  plugin: 'plugin_requires_remote_generation',
  strategy: 'strategy_requires_remote_generation',
} as const

/**
 * P2-2：后端在插件 id 非法时返回的 error_code（与 `maclaw.EngineErrPluginIDInvalid`
 * 逐字一致）。
 *
 * 与上面两个码分开，因为处置不同：远程依赖是「本环境跑不了」，这个是「这个 id
 * 从来就不存在」，后者还附带 `guidance`（能力搬到哪里去了）。
 */
export const PLUGIN_ID_INVALID_CODE = 'plugin_id_invalid'

/**
 * 一条插件/策略选项。requires_remote_generation 由后端目录生成。
 *
 * P2-1 新增 `requires_config`：**合法但缺配置**（当前仅 `policy`，它
 * `safeParse` 通过、fanout=1，只是平台还没有 `config.policy` 文本）。
 * 它与 `requires_remote_generation` 是两种不同的失败：远程依赖是「本环境
 * 永远跑不了」，requires_config 是「补一份配置就能跑」。所以它**不置灰**
 * ——置灰会把一个即将支持的能力伪装成不可用。
 */
export interface EngineCatalogOption {
  id: string
  name: string
  requires_remote_generation?: boolean
  requires_config?: boolean
}

/**
 * 插件选项额外带风险类别（与后端 catalog 的 category 对齐）。
 *
 * `fanout` 是该 id 展开成的子插件数（`harmful`=26、`pii`=4、`bias`=6，其余为 1）。
 * promptfoo 把 `numTests` 应用到**每个展开后的子插件**，所以
 * 「实际探针数 = numTests × Σfanout」—— 只看插件个数会低估最坏路径
 * （表单默认值恰好是 `plugins: ['harmful']`）。由后端目录生成。
 */
export interface EnginePluginCatalogOption extends EngineCatalogOption {
  category: string
  fanout?: number
}

/** 灰态选项：保留可见 + 置灰 + 附屏蔽原因，供 Select 的 options 使用。 */
export interface EngineSelectOption {
  value: string
  label: string
  disabled: boolean
  /** 灰态原因；非灰态为 undefined。 */
  reason?: string
  /**
   * P2-1：`requires_config` 的说明；未标 config-gated 时为 undefined。
   *
   * 与 `reason` **不是同一件事**，所以不复用同一个字段：`reason` 伴随
   * `disabled: true`（当前环境永远跑不了），而这里伴随 `disabled: false`
   * ——选项仍可选，只是要提醒用户「选了它这次跑不出用例，因为还缺一份配置」。
   * 合成一个字段会逼 UI 在「置灰」和「提示」之间二选一。
   */
  configReason?: string
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
  // P2-1：`requires_config` 不置灰。置灰会把「补一份配置就能跑」伪装成
  // 「本环境不可用」，而那正是我们想避免的误导。
  if (option.requires_config) {
    return {
      value: option.id,
      label: option.name,
      disabled: false,
      configReason: PLUGIN_REQUIRES_CONFIG_REASON,
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
 * 判断某个插件 id 是否「合法但缺配置」（P2-1，当前仅 `policy`）。
 *
 * 与 `isPluginRemoteGenerationGated` 是**两种不同的失败**，不能合并成一个
 * `disabled` 开关：远程依赖是「本环境永远跑不了」，requires_config 是
 * 「补一份配置就能跑」。所以它不参与 `toEngineSelectOption` 的置灰分支——
 * 置灰会把一个即将支持的能力伪装成不可用。
 */
export function isPluginConfigGated(id: string): boolean {
  return ENGINE_PLUGIN_OPTIONS.some((o) => o.id === id && o.requires_config === true)
}

/**
 * 返回该 id 展开成的子插件数（默认 1）。
 *
 * promptfoo 把 `numTests` 应用到**每个展开后的子插件**，所以实际探针数是
 * `num_tests × Σfanout`：`harmful`=26、`pii`=4、`bias`=6，其余为 1。只按
 * 「选了几个插件」估算成本会把最坏路径低估 26 倍，而表单默认值恰好就是
 * `plugins: ['harmful']`。
 */
export function pluginFanout(id: string): number {
  return ENGINE_PLUGIN_OPTIONS.find((o) => o.id === id)?.fanout ?? 1
}

/**
 * 一组插件 id 展开后的子插件总数，即 `num_tests` 的真实乘数（P0-1 的权重源）。
 *
 * 空串与重复项按「一次选择」计：重复勾选同一 id 不应让用户以为成本翻倍，
 * 而空串是下拉多选的正常残留。
 */
export function totalPluginFanout(ids: readonly string[]): number {
  const seen = new Set<string>()
  let total = 0
  for (const id of ids) {
    const trimmed = id.trim()
    if (!trimmed || seen.has(trimmed)) continue
    seen.add(trimmed)
    total += pluginFanout(trimmed)
  }
  return total
}

/**
 * P2-1 已下线的插件 id 及其替代路径（与后端 retiredPluginCatalog 同源）。
 *
 * 为什么要留这张表：把必然失败的选项删掉只解决了「会报错」，没解决
 * 「用户要的能力去哪了」。策略目录早就把 `jailbreak` / `prompt-injection`
 * 识别成废弃别名并给出推荐替代，插件目录一直没做同一件事 —— 这正是这三个
 * id 能长期混在插件选择器里的根因。
 *
 * 与 `requires_remote_generation` 的处置相反：这里**不置灰**，因为替代项是
 * 真实存在且可用的（`iterative:meta` / `iterative:tree` 等），引导过去即可。
 */
export interface RetiredEnginePlugin {
  id: string
  name: string
  reason: string
  /** 替代能力所在的槽位：'strategy' | 'plugin'。 */
  replacement_slot: 'strategy' | 'plugin'
  replacement_ids: string[]
  replacement_label: string
}

export const RETIRED_ENGINE_PLUGINS: ReadonlyArray<RetiredEnginePlugin> = [
  {
    id: 'jailbreak',
    name: '越狱对抗检测',
    reason:
      'jailbreak 在 promptfoo 里是攻击策略而非插件，作为插件 id 会被引擎拒绝，该次运行必然失败。',
    replacement_slot: 'strategy',
    replacement_ids: ['iterative:meta', 'iterative:tree'],
    replacement_label:
      '「越狱对抗检测」是攻击维度、不是插件维度；越狱类攻击手法请在「攻击策略」中选择「元提示越狱」或「树搜索越狱」。',
  },
  {
    id: 'prompt-injection',
    name: '提示注入检测',
    reason:
      '平台自造的 id，promptfoo 没有同名插件；其语义由「间接提示注入检测」承接，但后者依赖远程生成、当前环境已屏蔽。',
    replacement_slot: 'strategy',
    replacement_ids: ['jailbreak-templates'],
    replacement_label:
      '「提示注入检测」已下线；提示注入攻击手法可在「攻击策略」中选择「越狱模板库」。间接提示注入面当前环境需远程生成、已屏蔽。',
  },
  {
    id: 'security-exploit',
    name: '安全利用类检测',
    reason:
      '平台自造的 id，promptfoo 把它拆成 sql-injection / shell-injection / ssrf 等多个具体插件，没有单一对应项。',
    replacement_slot: 'plugin',
    replacement_ids: ['reasoning-dos'],
    replacement_label:
      '「安全利用类检测」已下线；资源耗尽类攻击请选「推理资源耗尽」。SQL 注入 / 命令注入等具体利用面尚未入库。',
  },
]

/** 查某个已下线插件 id 的替代引导；未下线返回 undefined。 */
export function retiredEnginePluginGuidance(id: string): RetiredEnginePlugin | undefined {
  return RETIRED_ENGINE_PLUGINS.find((o) => o.id === id)
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

/**
 * P2-2：把后端 `plugin_id_invalid`（409）翻译成可读提示。
 *
 * 与上面那个函数分开，因为两件事的处置不同：远程依赖是「这个能力在本环境跑不
 * 了」，plugin_id_invalid 是「这个 id 从来就不存在」。前者要换选项，后者要知
 * 道能力搬到哪里去了——所以后端把 `guidance` 一起下发，这里原样带出来。
 */
export function explainPluginIDRejection(
  data: unknown,
): { message: string; code: string } | null {
  const body = data as { code?: string; error?: string; invalid_plugins?: string[]; guidance?: RetiredEnginePlugin[] } | null
  if (!body || body.code !== PLUGIN_ID_INVALID_CODE) return null
  const invalid = (body.invalid_plugins ?? []).filter((id) => typeof id === 'string' && id.trim() !== '')
  const guidance = (body.guidance ?? [])
    .map((g) => g?.replacement_label)
    .filter((label): label is string => typeof label === 'string' && label.trim() !== '')
  // 后端已经写好了完整句（含每个 id 的原因），优先原样展示，避免前端再拼一
  // 遍导致两处措辞漂移。后端没给 error 时才本地兜底，并把替代路径带上——
  // 只说「id 无效」而不说能力去哪了，用户只能自己猜。
  const fallback = `本次请求包含 promptfoo 不接受的插件 id${invalid.length > 0 ? `：${invalid.join('、')}` : ''}。`
    + (guidance.length > 0 ? ` ${guidance.join(' ')}` : '')
  return {
    message: (typeof body.error === 'string' && body.error.trim() !== '') ? body.error : fallback,
    code: body.code,
  }
}
