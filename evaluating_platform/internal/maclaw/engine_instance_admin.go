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

// ── P26c.2 · 实例可用性探活（51 号 §8.1 的静默 401 → 面板可见）──────────────
// 重新登记会轮换 token，而 token 要等 .env 更新 + recreate 才进容器；中间这段
// 窗口里登记状态是"已登记"但每一次路由过来的 run 都会 401——51 号原话「两次
// token 不一致时 BFF 调用会 401」，没人看得见。探活用存储的密文 token 打一次
// 带鉴权的只读端点，把这段窗口变成状态。

type EngineInstanceHealth string

const (
	EngineInstanceHealthOK            EngineInstanceHealth = "ok"             // token 已部署，实例应答
	EngineInstanceHealthTokenMismatch EngineInstanceHealth = "token_mismatch" // 登记过但容器还是旧 token → 更新 .env 后 recreate
	EngineInstanceHealthUnreachable   EngineInstanceHealth = "unreachable"    // 容器没起/挂了
	EngineInstanceHealthIncomplete    EngineInstanceHealth = "incomplete"     // 半登记（有地址没 token 密文）
	EngineInstanceHealthUnregistered  EngineInstanceHealth = "unregistered"   // 未登记
)

// Probe run id is deliberately not `er-…` shaped: real run ids are er-<uuid>,
// so this can never collide with a real record.
const engineInstanceProbeRunID = "health-probe-never-a-real-run"

// Probe asks the registered instance one authenticated read-only question.
// It never mutates anything on the instance; a run id that does not exist
// answers 404 AFTER the bearer check, which is exactly the signal we need:
// 401 = the container still runs an older token, anything else = auth passed.
func (a *EngineInstanceAdmin) Probe(ctx context.Context, userID uuid.UUID) (EngineInstanceHealth, error) {
	if a == nil || a.store == nil || a.keys == nil {
		return "", ErrNotConfigured
	}
	mapping, err := a.store.GetByPlatformUserID(ctx, userID)
	if err != nil {
		return "", err
	}
	if mapping == nil || strings.TrimSpace(mapping.EngineBaseURL) == "" {
		return EngineInstanceHealthUnregistered, nil
	}
	if len(mapping.EngineTokenCiphertext) == 0 || strings.TrimSpace(mapping.EngineTokenKeyID) == "" {
		return EngineInstanceHealthIncomplete, nil
	}
	key, err := a.keys.GetKey(mapping.EngineTokenKeyID)
	if err != nil {
		return "", err
	}
	token, err := appcrypto.Decrypt(mapping.EngineTokenCiphertext, key)
	if err != nil {
		return "", err
	}
	// Short timeout: this runs inside the admin account LIST. A dead instance
	// must not stall the page for long, and a healthy one answers in ms.
	client := NewPromptfooEngineClient(strings.TrimSpace(mapping.EngineBaseURL), string(token), 3)
	_, probeErr := client.GetRun(ctx, engineInstanceProbeRunID)
	switch {
	case probeErr == nil, errors.Is(probeErr, ErrEngineNotFound):
		return EngineInstanceHealthOK, nil
	case errors.Is(probeErr, ErrEngineUnauthorized):
		return EngineInstanceHealthTokenMismatch, nil
	case errors.Is(probeErr, ErrEngineUnavailable):
		return EngineInstanceHealthUnreachable, nil
	default:
		return "", probeErr
	}
}
