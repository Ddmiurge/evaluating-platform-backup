package maclaw

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The refusal code is minted by the engine and explained by the frontend. There
// is no shared header between them: the engine is TypeScript, the platform only
// sees the string in `engine_runs.error_code`. If the two literals drift, the
// hint silently disappears and the operator is back to a bare red tag — the
// exact state this code was added to end (2026-10-26, record 38).
//
// Neither source can be imported here, so both are read from disk. The frontend
// constant is derived from the literal, so a rename on one side fails this test
// instead of shipping a code nobody explains.
func TestModelRefusedCodeMirrorsEngineAndFrontend(t *testing.T) {
	const code = "native_generation_model_refused"

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to resolve the test file path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	sources := map[string]string{
		"engine":   filepath.Join(root, "promptfoo_engine", "src", "runner", "nativeRedteam.ts"),
		"frontend": filepath.Join(root, "frontend", "src", "services", "engineEval.ts"),
	}
	for name, path := range sources {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s source %s: %v", name, path, err)
		}
		if !strings.Contains(string(raw), code) {
			t.Errorf("%s source does not mention %q — the error code and its explanation have drifted apart", name, code)
		}
	}
}
