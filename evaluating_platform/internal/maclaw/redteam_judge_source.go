package maclaw

// redteam_judge_source.go — U8 判定标准的可追溯层。
//
// # 与 U4 的 judge_track 刻意不合并
//
//	judge_track  = 「谁判的」    —— platform | engine（U4，判定**链路**）
//	judge_source = 「按什么标准判」—— plugin:<id> | category:<key> |
//	                                fallback:unmapped（U8，判定**标准**）
//
// 两者**正交**：engine 链路既可能按插件判也可能按族判，platform 链路同理。
// 合成一个字段会出现「换了链路」覆盖「换了标准」的情况——例如从 engine
// 链路切回 platform 链路时，标准的变化被链路的同一个值吃掉，报告上看不出来。
// 故本文件只管标准这一层，键名与 judge_track 不冲突、不复用。
//
// # 为什么要落进 report.metadata 而不是新增数据库列
//
// 与 U4 同一理由（DD-5=A）：复用同一 schema，不新增列、不新增 migration。
// 旧报告天然缺该键，由解析层返回「未记录」，**不报错、不推断**——
// 判定标准无法从旧数据里猜出来（U7 之前只有一条 rubric，但那条rubric 是不是
// 「通用档」取决于当时的代码版本，猜出来的答案是不可信的）。
//
// 【与 U4 的一处刻意差异】judge_track 的非法值必须报错（fail-closed），
// 因为 platform/engine 二选一，猜错会直接误导；而 judge_source 的非法值
// **同样 fail-closed**，理由是它是一个**可枚举的标识**（档位前缀 + 已知族名），
// 不在枚举内说明写报告的代码有 bug 或数据被改过，静默接受等于把一个
// 没人能解释的口径写进报告。这里不做「宽松读路径」，因为本键**不参与**
// 老报告的渲染兜底：缺键走「未记录」分支即可，不需要推断。

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// RedteamJudgeSourceMetadataKey 是 judge_source 在 report.metadata 中的键名。
	//
	// 必须与引擎侧 `PLUGIN_JUDGE_SOURCE_METADATA_KEY` 语义对应，但**字符串本身
	// 不要求相同**：引擎那个键落在每条 eval 结果的 testCase.metadata 上（本文件
	// 不读它），本键落在报告 metadata 上。两处不是同一条数据通道。
	RedteamJudgeSourceMetadataKey = "judge_source"

	// RedteamJudgeSourcesMetadataKey 是本次运行**按档位计数**的键（JSON 数组）。
	//
	// 为什么与单值键并存：单值键回答「这份报告主要按什么标准判」，
	// 计数键回答「有没有哪一部分探测其实没有标准」。只有单值键时，
	// 一个run 里 99 条按族判、1 条落兜底会显示成「按族判」——退化被抹掉了。
	RedteamJudgeSourcesMetadataKey = "judge_sources"

	// RedteamJudgeSourceFallback 表示报告中存在落兜底档的探测。
	// 取值 "1"，与其他 metadata 的布尔约定一致（见 requires_remote_generation）。
	RedteamJudgeSourceFallback = "1"
)

// JudgeSourceSummary 是一次运行的判定标准分布。
type JudgeSourceSummary struct {
	// Sources 是出现过的档位标识 → 探测数。
	Sources map[string]int
	// FallbackProbes 是落在未映射兜底档的探测数。
	FallbackProbes int
	// PromptfooOwnedProbes 是「平台标准注入不了、由 promptfoo 自带 grader 判定」
	// 的探测数。与 FallbackProbes 分开，因为成因与处置都不同。
	PromptfooOwnedProbes int
	// Distinct 是出现过的档位数。
	Distinct int
}

// HasFallback 报告本次运行是否有探测落兜底档。
func (s JudgeSourceSummary) HasFallback() bool {
	return s.FallbackProbes > 0
}

// SummarizeEngineJudgeSources 从引擎结果汇总判定标准分布。
//
// result 为 nil 或没有 judge_sources（U8 之前的引擎）时返回零值 Summary：
// 「未记录」与「全部按族判」是两种不同的事实，**不能互相顶替**——
// 前者说明这份报告产生时还没有分档判定，后者说明确实每条都有标准。
func SummarizeEngineJudgeSources(result *EngineSafeResult) JudgeSourceSummary {
	summary := JudgeSourceSummary{Sources: map[string]int{}}
	if result == nil {
		return summary
	}
	recorded := false
	for _, entry := range result.JudgeSources {
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			continue
		}
		recorded = true
		summary.Sources[source] += entry.Probes
		if entry.IsFallback || source == JudgeSourceFallbackUnmapped {
			summary.FallbackProbes += entry.Probes
		}
		if strings.HasPrefix(source, JudgeSourcePrefixPromptfoo+":") {
			summary.PromptfooOwnedProbes += entry.Probes
		}
	}
	if !recorded {
		return JudgeSourceSummary{Sources: map[string]int{}}
	}
	summary.Distinct = len(summary.Sources)
	return summary
}

// formatJudgeSourceHistogram 把分布渲染成稳定排序的一行，便于落进 metadata
// 并做精确比对（顺序固定 → 可以直接断言，不受 map 迭代顺序影响）。
func formatJudgeSourceHistogram(summary JudgeSourceSummary) string {
	if len(summary.Sources) == 0 {
		return ""
	}
	keys := make([]string, 0, len(summary.Sources))
	for key := range summary.Sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, summary.Sources[key]))
	}
	return strings.Join(parts, ",")
}

// NormalizeJudgeSource 归一化并校验一个 judge_source 档位标识。
//
// 空串返回 ("", nil) —— 表示「未记录」，由调用方决定呈现方式。
// 非空但不在允许集合内返回错误，写路径必须 fail-closed。
//
// 允许集合是**前缀 + 已知族名**，不是自由文本：
//   - `fallback:unmapped` —— 未映射兜底档（唯一允许的 fallback 值）；
//   - `category:<key>`   —— 族级，key 必须在族级 rubric 表里；
//   - `plugin:<id>`      —— 插件覆盖，id 必须在插件目录里。
//
// 这样约束住形状是有意的：judge_source 是给人看的溯源标识，
// 出现 `category:未知族` 说明写报告的一侧算错了族，那必须报错而不是照抄。
func NormalizeJudgeSource(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if trimmed == JudgeSourceFallbackUnmapped {
		return trimmed, nil
	}
	prefix, value, found := strings.Cut(trimmed, ":")
	if !found || value == "" {
		return "", fmt.Errorf("invalid %s %q: expected %q, %s:<category> or %s:<plugin_id>",
			RedteamJudgeSourceMetadataKey, raw, JudgeSourceFallbackUnmapped,
			JudgeSourcePrefixCategory, JudgeSourcePrefixPlugin)
	}
	switch prefix {
	case JudgeSourcePrefixCategory:
		if _, ok := promptfooCategoryJudgeRubrics[value]; !ok {
			return "", fmt.Errorf("invalid %s %q: category %q has no judge rubric", RedteamJudgeSourceMetadataKey, raw, value)
		}
	case JudgeSourcePrefixPlugin:
		if PromptfooPluginCategory(value) == "" {
			return "", fmt.Errorf("invalid %s %q: plugin %q is not in the catalog", RedteamJudgeSourceMetadataKey, raw, value)
		}
	case JudgeSourcePrefixPromptfoo:
		// 只允许「确实受上游缺陷影响、平台标准注入不了」的插件用这一档，
		// 否则它会变成一个后门：任何插件都能声称「我这条是 promptfoo 判的」，
		// 从而绕过平台标准而不被察觉。故与引擎侧的跳过清单同源。
		if !PromptfooJudgeRubricInjectedByPromptfoo(value) {
			return "", fmt.Errorf("invalid %s %q: plugin %q is not affected by the promptfoo config defect, so it cannot claim %s",
				RedteamJudgeSourceMetadataKey, raw, value, JudgeSourcePrefixPromptfoo)
		}
	default:
		return "", fmt.Errorf("invalid %s %q: unknown prefix %q", RedteamJudgeSourceMetadataKey, raw, prefix)
	}
	return trimmed, nil
}

// ValidateReportJudgeSourceMetadata 校验报告 metadata 里显式声明的 judge_source。
//
// 与 U4 的 ValidateReportJudgeTrackMetadata 同形：只校验、不改写、不兜底。
func ValidateReportJudgeSourceMetadata(metadata map[string]string) (string, error) {
	if len(metadata) == 0 {
		return "", nil
	}
	raw, ok := metadata[RedteamJudgeSourceMetadataKey]
	if !ok {
		return "", nil
	}
	return NormalizeJudgeSource(raw)
}

const (
	judgeSourceCaptionSingle    = "判定标准：%s —— 本报告的判定按此标准执行。"
	judgeSourceCaptionMultiple  = "判定标准：本次运行按 %d 档标准判定（%s）—— 同一报告内不同插件的判定尺度不同，跨插件比较攻击成功率时须注意。"
	judgeSourceCaptionFallback  = "⚠️ 其中 %d 条探测未匹配到插件族标准，已回落到通用兜底标准；这部分结论的口径弱于按族判定的结论。"
	judgeSourceCaptionPromptfoo = "其中 %d 条探测由 promptfoo 自带的判定标准作出（平台标准未能注入）；该部分与平台族标准口径不同，横向比较时请注意。"
	judgeSourceSuffixUnrecorded = "（本报告未记录判定标准，产生于按档位判定上线之前。）"

	// 页脚用的**短**句，理由与 U4 的 judgeTrackFooter* 相同：8pt 字号下
	// 完整句会超宽被截断，而被截掉的正是最该被看见的兜底警告。
	judgeSourceFooterStandard = "判定标准：%s"
	judgeSourceFooterFallback = "判定标准：%s·含 %d 条兜底判定（口径较弱）"
	judgeSourceFooterUnknown  = "判定标准未记录"
)

// RedteamJudgeSourceCaption 渲染判定标准说明，进报告基本信息段落。
//
// 三种形态，措辞差异本身就是信息：
//   - 未记录 → 明确说「未记录」，**不**替它选一个标准（U8 之前的历史报告）；
//   - 单档   → 直接给出该档；
//   - 多档   → 说明「同一报告内尺度不同」，这是 U8 落地成功的标志，
//     不是异常，所以措辞是陈述而非警告。
//
// 兜底警告**只在该run 真的有探测落兜底时**出现，且用 ⚠️ 前缀与计数，
// 让读者能立刻判断这条 run 的分数能不能与别的 run 比。
func RedteamJudgeSourceCaption(metadata map[string]string, summary JudgeSourceSummary) string {
	if summary.Distinct > 0 && len(metadata) > 0 {
		if _, err := NormalizeJudgeSource(metadata[RedteamJudgeSourceMetadataKey]); err != nil {
			// 读路径不 panic（报告必须打得开），但也不能装作没事：
			// 明确说元数据非法，而不是按某个标准呈现。
			return "判定标准：报告元数据中的判定标准字段非法，已忽略；请联系报告生成方核对。"
		}
	}
	// 分布键（计数）比单值键更完整：它记录了本次运行**每一档**的探测数。
	// 单值键缺失时不能就此说「未记录」—— 那会把一份有明确标准、只是
	// 写报告的一侧漏了单值键的报告，误标成没有标准。前端
	// reportJudgeSourceCaption 用了同一套优先级，两侧必须一致。
	if summary.Distinct > 1 {
		return multipleJudgeSourceCaption(summary)
	}
	if len(metadata) == 0 || strings.TrimSpace(metadata[RedteamJudgeSourceMetadataKey]) == "" {
		if summary.HasFallback() {
			return multipleJudgeSourceCaption(summary)
		}
		return judgeSourceSuffixUnrecorded
	}
	source := strings.TrimSpace(metadata[RedteamJudgeSourceMetadataKey])
	caption := fmt.Sprintf(judgeSourceCaptionSingle, source)
	if summary.HasFallback() {
		caption += fmt.Sprintf(judgeSourceCaptionFallback, summary.FallbackProbes)
	}
	if summary.PromptfooOwnedProbes > 0 {
		caption += fmt.Sprintf(judgeSourceCaptionPromptfoo, summary.PromptfooOwnedProbes)
	}
	return caption
}

func multipleJudgeSourceCaption(summary JudgeSourceSummary) string {
	caption := fmt.Sprintf(judgeSourceCaptionMultiple, summary.Distinct, formatJudgeSourceHistogram(summary))
	if summary.HasFallback() {
		caption += fmt.Sprintf(judgeSourceCaptionFallback, summary.FallbackProbes)
	}
	if summary.PromptfooOwnedProbes > 0 {
		caption += fmt.Sprintf(judgeSourceCaptionPromptfoo, summary.PromptfooOwnedProbes)
	}
	return caption
}

// RedteamJudgeSourceFooterCaption 渲染页脚专用短句。
//
// 非法元数据在页脚必须显示「未记录」而不是某个具体标准 ——
// 页脚是每页都在的位置，在此处说谎会让整份 PDF 的口径标注都不可信。
func RedteamJudgeSourceFooterCaption(metadata map[string]string, summary JudgeSourceSummary) string {
	if len(metadata) == 0 || strings.TrimSpace(metadata[RedteamJudgeSourceMetadataKey]) == "" {
		if summary.HasFallback() {
			return fmt.Sprintf(judgeSourceFooterFallback, formatJudgeSourceHistogram(summary), summary.FallbackProbes)
		}
		return judgeSourceFooterUnknown
	}
	if _, err := NormalizeJudgeSource(metadata[RedteamJudgeSourceMetadataKey]); err != nil {
		return judgeSourceFooterUnknown
	}
	source := strings.TrimSpace(metadata[RedteamJudgeSourceMetadataKey])
	if summary.HasFallback() {
		return fmt.Sprintf(judgeSourceFooterFallback, source, summary.FallbackProbes)
	}
	return fmt.Sprintf(judgeSourceFooterStandard, source)
}

// parseJudgeSourceHistogramMetadata 从报告 metadata 读回判定标准分布。
//
// 读路径**不 panic**：报告必须打得开。格式非法时返回零值 Summary
// （=未记录），而 caption 会在单值键非法时明确提示元数据有问题——
// 两处的分工是「打得开」与「不说谎」各自负责一件事。
func parseJudgeSourceHistogramMetadata(metadata map[string]string) JudgeSourceSummary {
	raw := strings.TrimSpace(metadata[RedteamJudgeSourcesMetadataKey])
	if raw == "" {
		return JudgeSourceSummary{Sources: map[string]int{}}
	}
	sources, err := ParseJudgeSourceHistogram(raw)
	if err != nil || len(sources) == 0 {
		return JudgeSourceSummary{Sources: map[string]int{}}
	}
	summary := JudgeSourceSummary{Sources: sources, Distinct: len(sources)}
	for source, probes := range sources {
		if source == JudgeSourceFallbackUnmapped {
			summary.FallbackProbes += probes
		}
		if strings.HasPrefix(source, JudgeSourcePrefixPromptfoo+":") {
			summary.PromptfooOwnedProbes += probes
		}
	}
	return summary
}

// dominantJudgeSource 取探测数最多的档位；同数时按标识字典序取较小者，
// 保证同一个 run 反复渲染得到同一个值（否则报告每次打开口径都可能跳）。
//
// 与 formatJudgeSourceHistogram 放在同一处，因为两者必须用**同一套**排序
// 规则：单值键与计数键对不上会让读者看到两个互相矛盾的「主要口径」。
func dominantJudgeSource(summary JudgeSourceSummary) string {
	best := ""
	bestCount := -1
	for _, source := range sortedJudgeSources(summary.Sources) {
		if count := summary.Sources[source]; count > bestCount {
			best, bestCount = source, count
		}
	}
	return best
}

func sortedJudgeSources(sources map[string]int) []string {
	keys := make([]string, 0, len(sources))
	for key := range sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ParseJudgeSourceHistogram 解析 formatJudgeSourceHistogram 的输出。
//
// 用途是**读回自己写下的 metadata**（例如校验落库后的值仍能解析），
// 而不是解析外部输入，故格式非法时返回错误而不是尽力猜测。
func ParseJudgeSourceHistogram(raw string) (map[string]int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]int{}, nil
	}
	out := map[string]int{}
	for _, part := range strings.Split(trimmed, ",") {
		key, count, found := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, fmt.Errorf("invalid judge source histogram %q: bad segment %q", raw, part)
		}
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(count), "%d", &n); err != nil || n < 0 {
			return nil, fmt.Errorf("invalid judge source histogram %q: bad count in %q", raw, part)
		}
		out[key] = n
	}
	return out, nil
}
