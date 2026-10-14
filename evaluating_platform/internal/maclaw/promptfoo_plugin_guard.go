package maclaw

// promptfoo_plugin_guard.go — P2-2 · plugin-id legality pre-flight guard (L2).
//
// # The defect this guard closes
//
// P2-1 (promptfoo_plugin_legality.go) removed the three ids promptfoo rejects
// from the catalog, so a user cannot pick one in the selector. That fixes the
// SELECTOR and nothing else. Three other paths still reach the engine with an
// arbitrary plugin id:
//
//   - an expert-authored package that names a platform id internally,
//   - a stored wizard configuration / a client calling the API directly,
//   - a run already queued when the catalog changed.
//
// promptfoo's zod rejects the id mid-run and the WHOLE run fails — after the
// form was submitted, after an engine_runs row exists, after the user waited.
// This guard moves that verdict to the request boundary: no run record, no
// engine call, and a message that says which id and why.
//
// # Why it is separate from the U6 guard
//
// U6 asks "can this plugin run here" (remote generation is fail-closed). This
// asks "does this plugin exist". Both are pre-flight and both refuse with 409,
// but they are different questions with different remedies: U6 says "pick a
// local one", this says "this id was never valid" and, for a withdrawn id,
// where the capability moved.
//
// # Position in the handler
//
// Before target-credential materialization, run persistence and engine
// dispatch — same as U6. A request that cannot succeed must not decrypt a
// target secret or leave a row behind.

import (
	"fmt"
	"sort"
	"strings"
)

// EngineErrPluginIDInvalid is the machine-readable refusal code. The frontend
// maps it to a readable message; operators grep for it when a run is refused.
const EngineErrPluginIDInvalid = "plugin_id_invalid"

// EngineErrPluginRequiresConfig is the refusal code for a LEGAL plugin the
// platform cannot feed yet. Distinct from EngineErrPluginIDInvalid on purpose:
// the id is fine and the capability is intended to work — what is missing is
// configuration the platform does not collect (currently only `config.policy`).
// One code would tell the user their selection is broken when the truth is that
// a platform feature is pending.
const EngineErrPluginRequiresConfig = "plugin_requires_config"

// EngineErrNumTestsTooSmall refuses a `num_tests` that cannot cover the selected
// plugins' expansion (P2-4). Sibling of EngineErrNumTestsTooLarge: one bounds the
// total from above, this bounds it from below.
const EngineErrNumTestsTooSmall = "num_tests_too_small"

// EngineNumTestsTooSmallError reports that the requested total is below the floor
// its selection implies.
type EngineNumTestsTooSmallError struct {
	Requested int
	// ExpandedPlugins is sum(fanout) for the selection: the number of redteam
	// sub-plugins promptfoo will generate for, each of which needs at least one probe.
	ExpandedPlugins int
	// Collections names the ids whose expansion drove the floor, so the message can
	// point at the choice that made the number too small.
	Collections []string
}

// Error implements error. It states the arithmetic and both ways out — raise the
// number, or pick specific sub-plugins — because "too small" alone leaves the user
// with no idea that `harmful` is 26 probes.
func (e *EngineNumTestsTooSmallError) Error() string {
	detail := ""
	if len(e.Collections) > 0 {
		detail = "（" + strings.Join(e.Collections, "、") + " 会展开成多个子插件）"
	}
	return fmt.Sprintf(
		"用例数量 %d 少于选中插件展开后的子插件数 %d%s：每个子插件至少要产生 1 条用例。"+
			"请提高用例数量到 %d 以上，或改选具体的子插件（例如用 harmful:cybercrime 代替 harmful）。",
		e.Requested, e.ExpandedPlugins, detail, e.ExpandedPlugins,
	)
}

// ResolveEngineRunNumTests returns the effective `num_tests` for a request.
//
// When the caller supplied a count, it is returned unchanged — validation is the
// caller's next step, and silently adjusting a number somebody typed is the
// behaviour this batch exists to remove. Only an ABSENT count (<= 0) is resolved,
// and then to `max(fallback, the selection's floor)`.
//
// Why the floor raises the fallback: the default plugin selection is `harmful`,
// which expands to 26 sub-plugins, while the default counts were 20 (wizard) and
// 5 (chat). Raising to the floor keeps the platform's own defaults submittable;
// without it the default form and the default plan card would both be refused by
// the coverage check — a self-inflicted dead end that no user choice produced. The
// resolved number is what the run record and the report carry, so nothing is
// hidden: the user sees 26 rather than a refusal for a count they never chose.
func ResolveEngineRunNumTests(requested int, fallback int, plugins []EngineCapabilityRef) int {
	if requested > 0 {
		return requested
	}
	if floor := numTestsFloor(plugins); floor > fallback {
		return floor
	}
	return fallback
}

// numTestsFloor is sum(fanout) for a selection: the fewest probes that can still
// give every expanded sub-plugin a case.
func numTestsFloor(plugins []EngineCapabilityRef) int {
	ids := make([]string, 0, len(plugins))
	for _, ref := range plugins {
		id := strings.TrimSpace(ref.ID)
		if id == "" || isCustomPluginReference(id) {
			continue
		}
		ids = append(ids, id)
	}
	return PromptfooPluginFanoutTotals(ids).ExpandedPlugins
}

// ValidateEngineRunNumTestsCoverage refuses a total below the selection's
// expansion floor (P2-4).
//
// Same placement as its siblings, and for the same reason: a request that cannot
// succeed must be refused before a run record exists. The floor is not a platform
// policy — it is arithmetic: promptfoo applies `numTests` to each EXPANDED
// sub-plugin, so selecting `harmful` runs all 26 of them however small the total
// is. The engine enforces this too (`native_generation_num_tests_too_small`), but
// refusing here means no engine_runs row and no engine call.
func ValidateEngineRunNumTestsCoverage(plugins []EngineCapabilityRef, numTests int) error {
	floor := numTestsFloor(plugins)
	if floor == 0 || numTests >= floor {
		return nil
	}
	ids := make([]string, 0, len(plugins))
	for _, ref := range plugins {
		id := strings.TrimSpace(ref.ID)
		if id == "" || isCustomPluginReference(id) {
			continue
		}
		ids = append(ids, id)
	}
	totals := PromptfooPluginFanoutTotals(ids)
	collections := make([]string, 0, len(totals.FanoutByPlugin))
	for id, fanout := range totals.FanoutByPlugin {
		if fanout > 1 {
			collections = append(collections, id)
		}
	}
	sort.Strings(collections)
	return &EngineNumTestsTooSmallError{
		Requested:       numTests,
		ExpandedPlugins: floor,
		Collections:     collections,
	}
}

// EnginePluginConfigGuardError refuses a run that selects a plugin which cannot
// produce test cases until the platform collects configuration for it.
//
// P2-2 / plan 18 D-3(a): the catalogue and the selector already advertise
// `requires_config`, but advertising is not refusing. Without this guard the
// request reaches the engine, generates 0 cases, and fails with
// `native_generation_no_test_cases` — a failure the user cannot act on, which is
// the shape D-3 exists to remove.
type EnginePluginConfigGuardError struct {
	Code string
	// Plugins are the offending ids, deduplicated and sorted.
	Plugins []string
	// Reasons maps each id to why it needs configuration.
	Reasons map[string]string
}

// Error implements error. It names each plugin and what the platform is missing.
func (e *EnginePluginConfigGuardError) Error() string {
	parts := make([]string, 0, len(e.Plugins))
	for _, id := range e.Plugins {
		if reason := strings.TrimSpace(e.Reasons[id]); reason != "" {
			parts = append(parts, id+"（"+reason+"）")
			continue
		}
		parts = append(parts, id)
	}
	return "以下插件需要平台尚未收集的配置，选中它们本次会生成 0 条用例并使运行失败，请改选其它插件：" +
		strings.Join(parts, "；")
}

// ValidateEngineRunPluginConfig refuses a request naming a plugin that is legal
// but cannot generate cases until configuration exists (`policy` today).
//
// Runs alongside ValidateEngineRunPluginLegality and for the same reason: the
// verdict belongs at the request boundary, before credential materialization,
// persistence and dispatch, so a request that cannot produce cases costs
// nothing. Custom plugin references are exempt for the same reason as there.
func ValidateEngineRunPluginConfig(plugins []EngineCapabilityRef) error {
	invalid := make(map[string]bool)
	for _, ref := range plugins {
		id := strings.TrimSpace(ref.ID)
		if id == "" || isCustomPluginReference(id) {
			continue
		}
		if PluginRequiresConfig(id) {
			invalid[id] = true
		}
	}
	if len(invalid) == 0 {
		return nil
	}

	guard := &EnginePluginConfigGuardError{
		Code:    EngineErrPluginRequiresConfig,
		Plugins: make([]string, 0, len(invalid)),
		Reasons: make(map[string]string, len(invalid)),
	}
	for id := range invalid {
		guard.Plugins = append(guard.Plugins, id)
		guard.Reasons[id] = PluginRequiresConfigReason(id)
	}
	sort.Strings(guard.Plugins)
	return guard
}

// EnginePluginLegalityGuardError reports which requested plugin ids promptfoo
// does not accept. It carries the engine's own rejection text (so the user sees
// the real reason rather than a paraphrase) and, for withdrawn ids, the
// replacement guidance recorded in retiredPluginCatalog.
type EnginePluginLegalityGuardError struct {
	Code string
	// InvalidPlugins are the offending ids, deduplicated and sorted.
	InvalidPlugins []string
	// Rejections maps each id to promptfoo's verbatim zod message.
	Rejections map[string]string
	// Reasons maps each id to the platform's own explanation (why the catalog
	// carried it in the first place) — present only for ids the platform
	// withdrew, which is the case where the user needs more than "invalid".
	Reasons map[string]string
	// Guidance carries the replacement path for withdrawn ids, so the refusal
	// answers "where did that capability go" as well as "why can't I use it".
	Guidance []PromptfooRetiredPlugin
}

// Error implements error. It names every offending id, the engine's reason, and
// — for withdrawn ids — what to use instead. An error that only said "invalid
// plugin" would leave the user to guess which one and why.
func (e *EnginePluginLegalityGuardError) Error() string {
	parts := make([]string, 0, len(e.InvalidPlugins))
	for _, id := range e.InvalidPlugins {
		detail := strings.TrimSpace(e.Rejections[id])
		if reason := strings.TrimSpace(e.Reasons[id]); reason != "" {
			detail = reason
		}
		if detail == "" {
			parts = append(parts, id)
			continue
		}
		parts = append(parts, id+"（"+detail+"）")
	}
	msg := "以下插件不是 promptfoo 支持的插件 id，选中它们的评测必然整次失败：" + strings.Join(parts, "；")
	for _, g := range e.Guidance {
		if label := strings.TrimSpace(g.ReplacementLabel); label != "" {
			msg += "。" + label
		}
	}
	return msg
}

// ValidateEngineRunPluginLegality refuses a request naming a plugin id
// promptfoo rejects. Returns nil when every id is acceptable (or unknown — see
// PluginIDIsLegal for why unknown ids are not this guard's business).
//
// Custom plugin references are skipped: `pf_plugin:<handle>` is resolved
// server-side into a `file://` package the engine loads itself, and those ids
// take a separate zod branch. Judging them here would reject the one class of
// plugin whose legality genuinely cannot be known until the package is read.
func ValidateEngineRunPluginLegality(plugins []EngineCapabilityRef) error {
	invalid := make(map[string]bool)
	for _, ref := range plugins {
		id := strings.TrimSpace(ref.ID)
		if id == "" || isCustomPluginReference(id) {
			continue
		}
		if !PluginIDIsLegal(id) {
			invalid[id] = true
		}
	}
	if len(invalid) == 0 {
		return nil
	}

	guard := &EnginePluginLegalityGuardError{
		Code:           EngineErrPluginIDInvalid,
		InvalidPlugins: make([]string, 0, len(invalid)),
		Rejections:     make(map[string]string, len(invalid)),
		Reasons:        make(map[string]string, len(invalid)),
	}
	for id := range invalid {
		guard.InvalidPlugins = append(guard.InvalidPlugins, id)
		if rejection := PluginLegalityRejection(id); rejection != "" {
			guard.Rejections[id] = rejection
		}
		if reason := PluginLegalityReason(id); reason != "" {
			guard.Reasons[id] = reason
		}
		if guidance, ok := PromptfooRetiredPluginGuidance(id); ok {
			guard.Guidance = append(guard.Guidance, guidance)
		}
	}
	sort.Strings(guard.InvalidPlugins)
	sort.Slice(guard.Guidance, func(i, j int) bool { return guard.Guidance[i].ID < guard.Guidance[j].ID })
	return guard
}

// isCustomPluginReference reports whether an id names an expert-published
// package rather than a built-in plugin.
//
// Three spellings reach this point: the `pf_plugin:<handle>` reference the
// wizard sends, the `promptfoo-custom:` marker the bridge swaps in for the
// engine, and a raw `file://` path (expert packs, and the engine's own
// internal form). All three are resolved/validated downstream.
func isCustomPluginReference(id string) bool {
	if IsCustomPluginRef(id) {
		return true
	}
	lower := strings.ToLower(id)
	return strings.HasPrefix(lower, CustomPluginEngineMarker) || strings.HasPrefix(lower, "file://")
}

// NumTestsFloorForTest exposes the selection floor to tests in this package
// without widening the production API surface.
func NumTestsFloorForTest(pluginIDs []string) int {
	refs := make([]EngineCapabilityRef, 0, len(pluginIDs))
	for _, id := range pluginIDs {
		refs = append(refs, EngineCapabilityRef{ID: id})
	}
	return numTestsFloor(refs)
}
