package maclaw

// engine_error_brief.go — P2-5 · the platform's half of the `error_brief` channel.
//
// # Why the platform redacts again
//
// The engine already redacts before sending (`promptfoo_engine/src/sanitize/errorBrief.ts`).
// This is a second, independent implementation, and the duplication is deliberate:
// `engine_runs` is a persisted, tenant-facing table, and the only thing worse than
// one missed channel is a second channel that trusts the first's output. Neither
// side may assume the other's text is already safe.
//
// It also has to stand alone: the engine's copy is TypeScript in a different
// container at a different version, and a platform-side fix must not depend on
// shipping an engine image.
//
// # Rules (intentionally the same shape as the engine's)
//
//   - content-shaped messages are replaced wholesale rather than filtered
//   - credentials, URL hosts and filesystem paths are removed
//   - the result is bounded, so the field cannot become a storage-growth vector
//
// # The ordering lesson, kept here so it is not relearned
//
// Paths are rewritten BEFORE the opaque-token rule. The token rule admits `/` and
// `+` (the base64 alphabet), so running it first swallows whole filesystem paths
// as if they were one credential. And the token rule requires a token-LIKE shape
// (mixed case, base64 punctuation, long hex) because matching any 32+ character
// run of identifier characters also matched the error codes this field exists to
// carry — `agent_session_requires_multiturn_strategy` is 41 characters of exactly
// that alphabet. Redaction that removes the diagnosis is not conservative.

import (
	"regexp"
	"strings"
)

// engineErrorBriefEllipsis marks a truncated brief. Counted against the cap below,
// not on top of it.
const engineErrorBriefEllipsis = "..."

// maxEngineErrorBriefRunes bounds the stored brief, ellipsis included.
//
// Runes, not bytes: the messages are largely Chinese, and a byte budget would
// truncate them at roughly a third of the intended length.
const maxEngineErrorBriefRunes = 500

// engineErrorBriefContentMarkers are phrases that mean "this text carries
// content, not a diagnosis". A message containing one is replaced wholesale: a
// partially redacted payload reads as trustworthy, which is worse than an
// obviously missing one.
var engineErrorBriefContentMarkers = []string{
	"raw payload",
	"payload body",
	"raw prompt",
	"prompt body",
	"raw response",
	"target response body",
	"evidence content",
	"test case body",
}

// engineErrorBriefURLPattern replaces the host and port, keeping the scheme and
// the path shape. The path usually names the step that failed; the host says where
// the deployment lives, which the reader does not need and should not have.
var engineErrorBriefURLPattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*)://[^/\s"']+(/[^\s"']*)?`)

// engineErrorBriefPathPattern reduces a filesystem path to its basename, and only
// when it ends in a file extension. The extension requirement is what separates it
// from a URL path (handled above) and from prose.
var engineErrorBriefPathPattern = regexp.MustCompile(`(^|[\s("'=])(?:/[\w.@+-]+)+/([\w.@+-]+\.[A-Za-z0-9]{1,8})\b`)

// engineErrorBriefSecretPatterns mirror the engine's list, minus the "opaque run"
// rule — see engineErrorBriefOpaqueRunPattern for why that one is a predicate
// rather than a regex.
var engineErrorBriefSecretPatterns = []*regexp.Regexp{
	// Vendor-style keys and explicit credential assignments.
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{6,}`),
	regexp.MustCompile(`(?i)\b(?:authorization|bearer|api[_-]?key|apikey|access[_-]?token|password|secret)\b\s*[:=]?\s*["']?[^\s"',;)]+`),
}

// engineErrorBriefOpaqueRunPattern finds runs long enough to be a credential.
//
// This is deliberately NOT a set of hand-written patterns: Go's regexp is RE2, so
// lookahead is unavailable, and expressing "32+ characters, mixed case" as an
// alternation would be both unreadable and wrong at the edges. Matching the run
// and deciding with a predicate says what the rule is.
var engineErrorBriefOpaqueRunPattern = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)

// looksLikeToken reports whether a long run is credential-shaped rather than an
// identifier.
//
// This distinction is the whole reason the rule is a predicate. An earlier version
// redacted any 32+ character run of the identifier alphabet, which swallowed the
// error codes this field exists to carry
// (`agent_session_requires_multiturn_strategy` is 41 characters of exactly that
// alphabet), producing briefs like `[[redacted]] nope`.
func looksLikeToken(run string) bool {
	if len(run) >= 64 {
		// A very long unbroken run is not prose, whatever its case.
		return true
	}
	hasUpper, hasLower, hasBase64Punct, hexOnly := false, false, false, true
	for _, r := range run {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
			hexOnly = false
		case r >= 'a' && r <= 'f':
			hasLower = true
		case r >= 'g' && r <= 'z':
			hasLower = true
			hexOnly = false
		case r >= '0' && r <= '9':
			// keeps hexOnly as-is: digits are valid hex
		case r == '+' || r == '/' || r == '=':
			hasBase64Punct = true
			hexOnly = false
		case r == '_' || r == '-':
			hexOnly = false
		}
	}
	if hasBase64Punct || (hasUpper && hasLower) {
		return true
	}
	// Pure hex has no case distinction, so mixed case cannot identify it.
	return hexOnly && len(run) >= 32
}

// redactEngineErrorBrief returns a storable form of an engine error message.
//
// Returns "" for empty input so the column holds an empty string rather than
// "undefined" / "<nil>": readers treat emptiness as "no error recorded" and a
// literal would read as one.
func redactEngineErrorBrief(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	lower := strings.ToLower(trimmed)
	for _, marker := range engineErrorBriefContentMarkers {
		if strings.Contains(lower, marker) {
			return "[redacted: message carried content]"
		}
	}

	out := engineErrorBriefURLPattern.ReplaceAllString(trimmed, "$1://[host]$2")
	out = engineErrorBriefPathPattern.ReplaceAllString(out, "${1}${2}")
	for _, pattern := range engineErrorBriefSecretPatterns {
		out = pattern.ReplaceAllString(out, "[redacted]")
	}
	out = engineErrorBriefOpaqueRunPattern.ReplaceAllStringFunc(out, func(run string) string {
		if looksLikeToken(run) {
			return "[redacted]"
		}
		return run
	})

	runes := []rune(out)
	if len(runes) > maxEngineErrorBriefRunes {
		// The bound holds for the RESULT, ellipsis included. Appending three
		// characters past the limit produced 503-rune values for a documented
		// 500-rune cap (2026-10-14 实测记录 §2.3) — and a bound the code does not
		// enforce is not a bound.
		body := maxEngineErrorBriefRunes - len([]rune(engineErrorBriefEllipsis))
		return string(runes[:body]) + engineErrorBriefEllipsis
	}
	return out
}
