package maclaw

// engine_job_persistence_test.go — U5 coverage for the `pfj-` job →
// engine_runs binding.
//
// Three properties the design makes hard requirements:
//   ① idempotency — linking the same job repeatedly yields exactly one run,
//     no duplicates (BFF restart / retry safety);
//   ② restart recovery — after the in-memory store is wiped, a poll for the
//     job id still resolves from PostgreSQL;
//   ③ fail-closed — a failed PostgreSQL write is observable, never silent.
//
// Plus the zero-payload-leakage guard: nothing but a BFF-minted `pfj-<uuid>`
// identifier may reach the job_id column.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// newLinkedEngineRun builds a persisted chat_confirm run the way the confirm
// fast path does: create the run, then bind a pfj- job to it.
func newLinkedEngineRun(t *testing.T, svc *EngineRunService, store *fakeEngineRunStore, userID uuid.UUID, jobID string) *EngineRunRecord {
	t.Helper()
	rec := svc.NewRun(userID, "inst-1", "sess-1", EngineRunSourceChatConfirm, "purpose", EngineJudgeAuto, 5, "target-1", nil, nil)
	saved, err := svc.Save(context.Background(), rec)
	if err != nil {
		t.Fatalf("create engine run: %v", err)
	}
	if _, err := svc.LinkJob(context.Background(), userID, saved.ID, jobID); err != nil {
		t.Fatalf("link job: %v", err)
	}
	return saved
}

func newJobID() string {
	return EngineJobIDPrefix + uuid.NewString()
}

// ─── ① Idempotency ────────────────────────────────────────────────

// TestEngineRunLinkSurvivesProgressUpdates is a regression test for a real
// U5 bug: the background wait loop holds its own copy of the record from
// Prepare time, whose JobID is still "". Saving that copy used to blank the
// job_id column, so a restart one minute into a run could no longer recover
// the job — the exact silent loss U5 exists to prevent.
func TestEngineRunLinkSurvivesProgressUpdates(t *testing.T) {
	store := newFakeEngineRunStore()
	svc := NewEngineRunService(store)
	user := uuid.New()
	jobID := newJobID()

	// This is precisely what the confirm fast path does: `inFlight` is the
	// record captured at Prepare time, BEFORE the job id even exists.
	inFlight := svc.NewRun(user, "inst-1", "sess-1", EngineRunSourceChatConfirm, "p", EngineJudgeAuto, 5, "target-1", nil, nil)
	saved, err := svc.Save(context.Background(), inFlight)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The background goroutine captured its own copy here.
	backgroundCopy := *saved
	if backgroundCopy.JobID != "" {
		t.Fatal("precondition: the background copy must not know the job id yet")
	}

	// The handler then links the job id in the database.
	if _, err := svc.LinkJob(context.Background(), user, saved.ID, jobID); err != nil {
		t.Fatalf("link: %v", err)
	}

	// The background loop saves progress using its STALE copy, repeatedly.
	for i := 0; i < 3; i++ {
		svc.ApplyStatus(&backgroundCopy, &EngineRunStatus{
			Phase: EnginePhaseExecutingProbes, PlannedCount: 10, ExecutedCount: i,
		})
		if _, err := svc.Save(context.Background(), &backgroundCopy); err != nil {
			t.Fatalf("progress save %d: %v", i, err)
		}
	}

	// The binding must still be intact, or restart recovery is broken.
	got, err := svc.GetByJobID(context.Background(), user, jobID)
	if err != nil {
		t.Fatalf("GetByJobID: %v", err)
	}
	if got == nil {
		t.Fatal("REGRESSION: progress updates erased the pfj- job link — " +
			"a restart could no longer recover this job")
	}
	if got.ID != saved.ID {
		t.Fatalf("link moved to run %s, want %s", got.ID, saved.ID)
	}
	if got.ExecutedCount != 2 {
		t.Fatalf("progress not applied: executed=%d", got.ExecutedCount)
	}
}

// TestEngineRunLinkJobIdempotent is the DoD idempotency test: the same job
// written N times must leave exactly one record bound to it.
func TestEngineRunLinkJobIdempotent(t *testing.T) {
	store := newFakeEngineRunStore()
	svc := NewEngineRunService(store)
	user := uuid.New()
	jobID := newJobID()

	run := newLinkedEngineRun(t, svc, store, user, jobID)

	// Replay the link 5 more times (simulating retries / restart replays).
	for i := 0; i < 5; i++ {
		if _, err := svc.LinkJob(context.Background(), user, run.ID, jobID); err != nil {
			t.Fatalf("replay %d must be a no-op, got error: %v", i, err)
		}
	}

	// Exactly one run row exists — no duplicate run was created.
	if len(store.records) != 1 {
		t.Fatalf("expected exactly 1 engine_runs row after 6 links, got %d", len(store.records))
	}

	// And exactly one of them is bound to this job id.
	bound := 0
	for _, rec := range store.records {
		if rec.JobID == jobID {
			bound++
		}
	}
	if bound != 1 {
		t.Fatalf("job %s bound to %d runs, want exactly 1", jobID, bound)
	}

	// The binding must resolve back to the same run.
	got, err := svc.GetByJobID(context.Background(), user, jobID)
	if err != nil {
		t.Fatalf("GetByJobID: %v", err)
	}
	if got == nil || got.ID != run.ID {
		t.Fatalf("job resolved to %+v, want run %s", got, run.ID)
	}
}

// TestEngineRunLinkJobRejectsCrossBinding proves one browser job can never end
// up pointing at two runs (which would make recovery ambiguous).
func TestEngineRunLinkJobRejectsCrossBinding(t *testing.T) {
	store := newFakeEngineRunStore()
	svc := NewEngineRunService(store)
	user := uuid.New()
	jobID := newJobID()

	first := newLinkedEngineRun(t, svc, store, user, jobID)
	second := svc.NewRun(user, "", "sess-1", EngineRunSourceChatConfirm, "p", EngineJudgeAuto, 5, "t", nil, nil)
	if _, err := svc.Save(context.Background(), second); err != nil {
		t.Fatalf("create second run: %v", err)
	}

	if _, err := svc.LinkJob(context.Background(), user, second.ID, jobID); err == nil {
		t.Fatal("binding an already-bound job id to a second run must fail")
	}

	// The first binding must be untouched.
	got, err := svc.GetByJobID(context.Background(), user, jobID)
	if err != nil || got == nil || got.ID != first.ID {
		t.Fatalf("binding moved to %+v, want it to stay on %s", got, first.ID)
	}
}

// TestEngineRunLinkJobRejectsMalformedID is the fail-closed guard: only
// `pfj-<uuid>` may be persisted, so arbitrary user content can never reach
// engine_runs.job_id.
func TestEngineRunLinkJobRejectsMalformedID(t *testing.T) {
	store := newFakeEngineRunStore()
	svc := NewEngineRunService(store)
	user := uuid.New()
	rec := svc.NewRun(user, "", "", EngineRunSourceChatConfirm, "p", EngineJudgeAuto, 5, "t", nil, nil)
	if _, err := svc.Save(context.Background(), rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	bad := []string{
		"",
		"   ",
		"pfj-",
		"pfj-not-a-uuid",
		"evil-" + uuid.NewString(),
		strings.Repeat("A", 200),
		// A prompt smuggled into the id column must be refused outright.
		"pfj-" + strings.Repeat("ignore previous instructions ", 3),
	}
	for _, jobID := range bad {
		if _, err := svc.LinkJob(context.Background(), user, rec.ID, jobID); err == nil {
			t.Errorf("malformed job id %q must be rejected", jobID)
		}
	}

	// The guard rejects before touching the store, so zero writes were issued.
	if store.linkN != 0 {
		t.Fatalf("malformed ids must be rejected before persistence, store saw %d link attempts", store.linkN)
	}
	for _, stored := range store.records {
		if stored.JobID != "" {
			t.Fatalf("rejected id leaked into the store: %q", stored.JobID)
		}
	}
}

func TestIsValidEngineJobID(t *testing.T) {
	if !IsValidEngineJobID(newJobID()) {
		t.Fatal("a freshly minted pfj-<uuid> must be valid")
	}
	for _, bad := range []string{"", "pfj-", "er-" + uuid.NewString(), "pfj-xyz", newJobID() + "x"} {
		if IsValidEngineJobID(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

// ─── ② Restart recovery ────────────────────────────────────────────

// TestEngineJobRecoveryAfterRestart simulates a BFF restart: the in-memory
// store is replaced by a brand-new empty instance (exactly what a process
// restart produces) while PostgreSQL keeps the binding. The poll must still
// resolve.
func TestEngineJobRecoveryAfterRestart(t *testing.T) {
	store := newFakeEngineRunStore()
	svc := NewEngineRunService(store)
	user := uuid.New()
	jobID := newJobID()

	// ── before restart ──
	before := NewPlatformEngineJobStore()
	run := newLinkedEngineRun(t, svc, store, user, jobID)
	before.Create(&PlatformEngineJob{
		ID: jobID, EngineRunID: run.ID, UserID: user.String(),
		Status: EvaluationJobStatusRunning, StartedAt: time.Now().UTC(),
	})
	if before.Get(jobID) == nil {
		t.Fatal("job must be visible before restart")
	}

	// Engine makes progress while "the process is down".
	stored := store.records[run.ID]
	svc.ApplyStatus(stored, &EngineRunStatus{
		Phase: EnginePhaseExecutingProbes, PlannedCount: 12, ExecutedCount: 7,
	})
	if _, err := svc.Save(context.Background(), stored); err != nil {
		t.Fatalf("progress save: %v", err)
	}

	// ── restart: a fresh, empty in-memory store ──
	after := NewPlatformEngineJobStore()
	if after.Get(jobID) != nil {
		t.Fatal("fresh store must start empty")
	}

	// Recovery reads from PostgreSQL via the job link.
	record, err := svc.GetByJobID(context.Background(), user, jobID)
	if err != nil || record == nil {
		t.Fatalf("recovery lookup failed: record=%+v err=%v", record, err)
	}
	job := PlatformEngineJobFromRecord(record, user)
	if job == nil {
		t.Fatal("recovery must rebuild a job")
	}
	if job.ID != jobID || job.EngineRunID != run.ID {
		t.Fatalf("recovered wrong job: %+v", job)
	}
	if job.Status != EvaluationJobStatusRunning {
		t.Fatalf("status = %q, want running", job.Status)
	}
	if job.PlannedCount != 12 || job.ExecutedCount != 7 {
		t.Fatalf("progress not recovered: %+v", job)
	}
	if !job.Recovered {
		t.Fatal("recovered job must be flagged Recovered")
	}
	if job.StatusText != enginePhaseStatusText(EnginePhaseExecutingProbes) {
		t.Fatalf("status text = %q", job.StatusText)
	}

	// Re-seeding the cache is idempotent (this is what the poll endpoint does).
	after.Create(job)
	after.Create(job)
	if got := after.Get(jobID); got == nil || got.ID != jobID {
		t.Fatalf("re-seed failed: %+v", got)
	}
}

// TestPlatformEngineJobFromRecordRejectsUnbound guards the recovery path
// against inventing a job for a run that has no pfj- link (e.g. wizard runs).
func TestPlatformEngineJobFromRecordRejectsUnbound(t *testing.T) {
	if got := PlatformEngineJobFromRecord(nil, uuid.New()); got != nil {
		t.Fatal("nil record must yield nil job")
	}
	rec := &EngineRunRecord{ID: "er-1", Source: EngineRunSourceWizard, JobID: ""}
	if got := PlatformEngineJobFromRecord(rec, uuid.New()); got != nil {
		t.Fatalf("run without job_id must not become a pfj job: %+v", got)
	}
}

// TestPlatformEngineJobFromRecordTerminalMapping checks every engine phase maps
// to the right job status after recovery.
func TestPlatformEngineJobFromRecordTerminalMapping(t *testing.T) {
	user := uuid.New()
	completed := time.Now().UTC()
	cases := []struct {
		phase EngineRunPhase
		want  EvaluationJobStatus
	}{
		{EnginePhaseQueued, EvaluationJobStatusPending},
		{EnginePhaseGeneratingTests, EvaluationJobStatusRunning},
		{EnginePhaseExecutingProbes, EvaluationJobStatusRunning},
		{EnginePhaseEngineJudging, EvaluationJobStatusRunning},
		{EnginePhaseCompilingReport, EvaluationJobStatusRunning},
		{EnginePhaseSucceeded, EvaluationJobStatusSucceeded},
		{EnginePhaseFailed, EvaluationJobStatusFailed},
		{EnginePhaseCanceled, EvaluationJobStatusCanceled},
	}
	for _, tc := range cases {
		job := PlatformEngineJobFromRecord(&EngineRunRecord{
			ID: "er-1", JobID: newJobID(), Status: tc.phase, CompletedAt: &completed,
		}, user)
		if job == nil {
			t.Fatalf("phase %q produced no job", tc.phase)
		}
		if job.Status != tc.want {
			t.Errorf("phase %q → status %q, want %q", tc.phase, job.Status, tc.want)
		}
		if job.StatusText == "" {
			t.Errorf("phase %q has empty status text", tc.phase)
		}
	}
}

// TestPlatformEngineJobFromRecordUsesStartedAt verifies the recovery timestamp
// prefers started_at over created_at when both exist.
func TestPlatformEngineJobFromRecordUsesStartedAt(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	started := created.Add(5 * time.Second)
	job := PlatformEngineJobFromRecord(&EngineRunRecord{
		ID: "er-1", JobID: newJobID(), Status: EnginePhaseExecutingProbes,
		CreatedAt: created, StartedAt: &started,
	}, uuid.New())
	if !job.StartedAt.Equal(started) {
		t.Fatalf("StartedAt = %v, want %v", job.StartedAt, started)
	}

	fallback := PlatformEngineJobFromRecord(&EngineRunRecord{
		ID: "er-2", JobID: newJobID(), Status: EnginePhaseQueued, CreatedAt: created,
	}, uuid.New())
	if !fallback.StartedAt.Equal(created) {
		t.Fatalf("StartedAt fallback = %v, want created_at %v", fallback.StartedAt, created)
	}
}

// ─── ③ Fail-closed on PostgreSQL failure ───────────────────────────

// TestEngineRunLinkJobPropagatesStoreError is the DoD reverse test: when
// PostgreSQL is unavailable the failure must surface, never be swallowed into
// an apparently-healthy run.
func TestEngineRunLinkJobPropagatesStoreError(t *testing.T) {
	store := newFakeEngineRunStore()
	svc := NewEngineRunService(store)
	user := uuid.New()
	rec := svc.NewRun(user, "", "", EngineRunSourceChatConfirm, "p", EngineJudgeAuto, 5, "t", nil, nil)
	if _, err := svc.Save(context.Background(), rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	store.linkErr = errors.New("dial tcp 127.0.0.1:5432: connection refused")
	if _, err := svc.LinkJob(context.Background(), user, rec.ID, newJobID()); err == nil {
		t.Fatal("a PostgreSQL failure must be returned to the caller")
	}
}

// reverseTestSkipMarkPersistFailed is a fault-injection switch used only while
// proving the fail-closed guard actually fails (see the reverse-test notes in
// the U5 report). Always false in committed code.
const reverseTestSkipMarkPersistFailed = false

// TestMarkPersistFailedMakesJobObservable is the memory-side guard: when the
// job cannot be recorded, the job must report failure rather than keep
// looking like a healthy running evaluation.
func TestMarkPersistFailedMakesJobObservable(t *testing.T) {
	jobs := NewPlatformEngineJobStore()
	jobID := newJobID()
	jobs.Create(&PlatformEngineJob{
		ID: jobID, EngineRunID: "er-1", UserID: uuid.New().String(),
		Status: EvaluationJobStatusRunning, StartedAt: time.Now().UTC(),
	})

	// REVERSE-TEST: no-op (the pre-U5 behavior) → the observability
	// assertions below must go red, proving they actually bite.
	if !reverseTestSkipMarkPersistFailed {
		jobs.MarkPersistFailed(jobID, "engine_job_persist_failed")
	}

	job := jobs.Get(jobID)
	if job == nil {
		t.Fatal("job must still be observable after a persist failure")
	}
	if job.PersistError == "" {
		t.Fatal("persist error must be recorded on the job")
	}
	if job.Status != EvaluationJobStatusFailed {
		t.Fatalf("status = %q, want failed — a job nobody records must not look healthy", job.Status)
	}

	// And it must reach the browser as an error, not as a running job.
	dto := EvaluationJobFromPlatformEngine(job)
	if dto.Error != "engine_job_persist_failed" {
		t.Fatalf("dto error = %q, want engine_job_persist_failed", dto.Error)
	}
	if dto.Status == EvaluationJobStatusRunning {
		t.Fatal("dto must not report a persisted-failed job as running")
	}
}

// TestMarkPersistFailedPreservesTerminalResult ensures the guard never
// overwrites an already-terminal job's error code with the persist error.
func TestMarkPersistFailedPreservesTerminalResult(t *testing.T) {
	jobs := NewPlatformEngineJobStore()
	jobID := newJobID()
	jobs.Create(&PlatformEngineJob{
		ID: jobID, EngineRunID: "er-1", UserID: uuid.New().String(),
		Status: EvaluationJobStatusFailed, ErrorCode: "engine_execution_failed",
		StartedAt: time.Now().UTC(),
	})

	jobs.MarkPersistFailed(jobID, "engine_job_persist_failed")

	job := jobs.Get(jobID)
	if job.Status != EvaluationJobStatusFailed {
		t.Fatalf("status = %q, want failed", job.Status)
	}
	// The root cause (execution failed) must survive.
	if job.ErrorCode != "engine_execution_failed" {
		t.Fatalf("ErrorCode = %q, want the original engine failure preserved", job.ErrorCode)
	}
	if job.PersistError != "engine_job_persist_failed" {
		t.Fatalf("PersistError = %q, want it still recorded", job.PersistError)
	}
}

// TestMarkPersistFailedNoOpOnUnknownJob keeps the guard nil-safe.
func TestMarkPersistFailedNoOpOnUnknownJob(t *testing.T) {
	jobs := NewPlatformEngineJobStore()
	jobs.MarkPersistFailed("pfj-does-not-exist", "boom") // must not panic
	jobs.MarkPersistFailed("", "boom")                   // must not panic
	var nilStore *PlatformEngineJobStore
	nilStore.MarkPersistFailed("x", "boom") // must not panic
	if jobs.Get("pfj-does-not-exist") != nil {
		t.Fatal("unknown job must not be created by the guard")
	}
}

// TestGetByJobIDIgnoresMalformedAndPropagatesErrors covers the recovery
// read path's two failure modes.
func TestGetByJobIDIgnoresMalformedAndPropagatesErrors(t *testing.T) {
	store := newFakeEngineRunStore()
	svc := NewEngineRunService(store)
	user := uuid.New()

	// Malformed ids resolve to "no job" without touching the store, so a junk
	// poll id can never become a database lookup.
	got, err := svc.GetByJobID(context.Background(), user, "not-a-job-id")
	if err != nil || got != nil {
		t.Fatalf("malformed id must yield (nil, nil), got (%+v, %v)", got, err)
	}

	// A real PostgreSQL failure must propagate so the caller can log it.
	store.getByJobErr = errors.New("db down")
	if _, err := svc.GetByJobID(context.Background(), user, newJobID()); err == nil {
		t.Fatal("store error must propagate on the recovery path")
	}
}

// TestEngineRunServiceLinkJobRequiresRunID covers the fail-closed guard on the
// link target.
func TestEngineRunServiceLinkJobRequiresRunID(t *testing.T) {
	svc := NewEngineRunService(newFakeEngineRunStore())
	if _, err := svc.LinkJob(context.Background(), uuid.New(), "", newJobID()); err == nil {
		t.Fatal("empty run id must be rejected")
	}
}

// ─── ④ Source semantics ────────────────────────────────────────────

func TestEngineRunSourceAcceptsChatConfirm(t *testing.T) {
	svc := NewEngineRunService(newFakeEngineRunStore())
	user := uuid.New()

	rec := svc.NewRun(user, "", "sess-1", EngineRunSourceChatConfirm, "p", EngineJudgeAuto, 5, "t", nil, nil)
	if rec.Source != EngineRunSourceChatConfirm {
		t.Fatalf("source = %q, want chat_confirm", rec.Source)
	}

	// wizard / chat must keep working (no regression for existing links).
	if got := svc.NewRun(user, "", "", EngineRunSourceWizard, "p", EngineJudgeAuto, 5, "t", nil, nil).Source; got != EngineRunSourceWizard {
		t.Fatalf("wizard source regressed: %q", got)
	}
	if got := svc.NewRun(user, "", "s", EngineRunSourceChat, "p", EngineJudgeAuto, 5, "t", nil, nil).Source; got != EngineRunSourceChat {
		t.Fatalf("chat source regressed: %q", got)
	}
	// Unknown values still fall back to wizard (mirrors the 027 CHECK).
	if got := svc.NewRun(user, "", "", "chat_confrm", "p", EngineJudgeAuto, 5, "t", nil, nil).Source; got != EngineRunSourceWizard {
		t.Fatalf("typo source = %q, want wizard fallback", got)
	}
}
