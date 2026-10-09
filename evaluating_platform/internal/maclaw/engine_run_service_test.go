package maclaw

// engine_run_service_test.go — covers the pure bookkeeping logic of the
// engine run service with an in-memory fake store. SQL-level behavior is
// exercised by repository integration tests elsewhere.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeEngineRunStore struct {
	records map[string]*EngineRunRecord
	createN int
	updateN int
	getErr  error
}

func newFakeEngineRunStore() *fakeEngineRunStore {
	return &fakeEngineRunStore{records: map[string]*EngineRunRecord{}}
}

func (s *fakeEngineRunStore) Create(_ context.Context, record EngineRunRecord) (*EngineRunRecord, error) {
	s.createN++
	now := time.Now().UTC()
	record.CreatedAt = now
	record.UpdatedAt = now
	snapshot := record
	s.records[record.ID] = &snapshot
	return &snapshot, nil
}

func (s *fakeEngineRunStore) Update(_ context.Context, record EngineRunRecord) (*EngineRunRecord, error) {
	s.updateN++
	if _, ok := s.records[record.ID]; !ok {
		return nil, errors.New("not found")
	}
	record.UpdatedAt = time.Now().UTC()
	snapshot := record
	s.records[record.ID] = &snapshot
	return &snapshot, nil
}

func (s *fakeEngineRunStore) Get(_ context.Context, userID uuid.UUID, runID string) (*EngineRunRecord, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if rec, ok := s.records[runID]; ok {
		if rec.PlatformUserID != uuid.Nil && rec.PlatformUserID != userID {
			return nil, nil
		}
		snapshot := *rec
		return &snapshot, nil
	}
	return nil, nil
}

func (s *fakeEngineRunStore) List(_ context.Context, _ uuid.UUID, _ string, _ int) ([]EngineRunRecord, error) {
	out := []EngineRunRecord{}
	for _, rec := range s.records {
		out = append(out, *rec)
	}
	return out, nil
}

func TestNewEngineRunIDFormat(t *testing.T) {
	id := NewEngineRunID()
	if len(id) != 3+32 || id[:3] != "er-" {
		t.Fatalf("unexpected id %q", id)
	}
	for _, ch := range id[3:] {
		if !(ch >= '0' && ch <= '9') && !(ch >= 'a' && ch <= 'f') {
			t.Fatalf("id contains non-hex char %q in %q", ch, id)
		}
	}
	if NewEngineRunID() == id {
		t.Fatal("ids collided")
	}
}

func TestEngineRunServiceNewRunDefaults(t *testing.T) {
	store := newFakeEngineRunStore()
	svc := NewEngineRunService(store)
	user := uuid.New()
	plugins := []EngineCapabilityRef{{ID: "harmful"}, {ID: "pii", Config: map[string]interface{}{"k": "v"}}}

	t.Run("wizard default", func(t *testing.T) {
		rec := svc.NewRun(user, "inst-1", "", "wizard", "合规安全评测", EngineJudgeAuto, 20, "target-1", plugins, nil)
		if rec.Status != EnginePhaseQueued || rec.Source != EngineRunSourceWizard || rec.Engine != "promptfoo" {
			t.Fatalf("unexpected %+v", rec)
		}
		if rec.Purpose != "合规安全评测" || rec.JudgeMode != EngineJudgeAuto || rec.NumTests != 20 || rec.TargetID != "target-1" {
			t.Fatalf("unexpected %+v", rec)
		}
		if len(rec.Plugins) != 2 || rec.StageDurations == nil || rec.Result != nil {
			t.Fatalf("unexpected %+v", rec)
		}
	})

	t.Run("chat source kept, invalid falls back", func(t *testing.T) {
		rec := svc.NewRun(user, "", "sess-1", "chat", "p", EngineJudgePromptfooNative, 5, "t", nil, nil)
		if rec.Source != EngineRunSourceChat {
			t.Fatalf("source = %q, want chat", rec.Source)
		}
		rec2 := svc.NewRun(user, "", "", "bogus", "p", EngineJudgeAuto, 5, "t", nil, nil)
		if rec2.Source != EngineRunSourceWizard {
			t.Fatalf("source = %q, want wizard fallback", rec2.Source)
		}
	})
}

func TestEngineRunServiceApplyStatusProgress(t *testing.T) {
	svc := NewEngineRunService(newFakeEngineRunStore())
	rec := &EngineRunRecord{Status: EnginePhaseQueued, StageDurations: map[string]int64{}}

	svc.ApplyStatus(rec, &EngineRunStatus{
		Phase:        EnginePhaseGeneratingTests,
		PlannedCount: 10,
	})
	if rec.Status != EnginePhaseGeneratingTests || rec.PlannedCount != 10 {
		t.Fatalf("unexpected %+v", rec)
	}
	if rec.StartedAt == nil {
		t.Fatal("StartedAt should be stamped on first non-queued phase")
	}
	if rec.CompletedAt != nil {
		t.Fatal("CompletedAt must stay nil for non-terminal phase")
	}

	// A queued snapshot must not clear started_at once set.
	svc.ApplyStatus(rec, &EngineRunStatus{Phase: EnginePhaseQueued})
	if rec.StartedAt == nil {
		t.Fatal("StartedAt lost on queued snapshot")
	}
}

func TestEngineRunServiceApplyStatusTerminal(t *testing.T) {
	svc := NewEngineRunService(newFakeEngineRunStore())
	rec := &EngineRunRecord{Status: EnginePhaseExecutingProbes}

	svc.ApplyStatus(rec, &EngineRunStatus{
		Phase:      EnginePhaseFailed,
		ErrorCode:  "target_call_failed",
		DurationMs: 1234,
		Result: &EngineSafeResult{
			Totals:     EngineRunTotals{Probes: 3, AttackSuccess: 1},
			TokenUsage: EngineTokenUsage{Generation: 10, Judging: 20, Target: 30},
		},
	})
	if rec.Status != EnginePhaseFailed || rec.ErrorCode != "target_call_failed" || rec.DurationMs != 1234 {
		t.Fatalf("unexpected %+v", rec)
	}
	if rec.CompletedAt == nil {
		t.Fatal("CompletedAt must be stamped on terminal phase")
	}
	if rec.Result == nil || rec.Result.Totals.Probes != 3 {
		t.Fatalf("result not merged: %+v", rec.Result)
	}
	if rec.TokenUsage.Generation != 10 || rec.TokenUsage.Judging != 20 || rec.TokenUsage.Target != 30 {
		t.Fatalf("token usage not merged: %+v", rec.TokenUsage)
	}
}

func TestEngineRunServiceApplyStatusNilSafety(t *testing.T) {
	svc := NewEngineRunService(newFakeEngineRunStore())
	svc.ApplyStatus(nil, nil) // must not panic
	rec := &EngineRunRecord{Status: EnginePhaseQueued}
	svc.ApplyStatus(rec, nil) // must not panic
	if rec.Status != EnginePhaseQueued {
		t.Fatal("nil status must not mutate record")
	}
}

func TestIsEngineRunTerminal(t *testing.T) {
	terminal := []EngineRunPhase{EnginePhaseSucceeded, EnginePhaseFailed, EnginePhaseCanceled}
	for _, p := range terminal {
		if !IsEngineRunTerminal(p) {
			t.Errorf("phase %q should be terminal", p)
		}
	}
	nonTerminal := []EngineRunPhase{
		EnginePhaseQueued, EnginePhaseGeneratingTests, EnginePhaseExecutingProbes,
		EnginePhaseEngineJudging, EnginePhaseCompilingReport,
	}
	for _, p := range nonTerminal {
		if IsEngineRunTerminal(p) {
			t.Errorf("phase %q should not be terminal", p)
		}
	}
}

func TestEngineRunServiceSaveCreateThenUpdate(t *testing.T) {
	store := newFakeEngineRunStore()
	svc := NewEngineRunService(store)
	user := uuid.New()

	rec := svc.NewRun(user, "", "sess-1", "chat", "purpose", EngineJudgePromptfooNative, 5, "target-1", nil, nil)
	saved, err := svc.Save(context.Background(), rec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createN != 1 || store.updateN != 0 {
		t.Fatalf("createN=%d updateN=%d", store.createN, store.updateN)
	}

	svc.ApplyStatus(saved, &EngineRunStatus{Phase: EnginePhaseExecutingProbes, ExecutedCount: 2})
	saved2, err := svc.Save(context.Background(), saved)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if store.createN != 1 || store.updateN != 1 {
		t.Fatalf("createN=%d updateN=%d", store.createN, store.updateN)
	}
	if saved2.ExecutedCount != 2 {
		t.Fatalf("executed=%d", saved2.ExecutedCount)
	}

	got, err := svc.Get(context.Background(), user, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.ID != rec.ID || got.Status != EnginePhaseExecutingProbes {
		t.Fatalf("unexpected stored record %+v", got)
	}

	if _, err := svc.Get(context.Background(), user, "nope"); err != nil {
		t.Fatalf("get missing should be nil,nil: %v", err)
	}
}

func TestEngineRunServiceGetPropagatesStoreError(t *testing.T) {
	store := newFakeEngineRunStore()
	store.getErr = errors.New("db down")
	svc := NewEngineRunService(store)
	if _, err := svc.Get(context.Background(), uuid.New(), "x"); err == nil {
		t.Fatal("expected propagated error")
	}
}