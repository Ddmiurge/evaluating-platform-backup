package maclaw

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
	"evaluating_platform/pkg/config"
)

// P26c registration core. The properties that must not rot: the token exists
// in plaintext exactly twice (return value, generation input) and is stored
// only encrypted; a dead URL is caught at registration, not by the first
// routed run; re-registration rotates and leaves one row; unregister falls
// back to shared.

type adminStore struct {
	mapping     *model.MaclawAccountMapping
	setCalls    int
	cleared     int
	lastBase    string
	lastCipher  []byte
	lastKeyID   string
	setErr      error
}

func (s *adminStore) GetByPlatformUserID(context.Context, uuid.UUID) (*model.MaclawAccountMapping, error) {
	return s.mapping, nil
}

func (s *adminStore) SetEngineInstance(_ context.Context, _ uuid.UUID, baseURL string, cipher []byte, keyID string) (bool, error) {
	if s.setErr != nil {
		return false, s.setErr
	}
	s.setCalls++
	s.lastBase, s.lastCipher, s.lastKeyID = baseURL, cipher, keyID
	return true, nil
}

func (s *adminStore) ClearEngineInstance(context.Context, uuid.UUID) (bool, error) {
	s.cleared++
	return true, nil
}

func newAdminFixture(t *testing.T, store *adminStore, proberURL string) *EngineInstanceAdmin {
	t.Helper()
	ks, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: strings.Repeat("7a", 32), KeyID: "k1"})
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	a := NewEngineInstanceAdmin(store, ks)
	if proberURL != "" {
		target := proberURL
		a.prober = func(baseURL string) EngineInstanceProber {
			return &urlProber{target: target, asked: baseURL}
		}
	}
	return a
}

type urlProber struct {
	target string
	asked  string
	err    error
}

func (p *urlProber) Health(ctx context.Context) error {
	if p.err != nil {
		return p.err
	}
	return errors.New("stub")
}

func TestValidateEngineInstanceBaseURL(t *testing.T) {
	ok := []string{"http://promptfoo-engine-acme:8090", "https://engine.example.com/v1/"}
	for _, v := range ok {
		if got, err := ValidateEngineInstanceBaseURL(v); err != nil {
			t.Fatalf("%s rejected: %v", v, err)
		} else if strings.HasSuffix(got, "/") {
			t.Fatalf("trailing slash not trimmed: %s", got)
		}
	}
	bad := []string{"", "promptfoo-engine:8090", "http://user:pass@host", "http://host?token=x", "http://host#f", "ftp://host", "http:///nohost", "http://h/" + strings.Repeat("x", 250)}
	for _, v := range bad {
		if _, err := ValidateEngineInstanceBaseURL(v); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
}

func TestRegisterStoresCiphertextOnlyAndReturnsTokenOnce(t *testing.T) {
	store := &adminStore{mapping: &model.MaclawAccountMapping{PlatformUserID: uuid.New()}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	a := newAdminFixture(t, store, server.URL)
	// The fixture prober always errors after recording; use a live target so
	// health passes: swap in a prober that hits server.URL directly.
	a.prober = func(baseURL string) EngineInstanceProber { return NewPromptfooEngineClient(baseURL, "", 5) }

	token, err := a.Register(context.Background(), uuid.New(), server.URL)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(token) != 48 {
		t.Fatalf("token = %q, want 48 hex chars", token)
	}
	if _, derr := hex.DecodeString(token); derr != nil {
		t.Fatalf("token not hex: %v", derr)
	}
	if store.lastCipher == nil || bytesContains(store.lastCipher, []byte(token)) {
		t.Fatal("stored value must be the encrypted token, never the plaintext")
	}
	if store.lastBase != server.URL {
		t.Fatalf("stored base = %q", store.lastBase)
	}
	// Rotation: second registration issues a DIFFERENT token, still one row.
	token2, err := a.Register(context.Background(), uuid.New(), server.URL)
	if err != nil || token2 == token {
		t.Fatalf("re-register must rotate: %v %v", token2 == token, err)
	}
	if store.setCalls != 2 {
		t.Fatalf("setCalls = %d", store.setCalls)
	}
}

func TestRegisterRejectsDeadInstanceAndMissingAccount(t *testing.T) {
	dead := &urlProber{err: errors.New("connection refused")}
	store := &adminStore{mapping: &model.MaclawAccountMapping{}}
	a := newAdminFixture(t, store, "")
	a.prober = func(string) EngineInstanceProber { return dead }
	if _, err := a.Register(context.Background(), uuid.New(), "http://promptfoo-engine-acme:8090"); err == nil {
		t.Fatal("dead instance must refuse registration")
	}
	if store.setCalls != 0 {
		t.Fatal("refused registration must not write")
	}
	a2 := newAdminFixture(t, &adminStore{mapping: nil}, "")
	a2.prober = func(string) EngineInstanceProber { return &urlProber{} }
	if _, err := a2.Register(context.Background(), uuid.New(), "http://host"); err == nil {
		t.Fatal("unprovisioned account must be refused")
	}
}

func TestUnregisterClears(t *testing.T) {
	store := &adminStore{}
	a := newAdminFixture(t, store, "")
	if err := a.Unregister(context.Background(), uuid.New()); err != nil || store.cleared != 1 {
		t.Fatalf("unregister: %v calls=%d", err, store.cleared)
	}
}

func bytesContains(hay, needle []byte) bool {
	return strings.Contains(string(hay), string(needle))
}
