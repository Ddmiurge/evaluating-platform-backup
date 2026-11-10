package maclaw

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestEmbeddedStateLabelsMatchFrontendMirror is the source-level guard for the
// state→label pairing: the Go machine states are the source of truth, the FE
// mirror renders them. A one-sided rename would leave the portal showing raw
// machine codes (or a stale Chinese label for a state that moved) — the same
// drift class as the judge-track caption and retired-strategy mirrors.
func TestEmbeddedStateLabelsMatchFrontendMirror(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "frontend", "src", "services", "embeddedPluginStates.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read FE state mirror: %v", err)
	}
	src := string(raw)
	states := []string{
		EmbeddedStatePendingInitialReview,
		EmbeddedStateRejected,
		EmbeddedStateApprovedForBuild,
		EmbeddedStateBuildRequested,
		EmbeddedStateBuildFailed,
		EmbeddedStateTesting,
		EmbeddedStateApproved,
		EmbeddedStateDisabled,
		EmbeddedStateRemoveRequested,
		EmbeddedStateRemoved,
	}
	for _, state := range states {
		label := EmbeddedStateLabel(state)
		if label == state {
			t.Fatalf("state %q has no Chinese label — add it to EmbeddedStateLabel and the FE mirror together", state)
		}
		want := fmt.Sprintf("%s: '%s'", state, label)
		if !strings.Contains(src, want) {
			t.Fatalf("frontend mirror must contain %s (label drift: FE shows something else for this state)", want)
		}
	}
}
