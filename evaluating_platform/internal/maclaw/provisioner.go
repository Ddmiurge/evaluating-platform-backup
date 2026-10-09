package maclaw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
)

type ProvisionerConfig struct {
	BaseURL        string
	RuntimeKind    string
	TimeoutSeconds int
}

type PlatformUserLookup interface {
	GetByID(context.Context, uuid.UUID) (*model.User, error)
}

type AccountMappingStore interface {
	GetByPlatformUserID(context.Context, uuid.UUID) (*model.MaclawAccountMapping, error)
	Upsert(context.Context, *model.MaclawAccountMapping) error
}

type ProvisioningAdminGateway interface {
	CreateTenant(context.Context, AdminCreateTenantInput) (*AdminTenant, error)
	CreateUser(context.Context, string, AdminCreateUserInput) (*AdminUser, error)
	CreateCredential(context.Context, string, string, AdminCreateCredentialInput) (*AdminCredential, error)
	IssueToken(context.Context, TokenIssueInput) (*TokenIssueResult, error)
	UpdateUserConfig(context.Context, string, RuntimeAppConfig) (*RuntimeUserConfig, error)
	CreateInstance(context.Context, string, RuntimeInstanceInput) (*RuntimeInstance, error)
}

type DefaultRuntimeConfigProvider interface {
	GetDefaultRuntimeConfig(context.Context) (*RuntimeAppConfig, error)
}

type HubRuntimeConfigProvider interface {
	RuntimeConfigPatch(context.Context) (*RuntimeAppConfig, error)
}

type RedteamMCPBridgeProvisioningConfig struct {
	EndpointURL string
	AuthSecret  string
}

type Provisioner struct {
	cfg                   ProvisionerConfig
	users                 PlatformUserLookup
	store                 AccountMappingStore
	admin                 ProvisioningAdminGateway
	keyStore              *appcrypto.KeyStore
	defaultConfigProvider DefaultRuntimeConfigProvider
	hubConfigProvider     HubRuntimeConfigProvider
	runtimeLockMu         sync.Mutex
	runtimeLocks          map[uuid.UUID]*sync.Mutex
	redteamMCPBridge      RedteamMCPBridgeProvisioningConfig
	redteamMCPBridgeMu    sync.Mutex
	redteamMCPBridgeReady map[string]struct{}
	now                   func() time.Time
}

type ProvisionedRuntime struct {
	Client     GatewayClient
	InstanceID string
	Mapping    *model.MaclawAccountMapping
}

func NewProvisioner(cfg ProvisionerConfig, users PlatformUserLookup, store AccountMappingStore, admin ProvisioningAdminGateway, keyStore *appcrypto.KeyStore) (*Provisioner, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("maclaw base url is required")
	}
	if users == nil {
		return nil, errors.New("platform user lookup is required")
	}
	if store == nil {
		return nil, errors.New("maclaw account mapping store is required")
	}
	if admin == nil {
		return nil, errors.New("maclaw provisioning admin gateway is required")
	}
	if keyStore == nil {
		return nil, errors.New("crypto key store is required")
	}
	return &Provisioner{
		cfg:                   cfg,
		users:                 users,
		store:                 store,
		admin:                 admin,
		keyStore:              keyStore,
		runtimeLocks:          make(map[uuid.UUID]*sync.Mutex),
		redteamMCPBridgeReady: make(map[string]struct{}),
		now:                   time.Now,
	}, nil
}

func (p *Provisioner) SetDefaultConfigProvider(provider DefaultRuntimeConfigProvider) {
	if p == nil {
		return
	}
	p.defaultConfigProvider = provider
}

func (p *Provisioner) SetHubConfigProvider(provider HubRuntimeConfigProvider) {
	if p == nil {
		return
	}
	p.hubConfigProvider = provider
}

func (p *Provisioner) SetRedteamMCPBridgeConfig(cfg RedteamMCPBridgeProvisioningConfig) {
	if p == nil {
		return
	}
	cfg.EndpointURL = strings.TrimSpace(cfg.EndpointURL)
	cfg.AuthSecret = strings.TrimSpace(cfg.AuthSecret)
	p.redteamMCPBridge = cfg
}

func (p *Provisioner) EnsureRuntime(ctx context.Context, platformUserID uuid.UUID) (*ProvisionedRuntime, error) {
	if platformUserID == uuid.Nil {
		return nil, errors.New("platform user id is required")
	}
	unlock := p.lockRuntime(platformUserID)
	defer unlock()

	mapping, err := p.store.GetByPlatformUserID(ctx, platformUserID)
	if err != nil {
		return nil, fmt.Errorf("load maclaw account mapping: %w", err)
	}
	if mapping != nil {
		runtime, err := p.runtimeFromMapping(ctx, mapping)
		if err == nil {
			return runtime, nil
		}
		if !isRejectedStoredCredential(err) {
			return nil, err
		}
		user, loadErr := p.users.GetByID(ctx, platformUserID)
		if loadErr != nil {
			return nil, fmt.Errorf("load platform user for maclaw reprovisioning: %w", loadErr)
		}
		if user == nil || !user.IsActive {
			return nil, errors.New("platform user is not active")
		}
		return p.provision(ctx, user)
	}
	user, err := p.users.GetByID(ctx, platformUserID)
	if err != nil {
		return nil, fmt.Errorf("load platform user: %w", err)
	}
	if user == nil || !user.IsActive {
		return nil, errors.New("platform user is not active")
	}
	return p.provision(ctx, user)
}

func (p *Provisioner) lockRuntime(platformUserID uuid.UUID) func() {
	p.runtimeLockMu.Lock()
	lock := p.runtimeLocks[platformUserID]
	if lock == nil {
		lock = &sync.Mutex{}
		p.runtimeLocks[platformUserID] = lock
	}
	p.runtimeLockMu.Unlock()

	lock.Lock()
	return lock.Unlock
}

func isRejectedStoredCredential(err error) bool {
	if err == nil {
		return false
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		return false
	}
	if upstream.StatusCode != http.StatusUnauthorized && upstream.StatusCode != http.StatusForbidden {
		return false
	}
	return strings.TrimSpace(upstream.Path) == "/api/v1/auth/token"
}

func (p *Provisioner) runtimeFromMapping(ctx context.Context, mapping *model.MaclawAccountMapping) (*ProvisionedRuntime, error) {
	token, err := p.cachedToken(mapping)
	if err != nil {
		return nil, err
	}
	if token == "" {
		token, err = p.refreshToken(ctx, mapping)
		if err != nil {
			return nil, err
		}
	}
	if err := p.ensureRedteamMCPBridgeForMapping(ctx, mapping, token); err != nil {
		return nil, err
	}
	client, err := NewRuntimeClient(p.cfg.RuntimeKind, Config{
		BaseURL:        p.cfg.BaseURL,
		APIToken:       token,
		TimeoutSeconds: p.cfg.TimeoutSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("create maclaw request client: %w", err)
	}
	client = clientForGatewayInstance(client, mapping.MaclawInstanceID)
	return &ProvisionedRuntime{Client: client, InstanceID: mapping.MaclawInstanceID, Mapping: mapping}, nil
}

func (p *Provisioner) provision(ctx context.Context, user *model.User) (*ProvisionedRuntime, error) {
	tenant, err := p.admin.CreateTenant(ctx, AdminCreateTenantInput{
		Name:                   tenantName(user),
		DeleteProtected:        true,
		DeleteProtectionReason: "managed by evaluating_platform account provisioning",
	})
	if err != nil {
		return nil, fmt.Errorf("create maclaw tenant: %w", err)
	}
	maclawUser, err := p.admin.CreateUser(ctx, tenant.ID, AdminCreateUserInput{
		Name:                   firstNonEmptyString(user.Name, user.Email),
		Email:                  user.Email,
		DeleteProtected:        true,
		DeleteProtectionReason: "managed by evaluating_platform account provisioning",
	})
	if err != nil {
		return nil, fmt.Errorf("create maclaw user: %w", err)
	}
	credential, err := p.admin.CreateCredential(ctx, tenant.ID, maclawUser.ID, AdminCreateCredentialInput{Name: "evaluating_platform-bff"})
	if err != nil {
		return nil, fmt.Errorf("create maclaw credential: %w", err)
	}
	if strings.TrimSpace(credential.APIKey) == "" || strings.TrimSpace(credential.APISecret) == "" {
		return nil, errors.New("maclaw credential create response did not include one-time API key and secret")
	}
	token, err := p.admin.IssueToken(ctx, TokenIssueInput{APIKey: credential.APIKey, APISecret: credential.APISecret})
	if err != nil {
		return nil, fmt.Errorf("issue maclaw token: %w", err)
	}
	if err := p.applyInitialRuntimeConfig(ctx, token.AccessToken); err != nil {
		return nil, err
	}
	instance, err := p.admin.CreateInstance(ctx, token.AccessToken, RuntimeInstanceInput{
		Name:        "Evaluating Platform - " + user.Email,
		Description: "Default runtime instance for evaluating_platform account",
		Metadata: map[string]string{
			"platform":         "evaluating_platform",
			"platform_user_id": user.ID.String(),
			"platform_role":    string(user.Role),
			"platform_email":   user.Email,
			"platform_org":     user.OrgName,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create maclaw instance: %w", err)
	}
	if p.shouldEnsureRedteamMCPBridge() {
		if err := p.ensureRedteamMCPBridge(ctx, token.AccessToken, redteamMCPBridgeIdentityHeaders(user.ID, string(user.Role), tenant.ID, maclawUser.ID, instance.ID)); err != nil {
			return nil, err
		}
		p.markRedteamMCPBridgeReady(redteamMCPBridgeReadyKey(tenant.ID, p.redteamMCPBridge))
	}

	credentialKey, credentialKeyID, err := p.encryptString(credential.APIKey)
	if err != nil {
		return nil, err
	}
	credentialSecret, _, err := p.encryptString(credential.APISecret)
	if err != nil {
		return nil, err
	}
	accessToken, accessTokenKeyID, err := p.encryptString(token.AccessToken)
	if err != nil {
		return nil, err
	}
	mapping := &model.MaclawAccountMapping{
		PlatformUserID:       user.ID,
		PlatformRole:         user.Role,
		PlatformEmail:        user.Email,
		PlatformOrgName:      user.OrgName,
		MaclawTenantID:       tenant.ID,
		MaclawUserID:         maclawUser.ID,
		MaclawCredentialID:   credential.ID,
		MaclawInstanceID:     instance.ID,
		EncryptedAPIKey:      credentialKey,
		EncryptedAPISecret:   credentialSecret,
		CredentialKeyID:      credentialKeyID,
		EncryptedAccessToken: accessToken,
		AccessTokenKeyID:     accessTokenKeyID,
		AccessTokenExpiresAt: &token.ExpiresAt,
		ProvisioningStatus:   model.MaclawProvisioningReady,
	}
	if err := p.store.Upsert(ctx, mapping); err != nil {
		return nil, fmt.Errorf("store maclaw account mapping: %w", err)
	}
	return p.runtimeFromMapping(ctx, mapping)
}

func (p *Provisioner) applyInitialRuntimeConfig(ctx context.Context, accessToken string) error {
	var merged RuntimeAppConfig
	if p.defaultConfigProvider != nil {
		cfg, err := p.defaultConfigProvider.GetDefaultRuntimeConfig(ctx)
		if err != nil {
			return fmt.Errorf("load default maclaw runtime config: %w", err)
		}
		if cfg != nil {
			merged = MergeRuntimeConfigPatch(merged, *cfg)
		}
	}
	if p.hubConfigProvider != nil {
		cfg, err := p.hubConfigProvider.RuntimeConfigPatch(ctx)
		if err != nil {
			return fmt.Errorf("load maclaw hub config: %w", err)
		}
		if cfg != nil {
			merged = MergeRuntimeConfigPatch(merged, *cfg)
		}
	}
	if merged.IsEmpty() {
		return nil
	}
	if _, err := p.admin.UpdateUserConfig(ctx, accessToken, merged); err != nil {
		return fmt.Errorf("apply initial maclaw runtime config: %w", err)
	}
	return nil
}

func MergeRuntimeConfigPatch(base RuntimeAppConfig, patch RuntimeAppConfig) RuntimeAppConfig {
	if strings.TrimSpace(patch.MaclawLLMUrl) != "" {
		base.MaclawLLMUrl = patch.MaclawLLMUrl
	}
	if strings.TrimSpace(patch.MaclawLLMKey) != "" {
		base.MaclawLLMKey = patch.MaclawLLMKey
	}
	if strings.TrimSpace(patch.MaclawLLMModel) != "" {
		base.MaclawLLMModel = patch.MaclawLLMModel
	}
	if strings.TrimSpace(patch.MaclawLLMProtocol) != "" {
		base.MaclawLLMProtocol = patch.MaclawLLMProtocol
	}
	if patch.MaclawLLMContextLength != 0 {
		base.MaclawLLMContextLength = patch.MaclawLLMContextLength
	}
	if patch.MaclawLLMTimeoutSec != 0 {
		base.MaclawLLMTimeoutSec = patch.MaclawLLMTimeoutSec
	}
	if len(patch.MaclawLLMProviders) > 0 {
		base.MaclawLLMProviders = patch.MaclawLLMProviders
	}
	if strings.TrimSpace(patch.MaclawLLMCurrentProvider) != "" {
		base.MaclawLLMCurrentProvider = patch.MaclawLLMCurrentProvider
	}
	if strings.TrimSpace(patch.RemoteHubURL) != "" {
		base.RemoteHubURL = patch.RemoteHubURL
	}
	if len(patch.SkillSourcesAllowed) > 0 {
		base.SkillSourcesAllowed = patch.SkillSourcesAllowed
	}
	return base
}

func (p *Provisioner) ensureRedteamMCPBridge(ctx context.Context, accessToken string, headers map[string]string) error {
	if !p.shouldEnsureRedteamMCPBridge() {
		return nil
	}
	client, err := NewMaclawSrvClient(Config{
		BaseURL:        p.cfg.BaseURL,
		APIToken:       accessToken,
		TimeoutSeconds: p.cfg.TimeoutSeconds,
	})
	if err != nil {
		return fmt.Errorf("create maclaw mcp bridge client: %w", err)
	}
	if _, err := client.EnsureRedteamMCPBridge(ctx, RedteamMCPBridgeInput{
		EndpointURL: p.redteamMCPBridge.EndpointURL,
		AuthSecret:  p.redteamMCPBridge.AuthSecret,
		Headers:     headers,
	}); err != nil {
		return fmt.Errorf("ensure redteam mcp bridge: %w", err)
	}
	return nil
}

func (p *Provisioner) shouldEnsureRedteamMCPBridge() bool {
	if p == nil {
		return false
	}
	return NormalizeRuntimeKind(p.cfg.RuntimeKind) == RuntimeKindMaclawSrv &&
		strings.TrimSpace(p.redteamMCPBridge.EndpointURL) != "" &&
		strings.TrimSpace(p.redteamMCPBridge.AuthSecret) != ""
}

func (p *Provisioner) ensureRedteamMCPBridgeForMapping(ctx context.Context, mapping *model.MaclawAccountMapping, accessToken string) error {
	if mapping == nil {
		return nil
	}
	if !p.shouldEnsureRedteamMCPBridge() {
		return nil
	}
	key := redteamMCPBridgeReadyKey(firstNonEmptyString(mapping.MaclawTenantID, mapping.MaclawUserID, mapping.PlatformUserID.String()), p.redteamMCPBridge)
	if key == "" || p.isRedteamMCPBridgeReady(key) {
		return nil
	}
	if err := p.ensureRedteamMCPBridge(ctx, accessToken, redteamMCPBridgeIdentityHeaders(mapping.PlatformUserID, string(mapping.PlatformRole), mapping.MaclawTenantID, mapping.MaclawUserID, mapping.MaclawInstanceID)); err != nil {
		return err
	}
	p.markRedteamMCPBridgeReady(key)
	return nil
}

func redteamMCPBridgeIdentityHeaders(platformUserID uuid.UUID, platformRole, maclawTenantID, maclawUserID, maclawInstanceID string) map[string]string {
	headers := map[string]string{}
	if platformUserID != uuid.Nil {
		headers["X-Evaluating-Platform-User-ID"] = platformUserID.String()
	}
	if strings.TrimSpace(platformRole) != "" {
		headers["X-Evaluating-Platform-Role"] = strings.TrimSpace(platformRole)
	}
	if strings.TrimSpace(maclawTenantID) != "" {
		headers["X-Evaluating-Platform-Tenant-ID"] = strings.TrimSpace(maclawTenantID)
	}
	if strings.TrimSpace(maclawUserID) != "" {
		headers["X-Evaluating-Platform-Maclaw-User-ID"] = strings.TrimSpace(maclawUserID)
	}
	if strings.TrimSpace(maclawInstanceID) != "" {
		headers["X-Evaluating-Platform-Instance-ID"] = strings.TrimSpace(maclawInstanceID)
	}
	return headers
}

func redteamMCPBridgeReadyKey(identity string, cfg RedteamMCPBridgeProvisioningConfig) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	return strings.Join([]string{
		identity,
		strings.TrimSpace(cfg.EndpointURL),
		strings.TrimSpace(cfg.AuthSecret),
	}, "\x00")
}

func (p *Provisioner) isRedteamMCPBridgeReady(key string) bool {
	if p == nil {
		return true
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}
	p.redteamMCPBridgeMu.Lock()
	defer p.redteamMCPBridgeMu.Unlock()
	_, ok := p.redteamMCPBridgeReady[key]
	return ok
}

func (p *Provisioner) markRedteamMCPBridgeReady(key string) {
	if p == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	p.redteamMCPBridgeMu.Lock()
	defer p.redteamMCPBridgeMu.Unlock()
	p.redteamMCPBridgeReady[key] = struct{}{}
}

func (p *Provisioner) cachedToken(mapping *model.MaclawAccountMapping) (string, error) {
	if mapping == nil || len(mapping.EncryptedAccessToken) == 0 || mapping.AccessTokenExpiresAt == nil {
		return "", nil
	}
	if mapping.AccessTokenExpiresAt.Before(p.now().Add(2 * time.Minute)) {
		return "", nil
	}
	return p.decryptString(mapping.EncryptedAccessToken, mapping.AccessTokenKeyID)
}

func (p *Provisioner) refreshToken(ctx context.Context, mapping *model.MaclawAccountMapping) (string, error) {
	apiKey, err := p.decryptString(mapping.EncryptedAPIKey, mapping.CredentialKeyID)
	if err != nil {
		return "", err
	}
	apiSecret, err := p.decryptString(mapping.EncryptedAPISecret, mapping.CredentialKeyID)
	if err != nil {
		return "", err
	}
	token, err := p.admin.IssueToken(ctx, TokenIssueInput{APIKey: apiKey, APISecret: apiSecret})
	if err != nil {
		return "", fmt.Errorf("refresh maclaw token: %w", err)
	}
	encryptedToken, tokenKeyID, err := p.encryptString(token.AccessToken)
	if err != nil {
		return "", err
	}
	mapping.EncryptedAccessToken = encryptedToken
	mapping.AccessTokenKeyID = tokenKeyID
	mapping.AccessTokenExpiresAt = &token.ExpiresAt
	if err := p.store.Upsert(ctx, mapping); err != nil {
		return "", fmt.Errorf("store refreshed maclaw token: %w", err)
	}
	return token.AccessToken, nil
}

func (p *Provisioner) encryptString(value string) ([]byte, string, error) {
	return encryptJSON(p.keyStore, value, "maclaw secret")
}

func (p *Provisioner) decryptString(value []byte, keyID string) (string, error) {
	key, err := p.keyStore.GetKey(keyID)
	if err != nil {
		return "", fmt.Errorf("load maclaw secret key: %w", err)
	}
	plaintext, err := appcrypto.Decrypt(value, key)
	if err != nil {
		return "", fmt.Errorf("decrypt maclaw secret: %w", err)
	}
	var out string
	if err := json.Unmarshal(plaintext, &out); err == nil {
		return out, nil
	}
	// Legacy fallback: 2026-07 era containers persisted maclaw secrets as raw
	// (non-JSON-wrapped) strings. Read them back so account mappings created by
	// those builds keep resolving after the encryptJSON migration.
	return string(plaintext), nil
}

func tenantName(user *model.User) string {
	parts := []string{"evaluating-platform", string(user.Role), user.Email}
	return strings.Join(parts, ":")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
