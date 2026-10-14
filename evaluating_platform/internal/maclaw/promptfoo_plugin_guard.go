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
	"sort"
	"strings"
)

// EngineErrPluginIDInvalid is the machine-readable refusal code. The frontend
// maps it to a readable message; operators grep for it when a run is refused.
const EngineErrPluginIDInvalid = "plugin_id_invalid"

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
