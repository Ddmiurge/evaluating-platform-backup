package maclaw

// promptfoo_strategy_catalog_test.go — Phase 3 开篇 · 任务 1 · the strategy
// catalogue is discovered from promptfoo, and the discovery is pinned.
//
// The assertions here are deliberately about *properties*, not counts. A test
// that asserted "46 strategies" would pass today and fail on the next promptfoo
// upgrade for a reason that has nothing to do with a regression. What must stay
// true is: every offered strategy exists upstream, nothing provably-generates-
// nothing is offered, U6's gated set is still withheld, and the platform's own
// invented dimensions survive.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// engineSourcePath resolves a file inside promptfoo_engine relative to this test
// file, so the assertions work from any `go test` working directory. Same
// approach as `promptfooRemoteDepPath` in promptfoo_remote_dep_test.go.
func engineSourcePath(t *testing.T, rel string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed to resolve the test file path")
	}
	// internal/maclaw -> repo root is two levels up.
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	return filepath.Join(root, "promptfoo_engine", rel)
}

// TestPromptfooStrategyCatalogTotalsAreConsistent checks the generated file
// agrees with itself. A hand-edited or partially-regenerated table would break
// the arithmetic that the availability filtering relies on.
func TestPromptfooStrategyCatalogTotalsAreConsistent(t *testing.T) {
	stats := PromptfooStrategyCatalogStats()
	if stats.NativeTotal != strategyCatalogData.Totals.NativeStrategies {
		t.Errorf("native total = %d, table says %d", stats.NativeTotal, strategyCatalogData.Totals.NativeStrategies)
	}
	sum := strategyCatalogData.Totals.Local + strategyCatalogData.Totals.Remote +
		strategyCatalogData.Totals.NoOp + strategyCatalogData.Totals.RequiresRuntimeInput +
		strategyCatalogData.Totals.Media + strategyCatalogData.Totals.Unknown +
		strategyCatalogData.Totals.Unsupported
	if sum != stats.NativeTotal {
		t.Errorf("availability buckets sum to %d, want %d (a strategy must be in exactly one bucket)", sum, stats.NativeTotal)
	}
	if stats.SelectableTotal != len(promptfooStrategyCatalog)+len(LocalNativeStrategyIDs()) {
		t.Errorf("selectable total = %d, recomputed %d", stats.SelectableTotal,
			len(promptfooStrategyCatalog)+len(LocalNativeStrategyIDs()))
	}
	if stats.PromptfooVersion == "" || stats.DiscoveryMethod == "" {
		t.Error("catalog stats must record the promptfoo version and the discovery method")
	}
}

// TestPromptfooStrategyCatalogOmitsDefaultEnumEntry pins the specific trap that
// made runtime evaluation necessary rather than reading the zod enum.
//
// `ALL_STRATEGIES` — the value `strategyIdSchema` validates against — is
// `["default", ...DEFAULT_STRATEGIES, ...ADDITIONAL_STRATEGIES,
// ...STRATEGY_COLLECTIONS, ...AGENTIC_STRATEGIES]`: 36 entries for 35 executable
// strategies. `default` is accepted by the schema but has no action, so a
// catalogue built from the enum would offer a strategy that generates nothing.
func TestPromptfooStrategyCatalogOmitsDefaultEnumEntry(t *testing.T) {
	if _, ok := NativeStrategyByID("default"); ok {
		t.Error("`default` is in promptfoo's ALL_STRATEGIES enum but has no executable action; " +
			"it must not appear in the discovered catalogue")
	}
	for _, id := range LocalNativeStrategyIDs() {
		if id == "default" {
			t.Fatal("`default` must never be offered as a selectable strategy")
		}
	}
}

// TestPromptfooStrategyCatalogOffersNothingThatGeneratesNothing is the guardrail
// this whole task turns on.
//
// `simba` is in promptfoo's strategy list and the platform used to forward it,
// but its action logs "has been deprecated and removed … will be skipped" and
// returns `[]`. The run exits 0 and the report shows the strategy as applied:
// the silent-0-coverage shape U6 exists to eliminate, reached by a path U6's
// remote-dependency table does not cover because simba needs no remote
// generation — it simply does nothing.
func TestPromptfooStrategyCatalogOffersNothingThatGeneratesNothing(t *testing.T) {
	for _, id := range LocalNativeStrategyIDs() {
		row, ok := NativeStrategyByID(id)
		if !ok {
			t.Fatalf("offered strategy %q is absent from the generated table", id)
		}
		if row.Availability != StrategyAvailabilityLocal {
			t.Errorf("offered strategy %q has availability %q; only %q may be offered",
				id, row.Availability, StrategyAvailabilityLocal)
		}
		if row.ProbeGeneratedCases < 1 {
			t.Errorf("offered strategy %q generated %d cases in the runtime probe", id, row.ProbeGeneratedCases)
		}
	}
	// The specific known no-ops must be withheld with a stated reason, so an
	// operator asking "why can't I use simba?" gets an answer.
	for _, id := range []string{"simba", "basic"} {
		row, ok := NativeStrategyByID(id)
		if !ok {
			t.Fatalf("%q must be present in the table so its unavailability is explainable", id)
		}
		if row.Availability != StrategyAvailabilityNoOp {
			t.Errorf("%q availability = %q, want %q", id, row.Availability, StrategyAvailabilityNoOp)
		}
		if StrategyIsSelectable(id) {
			t.Errorf("%q is selectable; it provably generates zero cases", id)
		}
		if reason := UnavailableNativeStrategyIDs()[id]; strings.TrimSpace(reason) == "" {
			t.Errorf("%q is withheld without a reason", id)
		}
	}
}

// TestPromptfooStrategyCatalogHonoursTheMeasuredRemoteGate verifies the Phase 3
// widening did not weaken the remote-generation gate, and that the gate's
// verdict is the MEASURED one rather than a hand-kept flag.
//
// P2-2: the gate grew from four strategies to six. `jailbreak` (promptfoo's
// deprecated alias, whose action delegates to the iterative:meta provider) and
// `jailbreak:meta` were both offered and both failed mid-run — the alias because
// nothing linked it to the gated implementation by name.
func TestPromptfooStrategyCatalogHonoursTheMeasuredRemoteGate(t *testing.T) {
	for platformID, nativeID := range remoteDepTableData.RemoteGatedStrategies {
		if StrategyIsSelectable(nativeID) {
			t.Errorf("native strategy %q (platform %q) is gated but is offered as selectable", nativeID, platformID)
		}
		row, ok := NativeStrategyByID(nativeID)
		if !ok {
			t.Errorf("gated native strategy %q is missing from the discovered table", nativeID)
			continue
		}
		if row.Availability != StrategyAvailabilityRemote {
			t.Errorf("gated %q has availability %q, want %q", nativeID, row.Availability, StrategyAvailabilityRemote)
		}
		// The verdict must come from a measurement, and the measurement must say
		// so — a gate with no recorded evidence cannot be audited after an upgrade.
		if !row.RemoteRequired {
			t.Errorf("gated %q does not carry the measured remote_required verdict", nativeID)
		}
		if strings.TrimSpace(row.RemoteEvidence) == "" {
			t.Errorf("gated %q records no remote_evidence", nativeID)
		}
	}

	// P2-2 的具体回归点：这两个 id 曾经都在选择器里且都跑不通。
	for _, nativeID := range []string{"jailbreak", "jailbreak:meta"} {
		row, ok := NativeStrategyByID(nativeID)
		if !ok {
			t.Fatalf("%q must be present so its unavailability is explainable", nativeID)
		}
		if !row.RemoteRequired || StrategyIsSelectable(nativeID) {
			t.Errorf("%q requires remote generation at provider construction and must not be offered", nativeID)
		}
	}

	// `best-of-n` is the case that proves the measured verdict must win over a
	// runtime probe: its strategy wrapper happily rewrites test cases and returns
	// them (so a probe counts N cases), while `new BestOfNProvider()` throws
	// "requires remote generation, which has been explicitly disabled". Trusting
	// the probe would have quietly opened a gated capability.
	row, ok := NativeStrategyByID("best-of-n")
	if !ok {
		t.Fatal("best-of-n must be present in the table")
	}
	if !row.RemoteRequired || StrategyIsSelectable("best-of-n") {
		t.Error("best-of-n is remote-gated at provider construction and must not be offered")
	}
}

// TestPromptfooStrategyCatalogWithdrawsPlatformInventedDimensions pins P3-1.
//
// The previous version of this test asserted the OPPOSITE — that the platform's own
// dimensions survived the switch to dynamic discovery. That assertion was right for
// its time and is now the defect: those ids have no implementation on the Plan C
// path, so keeping them in the selector meant offering nine dimensions that silently
// did nothing (the wizard's own default was one of them). The test is inverted rather
// than deleted, because the reason the inversion is SAFE is what has to stay pinned:
// every withdrawal carries a reason and a replacement path, and every replacement
// actually runs here.
//
// Counter-proof: put `role-play` back in promptfooStrategyCatalog → red.
// Drop its record from retiredStrategyCatalog → red.
func TestPromptfooStrategyCatalogWithdrawsPlatformInventedDimensions(t *testing.T) {
	present := make(map[string]bool, len(promptfooStrategyCatalog))
	for _, entry := range promptfooStrategyCatalog {
		present[entry.ID] = true
	}

	withdrawn := []string{
		"direct", "role-play", "encoding", "multi-turn",
		"assumed-knowledge", "few-shot", "many-shot", "translate", "otherEncodings",
	}
	for _, id := range withdrawn {
		if present[id] {
			t.Errorf("platform-invented dimension %q is still offered; on the Plan C path it has no implementation and is dropped silently", id)
		}
		entry, ok := PromptfooRetiredStrategyGuidance(id)
		if !ok {
			t.Fatalf("withdrawn dimension %q has no record in retiredStrategyCatalog, so a user who selected it gets no explanation", id)
		}
		if strings.TrimSpace(entry.Reason) == "" || strings.TrimSpace(entry.ReplacementLabel) == "" {
			t.Errorf("withdrawn dimension %q records no reason / no replacement sentence", id)
		}
		if strings.TrimSpace(entry.Name) == "" {
			t.Errorf("withdrawn dimension %q lost the label users saw", id)
		}
	}

	// The retired set must be exactly the withdrawn set. A withdrawal without a
	// record would mean it vanished silently; a record without a withdrawal would
	// mean an id is both gone and unexplained.
	recorded := RetiredStrategyIDs()
	if len(recorded) != len(withdrawn) {
		t.Errorf("RetiredStrategyIDs() has %d entries, want %d (%v)", len(recorded), len(withdrawn), recorded)
	}
	recordedSet := toIDSet(recorded)
	for _, id := range withdrawn {
		if !recordedSet[id] {
			t.Errorf("withdrawn dimension %q is not in RetiredStrategyIDs()", id)
		}
	}

	// All three dispositions must be represented, so the enum cannot silently collapse
	// to "everything points at ids" (which is the shape that would hide the
	// no-equivalent cases behind a plausible-looking near neighbour).
	kinds := map[string]int{}
	for _, entry := range PromptfooRetiredStrategies() {
		kinds[entry.ReplacementKind]++
	}
	for _, want := range []string{strategyReplacementByIDs, strategyReplacementNoneNeeded, strategyReplacementNoEquivalent} {
		if kinds[want] == 0 {
			t.Errorf("no withdrawn dimension uses replacement_kind %q; the enum is not exercising it", want)
		}
	}
	// `direct` specifically: its replacement is the ABSENCE of a selection, which is
	// why it must not be lumped in with "no equivalent" — one says the capability
	// never existed as a strategy, the other says it did and is gone.
	if entry, ok := PromptfooRetiredStrategyGuidance("direct"); !ok || entry.ReplacementKind != strategyReplacementNoneNeeded {
		t.Errorf("direct should be kind=%s (不选策略即直接攻击), got %q", strategyReplacementNoneNeeded, entry.ReplacementKind)
	}

	// Every replacement id must be OFFERED and runnable here. A replacement that
	// cannot run is the "the platform told me to pick something broken" failure this
	// mechanism exists to prevent — the same bar the plugin axis uses.
	offered := toIDSet(PromptfooEngineSupportedStrategyIDs())
	for _, entry := range PromptfooRetiredStrategies() {
		for _, id := range entry.ReplacementIDs {
			if !offered[id] {
				t.Errorf("withdrawn %q points at strategy %q, which is not offered", entry.ID, id)
				continue
			}
			if StrategyRequiresRemoteGeneration(id) {
				t.Errorf("withdrawn %q points at strategy %q, which is remote-gated — the guidance sends the user to another refusal", entry.ID, id)
			}
			if !StrategyIsSelectable(StrategyNativeID(id)) {
				t.Errorf("withdrawn %q points at strategy %q, which cannot run in this deployment", entry.ID, id)
			}
		}
		// The three dispositions are expressed as DATA, not as prose — the same
		// discipline the plugin axis uses for `replacement_slot`. Asserting on wording
		// would make the test fragile and, worse, would let a label drift while the
		// data stayed wrong.
		switch entry.ReplacementKind {
		case strategyReplacementByIDs:
			if len(entry.ReplacementIDs) == 0 {
				t.Errorf("withdrawn %q is kind=%s but names no replacement ids", entry.ID, entry.ReplacementKind)
			}
		case strategyReplacementNoneNeeded, strategyReplacementNoEquivalent:
			if len(entry.ReplacementIDs) != 0 {
				t.Errorf("withdrawn %q is kind=%s but names ids (%v)", entry.ID, entry.ReplacementKind, entry.ReplacementIDs)
			}
		default:
			t.Errorf("withdrawn %q has unknown replacement_kind %q", entry.ID, entry.ReplacementKind)
		}
	}
}

// TestPromptfooStrategyNativeMappingMatchesEngineTable asserts the Go mapping
// and the engine's `nativeStrategyMap.ts` agree.
//
// They answer the same question from opposite ends of the wire, so a drift
// between them means the platform offers a strategy the engine will not forward
// (silently downgraded to Plan A) or forwards one the platform never validated.
func TestPromptfooStrategyNativeMappingMatchesEngineTable(t *testing.T) {
	engineNative := readEngineNativeStrategyIDs(t)
	for platformID, nativeID := range strategyCatalogData.PlatformToNativeMap {
		if nativeID != engineNative[platformID] {
			t.Errorf("platform %q maps to native %q in Go, but the engine table has %q",
				platformID, nativeID, engineNative[platformID])
		}
	}
	for platformID := range engineNative {
		if _, ok := strategyCatalogData.PlatformToNativeMap[platformID]; !ok {
			t.Errorf("engine maps platform id %q but the Go table does not", platformID)
		}
	}
	// A native mapping may point at a withheld strategy only when the platform id
	// for it is ITSELF gated. That is the case where the id names the withheld
	// capability (`iterative:meta` -> `jailbreak:meta`, `jailbreak` -> the same
	// provider via promptfoo's deprecated alias): the BFF pre-flight refuses it,
	// so nothing is forwarded. Any other withheld mapping would send a request to
	// a strategy this deployment cannot generate.
	for platformID, nativeID := range strategyCatalogData.PlatformToNativeMap {
		if StrategyIsSelectable(nativeID) {
			continue
		}
		if _, gated := remoteDepTableData.RemoteGatedStrategies[platformID]; !gated {
			t.Errorf("platform %q maps to withheld native strategy %q and is not itself gated; "+
				"a request naming it would be forwarded to a strategy this deployment cannot generate",
				platformID, nativeID)
		}
	}
}

// readEngineNativeStrategyIDs parses `NATIVE` out of nativeStrategyMap.ts so the
// two tables can be compared without running node.
func readEngineNativeStrategyIDs(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(engineSourcePath(t, filepath.Join("src", "runner", "nativeStrategyMap.ts")))
	if err != nil {
		t.Fatalf("read nativeStrategyMap.ts: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "const NATIVE: Readonly<Record<string, string>> = {")
	if start < 0 {
		t.Fatal("could not locate the NATIVE table in nativeStrategyMap.ts")
	}
	body := src[start:]
	// Start after the declaration's opening brace: the `const NATIVE: Readonly<…>
	// = {` line itself contains a colon and would otherwise parse as an entry
	// named "const NATIVE".
	if brace := strings.Index(body, "{"); brace >= 0 {
		body = body[brace+1:]
	}
	end := strings.Index(body, "\n};")
	if end < 0 {
		t.Fatal("could not locate the end of the NATIVE table")
	}
	body = body[:end]
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		// Strip comments first: the engine table interleaves explanatory
		// comments, several of which contain colons, so splitting before
		// stripping would produce bogus keys.
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Keys are either bare identifiers (`base64:`) or quoted when they
		// contain characters an identifier cannot (`'jailbreak:meta':`).
		var key, value string
		if strings.HasPrefix(trimmed, "'") {
			closing := strings.Index(trimmed[1:], "'")
			if closing < 0 {
				continue
			}
			key = trimmed[1 : closing+1]
			rest := strings.TrimSpace(trimmed[closing+2:])
			if !strings.HasPrefix(rest, ":") {
				continue
			}
			value = strings.Trim(strings.TrimSpace(rest[1:]), "',")
		} else {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) != 2 {
				continue
			}
			key = strings.TrimSpace(parts[0])
			value = strings.Trim(strings.TrimSpace(parts[1]), "',")
		}
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		t.Fatal("parsed zero entries from the engine NATIVE table")
	}
	return out
}

// TestPromptfooStrategyCatalogWidenedBeyondTheHandWrittenList is the regression
// this task exists to prevent: the catalogue must not collapse back to the 30
// hand-maintained ids.
func TestPromptfooStrategyCatalogWidenedBeyondTheHandWrittenList(t *testing.T) {
	cards := PromptfooPluginCatalogCards()
	offered := map[string]bool{}
	for _, card := range cards {
		if strings.HasPrefix(card.SourceRef, "promptfoo_strategy:") {
			offered[strings.TrimPrefix(card.SourceRef, "promptfoo_strategy:")] = true
		}
	}
	// Every discovered-and-selectable native id must be *reachable* — offered either
	// under its own id or under the platform id that forwards to it. Offering
	// both would list the same attack technique twice under two names
	// (`hydra` and `jailbreak:hydra`), which is worse than choosing one.
	reachable := func(id string) bool {
		if offered[id] {
			return true
		}
		return promptfooPlatformIDForNative(id) != ""
	}
	for _, id := range LocalNativeStrategyIDs() {
		if !reachable(id) {
			t.Errorf("discovered strategy %q is neither offered directly nor reachable via a platform id", id)
		}
	}
	// Specifically the ones discovery newly surfaced.
	for _, id := range []string{"morse", "piglatin", "camelcase", "emoji", "jailbreak-templates", "layer", "custom"} {
		if !offered[id] {
			t.Errorf("strategy %q was unreachable before discovery and is still not offered", id)
		}
	}
	// No id may appear twice (a duplicate would be indistinguishable in search
	// and would double-count in the report).
	seen := map[string]int{}
	for id := range offered {
		seen[id]++
	}
	total := 0
	for _, card := range cards {
		if strings.HasPrefix(card.SourceRef, "promptfoo_strategy:") {
			total++
		}
	}
	if total != len(offered) {
		t.Errorf("catalogue has %d strategy cards for %d distinct ids — duplicates present", total, len(offered))
	}
}

// TestPromptfooStrategyCardsCarryTheirNativeID pins the metadata the engine and
// the report need. Without it a card would only carry the platform id, and the
// mapping would have to be re-derived downstream.
func TestPromptfooStrategyCardsCarryTheirNativeID(t *testing.T) {
	cards := PromptfooPluginCatalogCards()
	for _, card := range cards {
		if !strings.HasPrefix(card.SourceRef, "promptfoo_strategy:") {
			continue
		}
		id := strings.TrimPrefix(card.SourceRef, "promptfoo_strategy:")
		if want := PlatformToNativeStrategyID(id); want != "" {
			// The native id is published only when it differs from the platform
			// id; an identity mapping would put `promptfoo_strategy_id: base64`
			// on every card. `StrategyNativeID` is what consumers call.
			got := card.SafeMetadata["promptfoo_strategy_id"]
			if want == id {
				if got != "" {
					t.Errorf("strategy %q is an identity mapping but shipped promptfoo_strategy_id=%q", id, got)
				}
			} else if got != want {
				t.Errorf("strategy %q: promptfoo_strategy_id = %q, want %q", id, got, want)
			}
		}
		// A withheld strategy must carry a reason, and U6's remote message must
		// win over a more specific one when both apply.
		if card.SafeMetadata["unavailable"] == "true" || card.SafeMetadata["requires_remote_generation"] == "true" {
			if card.SafeMetadata["unavailable_reason"] == "" {
				t.Errorf("strategy %q is marked unavailable without a reason", id)
			}
		}
	}
	// The precedence rule itself, stated directly.
	for _, entry := range allPromptfooStrategyEntries() {
		if entry.RequiresRemoteGeneration && entry.Unavailable &&
			entry.UnavailableReason != RemoteGenerationUnavailableMessage {
			t.Errorf("strategy %q is remote-gated and also unavailable; U6's message must win", entry.ID)
		}
	}
}

// TestCustomStrategyAllowedKeysMatchEngine keeps the two allow-lists in step.
//
// The platform validates at publication and the engine re-validates at load
// (two independent gates, because the engine is separately deployable). If the
// lists drift, the stricter one silently defines the real contract and the
// other becomes dead code that looks like protection.
func TestCustomStrategyAllowedKeysMatchEngine(t *testing.T) {
	engineKeys := readEngineAllowedStrategyKeys(t)
	if len(engineKeys) == 0 {
		t.Fatal("parsed zero allowed keys from customStrategy.ts")
	}
	// Engine-only keys are the two it owns: they select the endpoint the attack
	// model is called on and must never come from an expert package.
	engineOwned := map[string]bool{"redteamProvider": true, "targetId": true}
	for key := range engineKeys {
		if engineOwned[key] {
			continue
		}
		if !customStrategyAllowedKeys[key] {
			t.Errorf("engine accepts strategy key %q but the platform does not", key)
		}
	}
	for key := range customStrategyAllowedKeys {
		if !engineKeys[key] {
			t.Errorf("platform accepts strategy key %q but the engine does not", key)
		}
	}
	// And the platform must never accept the engine-owned ones.
	for key := range engineOwned {
		if customStrategyAllowedKeys[key] {
			t.Errorf("platform must not accept engine-owned strategy key %q", key)
		}
	}
}

func readEngineAllowedStrategyKeys(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(engineSourcePath(t, filepath.Join("src", "runner", "customStrategy.ts")))
	if err != nil {
		t.Fatalf("read customStrategy.ts: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "export const ALLOWED_STRATEGY_KEYS = new Set([")
	if start < 0 {
		t.Fatal("could not locate ALLOWED_STRATEGY_KEYS in customStrategy.ts")
	}
	end := strings.Index(src[start:], "]);")
	if end < 0 {
		t.Fatal("could not locate the end of ALLOWED_STRATEGY_KEYS")
	}
	body := src[start : start+end]
	out := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		// Comments are interleaved with the keys and several contain quotes, so
		// strip them before extracting — otherwise a commented-out key would be
		// read as a real one.
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "'") {
			continue
		}
		closing := strings.Index(trimmed[1:], "'")
		if closing < 0 {
			continue
		}
		key := trimmed[1 : closing+1]
		// A trailing comma means the entry; a bare string does not.
		if !strings.HasSuffix(strings.TrimSpace(trimmed[closing+2:]), ",") {
			continue
		}
		out[key] = true
	}
	return out
}

// TestPromptfooStrategyCatalogJSONStaysRegenerable proves the committed table is
// exactly what the generator's payload shape describes, so a regeneration is a
// reviewable diff rather than a silent rewrite.
func TestPromptfooStrategyCatalogJSONStaysRegenerable(t *testing.T) {
	var probe struct {
		NativeStrategies []map[string]any `json:"native_strategies"`
	}
	if err := json.Unmarshal(strategyCatalogTableJSON, &probe); err != nil {
		t.Fatalf("committed catalog is not valid JSON: %v", err)
	}
	if len(probe.NativeStrategies) != len(strategyCatalogData.NativeStrategies) {
		t.Fatalf("JSON has %d strategies, parsed %d", len(probe.NativeStrategies), len(strategyCatalogData.NativeStrategies))
	}
	for _, row := range probe.NativeStrategies {
		for _, key := range []string{"id", "availability", "reason", "probe_generated_cases"} {
			if _, ok := row[key]; !ok {
				t.Errorf("strategy row %v is missing %q", row["id"], key)
			}
		}
	}
}
