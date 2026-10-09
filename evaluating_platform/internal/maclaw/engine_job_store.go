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
	if job.CompletedAt != nil {
		out.CompletedAt = job.CompletedAt
	}
	if job.ReportID != "" {
		out.Progress.CurrentStage = "compile_report"
		out.Result = &EvaluationRunResult{}
	}
	return out
}
