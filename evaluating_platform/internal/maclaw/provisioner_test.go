package maclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
	"evaluating_platform/pkg/config"
)

func TestProvisionerCreatesIndependentRuntimeForEachPlatformAccount(t *testing.T) {
	ctx := context.Background()
	store := newFakeMappingStore()
	users := fakeUserLookup{
		user("enterprise@example.com", model.RoleEnterprise),
		user("expert@example.com", model.RoleExpert),
	}
	admin := &fakeProvisioningAdmin{}
	provisioner := newTestProvisioner(t, users, store, admin)

	enterprise, err := provisioner.EnsureRuntime(ctx, users[0].ID)
	if err != nil {
		t.Fatalf("EnsureRuntime enterprise: %v", err)
	}
	expert, err := provisioner.EnsureRuntime(ctx, users[1].ID)
	if err != nil {
		t.Fatalf("EnsureRuntime expert: %v", err)
	}

	if enterprise.Mapping.MaclawTenantID == expert.Mapping.MaclawTenantID {
		t.Fatalf("expected distinct tenant ids, got %q", enterprise.Mapping.MaclawTenantID)
	}
	if enterprise.InstanceID == expert.InstanceID {
		t.Fatalf("expected distinct instance ids, got %q", enterprise.InstanceID)
	}
	if clientAPIToken(enterprise.Client) == "" {
		t.Fatalf("expected provisioned runtime client")
	}
	if clientAPIToken(expert.Client) == "" {
		t.Fatalf("expected provisioned expert runtime client")
	}
	if admin.createdTenants != 2 {
		t.Fatalf("created tenants = %d, want 2", admin.createdTenants)
	}

	stored := store.mappings[users[0].ID]
	if stored == nil {
		t.Fatalf("missing stored enterprise mapping")
	}
	for label, secret := range map[string][]byte{
		"api key":      []byte("api-key-1"),
		"api secret":   []byte("api-secret-1"),
		"access token": []byte("token-api-key-1"),
	} {
		if bytes.Contains(stored.EncryptedAPIKey, secret) ||
			bytes.Contains(stored.EncryptedAPISecret, secret) ||
			bytes.Contains(stored.EncryptedAccessToken, secret) {
			t.Fatalf("%s was stored in plaintext", label)
		}
	}
}

func TestProvisionerReusesExistingMappingAndRefreshesExpiredToken(t *testing.T) {
	ctx := context.Background()
	store := newFakeMappingStore()
	users := fakeUserLookup{user("enterprise@example.com", model.RoleEnterprise)}
	admin := &fakeProvisioningAdmin{}
	provisioner := newTestProvisioner(t, users, store, admin)

	first, err := provisioner.EnsureRuntime(ctx, users[0].ID)
	if err != nil {
		t.Fatalf("EnsureRuntime first: %v", err)
	}
	if admin.createdTenants != 1 {
		t.Fatalf("created tenants = %d, want 1", admin.createdTenants)
	}

	second, err := provisioner.EnsureRuntime(ctx, users[0].ID)
	if err != nil {
		t.Fatalf("EnsureRuntime second: %v", err)
	}
	if second.InstanceID != first.InstanceID {
		t.Fatalf("instance id changed from %q to %q", first.InstanceID, second.InstanceID)
	}
	if admin.createdTenants != 1 {
		t.Fatalf("created tenants after reuse = %d, want 1", admin.createdTenants)
	}
	if admin.tokenIssues != 1 {
		t.Fatalf("token issues after cached reuse = %d, want 1", admin.tokenIssues)
	}

	expired := time.Now().Add(-time.Minute)
	store.mappings[users[0].ID].AccessTokenExpiresAt = &expired
	refreshed, err := provisioner.EnsureRuntime(ctx, users[0].ID)
	if err != nil {
		t.Fatalf("EnsureRuntime refresh: %v", err)
	}
	if refreshed.InstanceID != first.InstanceID {
		t.Fatalf("instance id changed after refresh from %q to %q", first.InstanceID, refreshed.InstanceID)
	}
	if admin.createdTenants != 1 {
		t.Fatalf("created tenants after refresh = %d, want 1", admin.createdTenants)
	}
	if admin.tokenIssues != 2 {
		t.Fatalf("token issues after refresh = %d, want 2", admin.tokenIssues)
	}
}

func TestProvisionerSerializesConcurrentEnsureRuntimeForSameUser(t *testing.T) {
	ctx := context.Background()
	store := newFakeMappingStore()
	users := fakeUserLookup{user("enterprise@example.com", model.RoleEnterprise)}
	admin := &fakeProvisioningAdmin{createTenantDelay: 20 * time.Millisecond}
	provisioner := newTestProvisioner(t, users, store, admin)

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	instances := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime, err := provisioner.EnsureRuntime(ctx, users[0].ID)
			if err != nil {
				errs <- err
				return
			}
			instances <- runtime.InstanceID
		}()
	}
	wg.Wait()
	close(errs)
	close(instances)

	for err := range errs {
		t.Fatalf("EnsureRuntime concurrent: %v", err)
	}
	var first string
	for instanceID := range instances {
		if first == "" {
			first = instanceID
			continue
		}
		if instanceID != first {
			t.Fatalf("concurrent calls returned different instances: %q and %q", first, instanceID)
		}
	}
	if admin.createdTenants != 1 {
		t.Fatalf("created tenants = %d, want 1", admin.createdTenants)
	}
	if admin.tokenIssues != 1 {
		t.Fatalf("token issues = %d, want 1", admin.tokenIssues)
	}
}

func TestProvisioningGatewayProviderResolveIgnoresCallerCancellation(t *testing.T) {
	store := newFakeMappingStore()
	users := fakeUserLookup{user("enterprise@example.com", model.RoleEnterprise)}
	admin := &fakeProvisioningAdmin{}
	provisioner := newTestProvisioner(t, users, store, admin)
	provider := NewProvisioningGatewayProvider(provisioner)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	session, err := provider.Resolve(ctx, RuntimeIdentity{UserID: users[0].ID.String(), Role: string(model.RoleEnterprise)})
	if err != nil {
		t.Fatalf("Resolve with canceled caller context: %v", err)
	}
	if session == nil || session.InstanceID == "" || session.Mapping == nil {
		t.Fatalf("Resolve returned incomplete session: %#v", session)
	}
	if admin.createdTenants != 1 {
		t.Fatalf("created tenants = %d, want 1", admin.createdTenants)
	}
}

func TestProvisionerReprovisionsWhenStoredCredentialIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newFakeMappingStore()
	users := fakeUserLookup{user("expert@example.com", model.RoleExpert)}
	admin := &fakeProvisioningAdmin{}
	provisioner := newTestProvisioner(t, users, store, admin)

	first, err := provisioner.EnsureRuntime(ctx, users[0].ID)
	if err != nil {
		t.Fatalf("EnsureRuntime first: %v", err)
	}
	expired := time.Now().Add(-time.Minute)
	store.mappings[users[0].ID].AccessTokenExpiresAt = &expired
	admin.rejectedAPIKeys = map[string]bool{"api-key-1": true}

	second, err := provisioner.EnsureRuntime(ctx, users[0].ID)
	if err != nil {
		t.Fatalf("EnsureRuntime after rejected credential: %v", err)
	}
	if second.InstanceID == first.InstanceID {
		t.Fatalf("instance should be reprovisioned after rejected credential, still %q", second.InstanceID)
	}
	if second.Mapping.MaclawTenantID == first.Mapping.MaclawTenantID {
		t.Fatalf("tenant should be reprovisioned after rejected credential, still %q", second.Mapping.MaclawTenantID)
	}
	if admin.createdTenants != 2 {
		t.Fatalf("created tenants = %d, want 2", admin.createdTenants)
	}
	if clientAPIToken(second.Client) != "token-api-key-2" {
		t.Fatalf("client token = %q, want new credential token", clientAPIToken(second.Client))
	}
}

func TestProvisionerAppliesDefaultRuntimeConfigBeforeCreatingInstance(t *testing.T) {
	ctx := context.Background()
	store := newFakeMappingStore()
	users := fakeUserLookup{user("enterprise@example.com", model.RoleEnterprise)}
	admin := &fakeProvisioningAdmin{}
	provisioner := newTestProvisioner(t, users, store, admin)
	provisioner.SetDefaultConfigProvider(fakeDefaultRuntimeConfigProvider{cfg: &RuntimeAppConfig{
		MaclawLLMProviders: []RuntimeLLMProvider{{
			Name:  "openai-prod",
			URL:   "https://api.example/v1",
			Key:   "sk-default",
			Model: "gpt-test",
		}},
		MaclawLLMCurrentProvider: "openai-prod",
	}})

	if _, err := provisioner.EnsureRuntime(ctx, users[0].ID); err != nil {
		t.Fatalf("EnsureRuntime: %v", err)
	}

	if len(admin.updatedConfigs) != 1 {
		t.Fatalf("updated configs = %d, want 1", len(admin.updatedConfigs))
	}
	if admin.updatedConfigs[0].token != "token-api-key-1" {
		t.Fatalf("config token = %q", admin.updatedConfigs[0].token)
	}
	if admin.updatedConfigs[0].cfg.MaclawLLMProviders[0].Key != "sk-default" {
		t.Fatalf("default config key was not passed to maclaw")
	}
	if !admin.configUpdatedBeforeInstance {
		t.Fatalf("default config was not applied before instance creation")
	}
}

func TestProvisionerAppliesHubConfigBeforeCreatingInstance(t *testing.T) {
	ctx := context.Background()
	store := newFakeMappingStore()
	users := fakeUserLookup{user("enterprise@example.com", model.RoleEnterprise)}
	admin := &fakeProvisioningAdmin{}
	provisioner := newTestProvisioner(t, users, store, admin)
	provisioner.SetHubConfigProvider(fakeHubRuntimeConfigProvider{cfg: &RuntimeAppConfig{
		RemoteHubURL:        "https://hub.example.test",
		SkillSourcesAllowed: []string{"skillhub"},
	}})

	if _, err := provisioner.EnsureRuntime(ctx, users[0].ID); err != nil {
		t.Fatalf("EnsureRuntime: %v", err)
	}

	if len(admin.updatedConfigs) != 1 {
		t.Fatalf("updated configs = %d, want 1", len(admin.updatedConfigs))
	}
	if admin.updatedConfigs[0].cfg.RemoteHubURL != "https://hub.example.test" {
		t.Fatalf("hub url = %q", admin.updatedConfigs[0].cfg.RemoteHubURL)
	}
	if len(admin.updatedConfigs[0].cfg.SkillSourcesAllowed) != 1 || admin.updatedConfigs[0].cfg.SkillSourcesAllowed[0] != "skillhub" {
		t.Fatalf("skill sources = %#v", admin.updatedConfigs[0].cfg.SkillSourcesAllowed)
	}
	if !admin.configUpdatedBeforeInstance {
		t.Fatalf("hub config was not applied before instance creation")
	}
}

func TestProvisionerEnsuresRedteamMCPBridgeForExistingMapping(t *testing.T) {
	ctx := context.Background()
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.String())
		if got := r.Header.Get("Authorization"); got != "Bearer token-api-key-1" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/mcp/servers":
			_ = json.NewEncoder(w).Encode(struct {
				Items []MCPServerView `json:"items"`
			}{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/mcp/servers":
			var in MCPServerInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatalf("decode mcp input: %v", err)
			}
			if in.Name != RedteamMCPBridgeName || in.EndpointURL != "https://platform.internal/api/v1/internal/maclaw/redteam-mcp" {
				t.Fatalf("mcp input = %#v", in)
			}
			if in.AuthSecret != "bridge-secret" {
				t.Fatalf("auth secret was not sent for server-side registration")
			}
			_ = json.NewEncoder(w).Encode(MCPServerView{ID: "mcp_1", Name: in.Name, HasAuthSecret: true})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	store := newFakeMappingStore()
	users := fakeUserLookup{user("enterprise@example.com", model.RoleEnterprise)}
	admin := &fakeProvisioningAdmin{}
	provisioner := newTestProvisionerWithBaseURLAndKind(t, users, store, admin, server.URL, RuntimeKindMaclawSrv)

	if _, err := provisioner.EnsureRuntime(ctx, users[0].ID); err != nil {
		t.Fatalf("initial EnsureRuntime: %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("redteam bridge should not be touched before config is set: %v", seen)
	}

	provisioner.SetRedteamMCPBridgeConfig(RedteamMCPBridgeProvisioningConfig{
		EndpointURL: "https://platform.internal/api/v1/internal/maclaw/redteam-mcp",
		AuthSecret:  "bridge-secret",
	})
	if _, err := provisioner.EnsureRuntime(ctx, users[0].ID); err != nil {
		t.Fatalf("EnsureRuntime existing mapping: %v", err)
	}
	if got := strings.Join(seen, "\n"); got != "GET /api/v1/mcp/servers?limit=100\nPOST /api/v1/mcp/servers" {
		t.Fatalf("seen requests:\n%s", got)
	}

	if _, err := provisioner.EnsureRuntime(ctx, users[0].ID); err != nil {
		t.Fatalf("EnsureRuntime cached bridge: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("redteam bridge should be cached after successful ensure, seen=%v", seen)
	}
}

func TestProvisionerReEnsuresRedteamMCPBridgeWhenConfigChanges(t *testing.T) {
	ctx := context.Background()
	var inputs []MCPServerInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-api-key-1" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/mcp/servers":
			_ = json.NewEncoder(w).Encode(struct {
				Items []MCPServerView `json:"items"`
			}{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/mcp/servers":
			var in MCPServerInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatalf("decode mcp input: %v", err)
			}
			inputs = append(inputs, in)
			_ = json.NewEncoder(w).Encode(MCPServerView{ID: "mcp_1", Name: in.Name, HasAuthSecret: true})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	store := newFakeMappingStore()
	users := fakeUserLookup{user("enterprise@example.com", model.RoleEnterprise)}
	admin := &fakeProvisioningAdmin{}
	provisioner := newTestProvisionerWithBaseURLAndKind(t, users, store, admin, server.URL, RuntimeKindMaclawSrv)
	if _, err := provisioner.EnsureRuntime(ctx, users[0].ID); err != nil {
		t.Fatalf("initial EnsureRuntime: %v", err)
	}
	provisioner.SetRedteamMCPBridgeConfig(RedteamMCPBridgeProvisioningConfig{
		EndpointURL: "https://platform.internal/v1/redteam-mcp",
		AuthSecret:  "bridge-secret-v1",
	})
	if _, err := provisioner.EnsureRuntime(ctx, users[0].ID); err != nil {
		t.Fatalf("EnsureRuntime v1: %v", err)
	}
	provisioner.SetRedteamMCPBridgeConfig(RedteamMCPBridgeProvisioningConfig{
		EndpointURL: "https://platform.internal/v2/redteam-mcp",
		AuthSecret:  "bridge-secret-v2",
	})
	if _, err := provisioner.EnsureRuntime(ctx, users[0].ID); err != nil {
		t.Fatalf("EnsureRuntime v2: %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("expected bridge ensure after config change, got %#v", inputs)
	}
	if inputs[0].EndpointURL != "https://platform.internal/v1/redteam-mcp" || inputs[1].EndpointURL != "https://platform.internal/v2/redteam-mcp" {
		t.Fatalf("bridge endpoints = %#v", inputs)
	}
	if inputs[0].Headers["X-Evaluating-Platform-Role"] != "enterprise" || inputs[1].Headers["X-Evaluating-Platform-Role"] != "enterprise" {
		t.Fatalf("bridge headers missing role: %#v", inputs)
	}
}

func clientAPIToken(client GatewayClient) string {
	switch c := client.(type) {
	case *Client:
		return c.apiToken
	case *MaclawSrvClient:
		if c.Client == nil {
			return ""
		}
		return c.Client.apiToken
	default:
		return ""
	}
}

func newTestProvisioner(t *testing.T, users fakeUserLookup, store *fakeMappingStore, admin *fakeProvisioningAdmin) *Provisioner {
	return newTestProvisionerWithBaseURLAndKind(t, users, store, admin, "http://maclaw.local", "")
}

func newTestProvisionerWithBaseURLAndKind(t *testing.T, users fakeUserLookup, store *fakeMappingStore, admin *fakeProvisioningAdmin, baseURL, runtimeKind string) *Provisioner {
	t.Helper()
	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{
		MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		KeyID:     "test",
	})
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	provisioner, err := NewProvisioner(ProvisionerConfig{
		BaseURL:        baseURL,
		RuntimeKind:    runtimeKind,
		TimeoutSeconds: 1,
	}, users, store, admin, keyStore)
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}
	return provisioner
}

func user(email string, role model.UserRole) *model.User {
	return &model.User{
		ID:       uuid.New(),
		Email:    email,
		Name:     email,
		Role:     role,
		OrgName:  "org-" + string(role),
		IsActive: true,
	}
}

type fakeUserLookup []*model.User

func (f fakeUserLookup) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	for _, user := range f {
		if user.ID == id {
			return user, nil
		}
	}
	return nil, fmt.Errorf("user not found")
}

type fakeMappingStore struct {
	mu       sync.Mutex
	mappings map[uuid.UUID]*model.MaclawAccountMapping
}

func newFakeMappingStore() *fakeMappingStore {
	return &fakeMappingStore{mappings: make(map[uuid.UUID]*model.MaclawAccountMapping)}
}

func (s *fakeMappingStore) GetByPlatformUserID(ctx context.Context, id uuid.UUID) (*model.MaclawAccountMapping, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping := s.mappings[id]
	if mapping == nil {
		return nil, nil
	}
	copied := *mapping
	return &copied, nil
}

func (s *fakeMappingStore) Upsert(ctx context.Context, mapping *model.MaclawAccountMapping) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *mapping
	s.mappings[mapping.PlatformUserID] = &copied
	return nil
}

type fakeProvisioningAdmin struct {
	createdTenants              int
	tokenIssues                 int
	updatedConfigs              []fakeUpdatedRuntimeConfig
	configUpdatedBeforeInstance bool
	rejectedAPIKeys             map[string]bool
	createTenantDelay           time.Duration
}

type fakeUpdatedRuntimeConfig struct {
	token string
	cfg   RuntimeAppConfig
}

func (a *fakeProvisioningAdmin) CreateTenant(_ context.Context, _ AdminCreateTenantInput) (*AdminTenant, error) {
	if a.createTenantDelay > 0 {
		time.Sleep(a.createTenantDelay)
	}
	a.createdTenants++
	return &AdminTenant{ID: fmt.Sprintf("tenant-%d", a.createdTenants)}, nil
}

func (a *fakeProvisioningAdmin) CreateUser(_ context.Context, tenantID string, _ AdminCreateUserInput) (*AdminUser, error) {
	return &AdminUser{ID: "user-" + tenantID, TenantID: tenantID}, nil
}

func (a *fakeProvisioningAdmin) CreateCredential(_ context.Context, tenantID, userID string, _ AdminCreateCredentialInput) (*AdminCredential, error) {
	idx := a.createdTenants
	return &AdminCredential{
		ID:        fmt.Sprintf("cred-%d", idx),
		TenantID:  tenantID,
		UserID:    userID,
		APIKey:    fmt.Sprintf("api-key-%d", idx),
		APISecret: fmt.Sprintf("api-secret-%d", idx),
	}, nil
}

func (a *fakeProvisioningAdmin) IssueToken(_ context.Context, in TokenIssueInput) (*TokenIssueResult, error) {
	a.tokenIssues++
	if a.rejectedAPIKeys != nil && a.rejectedAPIKeys[in.APIKey] {
		return nil, NewUpstreamError(http.MethodPost, "/api/v1/auth/token", http.StatusUnauthorized, `{"error":"unauthorized"}`)
	}
	return &TokenIssueResult{
		AccessToken: "token-" + in.APIKey,
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(time.Hour),
	}, nil
}

func (a *fakeProvisioningAdmin) CreateInstance(_ context.Context, _ string, _ RuntimeInstanceInput) (*RuntimeInstance, error) {
	a.configUpdatedBeforeInstance = len(a.updatedConfigs) > 0
	return &RuntimeInstance{ID: fmt.Sprintf("inst-%d", a.createdTenants)}, nil
}

func (a *fakeProvisioningAdmin) UpdateUserConfig(_ context.Context, accessToken string, cfg RuntimeAppConfig) (*RuntimeUserConfig, error) {
	a.updatedConfigs = append(a.updatedConfigs, fakeUpdatedRuntimeConfig{token: accessToken, cfg: cfg})
	return &RuntimeUserConfig{AppConfig: cfg}, nil
}

type fakeDefaultRuntimeConfigProvider struct {
	cfg *RuntimeAppConfig
}

func (p fakeDefaultRuntimeConfigProvider) GetDefaultRuntimeConfig(context.Context) (*RuntimeAppConfig, error) {
	return p.cfg, nil
}

type fakeHubRuntimeConfigProvider struct {
	cfg *RuntimeAppConfig
}

func (p fakeHubRuntimeConfigProvider) RuntimeConfigPatch(context.Context) (*RuntimeAppConfig, error) {
	return p.cfg, nil
}

// TestProvisionerDecryptStringLegacyCompat locks in backward compatibility for
// account-mapping credentials written by 2026-07 era containers, which
// encrypted raw strings without the JSON wrapper that encryptJSON adds now.
func TestProvisionerDecryptStringLegacyCompat(t *testing.T) {
	p := newTestProvisioner(t, fakeUserLookup{}, newFakeMappingStore(), &fakeProvisioningAdmin{})

	// New format: JSON-wrapped string via encryptString.
	secret := "e-legacy-raw-secret-value"
	encJSON, keyIDJSON, err := p.encryptString(secret)
	if err != nil {
		t.Fatalf("encryptString: %v", err)
	}
	got, err := p.decryptString(encJSON, keyIDJSON)
	if err != nil {
		t.Fatalf("decryptString(json format): %v", err)
	}
	if got != secret {
		t.Fatalf("json roundtrip = %q, want %q", got, secret)
	}

	// Legacy format: raw bytes encrypted directly (what the 2026-07 build wrote).
	_, key := p.keyStore.CurrentKey()
	encRaw, err := appcrypto.Encrypt([]byte(secret), key)
	if err != nil {
		t.Fatalf("raw encrypt: %v", err)
	}
	got, err = p.decryptString(encRaw, "test")
	if err != nil {
		t.Fatalf("decryptString(legacy raw format): %v", err)
	}
	if got != secret {
		t.Fatalf("legacy raw = %q, want %q", got, secret)
	}
}
