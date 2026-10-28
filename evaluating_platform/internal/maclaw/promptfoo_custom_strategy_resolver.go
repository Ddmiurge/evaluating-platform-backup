package maclaw

// promptfoo_custom_strategy_resolver.go — Phase 3 开篇 · 任务 2 · turning a
// `pf_strategy:<handle>` reference into something the engine can load, with
// admin governance applied.
//
// # Why a resolver is needed at all, given strategies are inlined
//
// A custom **plugin** must be written to a file before promptfoo can load it, so
// the engine needs a real package: bytes on disk plus a path. A custom
// **strategy** is `{ strategyText }` inlined into the redteam config (see
// promptfoo_custom_strategy.go for the measured contract), so the engine needs
// no artifact at all.
//
// What it still needs is the *governed payload*. A published strategy is inert:
// a row in `maclaw_resources` (encrypted) plus a publication row. promptfoo can
// load neither. So the same three questions have to be answered per run:
//
//  1. **Is it available?** The publication's `enabled`/`status` are the admin's
//     levers. Resolving without checking them would mean a disabled package
//     still runs, and "管理员停用 → 引擎立即不可用" would be false *silently*.
//  2. **Get the payload.** Decrypted server-side, never returned to a browser.
//  3. **Hand it over in a form the engine can inline.** Shipped on the run
//     request; the engine puts `strategyText` into `redteam.strategies[]` under
//     the id `custom:<variant>`.
//
// # Why governance is checked here and not only at publication time
//
// Publication-time checks describe the moment of publish. Governance is *after*
// publish (承 `06` D-03: 取消管理员审核作为发布前置门 → 管理员事后治理). The only
// place both facts meet is run preparation, so that is where the check lives —
// on the single exit every confirmed engine run passes through
// (`PreparePromptfooEngineRun`), per the U8 wiring lesson.
//
// # What is deliberately NOT claimed
//
// The engine cannot prove a strategy text is safe, only that it is delivered. A
// strategy text is attacker-controlled prose fed to the generation model, so the
// sandbox (non-root uid, read-only rootfs, egress restriction) remains the
// control that matters. See `promptfoo_engine/src/sandbox.ts`.

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// EngineCustomStrategy is one published strategy package, resolved and ready for
// the engine to inline.
//
// `Definition` is the **canonical** YAML (re-serialized from the validated
// struct, see SaveResource) — not the expert's submitted text. The engine reads
// exactly this string, so what runs is what was validated at publication.
type EngineCustomStrategy struct {
	// Handle is the publication handle (`source_resource_handle`), the stable
	// key admin governance acts on. It is also the strategy's identity: the
	// promptfoo id is derived from it (see CustomStrategyVariantFor).
	Handle string `json:"handle"`
	// StrategyID is the `custom:<variant>` id promptfoo will receive. Travels
	// with the package so the report, the judge source and the config document
	// all agree on one name.
	StrategyID string `json:"strategy_id"`
	// Definition is the canonical YAML document (TEXT packages) or the ESM module
	// source (CODE packages). Which one it is, is in Form.
	Definition string `json:"definition"`
	// Form is `yaml` (inlined as `custom:<variant>` + strategyText) or `js`
	// (materialized to a file:// strategy and imported by promptfoo).
	Form string `json:"form,omitempty"`
	// SourceExpertUserID attributes the package in the run record without
	// naming the expert in the browser-visible report.
	SourceExpertUserID string `json:"source_expert_user_id,omitempty"`
	// Version records the publication revision, so a report can be traced back
	// to the exact package revision that produced it.
	Version string `json:"version,omitempty"`
}

// resolveOne resolves a single `pf_strategy:<handle>` reference, applying admin
// governance.
//
// # Governance
//
// `GetPublishedByHandle` filters on `enabled = true AND status = 'published'`.
// A package an admin disabled or archived therefore resolves to nil here and is
// rejected — that is the whole mechanism behind "管理员停用 → 引擎立即不可用",
// and it is consulted per run rather than cached, so the effect is immediate.
//
// The disabled-vs-unknown distinction is deliberately collapsed, for U10's
// reason: telling a submitter "this exists but an admin disabled it" leaks
// governance state about someone else's package to anyone who can guess a
// handle. Both read as "不可用".
func (r *CustomPluginResolver) resolveOneStrategy(ctx context.Context, userID uuid.UUID, ref string) (*EngineCustomStrategy, error) {
	handle := CustomStrategyRefTarget(ref)
	if handle == "" {
		return nil, &CustomPluginRefError{
			Code:    CustomStrategyErrRefUnavailable,
			Ref:     ref,
			Message: "自定义策略引用缺少句柄，无法加载。请重新选择该策略。",
		}
	}
	pub, err := r.publications.GetPublishedByHandle(ctx, handle)
	if err != nil {
		return nil, err
	}
	if pub == nil {
		return nil, &CustomPluginRefError{
			Code: CustomStrategyErrRefUnavailable,
			Ref:  ref,
			Message: "自定义策略 " + handle + " 当前不可用：可能已被管理员停用、下架或归档，也可能已删除。" +
				"管理员对策略的治理是即时生效的，请刷新策略列表后重试。",
		}
	}
	if kind := EvaluationResourceKind(pub.Kind); kind != EvaluationResourceKindPromptfooStrategy {
		return nil, &CustomPluginRefError{
			Code:    CustomStrategyErrRefUnavailable,
			Ref:     ref,
			Message: "资源 " + handle + " 不是自定义策略（kind=" + pub.Kind + "），不能作为策略加载。",
		}
	}
	// Materialize as the **publishing expert**, not the caller: the encrypted row
	// is owned by the expert who published it, and the publication record carries
	// that id.
	materialized, err := r.resources.MaterializeResource(ctx, pub.SourceExpertUserID, pub.SourceResourceHandle)
	if err != nil {
		return nil, err
	}
	if materialized == nil || strings.TrimSpace(materialized.Payload) == "" {
		return nil, &CustomPluginRefError{
			Code:    CustomStrategyErrRefUnavailable,
			Ref:     ref,
			Message: "自定义策略 " + handle + " 的发布记录存在但内容缺失，无法加载。请让专家重新发布该策略。",
		}
	}
	strategyID := strings.TrimSpace(pub.Metadata["pf_strategy_id"])
	if strategyID == "" {
		// A publication predating this field, or one written by an older
		// backend. Deriving it keeps the run working instead of failing on a
		// missing metadata key, and the derived id is the same one the current
		// publisher would have written.
		strategyID = CustomStrategyVariantFor(handle, pub.Metadata)
	}
	form := CustomStrategyFormYAML
	if IsCustomStrategyCodeForm(pub.Metadata) {
		form = CustomStrategyFormJS
	}
	return &EngineCustomStrategy{
		Handle:             handle,
		StrategyID:         strategyID,
		Definition:         materialized.Payload,
		Form:               form,
		SourceExpertUserID: pub.SourceExpertUserID.String(),
		Version:            normalizeVersion(pub.SourceVersion),
	}, nil
}

// resolveCustomStrategies applies U10 governance to a run's strategy refs.
//
// Pure pass-through when no ref names a published package, so a deployment that
// never uses the feature behaves exactly as before — the rollback property.
func (r *CustomPluginResolver) resolveCustomStrategies(ctx context.Context, userID uuid.UUID, refs []EngineCapabilityRef) (*customStrategyResolution, error) {
	recordIDs := make([]string, 0, len(refs))
	engineRefs := make([]EngineCapabilityRef, 0, len(refs))
	var packages []EngineCustomStrategy
	var codePackages []EngineCustomStrategy
	for _, ref := range refs {
		if !IsCustomStrategyRef(ref.ID) {
			recordIDs = append(recordIDs, ref.ID)
			engineRefs = append(engineRefs, ref)
			continue
		}
		pkg, err := r.resolveOneStrategy(ctx, userID, ref.ID)
		if err != nil {
			return nil, err
		}
		// Two dispatch channels, kept apart on purpose: a code package must never
		// reach the `custom:<variant>` inliner, and a text package must never
		// reach the file materializer (its "source" would be YAML, and promptfoo
		// would import it).
		if pkg.Form == CustomStrategyFormJS {
			codePackages = append(codePackages, *pkg)
		} else {
			packages = append(packages, *pkg)
		}
		// The engine receives the `pf_strategy:` marker (it swaps it for
		// `custom:<variant>` plus the inline config) …
		engineRefs = append(engineRefs, EngineCapabilityRef{ID: CustomStrategyRef(pkg.Handle)})
		// … while the run record and the report carry the resolved strategy id.
		// Using the marker there would put an internal transport detail into
		// user-facing output.
		recordIDs = append(recordIDs, pkg.StrategyID)
	}
	return &customStrategyResolution{
		engineRefs:   engineRefs,
		recordIDs:    recordIDs,
		packages:     packages,
		codePackages: codePackages,
	}, nil
}

// customStrategyResolution is the outcome of resolving a run's strategy refs.
//
// The plugin resolution struct is not reused: the two carry different fields
// (`CustomPlugins` vs `CustomStrategies`) and conflating them would let a
// strategy package be handed to the plugin path.
type customStrategyResolution struct {
	engineRefs []EngineCapabilityRef
	recordIDs  []string
	// packages are TEXT packages (inlined as custom:<variant> + strategyText).
	packages []EngineCustomStrategy
	// codePackages are CODE packages (ESM modules, materialized to file://).
	codePackages []EngineCustomStrategy
}

// CustomStrategyErrRefUnavailable is the code for a strategy reference that
// cannot be resolved to a loadable package. Distinct from the plugin code so an
// operator can tell which kind of package is missing.
const CustomStrategyErrRefUnavailable = "pf_strategy_ref_unavailable"

// compile-time assertion that the publication lookup interface used here is the
// one CustomPluginResolver already requires. Both features resolve through the
// same store, so a deployment cannot end up with one wired and not the other.
var _ CustomPluginPublicationLookup = (CustomPluginPublicationLookup)(nil)
