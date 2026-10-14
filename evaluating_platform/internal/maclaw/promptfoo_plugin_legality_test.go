package maclaw

// promptfoo_plugin_legality_test.go — P2-1 guardrail integrity assertions.
//
// # What this file protects
//
// U6 (promptfoo_remote_dep.go) answered "does this plugin need remote
// generation". It never answered "does this plugin EXIST". Three of its 29
// unmarked ids were not promptfoo plugin ids at all, so the platform offered
// options whose only possible outcome was a failed run:
//
//	prompt-injection   platform-invented
//	jailbreak          a promptfoo STRATEGY, not a plugin
//	security-exploit   platform-invented
//
// P2-1 withdraws them and records where each capability moved. Those two facts
// — the verdict table and the withdrawal record — are only worth anything if
// they stay true, and they can rot in three ways:
//
//  1. promptfoo is upgraded and its accepted-id set changes → the embedded
//     table describes a build nobody runs.
//  2. someone re-adds a rejected id to the catalog → the platform offers a
//     landmine again, and nothing goes red.
//  3. someone withdraws an id without recording a replacement → the option
//     disappears and the user is left with no explanation.
//
// The assertions below close all three. They are cheap (no network, no Node)
// and run in the ordinary `go test ./...` CI job.
//
// # A note on scope
//
// Like the U6 tests, these verify INTERNAL CONSISTENCY plus declared
// provenance. They cannot re-derive the table from an installed promptfoo,
// because the backend CI job does not install promptfoo_engine's node_modules.
// The value-level re-derivation is the generator's job:
//
//	node scripts/gen_plugin_legality.mjs && git diff --exit-code \
//	  internal/maclaw/testdata/promptfoo_plugin_legality.json

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// promptfooPluginLegalityPath resolves the committed table relative to this test
// file, so the assertions work from any `go test` working directory.
func promptfooPluginLegalityPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed to resolve the test file path")
	}
	return filepath.Join(filepath.Dir(thisFile), "testdata", "promptfoo_plugin_legality.json")
}

// readPluginLegalityTable parses the committed table fresh (not via the
// embedded copy) so these tests inspect the file a reviewer edits.
func readPluginLegalityTable(t *testing.T) pluginLegalityTable {
	t.Helper()
	raw, err := os.ReadFile(promptfooPluginLegalityPath(t))
	if err != nil {
		t.Fatalf("read legality table: %v", err)
	}
	var table pluginLegalityTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse legality table %s: %v", promptfooPluginLegalityPath(t), err)
	}
	return table
}

// TestPromptfooPluginLegalityTableMatchesCatalog is the anti-drift anchor: the
// artifact's self-reported totals must equal what the compiled-in catalog
// actually implies. A stale artifact that still certified a withdrawn id would
// otherwise make the guardrail look healthy while the catalog drifted.
func TestPromptfooPluginLegalityTableMatchesCatalog(t *testing.T) {
	table := readPluginLegalityTable(t)
	stats := PromptfooPluginLegalityStatsForCatalog()

	if got, want := table.CatalogTotals.Plugins, len(promptfooPluginCatalog); got != want {
		t.Errorf("table declares %d catalog plugins, catalog has %d — regenerate with `node scripts/gen_plugin_legality.mjs`", got, want)
	}
	if got, want := table.CatalogTotals.Plugins, stats.CatalogPlugins; got != want {
		t.Errorf("table declares %d plugins, stats computed %d", got, want)
	}
	if got, want := table.CatalogTotals.Legal, stats.Legal; got != want {
		t.Errorf("table declares %d legal ids, stats computed %d", got, want)
	}
	if got, want := table.CatalogTotals.LocalOnlyLegal, stats.LocalOnlyLegal; got != want {
		t.Errorf("table declares %d local-only legal ids, stats computed %d", got, want)
	}
	if got, want := table.CatalogTotals.RemoteLegal, stats.RemoteLegal; got != want {
		t.Errorf("table declares %d remote-gated legal ids, stats computed %d", got, want)
	}
	if got, want := table.CatalogTotals.RequiresConfig, stats.RequiresConfig; got != want {
		t.Errorf("table declares %d config-gated ids, stats computed %d", got, want)
	}
	if got, want := table.CatalogTotals.Illegal, stats.Retired; got != want {
		t.Errorf("table records %d withdrawn ids, IllegalPluginIDs() returns %d", got, want)
	}

	// The number that must be zero. `catalog_illegal` is the count of LIVE
	// catalog entries promptfoo rejects; one is enough to make every run that
	// selects it fail outright.
	if got := table.CatalogTotals.CatalogIllegal; got != 0 {
		t.Errorf("table reports %d rejected ids still in the catalog; L1 did not remove them", got)
	}
	if got := stats.CatalogIllegal; got != 0 {
		t.Errorf("catalog contains %d ids promptfoo rejects: %v", got, rejectedCatalogIDs())
	}

	// Headline numbers, pinned so a silent change to the usable surface cannot
	// land unnoticed. These are the figures the docs quote.
	if got, want := stats.CatalogPlugins, 116; got != want {
		t.Errorf("catalog plugins = %d, want %d", got, want)
	}
	if got, want := stats.LocalOnlyLegal, 26; got != want {
		t.Errorf("locally runnable plugins = %d, want %d", got, want)
	}
	if got, want := stats.Retired, 3; got != want {
		t.Errorf("withdrawn plugin ids = %d, want %d", got, want)
	}
}

// rejectedCatalogIDs names the offending ids so the failure above is actionable
// rather than a bare count.
func rejectedCatalogIDs() []string {
	out := make([]string, 0)
	for _, entry := range promptfooPluginCatalog {
		if !PluginIDIsLegal(entry.ID) {
			out = append(out, entry.ID)
		}
	}
	sort.Strings(out)
	return out
}

// TestPromptfooPluginCatalogContainsNoIllegalIDs is the L1 invariant stated
// directly: nothing the catalog offers may be an id promptfoo rejects.
//
// Counter-proof: put `prompt-injection` back in promptfooPluginCatalog → this
// goes red, naming it. That is the exact regression P0-2 exists to prevent.
func TestPromptfooPluginCatalogContainsNoIllegalIDs(t *testing.T) {
	for _, entry := range promptfooPluginCatalog {
		if !PluginIDIsLegal(entry.ID) {
			t.Errorf("catalog offers %q, which promptfoo rejects: %s",
				entry.ID, PluginLegalityRejection(entry.ID))
		}
	}
}

// TestPromptfooRetiredPluginsMatchIllegalPluginIDs ties the withdrawal record to
// the verdict table in BOTH directions.
//
// One direction alone is not enough: a withdrawal without a verdict means the
// table was generated before the catalog changed; a verdict without a
// withdrawal means an id disappeared with no explanation for the user.
func TestPromptfooRetiredPluginsMatchIllegalPluginIDs(t *testing.T) {
	verdicts := IllegalPluginIDs()
	retired := PromptfooRetiredPlugins()

	if len(retired) != len(verdicts) {
		t.Fatalf("retiredPluginCatalog has %d entries, the verdict table has %d illegal ids (%v)",
			len(retired), len(verdicts), verdicts)
	}
	byID := make(map[string]PromptfooRetiredPlugin, len(retired))
	for _, entry := range retired {
		byID[entry.ID] = entry
	}
	for _, id := range verdicts {
		entry, ok := byID[id]
		if !ok {
			t.Errorf("promptfoo rejects %q but retiredPluginCatalog records no reason or replacement for it", id)
			continue
		}
		if strings.TrimSpace(entry.Reason) == "" {
			t.Errorf("withdrawn id %q carries an empty reason; a withdrawal must explain itself", id)
		}
		if strings.TrimSpace(entry.Name) == "" {
			t.Errorf("withdrawn id %q lost the label users saw (%q)", id, entry.Name)
		}
		if strings.TrimSpace(entry.ReplacementLabel) == "" {
			t.Errorf("withdrawn id %q has no replacement guidance; deleting the row is not the fix", id)
		}
		if len(entry.ReplacementIDs) == 0 {
			t.Errorf("withdrawn id %q names no replacement ids", id)
		}
	}
	for _, entry := range retired {
		if PluginIDIsLegal(entry.ID) {
			t.Errorf("retiredPluginCatalog withdraws %q, but promptfoo accepts it — the withdrawal is stale", entry.ID)
		}
	}
}

// TestPromptfooRetiredPluginReplacementsResolve is the plugin-side equivalent of
// the strategy catalogue's deprecated-alias mapping (plan 18 §3.5.1 / R-2-f).
//
// Withdrawing an id fixes "the option crashes" and leaves "where did my
// capability go" unanswered. The strategy side already answered that question
// for its own deprecated aliases; this asserts the plugin side now does too, and
// that every replacement it names actually resolves in the slot it points at.
//
// Counter-proof: change a replacement id to a typo → red. Without this the
// guidance could rot into a sentence pointing at nothing.
func TestPromptfooRetiredPluginReplacementsResolve(t *testing.T) {
	pluginIDs := toIDSet(PromptfooEngineSupportedPluginIDs())
	strategyIDs := make(map[string]bool)
	for _, entry := range allPromptfooStrategyEntries() {
		strategyIDs[entry.ID] = true
	}

	for _, entry := range PromptfooRetiredPlugins() {
		switch entry.ReplacementSlot {
		case "plugin":
			for _, id := range entry.ReplacementIDs {
				if !pluginIDs[id] {
					t.Errorf("withdrawn %q points at plugin %q, which is not in the plugin catalog", entry.ID, id)
				}
			}
		case "strategy":
			for _, id := range entry.ReplacementIDs {
				if !strategyIDs[id] {
					t.Errorf("withdrawn %q points at strategy %q, which is not in the strategy catalog", entry.ID, id)
				}
			}
		default:
			t.Errorf("withdrawn %q has replacement_slot %q, want \"plugin\" or \"strategy\"", entry.ID, entry.ReplacementSlot)
		}
	}
}

// TestPromptfooPluginCatalogHasNoStrategyNameCollision is the general guard
// behind the `jailbreak` confusion (R-2-e).
//
// `jailbreak` reached the plugin selector because the platform invented a plugin
// id that happens to be a legitimate promptfoo STRATEGY name. The two are
// different namespaces and the same word means different things in each. Pinning
// only that one id would leave the next collision free to land, so this asserts
// the invariant for every id: no plugin id may equal a promptfoo strategy name.
//
// Counter-proof: add p("jailbreak", …) back → red, naming the collision.
func TestPromptfooPluginCatalogHasNoStrategyNameCollision(t *testing.T) {
	nativeStrategies := make(map[string]bool)
	for _, row := range nativeStrategyRows() {
		nativeStrategies[row.ID] = true
	}
	if len(nativeStrategies) == 0 {
		t.Fatal("no native strategies loaded; the collision guard would be a no-op")
	}
	for _, entry := range promptfooPluginCatalog {
		if nativeStrategies[entry.ID] {
			t.Errorf("plugin id %q is also a promptfoo strategy name — the two namespaces collide, "+
				"so a user reading the plugin selector cannot tell which one they are choosing", entry.ID)
		}
	}
}

// TestPromptfooPluginFanoutIsPinned pins the expansion weights P0-1's allocator
// depends on. `numTests` is applied per EXPANDED sub-plugin, so these numbers are
// the multiplier between "what the user typed" and "how many probes run" — an
// unnoticed change here is a silent cost change.
func TestPromptfooPluginFanoutIsPinned(t *testing.T) {
	for id, want := range map[string]int{"harmful": 26, "bias": 6, "pii": 4, "contracts": 1, "policy": 1} {
		if got := PluginFanout(id); got != want {
			t.Errorf("PluginFanout(%q) = %d, want %d", id, got, want)
		}
	}
	// Unknown ids are 1, not 0: `file://` custom plugins never expand, and
	// returning 0 would make an unknown id free and under-weight a selection.
	for _, unknown := range []string{"", "  ", "file:///tmp/custom.yaml", "no-such-plugin"} {
		if got := PluginFanout(unknown); got != 1 {
			t.Errorf("PluginFanout(%q) = %d, want 1 (documented convention for unknown ids)", unknown, got)
		}
	}
	// The recorded expansion and the recorded count must agree, or the allocator
	// and the report would disagree about how many sub-plugins a selection made.
	for _, entry := range promptfooPluginCatalog {
		fanout := PluginFanout(entry.ID)
		expandsTo := PluginExpandsTo(entry.ID)
		if fanout > 1 {
			if len(expandsTo) != fanout {
				t.Errorf("PluginFanout(%q) = %d but PluginExpandsTo returns %d ids", entry.ID, fanout, len(expandsTo))
			}
			continue
		}
		if len(expandsTo) != 0 {
			t.Errorf("PluginFanout(%q) = 1 but PluginExpandsTo returns %v; a non-expanding id must record none", entry.ID, expandsTo)
		}
	}
}

// TestPromptfooPluginFanoutTotals pins the cost shape P0-1 consumes.
func TestPromptfooPluginFanoutTotals(t *testing.T) {
	totals := PromptfooPluginFanoutTotals([]string{"harmful", " pii ", "", "harmful"})
	// `harmful` twice and the blank entry must not inflate the multiplier: a
	// double selection is still one selection, and a blank is a leftover from a
	// multi-select widget. (Blank is dropped; the duplicate collapses because the
	// per-id map is keyed by id.)
	if totals.ExpandedPlugins != 30 {
		t.Errorf("ExpandedPlugins = %d, want 30 (harmful 26 + pii 4)", totals.ExpandedPlugins)
	}
	if totals.FanoutByPlugin["harmful"] != 26 || totals.FanoutByPlugin["pii"] != 4 {
		t.Errorf("FanoutByPlugin = %v, want harmful:26 pii:4", totals.FanoutByPlugin)
	}
	if got := PromptfooPluginFanoutTotals(nil).ExpandedPlugins; got != 0 {
		t.Errorf("empty selection expands to %d, want 0", got)
	}
}

// TestPromptfooRequiresConfigIsNotIllegal keeps the two verdicts apart.
//
// `policy` is a legal promptfoo plugin (safeParse passes, fanout 1) that yields
// 0 cases because the platform holds no config.policy text. Classifying it as
// illegal would disguise a capability gap as a broken id and would discard U8's
// `promptfoo:policy` judging semantics with it.
func TestPromptfooRequiresConfigIsNotIllegal(t *testing.T) {
	ids := RequiresConfigPluginIDs()
	if len(ids) != 1 || ids[0] != "policy" {
		t.Fatalf("RequiresConfigPluginIDs() = %v, want [policy]", ids)
	}
	if !PluginIDIsLegal("policy") {
		t.Error("policy is marked config-gated but is treated as illegal; the two verdicts must not merge")
	}
	if PluginFanout("policy") != 1 {
		t.Errorf("PluginFanout(policy) = %d, want 1", PluginFanout("policy"))
	}
	if !PluginRequiresConfig("policy") {
		t.Error("PluginRequiresConfig(policy) = false")
	}
	if strings.TrimSpace(PluginRequiresConfigReason("policy")) == "" {
		t.Error("policy carries no explanation of what configuration it needs")
	}
	// The config-gated id must stay selectable: the moment the platform collects
	// policy text it becomes runnable with no further change, so removing it
	// would turn a temporary gap into a permanent hole.
	if !toIDSet(PromptfooEngineSupportedPluginIDs())["policy"] {
		t.Error("policy is config-gated but no longer offered in the catalog; a temporary gap became a permanent one")
	}
	// And it must not be recommended as a working alternative — see
	// TestRemoteGenerationHintRecommendsAreLegal.
	if !strings.Contains(PluginRequiresConfigReason("policy"), "config.policy") {
		t.Errorf("the reason for policy should name config.policy, got %q", PluginRequiresConfigReason("policy"))
	}
}

// TestPromptfooLegalityProvenanceMatchesInstalledPromptfoo binds the table to the
// promptfoo build the platform actually ships, so an upgrade cannot leave the
// legality table describing a version nobody runs.
func TestPromptfooLegalityProvenanceMatchesInstalledPromptfoo(t *testing.T) {
	table := readPluginLegalityTable(t)

	repoRoot := filepath.Join(filepath.Dir(promptfooPluginLegalityPath(t)), "..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(repoRoot, "promptfoo_engine", "package.json"))
	if err != nil {
		t.Fatalf("read promptfoo_engine/package.json: %v", err)
	}
	var pkg struct {
		Dependencies struct {
			Promptfoo string `json:"promptfoo"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse promptfoo_engine/package.json: %v", err)
	}
	if want := strings.TrimPrefix(pkg.Dependencies.Promptfoo, "^"); table.PromptfooVersion != want {
		t.Errorf("legality table was generated against promptfoo %q but promptfoo_engine/package.json pins %q. "+
			"After upgrading promptfoo, run `node scripts/gen_plugin_legality.mjs` and commit the regenerated table.",
			table.PromptfooVersion, want)
	}
	if table.PromptfooSchemaBinding.SemanticName != "RedteamConfigSchema" {
		t.Errorf("table records schema binding %q, want RedteamConfigSchema — the generator resolved something else",
			table.PromptfooSchemaBinding.SemanticName)
	}
}
