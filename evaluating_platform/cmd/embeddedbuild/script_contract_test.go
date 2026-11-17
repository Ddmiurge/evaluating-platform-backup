package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// E-4d-4 · the deploy script's hot-path contract, asserted at the source level
// (design §14.4: bash -n + 源级断言 HUP/轮询/finalize 顺序).
//
// The script runs only on the deploy host, so the ordering it promises —
// stage → SIGHUP → poll both receipts → finalize, with the hot branch doing
// NO build and NO recreate — is otherwise only checkable by running it there.
// These assertions fail on a one-sided edit the same way the caption mirror
// tests do.
func TestEmbeddedBuildScriptHotPathContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "embedded-build.sh"))
	if err != nil {
		t.Fatalf("read embedded-build.sh: %v", err)
	}
	src := string(raw)

	mustContain := []string{
		// The receipt contract both sides must echo.
		"docker kill -s HUP",
		"embedded_batch",
		"embedded_catalog_hash",
		// Fail-fast reasons (static strings from the reload hooks).
		"malformed_index",
		"missing_index",
		"not_patched",
		// Batch id parsed from stage output — the value the poll matches on.
		"awk '/^STAGED /",
	}
	for _, needle := range mustContain {
		if !strings.Contains(src, needle) {
			t.Errorf("script is missing %q", needle)
		}
	}

	// Order: the poll is defined, then HUP is sent inside it, then finalize
	// runs only after the poll returned success (同步了再显示). The finalize
	// marker is the actual invocation — the header comment mentions it too.
	hup := strings.Index(src, "docker kill -s HUP")
	finalize := strings.Index(src, "go run ./cmd/embeddedbuild -finalize -ok")
	poll := strings.Index(src, "sync_hot_batch()")
	switch {
	case hup < 0 || finalize < 0 || poll < 0:
		t.Fatalf("markers not found (hup=%d finalize=%d poll=%d)", hup, finalize, poll)
	case finalize < hup:
		t.Error("SIGHUP must be sent before -finalize -ok (同步了再显示)")
	case hup < poll:
		t.Error("the sync poll must be defined before the HUP inside it")
	case finalize < poll:
		t.Error("-finalize -ok must come after the receipt poll")
	}

	// The hot branch (--skip-build) must contain no build and no recreate: a
	// zero-downtime batch keeps container uptime (§14.5-1). Extract from the
	// branch header to the build log of the else-arm.
	skipIdx := strings.Index(src, "if [[ $SKIP_BUILD -eq 1 ]]")
	buildLogIdx := strings.Index(src, `log "build: promptfoo-engine and backend"`)
	if skipIdx < 0 || buildLogIdx < 0 || buildLogIdx < skipIdx {
		t.Fatalf("skip-build branch layout changed (skip=%d buildLog=%d)", skipIdx, buildLogIdx)
	}
	hotSegment := src[skipIdx:buildLogIdx]
	for _, banned := range []string{"docker compose build", "force-recreate"} {
		if strings.Contains(hotSegment, banned) {
			t.Errorf("hot branch must not %q — that is the build path's job", banned)
		}
	}
	if !strings.Contains(hotSegment, "sync_hot_batch") {
		t.Error("hot branch must drive the receipt poll")
	}
}
