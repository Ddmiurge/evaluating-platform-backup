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
	"strings"
	"time"

	"github.com/google/uuid"
)

// Run sources.
const (
	EngineRunSourceWizard = "wizard"
	EngineRunSourceChat   = "chat"
)

// EngineRunRecord is the persisted row in engine_runs.
// Status semantics mirror engine_runs.status (values shared with
// EngineRunPhase constants defined in promptfoo_engine_client.go).
type EngineRunRecord struct {
	ID             string
	PlatformUserID uuid.UUID
	InstanceID     string
	SessionID      string
	Source         string // wizard | chat (EngineRunSourceWizard/EngineRunSourceChat)
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
	if source != EngineRunSourceChat {
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