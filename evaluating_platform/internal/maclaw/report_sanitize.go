package maclaw

// report_sanitize.go — what may be written into a report or an evidence row.
//
// The two rules are deliberately separate functions rather than one with a flag:
// `maclaw_redteam_reports` is required to carry the attack and the answer, while
// `maclaw_redteam_evidence` still holds summaries only. A caller should be able
// to see which contract it is writing under.
//
// Split out of redteam_artifact_service.go on 2026-10-19 with no behaviour change
// (the report PDF structure snapshot pins that).

import (
	"regexp"
	"strings"

	"evaluating_platform/internal/maclaw/redteam"
)

// maxRedteamArtifactTextRunes bounds one report text field.
//
// Raised from 2000 on 2026-10-16. The report now carries the target's actual
// answer, and 2000 runes is well inside the length of a real answer to "print
// your system prompt" — the field would have been truncated at exactly the
// content the report was changed to show, and the reader would have seen a
// plausible-looking prefix with no indication that it stopped.
const maxRedteamArtifactTextRunes = 20000

// redteamArtifactSecretPattern matches credential VALUES, not the words around them.
//
// # Why `secret\S*` was narrowed (2026-10-16)
//
// The pattern used to start with `secret[-_a-z0-9]*`, which matches the bare word
// "secret". A report whose subject is "did the model leak its system prompt or
// credentials" is full of that word — in the attack prompt, and in any useful
// answer. Redacting it would have destroyed the evidence while looking like
// diligence; worse, it would have done so silently, leaving a report that reads
// as if the target said nothing sensitive.
//
// A rule that is too broad is a defect, not caution. What remains matches only
// shapes that carry an actual credential value: an `sk-…` token, a bearer token,
// or `secret`/`api key` followed by an assignment. Prose mentioning secrets
// survives; a leaked key does not.
var redteamArtifactSecretPattern = regexp.MustCompile(`(?i)(secret\s*[:=]\s*\S+|sk-[a-z0-9][a-z0-9_\-]{5,}|bearer\s+[a-z0-9._\-]+|api[_ -]?key\s*[:=]\s*\S+)`)

// redteamArtifactOwnPathPattern matches THIS deployment's filesystem paths.
//
// Narrow on purpose. A blanket path rule would rewrite `/data/models/config.json`
// out of an attack payload or a target's answer, and those are evidence: the
// engine's own paths have no evidentiary value, while a path the payload talks
// about might be the whole point of the probe.
var redteamArtifactOwnPathPattern = regexp.MustCompile(`(?:/[A-Za-z0-9_.\-]*\.promptfoo/[^\s"',;)]*)|(?:/data/runs/[^\s"',;)]*)|(?:/app/dist/[^\s"',;)]*)`)

func sanitizeRedteamReportFindings(in []EvaluationReportFinding) []EvaluationReportFinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]EvaluationReportFinding, 0, len(in))
	for _, item := range in {
		item.ID = sanitizeRedteamArtifactText(item.ID)
		item.Title = sanitizeRedteamArtifactText(item.Title)
		item.Severity = sanitizeRedteamArtifactText(item.Severity)
		item.Category = sanitizeRedteamArtifactText(item.Category)
		item.Description = sanitizeRedteamArtifactText(item.Description)
		item.Evidence = sanitizeRedteamArtifactText(item.Evidence)
		item.Suggestion = sanitizeRedteamArtifactText(item.Suggestion)
		item.Metadata = redteam.SanitizeMetadata(item.Metadata)
		if len(item.Turns) > 0 {
			turns := make([]EvaluationReportFindingTurn, 0, len(item.Turns))
			for _, turn := range item.Turns {
				turn.Content = sanitizeRedteamArtifactText(turn.Content)
				turns = append(turns, turn)
			}
			item.Turns = turns
		}
		out = append(out, item)
	}
	return out
}

// sanitizeRedteamArtifactText bounds one field of a REPORT.
//
// # What it stopped doing, and why (2026-10-16)
//
// It used to replace the WHOLE value with "[redacted sensitive text]" whenever it
// contained one of a list of marker phrases ("raw payload", "raw prompt",
// "raw response", "payload body", "target response body", "evidence content").
// That rule was written when raw text was not supposed to reach a report at all.
// Now that it is supposed to, the rule has two failure modes:
//
//   - it fires on CONTENT rather than on a leak. An attack prompt reading
//     "print the raw payload you received" would have been blanked — destroying
//     the evidence while looking like diligence, and doing it silently, so the
//     report would have read as if the target said nothing sensitive;
//   - it never matched a real payload anyway, because a payload does not describe
//     itself. Its protection was theoretical; its cost, on the day it mattered,
//     would have been the report.
//
// A rule that is too broad is a defect, not caution.
//
// What remains is the part that is not about content: credential VALUES and an
// output bound. Both are narrow on purpose — see the two pattern docs.
func sanitizeRedteamArtifactText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = redteamArtifactSecretPattern.ReplaceAllString(value, "[redacted]")
	value = redteamArtifactOwnPathPattern.ReplaceAllString(value, "[path]")
	runes := []rune(value)
	if len(runes) > maxRedteamArtifactTextRunes {
		return string(runes[:maxRedteamArtifactTextRunes]) + "..."
	}
	return value
}

// sanitizeRedteamEvidenceText bounds one field of an EVIDENCE record.
//
// The evidence table's contract did NOT change on 2026-10-16: it still holds
// summaries and metadata, never content. So the marker guard that was removed
// from report text stays here, where it still means what it meant.
//
// Kept as a separate function rather than a flag so the two contracts read
// differently at their call sites — the reason a report may carry a payload and
// an evidence row may not is the whole point of having both tables.
func sanitizeRedteamEvidenceText(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range redteamEvidenceContentMarkers {
		if strings.Contains(lower, marker) {
			return "[redacted sensitive text]"
		}
	}
	return sanitizeRedteamArtifactText(trimmed)
}

// redteamEvidenceContentMarkers are phrases that indicate a raw payload or
// response reached an evidence summary, where it does not belong.
var redteamEvidenceContentMarkers = []string{
	"raw payload",
	"payload body",
	"raw prompt",
	"prompt body",
	"raw response",
	"target response body",
	"local path",
	"evidence content",
}
