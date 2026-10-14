package maclaw

// promptfoo_engine_num_tests_test.go — P2-2 (S-4) · one cap, three layers.
//
// # The defect this file closes
//
// `num_tests` had three different limits: the wizard accepted 200, the engine
// clamped to 50, and the form maxed at 50. Nothing was broken in isolation —
// the failure was the gap between them: a caller submitting 120 got a 200 OK, a
// run record saying 120, and 50 probes actually executed, with no notice
// anywhere in the response.
//
// # Why a cross-language assertion instead of three unit tests
//
// A Go test asserting EngineMaxNumTests == 50 would pass forever while the
// TypeScript clamp drifted to 40, because nothing checks the other two layers.
// The repo already solves this shape by parsing the TS/TSX source literal
// (gen_frontend_options_test.go does it for the plugin id set), so the same
// discipline applies: read the numbers out of the files that actually enforce
// them, and fail when any of the three moves alone.
//
// Counter-proof: change the wizard to accept 200 again, or the engine clamp to
// 40 → this test fails and names the layer that drifted.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// repoRelativePath resolves a path relative to the evaluating_platform root
// (where both frontend/ and promptfoo_engine/ live), so the guard works from any
// `go test` working directory.
func repoRelativePath(t *testing.T, parts ...string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed to resolve the test file path")
	}
	// <root>/internal/maclaw/<this file> → ../.. → <root>
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Clean(filepath.Join(append([]string{root}, parts...)...))
}

// engineNumTestsClamp reads the literal out of `nativeRedteam.ts` rather than
// restating it: the engine is a separate program and its clamp is the value a
// run actually obeys when the Go guards are bypassed.
func engineNumTestsClamp(t *testing.T) int {
	t.Helper()
	path := repoRelativePath(t, "promptfoo_engine", "src", "runner", "nativeRedteam.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// `const numTests = Math.min(Math.max(request.num_tests, 1), 50);`
	pattern := regexp.MustCompile(`numTests\s*=\s*Math\.min\(\s*Math\.max\(\s*request\.num_tests\s*,\s*1\s*\)\s*,\s*(\d+)\s*\)`)
	match := pattern.FindStringSubmatch(string(raw))
	if match == nil {
		t.Fatalf("could not find the num_tests clamp in %s. If the expression was "+
			"restructured, update engineNumTestsClamp in this file together with it — "+
			"do not delete the guard.", path)
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("clamp literal %q is not an integer: %v", match[1], err)
	}
	return value
}

// frontendNumTestsMax reads the form's InputNumber max out of the wizard page.
func frontendNumTestsMax(t *testing.T) int {
	t.Helper()
	path := repoRelativePath(t, "frontend", "src", "pages", "enterprise", "EngineEvalPage.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pattern := regexp.MustCompile(`<InputNumber[^>]*\bmax=\{(\d+)\}`)
	match := pattern.FindStringSubmatch(string(raw))
	if match == nil {
		t.Fatalf("could not find the num_tests <InputNumber max={…}> in %s. If the field was "+
			"restructured, update frontendNumTestsMax in this file together with it — "+
			"do not delete the guard.", path)
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("form max literal %q is not an integer: %v", match[1], err)
	}
	return value
}

// TestEngineNumTestsCapAgreesAcrossLayers is the S-4 assertion: the wizard's
// cap, the engine's clamp and the form's max are one number.
//
// The frontend is included even though a client-side max is not enforcement:
// it is the number the user sees, so a form that allows 50 while the backend
// accepts 200 (or vice versa) teaches the wrong limit and produces the "it ran
// fewer cases than I asked for" report in the first place.
func TestEngineNumTestsCapAgreesAcrossLayers(t *testing.T) {
	const want = 50
	if EngineMaxNumTests != want {
		t.Errorf("maclaw.EngineMaxNumTests = %d, want %d", EngineMaxNumTests, want)
	}
	if got := engineNumTestsClamp(t); got != EngineMaxNumTests {
		t.Errorf("engine clamp = %d but the Go cap is %d; a request inside the Go cap "+
			"would be silently reduced by the engine", got, EngineMaxNumTests)
	}
	if got := frontendNumTestsMax(t); got != EngineMaxNumTests {
		t.Errorf("form max = %d but the Go cap is %d; the form would teach a limit the "+
			"backend does not enforce", got, EngineMaxNumTests)
	}
}

// TestEngineNumTestsErrorMessageIsActionable pins the refusal's wording: it must
// say the platform will NOT quietly substitute a smaller number, because that
// promise is the difference between this and the clamp it replaced.
func TestEngineNumTestsErrorMessageIsActionable(t *testing.T) {
	err := &EngineNumTestsTooLargeError{Requested: 120, Max: EngineMaxNumTests}
	msg := err.Error()
	for _, want := range []string{"120", strconv.Itoa(EngineMaxNumTests), "不会静默"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message %q does not mention %q", msg, want)
		}
	}
}
