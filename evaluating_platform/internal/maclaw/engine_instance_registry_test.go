package maclaw

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/pkg/config"
	"evaluating_platform/internal/model"
)

type registryMappingStore struct {
	mapping *model.MaclawAccountMapping
	err     error
	calls   int
}

func (s *registryMappingStore) GetByPlatformUserID(_ context.Context, _ uuid.UUID) (*model.MaclawAccountMapping, error) {
	s.calls++
	return s.mapping, s.err
}

func newTestKeyStore(t *testing.T) (*appcrypto.KeyStore, []byte) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	ks, err := appcrypto.NewKeyStore(config.CryptoConfig{
		MasterKey: hex.EncodeToString(key),
		KeyID:     "k1",
	})
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	return ks, key
}

func TestEngineInstanceRegistryDisabledKeepsShared(t *testing.T) {
	shared := NewPromptfooEngineClient("http://shared", "t", 5)
	store := &registryMappingStore{}
	registry := NewEngineInstanceRegistry(false, shared, store, nil, 5)
	if registry.Enabled() {
		t.Fatal("disabled registry must report Enabled()=false")
	}
	client, dedicated := registry.Resolve(context.Background(), uuid.New())
	if client != shared || dedicated {
		t.Fatalf("disabled registry must return the shared client: %#v dedicated=%v", client, dedicated)
	}
	// The disabled path must not even touch the store (byte-for-byte pre-P26).
	if store.calls != 0 {
		t.Fatalf("disabled registry queried mappings %d times", store.calls)
	}
}

func TestEngineInstanceRegistryNilSafe(t *testing.T) {
	var registry *EngineInstanceRegistry
	if registry.Enabled() {
		t.Fatal("nil registry must be disabled")
	}
	shared := NewPromptfooEngineClient("http://shared", "t", 5)
	registry = NewEngineInstanceRegistry(true, shared, nil, nil, 5)
	client, dedicated := registry.Resolve(context.Background(), uuid.New())
	if client != shared || dedicated {
		t.Fatal("enabled registry without stores must fall back to shared")
	}
}

func TestEngineInstanceRegistryResolvesDedicatedClient(t *testing.T) {
	var dedicatedAuth string
	dedicated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /runs is the authenticated surface (/health is a no-auth probe, so it
		// would never show whether the tenant token reached the request).
		dedicatedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer dedicated.Close()

	shared := NewPromptfooEngineClient("http://shared.invalid", "shared-token", 5)
	ks, key := newTestKeyStore(t)
	tenant := uuid.New()
	ciphertext, err := appcrypto.Encrypt([]byte("tenant-token-1234567890abcdef"), key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	store := &registryMappingStore{mapping: &model.MaclawAccountMapping{
		PlatformUserID:        tenant,
		EngineBaseURL:         dedicated.URL,
		EngineTokenCiphertext: ciphertext,
		EngineTokenKeyID:      "k1",
	}}
	registry := NewEngineInstanceRegistry(true, shared, store, ks, 5)
	client, isDedicated := registry.Resolve(context.Background(), tenant)
	if !isDedicated {
		t.Fatal("registered tenant must resolve dedicated")
	}
	if err := client.CreateRun(context.Background(), &EngineRunRequest{RunID: "r-1"}); err != nil {
		t.Fatalf("dedicated submit: %v", err)
	}
	if dedicatedAuth != "Bearer tenant-token-1234567890abcdef" {
		t.Fatalf("dedicated client must carry the DECRYPTED tenant token, got %q", dedicatedAuth)
	}
}

func TestEngineInstanceRegistryDegradationAlwaysShared(t *testing.T) {
	cases := []struct {
		name    string
		mapping *model.MaclawAccountMapping
		err     error
	}{
		{"store error", nil, errors.New("db down")},
		{"no mapping", nil, nil},
		{"empty url", &model.MaclawAccountMapping{}, nil},
		{"url without token", &model.MaclawAccountMapping{EngineBaseURL: "http://dedicated"}, nil},
		{"unknown key id", &model.MaclawAccountMapping{EngineBaseURL: "http://dedicated", EngineTokenCiphertext: []byte{1, 2, 3}, EngineTokenKeyID: "nope"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shared := NewPromptfooEngineClient("http://shared", "t", 5)
			ks, _ := newTestKeyStore(t)
			registry := NewEngineInstanceRegistry(true, shared, &registryMappingStore{mapping: tc.mapping, err: tc.err}, ks, 5)
			client, dedicated := registry.Resolve(context.Background(), uuid.New())
			if client != shared || dedicated {
				t.Fatalf("%s: must fall back to shared, got dedicated=%v", tc.name, dedicated)
			}
		})
	}
}

func TestResolveForRunStaysOnStartInstance(t *testing.T) {
	tenant := uuid.New()
	ks, key := newTestKeyStore(t)
	ciphertext, err := appcrypto.Encrypt([]byte("tok-1234567890"), key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	shared := NewPromptfooEngineClient("http://shared", "t", 5)
	store := &registryMappingStore{mapping: &model.MaclawAccountMapping{
		PlatformUserID:        tenant,
		EngineBaseURL:         "http://dedicated-a",
		EngineTokenCiphertext: ciphertext,
		EngineTokenKeyID:      "k1",
	}}
	registry := NewEngineInstanceRegistry(true, shared, store, ks, 5)

	// Shared-era run (empty recorded URL): shared client, always ok.
	client, ok := registry.ResolveForRun(context.Background(), tenant, "")
	if !ok || client != shared {
		t.Fatalf("empty recorded URL must map to shared, ok=%v", ok)
	}
	// Run recorded on its dedicated instance: resolves to it.
	client, ok = registry.ResolveForRun(context.Background(), tenant, "http://dedicated-a")
	if !ok || client == nil || client.BaseURL() != "http://dedicated-a" {
		t.Fatalf("dedicated run must resolve its own instance, ok=%v", ok)
	}
	// Registration moved elsewhere mid-run: the OLD run must NOT be driven on
	// the new instance — refuse (ghost-run guard).
	store.mapping.EngineBaseURL = "http://dedicated-b"
	if client, ok := registry.ResolveForRun(context.Background(), tenant, "http://dedicated-a"); ok || client != nil {
		t.Fatal("remapped tenant must not route an old run onto the new instance")
	}
}

func TestNoDedicatedErrorIDsAreDeduplicated(t *testing.T) {
	e := &EngineCodeStrategyNoDedicatedError{Strategies: []string{"custom:code:a", "", "custom:code:a", "custom:code:b"}}
	ids := e.CodeStrategyIDs()
	if len(ids) != 2 || ids[0] != "custom:code:a" || ids[1] != "custom:code:b" {
		t.Fatalf("ids = %#v", ids)
	}
	if e.Error() == "" {
		t.Fatal("refusal must carry an actionable message")
	}
}
