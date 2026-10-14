package maclaw

// promptfoo_plugin_legality.go — P2-1 · plugin-id legality + expansion fanout.
//
// # The defect this file exists to prevent
//
// U6 (promptfoo_remote_dep.go) answers "does this plugin need remote
// generation". It never answered "does this plugin EXIST". Its 29 unmarked
// (`local_only`) ids were therefore read as "runnable here", and three of
// them are not promptfoo plugin ids at all:
//
//	prompt-injection   platform-invented
//	jailbreak          a promptfoo STRATEGY, not a plugin
//	security-exploit   platform-invented
//
// A user could select one, the BFF accepted it, and promptfoo's zod rejected
// it mid-run — the whole run fails, after the user has already filled in the
// form. "Unmarked" is not "legal".
//
// # Single source of truth
//
// Like U6, this table is NOT hand-maintained. scripts/gen_plugin_legality.mjs
// produces testdata/promptfoo_plugin_legality.json by evaluating promptfoo's
// own RedteamConfigSchema at runtime, one safeParse per catalog id, and the
// result is embedded below.
//
// Why safeParse and not a cheaper lookup — three alternatives were tried and
// all three are wrong:
//
//  1. The ALL_PLUGINS literal is NOT the zod acceptance set. The latter is
//     [...COLLECTIONS, ...ALL_PLUGINS, ...ALIASED_PLUGIN_MAPPINGS]; omitting
//     COLLECTIONS would reject `pii`/`harmful`/`bias` — the collection ids
//     that legally EXPAND.
//  2. The redteam.Plugins instance table (157 entries) is a different set:
//     `jailbreak` is absent (it is a strategy) while `pii`/`harmful` appear
//     only in expanded form.
//  3. A hand-kept list drifts. U6's 29 is exactly that list, and it is how 3
//     unrunnable ids reached production.
//
// # Two products, one run
//
//	legality — which ids promptfoo accepts (this file's primary output)
//	fanout   — how many sub-plugins each accepted id expands into, which is
//	          P0-1's split weight. promptfoo applies numTests per EXPANDED
//	          sub-plugin, so `harmful` costs 26x what `policy` costs. A cost
//	          estimator that treated them alike would understate the worst
//	          case (the form default is `plugins: ['harmful']`).
//
// # Deliberate non-definitions
//
// Illegal and "needs config" are different verdicts and must never be merged:
//
//   - Illegal: promptfoo rejects the id. Nothing the platform does can fix it.
//     Policy is the counterexample — it IS a legal id (safeParse passes,
//     fanout 1) that generates 0 cases because the platform holds no
//     config.policy text to assert against. Classifying it as illegal would
//     disguise a capability gap as a broken id and would discard U8's
//     `promptfoo:policy` judging semantics along with it.
//
// # Update procedure (after any promptfoo upgrade)
//
//	cd evaluating_platform/promptfoo_engine && npm ci
//	cd .. && node scripts/gen_plugin_legality.mjs
//	cd .. && go test ./internal/maclaw/ -run TestPromptfooPluginLegality

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// pluginLegalityTable is the on-disk shape of
// testdata/promptfoo_plugin_legality.json. Every field is a declared input to
// a decision; nothing is captured "just in case".
type pluginLegalityTable struct {
	Comment                 string                          `json:"_comment"`
	PromptfooVersion        string                          `json:"promptfoo_version"`
	PromptfooTablesChunk    string                          `json:"promptfoo_tables_chunk"`
	PromptfooSchemaBinding  pluginLegalitySchemaBinding     `json:"promptfoo_schema_binding"`
	CatalogTotals           pluginLegalityCatalogTotals     `json:"catalog_totals"`
	IllegalPluginIDs        map[string]string               `json:"illegal_plugin_ids"`
	RequiresConfigPluginIDs map[string]string               `json:"requires_config_plugin_ids"`
	Fanout                  map[string]int                  `json:"fanout"`
	Plugins                 map[string]pluginLegalityRecord `json:"plugins"`
}

// pluginLegalitySchemaBinding records how the generator found the zod schema.
// Carried into the artifact so a reader can tell "the generator resolved
// RedteamConfigSchema" from "the generator guessed at something that looks
// like a redteam schema" — the two produce very different files.
type pluginLegalitySchemaBinding struct {
	SemanticName string `json:"semantic_name"`
	ChunkExport  string `json:"chunk_export"`
}

// pluginLegalityCatalogTotals carries the self-reported counts. They are
// asserted against the compiled-in catalog rather than trusted, so a stale
// artifact cannot make the guardrail look healthy.
type pluginLegalityCatalogTotals struct {
	Plugins        int `json:"plugins"`
	Legal          int `json:"legal"`
	CatalogIllegal int `json:"catalog_illegal"`
	Illegal        int `json:"illegal"`
	LocalOnlyLegal int `json:"local_only_legal"`
	RemoteLegal    int `json:"remote_legal"`
	RequiresConfig int `json:"requires_config"`
}

// pluginLegalityRecord is the per-id verdict.
type pluginLegalityRecord struct {
	Legal  bool   `json:"legal"`
	Fanout int    `json:"fanout"`
	Reason string `json:"reason"`
	// Rejection is promptfoo's own zod message, captured verbatim. It is the
	// authoritative "why", and keeping it means an operator reading the report
	// sees the engine's words rather than the platform's paraphrase.
	Rejection string `json:"rejection"`
	// ExpandsTo is present only when the id actually expands. 116 records of
	// `["itself"]` would bury the three ids that matter.
	ExpandsTo []string `json:"expands_to"`
	// RequiresConfig marks a legal id the platform cannot feed yet. Distinct
	// from illegal on purpose — see the file header.
	RequiresConfig string `json:"requires_config"`
}

//go:embed testdata/promptfoo_plugin_legality.json
var pluginLegalityTableJSON []byte

// pluginLegalityTableData is the parsed table. Parsed once at init; a malformed
// table is a generator bug, not a runtime condition, so init panics rather
// than degrading into a guardrail that reports "everything is legal".
var pluginLegalityTableData = mustParsePluginLegalityTable()

func mustParsePluginLegalityTable() pluginLegalityTable {
	var table pluginLegalityTable
	if err := json.Unmarshal(pluginLegalityTableJSON, &table); err != nil {
		panic(fmt.Sprintf("maclaw: embedded promptfoo_plugin_legality.json is malformed: %v", err))
	}
	if len(table.Plugins) == 0 {
		panic("maclaw: embedded promptfoo_plugin_legality.json records zero plugins; the guardrail would be a no-op")
	}
	if len(table.IllegalPluginIDs) == 0 {
		// Not a "must be non-empty" nicety: an empty illegal set is the exact
		// shape of a run where schema resolution silently failed. Fail at init
		// rather than shipping a guardrail that certifies every id.
		panic("maclaw: embedded promptfoo_plugin_legality.json marks zero ids illegal; " +
			"promptfoo rejects at least the platform-invented ids, so this means the schema was not resolved")
	}
	return table
}

// PluginIDIsLegal reports whether promptfoo accepts the id as a redteam plugin.
//
// Ids the table does not know at all return true: this predicate guards the
// CATALOG plus the recorded withdrawals, and an unknown id is handled by
// ValidateEngineRunPluginLegality (P0-2 L2) which owns the "is this id offered
// at all" question. Returning false here for every non-catalog id would make
// the two guards contradict each other on `file://` custom plugins from expert
// packs.
//
// Withdrawn ids (prompt-injection / jailbreak / security-exploit) are IN the
// table and marked illegal, so they return false even though they are no longer
// in the catalog — the withdrawal is a verdict about the id, not merely its
// absence from a list.
func PluginIDIsLegal(pluginID string) bool {
	record, ok := pluginLegalityTableData.Plugins[strings.TrimSpace(pluginID)]
	if !ok {
		return true
	}
	return record.Legal
}

// PluginLegalityRejection returns promptfoo's verbatim zod rejection message
// for an illegal id, or "" when the id is legal / unknown.
func PluginLegalityRejection(pluginID string) string {
	return pluginLegalityTableData.Plugins[strings.TrimSpace(pluginID)].Rejection
}

// PluginLegalityReason returns the platform's rationale for an illegal id.
// Unlike Rejection it says WHY the platform carried a bogus id (e.g. "jailbreak
// is a strategy, not a plugin"), which is what a report or an announcement
// needs; Rejection is what the engine said.
func PluginLegalityReason(pluginID string) string {
	return pluginLegalityTableData.IllegalPluginIDs[strings.TrimSpace(pluginID)]
}

// PluginFanout reports how many redteam sub-plugins the id expands into.
// `harmful` -> 26, `pii` -> 4, `bias` -> 6, everything else -> 1.
//
// This is P0-1's weight source: promptfoo applies numTests to each EXPANDED
// sub-plugin, so a run's real probe count is numTests x sum(fanout).
//
// Returns 1 for unknown ids. That is the documented convention, not a
// fallback: `file://` custom plugins take a separate zod branch
// (z.string().superRefine) and never participate in collection expansion, so
// their fanout genuinely is 1. Returning 0 would make an unknown id free and
// silently under-weight a selection.
func PluginFanout(pluginID string) int {
	fanout, ok := pluginLegalityTableData.Fanout[strings.TrimSpace(pluginID)]
	if !ok || fanout < 1 {
		return 1
	}
	return fanout
}

// PluginExpandsTo returns the sub-plugin ids an id expands into, or nil when
// it does not expand. P0-1's split allocator needs the count; this answers
// "which ones", for reporting and for tests that pin the expansion.
func PluginExpandsTo(pluginID string) []string {
	record, ok := pluginLegalityTableData.Plugins[strings.TrimSpace(pluginID)]
	if !ok || len(record.ExpandsTo) == 0 {
		return nil
	}
	return append([]string(nil), record.ExpandsTo...)
}

// PluginRequiresConfig reports whether a legal id is unusable until the
// platform collects configuration for it. Currently only `policy`, which is a
// legal promptfoo plugin (safeParse passes, fanout 1) that yields 0 cases
// without config.policy text.
//
// Deliberately NOT a legality question: a requires_config id must stay
// selectable, because the moment the platform collects policy text it becomes
// fully runnable with no further change. Removing it would turn a capability
// gap into a permanent hole.
func PluginRequiresConfig(pluginID string) bool {
	_, ok := pluginLegalityTableData.RequiresConfigPluginIDs[strings.TrimSpace(pluginID)]
	return ok
}

// PluginRequiresConfigReason returns why the id needs configuration.
func PluginRequiresConfigReason(pluginID string) string {
	return pluginLegalityTableData.RequiresConfigPluginIDs[strings.TrimSpace(pluginID)]
}

// IllegalPluginIDs returns the sorted ids promptfoo rejects. After the L1
// catalog filter these have no entry in promptfooPluginCatalog, so this is the
// record of what was withdrawn and why — the answer to "why did
// prompt-injection disappear from the selector".
func IllegalPluginIDs() []string {
	ids := make([]string, 0, len(pluginLegalityTableData.IllegalPluginIDs))
	for id := range pluginLegalityTableData.IllegalPluginIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RequiresConfigPluginIDs returns the sorted legal ids that need config text
// before they can run.
func RequiresConfigPluginIDs() []string {
	ids := make([]string, 0, len(pluginLegalityTableData.RequiresConfigPluginIDs))
	for id := range pluginLegalityTableData.RequiresConfigPluginIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// LegalAndLocalPluginIDs returns the sorted ids this deployment can both
// generate locally AND that promptfoo accepts — the set a user can pick today
// and get a complete evaluation.
//
// Strictly smaller than LocalOnlyPluginIDs(): U6's 29 includes 3 ids that
// cannot run at all. Any user-facing "you can fully evaluate N plugins"
// message must use this count, or the platform advertises capabilities whose
// runs are guaranteed to fail.
func LegalAndLocalPluginIDs() []string {
	ids := make([]string, 0, len(promptfooPluginCatalog))
	for _, entry := range promptfooPluginCatalog {
		if PluginIDIsLegal(entry.ID) && !PluginRequiresRemoteGeneration(entry.ID) {
			ids = append(ids, entry.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// PluginFanoutTotals is the cost shape of a selection: P0-1's
// "actual probe count = numTests x sum(fanout)" made concrete.
type PluginFanoutTotals struct {
	// Plugins is how many ids were selected.
	Plugins int `json:"plugins"`
	// ExpandedPlugins is sum(fanout) — the real number of redteam
	// sub-plugins, i.e. the multiplier on numTests.
	ExpandedPlugins int `json:"expanded_plugins"`
	// FanoutByPlugin is the per-id breakdown, so a cost estimate can show
	// which selection drove the number instead of just the total.
	FanoutByPlugin map[string]int `json:"fanout_by_plugin"`
}

// PromptfooPluginFanoutTotals sums the expansion of a selection. It reads the
// legality table, so a promptfoo upgrade that changes what a collection id
// expands into is reflected here without touching this code.
//
// Duplicates and blank entries collapse, matching the frontend's
// totalPluginFanout: a cost estimate that counted `harmful` twice because the
// multi-select held it twice would disagree with what actually runs, and two
// surfaces showing different numbers for the same selection is worse than
// either number alone.
func PromptfooPluginFanoutTotals(pluginIDs []string) PluginFanoutTotals {
	totals := PluginFanoutTotals{FanoutByPlugin: make(map[string]int, len(pluginIDs))}
	for _, id := range pluginIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, seen := totals.FanoutByPlugin[trimmed]; seen {
			continue
		}
		fanout := PluginFanout(trimmed)
		totals.FanoutByPlugin[trimmed] = fanout
		totals.Plugins++
		totals.ExpandedPlugins += fanout
	}
	return totals
}

// PromptfooPluginLegalityStats summarizes the guardrail for docs and tests.
//
// Three counts are easy to confuse and each means something different:
//
//	CatalogIllegal — LIVE catalog entries promptfoo rejects. Must be 0; a single
//	                 one makes every run selecting it fail outright.
//	Retired        — withdrawn ids the table still records (with a reason), so
//	                 "why did prompt-injection disappear" has an answer.
//	Legal          — live catalog entries promptfoo accepts.
type PromptfooPluginLegalityStats struct {
	PromptfooVersion  string `json:"promptfoo_version"`
	TablesChunk       string `json:"promptfoo_tables_chunk"`
	CatalogPlugins    int    `json:"catalog_plugins"`
	Legal             int    `json:"legal"`
	CatalogIllegal    int    `json:"catalog_illegal"`
	Retired           int    `json:"retired"`
	LocalOnlyLegal    int    `json:"local_only_legal"`
	RemoteLegal       int    `json:"remote_legal"`
	RequiresConfig    int    `json:"requires_config"`
	MaxFanout         int    `json:"max_fanout"`
	MaxFanoutPluginID string `json:"max_fanout_plugin_id"`
}

// PromptfooPluginLegalityStatsForCatalog computes the stats implied by the
// compiled-in catalog, so a test can compare them against the artifact's
// self-reported totals instead of trusting the artifact.
func PromptfooPluginLegalityStatsForCatalog() PromptfooPluginLegalityStats {
	stats := PromptfooPluginLegalityStats{
		PromptfooVersion: pluginLegalityTableData.PromptfooVersion,
		TablesChunk:      pluginLegalityTableData.PromptfooTablesChunk,
		CatalogPlugins:   len(promptfooPluginCatalog),
		RequiresConfig:   len(pluginLegalityTableData.RequiresConfigPluginIDs),
		Retired:          len(IllegalPluginIDs()),
	}
	for _, entry := range promptfooPluginCatalog {
		if !PluginIDIsLegal(entry.ID) {
			stats.CatalogIllegal++
			continue
		}
		stats.Legal++
		if PluginRequiresRemoteGeneration(entry.ID) {
			stats.RemoteLegal++
		} else {
			stats.LocalOnlyLegal++
		}
		if fanout := PluginFanout(entry.ID); fanout > stats.MaxFanout {
			stats.MaxFanout = fanout
			stats.MaxFanoutPluginID = entry.ID
		}
	}
	return stats
}
