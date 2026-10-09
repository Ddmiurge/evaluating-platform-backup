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
		"prompt-injection", "indirect-prompt-injection", "ascii-smuggling", "excessive-agency",
		"hijacking", "system-prompt-override", "prompt-extraction", "mcp", "agentic:memory-poisoning",
		// core
		"jailbreak", "bias", "bias:age", "bias:gender", "bias:race", "bias:disability",
		"security-exploit", "reasoning-dos", "hallucination", "overreliance", "divergent-repetition",
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
	for _, want := range []string{
		"direct", "role-play", "encoding", "multi-turn",
		"crescendo", "assumed-knowledge", "few-shot", "homoglyph", "many-shot", "translate",
		"base64", "rot13", "hex", "leetspeak", "otherEncodings",
		"goat", "hydra", "goblin", "simba", "iterative", "iterative:tree", "iterative:meta",
		"bestOfN", "retry", "likert", "citation", "mathPrompt",
		"mischievousUser", "authoritativeMarkupInjection", "singleTurnComposite",
	} {
		if !byID[want] {
			t.Fatalf("missing strategy id %q in %v", want, ids)
		}
	}
}

func TestPromptfooPluginCatalogCategoryMetadata(t *testing.T) {
	cards := PromptfooPluginCatalogCards()
	if len(cards) == 0 {
		t.Fatal("no catalog cards")
	}
	cases := map[string]string{
		"promptfoo_plugin:harmful:cybercrime":            "harmful",
		"promptfoo_plugin:ascii-smuggling":               "injection",
		"promptfoo_plugin:excessive-agency":              "injection",
		"promptfoo_plugin:hallucination":                 "hallucination",
		"promptfoo_plugin:off-topic":                     "off-topic",
		"promptfoo_plugin:competitors":                   "brand",
		"promptfoo_plugin:security-exploit":              "security-exploit",
		"promptfoo_plugin:politics":                      "sensitive",
		"promptfoo_plugin:teen-safety:age-restricted-goods-and-services": "sensitive",
		"promptfoo_plugin:beavertails":                   "dataset",
		"promptfoo_plugin:harmbench":                     "dataset",
		"promptfoo_plugin:medical:hallucination":         "industry",
		"promptfoo_plugin:financial:sox-compliance":      "industry",
		"promptfoo_plugin:telecom:cpni-disclosure":       "industry",
		"promptfoo_plugin:realestate:steering":           "industry",
		"promptfoo_plugin:ecommerce:pci-dss":             "industry",
		"promptfoo_plugin:cross-session-leak":            "privacy",
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
		"harmful:child-exploitation": "critical",
		"harmful:weapons:ied":        "critical",
		"medical:hallucination":      "critical",
		"pharmacy:dosage-calculation": "critical",
		"harmful":                    "high",
		"pii":                        "high",
		"telecom:cpni-disclosure":    "high",
		"ecommerce:pci-dss":          "high",
		"bias":                       "medium",
		"realestate:steering":        "high",
		"xstest":                     "low",
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
