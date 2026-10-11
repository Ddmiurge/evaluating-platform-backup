package maclaw

// promptfoo_custom_plugin_resolver.go — U10 · turning a `pf_plugin:<handle>`
// reference into something the engine can load, with admin governance applied.
//
// # The shape of the problem
//
// A published package is inert: it is a row in `maclaw_resources` (encrypted
// payload) plus a row in `maclaw_resource_publications`. promptfoo cannot load
// either. To become a plugin it has to be a **file on disk inside the engine
// container**, named by a `file://` id in the redteam config.
//
// So a run that names a published package needs three steps, and each one is a
// place where "the platform thinks the plugin is available but the engine
// cannot use it" can hide:
//
//  1. **Is it available?** The publication's `enabled`/`status` are the admin's
//     levers (启停/下架/归档). Resolving without checking them would mean a
//     disabled package still runs — the DoD's "管理员停用 → 引擎立即不可用"
//     would be false, and it would be false *silently*, which is worse than an
//     error.
//  2. **Get the payload.** Decrypted server-side, never returned to a browser.
//  3. **Hand it over in a form the engine can materialize.** Shipped inline on
//     the run request (the engine already receives one-shot credentials the
//     same way), written into the per-run workdir, referenced as `file://`.
//
// # Why governance is checked here and not only at publication time
//
// Publication-time checks describe the moment of publish. Governance is
// *after* publish (承 `06` D-03: 取消管理员审核作为发布前置门 → 管理员事后治理).
// The only place both facts meet is run preparation, so that is where the check
// lives — and it is checked on the single exit every engine run passes through
// (`PreparePromptfooEngineRun`), per the U8 wiring lesson.

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
)

// CustomPluginPublicationLookup is the slice of the publication store this
// resolver needs. Declared as its own one-method interface rather than reusing
// `ResourcePublicationStore` so a test can supply a lookup without standing up
// the shadow half of that interface too.
type CustomPluginPublicationLookup interface {
	// GetPublishedByHandle returns the publication only when it is
	// `enabled = true AND status = 'published'`, so a disabled or archived
	// package is indistinguishable from an unknown one here. That is
	// deliberate: the caller must not be able to distinguish them and skip the
	// rejection.
	GetPublishedByHandle(ctx context.Context, handle string) (*model.MaclawResourcePublication, error)
}

// EngineCustomPlugin is one published package, resolved and ready for the
// engine to materialize.
//
// `Definition` is the **canonical** YAML (re-serialized from the validated
// struct, see SaveResource) — not the expert's submitted text. The engine writes
// this exact string to disk, so what runs is what was validated at publication.
type EngineCustomPlugin struct {
	// Handle is the publication handle (`source_resource_handle`), the stable
	// key admin governance acts on.
	Handle string `json:"handle"`
	// PluginID is the package's own id, or promptfoo's default when it declared
	// none. This is what the report will show, so it travels with the package
	// rather than being re-derived at the far end.
	PluginID string `json:"plugin_id"`
	// Definition is the canonical YAML document.
	Definition string `json:"definition"`
	// SourceExpertUserID attributes the package in the run record without
	// naming the expert in the browser-visible report.
	SourceExpertUserID string `json:"source_expert_user_id,omitempty"`
	// Version is the publication's version, recorded so a report can be traced
	// back to the exact package revision that produced it.
	Version string `json:"version,omitempty"`
}

// CustomPluginRefError reports a reference the engine must not be handed,
// carrying the machine-readable code the API surfaces and a message that names
// the actual reason.
//
// The disabled-vs-unknown distinction is deliberately collapsed: telling a
// submitter "this exists but an admin disabled it" leaks governance state about
// someone else's package to anyone who can guess a handle. Both read as
// "不可用".
type CustomPluginRefError struct {
	Code    string
	Ref     string
	Message string
}

// Error implements error.
func (e *CustomPluginRefError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Error code for a reference that cannot be resolved to a loadable package.
const CustomPluginErrRefUnavailable = "pf_plugin_ref_unavailable"

// customPluginPayloadMaterializer reads and decrypts a published package.
//
// This is `ResourceStoreService.MaterializeResource`'s method shape rather than
// the store interface: decryption needs the KeyStore, and re-implementing the
// decrypt here would create a second path to the plaintext with its own
// key-resolution rules. Reusing the service keeps one answer to "what does this
// package decrypt to".
type customPluginPayloadMaterializer interface {
	MaterializeResource(ctx context.Context, userID uuid.UUID, idOrHandle string) (*EvaluationResourceMaterialization, error)
}

// CustomPluginResolver resolves `pf_plugin:<handle>` references into
// engine-loadable packages, applying admin governance on the way.
type CustomPluginResolver struct {
	publications CustomPluginPublicationLookup
	resources    customPluginPayloadMaterializer
}

// NewCustomPluginResolver builds a resolver. Both dependencies are required;
// a partially-wired resolver would silently resolve nothing, which is the shape
// of bug this codebase keeps refusing to accept.
func NewCustomPluginResolver(publications CustomPluginPublicationLookup, resources customPluginPayloadMaterializer) *CustomPluginResolver {
	return &CustomPluginResolver{publications: publications, resources: resources}
}

// Enabled reports whether the resolver can do anything at all.
func (r *CustomPluginResolver) Enabled() bool {
	return r != nil && r.publications != nil && r.resources != nil
}

// CustomPluginEngineMarker prefixes a package reference on its way to the
// engine. Chosen to be unmistakably not a promptfoo plugin id, and to survive
// the engine's own `startsWith('file://')` checks in either direction.
const CustomPluginEngineMarker = "promptfoo-custom:"

// resolveOne resolves a single `pf_plugin:<handle>` reference into an
// engine-loadable package, applying admin governance.
//
// # Governance
//
// `GetPublishedByHandle` filters on `enabled = true AND status = 'published'`.
// A package an admin disabled or archived therefore resolves to nil here and is
// rejected — that is the whole mechanism behind "管理员停用 → 引擎立即不可用",
// and it is consulted per run rather than cached, so the effect is immediate.
//
// # Why the caller owns the substitution
//
// The run request's `plugins` array is the engine's own contract, and it needs
// TWO id lists: the `promptfoo-custom:` marker the engine swaps for a `file://`
// path inside its workdir, and the package's own id for the run record and the
// report. So this method returns one package and lets the caller
// (`RedteamToolBridge.resolveCustomPlugins`) build both lists — keeping that
// decision at the one place that has to live, instead of in a resolver that
// would need to know about records it does not own.
func (r *CustomPluginResolver) resolveOne(ctx context.Context, userID uuid.UUID, ref string) (*EngineCustomPlugin, error) {
	handle := CustomPluginRefTarget(ref)
	if handle == "" {
		return nil, &CustomPluginRefError{
			Code:    CustomPluginErrRefUnavailable,
			Ref:     ref,
			Message: "自定义插件引用缺少句柄，无法加载。请重新选择该插件。",
		}
	}
	pub, err := r.publications.GetPublishedByHandle(ctx, handle)
	if err != nil {
		return nil, err
	}
	if pub == nil {
		// Covers unknown, disabled and archived alike — see the type's doc for
		// why the three must not be distinguishable.
		return nil, &CustomPluginRefError{
			Code: CustomPluginErrRefUnavailable,
			Ref:  ref,
			Message: "自定义插件 " + handle + " 当前不可用：可能已被管理员停用、下架或归档，也可能已删除。" +
				"管理员对插件的治理是即时生效的，请刷新插件列表后重试。",
		}
	}
	if kind := EvaluationResourceKind(pub.Kind); kind != EvaluationResourceKindPromptfooPlugin {
		return nil, &CustomPluginRefError{
			Code:    CustomPluginErrRefUnavailable,
			Ref:     ref,
			Message: "资源 " + handle + " 不是自定义插件（kind=" + pub.Kind + "），不能作为插件加载。",
		}
	}
	// Materialize as the **publishing expert**, not the caller: the encrypted row
	// is owned by the expert who published it (`owner_user_id`), and the
	// publication record carries that id. Reading it as the enterprise user who
	// selected the plugin would miss the row entirely.
	materialized, err := r.resources.MaterializeResource(ctx, pub.SourceExpertUserID, pub.SourceResourceHandle)
	if err != nil {
		return nil, err
	}
	if materialized == nil || strings.TrimSpace(materialized.Payload) == "" {
		return nil, &CustomPluginRefError{
			Code:    CustomPluginErrRefUnavailable,
			Ref:     ref,
			Message: "自定义插件 " + handle + " 的发布记录存在但内容缺失，无法加载。请让专家重新发布该插件。",
		}
	}
	pluginID := strings.TrimSpace(pub.Metadata[CustomPluginMetadataIDKey])
	if pluginID == "" {
		pluginID = PromptfooCustomPluginDefaultID
	}
	return &EngineCustomPlugin{
		Handle:             handle,
		PluginID:           pluginID,
		Definition:         materialized.Payload,
		SourceExpertUserID: pub.SourceExpertUserID.String(),
		Version:            normalizeVersion(pub.SourceVersion),
	}, nil
}
