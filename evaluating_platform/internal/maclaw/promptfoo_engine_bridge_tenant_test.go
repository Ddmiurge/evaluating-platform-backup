package maclaw

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
)

// P26a: the bridge (chat confirm door) shares the registry with the wizard, so
// both doors resolve the SAME way. These tests pin the bridge-side helpers
// directly; the wizard-side end-to-end lives in handler tests.

type bridgeFakeRunner struct {
	enabled    bool
	createReqs []*EngineRunRequest
}

func (f *bridgeFakeRunner) Enabled() bool { return f.enabled }
func (f *bridgeFakeRunner) CreateRun(_ context.Context, run *EngineRunRequest) error {
	f.createReqs = append(f.createReqs, run)
	return nil
}
func (f *bridgeFakeRunner) GetRun(context.Context, string) (*EngineRunStatus, error) {
	return &EngineRunStatus{Phase: EnginePhaseSucceeded}, nil
}

func newBridgeWithRegistry(t *testing.T, registered bool) (*RedteamToolBridge, *bridgeFakeRunner, uuid.UUID) {
	t.Helper()
	shared := NewPromptfooEngineClient("http://shared.invalid", "shared-token", 5)
	fake := &bridgeFakeRunner{enabled: true}
	b := NewRedteamToolBridge(nil, nil, nil)
	b.SetPromptfooEngine(fake, NewEngineRunService(nil), nil, nil)
	userID := uuid.New()
	var mapping *model.MaclawAccountMapping
	if registered {
		mapping = &model.MaclawAccountMapping{EngineBaseURL: "http://dedicated.invalid", EngineTokenCiphertext: []byte{9, 9}, EngineTokenKeyID: "k1"}
	} else {
		mapping = &model.MaclawAccountMapping{}
	}
	b.SetEngineInstances(NewEngineInstanceRegistry(true, shared, &registryMappingStore{mapping: mapping}, func() *appcrypto.KeyStore { ks, _ := newTestKeyStore(t); return ks }(), 5))
	return b, fake, userID
}

func TestBridgeDoorRefusesCodeWithoutDedicatedSameError(t *testing.T) {
	b, fake, userID := newBridgeWithRegistry(t, false)
	runner, baseURL, refusal := b.submitRunnerFor(context.Background(), userID, []string{"custom:p26"})
	if refusal == nil {
		t.Fatal("registry ON + unregistered tenant + code strategy must refuse")
	}
	var typed *EngineCodeStrategyNoDedicatedError
	if !errors.As(refusal, &typed) {
		t.Fatalf("refusal must be the shared typed error, got %T", refusal)
	}
	if runner != nil || baseURL != "" {
		t.Fatal("refused runs resolve to no runner")
	}
	if len(fake.createReqs) != 0 {
		t.Fatal("the refusal must precede any submit")
	}

	// Same door, same inputs, but NO code packages: the run proceeds on the
	// shared engine unchanged — the gate is only about expert code.
	runner, baseURL, refusal = b.submitRunnerFor(context.Background(), userID, nil)
	if refusal != nil || baseURL != "" || runner != PromptfooEngineRunner(fake) {
		t.Fatalf("non-code runs must stay on the shared runner: %#v %q %v", runner, baseURL, refusal)
	}
}

func TestBridgeDoorRegistryOffKeepsShared(t *testing.T) {
	fake := &bridgeFakeRunner{enabled: true}
	b := NewRedteamToolBridge(nil, nil, nil)
	b.SetPromptfooEngine(fake, NewEngineRunService(nil), nil, nil)
	// engineInstances left nil — the pre-P26 deployment shape.
	runner, baseURL, refusal := b.submitRunnerFor(context.Background(), uuid.New(), []string{"custom:x"})
	if refusal != nil || baseURL != "" || runner != PromptfooEngineRunner(fake) {
		t.Fatalf("nil registry must keep the old path exactly: %#v", refusal)
	}
	if _, ok := b.runRunner(context.Background(), uuid.New(), &EngineRunRecord{}); !ok {
		t.Fatal("nil registry must always resolve a lifecycle runner")
	}
}

func TestBridgeRunRunnerFollowsRecordInstance(t *testing.T) {
	b, fake, userID := newBridgeWithRegistry(t, true)
	// Registered tenant: Resolve hits the dedicated client (decrypt of the
	// 2-byte fake ciphertext fails by design → shared fallback); pin the
	// lifecycle rule directly on a record whose URL equals the SHARED base:
	// empty recorded URL always routes to the bridge runner.
	if runner, ok := b.runRunner(context.Background(), userID, &EngineRunRecord{}); !ok || runner != PromptfooEngineRunner(fake) {
		t.Fatal("empty recorded URL must route to the bridge runner")
	}
	// Recorded dedicated URL that the tenant no longer resolves to → refuse
	// (never repoint a live run).
	if _, ok := b.runRunner(context.Background(), userID, &EngineRunRecord{EngineBaseURL: "http://gone-instance"}); ok {
		t.Fatal("a vanished dedicated instance must NOT fall through to shared")
	}
}
