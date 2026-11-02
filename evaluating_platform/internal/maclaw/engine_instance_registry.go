// Package-level notes for engine_instance_registry.go (P26a, 2026-11-02):
//
// The tenant engine registry is the routing half of G-1/G-3
// (docs/architecture/tenant-isolation-g1-g3.md). It exists because expert code
// strategies execute INSIDE the engine process; until a tenant has its own
// engine instance, its own generation key and its own instance token, running
// a code package on the shared engine means the code can read everything that
// process holds. The registry makes "which engine instance runs this tenant's
// runs" a data fact, and the refusal below makes "code strategy without a
// dedicated instance" a loud 409 instead of a silent downgrade.
//
// The whole mechanism lives behind `enabled` (config `MACLAW_TENANT_ENGINES`,
// default off): with it off, every call returns the shared client and the
// behavior is byte-for-byte the pre-P26 behavior — so this can ship before the
// instances exist, and record 47's gate-open verification (shared engine, flag
// on, no registry) is untouched.
package maclaw

import (
	"context"
	"log"
	"strings"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/model"
)

// EngineErrCodeStrategyNoDedicatedInstance is the pre-flight refusal code when
// a run selects CODE strategy packages but the submitting tenant has no
// dedicated engine instance.
const EngineErrCodeStrategyNoDedicatedInstance = "code_strategy_requires_dedicated_instance"

// EngineErrCodeInstanceGone marks the lifecycle error for a run whose recorded
// dedicated instance is no longer registered (removed/remapped mid-run). It is
// deliberately distinct from "engine unavailable": the fix is re-registering
// the instance, not retrying the shared one.
const EngineErrCodeInstanceGone = "engine_instance_unavailable"

// EngineCodeStrategyNoDedicatedError refuses a code-strategy run that would
// otherwise land on the shared instance. It is a *request* problem (the
// deployment has not provisioned the tenant's engine yet), so it maps to 409
// through writeEnginePreflightRejection like every other pre-flight guard —
// both doors, one dispatcher.
type EngineCodeStrategyNoDedicatedError struct {
	Strategies []string
}

func (e *EngineCodeStrategyNoDedicatedError) Error() string {
	return "策略代码包需要租户专属引擎实例（当前部署未注册），请改用文本策略或联系管理员为该账号注册专属引擎。"
}

// CodeStrategyIDs returns the refused strategy ids (safe ids only, never payload).
func (e *EngineCodeStrategyNoDedicatedError) CodeStrategyIDs() []string {
	out := make([]string, 0, len(e.Strategies))
	seen := map[string]bool{}
	for _, id := range e.Strategies {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// EngineInstanceMappingStore is the registry's read surface onto
// maclaw_account_mappings (P26a columns engine_base_url / engine_token_*).
// *repository.MaclawAccountMappingRepository satisfies it.
type EngineInstanceMappingStore interface {
	GetByPlatformUserID(ctx context.Context, id uuid.UUID) (*model.MaclawAccountMapping, error)
}

// EngineInstanceRegistry resolves which engine client a run goes to.
// Zero value / nil registry / enabled=false all mean "shared instance for
// everyone" — exactly the pre-P26 behavior.
type EngineInstanceRegistry struct {
	enabled        bool
	shared         *PromptfooEngineClient
	mappings       EngineInstanceMappingStore
	keys           *appcrypto.KeyStore
	timeoutSeconds int
}

func NewEngineInstanceRegistry(enabled bool, shared *PromptfooEngineClient, mappings EngineInstanceMappingStore, keys *appcrypto.KeyStore, timeoutSeconds int) *EngineInstanceRegistry {
	return &EngineInstanceRegistry{
		enabled:        enabled,
		shared:         shared,
		mappings:       mappings,
		keys:           keys,
		timeoutSeconds: timeoutSeconds,
	}
}

// Enabled reports whether tenant routing is active in this deployment.
func (r *EngineInstanceRegistry) Enabled() bool {
	return r != nil && r.enabled && r.mappings != nil && r.keys != nil
}

// Resolve returns the engine client for one submitting user and whether it is
// a DEDICATED instance. Every degradation path (no mapping row, empty URL,
// undecryptable token, store error) returns the shared client rather than
// failing the run: a routing lookup must never break non-code evaluations —
// and for code runs the shared answer is exactly what triggers the 409, which
// is the fail-closed direction.
//
// No client cache on purpose (the resolver-must-not-cache family): rotating a
// tenant's token or URL must take effect on the next run, not the next restart.
func (r *EngineInstanceRegistry) Resolve(ctx context.Context, userID uuid.UUID) (*PromptfooEngineClient, bool) {
	if !r.Enabled() || userID == uuid.Nil {
		return r.shared, false
	}
	mapping, err := r.mappings.GetByPlatformUserID(ctx, userID)
	if err != nil {
		log.Printf("[WARN] tenant engine mapping lookup degraded (user=%s): %v", userID, err)
		return r.shared, false
	}
	if mapping == nil {
		return r.shared, false
	}
	baseURL := strings.TrimRight(strings.TrimSpace(mapping.EngineBaseURL), "/")
	if baseURL == "" {
		return r.shared, false
	}
	if len(mapping.EngineTokenCiphertext) == 0 || strings.TrimSpace(mapping.EngineTokenKeyID) == "" {
		// A URL without its token is a half-registration. Using the SHARED token
		// against it would either 401 or (worse) authenticate a run the tenant
		// did not configure — treat it as unregistered and let the code gate
		// refuse loudly.
		log.Printf("[WARN] tenant engine registration incomplete for user=%s (missing token); falling back to shared instance", userID)
		return r.shared, false
	}
	key, err := r.keys.GetKey(mapping.EngineTokenKeyID)
	if err != nil {
		log.Printf("[WARN] tenant engine token key unavailable (user=%s): %v", userID, err)
		return r.shared, false
	}
	token, err := appcrypto.Decrypt(mapping.EngineTokenCiphertext, key)
	if err != nil {
		log.Printf("[WARN] tenant engine token undecryptable (user=%s); falling back to shared instance", userID)
		return r.shared, false
	}
	return NewPromptfooEngineClient(baseURL, string(token), r.timeoutSeconds), true
}

// BaseURL exposes the client's engine endpoint for lifecycle routing checks
// (ResolveForRun compares it against the run's recorded instance). Safe to
// return: compose-internal URLs are not secrets, and this value already lives
// on the run record the operator can read.
func (c *PromptfooEngineClient) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// ResolveForRun routes the lifecycle (GetRun/Cancel/Stream/poll) of ONE run.
// The run must be driven on the instance it was submitted to — its persisted
// EngineBaseURL is the record of that fact (sticky in the repository, exactly
// like job_id after U5). A run recorded on a dedicated instance whose
// registration later disappeared is refused rather than repointed: polling a
// live run against the wrong engine 404s and looks like "still queued" forever
// (the ghost-run shape this project already refuses twice: U5's job link,
// record 40's terminal window).
//
// Returns the shared client when the run predates the registry / is registered
// nowhere, and (nil, false) only when the run's recorded instance can no
// longer be resolved at all — callers treat that as "engine unavailable for
// this run", never as "fall through to shared".
func (r *EngineInstanceRegistry) ResolveForRun(ctx context.Context, userID uuid.UUID, runEngineBaseURL string) (*PromptfooEngineClient, bool) {
	recorded := strings.TrimRight(strings.TrimSpace(runEngineBaseURL), "/")
	if recorded == "" {
		// Run lives on the shared instance (every run before P26a, and every
		// unregistered tenant after it).
		return r.shared, true
	}
	client, dedicated := r.Resolve(ctx, userID)
	if dedicated && client.BaseURL() == recorded {
		return client, true
	}
	// The run was submitted to a dedicated instance this tenant no longer
	// resolves to (registration removed or remapped mid-run). Driving it on
	// any other engine is the ghost-run shape; refuse loudly.
	return nil, false
}
