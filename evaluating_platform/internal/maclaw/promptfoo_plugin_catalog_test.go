package maclaw

// promptfoo_plugin_catalog_test.go — L1 catalog expansion coverage:
// plugin count, category metadata, and search hits for the new entries.

import "testing"

func TestPromptfooPluginCatalogL1Expansion(t *testing.T) {
	ids := PromptfooEngineSupportedPluginIDs()
	if len(ids) < 100 {
		t.Fatalf("plugin count = %d, want >= 100", len(ids))
	}
	byID := map[string]bool{}
	for _, id := range ids {
		byID[id] = true
	}
	for _, want := range []string{
		// harmful family
		"harmful", "harmful:cybercrime", "harmful:illegal-activities", "harmful:violent-crime",
		"harmful:child-exploitation", "harmful:chemical-biological-weapons", "harmful:weapons:ied",
		"harmful:illegal-drugs:meth", "harmful:radicalization", "harmful:misinformation-disinformation",
		// privacy / injection family
		"pii", "pii:direct", "pii:social", "pii:api-db", "pii:session", "cross-session-leak",
		"indirect-prompt-injection", "ascii-smuggling", "excessive-agency",
		"hijacking", "system-prompt-override", "prompt-extraction", "mcp", "agentic:memory-poisoning",
		// core
		"bias", "bias:age", "bias:gender", "bias:race", "bias:disability",
		"reasoning-dos", "hallucination", "overreliance", "divergent-repetition",
		"off-topic", "wordplay", "competitors", "imitation",
		"politics", "religion", "teen-safety:age-restricted-goods-and-services",
		"contracts", "policy",
		// dataset benchmarks
		"beavertails", "harmbench", "pliny", "donotanswer", "cyberseceval", "xstest",
		// industry suites
		"medical:hallucination", "medical:fda:cyber-access-control",
		"pharmacy:dosage-calculation", "pharmacy:drug-interaction",
		"financial:hallucination", "financial:sox-compliance", "financial:misconduct",
		"insurance:phi-disclosure", "insurance:coverage-discrimination",
		"telecom:cpni-disclosure", "telecom:account-takeover", "telecom:tcpa-violation",
		"realestate:fair-housing-discrimination", "realestate:steering",
		"ecommerce:pci-dss", "ecommerce:order-fraud",
	} {
		if !byID[want] {
			t.Fatalf("missing plugin id %q (catalog has %d entries)", want, len(ids))
		}
	}

	// P2-1 · L1：三个 promptfoo 不接受的 id 已移出目录。
	//
	// 断言「不在目录里」与断言「在目录里」同等重要：这三个 id 曾经就在这里，
	// 而它们被选中时整 run 必然失败（promptfoo zod 拒绝，不是降级）。
	// 反证：把任意一个塞回 promptfooPluginCatalog → 本测试红。
	for _, gone := range IllegalPluginIDs() {
		if byID[gone] {
			t.Errorf("已下线的非法 plugin id %q 仍在目录里；选中它的 run 必然失败", gone)
		}
		// 而它必须仍然**可被解释**：移出只解决「会报错」，不解决
		// 「用户要的能力去哪了」。没有引导的删除会被当成功能退化。
		if _, ok := PromptfooRetiredPluginGuidance(gone); !ok {
			t.Errorf("非法 plugin id %q 已移出目录，但 retiredPluginCatalog 没有记录替代路径", gone)
		}
	}
}

func TestPromptfooStrategyCatalogL1Expansion(t *testing.T) {
	ids := PromptfooEngineSupportedStrategyIDs()
	if len(ids) < 25 {
		t.Fatalf("strategy count = %d, want >= 25", len(ids))
	}
	byID := map[string]bool{}
	for _, id := range ids {
		byID[id] = true
	}
	// Phase 3 开篇 · 任务 1: `simba`、`retry`、`mathPrompt` are no longer
	// required, because they are no longer offered — all three fail or do
	// nothing when forwarded (see promptfoo_plugin_catalog.go for the measured
	// reasons). Their absence is asserted below so a future edit cannot quietly
	// re-add a strategy that cannot run.
	for _, want := range []string{
		"direct", "role-play", "encoding", "multi-turn",
		"crescendo", "assumed-knowledge", "few-shot", "homoglyph", "many-shot", "translate",
		"base64", "rot13", "hex", "leetspeak", "otherEncodings",
		"goat", "hydra", "goblin", "iterative", "iterative:tree", "iterative:meta",
		"bestOfN", "likert", "citation",
		"mischievousUser", "authoritativeMarkupInjection", "singleTurnComposite",
		// Newly reachable through discovery.
		"morse", "piglatin", "camelcase", "emoji", "jailbreak-templates", "layer",
	} {
		if !byID[want] {
			t.Fatalf("missing strategy id %q in %v", want, ids)
		}
	}
	for _, withdrawn := range []string{"simba", "retry", "mathPrompt"} {
		if byID[withdrawn] {
			t.Errorf("strategy %q is offered again, but it provably cannot run here "+
				"(simba: deprecated no-op; retry: needs targetIds; mathPrompt: needs an OpenAI key)", withdrawn)
		}
	}
}

func TestPromptfooPluginCatalogCategoryMetadata(t *testing.T) {
	cards := PromptfooPluginCatalogCards()
	if len(cards) == 0 {
		t.Fatal("no catalog cards")
	}
	cases := map[string]string{
		"promptfoo_plugin:harmful:cybercrime":                            "harmful",
		"promptfoo_plugin:ascii-smuggling":                               "injection",
		"promptfoo_plugin:excessive-agency":                              "injection",
		"promptfoo_plugin:hallucination":                                 "hallucination",
		"promptfoo_plugin:off-topic":                                     "off-topic",
		"promptfoo_plugin:competitors":                                   "brand",
		"promptfoo_plugin:security-exploit":                              "security-exploit",
		"promptfoo_plugin:politics":                                      "sensitive",
		"promptfoo_plugin:teen-safety:age-restricted-goods-and-services": "sensitive",
		"promptfoo_plugin:beavertails":                                   "dataset",
		"promptfoo_plugin:harmbench":                                     "dataset",
		"promptfoo_plugin:medical:hallucination":                         "industry",
		"promptfoo_plugin:financial:sox-compliance":                      "industry",
		"promptfoo_plugin:telecom:cpni-disclosure":                       "industry",
		"promptfoo_plugin:realestate:steering":                           "industry",
		"promptfoo_plugin:ecommerce:pci-dss":                             "industry",
		"promptfoo_plugin:cross-session-leak":                            "privacy",
	}
	for _, card := range cards {
		want, ok := cases[card.SourceRef]
		if !ok {
			continue
		}
		if got := card.SafeMetadata["category"]; got != want {
			t.Fatalf("%s category = %q, want %q", card.SourceRef, got, want)
		}
		if card.SafeMetadata["plugin_id"] == "" || card.SafeMetadata["default_severity"] == "" {
			t.Fatalf("%s missing plugin_id/default_severity metadata: %v", card.SourceRef, card.SafeMetadata)
		}
	}
}

func TestPromptfooPluginCatalogSeverityTiers(t *testing.T) {
	severity := map[string]string{}
	for _, entry := range promptfooPluginCatalog {
		severity[entry.ID] = entry.DefaultSev
	}
	for id, want := range map[string]string{
		"harmful:child-exploitation":  "critical",
		"harmful:weapons:ied":         "critical",
		"medical:hallucination":       "critical",
		"pharmacy:dosage-calculation": "critical",
		"harmful":                     "high",
		"pii":                         "high",
		"telecom:cpni-disclosure":     "high",
		"ecommerce:pci-dss":           "high",
		"bias":                        "medium",
		"realestate:steering":         "high",
		"xstest":                      "low",
	} {
		if severity[id] != want {
			t.Fatalf("%s severity = %q, want %q", id, severity[id], want)
		}
	}
}

func TestSearchPromptfooPluginCatalogNewEntries(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		wantHit string
	}{
		{"cybercrime chinese", "网络犯罪", "promptfoo_plugin:harmful:cybercrime"},
		{"ascii smuggling id", "ascii-smuggling", "promptfoo_plugin:ascii-smuggling"},
		{"excessive agency chinese", "越权", "promptfoo_plugin:excessive-agency"},
		{"hallucination chinese", "幻觉", "promptfoo_plugin:hallucination"},
		{"competitors id", "competitors", "promptfoo_plugin:competitors"},
		{"indirect injection id", "indirect-prompt-injection", "promptfoo_plugin:indirect-prompt-injection"},
		{"medical chinese", "医疗", "promptfoo_plugin:medical:hallucination"},
		{"financial chinese", "金融", "promptfoo_plugin:financial:hallucination"},
		{"telecom chinese", "电信", "promptfoo_plugin:telecom:cpni-disclosure"},
		{"housing chinese", "住房", "promptfoo_plugin:realestate:fair-housing-discrimination"},
		{"benchmark chinese", "基准", "promptfoo_plugin:beavertails"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cards := SearchPromptfooPluginCatalog(tc.query, 30)
			refs := map[string]bool{}
			for _, card := range cards {
				refs[card.SourceRef] = true
			}
			if !refs[tc.wantHit] {
				t.Fatalf("query %q: expected %s in results, got %v", tc.query, tc.wantHit, refs)
			}
		})
	}
}

// TestPromptfooEngineCatalogBuilder verifies the U2/T2.2 catalog the BFF ships
// to the engine: every plugin gets a complete category/label/severity, labels
// come from PromptfooCategoryLabel, and the specific values the engine's
// catalogClassification.test.ts mirrors are pinned here too (so the two sides
// cannot drift).
func TestPromptfooEngineCatalogBuilder(t *testing.T) {
	catalog := PromptfooEngineCatalog()
	if catalog == nil {
		t.Fatal("PromptfooEngineCatalog() returned nil")
	}
	if len(catalog.Plugins) != len(promptfooPluginCatalog) {
		t.Fatalf("catalog plugins = %d, want %d", len(catalog.Plugins), len(promptfooPluginCatalog))
	}
	if len(catalog.Strategies) != len(promptfooStrategyCatalog) {
		t.Fatalf("catalog strategies = %d, want %d", len(catalog.Strategies), len(promptfooStrategyCatalog))
	}

	byID := make(map[string]EngineCatalogPlugin, len(catalog.Plugins))
	for _, plugin := range catalog.Plugins {
		if plugin.ID == "" || plugin.Category == "" || plugin.CategoryLabel == "" || plugin.Severity == "" {
			t.Fatalf("incomplete catalog entry: %+v", plugin)
		}
		if want := PromptfooCategoryLabel(plugin.Category); plugin.CategoryLabel != want {
			t.Fatalf("%s label = %q, want %q", plugin.ID, plugin.CategoryLabel, want)
		}
		byID[plugin.ID] = plugin
	}

	// Values mirrored by promptfoo_engine/tests/catalogClassification.test.ts.
	for id, want := range map[string][2]string{
		"harmful:cybercrime":          {"harmful", "critical"},
		"medical:hallucination":       {"industry", "critical"},
		"pharmacy:dosage-calculation": {"industry", "critical"},
		"harmful":                     {"harmful", "high"},
		"telecom:cpni-disclosure":     {"industry", "high"},
		"beavertails":                 {"dataset", "high"},
		"hallucination":               {"hallucination", "medium"},
		"competitors":                 {"brand", "low"},
		"wordplay":                    {"off-topic", "low"},
		"xstest":                      {"dataset", "low"},
		"teen-safety:age-restricted-goods-and-services": {"sensitive", "high"},
	} {
		got, ok := byID[id]
		if !ok {
			t.Fatalf("catalog missing %s", id)
		}
		if got.Category != want[0] || got.Severity != want[1] {
			t.Fatalf("%s = (%s, %s), want (%s, %s)", id, got.Category, got.Severity, want[0], want[1])
		}
	}

	if got := PromptfooCategoryLabel("other"); got != "其他" {
		t.Fatalf("other label = %q, want 其他", got)
	}
	if got := PromptfooCategoryLabel("unmapped-category"); got != "unmapped-category" {
		t.Fatalf("unmapped label = %q, want the key itself", got)
	}
}
