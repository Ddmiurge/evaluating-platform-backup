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
	marker := strings.Index(src, constName)
	if marker < 0 {
		t.Fatalf("frontend engineEval.ts: const %q not found", constName)
	}
	// Anchor on the ASSIGNED array literal, not merely the first "[" after the
	// name. A TypeScript type annotation on the const (`const X: Foo[] = [`)
	// contains its own brackets, and treating that as the array body yields
	// zero ids — a false "catalog drift" that looks like a truncated file but
	// is really a parser bug. Requiring the bracket to be preceded by "= "
	// keeps the guard correct for any annotation style (`ReadonlyArray<T>`,
	// `as const`, no annotation at all).
	assignRel := strings.Index(src[marker:], "= [")
	if assignRel < 0 {
		t.Fatalf(
			"frontend engineEval.ts: could not find the array literal assigned to %q "+
				"(expected `= [` after the const name). If the declaration was restructured, "+
				"update extractTSArrayIDs in this file together with it — do not weaken the guard.",
			constName,
		)
	}
	open := marker + assignRel + 2 // land on the "[" of "= ["
	closeRel := strings.Index(src[open:], "]")
	if closeRel < 0 {
		t.Fatalf("frontend engineEval.ts: array end for %q not found", constName)
	}
	block := src[open : open+closeRel]
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
