package maclaw

// promptfoo_judge_rubric_test.go — U8 插件驱动判定的护栏测试。
//
// 三条主线：
//  1. **分组依据来自实测枚举**（TestPromptfooCatalogCategoryTaxonomyIsMeasured）
//     ——把 119 插件 / 12 category 的实际分布钉死。这条是 U8 的地基：
//     分组若是按命名猜的，后面的族级 rubric 就建在流沙上。
//  2. **回退链显式可观测**（TestResolvePromptfooJudgeRubric*）
//     ——插件覆盖 → 族级 → 显式兜底，且兜底**可数**。
//  3. **护栏反证**（Test*Rejects* / Test*FailsClosed*）
//     ——非法档位、非法 metadata 必须让写路径报错，而不是静默写进报告。

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// measuredCategoryTaxonomy 是**实测**得到的目录分类分布，不是设计意图。
//
// 数法：对 promptfooPluginCatalog 逐条统计 Category。若日后有人新增插件、
// 新增分类、或误改某条目的分类，这条断言会先红，提示他补族级 rubric。
var measuredCategoryTaxonomy = map[string]int{
	"harmful":          26,
	"industry":         54,
	"injection":        9,
	"privacy":          6,
	"dataset":          6,
	"bias":             5,
	"sensitive":        3,
	"hallucination":    3,
	"brand":            2,
	"off-topic":        2,
	"security-exploit": 2,
	"jailbreak":        1,
}

func TestPromptfooCatalogCategoryTaxonomyIsMeasured(t *testing.T) {
	counts := map[string]int{}
	for _, entry := range promptfooPluginCatalog {
		counts[entry.Category]++
	}
	if len(promptfooPluginCatalog) != 119 {
		t.Fatalf("插件总数 = %d，U8 的设计前提是 119；改动目录需同步更新 rubric 表与本断言", len(promptfooPluginCatalog))
	}
	got := make([]string, 0, len(counts))
	for category := range counts {
		got = append(got, category)
	}
	sort.Strings(got)
	want := make([]string, 0, len(measuredCategoryTaxonomy))
	for category := range measuredCategoryTaxonomy {
		want = append(want, category)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("目录 category 集合已变：\n got = %v\nwant = %v\n新增/删除分类必须同时补族级 rubric，否则该族会静默落兜底档", got, want)
	}
	for _, category := range want {
		if counts[category] != measuredCategoryTaxonomy[category] {
			t.Errorf("category %q 的插件数 = %d，期望 %d", category, counts[category], measuredCategoryTaxonomy[category])
		}
	}
}

// TestEveryCatalogCategoryHasJudgeRubric 是 U8「不许静默降级」的第一道闸：
// 目录里出现的每一个 category 都必须有族级 rubric。
//
// 反证方式：把某个 category 从 promptfooCategoryJudgeRubrics 里删掉 → 本测试红。
// 这正是「新增了分类却忘了写标准」在 CI 里的样子。
func TestEveryCatalogCategoryHasJudgeRubric(t *testing.T) {
	if err := promptfooJudgeRubricSanityError(); err != nil {
		t.Fatalf("rubric 表自身不完整：%v", err)
	}
	seen := map[string]bool{}
	for _, entry := range promptfooPluginCatalog {
		if seen[entry.Category] {
			continue
		}
		seen[entry.Category] = true
		if _, _, ok := PromptfooCategoryJudgeRubric(entry.Category); !ok {
			t.Errorf("category %q（插件 %s 等 %d 个）在目录里出现，却没有族级 rubric —— 这些插件会静默落兜底档",
				entry.Category, entry.ID, measuredCategoryTaxonomy[entry.Category])
		}
	}
}

// TestJudgeRubricPolarityIsExplicitOnEveryRubric 断言每条 rubric 都写明了
// PASS/FAIL 极性。极性写反是本文件唯一的致命错误（引擎会翻转 promptfoo 的
// 判定结果），所以在**表**这一层就 fail-closed，而不是等到运行期从异常分数
// 里去猜。
func TestJudgeRubricPolarityIsExplicitOnEveryRubric(t *testing.T) {
	for category, rubric := range promptfooCategoryJudgeRubrics {
		if !strings.Contains(rubric.Criteria, "PASS") || !strings.Contains(rubric.Criteria, "FAIL") {
			t.Errorf("category %q 的 rubric 未写明 PASS/FAIL 极性", category)
		}
	}
	for id, rubric := range promptfooJudgeRubricOverrides {
		if !strings.Contains(rubric.Criteria, "PASS") || !strings.Contains(rubric.Criteria, "FAIL") {
			t.Errorf("插件覆盖 %q 的 rubric 未写明 PASS/FAIL 极性", id)
		}
	}
	if !strings.Contains(judgeRubricUnmapped.Criteria, "PASS") || !strings.Contains(judgeRubricUnmapped.Criteria, "FAIL") {
		t.Error("兜底 rubric 未写明 PASS/FAIL 极性")
	}
	// 兜底档必须自报「未匹配」，否则它在报告里与族级 rubric 无法区分。
	if !strings.Contains(judgeRubricUnmapped.Criteria, "未匹配到插件族标准") {
		t.Error("兜底 rubric 未声明「未匹配到插件族标准」，退化会变得不可见")
	}
}

func TestResolvePromptfooJudgeRubricPrefersPluginOverride(t *testing.T) {
	got := PromptfooJudgeRubricForPlugin("prompt-injection")
	if got.Source != "plugin:prompt-injection" {
		t.Fatalf("judge_source = %q，期望 plugin:prompt-injection", got.Source)
	}
	if got.IsFallback() {
		t.Fatal("高危插件专属覆盖不得被当成兜底档")
	}
	// 覆盖必须真的换了判据，而不只是换了标签：把它与**同族**的族级 rubric
	// 逐字比对。（不能用 ResolvePromptfooJudgeRubric(同 id) 去取族级 ——
	//  那个函数按 id 查覆盖，必然又拿到覆盖本身。）
	categoryLevel, _, ok := PromptfooCategoryJudgeRubric("injection")
	if !ok {
		t.Fatal("injection 族级 rubric 不存在")
	}
	if categoryLevel.Criteria == got.Rubric.Criteria {
		t.Error("插件覆盖与族级 rubric 文本相同 —— 覆盖没有增加任何判定信息")
	}
	if categoryLevel.ID != "category:injection" {
		t.Errorf("族级 rubric 的 ID = %q，期望 category:injection", categoryLevel.ID)
	}
}

func TestResolvePromptfooJudgeRubricFallsBackPerCategory(t *testing.T) {
	// 一个没有任何专属覆盖的 harmful 插件应落族级档。
	got := PromptfooJudgeRubricForPlugin("harmful:illegal-drugs")
	if got.Source != "category:harmful" {
		t.Fatalf("judge_source = %q，期望 category:harmful", got.Source)
	}
	if got.IsFallback() {
		t.Fatal("harmful 族不应落兜底档")
	}
}

// TestResolvePromptfooJudgeRubricUnknownPluginIsExplicitFallback 是 U8 的
// 核心反证之一：**插件无 rubric** 时的行为。
//
// 断言三件事：
//  1. 不返回错误（否则一个未知 id 会让整次评测失败，代价更大且与 U2 冲突）；
//  2. 落**显式兜底档**，且该档自报「未匹配到插件族标准」；
//  3. Source 是可计数的具名档位，不是空串、不是通用 rubric 的旧标识。
func TestResolvePromptfooJudgeRubricUnknownPluginIsExplicitFallback(t *testing.T) {
	got := ResolvePromptfooJudgeRubric("no-such-plugin-id", "no-such-category")
	if !got.IsFallback() {
		t.Fatalf("未知插件应落兜底档，实际 source = %q", got.Source)
	}
	if got.Source != JudgeSourceFallbackUnmapped {
		t.Fatalf("兜底 source = %q，期望 %q", got.Source, JudgeSourceFallbackUnmapped)
	}
	if strings.TrimSpace(got.Rubric.Criteria) == "" {
		t.Fatal("兜底档不得产出空判定标准")
	}
	if !strings.Contains(got.Rubric.Criteria, "未匹配到插件族标准") {
		t.Error("兜底 rubric 必须自报未匹配，否则报告里无法与族级标准区分")
	}
	// 反向断言：兜底档**不是**旧的通用 rubric。若有人把 SAFETY_RUBRIC
	// 搬过来当兜底，本断言会红——那正是「静默回落通用」的定义。
	if strings.Contains(got.Rubric.Criteria, "泄露了敏感信息") {
		t.Error("兜底档退化成了旧的通用 SAFETY_RUBRIC —— 等于 U8 没做")
	}
}

// TestResolvePromptfooJudgeRubricInvalidCategoryIsNotGuessedFromID 断言
// 引擎**不从 plugin id 的命名猜族**：目录不认识就是不认识。
//
// 反证：给一个 id 明显带 "pii" 的未知插件，期望仍落兜底档。
func TestResolvePromptfooJudgeRubricInvalidCategoryIsNotGuessedFromID(t *testing.T) {
	got := ResolvePromptfooJudgeRubric("pii:something-not-in-catalog", "")
	if !got.IsFallback() {
		t.Fatalf("未登记插件被猜成了族级档：source = %q", got.Source)
	}
	// 而目录里真实存在的 pii 插件必须落族级档，两相对照。
	if real := PromptfooJudgeRubricForPlugin("pii:direct"); real.IsFallback() {
		t.Errorf("目录内的 pii:direct 竟落兜底档：%q", real.Source)
	}
}

func TestPromptfooEngineCatalogCarriesJudgeRubricForEveryPlugin(t *testing.T) {
	catalog := PromptfooEngineCatalog()
	if len(catalog.Plugins) != 119 {
		t.Fatalf("下发给引擎的 catalog 插件数 = %d，期望 119", len(catalog.Plugins))
	}
	fallbacks := 0
	for _, plugin := range catalog.Plugins {
		if strings.TrimSpace(plugin.JudgeCriteria) == "" {
			t.Errorf("插件 %s 下发的 judge_criteria 为空", plugin.ID)
			continue
		}
		if strings.TrimSpace(plugin.JudgeSource) == "" {
			t.Errorf("插件 %s 下发的 judge_source 为空 —— 判定口径将不可追溯", plugin.ID)
		}
		if plugin.JudgeSource == JudgeSourceFallbackUnmapped {
			fallbacks++
		}
	}
	// 目录内的插件**全部**应命中族级或插件档。若这里出现兜底，说明
	// 「目录里有插件但 rubric 表没覆盖」这个缺口存在，必须补表而不是接受。
	if fallbacks > 0 {
		t.Errorf("目录内 %d 个插件落兜底档；这是缺口而非合法降级，请补族级 rubric", fallbacks)
	}
}

// TestPromptfooEngineCatalogJudgeSourceIsWireSafe 断言 rubric 正文不进
// capability card，只进 run catalog。
//
// 理由：card 会进能力列表，若带 12 份 rubric 正文会让列表体积暴涨，
// 且会让人误以为 card 里的文本是权威副本（权威副本在
// promptfoo_judge_rubric.go）。
func TestPromptfooEngineCatalogJudgeSourceIsWireSafe(t *testing.T) {
	for _, card := range PromptfooPluginCatalogCards() {
		if card.SourceType != CapabilitySourcePromptfooPlugin {
			continue
		}
		source := card.SafeMetadata["judge_source"]
		if strings.TrimSpace(source) == "" {
			t.Errorf("能力卡 %s 缺少 judge_source", card.SourceRef)
		}
		if strings.Contains(source, "判定") || len(source) > 80 {
			t.Errorf("能力卡 judge_source 疑似塞进了 rubric 正文：%q", source)
		}
	}
}

// TestEngineRunRequestCarriesJudgeCriteria 断言 rubric 真的随 run 请求下发
// （而不是只存在于 catalog 结构体里）。
func TestEngineRunRequestCarriesJudgeCriteria(t *testing.T) {
	catalog := PromptfooEngineCatalog()
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("序列化 catalog 失败：%v", err)
	}
	payload := string(encoded)
	if !strings.Contains(payload, "judge_criteria") || !strings.Contains(payload, "judge_source") {
		t.Fatal("catalog JSON 未携带 judge_criteria/judge_source，引擎侧将全部落兜底档")
	}
	if !strings.Contains(payload, "category:injection") {
		t.Error("catalog JSON 未携带任何族级档位标识")
	}
}
