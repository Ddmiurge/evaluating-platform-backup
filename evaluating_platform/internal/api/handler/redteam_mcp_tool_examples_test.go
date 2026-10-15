package handler

// redteam_mcp_tool_examples_test.go — 护栏：MCP 工具描述里举的例子必须是本环境**真能跑**的 id。
//
// # 这个测试防的是什么
//
// `run_promptfoo_redteam_evaluation` 的 `plugins` / `strategies` 字段描述是 MaClaw 唯一
// 能读到的「该传什么」的说明。它举的 id 会被当作建议直接传回来，所以一个跑不通的例子
// 就是平台在教 agent 自己把 run 弄坏 —— 而且失败点在提交边界（409）或整 run（非法插件
// id），用户看到的是「按提示选的却不让跑」。
//
// 这不是假设。修之前这两句话是：
//
//	plugins:    "…for example harmful, pii, prompt-injection, jailbreak."
//	strategies: "Optional strategy ids such as direct, role-play, encoding, multi-turn."
//
// 八个 id 里六个已经死了：`prompt-injection` / `jailbreak` 被 promptfoo 自己的
// `RedteamConfigSchema.safeParse` 拒绝（选中会让**整个 run** 失败，P2-1/P2-2），另外四个
// 是 P3-1 下线的平台自造策略。它们是**目录那两次改动的遗漏**：id 从目录里删了，描述没跟着改。
//
// 因此本测试做三件事：
//
//  1. 两个例子清单里的每个 id 都要过各自的可运行判据（与 `remoteGenerationHintRecommends`
//     同一把尺：用户今天选它，能拿到一次完整评测）；
//  2. 两个 id 位描述必须**逐字包含**由这两个清单渲染出的整段（描述由清单拼出来，不再是
//     手写散文 —— 这一条锁住这个事实，防止有人改回手写）；
//  3. 这两个 id 位描述**不得**出现任何已下线策略 / 非法插件 / 需配置插件 id（按 token 匹配，
//     所以 `jailbreak-templates` 不会被误判成非法的 `jailbreak`）。
//
// 第 3 条是反证点：把 `harmful` 换成 `jailbreak` → 红。
//
// 扫描范围**只限 id 位**（`run_promptfoo_redteam_evaluation` 的 plugins/strategies，
// 以及 `search_redteam_plugin_catalog` 的描述）。其余描述里的 "jailbreak"、"prompt injection"
// 是**自然语言检索词**（例如 `query` 字段的示例查询），不是 id，扫它们会得到假阳性。

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"evaluating_platform/internal/maclaw"
)

// toolPropertyDescription 取出某个工具某个入参的描述文本。
func toolPropertyDescription(t *testing.T, toolName, property string) string {
	t.Helper()
	for _, item := range redteamMCPToolDefinitions() {
		name, _ := item["name"].(string)
		if name != toolName {
			continue
		}
		schema, ok := item["inputSchema"].(gin.H)
		if !ok {
			t.Fatalf("tool %q has no inputSchema object", toolName)
		}
		properties, ok := schema["properties"].(gin.H)
		if !ok {
			t.Fatalf("tool %q has no properties object", toolName)
		}
		prop, ok := properties[property].(gin.H)
		if !ok {
			t.Fatalf("tool %q has no %q property", toolName, property)
		}
		description, ok := prop["description"].(string)
		if !ok {
			t.Fatalf("tool %q property %q has no description", toolName, property)
		}
		return description
	}
	t.Fatalf("tool %q not found in tools/list", toolName)
	return ""
}

// toolDescription 取出某个工具的顶层描述文本。
func toolDescription(t *testing.T, toolName string) string {
	t.Helper()
	for _, item := range redteamMCPToolDefinitions() {
		if name, _ := item["name"].(string); name == toolName {
			description, _ := item["description"].(string)
			return description
		}
	}
	t.Fatalf("tool %q not found in tools/list", toolName)
	return ""
}

// descriptionNamesID 报告一段文本是否把 id 当作**独立 token** 提到。
//
// 两侧都要求不是 id 字符（`[a-z0-9:_-]`），因此 `jailbreak-templates` 不会命中 `jailbreak`，
// `prompt_injection` 不会命中 `prompt-injection`。这是本测试要的语义：非法 id 只有在被
// 单独列出时才算「平台在推荐它」。
func descriptionNamesID(text, id string) bool {
	pattern := `(^|[^a-z0-9:_-])` + regexp.QuoteMeta(strings.ToLower(id)) + `([^a-z0-9:_-]|$)`
	return regexp.MustCompile(pattern).MatchString(strings.ToLower(text))
}

// TestRedteamToolExampleIDsAreUsable 钉住两个例子清单的可运行性、描述与清单的一致性，
// 以及 id 位描述里不出现已失效 id。
func TestRedteamToolExampleIDsAreUsable(t *testing.T) {
	if len(redteamToolExamplePluginIDs) == 0 || len(redteamToolExampleStrategyIDs) == 0 {
		t.Fatal("example id lists are empty; the descriptions would advertise nothing")
	}

	// (1a) 插件例子：合法 + 本环境可完整评测 + 不需要平台不收集的 config。
	legalLocal := toSet(maclaw.LegalAndLocalPluginIDs())
	configGated := toSet(maclaw.RequiresConfigPluginIDs())
	for _, id := range redteamToolExamplePluginIDs {
		if !legalLocal[id] {
			t.Errorf("plugin example %q is not in LegalAndLocalPluginIDs(); the description "+
				"recommends a plugin whose run cannot complete here", id)
		}
		if configGated[id] {
			t.Errorf("plugin example %q needs config the platform does not collect; "+
				"selecting it generates 0 cases and the run fails", id)
		}
	}

	// (1b) 策略例子：已提供 + 未被远程闸拦 + 引擎认 + 本部署可选 + 未下线。
	offered := toSet(maclaw.PromptfooEngineSupportedStrategyIDs())
	for _, id := range redteamToolExampleStrategyIDs {
		if _, retired := maclaw.PromptfooRetiredStrategyGuidance(id); retired {
			t.Errorf("strategy example %q is recorded as withdrawn; the description would "+
				"recommend an id the submit gate rejects", id)
		}
		if !offered[id] {
			t.Errorf("strategy example %q is not offered by PromptfooEngineSupportedStrategyIDs()", id)
		}
		if maclaw.StrategyRequiresRemoteGeneration(id) {
			t.Errorf("strategy example %q requires remote generation, which is fail-closed here", id)
		}
		native := maclaw.StrategyNativeID(id)
		if native == "" {
			t.Errorf("strategy example %q resolves to no promptfoo id; the engine cannot forward it", id)
		} else if !maclaw.StrategyIsSelectable(native) {
			t.Errorf("strategy example %q resolves to %q, which the strategy catalogue marks unselectable", id, native)
		}
	}

	// (2) 描述由清单拼出，且包含整段渲染结果。
	pluginDescription := toolPropertyDescription(t, "run_promptfoo_redteam_evaluation", "plugins")
	strategyDescription := toolPropertyDescription(t, "run_promptfoo_redteam_evaluation", "strategies")
	if want := toolExampleIDs(redteamToolExamplePluginIDs); !strings.Contains(pluginDescription, want) {
		t.Errorf("plugins description does not contain the example list %q:\n%s", want, pluginDescription)
	}
	if want := toolExampleIDs(redteamToolExampleStrategyIDs); !strings.Contains(strategyDescription, want) {
		t.Errorf("strategies description does not contain the example list %q:\n%s", want, strategyDescription)
	}

	// (3) id 位描述里不得出现任何已失效 id。
	idBearing := map[string]string{
		"run_promptfoo_redteam_evaluation.plugins":    pluginDescription,
		"run_promptfoo_redteam_evaluation.strategies": strategyDescription,
		"search_redteam_plugin_catalog.description":   toolDescription(t, "search_redteam_plugin_catalog"),
	}
	dead := make([]string, 0, 8)
	dead = append(dead, maclaw.IllegalPluginIDs()...)
	dead = append(dead, maclaw.RequiresConfigPluginIDs()...)
	for _, entry := range maclaw.PromptfooRetiredStrategies() {
		dead = append(dead, entry.ID)
	}
	for _, entry := range maclaw.PromptfooRetiredPlugins() {
		dead = append(dead, entry.ID)
	}
	for _, id := range dead {
		for where, text := range idBearing {
			if descriptionNamesID(text, id) {
				t.Errorf("%s names %q, which this deployment cannot run; a tool description "+
					"must only suggest ids that work:\n%s", where, id, text)
			}
		}
	}
}

// toSet 把字符串切片转成集合。
func toSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}
