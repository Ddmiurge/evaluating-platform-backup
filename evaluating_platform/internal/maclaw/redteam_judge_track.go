package maclaw

// redteam_judge_track.go — U4 判定双轨显式化（承接 04DD-2=A / DD-5=A）。
//
// 背景：平台存在**两条互不相同的判定链路**，产出的安全分与风险等级口径不同：
//
//	platform —— 平台侧 Judge（judge_attack_result / LLM 复判），
//	             样本、模板与平台 rubric 都在平台内闭环。
//	engine   —— promptfoo 引擎侧 Judge（run_promptfoo_redteam_evaluation），
//	             样本与 rubric 都在引擎内闭环。
//
// 此前两份报告的 JSON 结构完全一致，**报告本身不携带「这份分数是哪套口径算出来的」**，
// 用户只能靠标题里的「（promptfoo 引擎）」字样猜——而标题可被自由覆盖，
// 于是「口径混读」成为可能：把两条链路的分数并排放在同一张表里做趋势对比。
//
// 本文件只做一件事：把口径显式化并**可校验**。
//
// 【明确不做】统一评分（04 DD-2 的 C 方案）。两条链路的分数**不可比**，
// 本文件不会、也不允许把platform 与 engine 的分数折算到同一量纲——
// 那会让「口径」这个字段失去意义（折算本身就是第二次隐藏口径）。
//
// 【落库方式】复用同一schema，judge_track 存在 report.metadata 里（DD-5=A），
// 不新增数据库列、不新增 migration。这样旧报告的 JSONB 行天然缺该键，
// 由解析层按 metadata.engine 推断，向后兼容不需要数据回填。
//
// 【读写的两种严格度】这是本文件最容易被误改的地方，故显式说明：
//
//   - 写路径（CompileReport 等）：显式传入的 judge_track **非法即报错**，
//     fail-closed。静默接受一个非法口径，等于把错误口径写进报告并让所有人误读。
//   - 读路径（渲染/导出）：非法值**不报错**，但必须由解析层把异常带出去，
//     由渲染层显式提示（见 RedteamJudgeTrackCaption）。理由是读路径的输入
//     包含历史行与人工改动过的行，让它 panic 或报错等于让一份旧报告打不开——
//     「打不开」比「口径不明」更糟。但「口径不明」也不能静默：
//     Invalid 字段非空时，caption 会明说元数据非法。
//
// 【分层归属】按 U3 的分包规则，无状态纯函数本应放internal/maclaw/redteam 子包。
// 本文件**故意留在父包**：① 判定的权威口径归属报告域（artifact_service / bridges），
// 放进 redteam 子包会让「报告渲染」反向依赖「批量执行」子包；② U4 的范围纪律要求
// 不动 U3 已验收的分包边界。若后续架构评审要求下沉，需连带 review 子包 doc.go 的
// 职责声明（该文件已把职责限定为「批量执行的纯逻辑」）。

import (
	"fmt"
	"strings"
)

// RedteamJudgeTrack 标识一份报告的判定口径（判定链路）。
type RedteamJudgeTrack string

const (
	// RedteamJudgeTrackPlatform 平台侧 Judge 链路（execute_redteam_evaluation_batch）。
	RedteamJudgeTrackPlatform RedteamJudgeTrack = "platform"
	// RedteamJudgeTrackEngine promptfoo 引擎侧 Judge 链路（run_promptfoo_redteam_evaluation）。
	RedteamJudgeTrackEngine RedteamJudgeTrack = "engine"
)

const (
	// RedteamJudgeTrackMetadataKey 是 judge_track 在 report.metadata 中的键名。
	// 与前端 services/report.ts 的 MaclawReport.judge_track、聊天卡片
	// metadata 的 judge_track 共用同一个字符串，三处不允许各自拼写。
	RedteamJudgeTrackMetadataKey = "judge_track"

	// redteamJudgeTrackEngineMetadataKey 是引擎链路在 metadata 中留下的既有标记，
	// U4 之前就存在（见 promptfoo_engine_bridge.go 的 engineReportMetadata）。
	// 它是旧报告推断 judge_track 的唯一依据。
	redteamJudgeTrackEngineMetadataKey = "engine"
)

// RedteamJudgeTrackSource 说明 judge_track 的来源，决定 caption 的措辞。
type RedteamJudgeTrackSource string

const (
	// RedteamJudgeTrackSourceExplicit metadata.judge_track 显式存在且合法。
	RedteamJudgeTrackSourceExplicit RedteamJudgeTrackSource = "explicit"
	// RedteamJudgeTrackSourceEngineMetadata judge_track 缺失，由 metadata.engine 推断。
	RedteamJudgeTrackSourceEngineMetadata RedteamJudgeTrackSource = "engine_metadata"
	// RedteamJudgeTrackSourceLegacyDefault judge_track 与 engine 都缺失（U4 之前的报告）。
	RedteamJudgeTrackSourceLegacyDefault RedteamJudgeTrackSource = "legacy_default"
)

// RedteamJudgeTrackResolution 是一次 judge_track 解析的结果。
//
// Track 永远非空：即使元数据非法也会给出一个可渲染的兜底值，
// 这样报告永远打得开；但 Invalid 会如实记录被拒绝的原始值。
type RedteamJudgeTrackResolution struct {
	Track   RedteamJudgeTrack
	Source  RedteamJudgeTrackSource
	Invalid string
}

// NormalizeRedteamJudgeTrack 归一化并校验 judge_track 字面量。
//
// 空串返回 ("", nil) —— 表示「未声明」，由解析层按metadata.engine 推断。
// 任何非空且非platform/engine 的值返回错误，写路径必须 fail-closed。
func NormalizeRedteamJudgeTrack(raw string) (RedteamJudgeTrack, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	switch trimmed {
	case "":
		return "", nil
	case string(RedteamJudgeTrackPlatform):
		return RedteamJudgeTrackPlatform, nil
	case string(RedteamJudgeTrackEngine):
		return RedteamJudgeTrackEngine, nil
	default:
		return "", fmt.Errorf("invalid %s %q: expected %q or %q",
			RedteamJudgeTrackMetadataKey, raw, RedteamJudgeTrackPlatform, RedteamJudgeTrackEngine)
	}
}

// ValidateReportJudgeTrackMetadata 校验报告 metadata 里显式声明的 judge_track。
//
// 只做校验，不改写、不兜底：返回 ("", nil) 表示「未声明」，
// 由调用方决定是否推断。非法值返回错误，供写路径 fail-closed。
func ValidateReportJudgeTrackMetadata(metadata map[string]string) (RedteamJudgeTrack, error) {
	if len(metadata) == 0 {
		return "", nil
	}
	raw, ok := metadata[RedteamJudgeTrackMetadataKey]
	if !ok {
		return "", nil
	}
	return NormalizeRedteamJudgeTrack(raw)
}

// ResolveRedteamJudgeTrack 解析一份报告的判定口径（读路径）。
//
// 优先级：
//  1. metadata.judge_track 显式且合法 → 采用；
//  2. metadata.judge_track 显式但非法 → **不返回错误**（读路径要能打开旧报告），
//     记录 Invalid 并退回 platform，交给渲染层显式提示；
//  3. metadata.engine 非空（引擎链路的历史标记）→ engine；
//  4. 都没有（U4 之前的报告）→ platform。
//
// 第4 条的兜底依据：引擎链路从第一天起就写 metadata.engine，
// 而平台 Judge 链路从不写它。因此「engine 键缺失」等价于「平台链路」。
// 该等价关系只在**读**路径使用（渲染旧报告），写路径一律要求显式声明。
func ResolveRedteamJudgeTrack(metadata map[string]string) RedteamJudgeTrackResolution {
	if len(metadata) == 0 {
		return RedteamJudgeTrackResolution{Track: RedteamJudgeTrackPlatform, Source: RedteamJudgeTrackSourceLegacyDefault}
	}
	if raw, ok := metadata[RedteamJudgeTrackMetadataKey]; ok {
		track, err := NormalizeRedteamJudgeTrack(raw)
		if err == nil && track != "" {
			return RedteamJudgeTrackResolution{Track: track, Source: RedteamJudgeTrackSourceExplicit}
		}
		return RedteamJudgeTrackResolution{
			Track:   RedteamJudgeTrackPlatform,
			Source:  RedteamJudgeTrackSourceLegacyDefault,
			Invalid: strings.TrimSpace(raw),
		}
	}
	if strings.TrimSpace(metadata[redteamJudgeTrackEngineMetadataKey]) != "" {
		return RedteamJudgeTrackResolution{Track: RedteamJudgeTrackEngine, Source: RedteamJudgeTrackSourceEngineMetadata}
	}
	return RedteamJudgeTrackResolution{Track: RedteamJudgeTrackPlatform, Source: RedteamJudgeTrackSourceLegacyDefault}
}

// RedteamJudgeTrackOf 是resolve 的便捷包装，只取Track（供结构体字段赋值）。
func RedteamJudgeTrackOf(metadata map[string]string) RedteamJudgeTrack {
	return ResolveRedteamJudgeTrack(metadata).Track
}

const (
	judgeTrackCaptionPlatform = "判定口径：平台 Judge（platform）—— 风险等级与安全评分由平台侧判定链路计算。"
	judgeTrackCaptionEngine   = "判定口径：promptfoo 引擎（engine）—— 风险等级与安全评分由 promptfoo 引擎侧判定链路计算。"
	judgeTrackSuffixInferred  = "（本报告未记录该字段，按报告元数据推断。）"
	judgeTrackSuffixLegacy    = "（历史报告未记录判定口径，按平台侧判定链路呈现。）"
	judgeTrackSuffixInvalid   = "（报告元数据中的判定口径字段非法，已忽略并按平台侧判定链路呈现；请联系报告生成方核对。）"
	judgeTrackSuffixDistinct  = "两条链路的分数口径不同，不可直接横向比较。"

	// 页脚用的**短**口径句。完整句（judgeTrackCaption*）在 8pt 字号下宽度超过
	// 页脚可用宽度（425pt≈ 53 个汉字），会被截断成「…」—— 截断掉的正是
	// 「不可直接横向比较」这句最该被看见的警告。所以页脚不截断完整句，
	// 改用短句；完整句保留在报告基本信息段落（markdown / 导出）。
	judgeTrackFooterPlatform = "判定口径：平台 Judge（platform）· 分数口径与引擎链路不同，不可横向比较。"
	judgeTrackFooterEngine   = "判定口径：promptfoo 引擎（engine）· 分数口径与平台链路不同，不可横向比较。"
	judgeTrackFooterInvalid  = "判定口径未知（元数据非法）· 请联系报告生成方核对。"
)

// RedteamJudgeTrackCaption 渲染一句话口径说明，用于报告卡与 PDF 页脚。
//
// 措辞按来源分档，是为了让用户知道「这个口径有多可信」：
// 显式声明 > 推断 > 历史兜底。非法元数据必须显式提示，不能静默按platform 呈现。
func RedteamJudgeTrackCaption(resolution RedteamJudgeTrackResolution) string {
	base := judgeTrackCaptionPlatform
	if resolution.Track == RedteamJudgeTrackEngine {
		base = judgeTrackCaptionEngine
	}
	suffix := ""
	switch {
	case resolution.Invalid != "":
		suffix = judgeTrackSuffixInvalid
	case resolution.Source == RedteamJudgeTrackSourceExplicit:
		suffix = ""
	case resolution.Source == RedteamJudgeTrackSourceEngineMetadata:
		suffix = judgeTrackSuffixInferred
	default:
		suffix = judgeTrackSuffixLegacy
	}
	return base + suffix + judgeTrackSuffixDistinct
}

// RedteamJudgeTrackCaptionOf 是 caption 的便捷包装（直接吃 metadata）。
func RedteamJudgeTrackCaptionOf(metadata map[string]string) string {
	return RedteamJudgeTrackCaption(ResolveRedteamJudgeTrack(metadata))
}

// RedteamJudgeTrackFooterCaption 渲染**页脚专用**的短口径句。
//
// 与 RedteamJudgeTrackCaption 的区别只有长度，语义完全一致。
// 分成两个函数而不是给完整句加参数，是因为页脚空间是硬约束：
// 让渲染层自己判断「要不要截断」等于把「警告语被截掉」变成可选项。
//
// 非法元数据在页脚**必须**显示「判定口径未知」而不是 platform ——
// 页脚是每页都在的位置，在此处说谎会让整份 PDF 的口径标注都不可信。
func RedteamJudgeTrackFooterCaption(resolution RedteamJudgeTrackResolution) string {
	if resolution.Invalid != "" {
		return judgeTrackFooterInvalid
	}
	if resolution.Track == RedteamJudgeTrackEngine {
		return judgeTrackFooterEngine
	}
	return judgeTrackFooterPlatform
}

// RedteamJudgeTrackFooterCaptionOf 是页脚caption 的便捷包装（直接吃 metadata）。
func RedteamJudgeTrackFooterCaptionOf(metadata map[string]string) string {
	return RedteamJudgeTrackFooterCaption(ResolveRedteamJudgeTrack(metadata))
}
