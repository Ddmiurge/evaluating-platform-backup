package repository

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// GetReportByRunID must join on the ENGINE run id, and that id is not in `run_id`.
//
// This is a source-level guard on purpose. The query only runs against Postgres,
// which the test suite does not have, and the in-memory fake used by the service
// tests mirrors the SQL — so a wrong WHERE clause and a fake kept in sync with it
// would agree with each other while every real request 404s. That is exactly what
// shipped: `run_id = $2`, 0 of 72 rows carried an `er-…` run_id, and the first
// field verification found the button dead for every run (2026-10-27).
//
// Counter-proof: restore `AND run_id = $2` → red.
func TestGetReportByRunIDJoinsOnTheEngineRunID(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "maclaw_redteam_artifact.go"))
	if err != nil {
		t.Fatalf("read repository source: %v", err)
	}
	source := string(raw)

	start := strings.Index(source, "func (r *MaclawRedteamArtifactRepository) GetReportByRunID(")
	if start < 0 {
		t.Fatal("GetReportByRunID is gone — the run→report lookup the history download depends on")
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatal("cannot delimit GetReportByRunID")
	}
	body := source[start : start+end]

	for _, want := range []string{"engine_run_id = $2", "metadata->>'engine_run_id' = $2"} {
		if !strings.Contains(body, want) {
			t.Errorf("GetReportByRunID no longer matches on %q — the engine run id lives there, not in run_id", want)
		}
	}
	if strings.Contains(body, "AND run_id = $2") {
		t.Error("GetReportByRunID joins on run_id, which holds the planner's id (msg_… / run_…), never the engine run id")
	}
}
