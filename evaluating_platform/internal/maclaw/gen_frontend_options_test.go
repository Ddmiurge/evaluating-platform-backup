package maclaw

// gen_frontend_options_test.go — catalog single-source-of-truth guard (DD-7=A).
//
// The backend catalog (promptfoo_plugin_catalog.go) is the canonical list of
// promptfoo engine plugins/strategies. The frontend engineEval.ts ships a copy
// (ENGINE_PLUGIN_OPTIONS / ENGINE_STRATEGY_OPTIONS) that used to be kept in sync
// by hand through a dump-only helper whose test always passed. This test turns
// that helper into a real assertion: the id set parsed from the frontend source
// must equal the backend catalog id set in BOTH directions, so any drift fails
// `go test ./...` (and therefore the CI backend job) instead of silently
// shipping.
//
//   go test ./internal/maclaw/ -run TestFrontendOptionsConsistency -v

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// tsOptionIDPattern extracts the `id: '...'` literals from a frontend options
// array. The TS source is not compiled here on purpose: parsing the literal is
// enough and keeps this guard free of a Node toolchain.
var tsOptionIDPattern = regexp.MustCompile(`id:\s*'([^']+)'`)

// frontendEngineEvalPath resolves the frontend service file relative to this
// test file, so the guard works regardless of the `go test` working directory
// (local `internal/maclaw` dir, repo root, or CI checkout).
func frontendEngineEvalPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed to resolve the test file path")
	}
	// <repo>/evaluating_platform/internal/maclaw/gen_frontend_options_test.go
	//   ../..            -> <repo>/evaluating_platform
	//   frontend/...     -> <repo>/evaluating_platform/frontend/src/services/engineEval.ts
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "frontend", "src", "services", "engineEval.ts")
	return filepath.Clean(path)
}

// extractTSArrayIDs returns the `id: '...'` values inside the array literal of
// the given exported const in the frontend source.
func extractTSArrayIDs(t *testing.T, src, constName string) []string {
	t.Helper()
	// Anchoring on the ASSIGNED array literal (not merely the first "[" after the
	// name) is load-bearing: a TypeScript type annotation on the const
	// (`const X: Foo[] = [`) contains its own brackets, and treating that as the
	// array body yields zero ids — a false "catalog drift" that looks like a
	// truncated file but is really a parser bug. Requiring "= [" keeps the guard
	// correct for any annotation style (`ReadonlyArray<T>`, `as const`, none).
	block := tsArrayBlock(t, src, constName)
	matches := tsOptionIDPattern.FindAllStringSubmatch(block, -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m[1])
	}
	// A zero-length result means the parser anchored on the wrong bracket (or
	// the array was emptied). Report that as a parse failure rather than as
	// catalog drift, so the cause is obvious from the message alone.
	if len(ids) == 0 {
		t.Fatalf(
			"frontend engineEval.ts: parsed zero ids from the %q array literal. "+
				"This is a parser/anchor problem, not catalog drift.",
			constName,
		)
	}
	return ids
}

func toIDSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// assertIDSetsMatch fails when the symmetric difference of the two id sets is
// non-empty, printing the drifting ids in each direction.
func assertIDSetsMatch(t *testing.T, label string, frontendIDs, backendIDs []string) {
	t.Helper()
	frontSet := toIDSet(frontendIDs)
	backSet := toIDSet(backendIDs)

	missingInFrontend := make([]string, 0)
	for id := range backSet {
		if !frontSet[id] {
			missingInFrontend = append(missingInFrontend, id)
		}
	}
	extraInFrontend := make([]string, 0)
	for id := range frontSet {
		if !backSet[id] {
			extraInFrontend = append(extraInFrontend, id)
		}
	}
	sort.Strings(missingInFrontend)
	sort.Strings(extraInFrontend)

	if len(missingInFrontend) == 0 && len(extraInFrontend) == 0 {
		return
	}
	t.Fatalf(
		"catalog drift detected (%s): backend has %d ids, frontend has %d ids; "+
			"in backend but missing from frontend = %v; in frontend but missing from backend = %v. "+
			"Regenerate frontend/src/services/engineEval.ts from promptfoo_plugin_catalog.go.",
		label, len(backSet), len(frontSet), missingInFrontend, extraInFrontend,
	)
}

func TestFrontendOptionsConsistency(t *testing.T) {
	path := frontendEngineEvalPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read frontend engine catalog %s: %v", path, err)
	}
	src := string(raw)

	frontendPluginIDs := extractTSArrayIDs(t, src, "ENGINE_PLUGIN_OPTIONS")
	frontendStrategyIDs := extractTSArrayIDs(t, src, "ENGINE_STRATEGY_OPTIONS")

	backendPluginIDs := make([]string, 0, len(promptfooPluginCatalog))
	for _, entry := range promptfooPluginCatalog {
		backendPluginIDs = append(backendPluginIDs, entry.ID)
	}
	// Phase 3 开篇 · 任务 1: the strategy side is no longer the hand-written list
	// alone. `allPromptfooStrategyEntries()` is what the capability catalogue
	// offers — the platform's own dimensions plus every native strategy
	// discovered from promptfoo — so comparing against `promptfooStrategyCatalog`
	// here would let the two drift apart silently, which is the whole failure
	// this test exists to catch. Comparing against the shorter list is how the
	// 7 newly reachable strategies would have been dropped from the UI without
	// anything going red.
	backendStrategyEntries := allPromptfooStrategyEntries()
	backendStrategyIDs := make([]string, 0, len(backendStrategyEntries))
	for _, entry := range backendStrategyEntries {
		// Strategies withheld from the selector are absent from the UI on
		// purpose — a strategy that provably generates nothing must not occupy
		// a slot. U6's remote-gated ones are the exception: they stay visible
		// and greyed so the user learns "exists but blocked here".
		if entry.Unavailable && !entry.RequiresRemoteGeneration {
			continue
		}
		backendStrategyIDs = append(backendStrategyIDs, entry.ID)
	}

	assertIDSetsMatch(t, "ENGINE_PLUGIN_OPTIONS vs promptfooPluginCatalog", frontendPluginIDs, backendPluginIDs)
	assertIDSetsMatch(t, "ENGINE_STRATEGY_OPTIONS vs allPromptfooStrategyEntries", frontendStrategyIDs, backendStrategyIDs)

	// And the front end must not offer anything the backend has withdrawn for a
	// reason U6 does not cover. Asserted separately because the id-set
	// comparison above cannot express "this id must not be here at all".
	for _, id := range []string{"simba", "retry", "mathPrompt"} {
		for _, offered := range backendStrategyIDs {
			if offered == id {
				t.Errorf("strategy %q is withdrawn by the backend but still counted as offered", id)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// P2-2 additions. The id-set comparison above cannot express "this id is flagged
// the same way on both sides", and flags are exactly what drifted: the gate grew
// from four strategies to six while the frontend list still marked four, so the
// selector offered two strategies that the backend refuses. Both assertions below
// exist because that drift was found by hand.
// ---------------------------------------------------------------------------

// tsOptionRemoteGatedPattern finds each option entry and whether it is flagged.
// Entries are one per line in the generated arrays.
var tsOptionRemoteGatedPattern = regexp.MustCompile(`id:\s*'([^']+)'[^\n]*`)

// extractTSFlaggedIDs returns the ids the frontend marks with `flag: true` inside
// an exported const's array literal.
func extractTSFlaggedIDs(t *testing.T, src, constName, flag string) map[string]bool {
	t.Helper()
	block := tsArrayBlock(t, src, constName)
	flagged := map[string]bool{}
	for _, line := range strings.Split(block, "\n") {
		match := tsOptionRemoteGatedPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if strings.Contains(line, flag+": true") {
			flagged[match[1]] = true
		}
	}
	return flagged
}

// tsArrayBlock returns the array literal assigned to an exported const.
//
// Depth-aware, and it skips single-quoted strings: `RETIRED_ENGINE_PLUGINS`
// entries contain a nested `replacement_ids: [...]`, so stopping at the first
// `]` would return one truncated entry. The option arrays happen to have no
// nesting today, which is exactly why a first-`]` scan survived this long.
func tsArrayBlock(t *testing.T, src, constName string) string {
	t.Helper()
	// Anchor on the DECLARATION, not the bare name: a const may be mentioned in an
	// earlier comment (or JSDoc) before it is declared, and anchoring on the first
	// mention then walks forward into whatever array literal comes next — which
	// silently parses the WRONG array. That is how this parser first reported
	// "zero entries" for a table that exists.
	marker := strings.Index(src, "const "+constName)
	if marker < 0 {
		marker = strings.Index(src, constName)
	}
	if marker < 0 {
		t.Fatalf("frontend engineEval.ts: const %q not found", constName)
	}
	assignRel := strings.Index(src[marker:], "= [")
	if assignRel < 0 {
		t.Fatalf("frontend engineEval.ts: could not find the array literal assigned to %q", constName)
	}
	open := marker + assignRel + 2
	depth := 0
	inString := false
	for i := open; i < len(src); i++ {
		switch ch := src[i]; {
		case inString:
			if ch == '\\' {
				i++ // skip the escaped character
				continue
			}
			if ch == '\'' {
				inString = false
			}
		case ch == '\'':
			inString = true
		case ch == '[':
			depth++
		case ch == ']':
			depth--
			if depth == 0 {
				return src[open+1 : i]
			}
		}
	}
	t.Fatalf("frontend engineEval.ts: array end for %q not found", constName)
	return ""
}

// TestFrontendRemoteGenerationFlagsMatchBackend is the drift guard the P2-2 gate
// widening needed: the frontend greys an option from its own flag, so a flag the
// backend does not agree with produces an option the user can pick and the
// backend then refuses — or, in the other direction, an option held back that
// would have worked.
//
// Counter-proof: clear `requires_remote_generation` on `iterative:meta` in
// engineEval.ts → red.
func TestFrontendRemoteGenerationFlagsMatchBackend(t *testing.T) {
	path := frontendEngineEvalPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read frontend engine catalog %s: %v", path, err)
	}
	src := string(raw)

	// Plugins.
	wantPluginGated := map[string]bool{}
	for _, entry := range promptfooPluginCatalog {
		if PluginRequiresRemoteGeneration(entry.ID) {
			wantPluginGated[entry.ID] = true
		}
	}
	assertFlagSetsMatch(t, "ENGINE_PLUGIN_OPTIONS[requires_remote_generation]",
		extractTSFlaggedIDs(t, src, "ENGINE_PLUGIN_OPTIONS", "requires_remote_generation"), wantPluginGated)

	// The same mirroring requirement for `requires_config`: the frontend uses it
	// to warn ("you still need a policy document") and, on submit, to refuse
	// locally before the round trip. A plugin the backend considers config-gated
	// but the form does not would be sent and refused with no warning.
	wantPluginConfig := map[string]bool{}
	for _, entry := range promptfooPluginCatalog {
		if PluginRequiresConfig(entry.ID) {
			wantPluginConfig[entry.ID] = true
		}
	}
	assertFlagSetsMatch(t, "ENGINE_PLUGIN_OPTIONS[requires_config]",
		extractTSFlaggedIDs(t, src, "ENGINE_PLUGIN_OPTIONS", "requires_config"), wantPluginConfig)

	// Strategies: same source as the id comparison, so a strategy withheld from
	// the selector cannot be counted here either.
	wantStrategyGated := map[string]bool{}
	for _, entry := range allPromptfooStrategyEntries() {
		if entry.Unavailable && !entry.RequiresRemoteGeneration {
			continue
		}
		if entry.RequiresRemoteGeneration {
			wantStrategyGated[entry.ID] = true
		}
	}
	assertFlagSetsMatch(t, "ENGINE_STRATEGY_OPTIONS[requires_remote_generation]",
		extractTSFlaggedIDs(t, src, "ENGINE_STRATEGY_OPTIONS", "requires_remote_generation"), wantStrategyGated)
}

func assertFlagSetsMatch(t *testing.T, label string, got, want map[string]bool) {
	t.Helper()
	missing := make([]string, 0)
	for id := range want {
		if !got[id] {
			missing = append(missing, id)
		}
	}
	extra := make([]string, 0)
	for id := range got {
		if !want[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return
	}
	t.Fatalf("%s: requires_remote_generation drifted from the backend; "+
		"backend-gated but not flagged in the frontend = %v; flagged in the frontend but not gated by the backend = %v. "+
		"Regenerate frontend/src/services/engineEval.ts from promptfoo_plugin_catalog.go.",
		label, missing, extra)
}

// tsRetiredEntryPattern captures each RETIRED_ENGINE_PLUGINS entry's id and its
// replacement_ids list.
var tsRetiredEntryPattern = regexp.MustCompile(`id:\s*'([^']+)'[\s\S]*?replacement_ids:\s*\[([^\]]*)\]`)

// TestFrontendRetiredPluginGuidanceMirrorsBackend keeps the withdrawal guidance
// identical on both sides.
//
// The frontend renders it (the "已下线的插件 id 及替代路径" panel) and the backend
// serves it (the 409 body's `guidance`), so an id that differs between them is a
// user being told two different things about the same capability. The panel is
// also where the "where did it go" answer lives, which makes silent drift here
// worse than a cosmetic difference.
//
// Counter-proof: change a replacement id on one side only → red.
func TestFrontendRetiredPluginGuidanceMirrorsBackend(t *testing.T) {
	path := frontendEngineEvalPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read frontend engine catalog %s: %v", path, err)
	}
	block := tsArrayBlock(t, string(raw), "RETIRED_ENGINE_PLUGINS")

	frontend := map[string][]string{}
	for _, match := range tsRetiredEntryPattern.FindAllStringSubmatch(block, -1) {
		ids := make([]string, 0)
		for _, quoted := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(match[2], -1) {
			ids = append(ids, quoted[1])
		}
		sort.Strings(ids)
		frontend[match[1]] = ids
	}
	if len(frontend) == 0 {
		t.Fatal("parsed zero retired entries from the frontend; the parser anchored wrongly")
	}

	backend := PromptfooRetiredPlugins()
	if len(frontend) != len(backend) {
		t.Fatalf("frontend has %d retired entries, backend has %d", len(frontend), len(backend))
	}
	for _, entry := range backend {
		got, ok := frontend[entry.ID]
		if !ok {
			t.Errorf("withdrawn id %q is missing from the frontend guidance", entry.ID)
			continue
		}
		want := append([]string(nil), entry.ReplacementIDs...)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("withdrawn %q: frontend replacements %v, backend %v", entry.ID, got, want)
		}
	}
}

// tsRetiredStrategyPattern captures each RETIRED_ENGINE_STRATEGIES entry's id, its
// replacement_kind and its replacement_ids.
var tsRetiredStrategyPattern = regexp.MustCompile(
	`id:\s*'([^']+)'[\s\S]*?replacement_kind:\s*'([^']+)'[\s\S]*?replacement_ids:\s*\[([^\]]*)\]`,
)

// TestFrontendRetiredStrategyGuidanceMirrorsBackend is P3-1's mirror guard, the
// strategy-side twin of TestFrontendRetiredPluginGuidanceMirrorsBackend.
//
// Both sides render the same sentence to the same user (the frontend in the wizard's
// fold-out, the backend in the 409 body), so a divergence means one user is told the
// capability moved somewhere the other one is not. The *data* comparison is what
// matters: replacement_kind is what the UI branches on, and an empty
// replacement_ids list is meaningful (no equivalent) rather than a gap.
//
// Counter-proof: change a replacement id or a kind on one side → red.
func TestFrontendRetiredStrategyGuidanceMirrorsBackend(t *testing.T) {
	raw, err := os.ReadFile(frontendEngineEvalPath(t))
	if err != nil {
		t.Fatalf("read frontend engine catalog: %v", err)
	}
	block := tsArrayBlock(t, string(raw), "RETIRED_ENGINE_STRATEGIES")

	type mirror struct {
		kind string
		ids  []string
	}
	frontend := map[string]mirror{}
	for _, match := range tsRetiredStrategyPattern.FindAllStringSubmatch(block, -1) {
		ids := make([]string, 0)
		for _, quoted := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(match[3], -1) {
			ids = append(ids, quoted[1])
		}
		sort.Strings(ids)
		frontend[match[1]] = mirror{kind: match[2], ids: ids}
	}
	if len(frontend) == 0 {
		t.Fatal("parsed zero retired strategies from the frontend; the parser anchored wrongly")
	}

	backend := PromptfooRetiredStrategies()
	if len(frontend) != len(backend) {
		t.Fatalf("frontend has %d retired strategies, backend has %d", len(frontend), len(backend))
	}
	for _, entry := range backend {
		got, ok := frontend[entry.ID]
		if !ok {
			t.Errorf("withdrawn strategy %q is missing from the frontend guidance", entry.ID)
			continue
		}
		if got.kind != entry.ReplacementKind {
			t.Errorf("withdrawn %q: frontend kind %q, backend %q", entry.ID, got.kind, entry.ReplacementKind)
		}
		want := append([]string(nil), entry.ReplacementIDs...)
		sort.Strings(want)
		if strings.Join(got.ids, ",") != strings.Join(want, ",") {
			t.Errorf("withdrawn %q: frontend replacements %v, backend %v", entry.ID, got.ids, want)
		}
	}
}
