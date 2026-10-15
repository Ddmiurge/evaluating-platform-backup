package maclaw

// promptfoo_strategy_retirement.go — P3-1 · 策略轴上「平台自造维度」的退役记录与提交闸。
//
// # 为什么策略轴需要这一层（插件轴上一批已经做过）
//
// 平台历史上有一批**自造的**攻击策略 id（`direct` / `role-play` / `encoding` /
// `multi-turn` / `assumed-knowledge` / `few-shot` / `many-shot` / `translate` /
// `otherEncodings`）。它们不是冗余代码，也不是当初造错：在 Plan A 下它们是**真的
// 有效的** —— `generateTests.ts` 把策略 id 作为**文本**拼进平台的生成提示词
// （`攻击策略（strategy_id 只能从以下选择）：…`），由我们自己的生成 LLM 解释并回报。
// 换句话说，那时「策略」是**提示词级的词汇表**，归平台所有，所以任何字符串都"能用"。
//
// Plan C 把生成主体换成了 promptfoo，`redteam.strategies` 变成 promptfoo 拥有的
// **可执行变换**词汇表。这 9 个 id 在其中没有实现：转发过去会抛错，静默丢弃则什么
// 都不做。而**丢弃是静默的** —— 不报错、不降级，只在报告里留一个 `strategy_stats=none`
// （item12 实测就是这个形态）。向导表单的**默认值**恰好是其中一个（`direct`）。
//
// 所以这一层要做的事与插件轴完全同形：**下线 + 说清理由 + 指出能力搬到哪里**，
// 并让命名它们的请求在**提交边界**被显式拒绝，而不是跑到引擎里静默丢弃。
//
// # 与插件轴的一处刻意差异
//
// `ReplacementIDs` 允许为空。插件轴当时要求每条下线记录都给出替代 id；策略轴上
// 有几种情形**没有替代**（promptfoo 没有等价策略 / 该能力是配置项而非策略），
// 硬凑一个近邻就是「静默改变用户要的手法」—— 那正是本平台反复拒绝的事。
// 无替代时由 `ReplacementLabel` 承担全部解释责任，并由测试钉住它非空且说明了原因。
//
// # D 类四项：永久移除，不是待办（2026-10-15 修订）
//
// `role-play` / `assumed-knowledge` / `few-shot` / `many-shot` 曾经记着一条待办
// （原计划 P3-7a）：用 promptfoo 的 `custom:<variant>` + `config.strategyText` 把它们
// 实现回来。那条待办**已撤销**，这四项的处置是**永久移除**。
//
// 撤销的理由是它把问题问反了。这四项不是「promptfoo 有、平台没接」，而是**平台自己
// 造的**：实测（`testdata/promptfoo_strategy_catalog.json` 与
// `testdata/promptfoo_plugin_legality.json`，promptfoo 0.123.0）确认这 4 个 id
// **既不在 35 条原生策略里，也不在 119 个插件里** —— 两个槽位都没有。
//
// 用 `custom:` 把它们实现回来，等于承认平台可以继续自造攻击维度，只是换个更绕的
// 写法。而「两套词汇表并存」正是 P3-1 要消除的负债本身：向导的**默认值**曾经就是
// 其中一个（`direct`），默认提交得到一个 `strategy_stats=none` 的运行且无人告知。
//
// 所以策略轴的口径定为：**平台的策略词汇表就是 promptfoo 的策略词汇表**。需要新的
// 攻击手法时，先看 promptfoo 有没有；没有就不提供，而不是先造一个 id、再想办法让引擎
// 认识它。`custom` 这个扩展点仍然保留给**专家发布的策略包** —— 那是用户内容，不是
// 平台自造维度，两者不能混为一谈。

import (
	"fmt"
	"sort"
	"strings"
)

// 下线的三种情形。用**数据**表达而不是靠读那句散文 —— 与插件轴的
// `replacement_slot` 同形：测试与前端需要能判别，不该去 grep 文案。
const (
	// strategyReplacementByIDs：能力搬到了其它策略 id 上。
	strategyReplacementByIDs = "ids"
	// strategyReplacementNoneNeeded：能力没有丢，它本来就是「不选择」。
	// 只有 `direct` 属于这一类 —— Plan C 下不选策略就是直接攻击。
	strategyReplacementNoneNeeded = "none_needed"
	// strategyReplacementNoEquivalent：promptfoo 里没有对应机制（或它走的是配置项
	// 而不是策略）。此时不映射近邻 —— 那会静默改变用户要的手法。
	strategyReplacementNoEquivalent = "no_equivalent"
)

// retiredStrategyEntry is one withdrawn platform strategy id plus where the
// capability went (or why it has nowhere to go).
type retiredStrategyEntry struct {
	// ID is the withdrawn platform strategy id.
	ID string
	// Name is the label the withdrawn entry used to carry.
	Name string
	// Reason explains the withdrawal in user-facing terms.
	Reason string
	// ReplacementKind is one of the three constants above. It is the machine-readable
	// counterpart of ReplacementLabel: tests and the frontend branch on this, never on
	// the prose.
	ReplacementKind string
	// ReplacementIDs are the ids to use instead. Non-empty exactly when
	// ReplacementKind is strategyReplacementByIDs — see the file header for why empty
	// is allowed here but not on the plugin axis.
	ReplacementIDs []string
	// ReplacementLabel is the ready-to-render sentence. It is the only thing a user
	// reads, so it must be self-contained: what was withdrawn, what to pick instead,
	// or why nothing can be picked.
	ReplacementLabel string
}

// retiredStrategyCatalog records P3-1's nine removals.
//
// Nine entries, and they stay nine: the four D-class ids (role-play /
// assumed-knowledge / few-shot / many-shot) are permanently removed rather than
// waiting to be re-implemented as custom strategies — see the file header.
//
// Asserted against the *absence* of these ids from `promptfooStrategyCatalog` (a
// withdrawal and a lingering catalog entry are mutually exclusive), so an id cannot
// be dropped from the selector without a record, nor recorded without being dropped.
var retiredStrategyCatalog = []retiredStrategyEntry{
	{
		ID:   "direct",
		Name: "直接攻击",
		Reason: "「直接攻击」不是策略：Plan C 下不选择任何策略就是直接攻击，" +
			"它占一个策略位反而让默认提交落在一个没有实现的 id 上。",
		ReplacementKind:  strategyReplacementNoneNeeded,
		ReplacementIDs:   nil,
		ReplacementLabel: "「直接攻击」不需要选择：不选任何攻击策略时，评测就是直接向目标提问。",
	},
	{
		ID:   "role-play",
		Name: "角色扮演",
		Reason: "平台自造的提示词维度；promptfoo 的策略词汇表里没有角色扮演包装" +
			"（实测 35 条原生策略、119 个插件都没有）。平台不再自造攻击维度，因此永久移除。",
		ReplacementKind:  strategyReplacementByIDs,
		ReplacementIDs:   []string{"jailbreak-templates"},
		ReplacementLabel: "「角色扮演」已下线且不会再提供（promptfoo 没有该策略，平台不再自造维度）；最接近的是「越狱模板库」（静态框架式越狱话术，其中含角色包装类模板），但它不是等价项。",
	},
	{
		ID:               "encoding",
		Name:             "编码混淆",
		Reason:           "平台自造的**聚合**维度；promptfoo 把编码拆成具体策略并已逐个提供。",
		ReplacementKind:  strategyReplacementByIDs,
		ReplacementIDs:   []string{"base64", "rot13", "hex", "leetspeak", "homoglyph"},
		ReplacementLabel: "「编码混淆」是聚合项，已下线；请直接选择具体编码策略：Base64 编码 / ROT13 编码 / 十六进制编码 / Leet 字符替换 / 同形字符混淆。",
	},
	{
		ID:               "multi-turn",
		Name:             "多轮诱导",
		Reason:           "平台自造的**聚合**维度；promptfoo 的多轮策略是具体项并已逐个提供。",
		ReplacementKind:  strategyReplacementByIDs,
		ReplacementIDs:   []string{"crescendo", "goat", "hydra"},
		ReplacementLabel: "「多轮诱导」是聚合项，已下线；请直接选择具体多轮策略：渐进升级 / GOAT 多轮越狱 / Hydra 多分支越狱（另有树搜索越狱）。",
	},
	{
		ID:     "assumed-knowledge",
		Name:   "假设已知",
		Reason: "平台自造的提示词维度；promptfoo 没有「以已知为前提诱导补全」这类策略。平台不再自造攻击维度，因此永久移除。",
		// 有近邻（伪造权威标记）但语义不同 —— 映射过去就是静默改变手法，故不映射。
		ReplacementKind:  strategyReplacementNoEquivalent,
		ReplacementIDs:   nil,
		ReplacementLabel: "「假设已知」已下线且不会再提供；promptfoo 没有等价策略，平台不做近似映射（近似会静默改变你要的攻击手法）。",
	},
	{
		ID:               "few-shot",
		Name:             "少样本诱导",
		Reason:           "平台自造的提示词维度；promptfoo 没有示例注入类策略。平台不再自造攻击维度，因此永久移除。",
		ReplacementKind:  strategyReplacementNoEquivalent,
		ReplacementIDs:   nil,
		ReplacementLabel: "「少样本诱导」已下线且不会再提供；promptfoo 没有示例注入类策略，平台不做近似映射。",
	},
	{
		ID:               "many-shot",
		Name:             "多样本诱导",
		Reason:           "平台自造的提示词维度；promptfoo 没有示例注入类策略（长上下文诱导同样无对应项）。平台不再自造攻击维度，因此永久移除。",
		ReplacementKind:  strategyReplacementNoEquivalent,
		ReplacementIDs:   nil,
		ReplacementLabel: "「多样本诱导」已下线且不会再提供；promptfoo 没有示例注入类策略，平台不做近似映射。",
	},
	{
		ID:     "translate",
		Name:   "翻译绕过",
		Reason: "该能力在 promptfoo 里由 `language` **配置**表达，不是策略；平台尚未接入这个配置项。",
		// 能力存在但走的是另一种机制，所以这里没有 strategy id 可给。
		ReplacementKind:  strategyReplacementNoEquivalent,
		ReplacementIDs:   nil,
		ReplacementLabel: "「翻译绕过」已下线；跨语言绕过在 promptfoo 里是 `language` 配置而不是攻击策略，平台尚未接入该项（阶段三待办 P3-7b）。",
	},
	{
		ID:               "otherEncodings",
		Name:             "其他编码变体",
		Reason:           "平台对若干具体编码策略的**聚合**项；promptfoo 已把它们作为独立策略提供。",
		ReplacementKind:  strategyReplacementByIDs,
		ReplacementIDs:   []string{"morse", "piglatin", "camelcase", "emoji"},
		ReplacementLabel: "「其他编码变体」是聚合项，已下线；请直接选择具体编码策略：摩斯电码 / Pig Latin / 驼峰式 / Emoji 替换。",
	},
}

// PromptfooRetiredStrategy is one withdrawn strategy id plus its replacement path.
type PromptfooRetiredStrategy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Reason explains why the id was withdrawn.
	Reason string `json:"reason"`
	// ReplacementKind is `ids` | `none_needed` | `no_equivalent` — the machine-readable
	// counterpart of ReplacementLabel, so callers branch on data rather than prose.
	ReplacementKind string `json:"replacement_kind"`
	// ReplacementIDs may be empty: some platform dimensions have no equivalent on
	// the Plan C path, and inventing a near neighbour would silently change the
	// technique the user asked for.
	ReplacementIDs []string `json:"replacement_ids"`
	// ReplacementLabel is the ready-to-render sentence.
	ReplacementLabel string `json:"replacement_label"`
}

// PromptfooRetiredStrategies returns every withdrawn strategy id, sorted by id.
func PromptfooRetiredStrategies() []PromptfooRetiredStrategy {
	out := make([]PromptfooRetiredStrategy, 0, len(retiredStrategyCatalog))
	for _, entry := range retiredStrategyCatalog {
		out = append(out, retiredStrategyToPublic(entry))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PromptfooRetiredStrategyGuidance returns the replacement path for a withdrawn id.
func PromptfooRetiredStrategyGuidance(strategyID string) (PromptfooRetiredStrategy, bool) {
	trimmed := strings.TrimSpace(strategyID)
	for _, entry := range retiredStrategyCatalog {
		if entry.ID == trimmed {
			return retiredStrategyToPublic(entry), true
		}
	}
	return PromptfooRetiredStrategy{}, false
}

// retiredStrategyToPublic copies an entry out, including its slice (callers are HTTP
// handlers and tests; handing out the backing array would let one mutate the table).
func retiredStrategyToPublic(entry retiredStrategyEntry) PromptfooRetiredStrategy {
	return PromptfooRetiredStrategy{
		ID:               entry.ID,
		ReplacementKind:  entry.ReplacementKind,
		Name:             entry.Name,
		Reason:           entry.Reason,
		ReplacementIDs:   append([]string(nil), entry.ReplacementIDs...),
		ReplacementLabel: entry.ReplacementLabel,
	}
}

// RetiredStrategyIDs returns the sorted withdrawn strategy ids.
func RetiredStrategyIDs() []string {
	ids := make([]string, 0, len(retiredStrategyCatalog))
	for _, entry := range retiredStrategyCatalog {
		ids = append(ids, entry.ID)
	}
	sort.Strings(ids)
	return ids
}

// EngineErrStrategyIDInvalid 是提交边界的机器可读拒绝码。
//
// 与插件轴的 `plugin_id_invalid` 同形：用户要读的是 payload 里的 `guidance`，
// 而码只负责让前端/运维有一条稳定的判定依据。刻意**不为「已下线」单开一个码** ——
// 对用户来说「这个 id 用不了」是同一件事，区分它属于「平台撤掉了」还是「promptfoo
// 不认」是排查者的事，那份信息在 reason/label 里。
const EngineErrStrategyIDInvalid = "strategy_id_invalid"

// EngineStrategyRetiredGuardError 报告请求里出现了已下线的策略 id。
type EngineStrategyRetiredGuardError struct {
	Code string
	// Strategies 是命中的 id，去重并排序。
	Strategies []string
	// Guidance 是每个 id 的替代路径（含无替代的说明），供前端直接渲染。
	Guidance []PromptfooRetiredStrategy
}

// Error implements error.
func (e *EngineStrategyRetiredGuardError) Error() string {
	names := make([]string, 0, len(e.Strategies))
	for _, id := range e.Strategies {
		label := id
		if guidance, ok := PromptfooRetiredStrategyGuidance(id); ok && strings.TrimSpace(guidance.Name) != "" {
			label = fmt.Sprintf("%s（%s）", id, guidance.Name)
		}
		names = append(names, label)
	}
	msg := "以下攻击策略已下线（它们不是 promptfoo 的策略 id，在 Plan C 下没有实现，选中会导致该维度被静默丢弃）：" +
		strings.Join(names, "、") + "。"
	for _, g := range e.Guidance {
		if label := strings.TrimSpace(g.ReplacementLabel); label != "" {
			msg += label
		}
	}
	return msg
}

// ValidateEngineRunStrategyRetirement 在提交边界拒绝已下线的策略 id（P3-1）。
//
// 与 ValidateEngineRunPluginLegality 同位置、同形状：在凭据物化/落库/派发之前，
// 返回 409 + 每个 id 的替代路径。未知 id（不在下线表里的）**不在这里拦** ——
// 那属于「promptfoo 认不认」的问题，由引擎侧对未知策略显式拒绝兜底（P3-3），
// 这里只管平台自己撤掉的那些。
func ValidateEngineRunStrategyRetirement(strategies []EngineCapabilityRef) error {
	hits := make(map[string]bool)
	for _, ref := range strategies {
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			continue
		}
		if _, retired := PromptfooRetiredStrategyGuidance(id); retired {
			hits[id] = true
		}
	}
	if len(hits) == 0 {
		return nil
	}
	guard := &EngineStrategyRetiredGuardError{
		Code:       EngineErrStrategyIDInvalid,
		Strategies: make([]string, 0, len(hits)),
	}
	for id := range hits {
		guard.Strategies = append(guard.Strategies, id)
		if guidance, ok := PromptfooRetiredStrategyGuidance(id); ok {
			guard.Guidance = append(guard.Guidance, guidance)
		}
	}
	sort.Strings(guard.Strategies)
	sort.Slice(guard.Guidance, func(i, j int) bool { return guard.Guidance[i].ID < guard.Guidance[j].ID })
	return guard
}
