package maclaw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
)

// P26c · the registration half of D-4: an admin binds a tenant account to a
// dedicated engine instance. The token is GENERATED here (never accepted from
// the browser), stored encrypted in maclaw_account_mappings, and returned to
// the caller exactly once so the operator can put it in the deployment .env
// for the tenant's compose service. After that the platform only ever holds
// the ciphertext.

// EngineInstanceStore is the registration write surface on
// maclaw_account_mappings (concrete impl:
// *repository.MaclawAccountMappingRepository).
type EngineInstanceStore interface {
	GetByPlatformUserID(ctx context.Context, id uuid.UUID) (*model.MaclawAccountMapping, error)
	SetEngineInstance(ctx context.Context, id uuid.UUID, baseURL string, tokenCiphertext []byte, keyID string) (bool, error)
	ClearEngineInstance(ctx context.Context, id uuid.UUID) (bool, error)
}

// EngineInstanceProber checks a candidate instance over HTTP before binding
// (registers a typo = every routed run dies at submit; fail at registration
// instead). *PromptfooEngineClient satisfies it; Health needs no bearer.
type EngineInstanceProber interface {
	Health(ctx context.Context) error
}

// EngineInstanceAdmin orchestrates validate → probe → generate → encrypt →
// store. keyStore/instances must be wired; a deployment without the store
// reports ErrNotConfigured (registration requires provisioning on).
type EngineInstanceAdmin struct {
	store    EngineInstanceStore
	keys     *appcrypto.KeyStore
	prober   func(baseURL string) EngineInstanceProber
	newToken func() (string, error)
}

func NewEngineInstanceAdmin(store EngineInstanceStore, keys *appcrypto.KeyStore) *EngineInstanceAdmin {
	return &EngineInstanceAdmin{
		store: store,
		keys:  keys,
		prober: func(baseURL string) EngineInstanceProber {
			return NewPromptfooEngineClient(baseURL, "", 5)
		},
		newToken: randomEngineInstanceToken,
	}
}

func randomEngineInstanceToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate engine instance token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ValidateEngineInstanceBaseURL: http(s) only, absolute host, no embedded
// credentials/query/fragment, <=200 chars. These are compose-internal URLs
// (http://promptfoo-engine-acme:8090); https public hosts are fine too.
// Rejecting userinfo matters: an embedded secret would land in a plain column.
func ValidateEngineInstanceBaseURL(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" || len(v) > 200 {
		return "", errors.New("engine_base_url 为空或超过 200 字符")
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", errors.New("engine_base_url 必须是 http(s) 绝对地址且含主机名")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("engine_base_url 不允许携带凭据、查询串或片段")
	}
	return strings.TrimRight(v, "/"), nil
}

// Register binds (or re-registers) the account. Re-registration ROTATES the
// token: the old ciphertext is overwritten and the new plaintext is returned
// once. Returns ErrNotConfigured without store/keys; a missing mapping row is
// ErrResourceNotFound-style refusal (register only after provisioning).
func (a *EngineInstanceAdmin) Register(ctx context.Context, userID uuid.UUID, rawURL string) (string, error) {
	if a == nil || a.store == nil || a.keys == nil {
		return "", ErrNotConfigured
	}
	baseURL, err := ValidateEngineInstanceBaseURL(rawURL)
	if err != nil {
		return "", err
	}
	mapping, err := a.store.GetByPlatformUserID(ctx, userID)
	if err != nil {
		return "", err
	}
	if mapping == nil {
		return "", errors.New("该账号尚未 provision，无法登记专属引擎")
	}
	if a.prober != nil {
		if err := a.prober(baseURL).Health(ctx); err != nil {
			return "", fmt.Errorf("专属引擎探活失败（%s）：%w", baseURL, err)
		}
	}
	token, err := a.newToken()
	if err != nil {
		return "", err
	}
	keyID, key := a.keys.CurrentKey()
	encrypted, err := appcrypto.Encrypt([]byte(token), key)
	if err != nil {
		return "", fmt.Errorf("encrypt engine instance token: %w", err)
	}
	ok, err := a.store.SetEngineInstance(ctx, userID, baseURL, encrypted, keyID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("登记写入未命中账号行")
	}
	return token, nil
}

// Unregister clears the three columns so the account falls back to the shared
// instance (non-code runs only — the code gate keeps working).
func (a *EngineInstanceAdmin) Unregister(ctx context.Context, userID uuid.UUID) error {
	if a == nil || a.store == nil || a.keys == nil {
		return ErrNotConfigured
	}
	_, err := a.store.ClearEngineInstance(ctx, userID)
	return err
}
