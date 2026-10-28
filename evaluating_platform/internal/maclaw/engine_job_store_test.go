package maclaw

import "testing"

// A progress mirror must not declare the end of a job.
//
// `Complete` is the only writer that attaches report_id and CompletedAt. When the
// mirror flipped Status to succeeded first, the job sat terminal-without-report
// for ~2s: the snapshot suppressed the progress card and could not build a report
// card, and a browser that polled exactly then ended with no card at all
// (record 40, measured 2026-10-27).
func TestUpdateProgressCannotWriteATerminalStatus(t *testing.T) {
	store := NewPlatformEngineJobStore()
	store.Create(&PlatformEngineJob{ID: "pfj-1", SessionID: "sess-1", Status: EvaluationJobStatusRunning})
	job := store.Get("pfj-1")
	if job == nil {
		t.Fatal("Create did not store the job")
	}
	store.UpdateProgress("pfj-1", EvaluationJobStatusRunning, 6, 2, "executing_probes", "执行中", "", 1200)
	if got := store.Get("pfj-1"); got.Status != EvaluationJobStatusRunning || got.ExecutedCount != 2 {
		t.Fatalf("job = %+v, want the running progress applied", got)
	}

	for _, terminal := range []EvaluationJobStatus{EvaluationJobStatusSucceeded, EvaluationJobStatusFailed, EvaluationJobStatusCanceled} {
		store.UpdateProgress("pfj-1", terminal, 6, 6, "", "done", "some_code", 5000)
		got := store.Get("pfj-1")
		if got.Status != EvaluationJobStatusRunning {
			t.Fatalf("mirror wrote terminal status %q; only Complete may end a job", got.Status)
		}
		if got.ErrorCode == "some_code" {
			t.Error("mirror wrote an error code on a terminal transition it must not make")
		}
	}

	store.Complete("pfj-1", EvaluationJobStatusSucceeded, "", "redteam_report_1")
	if got := store.Get("pfj-1"); got.Status != EvaluationJobStatusSucceeded || got.ReportID != "redteam_report_1" {
		t.Fatalf("Complete did not finish the job: %+v", got)
	}
}
