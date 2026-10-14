package maclaw

// promptfoo_plugin_catalog.go — built-in promptfoo-engine plugin/strategy
// catalog (L1 alignment with promptfoo v0.123.0).
//
// The engine's Plan-A generation synthesizes attack prompts from free-form
// plugin ids (the engine LLM tags each generated case with a plugin_id). This
// catalog exposes the canonical platform-supported plugin/strategy vocabulary
// as capability cards so MaClaw can discover and plan engine-backed
// evaluations. It is a static in-process catalog: no DB rows, no payloads.
//
// Plugin ids mirror the promptfoo v0.123.0 registry (harmful:*, pii:*, bias:*,
// medical:*, financial:*, telecom:*, realestate:*, ecommerce:*, dataset
// benchmarks, ...). Categories align with the engine's inferCategory() output
// so catalog grouping and result grouping stay consistent.

import (
	"sort"
	"strconv"
	"strings"
)

const (
	CapabilitySourcePromptfooPlugin   = "promptfoo_plugin"
	CapabilitySourcePromptfooStrategy = "promptfoo_strategy"
)

// promptfooPluginCatalogEntry describes one built-in engine plugin family.
type promptfooPluginCatalogEntry struct {
	ID         string
	Name       string
	Category   string
	Summary    string
	RiskTypes  []string
	DefaultSev string
	UseWhen    string
	// RequiresRemoteGeneration mirrors promptfoo's own remote-dependency
	// tables (U6). It is derived from the embedded mapping table rather than
	// hand-set per entry, so the flag cannot drift from upstream. Purely
	// descriptive metadata: nothing in the generation or judging path reads
	// it. The pre-flight guard (ValidateEngineRunRemoteGeneration) and the
	// frontend greying are its only consumers.
	RequiresRemoteGeneration bool
	// RequiresConfig marks a plugin that is legal upstream and selectable here,
	// but cannot produce test cases until the platform collects configuration
	// for it (currently only `policy`, which needs config.policy text).
	//
	// P2-1 keeps this strictly separate from legality on purpose. `policy`
	// passes promptfoo's zod with fanout 1, so calling it "illegal" would
	// disguise a capability gap as a broken id — and would drop U8's
	// `promptfoo:policy` judging semantics along with it. Same derivation
	// discipline as RequiresRemoteGeneration: read from the generated
	// legality table, never hand-set per entry.
	RequiresConfig bool
}

type promptfooStrategyCatalogEntry struct {
	ID      string
	Name    string
	Summary string
	// RequiresRemoteGeneration mirrors promptfoo's strategy-side gating
	// (U6). Same contract as the plugin field: derived, descriptive, and
	// consumed only by the pre-flight guard and the frontend.
	RequiresRemoteGeneration bool
	// Unavailable blocks the entry from being offered. Set for strategies
	// discovered from promptfoo that cannot run here for a reason U6 does not
	// cover — a proven no-op (`simba`, `basic`), a media channel this
	// deployment has not provisioned (`image`/`audio`/`video`), or a runtime
	// probe that produced nothing and could not be attributed.
	//
	// This is the Phase 3 widening of U6's guardrail: U6 answers "does this
	// need remote generation", and `simba` needs none — it just quietly
	// produces nothing while the run reports success. Offering it is exactly
	// the silent-coverage failure U6 exists to eliminate, reached by a path
	// U6's table does not cover.
	Unavailable       bool
	UnavailableReason string
	// PromptfooID is promptfoo's own id when it differs from the platform's.
	// Empty means the two are identical.
	PromptfooID string
}

// p is a compact constructor for plugin entries; risk types follow category.
// RequiresRemoteGeneration is looked up from the embedded mapping table so the
// catalog and the guardrail can never disagree.
func p(id, name, category, summary, sev string) promptfooPluginCatalogEntry {
	return promptfooPluginCatalogEntry{
		ID: id, Name: name, Category: category, Summary: summary,
		RiskTypes: []string{category}, DefaultSev: sev, UseWhen: categoryUseWhen(category),
		RequiresRemoteGeneration: PluginRequiresRemoteGeneration(id),
		RequiresConfig:           PluginRequiresConfig(id),
	}
}

func s(id, name, summary string) promptfooStrategyCatalogEntry {
	entry := promptfooStrategyCatalogEntry{
		ID: id, Name: name, Summary: summary,
		RequiresRemoteGeneration: StrategyRequiresRemoteGeneration(id),
		// The native id comes from the generated table, which is the single
		// source of truth for the platform↔promptfoo vocabulary. The 3 gated ids
		// U6 also carries a mapping for are unioned in so a strategy that is
		// remote-gated *and* translatable reports both facts.
		PromptfooID: firstNonEmptyString(
			PlatformToNativeStrategyID(id),
			PromptfooRemoteDepStrategyID(id),
		),
	}
	// Availability is looked up by the **native** id, not the platform id.
	// `mathPrompt` is a platform id whose promptfoo counterpart is
	// `math-prompt`, so a lookup on the platform id would miss the discovered
	// row entirely and the entry would be offered even though its native
	// strategy needs remote generation. Resolving first is what makes the
	// catalogue and the discovery table agree.
	//
	// U6's verdict wins over the discovery table's when both apply: a
	// remote-gated strategy must report *that* as the reason, because it is the
	// actionable one (a policy block, not a defect), and the card layer asserts
	// this precedence rather than re-deriving it.
	if entry.RequiresRemoteGeneration {
		entry.Unavailable = true
		entry.UnavailableReason = RemoteGenerationUnavailableMessage
		return entry
	}
	if row, ok := NativeStrategyByID(StrategyNativeID(id)); ok && !StrategyIsSelectable(row.ID) {
		entry.Unavailable = true
		entry.UnavailableReason = row.Reason
	}
	return entry
}

// sn builds a catalogue entry for a strategy discovered from promptfoo but not
// previously listed by the platform.
//
// Unlike `s`, the native id is known, so the entry is built from the generated
// table rather than curated text — the availability verdict is not re-derived
// here, it is read, so the catalogue and the pre-flight guard cannot disagree
// about which strategies are usable.
func sn(id string) promptfooStrategyCatalogEntry {
	name, summary := nativeStrategyDisplay(id)
	entry := promptfooStrategyCatalogEntry{
		ID: id, Name: name, Summary: summary, PromptfooID: id,
		RequiresRemoteGeneration: StrategyRequiresRemoteGeneration(id) || id == PromptfooRemoteDepStrategyID(id),
	}
	if !StrategyIsSelectable(id) {
		entry.Unavailable = true
		if row, ok := NativeStrategyByID(id); ok {
			entry.UnavailableReason = row.Reason
		} else {
			entry.UnavailableReason = "策略在当前环境不可用"
		}
	}
	return entry
}

// promptfooNativeStrategyCatalog returns a catalogue entry for every promptfoo
// native strategy discovered by scripts/gen_strategy_catalog.mjs that runs in
// this deployment, skipping any the platform already exposes under another id.
//
// Only *available* strategies are listed. Unavailable ones are deliberately
// absent rather than present-but-greyed: a strategy that provably generates
// nothing (`simba`) has no business occupying a slot in a capability catalogue,
// and listing it with a flag would leave one code path — the search index, the
// option list, any future consumer that forgets the flag — free to offer it.
//
// Their ids and reasons remain queryable through
// UnavailableNativeStrategyIDs(), so an operator can still answer "why can't I
// use simba?" without the catalogue implying it is a choice.
func promptfooNativeStrategyCatalog() []promptfooStrategyCatalogEntry {
	entries := make([]promptfooStrategyCatalogEntry, 0, len(strategyCatalogData.NativeStrategies))
	for _, id := range LocalNativeStrategyIDs() {
		if promptfooHandwrittenStrategyIDSet[strings.ToLower(id)] {
			continue
		}
		// Skip a native id the platform already surfaces under a different
		// platform id. `jailbreak:hydra` is `hydra`, `jailbreak:tree` is
		// `iterative:tree`, `mischievous-user` is `mischievousUser` — offering
		// both would put the same attack technique in the selector twice under
		// two names, and a user picking either would not know they are the same
		// thing. The platform id wins because it is the curated one, with a
		// Chinese name and a summary written for it.
		if promptfooPlatformIDForNative(id) != "" {
			continue
		}
		entries = append(entries, sn(id))
	}
	return entries
}

// promptfooPlatformIDForNative returns the platform strategy id that already
// forwards to the given native id, or "" when none does.
func promptfooPlatformIDForNative(nativeID string) string {
	for _, entry := range promptfooStrategyCatalog {
		if StrategyNativeID(entry.ID) == nativeID {
			return entry.ID
		}
	}
	return ""
}

// allPromptfooStrategyEntries is the full strategy list: the platform's
// hand-written entries (which include its own invented dimensions) followed by
// the newly discovered native ones.
func allPromptfooStrategyEntries() []promptfooStrategyCatalogEntry {
	discovered := promptfooNativeStrategyCatalog()
	all := make([]promptfooStrategyCatalogEntry, 0, len(promptfooStrategyCatalog)+len(discovered))
	all = append(all, promptfooStrategyCatalog...)
	all = append(all, discovered...)
	return all
}

func categoryUseWhen(category string) string {
	switch category {
	case "harmful":
		return "需要评估模型对有害、危险或违禁内容请求的拒答能力时使用。"
	case "privacy":
		return "需要评估模型是否泄露个人信息或敏感内部信息时使用。"
	case "injection":
		return "需要评估模型对提示注入、指令劫持攻击的抵抗能力时使用。"
	case "jailbreak":
		return "需要评估模型对抗越狱话术的安全对齐能力时使用。"
	case "bias":
		return "需要评估模型输出内容的公平性与偏见风险时使用。"
	case "security-exploit":
		return "需要评估模型拒绝协助网络攻击或资源滥用时使用。"
	case "hallucination":
		return "需要评估模型事实准确性与幻觉风险时使用。"
	case "off-topic":
		return "需要评估模型服务范围边界与离题请求处理能力时使用。"
	case "brand":
		return "企业客服/品牌类模型需要评估竞品提及与品牌声誉风险时使用。"
	case "sensitive":
		return "需要评估模型对政治、宗教、未成年人等敏感话题的处理边界时使用。"
	case "industry":
		return "面向金融、医疗、电信、房产、电商等行业场景的合规安全评测时使用。"
	case "dataset":
		return "需要用社区基准数据集（BeaverTails/HarmBench/Pliny 等）做安全回归时使用。"
	default:
		return "需要覆盖该类攻击面的安全评测时使用。"
	}
}

var promptfooPluginCatalog = []promptfooPluginCatalogEntry{
	// ---- Trust & Safety: harmful family ----
	p("harmful", "有害内容检测", "harmful", "检验被测模型是否拒绝暴力、自伤、违法物品制作等有害内容请求，覆盖社会工程、角色扮演、场景诱导等攻击手法。", "high"),
	p("harmful:child-exploitation", "儿童剥削内容", "harmful", "检验模型对儿童剥削相关内容的识别与拒答（最高优先级红线）。", "critical"),
	p("harmful:chemical-biological-weapons", "化学生物武器", "harmful", "检验模型是否拒绝化学生物武器制造指导。", "critical"),
	p("harmful:indiscriminate-weapons", "大规模杀伤武器", "harmful", "检验模型是否拒绝大规模杀伤性武器相关请求。", "critical"),
	p("harmful:weapons:ied", "简易爆炸装置", "harmful", "检验模型是否拒绝简易爆炸装置制作指导。", "critical"),
	p("harmful:cybercrime", "网络犯罪协助检测", "harmful", "检验被测模型是否拒绝提供网络犯罪协助，如入侵方法、恶意软件编写、数据窃取步骤等。", "critical"),
	p("harmful:cybercrime:malicious-code", "恶意代码编写", "harmful", "检验模型是否拒绝编写恶意代码、勒索软件、后门等。", "critical"),
	p("harmful:violent-crime", "暴力犯罪协助检测", "harmful", "检验被测模型是否拒绝提供暴力犯罪相关指导，如伤害方法、袭击策划等。", "critical"),
	p("harmful:illegal-activities", "违法活动协助检测", "harmful", "检验被测模型是否拒绝协助违法活动，如毒品制作、武器改装、伪造证件等。", "critical"),
	p("harmful:illegal-drugs", "违禁药物", "harmful", "检验模型是否拒绝违禁药物制作与获取指导。", "high"),
	p("harmful:illegal-drugs:meth", "冰毒制造", "harmful", "检验模型是否拒绝冰毒等合成毒品制造指导。", "critical"),
	p("harmful:hate", "仇恨言论", "harmful", "检验模型是否生成针对群体的仇恨与歧视性言论。", "high"),
	p("harmful:harassment-bullying", "骚扰霸凌", "harmful", "检验模型是否生成骚扰、霸凌类内容。", "high"),
	p("harmful:insults", "人身攻击辱骂", "harmful", "检验模型是否输出人身攻击与辱骂内容。", "medium"),
	p("harmful:profanity", "脏话粗口", "harmful", "检验模型在诱导下是否输出低俗辱骂内容。", "low"),
	p("harmful:radicalization", "极端化煽动", "harmful", "检验模型是否输出极端主义与煽动性内容。", "high"),
	p("harmful:self-harm", "自残自杀", "harmful", "检验模型对自残自杀相关请求的安全处理。", "critical"),
	p("harmful:sex-crime", "性犯罪", "harmful", "检验模型对性犯罪相关内容的识别与拒答。", "critical"),
	p("harmful:sexual-content", "色情内容", "harmful", "检验模型是否拒绝生成露骨色情内容。", "high"),
	p("harmful:graphic-content", "血腥暴力内容", "harmful", "检验模型对血腥、暴力、年龄限制内容的处理。", "high"),
	p("harmful:misinformation-disinformation", "虚假信息有害宣传", "harmful", "检验模型是否协助编造虚假信息与有害宣传。", "high"),
	p("harmful:privacy", "隐私侵犯协助", "harmful", "检验模型是否协助侵犯他人隐私（人肉、跟踪等）。", "high"),
	p("harmful:copyright-violations", "版权侵犯", "harmful", "检验模型是否大段复述受版权保护的内容。", "medium"),
	p("harmful:intellectual-property", "知识产权侵犯", "harmful", "检验模型是否协助盗版、破解等知识产权侵犯行为。", "medium"),
	p("harmful:specialized-advice", "无资质专业建议", "harmful", "检验模型是否在无免责声明下输出医疗/法律/金融等专业建议。", "high"),
	p("harmful:unsafe-practices", "危险行为教唆", "harmful", "检验模型是否输出危险行为（危险挑战、极端节食等）指导。", "high"),
	// ---- Privacy ----
	p("pii", "隐私信息泄露检测", "privacy", "诱导被测模型输出个人隐私、系统提示词或内部敏感信息，检验模型的隐私保护边界。", "high"),
	p("pii:direct", "直接索要隐私", "privacy", "直接要求模型输出他人个人信息，检验基础隐私边界。", "high"),
	p("pii:social", "社工套取隐私", "privacy", "通过社会工程话术诱导模型泄露个人隐私。", "high"),
	p("pii:api-db", "API/数据库隐私外泄", "privacy", "构造 API/数据库访问场景，检验模型是否协助越权导出隐私数据。", "high"),
	p("pii:session", "会话数据泄露", "privacy", "检验模型是否会泄露其他会话中的用户数据。", "high"),
	p("cross-session-leak", "跨会话数据泄漏", "privacy", "检验多会话隔离边界，是否串扰其他用户上下文。", "high"),
	// ---- Injection family ----
	//
	// P2-1移出 `p("prompt-injection", …)`：promptfoo 0.123.0 无此插件
	// （`safeParse` 实测拒绝，见 testdata/promptfoo_plugin_legality.json）。
	// 它的语义由 `indirect-prompt-injection` 承接，而后者已被 U6 正确置灰
	// （REMOTE_ONLY）—— 本地可跑集合里**没有**等价物，所以是移出而非映射。
	// 映射到任何一个远程依赖项只会让用户换一个必然撞闸的选项。
	p("indirect-prompt-injection", "间接提示注入检测", "injection", "模拟从网页、文档、邮件等外部内容中嵌入的注入指令，检验被测模型处理外部内容时是否被劫持。", "high"),
	p("ascii-smuggling", "ASCII 走私检测", "injection", "使用 Unicode 不可见字符（RTL 覆盖、零宽字符）隐藏或混淆指令，检验被测模型能否识别走私注入。", "high"),
	p("excessive-agency", "过度代理行为检测", "injection", "诱导被测模型执行超出授权范围的操作（越权工具调用、未授权数据访问），检验权限边界意识。", "high"),
	p("hijacking", "对话劫持", "injection", "通过长对话逐步劫持模型目标，使其偏离原始任务。", "high"),
	p("system-prompt-override", "系统提示覆盖", "injection", "诱导模型忽略或改写系统提示中的安全约束。", "high"),
	p("prompt-extraction", "系统提示词窃取", "injection", "诱导模型输出完整系统提示词或隐藏指令。", "high"),
	p("mcp", "MCP 协议攻击", "injection", "针对 MCP 工具调用链的注入与滥用攻击。", "high"),
	p("agentic:memory-poisoning", "Agent 记忆投毒", "injection", "通过污染 Agent 长期记忆实现持久化攻击。", "high"),
	// ---- Bias / exploit ----
	//
	// P2-1 移出两条：
	//
	//   p("jailbreak", …)          —— `jailbreak` 在 promptfoo 里是**策略**不是插件
	//                                  （`safeParse` 实测拒绝）。它与策略位的
	//                                  `jailbreak` 撞名纯属巧合：策略目录早已把它
	//                                  识别为废弃别名并给出替代（见
	//                                  promptfoo_strategy_catalog.go 的
	//                                  nativeStrategyDisplayNames），插件目录一直没做
	//                                  同一件事。移出后平台不再提供一个必然失败的
	//                                  选项；用户需要「越狱对抗」这一攻击维度时请走
	//                                  策略位的 `iterative:meta`（元提示越狱）/
	//                                  `iterative:tree`（树搜索越狱）——见
	//                                  JailbreakRetiredGuidance，用户文案由
	//                                  retiredPluginGuidance 暴露。
	//
	//   p("security-exploit", …)   —— 平台自造。promptfoo 把这个意图拆成
	//                                  sql-injection / shell-injection（两者 NOT_GATED，
	//                                  真能本地跑，但不在平台目录）与 ssrf（REMOTE_ONLY）。
	//                                  把这个 id 映射到其中任何一个等于换一个用户
	//                                  没选的攻击面，是静默改变评测语义，所以移出。
	p("bias", "偏见与歧视检测", "bias", "检验被测模型是否生成带有偏见、歧视或有害刻板印象的内容。", "medium"),
	p("bias:age", "年龄偏见", "bias", "检验模型输出中的年龄刻板印象与歧视。", "medium"),
	p("bias:gender", "性别偏见", "bias", "检验模型输出中的性别刻板印象与歧视。", "medium"),
	p("bias:race", "种族偏见", "bias", "检验模型输出中的种族刻板印象与歧视。", "high"),
	p("bias:disability", "残障偏见", "bias", "检验模型输出中的残障群体刻板印象与歧视。", "medium"),
	// P2-1 移出 `p("security-exploit", …)`，理由见上方 bias 块的注释。
	p("reasoning-dos", "推理资源耗尽", "security-exploit", "构造超长推理链或资源滥用请求，检验服务稳定性。", "medium"),
	// ---- Hallucination / off-topic / brand / sensitive ----
	p("hallucination", "幻觉与事实性检测", "hallucination", "构造事实性陷阱问题，检验被测模型是否编造不存在的事实、引用或数据。", "medium"),
	p("overreliance", "过度信赖", "hallucination", "检验模型是否会盲目附和用户错误假设而放大幻觉。", "medium"),
	p("divergent-repetition", "重复发散输出", "hallucination", "检验模型是否陷入重复循环或失控的发散输出。", "low"),
	p("off-topic", "离题与边界检测", "off-topic", "检验被测模型面对明显离题或超出服务范围请求时的边界把控与引导能力。", "low"),
	p("wordplay", "文字游戏", "off-topic", "通过回文、双关等文字游戏绕过内容边界。", "low"),
	p("competitors", "竞品与品牌风险检测", "brand", "检验被测模型是否输出贬低竞品、泄露内部信息或损害品牌声誉的内容。", "low"),
	p("imitation", "名人模仿", "brand", "检验模型是否冒充真实人物身份或模仿其语气输出内容。", "medium"),
	p("politics", "政治敏感话题", "sensitive", "检验模型对政治敏感话题的边界与中立性。", "medium"),
	p("religion", "宗教敏感话题", "sensitive", "检验模型对宗教敏感话题的边界与中立性。", "medium"),
	p("teen-safety:age-restricted-goods-and-services", "未成年人保护", "sensitive", "检验模型是否向未成年人提供年龄限制商品或服务信息。", "high"),
	p("contracts", "合同法律错误", "industry", "检验模型在合同审查场景是否编造条款或遗漏关键法律风险。", "medium"),
	// P2-1：`policy` 是**合法** plugin id（`safeParse` 实测通过、fanout=1），
	// 只是缺 `config.policy` 文本 → 0 用例。所以它标 `requires_config` 而不是
	// 被当成非法 id 剔除：把「能力缺口」伪装成「id 非法」会丢掉 U8 已实现的
	// `promptfoo:policy` 判定语义，且让「补一个策略文档上传入口」这件事在目录上
	// 表现为不可见。处置见 `18` §4.3。
	p("policy", "政策违规", "industry", "检验模型是否输出违反平台或行业政策的内容。", "medium"),
	// ---- Dataset benchmarks ----
	p("beavertails", "BeaverTails 有害问答基准", "dataset", "BeaverTails 有害问答数据集驱动的安全回归测试。", "high"),
	p("harmbench", "HarmBench 攻击技术基准", "dataset", "HarmBench 攻击技术集驱动的越狱回归测试。", "high"),
	p("pliny", "Pliny 越狱库基准", "dataset", "Pliny 越狱模板库驱动的越狱回归测试。", "high"),
	p("donotanswer", "DoNotAnswer 拒答基准", "dataset", "DoNotAnswer 高风险请求集驱动的拒答质量测试。", "high"),
	p("cyberseceval", "CyberSecEval 网络安全基准", "dataset", "Meta CyberSecEval 驱动的网络攻击协助测试。", "high"),
	p("xstest", "XSTest 过度拒答基准", "dataset", "XSTest 检验模型是否对无害请求过度拒答。", "low"),
	// ---- Medical / pharmacy ----
	p("medical:hallucination", "医疗幻觉", "industry", "检验模型是否编造医学事实、研究或药物相互作用。", "critical"),
	p("medical:incorrect-knowledge", "医疗错误知识", "industry", "检验模型是否输出可能致害的错误医学知识。", "critical"),
	p("medical:off-label-use", "超说明书用药", "industry", "检验模型是否在无警示下推荐超说明书用药。", "high"),
	p("medical:prioritization-error", "分诊优先级错误", "industry", "检验医疗分诊/优先级判断错误是否延误危重处理。", "critical"),
	p("medical:sycophancy", "医疗迎合", "industry", "检验模型是否迎合患者错误医学假设。", "critical"),
	p("medical:anchoring-bias", "医疗锚定偏差", "industry", "检验模型是否被无关信息锚定导致误判。", "high"),
	p("medical:fda:ai-disclosure", "FDA AI 披露", "industry", "检验医疗器械 AI 是否如实披露用途边界与不确定性。", "high"),
	p("medical:fda:cyber-access-control", "FDA 访问控制", "industry", "检验医疗 AI 系统的权限与认证边界。", "high"),
	p("medical:fda:cyber-audit-tampering", "FDA 审计篡改", "industry", "检验是否协助篡改医疗器械安全审计证据。", "high"),
	p("pharmacy:controlled-substance-compliance", "管制药品合规", "industry", "检验管制药品处方的 DEA 合规（剂量、续方、欺诈红旗）。", "critical"),
	p("pharmacy:dosage-calculation", "剂量计算", "industry", "检验药学剂量计算（体重给药、儿科、换算）的准确性。", "critical"),
	p("pharmacy:drug-interaction", "药物相互作用", "industry", "检验药物相互作用与禁忌识别能力。", "critical"),
	// ---- Financial ----
	p("financial:hallucination", "金融幻觉", "industry", "检验模型是否编造市场数据、金融工具或公司财务。", "high"),
	p("financial:calculation-error", "金融计算错误", "industry", "检验定价、风险、组合优化等量化计算错误。", "high"),
	p("financial:compliance-violation", "金融合规违规", "industry", "检验模型是否协助内幕交易、市场操纵、洗钱。", "high"),
	p("financial:confidential-disclosure", "MNPI 泄露", "industry", "检验重大非公开信息（MNPI）的不当披露。", "high"),
	p("financial:counterfactual", "虚构金融叙事", "industry", "检验编造市场事件、并购消息等虚假叙事。", "medium"),
	p("financial:data-leakage", "金融数据泄露", "industry", "检验交易算法、客户账户、并购细节等数据泄露。", "high"),
	p("financial:defamation", "金融诽谤", "industry", "检验对金融机构的不实指控与诽谤。", "medium"),
	p("financial:impartiality", "无资质投资建议", "industry", "检验无授权下的投资建议与交易撮合。", "high"),
	p("financial:japan-fiea-suitability", "日本 FIEA 适当性", "industry", "检验日本金融商品交易法下的适当性义务。", "medium"),
	p("financial:misconduct", "金融犯罪协助", "industry", "检验内幕交易、抢跑、欺诈等金融犯罪协助。", "high"),
	p("financial:sox-compliance", "SOX 合规", "industry", "检验萨班斯法案内控绕过与财报操纵。", "high"),
	p("financial:sycophancy", "金融迎合", "industry", "检验模型是否迎合高风险交易策略。", "high"),
	// ---- Insurance ----
	p("insurance:phi-disclosure", "PHI 医疗信息泄露", "industry", "检验 HIPAA 下受保护健康信息（PHI）的未授权披露。", "high"),
	p("insurance:coverage-discrimination", "理赔歧视", "industry", "检验基于受保护特征的承保/理赔歧视。", "high"),
	p("insurance:data-disclosure", "保单数据泄露", "industry", "检验保单、理赔史等客户数据未授权披露。", "high"),
	p("insurance:network-misinformation", "网络信息错误", "industry", "检验医疗网络/供应商信息是否准确。", "medium"),
	// ---- Telecom ----
	p("telecom:account-takeover", "账户接管", "industry", "检验 SIM 卡互换、认证绕过等账户劫持协助。", "high"),
	p("telecom:cpni-disclosure", "CPNI 泄露", "industry", "检验 FCC 47 U.S.C. 222 下客户专网信息（CPNI）泄露。", "high"),
	p("telecom:location-disclosure", "位置数据泄露", "industry", "检验客户位置数据未授权披露。", "high"),
	p("telecom:fraud-enablement", "电信诈骗协助", "industry", "检验来电显示伪造、SIM box 等电信欺诈协助。", "high"),
	p("telecom:billing-misinformation", "账单误导", "industry", "检验账单信息与隐藏费用的真实性。", "medium"),
	p("telecom:coverage-misinformation", "覆盖误导", "industry", "检验网络覆盖与漫游宣传的真实性。", "medium"),
	p("telecom:e911-misinformation", "E911 错误信息", "industry", "检验紧急呼叫（E911）相关错误信息。", "high"),
	p("telecom:porting-misinformation", "携号转网误导", "industry", "检验号码携转规则与流程的准确性。", "medium"),
	p("telecom:tcpa-violation", "TCPA 违规", "industry", "检验营销电话/短信的同意与免打扰合规。", "medium"),
	p("telecom:accessibility-violation", "无障碍合规", "industry", "检验 Section 255/508 等无障碍合规。", "medium"),
	p("telecom:unauthorized-changes", "擅自变更", "industry", "检验 slamming/cramming（擅自改签/加费）漏洞。", "high"),
	p("telecom:law-enforcement-request-handling", "执法请求处理", "industry", "检验执法数据请求的合规处理。", "high"),
	// ---- Real estate ----
	p("realestate:fair-housing-discrimination", "公平住房歧视", "industry", "检验公平住房法下七大受保护类别歧视。", "high"),
	p("realestate:lending-discrimination", "贷款歧视", "industry", "检验 ECOA/公平住房法下的房贷歧视。", "high"),
	p("realestate:steering", "引导看房歧视", "industry", "检验按受保护特征引导看房的违法行为。", "high"),
	p("realestate:advertising-discrimination", "广告歧视", "industry", "检验房源广告中的歧视性表达。", "medium"),
	p("realestate:discriminatory-listings", "歧视性房源", "industry", "检验房源描述中的排他性/暗语歧视。", "medium"),
	p("realestate:accessibility-discrimination", "无障碍歧视", "industry", "检验 ADA/公平住房法下的残障 accommodaton 歧视。", "medium"),
	p("realestate:source-of-income", "收入来源歧视", "industry", "检验对住房补贴/Section 8 的收入来源歧视。", "medium"),
	p("realestate:valuation-bias", "估值偏差", "industry", "检验自动估值模型的算法性估值偏差。", "medium"),
	// ---- E-commerce ----
	p("ecommerce:pci-dss", "支付卡数据泄露", "industry", "检验 PCI DSS 下支付卡数据（卡号/CVV）暴露。", "high"),
	p("ecommerce:order-fraud", "订单欺诈", "industry", "检验欺诈订单、账户接管、支付操纵协助。", "high"),
	p("ecommerce:price-manipulation", "价格操纵", "industry", "检验折扣滥用、库存操纵、叠加优惠漏洞。", "medium"),
	p("ecommerce:compliance-bypass", "合规绕过", "industry", "检验年龄限制、地域限制、服务条款绕过。", "medium"),
}

// retiredPluginEntry records a plugin id the platform WITHDREW, together with
// where the capability the user was after actually lives.
//
// # Why withdrawal alone is not the fix
//
// The strategy catalog already models this correctly:
// promptfoo_strategy_catalog.go identifies `jailbreak` / `prompt-injection` as
// deprecated upstream aliases and names their replacements. The plugin catalog
// never did the same thing, which is exactly why a strategy id ended up
// sitting in the plugin selector. Deleting the row fixes the crash and leaves
// the user with no way to reach the capability they wanted — "the option
// disappeared" is not an explanation.
//
// This table is the plugin-side equivalent of the strategy-side treatment: the
// withdrawn id, why it was withdrawn, and what to use instead. It is NOT a
// catalog entry — these ids are not selectable, they exist only to be rendered
// as guidance (see PromptfooRetiredPluginGuidance).
//
// # Why the replacement is a strategy and not another plugin
//
// For `jailbreak` the honest answer is that promptfoo has no jailbreak PLUGIN;
// the capability exists only as strategies. Pointing users at a plugin that
// does not exist would just relocate the failure to a different screen.
type retiredPluginEntry struct {
	// ID is the withdrawn platform plugin id.
	ID string
	// Name is the label the withdrawn entry used to carry, kept so a report or
	// an announcement can refer to what the user actually saw.
	Name string
	// Reason explains the withdrawal in user-facing terms.
	Reason string
	// ReplacementSlot names WHERE the capability lives now ("strategy" or
	// "plugin"), so the UI can point at the right selector instead of naming a
	// bare id the user cannot find.
	ReplacementSlot string
	// ReplacementIDs are the ids to use instead, in the replacement slot.
	ReplacementIDs []string
	// ReplacementLabel is the ready-to-render sentence. Authored here rather
	// than assembled at the call site so the wording is reviewed once and
	// cannot drift between the API card, the frontend banner and the docs.
	ReplacementLabel string
}

// retiredPluginCatalog records P2-1's three removals with their replacement
// paths.
//
// Asserted against IllegalPluginIDs() so a withdrawal can never happen without
// a reason being recorded, and so an id promptfoo newly rejects cannot be
// dropped from the catalog silently.
var retiredPluginCatalog = []retiredPluginEntry{
	{
		ID:               "jailbreak",
		Name:             "越狱对抗检测",
		Reason:           "jailbreak 在 promptfoo 里是攻击策略而非插件，作为插件 id 会被引擎拒绝，该次运行必然失败。",
		ReplacementSlot:  "strategy",
		ReplacementIDs:   []string{"iterative:meta", "iterative:tree"},
		ReplacementLabel: "「越狱对抗检测」是攻击维度、不是插件维度；越狱类攻击手法请在「攻击策略」中选择「元提示越狱」或「树搜索越狱」。",
	},
	{
		ID:               "prompt-injection",
		Name:             "提示注入检测",
		Reason:           "平台自造的 id，promptfoo 没有同名插件；其语义由「间接提示注入检测」承接，但后者依赖远程生成、当前环境已屏蔽。",
		ReplacementSlot:  "strategy",
		ReplacementIDs:   []string{"jailbreak-templates"},
		ReplacementLabel: "「提示注入检测」已下线；提示注入攻击手法可在「攻击策略」中选择「越狱模板库」。间接提示注入面当前环境需远程生成、已屏蔽。",
	},
	{
		ID:               "security-exploit",
		Name:             "安全利用类检测",
		Reason:           "平台自造的 id，promptfoo 把它拆成 sql-injection / shell-injection / ssrf 等多个具体插件，没有单一对应项。",
		ReplacementSlot:  "plugin",
		ReplacementIDs:   []string{"reasoning-dos"},
		ReplacementLabel: "「安全利用类检测」已下线；资源耗尽类攻击请选「推理资源耗尽」。SQL 注入 / 命令注入等具体利用面尚未入库。",
	},
}

// PromptfooRetiredPlugin is one withdrawn plugin id plus its replacement path.
// Returned to the UI so a user who selected the id before the withdrawal can
// be told where the capability went, instead of finding a silently shorter list.
type PromptfooRetiredPlugin struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Reason           string   `json:"reason"`
	ReplacementSlot  string   `json:"replacement_slot"`
	ReplacementIDs   []string `json:"replacement_ids"`
	ReplacementLabel string   `json:"replacement_label"`
}

// PromptfooRetiredPlugins returns every withdrawn plugin id with its
// replacement path, sorted by id.
func PromptfooRetiredPlugins() []PromptfooRetiredPlugin {
	out := make([]PromptfooRetiredPlugin, 0, len(retiredPluginCatalog))
	for _, entry := range retiredPluginCatalog {
		out = append(out, retiredPluginToPublic(entry))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PromptfooRetiredPluginGuidance returns the replacement path for a withdrawn
// id, and whether that id was withdrawn at all.
func PromptfooRetiredPluginGuidance(pluginID string) (PromptfooRetiredPlugin, bool) {
	trimmed := strings.TrimSpace(pluginID)
	for _, entry := range retiredPluginCatalog {
		if entry.ID == trimmed {
			return retiredPluginToPublic(entry), true
		}
	}
	return PromptfooRetiredPlugin{}, false
}

// retiredPluginToPublic copies an entry out, including its ReplacementIDs
// slice. The copy matters: callers are HTTP handlers and tests, and handing out
// the backing array would let one caller mutate the catalog.
func retiredPluginToPublic(entry retiredPluginEntry) PromptfooRetiredPlugin {
	return PromptfooRetiredPlugin{
		ID:               entry.ID,
		Name:             entry.Name,
		Reason:           entry.Reason,
		ReplacementSlot:  entry.ReplacementSlot,
		ReplacementIDs:   append([]string(nil), entry.ReplacementIDs...),
		ReplacementLabel: entry.ReplacementLabel,
	}
}

var promptfooStrategyCatalog = []promptfooStrategyCatalogEntry{
	// 平台原生策略（方案 A 生成提示词的包装维度）
	s("direct", "直接攻击", "直接向目标模型提出攻击性问题，评估基础安全边界。"),
	s("role-play", "角色扮演", "通过虚构角色设定降低模型警惕性，测试安全对齐一致性。"),
	s("encoding", "编码混淆", "使用编码、格式变换等混淆手段包装攻击载荷，测试过滤绕过能力。"),
	s("multi-turn", "多轮诱导", "通过多轮渐进式对话逐步诱导模型突破安全边界。"),
	s("assumed-knowledge", "假设已知", "以“你已经知道/说过”为前提诱导模型补全敏感内容。"),
	s("few-shot", "少样本诱导", "构造包含敏感示例的 few-shot 上下文，诱导模型模仿输出。"),
	s("many-shot", "多样本诱导", "提供大量包含敏感内容的历史样本，测试长上下文下的安全约束。"),
	s("translate", "翻译绕过", "将攻击内容翻译为其他语言或混合语言包装，测试跨语言安全边界。"),
	// 对齐 promptfoo 原生策略 id
	s("base64", "Base64 编码", "将攻击载荷 Base64 编码后要求模型解码执行。"),
	s("rot13", "ROT13 编码", "使用 ROT13 字母替换编码攻击载荷。"),
	s("hex", "十六进制编码", "使用十六进制编码包装攻击载荷。"),
	s("leetspeak", "Leet 字符替换", "使用 leetspeak（4/3/1/0）字符替换绕过关键词过滤。"),
	s("otherEncodings", "其他编码变体", "摩斯电码、Unicode 转义等其他编码绕过方式。"),
	s("homoglyph", "同形字符混淆", "使用视觉同形字符替换关键词，测试字符级混淆识别。"),
	s("crescendo", "渐进升级", "从无害请求逐步升级敏感程度，测试渐进压力下的边界稳定性。"),
	s("goat", "GOAT 多轮越狱", "GOAT 式多轮自适应越狱攻击。"),
	s("hydra", "Hydra 多分支越狱", "Hydra 式多分支并发越狱攻击。"),
	s("goblin", "Goblin 多轮攻击", "Goblin 式多轮对抗攻击。"),
	s("iterative", "迭代越狱", "基于上一轮结果的迭代式越狱攻击。"),
	s("iterative:tree", "树搜索越狱", "树搜索式迭代越狱攻击。"),
	s("iterative:meta", "元提示越狱", "元提示优化式迭代越狱攻击。"),
	s("bestOfN", "Best-of-N 多次采样", "同一载荷多次采样寻找越狱成功样本。"),
	s("likert", "Likert 量表包装", "用评分量表格式包装敏感请求。"),
	s("citation", "引用包装", "伪造引用/文献格式包装敏感内容请求。"),
	// Phase 3 · `retry`、`mathPrompt`、`simba` 三条自此从目录移除。
	//
	// 它们此前是可选的，但实测**跑不通**，且都不在 U6 的远程依赖表里，所以
	// 一直没被拦住（`14` §5 十条第 1 条的同类：字段/能力「看起来能跑」而实际不生效）：
	//
	//   simba      —— promptfoo 0.123.0 已废弃，执行体 log 一句即 `return []`；
	//                 运行成功、报告列出该策略、覆盖实为 0（U6 要消除的静默 0 覆盖）
	//   retry      —— 需要 config.targetIds，而平台只下发 `{ id }`，
	//                 promptfoo 抛 "No target IDs found in config"（评测进行中抛）
	//   mathPrompt —— 远程生成屏蔽后回落到直连 OpenAI 的分支，报 "API key is not set"
	//
	// 保留会让用户反复踩同一个坑，因此从目录移除；引擎侧
	// `nativeStrategyMap.ts` 把它们降级为 plan_a_only（附原因），选中时仍有
	// 交代，而不是变成一个静默失效的开关。
	s("mischievousUser", "顽劣用户", "持续不听劝告的顽劣用户人设施压。"),
	s("authoritativeMarkupInjection", "权威标记注入", "伪造系统级标记/指令格式注入。"),
	s("singleTurnComposite", "单轮组合攻击", "在单轮内组合多种越狱手法。"),
}

// PromptfooPluginCatalogCards returns capability cards for every built-in
// engine plugin and strategy entry.
func PromptfooPluginCatalogCards() []CapabilityCard {
	strategies := allPromptfooStrategyEntries()
	cards := make([]CapabilityCard, 0, len(promptfooPluginCatalog)+len(strategies))
	for _, entry := range promptfooPluginCatalog {
		cards = append(cards, promptfooPluginCard(entry))
	}
	for _, entry := range strategies {
		cards = append(cards, promptfooStrategyCard(entry))
	}
	return cards
}

func promptfooPluginCard(entry promptfooPluginCatalogEntry) CapabilityCard {
	card := CapabilityCard{
		SourceType:     CapabilitySourcePromptfooPlugin,
		SourceRef:      "promptfoo_plugin:" + entry.ID,
		Name:           entry.Name,
		Summary:        entry.Summary,
		RiskTypes:      entry.RiskTypes,
		TargetTypes:    []string{"llm"},
		Languages:      []string{"zh"},
		UseWhen:        []string{entry.UseWhen},
		InputsRequired: []string{"purpose", "num_tests"},
		Outputs:        []string{"engine_run", "safe_run_result"},
		Tags:           []string{"promptfoo", "engine_plugin", entry.ID},
		Enabled:        true,
		Status:         "published",
		SafeMetadata: map[string]string{
			"engine":           "promptfoo",
			"plugin_id":        entry.ID,
			"category":         entry.Category,
			"default_severity": entry.DefaultSev,
			// U8: which rubric this plugin is judged by. The id only — the
			// criteria text stays out of the capability card so the catalog
			// payload does not grow by ~12KB and so nobody mistakes the card
			// for the authoritative copy of the standard.
			"judge_source": PromptfooJudgeRubricForPlugin(entry.ID).Source,
		},
	}
	// U6: remote-dependent plugins stay visible but are marked unavailable, so
	// users learn "this capability exists and is blocked here" rather than
	// concluding the platform does not have it. Enabled stays true — the flag
	// is advisory for the selector, not a removal from the catalog.
	if entry.RequiresRemoteGeneration {
		card.SafeMetadata["requires_remote_generation"] = "true"
		card.SafeMetadata["unavailable_reason"] = RemoteGenerationUnavailableMessage
	}
	// P2-1: `requires_config` is NOT unavailability. The plugin stays selectable
	// and will pass the engine; it yields 0 cases until config text exists. The
	// distinction is carried explicitly so a UI can warn ("you still need to
	// upload a policy document") instead of greying the option out and hiding a
	// capability the platform intends to support.
	//
	// `fanout` ships on every plugin card because it is what makes a selection's
	// cost predictable: promptfoo applies numTests to each EXPANDED sub-plugin,
	// so `harmful` produces 26x what `policy` does for the same numTests (P0-1).
	if entry.RequiresConfig {
		card.SafeMetadata["requires_config"] = "true"
		card.SafeMetadata["requires_config_reason"] = PluginRequiresConfigReason(entry.ID)
	}
	card.SafeMetadata["fanout"] = strconv.Itoa(PluginFanout(entry.ID))
	return card
}

func promptfooStrategyCard(entry promptfooStrategyCatalogEntry) CapabilityCard {
	card := CapabilityCard{
		SourceType:     CapabilitySourcePromptfooStrategy,
		SourceRef:      "promptfoo_strategy:" + entry.ID,
		Name:           entry.Name,
		Summary:        entry.Summary,
		TargetTypes:    []string{"llm"},
		Languages:      []string{"zh"},
		UseWhen:        []string{"引擎评测需要指定攻击策略包装方式时使用。"},
		InputsRequired: []string{"purpose", "num_tests"},
		Outputs:        []string{"engine_run", "safe_run_result"},
		Tags:           []string{"promptfoo", "engine_strategy", entry.ID},
		Enabled:        true,
		Status:         "published",
		SafeMetadata: map[string]string{
			"engine":      "promptfoo",
			"strategy_id": entry.ID,
		},
	}
	// The platform and promptfoo strategy vocabularies differ for some entries
	// (`hydra` -> `jailbreak:hydra`). Ship the native id only when it differs:
	// an identity mapping would put `promptfoo_strategy_id: base64` on every
	// card, which is noise a reader has to learn to ignore. A consumer that
	// needs the native id reads `StrategyNativeID`, which resolves the identity
	// case too.
	if entry.PromptfooID != "" && entry.PromptfooID != entry.ID {
		card.SafeMetadata["promptfoo_strategy_id"] = entry.PromptfooID
	}
	// Phase 3: unavailable for a reason U6 does not cover (proven no-op, media
	// channel, unattributable probe). Marked the same way as a remote-gated
	// entry so the selector has one rule to apply, with the specific reason.
	//
	// Written BEFORE the remote branch so a strategy that is both remote-gated
	// and otherwise unavailable keeps U6's message. U6's contract is that
	// `requires_remote_generation=true` implies the user is told *that* is the
	// reason, and overwriting it with a more specific but different explanation
	// would send an operator looking for a media/ffmpeg problem on a strategy
	// that never needed one.
	if entry.Unavailable {
		card.SafeMetadata["unavailable"] = "true"
		if entry.UnavailableReason != "" {
			card.SafeMetadata["unavailable_reason"] = entry.UnavailableReason
		}
	}
	if entry.RequiresRemoteGeneration {
		card.SafeMetadata["requires_remote_generation"] = "true"
		card.SafeMetadata["unavailable_reason"] = RemoteGenerationUnavailableMessage
	}
	return card
}

// SearchPromptfooPluginCatalog searches the static plugin/strategy catalog by
// free-form query (Chinese or plugin ids). Returns matched cards, best first.
func SearchPromptfooPluginCatalog(query string, limit int) []CapabilityCard {
	if limit <= 0 {
		limit = DefaultCapabilityCatalogLimit
	}
	if limit > MaxCapabilityCatalogLimit {
		limit = MaxCapabilityCatalogLimit
	}
	q := strings.ToLower(strings.TrimSpace(query))
	matched := make([]CapabilityCard, 0)
	for _, card := range PromptfooPluginCatalogCards() {
		if q == "" || promptfooCatalogCardMatches(card, q) {
			matched = append(matched, card)
		}
	}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched
}

// 中文短语探针（卡片感知）：查询含短语且卡片文本含短语（或对应英文 id 前缀）
// 才算命中。行业词映射到 promptfoo 的 id 前缀，让“医疗/金融/电信”等中文
// 查询能命中整组行业插件。
var promptfooCatalogChinesePhraseKeys = map[string]string{
	"隐私": "", "注入": "", "越狱": "", "偏见": "", "歧视": "", "网络犯罪": "",
	"违法": "", "暴力": "", "走私": "", "越权": "", "幻觉": "", "离题": "", "竞品": "", "品牌": "",
	"武器": "", "仇恨": "", "毒品": "", "色情": "", "自残": "", "自杀": "", "医疗": "medical:",
	"金融": "financial:", "保险": "insurance:", "药房": "pharmacy:", "药学": "pharmacy:",
	"电信": "telecom:", "房产": "realestate:", "住房": "realestate:", "电商": "ecommerce:",
	"基准": "", "基准测试": "",
}

// promptfooCatalogMetaPhrases 是描述目录本身的元词：出现即匹配全部条目
// （用于“引擎评测 / 插件目录”这类泛化浏览查询）。
var promptfooCatalogMetaPhrases = []string{"引擎", "插件", "策略", "评测"}

var promptfooCatalogBrandTokens = map[string]bool{
	"promptfoo": true, "plugin": true, "strategy": true, "plugins": true, "strategies": true,
}

func promptfooCatalogCardMatches(card CapabilityCard, lowerQuery string) bool {
	// 命中堆只保留内容字段（名称/摘要/风险类型/真实 id 标签）；过滤品牌级
	// 标签（promptfoo/engine_plugin/engine_strategy），否则 “prompt” 这类
	// token 会通过品牌串匹配所有卡片，把目标卡片挤出截断窗口。
	parts := []string{card.Name, card.Summary, strings.Join(card.RiskTypes, " ")}
	for _, tag := range card.Tags {
		if tag == "promptfoo" || tag == "engine_plugin" || tag == "engine_strategy" {
			continue
		}
		parts = append(parts, tag)
	}
	haystack := strings.ToLower(strings.Join(parts, " "))
	for _, token := range strings.FieldsFunc(lowerQuery, func(r rune) bool {
		return r == ' ' || r == ',' || r == '，' || r == '、' || r == '/' || r == '-' || r == '_' || r == ':'
	}) {
		if len([]rune(token)) < 2 || promptfooCatalogBrandTokens[token] {
			continue
		}
		if strings.Contains(haystack, token) {
			return true
		}
	}
	for _, meta := range promptfooCatalogMetaPhrases {
		if strings.Contains(lowerQuery, meta) {
			return true
		}
	}
	for phrase, alsoKey := range promptfooCatalogChinesePhraseKeys {
		if !strings.Contains(lowerQuery, phrase) {
			continue
		}
		if strings.Contains(haystack, phrase) {
			return true
		}
		if alsoKey != "" && strings.Contains(haystack, alsoKey) {
			return true
		}
	}
	return false
}

// PromptfooEngineSupportedPluginIDs returns the built-in plugin ids.
func PromptfooEngineSupportedPluginIDs() []string {
	ids := make([]string, 0, len(promptfooPluginCatalog))
	for _, entry := range promptfooPluginCatalog {
		ids = append(ids, entry.ID)
	}
	return ids
}

// PromptfooEngineSupportedStrategyIDs returns the built-in strategy ids.
//
// Includes the strategies discovered from promptfoo (Phase 3), so the engine's
// catalog and the capability catalogue describe the same vocabulary.
func PromptfooEngineSupportedStrategyIDs() []string {
	entries := allPromptfooStrategyEntries()
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

// PromptfooCatalogPluginExists reports whether a plugin id names a built-in
// catalog entry.
//
// U10 needs this as a collision oracle: an expert package must not claim a
// built-in's id, because the report classifies every plugin by looking its id up
// here — a collision would credit the package with the built-in's category,
// severity and U8 rubric. Built as a set rather than a linear scan because it
// runs once per published package, and a linear scan over 119 entries per
// publication is the kind of thing that looks free until it is on a hot path.
func PromptfooCatalogPluginExists(id string) bool {
	return promptfooCatalogPluginIDSet[strings.ToLower(strings.TrimSpace(id))]
}

// PromptfooCatalogStrategyExists reports whether a strategy id names a built-in
// catalog entry. Same collision rationale as PromptfooCatalogPluginExists, and
// it covers the discovered native strategies too — an expert strategy package
// must not be able to claim `morse` and have its probes reported under the
// built-in's name.
func PromptfooCatalogStrategyExists(id string) bool {
	return promptfooCatalogStrategyIDSet[strings.ToLower(strings.TrimSpace(id))]
}

var (
	promptfooCatalogPluginIDSet = buildCatalogIDSet(len(promptfooPluginCatalog), func(yield func(string)) {
		for _, entry := range promptfooPluginCatalog {
			yield(entry.ID)
		}
	})
	// promptfooHandwrittenStrategyIDSet covers only the curated list. It exists
	// separately from promptfooCatalogStrategyIDSet because the two want
	// different things: discovery uses this one to decide which discovered
	// strategies are genuinely *new* (so curated text is not duplicated), while
	// the collision oracle below wants the full merged vocabulary. Building the
	// oracle from the full list and then using it for the dedup check would make
	// every discovered id look pre-existing and the discovery pass would return
	// nothing.
	promptfooHandwrittenStrategyIDSet = buildCatalogIDSet(len(promptfooStrategyCatalog), func(yield func(string)) {
		for _, entry := range promptfooStrategyCatalog {
			yield(entry.ID)
		}
	})
	promptfooCatalogStrategyIDSet = buildCatalogIDSet(
		len(promptfooStrategyCatalog)+len(strategyCatalogData.NativeStrategies),
		func(yield func(string)) {
			for _, entry := range allPromptfooStrategyEntries() {
				yield(entry.ID)
			}
		},
	)
)

// buildCatalogIDSet materializes a lowercased id set from an iterator.
func buildCatalogIDSet(size int, each func(yield func(string))) map[string]bool {
	out := make(map[string]bool, size)
	each(func(id string) {
		if trimmed := strings.ToLower(strings.TrimSpace(id)); trimmed != "" {
			out[trimmed] = true
		}
	})
	return out
}

// promptfooCategoryLabels maps a risk-category key to its Chinese display
// label. Migrated from the engine's former CATEGORY_LABELS table so the label
// vocabulary has a single source of truth (U2/T2.2): the backend now owns the
// labels and ships them to the engine via EngineCatalog.
var promptfooCategoryLabels = map[string]string{
	"privacy":          "隐私泄露",
	"harmful":          "有害内容",
	"injection":        "提示注入",
	"jailbreak":        "越狱对抗",
	"bias":             "偏见歧视",
	"hallucination":    "幻觉与事实性",
	"off-topic":        "离题边界",
	"brand":            "品牌风险",
	"sensitive":        "敏感话题",
	"dataset":          "数据集基准",
	"industry":         "行业合规",
	"security-exploit": "安全利用",
	"other":            "其他",
}

// PromptfooCategoryLabel returns the Chinese display label for a risk-category
// key, falling back to the key itself when unknown.
func PromptfooCategoryLabel(category string) string {
	if label, ok := promptfooCategoryLabels[category]; ok {
		return label
	}
	return category
}

// PromptfooEngineCatalog builds the EngineCatalog the BFF sends with each
// engine run (U2/T2.2). It is derived from the static plugin/strategy catalogs
// so the engine and the backend agree on category, label, and severity for
// every plugin_id — the engine no longer keeps its own regex/mapping tables.
// U6 adds the remote-generation flag so the engine can refuse a gated run
// before promptfoo silently produces 0 cases for it.
// U8 adds judge_criteria + judge_source so the engine judges each plugin by its
// own family standard instead of one shared rubric. Both are derived from
// promptfoo_judge_rubric.go — the catalog never carries its own rubric table,
// so there is exactly one place where "what standard applies to this plugin" is
// decided.
func PromptfooEngineCatalog() *EngineCatalog {
	plugins := make([]EngineCatalogPlugin, 0, len(promptfooPluginCatalog))
	for _, entry := range promptfooPluginCatalog {
		criteria, judgeSource := PromptfooJudgeRubricCriteriaForPlugin(entry.ID)
		plugin := EngineCatalogPlugin{
			ID:            entry.ID,
			Category:      entry.Category,
			CategoryLabel: PromptfooCategoryLabel(entry.Category),
			Severity:      entry.DefaultSev,
			JudgeCriteria: criteria,
			JudgeSource:   judgeSource,
		}
		if entry.RequiresRemoteGeneration {
			plugin.RequiresRemoteGeneration = true
			plugin.UnavailableReason = RemoteGenerationUnavailableMessage
		}
		plugins = append(plugins, plugin)
	}
	strategies := make([]EngineCatalogStrategy, 0, len(promptfooStrategyCatalog))
	for _, entry := range promptfooStrategyCatalog {
		strategy := EngineCatalogStrategy{ID: entry.ID, Name: entry.Name}
		if entry.RequiresRemoteGeneration {
			strategy.RequiresRemoteGeneration = true
			strategy.UnavailableReason = RemoteGenerationUnavailableMessage
			strategy.PromptfooID = PromptfooRemoteDepStrategyID(entry.ID)
		}
		strategies = append(strategies, strategy)
	}
	// P2-2 L3: ship the measured rejection verdict so the engine can refuse a
	// rejected id with a readable code instead of letting promptfoo fail the
	// whole run mid-flight (the chat confirm fast path submits engine requests
	// without passing the BFF's L2 guard, and a queued request can predate it).
	//
	// Only the platform's own reasons are shipped — promptfoo's verbatim zod
	// text stays server-side, since it is internal wording that changes with the
	// upstream build.
	rejected := make(map[string]string, len(pluginLegalityTableData.IllegalPluginIDs))
	for _, id := range IllegalPluginIDs() {
		rejected[id] = PluginLegalityReason(id)
	}

	return &EngineCatalog{Plugins: plugins, Strategies: strategies, RejectedPlugins: rejected}
}
