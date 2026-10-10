package maclaw

// engine_job_store.go — in-memory job store for platform-orchestrated
// promptfoo engine evaluations started from the BFF confirm fast path.
//
// These jobs never touch MaClaw's runtime: the BFF prepares the engine run,
// a background goroutine waits for the terminal state, and the browser polls
// GET /api/v1/maclaw/evaluation/jobs/:id which resolves here first. Safe
// progress metadata only — counts, stages, durations, and report ids.

import (
	"sync"
	"time"
)

// PlatformEngineJobStore is a concurrency-safe in-memory job registry.
// Job entries are lost on backend restart; the underlying engine run
// records remain in the engine_runs table for recovery/audit.
type PlatformEngineJobStore struct {
	mu    sync.RWMutex
	jobs  map[string]*PlatformEngineJob
	order []string
}

// PlatformEngineJob is one platform-orchestrated engine evaluation.
type PlatformEngineJob struct {
	ID            string
	EngineRunID   string
	UserID        string
	InstanceID    string
	SessionID     string
	Status        EvaluationJobStatus
	PlannedCount  int
	ExecutedCount int
	CurrentStage  string
	StatusText    string
	DurationMs    int64
	ErrorCode     string
	ReportID      string
	StartedAt     time.Time
	CompletedAt   *time.Time

	// Recovered is true when this job was rebuilt from PostgreSQL after a
	// backend restart rather than created in this process. It never changes
	// the DTO shape the browser sees — it exists for logs and tests.
	Recovered bool

	// PersistError records a failure to mirror this job into engine_runs
	// (U5 fail-closed guard). When set, the job is NOT silently healthy: it is
	// surfaced through EvaluationJobFromPlatformEngine as the job error so a
	// disconnected PostgreSQL can never look like a normally progressing run.
	PersistError string
}

// MarkPersistFailed flags the job as un-persistable so the next poll reports a
// visible error instead of pretending the run is fine. Called when the
// engine_runs write fails (PostgreSQL down, constraint violation, …).
func (s *PlatformEngineJobStore) MarkPersistFailed(jobID, reason string) {
	if s == nil || jobID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return
	}
	job.PersistError = reason
	// Terminal-fail the job: a run whose progress cannot be recorded is not a
	// run we can honestly keep reporting as "running".
	if !isTerminalEvaluationJobStatus(job.Status) {
		job.Status = EvaluationJobStatusFailed
		if job.ErrorCode == "" {
			job.ErrorCode = "engine_job_persist_failed"
		}
		now := time.Now().UTC()
		if job.CompletedAt == nil {
			job.CompletedAt = &now
		}
	}
}

func NewPlatformEngineJobStore() *PlatformEngineJobStore {
	return &PlatformEngineJobStore{jobs: map[string]*PlatformEngineJob{}}
}

// Create registers a new running job.
func (s *PlatformEngineJobStore) Create(job *PlatformEngineJob) {
	if s == nil || job == nil || job.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return
	}
	s.jobs[job.ID] = job
	s.order = append(s.order, job.ID)
}

// Get returns a copy of the job if present.
func (s *PlatformEngineJobStore) Get(jobID string) *PlatformEngineJob {
	if s == nil || jobID == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if job, ok := s.jobs[jobID]; ok {
		copied := *job
		return &copied
	}
	return nil
}

// UpdateProgress refreshes progress fields for a non-terminal job.
func (s *PlatformEngineJobStore) UpdateProgress(jobID string, status EvaluationJobStatus, planned, executed int, stage, statusText, errorCode string, durationMs int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return
	}
	if isTerminalEvaluationJobStatus(job.Status) {
		return
	}
	job.Status = status
	if planned > 0 {
		job.PlannedCount = planned
	}
	job.ExecutedCount = executed
	if stage != "" {
		job.CurrentStage = stage
	}
	if statusText != "" {
		job.StatusText = statusText
	}
	if errorCode != "" {
		job.ErrorCode = errorCode
	}
	if durationMs > 0 {
		job.DurationMs = durationMs
	}
}

// Complete marks the job terminal with the final report id.
func (s *PlatformEngineJobStore) Complete(jobID string, status EvaluationJobStatus, errorCode, reportID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return
	}
	job.Status = status
	if errorCode != "" {
		job.ErrorCode = errorCode
	}
	if reportID != "" {
		job.ReportID = reportID
	}
	now := time.Now().UTC()
	if job.CompletedAt == nil {
		job.CompletedAt = &now
	}
}

func isTerminalEvaluationJobStatus(status EvaluationJobStatus) bool {
	switch status {
	case EvaluationJobStatusSucceeded, EvaluationJobStatusFailed, EvaluationJobStatusCanceled:
		return true
	default:
		return false
	}
}

// enginePhaseToJobStatus maps an engine_runs.status onto the shared job status
// vocabulary. Single source of truth so the recovery path (engine_run_service)
// and the live path (handler/engine_confirm.go) cannot drift apart.
func enginePhaseToJobStatus(phase EngineRunPhase) EvaluationJobStatus {
	switch phase {
	case EnginePhaseSucceeded:
		return EvaluationJobStatusSucceeded
	case EnginePhaseFailed:
		return EvaluationJobStatusFailed
	case EnginePhaseCanceled:
		return EvaluationJobStatusCanceled
	case EnginePhaseQueued:
		return EvaluationJobStatusPending
	default:
		return EvaluationJobStatusRunning
	}
}

// enginePhaseStatusText renders the human-readable stage label for a phase.
func enginePhaseStatusText(phase EngineRunPhase) string {
	switch phase {
	case EnginePhaseQueued:
		return "排队中"
	case EnginePhaseGeneratingTests:
		return "生成攻击用例"
	case EnginePhaseExecutingProbes:
		return "执行探针"
	case EnginePhaseEngineJudging:
		return "引擎判定"
	case EnginePhaseCompilingReport:
		return "生成报告"
	case EnginePhaseSucceeded:
		return "评测完成"
	case EnginePhaseFailed:
		return "评测失败"
	case EnginePhaseCanceled:
		return "已取消"
	default:
		return "执行中"
	}
}

// EvaluationJobFromPlatformEngine maps a platform engine job into the shared
// EvaluationJob DTO shape so the browser treats both engines uniformly.
func EvaluationJobFromPlatformEngine(job *PlatformEngineJob) *EvaluationJob {
	if job == nil {
		return nil
	}
	out := &EvaluationJob{
		ID:     job.ID,
		Kind:   EvaluationJobKindRun,
		Status: job.Status,
		UserID: job.UserID,
		Progress: &EvaluationJobProgress{
			Phase: string(job.Status),
			// Intentionally no RunID: a platform engine job id (pfj-…) is not a
			// maclaw runtime run id. Filling it here made the browser open
			// /evaluation/runs/pfj-…/events, which the runtime closes instantly;
			// the frontend then reconnected every poll tick, and each close
			// triggered a session snapshot that wiped the optimistic confirm
			// state, flickering cards in and out for the whole run.
			InstanceID:    job.InstanceID,
			SessionID:     job.SessionID,
			StatusText:    job.StatusText,
			PlannedCount:  job.PlannedCount,
			ExecutedCount: job.ExecutedCount,
			CurrentStage:  job.CurrentStage,
			DurationMs:    job.DurationMs,
		},
		Error:     job.ErrorCode,
		CreatedAt: job.StartedAt,
		StartedAt: &job.StartedAt,
	}
	// A persistence failure is reported ahead of the run error: it is the more
	// actionable diagnosis (progress is not being recorded at all).
	if job.PersistError != "" {
		out.Error = job.PersistError
	}
	if job.CompletedAt != nil {
		out.CompletedAt = job.CompletedAt
	}
	if job.ReportID != "" {
		out.Progress.CurrentStage = "compile_report"
		out.Result = &EvaluationRunResult{}
	}
	return out
}
