package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/model"
	"evaluating_platform/pkg/config"
)

// P26a door wiring (wizard): with the tenant registry ON the wizard must
// (a) SUBMIT a CODE strategy run to the tenant's dedicated instance and record
// that instance on the run, and (b) refuse the same run with a 409 pre-flight
// rejection — no engine call, no run row — when the tenant has no dedicated
// instance. Registry OFF keeps the shared path (existing
// TestWizardForwardsTheCodeStrategyChannel covers that regression).

type wizardMappingStore struct {
	mapping *model.MaclawAccountMapping
}

func (s *wizardMappingStore) GetByPlatformUserID(context.Context, uuid.UUID) (*model.MaclawAccountMapping, error) {
	return s.mapping, nil
}

func newWizardRegistry(t *testing.T, dedicatedURL string) *maclaw.EngineInstanceRegistry {
	t.Helper()
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 7)
	}
	ks, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: hex.EncodeToString(masterKey), KeyID: "k1"})
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	var mapping *model.MaclawAccountMapping
	if dedicatedURL != "" {
		ciphertext, err := appcrypto.Encrypt([]byte("dedicated-token-0123456789"), masterKey)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		mapping = &model.MaclawAccountMapping{
			EngineBaseURL:         dedicatedURL,
			EngineTokenCiphertext: ciphertext,
			EngineTokenKeyID:      "k1",
		}
	} else {
		mapping = &model.MaclawAccountMapping{}
	}
	return maclaw.NewEngineInstanceRegistry(true, maclaw.NewPromptfooEngineClient("http://shared.invalid", "shared-token", 5), &wizardMappingStore{mapping: mapping}, ks, 5)
}

func wizardCodeResolver() *fakeCapabilityResolver {
	return &fakeCapabilityResolver{caps: &maclaw.ResolvedEngineCapabilities{
		EnginePlugins:      []maclaw.EngineCapabilityRef{{ID: "contracts"}},
		EngineStrategies:   []maclaw.EngineCapabilityRef{{ID: "custom:p26probe"}},
		RecordPlugins:      []maclaw.EngineCapabilityRef{{ID: "contracts"}},
		RecordStrategies:   []maclaw.EngineCapabilityRef{{ID: "custom:p26probe"}},
		CustomStrategyCode: []maclaw.EngineCustomStrategy{{Handle: "h", StrategyID: "custom:p26probe", Definition: "export default {}", Form: maclaw.CustomStrategyFormJS}},
	}}
}

func wizardTargetService() *fakeTargetConfigService {
	return &fakeTargetConfigService{target: &maclaw.EvaluationTargetInput{
		ID: "t1", BaseURL: "https://target.example/v1", Model: "m", CredentialSecret: "sk", Enabled: true,
	}}
}

const wizardCodeRunBody = `{"purpose":"合规安全评测","plugins":[{"id":"contracts"}],"strategies":[{"id":"pf_strategy:h"}],"num_tests":1}`

func TestWizardRoutesCodeStrategyToDedicatedInstance(t *testing.T) {
	store := newFakeRunStore()
	sharedFake := newEnabledFakeEngine()
	h := newEngineRunHandler(sharedFake, maclaw.NewEngineRunService(store), wizardTargetService(), newFakeGenerationProvider())
	h.SetEngineCapabilityResolver(wizardCodeResolver())

	var dedicatedAuth, dedicatedPath string
	dedicated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dedicatedAuth = r.Header.Get("Authorization")
		dedicatedPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer dedicated.Close()
	h.SetEngineInstances(newWizardRegistry(t, dedicated.URL))
	r := newEngineRunRouter(h, "enterprise")

	w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", wizardCodeRunBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if dedicatedPath != "/runs" {
		t.Fatalf("the code run must be SUBMITTED to the dedicated instance; it saw path %q", dedicatedPath)
	}
	if dedicatedAuth != "Bearer dedicated-token-0123456789" {
		t.Fatalf("the dedicated submit must carry the tenant token, got %q", dedicatedAuth)
	}
	if sharedFake.createReq != nil {
		t.Fatal("the shared engine must NOT receive a registered tenant's code run")
	}
	records, _ := store.List(context.Background(), uuid.MustParse("11111111-1111-1111-1111-111111111111"), "", 10)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].EngineBaseURL != strings.TrimRight(dedicated.URL, "/") {
		t.Fatalf("the run must RECORD the instance it started on, got %q", records[0].EngineBaseURL)
	}
}

func TestWizardRefusesCodeStrategyWithoutDedicatedInstance(t *testing.T) {
	store := newFakeRunStore()
	sharedFake := newEnabledFakeEngine()
	h := newEngineRunHandler(sharedFake, maclaw.NewEngineRunService(store), wizardTargetService(), newFakeGenerationProvider())
	h.SetEngineCapabilityResolver(wizardCodeResolver())
	h.SetEngineInstances(newWizardRegistry(t, "")) // registry ON, tenant NOT registered
	r := newEngineRunRouter(h, "enterprise")

	w := doJSON(r, http.MethodPost, "/maclaw/engine/runs", wizardCodeRunBody)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want 409", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != maclaw.EngineErrCodeStrategyNoDedicatedInstance {
		t.Fatalf("code = %v, want %q", body["code"], maclaw.EngineErrCodeStrategyNoDedicatedInstance)
	}
	if ids, ok := body["code_strategies"].([]any); !ok || len(ids) != 1 || ids[0] != "custom:p26probe" {
		t.Fatalf("the refusal must name the refused packages, got %v", body["code_strategies"])
	}
	if sharedFake.createReq != nil {
		t.Fatal("a refused run must never reach the engine")
	}
	records, _ := store.List(context.Background(), uuid.MustParse("11111111-1111-1111-1111-111111111111"), "", 10)
	if len(records) != 0 {
		t.Fatal("a refused run must not be persisted — the refusal happens before the run row exists")
	}
}

// The browser-visible DTO contract must NOT leak the instance routing: the
// base URL is compose-internal and the run detail already shows everything a
// user can act on.
func TestEngineRunResponseHidesTenantRoutingInternals(t *testing.T) {
	record := &maclaw.EngineRunRecord{
		ID: "er-1", PlatformUserID: uuid.New(), Status: maclaw.EnginePhaseSucceeded,
		EngineBaseURL: "http://promptfoo-engine-acme:8090",
	}
	payload := engineRunResponse(record, nil)
	if _, leaked := payload["engine_base_url"]; leaked {
		t.Fatal("the run DTO must not carry the tenant engine URL")
	}
	raw, _ := json.Marshal(payload)
	if strings.Contains(string(raw), "promptfoo-engine-acme") {
		t.Fatalf("internal instance name reached the browser: %s", raw)
	}
}
