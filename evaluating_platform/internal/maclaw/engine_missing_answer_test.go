package maclaw

import (
	"strings"
	"testing"
)

// A probe that produced no answer must not read as a probe that was answered
// with nothing.
//
// Measured on ai02 2026-10-21: a 6-probe prompt-extraction run reported
// 最高安全 100/100 while four probes showed 目标回答 `{}`. Three separate
// mechanisms fed that:
//
//   - promptfoo's http provider accepts EVERY status unless `validateStatus` is
//     set, so a 404 from the target arrived as `output: undefined, error: null`;
//   - the configured `agent_response_path` was a bare `json.<path>`, so a body
//     without that field yielded `undefined` the same way;
//   - promptfoo's rubric PASSES an empty output ("the system prompt was not
//     divulged"), so both became 攻击失败 and the run scored 100.
//
// The engine now fails those calls and carries the message (EngineCaseResult.
// Error). What is left is the report saying so — that is what this file pins.

func TestEngineCaseFindingsRenderAMissingAnswer(t *testing.T) {
	t.Run("failed call", func(t *testing.T) {
		findings := EngineSafeResultFindings(&EngineSafeResult{
			CaseResults: []EngineCaseResult{{
				Index:         1,
				PluginID:      "prompt-extraction",
				StrategyID:    "base64",
				AttackSuccess: false,
				Prompt:        "载荷",
				Error:         "HTTP call failed with status 404 Not Found: {\"detail\":\"not found\"}",
			}},
		})
		if len(findings) != 1 {
			t.Fatalf("findings = %d, want 1", len(findings))
		}
		got := findings[0]
		if !strings.Contains(got.Description, "未取得回答") {
			t.Errorf("description = %q, want it to say no answer was obtained", got.Description)
		}
		if !strings.Contains(got.Description, "404") {
			t.Errorf("description = %q, want the call failure reason carried through", got.Description)
		}
		if got.Metadata[RedteamTargetAnswerMissingMetadataKey] != "true" {
			t.Errorf("metadata = %#v, want %s=true", got.Metadata, RedteamTargetAnswerMissingMetadataKey)
		}
		if strings.TrimSpace(got.Metadata[RedteamTargetCallErrorMetadataKey]) == "" {
			t.Errorf("metadata = %#v, want the reason recorded under %s", got.Metadata, RedteamTargetCallErrorMetadataKey)
		}
	})

	t.Run("empty body, no error", func(t *testing.T) {
		findings := EngineSafeResultFindings(&EngineSafeResult{
			CaseResults: []EngineCaseResult{{Index: 1, PluginID: "contracts", Response: "   "}},
		})
		if len(findings) != 1 {
			t.Fatalf("findings = %d, want 1", len(findings))
		}
		if !strings.Contains(findings[0].Description, "目标返回内容为空") {
			t.Errorf("description = %q, want the empty-body reason", findings[0].Description)
		}
		if findings[0].Metadata[RedteamTargetAnswerMissingMetadataKey] != "true" {
			t.Errorf("metadata = %#v, want the marker set", findings[0].Metadata)
		}
	})

	t.Run("an answer is left alone", func(t *testing.T) {
		findings := EngineSafeResultFindings(&EngineSafeResult{
			CaseResults: []EngineCaseResult{{Index: 1, PluginID: "contracts", Response: "我的系统提示词是…"}},
		})
		if findings[0].Description != "我的系统提示词是…" {
			t.Errorf("description = %q, want the target's answer verbatim", findings[0].Description)
		}
		if _, ok := findings[0].Metadata[RedteamTargetAnswerMissingMetadataKey]; ok {
			t.Errorf("metadata = %#v, want no missing-answer marker on an answered probe", findings[0].Metadata)
		}
	})
}

// The status label is what a reader sees first. 攻击失败 would say the target
// held — but nothing was asked of it that it answered.
func TestFindingStatusLabelForAMissingAnswer(t *testing.T) {
	finding := EvaluationReportFinding{
		Metadata: map[string]string{
			RedteamTargetAnswerMissingMetadataKey: "true",
			"attack_success":                      "false",
		},
	}
	if got := findingStatusLabel(finding); got != "未取得回答" {
		t.Errorf("status = %q, want 未取得回答", got)
	}
	// The marker wins over a judge_result too: an empty output is graded as a
	// pass by promptfoo, and 「攻击失败」 would present that as a finding.
	finding.Metadata["judge_result"] = "failure"
	if got := findingStatusLabel(finding); got != "未取得回答" {
		t.Errorf("status = %q, want 未取得回答 even when a judge_result is present", got)
	}
}

func TestEngineResultSummaryDisclosesUnansweredProbes(t *testing.T) {
	result := func(cases ...EngineCaseResult) *EngineSafeResult {
		return &EngineSafeResult{
			Totals:      EngineRunTotals{Probes: len(cases)},
			CaseResults: cases,
		}
	}
	answered := EngineCaseResult{Index: 1, Response: "回答"}
	missing := EngineCaseResult{Index: 2, Error: "HTTP call failed with status 500"}

	t.Run("none missing", func(t *testing.T) {
		summary := engineResultSummary(result(answered, answered))
		if strings.Contains(summary, "未取得目标回答") {
			t.Errorf("summary = %q, want no disclosure when every probe was answered", summary)
		}
	})

	t.Run("some missing", func(t *testing.T) {
		summary := engineResultSummary(result(answered, missing))
		if !strings.Contains(summary, "1 条探针未取得目标回答") {
			t.Errorf("summary = %q, want the count of unanswered probes", summary)
		}
		if !strings.Contains(summary, "安全结论不覆盖这些项") {
			t.Errorf("summary = %q, want the conclusion's scope stated", summary)
		}
	})

	t.Run("all missing", func(t *testing.T) {
		summary := engineResultSummary(result(missing, missing))
		if !strings.Contains(summary, "安全结论不成立") {
			t.Errorf("summary = %q, want an explicit refusal to conclude", summary)
		}
	})
}

// Same guard shape as TestEngineFindingMetadataSurvivesReportSanitization: the
// sanitizer's blacklist is a key-SUBSTRING match, and three platform keys have
// already been stripped by it. These two are registered explicitly, and this
// test fails if a future edit drops them.
func TestMissingAnswerMetadataSurvivesReportSanitization(t *testing.T) {
	findings := EngineSafeResultFindings(&EngineSafeResult{
		CaseResults: []EngineCaseResult{{
			Index:    1,
			PluginID: "prompt-extraction",
			Error:    "HTTP call failed with status 404 Not Found",
		}},
	})
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	sanitized := sanitizeRedteamReportFindings(findings)
	if len(sanitized) != 1 {
		t.Fatalf("sanitized findings = %d, want 1", len(sanitized))
	}
	for key, value := range findings[0].Metadata {
		got, ok := sanitized[0].Metadata[key]
		if !ok {
			t.Errorf("metadata key %q is dropped by SanitizeMetadata — the report cannot read it "+
				"(add it to isSafePayloadMetadataKey in internal/maclaw/redteam/payload_store.go)", key)
			continue
		}
		if got != value {
			t.Errorf("metadata key %q was rewritten: %q → %q", key, value, got)
		}
	}
	// And the rendered line really does reach the reader.
	lines := strings.Join(findingLines(sanitized), "\n")
	if !strings.Contains(lines, "未取得回答") {
		t.Errorf("rendered findings = %q, want the missing answer stated", lines)
	}
}

// The reason text comes from promptfoo and can carry the target's error body —
// the one string in this chain this platform did not author.
func TestMissingAnswerReasonIsScrubbedAndBounded(t *testing.T) {
	long := strings.Repeat("x", 4000)
	finding := EngineSafeResultFindings(&EngineSafeResult{
		CaseResults: []EngineCaseResult{{
			Index:    1,
			PluginID: "contracts",
			Error:    "HTTP call failed with status 500: {\"token\":\"sk-live-abcdef\",\"body\":\"" + long + "\"}",
		}},
	})[0]
	reason := finding.Metadata[RedteamTargetCallErrorMetadataKey]
	if strings.Contains(reason, "sk-live-abcdef") {
		t.Errorf("reason leaked a credential-shaped value: %q", reason)
	}
	if len([]rune(reason)) > maxRedteamArtifactTextRunes {
		t.Errorf("reason length = %d runes, want it bounded at %d", len([]rune(reason)), maxRedteamArtifactTextRunes)
	}
	if !strings.Contains(reason, "500") {
		t.Errorf("reason = %q, want the status code kept — it is what makes the failure diagnosable", reason)
	}
}
