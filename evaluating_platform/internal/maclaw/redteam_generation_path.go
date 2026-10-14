package maclaw

// redteam_generation_path.go — P2-6 · which generation path produced a report.
//
// # The problem this field answers
//
// After U7 opened Plan C, two runs at identical settings could produce
// incomparable numbers while looking identical: the U7 acceptance run measured
// Plan A = 50 cases against Plan C = 80 probes / 6 sub-plugins for the same input,
// with different strategies and (for a while) a different judging basis. Item 14
// recorded the consequence: "两次报告若并列对比必然误导".
//
// A reader cannot tell the two apart from the report, because nothing on it said
// which path ran. This is the field that says.
//
// # Shape, and why it mirrors judge_track
//
// Same template as `redteam_judge_track.go`, including the deliberate asymmetry:
//
//   - the WRITE path is fail-closed — an unknown value refuses the report, because
//     a value nobody can interpret would otherwise be published under a heading
//     that claims to explain the numbers;
//   - the READ path does not error — old reports have no such key, and refusing to
//     open them would be worse than saying "not recorded". It must not silently
//     present an unknown path as one of the two, so the caption says so.
//
// Stored in `report.metadata` (the column exists; DD-5=A — no migration, no new
// JSONB).

import (
	"fmt"
	"strings"
)

// Generation path values. A fixed enum, so the value is safe to store, render and
// compare — no free text, and nothing that could carry a prompt or a path.
const (
	// GenerationPathPlanA is the platform's own generation LLM synthesising attack
	// prompts, with promptfoo in evaluate mode.
	GenerationPathPlanA = "plan_a"
	// GenerationPathPlanC is promptfoo's native redteam machinery (`redteam.run`).
	GenerationPathPlanC = "plan_c"
)

// Metadata keys. `generation_path` is the field this file's name refers to;
// `num_tests_effective` is P2-4's honesty channel — the probe count actually
// configured, after the platform total was divided across the selection.
const (
	RedteamGenerationPathMetadataKey    = "generation_path"
	RedteamNumTestsEffectiveMetadataKey = "num_tests_effective"
	// U4's judge track is stored separately and answers a different question ("who
	// judged"), exactly as its own header explains. Neither replaces the other.
)

// NormalizeRedteamGenerationPath validates a generation-path value.
//
// Empty is allowed and means "not recorded" — that is what a pre-P2-6 report has,
// and inventing a value for it would be a guess published as a fact.
func NormalizeRedteamGenerationPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	switch trimmed {
	case GenerationPathPlanA, GenerationPathPlanC:
		return trimmed, nil
	default:
		return "", fmt.Errorf(
			"invalid %s %q: want %q or %q (a fixed enum; an unknown value cannot be explained in a report)",
			RedteamGenerationPathMetadataKey, raw, GenerationPathPlanA, GenerationPathPlanC,
		)
	}
}

// ValidateReportGenerationMetadata validates the path and the effective count
// together, because they are written together and a half-written pair is worse
// than neither: a report claiming Plan C with no probe count invites the reader to
// assume it equals the request.
func ValidateReportGenerationMetadata(metadata map[string]string) error {
	if len(metadata) == 0 {
		return nil
	}
	path, err := NormalizeRedteamGenerationPath(metadata[RedteamGenerationPathMetadataKey])
	if err != nil {
		return err
	}
	effective := strings.TrimSpace(metadata[RedteamNumTestsEffectiveMetadataKey])
	if effective == "" {
		return nil
	}
	count, err := parsePositiveInt(effective)
	if err != nil {
		return fmt.Errorf(
			"invalid %s %q: want a positive integer (the configured probe count, not a comparison)",
			RedteamNumTestsEffectiveMetadataKey, effective,
		)
	}
	// A path with no count is tolerated (older engine builds sent one without the
	// other); a count without a path is not, because a bare number is then read as
	// "probes" without the reader knowing which path configured it.
	if count > 0 && path == "" {
		return fmt.Errorf(
			"%s is set but %s is not: the count is only meaningful together with the path that produced it",
			RedteamNumTestsEffectiveMetadataKey, RedteamGenerationPathMetadataKey,
		)
	}
	return nil
}

func parsePositiveInt(raw string) (int, error) {
	value := 0
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, fmt.Errorf("want a positive integer, got %d", value)
	}
	return value, nil
}

// RedteamGenerationPathCaption renders the one-line explanation for the report's
// information section.
//
// Three shapes, and the wording carries the difference:
//
//   - unrecorded → says so, and does NOT pick a path (pre-P2-6 reports);
//   - plan_c → names the path, states the configured probe count, and says the two
//     paths are not directly comparable;
//   - plan_a → names the path and the count.
func RedteamGenerationPathCaption(metadata map[string]string) string {
	path, err := NormalizeRedteamGenerationPath(metadata[RedteamGenerationPathMetadataKey])
	if err != nil {
		// The read path must not fail, but it also must not pretend: a report whose
		// metadata is corrupt is labelled as such rather than presented as one of
		// the two paths.
		return "生成路径：报告元数据中的生成路径字段非法，已忽略；请联系报告生成方核对。"
	}
	effective := ""
	if raw := strings.TrimSpace(metadata[RedteamNumTestsEffectiveMetadataKey]); raw != "" {
		if count, parseErr := parsePositiveInt(raw); parseErr == nil {
			effective = fmt.Sprintf("·实际配置探针数 %d", count)
		}
	}
	switch path {
	case GenerationPathPlanA:
		return "生成路径：平台合成用例（plan_a）" + effective + "。"
	case GenerationPathPlanC:
		return "生成路径：promptfoo 原生生成（plan_c）" + effective +
			"·与平台合成用例（plan_a）的报告不可直接比较（探针数量与判定口径均不同）。"
	default:
		return "生成路径：未记录（本报告产生于记录生成路径之前，或由其它路径生成）。"
	}
}
