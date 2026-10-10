package handler

// engine_job_recovery_test.go — U5 coverage for the job poll endpoint.
//
// The browser polls GET /maclaw/evaluation/jobs/:id with the `pfj-` job id it
// received at confirm time. Two behaviors are asserted here:
//
//  ① Restart recovery — with an EMPTY in-memory job store (what a BFF restart
//     produces), the endpoint still resolves the job from PostgreSQL.
//  ② Fail-closed — when that PostgreSQL read fails, the endpoint reports
//     "not found" instead of fabricating a healthy-looking job.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
)

// newJobRecoveryHandler wires a handler with an empty job store (restart state).
func newJobRecoveryHandler(t *testing.T, store maclaw.EngineRunStore) *MaclawEvaluationHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &MaclawEvaluationHandler{}
	h.SetPromptfooEngineJobs(maclaw.NewPlatformEngineJobStore(), maclaw.NewEngineRunService(store))
	return h
}

// callJob runs the platformEngineJob resolution used by the jobs endpoint.
func callJob(t *testing.T, h *MaclawEvaluationHandler, userID, jobID string) *maclaw.EvaluationJob {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/jobs/"+jobID, nil)
	c.Set("user_id", userID)
	return h.platformEngineJob(c, jobID)
}

// TestPlatformEngineJobRecoversAfterRestart is the DoD "restart the BFF and the
// frontend progress comes back" test at the HTTP layer.
//
// The restart is simulated by handing the handler a brand-new empty
// PlatformEngineJobStore — byte-for-byte the state a fresh process starts in —
// while the fake PostgreSQL keeps the run and its pfj- binding.
func TestPlatformEngineJobRecoversAfterRestart(t *testing.T) {
	store := newFakeRunStore()
	svc := maclaw.NewEngineRunService(store)
	user := uuid.New()
	jobID := maclaw.EngineJobIDPrefix + uuid.NewString()

	// ── before restart: run created and job linked, as confirm fast path does ──
	run := svc.NewRun(user, "inst-1", "sess-1", maclaw.EngineRunSourceChatConfirm,
		"大模型安全评测", maclaw.EngineJudgeAuto, 5, "target-1", nil, nil)
	saved, err := svc.Save(context.Background(), run)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := svc.LinkJob(context.Background(), user, saved.ID, jobID); err != nil {
		t.Fatalf("link job: %v", err)
	}

	// Engine progresses while "the process is down".
	svc.ApplyStatus(saved, &maclaw.EngineRunStatus{
		Phase: maclaw.EnginePhaseExecutingProbes, PlannedCount: 20, ExecutedCount: 9,
	})
	if _, err := svc.Save(context.Background(), saved); err != nil {
		t.Fatalf("progress save: %v", err)
	}

	// ── restart ──
	h := newJobRecoveryHandler(t, store)
	if h.engineJobs.Get(jobID) != nil {
		t.Fatal("precondition: the restarted store must be empty")
	}

	job := callJob(t, h, user.String(), jobID)
	if job == nil {
		t.Fatal("job must be recovered from PostgreSQL after a restart")
	}
	if job.ID != jobID {
		t.Fatalf("recovered id = %q, want %q", job.ID, jobID)
	}
	if job.Status != maclaw.EvaluationJobStatusRunning {
		t.Fatalf("status = %q, want running", job.Status)
	}
	if job.Progress == nil {
		t.Fatal("recovered job must carry progress")
	}
	if job.Progress.PlannedCount != 20 || job.Progress.ExecutedCount != 9 {
		t.Fatalf("progress not recovered: %+v", job.Progress)
	}

	// The recovery re-seeds the cache, so the next poll is served from memory.
	if h.engineJobs.Get(jobID) == nil {
		t.Fatal("recovery must re-seed the in-memory cache")
	}
}

// TestPlatformEngineJobUnknownJobStillNotFound keeps the recovery path from
// turning every unknown id into a 200.
func TestPlatformEngineJobUnknownJobStillNotFound(t *testing.T) {
	h := newJobRecoveryHandler(t, newFakeRunStore())
	if got := callJob(t, h, uuid.New().String(), maclaw.EngineJobIDPrefix+uuid.NewString()); got != nil {
		t.Fatalf("unknown job must resolve to nil, got %+v", got)
	}
}

// TestPlatformEngineJobRecoveryFailsClosed is the DoD reverse test: with
// PostgreSQL unreachable the endpoint must NOT return a healthy-looking job.
// A fabricated "running" job is exactly the silent failure U5 forbids.
func TestPlatformEngineJobRecoveryFailsClosed(t *testing.T) {
	store := newFakeRunStore()
	store.getByJobErr = errors.New("dial tcp 127.0.0.1:5432: connection refused")
	user := uuid.New()
	jobID := maclaw.EngineJobIDPrefix + uuid.NewString()

	h := newJobRecoveryHandler(t, store)
	got := callJob(t, h, user.String(), jobID)
	if got != nil {
		t.Fatalf("a failed recovery must not fabricate a job, got %+v", got)
	}
	if h.engineJobs.Get(jobID) != nil {
		t.Fatal("a failed recovery must not seed the cache")
	}
}

// TestPlatformEngineJobRecoveryRejectsOtherUsers proves the recovery path is
// still tenant-scoped: a job id is not a bearer token.
func TestPlatformEngineJobRecoveryRejectsOtherUsers(t *testing.T) {
	store := newFakeRunStore()
	svc := maclaw.NewEngineRunService(store)
	owner := uuid.New()
	jobID := maclaw.EngineJobIDPrefix + uuid.NewString()

	run := svc.NewRun(owner, "", "sess-1", maclaw.EngineRunSourceChatConfirm, "p", maclaw.EngineJudgeAuto, 5, "t", nil, nil)
	saved, _ := svc.Save(context.Background(), run)
	if _, err := svc.LinkJob(context.Background(), owner, saved.ID, jobID); err != nil {
		t.Fatalf("link: %v", err)
	}

	h := newJobRecoveryHandler(t, store)
	if got := callJob(t, h, uuid.New().String(), jobID); got != nil {
		t.Fatalf("another user must not recover this job, got %+v", got)
	}
}

// TestPlatformEngineJobRecoverySerializesSafely asserts the recovered DTO
// round-trips as JSON without leaking a run id the runtime would reject.
// (The pfj- id must never appear in Progress.RunID — see engine_job_store.go.)
func TestPlatformEngineJobRecoverySerializesSafely(t *testing.T) {
	store := newFakeRunStore()
	svc := maclaw.NewEngineRunService(store)
	user := uuid.New()
	jobID := maclaw.EngineJobIDPrefix + uuid.NewString()

	run := svc.NewRun(user, "inst-1", "sess-1", maclaw.EngineRunSourceChatConfirm, "p", maclaw.EngineJudgeAuto, 5, "t", nil, nil)
	saved, _ := svc.Save(context.Background(), run)
	if _, err := svc.LinkJob(context.Background(), user, saved.ID, jobID); err != nil {
		t.Fatalf("link: %v", err)
	}

	h := newJobRecoveryHandler(t, store)
	job := callJob(t, h, user.String(), jobID)
	if job == nil {
		t.Fatal("expected a recovered job")
	}

	blob, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	progress, _ := decoded["progress"].(map[string]any)
	if progress == nil {
		t.Fatal("progress missing from the response")
	}
	if _, hasRunID := progress["run_id"]; hasRunID {
		t.Fatalf("progress.run_id must stay empty for a pfj- job, got %v", progress["run_id"])
	}
}

// TestEngineConfirmLinkFailureIsObservable covers the confirm-time guard: when
// the pfj- ↔ run link cannot be written, the in-memory job is marked failed
// (with the persist error surfaced) instead of silently continuing to "run".
func TestEngineConfirmLinkFailureIsObservable(t *testing.T) {
	jobs := maclaw.NewPlatformEngineJobStore()
	jobID := maclaw.EngineJobIDPrefix + uuid.NewString()
	jobs.Create(&maclaw.PlatformEngineJob{
		ID: jobID, EngineRunID: "er-1", UserID: uuid.New().String(),
		Status: maclaw.EvaluationJobStatusRunning, StartedAt: time.Now().UTC(),
	})

	// This mirrors exactly what engine_confirm.go does on a LinkJob error.
	jobs.MarkPersistFailed(jobID, "engine_job_persist_failed")

	blob, err := json.Marshal(maclaw.EvaluationJobFromPlatformEngine(jobs.Get(jobID)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["status"] != string(maclaw.EvaluationJobStatusFailed) {
		t.Fatalf("status = %v, want failed", decoded["status"])
	}
	if decoded["error"] != "engine_job_persist_failed" {
		t.Fatalf("error = %v, want engine_job_persist_failed", decoded["error"])
	}
}
