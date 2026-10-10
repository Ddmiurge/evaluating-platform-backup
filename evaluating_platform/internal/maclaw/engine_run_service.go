package maclaw

// engine_run_service.go — platform-side lifecycle bookkeeping for
// promptfoo-engine runs (Phase 0; §9.1 engine_runs).
//
// The engine adapter (promptfoo_engine container) owns actual execution.
// This service persists safe progress metadata and the redacted aggregate
// result so the BFF can list/poll runs and render reports. Credentials,
// prompts, target responses, and engine-local paths never reach this layer.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Run sources.
const (
	EngineRunSourceWizard = "wizard"
	EngineRunSourceChat   = "chat"
	// EngineRunSourceChatConfirm marks the confirm fast path (U5): the run was
	// started by a `pfj-` platform job created from a chat plan card. Kept
	// distinct from EngineRunSourceChat so operators can tell "user submitted
	// an engine run with a session" from "user clicked 确认执行 on a plan card".
	EngineRunSourceChatConfirm = "chat_confirm"
)

// EngineJobIDPrefix is the prefix of platform-orchestrated engine job ids.
// Kept here (not in the handler) so the service layer can validate the format
// before writing it to the database; the 027 migration enforces the same shape
// with a CHECK constraint as a second line of defense.
const EngineJobIDPrefix = "pfj-"

// IsValidEngineJobID reports whether id is a well-formed `pfj-<uuid>` job id.
// Anything else must be rejected before persistence so that arbitrary user
// content can never reach engine_runs.job_id.
func IsValidEngineJobID(id string) bool {
	if len(id) != len(EngineJobIDPrefix)+36 {
		return false
	}
	if id[:len(EngineJobIDPrefix)] != EngineJobIDPrefix {
		return false
	}
	if _, err := uuid.Parse(id[len(EngineJobIDPrefix):]); err != nil {
		return false
	}
	return true
}

// EngineRunRecord is the persisted row in engine_runs.
// Status semantics mirror engine_runs.status (values shared with
// EngineRunPhase constants defined in promptfoo_engine_client.go).
type EngineRunRecord struct {
	ID             string
	PlatformUserID uuid.UUID
	InstanceID     string
	SessionID      string
	Source         string // wizard | chat | chat_confirm (EngineRunSource*)
	JobID          string // pfj-<uuid> when Source == chat_confirm, else ""
	Engine         string // promptfoo
	Status         EngineRunPhase
	JudgeMode      EngineJudgeMode
	Purpose        string
	Plugins        []EngineCapabilityRef
	Strategies     []EngineCapabilityRef
	TargetID       string
	NumTests       int
	PlannedCount   int
	ExecutedCount  int
	CurrentStage   string
	DurationMs     int64
	StageDurations map[string]int64
	TokenUsage     EngineTokenUsage
	Result         *EngineSafeResult
	ErrorCode      string
	ErrorBrief     string
	StartedAt      *time.Time
	CompletedAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// EngineRunStore is the persistence contract (implemented by repository).
type EngineRunStore interface {
	Create(ctx context.Context, record EngineRunRecord) (*EngineRunRecord, error)
	Update(ctx context.Context, record EngineRunRecord) (*EngineRunRecord, error)
	Get(ctx context.Context, userID uuid.UUID, runID string) (*EngineRunRecord, error)
	List(ctx context.Context, userID uuid.UUID, sessionID string, limit int) ([]EngineRunRecord, error)
	// LinkJob binds a `pfj-` platform job id to an existing run (U5).
	// Implementations MUST make this idempotent: linking the same job id to
	// the same run any number of times leaves exactly one row unchanged, and
	// linking a job id already bound to a different run MUST fail rather than
	// silently move the binding.
	LinkJob(ctx context.Context, userID uuid.UUID, runID, jobID string) (*EngineRunRecord, error)
	// GetByJobID resolves the run behind a `pfj-` job id. It is the restart
	// recovery path: the browser still holds the job id, the in-memory job
	// store is empty, and PostgreSQL is the only remaining source of truth.
	// Returns (nil, nil) when no run is bound to that job id.
	GetByJobID(ctx context.Context, userID uuid.UUID, jobID string) (*EngineRunRecord, error)
}

// EngineRunService orchestrates persistence around engine calls.
type EngineRunService struct {
	store EngineRunStore
	now   func() time.Time
}

func NewEngineRunService(store EngineRunStore) *EngineRunService {
	return &EngineRunService{store: store, now: time.Now}
}

// NewRun builds the initial record for a submitted run.
func (s *EngineRunService) NewRun(userID uuid.UUID, instanceID, sessionID, source, purpose string, judgeMode EngineJudgeMode, numTests int, targetID string, plugins, strategies []EngineCapabilityRef) *EngineRunRecord {
	if !isKnownEngineRunSource(source) {
		source = EngineRunSourceWizard
	}
	return &EngineRunRecord{
		ID:             NewEngineRunID(),
		PlatformUserID: userID,
		InstanceID:     instanceID,
		SessionID:      sessionID,
		Source:         source,
		Engine:         "promptfoo",
		Status:         EnginePhaseQueued,
		JudgeMode:      judgeMode,
		Purpose:        purpose,
		Plugins:        plugins,
		Strategies:     strategies,
		TargetID:       targetID,
		NumTests:       numTests,
		StageDurations: map[string]int64{},
		TokenUsage:     EngineTokenUsage{},
	}
}

// isKnownEngineRunSource mirrors the 027 migration's source CHECK constraint.
// Unknown values fall back to wizard rather than reaching the database, so a
// typo can never trip the constraint at write time.
func isKnownEngineRunSource(source string) bool {
	switch source {
	case EngineRunSourceWizard, EngineRunSourceChat, EngineRunSourceChatConfirm:
		return true
	default:
		return false
	}
}

// LinkJob binds a `pfj-` job id to this run and returns the persisted row.
//
// Idempotency contract (U5 hard requirement — a restart/retry must never
// produce a duplicate run):
//   - same (runID, jobID) linked repeatedly → exactly one row, unchanged.
//   - the underlying store performs a guarded UPDATE
//     (job_id = ” OR job_id = $jobID), so a second write is a no-op.
//   - jobID that is already bound to a *different* run → error, never a
//     silent rebind (that would make one browser job point at two runs).
//
// Fail-closed: an empty or malformed jobID is rejected here, before any SQL,
// so arbitrary user content can never be persisted into engine_runs.job_id.
func (s *EngineRunService) LinkJob(ctx context.Context, userID uuid.UUID, runID, jobID string) (*EngineRunRecord, error) {
	jobID = strings.TrimSpace(jobID)
	if runID == "" {
		return nil, errors.New("engine run id is required")
	}
	if !IsValidEngineJobID(jobID) {
		return nil, fmt.Errorf("invalid engine job id")
	}
	if s == nil || s.store == nil {
		return nil, errors.New("engine run store is not configured")
	}
	return s.store.LinkJob(ctx, userID, runID, jobID)
}

// GetByJobID resolves the run bound to a `pfj-` job id. This is the restart
// recovery path: the browser polls with the job id it received before the
// restart, and PostgreSQL answers even though the in-memory store is empty.
func (s *EngineRunService) GetByJobID(ctx context.Context, userID uuid.UUID, jobID string) (*EngineRunRecord, error) {
	jobID = strings.TrimSpace(jobID)
	if !IsValidEngineJobID(jobID) {
		return nil, nil
	}
	if s == nil || s.store == nil {
		return nil, errors.New("engine run store is not configured")
	}
	return s.store.GetByJobID(ctx, userID, jobID)
}

// PlatformEngineJobFromRecord rebuilds a job DTO from a persisted engine run.
// Used on the recovery path so a post-restart poll renders the same shape the
// browser saw before, even though nothing lives in memory anymore.
func PlatformEngineJobFromRecord(record *EngineRunRecord, userID uuid.UUID) *PlatformEngineJob {
	if record == nil {
		return nil
	}
	jobID := strings.TrimSpace(record.JobID)
	if jobID == "" {
		return nil
	}
	startedAt := record.CreatedAt
	if record.StartedAt != nil {
		startedAt = *record.StartedAt
	}
	return &PlatformEngineJob{
		ID:            jobID,
		EngineRunID:   record.ID,
		UserID:        userID.String(),
		InstanceID:    record.InstanceID,
		SessionID:     record.SessionID,
		Status:        enginePhaseToJobStatus(record.Status),
		PlannedCount:  record.PlannedCount,
		ExecutedCount: record.ExecutedCount,
		CurrentStage:  record.CurrentStage,
		StatusText:    enginePhaseStatusText(record.Status),
		DurationMs:    record.DurationMs,
		ErrorCode:     record.ErrorCode,
		StartedAt:     startedAt,
		CompletedAt:   record.CompletedAt,
		// Recovered marks a job rebuilt from PostgreSQL rather than from the
		// in-memory store, so callers can tell "restored after restart" apart
		// from "live in this process".
		Recovered: true,
	}
}

// ApplyStatus merges a safe engine status snapshot into the record.
// Terminal snapshots also stamp StartedAt/CompletedAt windows.
func (s *EngineRunService) ApplyStatus(record *EngineRunRecord, status *EngineRunStatus) {
	if record == nil || status == nil {
		return
	}
	record.Status = status.Phase
	if status.PlannedCount > 0 {
		record.PlannedCount = status.PlannedCount
	}
	record.ExecutedCount = status.ExecutedCount
	if status.CurrentStage != "" {
		record.CurrentStage = status.CurrentStage
	}
	if status.DurationMs > 0 {
		record.DurationMs = status.DurationMs
	}
	if status.ErrorCode != "" {
		record.ErrorCode = status.ErrorCode
	}
	if status.Result != nil {
		record.Result = status.Result
		record.TokenUsage = status.Result.TokenUsage
	}
	if record.StartedAt == nil && status.Phase != EnginePhaseQueued {
		now := s.now()
		record.StartedAt = &now
	}
	if IsEngineRunTerminal(status.Phase) && record.CompletedAt == nil {
		now := s.now()
		record.CompletedAt = &now
	}
}

// Save persists via Create when the record has never been stored (no
// CreatedAt) and Update otherwise.
func (s *EngineRunService) Save(ctx context.Context, record *EngineRunRecord) (*EngineRunRecord, error) {
	if record.CreatedAt.IsZero() {
		return s.store.Create(ctx, *record)
	}
	return s.store.Update(ctx, *record)
}

func (s *EngineRunService) Get(ctx context.Context, userID uuid.UUID, runID string) (*EngineRunRecord, error) {
	return s.store.Get(ctx, userID, runID)
}

func (s *EngineRunService) List(ctx context.Context, userID uuid.UUID, sessionID string, limit int) ([]EngineRunRecord, error) {
	return s.store.List(ctx, userID, sessionID, limit)
}

// IsEngineRunTerminal reports whether the phase admits no further progress.
func IsEngineRunTerminal(phase EngineRunPhase) bool {
	switch phase {
	case EnginePhaseSucceeded, EnginePhaseFailed, EnginePhaseCanceled:
		return true
	default:
		return false
	}
}

// NewEngineRunID returns a URL-safe engine run id (idempotency key).
// Engine route validation accepts [A-Za-z0-9_-]{1,64}; we keep it lowercase
// hex with the "er-" prefix for readability.
func NewEngineRunID() string {
	return "er-" + strings.ReplaceAll(uuid.NewString(), "-", "")
}
