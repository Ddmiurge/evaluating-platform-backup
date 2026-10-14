package maclaw

// redteam_generation_path_test.go — P2-6 · the field that keeps two incomparable
// reports from looking identical.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeRedteamGenerationPath(t *testing.T) {
	for _, ok := range []string{GenerationPathPlanA, GenerationPathPlanC, " plan_c "} {
		if got, err := NormalizeRedteamGenerationPath(ok); err != nil || strings.TrimSpace(ok) != got {
			t.Errorf("NormalizeRedteamGenerationPath(%q) = %q, %v", ok, got, err)
		}
	}
	// Empty means "not recorded" — a pre-P2-6 report. It must be allowed, because
	// inventing a value for it would publish a guess as a fact.
	if got, err := NormalizeRedteamGenerationPath("   "); err != nil || got != "" {
		t.Errorf("blank = %q, %v; want empty and no error", got, err)
	}
	// Anything else is refused on the write path: `plan_c_v2`, a typo, or free text
	// would all end up under a heading that claims to explain the numbers.
	for _, bad := range []string{"plan_b", "planc", "Plan C", "plan_c:beta", "engine"} {
		if _, err := NormalizeRedteamGenerationPath(bad); err == nil {
			t.Errorf("NormalizeRedteamGenerationPath(%q) accepted an uninterpretable value", bad)
		}
	}
}

func TestValidateReportGenerationMetadata(t *testing.T) {
	cases := []struct {
		name    string
		meta    map[string]string
		wantErr bool
	}{
		{"empty metadata is fine", nil, false},
		{"path alone is fine", map[string]string{RedteamGenerationPathMetadataKey: GenerationPathPlanC}, false},
		{
			"path with count is fine",
			map[string]string{
				RedteamGenerationPathMetadataKey:    GenerationPathPlanC,
				RedteamNumTestsEffectiveMetadataKey: "50",
			},
			false,
		},
		{"unknown path is refused", map[string]string{RedteamGenerationPathMetadataKey: "plan_x"}, true},
		{
			"non-numeric count is refused",
			map[string]string{
				RedteamGenerationPathMetadataKey:    GenerationPathPlanA,
				RedteamNumTestsEffectiveMetadataKey: "many",
			},
			true,
		},
		{
			"zero count is refused",
			map[string]string{
				RedteamGenerationPathMetadataKey:    GenerationPathPlanA,
				RedteamNumTestsEffectiveMetadataKey: "0",
			},
			true,
		},
		{
			// A bare number would be read as "probes" with no way to know which path
			// configured it.
			"count without a path is refused",
			map[string]string{RedteamNumTestsEffectiveMetadataKey: "50"},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReportGenerationMetadata(tc.meta)
			if tc.wantErr && err == nil {
				t.Fatal("expected a refusal")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

func TestRedteamGenerationPathCaption(t *testing.T) {
	t.Run("plan_c names the path, the probe count and the incomparability", func(t *testing.T) {
		caption := RedteamGenerationPathCaption(map[string]string{
			RedteamGenerationPathMetadataKey:    GenerationPathPlanC,
			RedteamNumTestsEffectiveMetadataKey: "50",
		})
		for _, want := range []string{"plan_c", "50", "不可直接比较"} {
			if !strings.Contains(caption, want) {
				t.Errorf("caption %q does not mention %q", caption, want)
			}
		}
	})

	t.Run("plan_a is labelled too, not left blank", func(t *testing.T) {
		// R-5-c: the field must have a value on BOTH paths. A field that is only
		// populated on one path is the easiest shape to miss, because the reports
		// that lack it look fine.
		caption := RedteamGenerationPathCaption(map[string]string{
			RedteamGenerationPathMetadataKey: GenerationPathPlanA,
		})
		if !strings.Contains(caption, "plan_a") {
			t.Errorf("caption %q does not name the path", caption)
		}
		if strings.Contains(caption, "不可直接比较") {
			t.Errorf("the plan_a caption should not carry the plan_c comparison note: %q", caption)
		}
	})

	t.Run("unrecorded says so and does not pick a path", func(t *testing.T) {
		// Old reports. The read path must not fail (the report has to open) and must
		// not guess (either choice would be a false statement about the numbers).
		for _, meta := range []map[string]string{nil, {}, {RedteamGenerationPathMetadataKey: ""}} {
			caption := RedteamGenerationPathCaption(meta)
			if !strings.Contains(caption, "未记录") {
				t.Errorf("caption %q does not say the path is unrecorded", caption)
			}
			if strings.Contains(caption, "plan_a") || strings.Contains(caption, "plan_c") {
				t.Errorf("caption %q guessed a path", caption)
			}
		}
	})

	t.Run("invalid metadata says so rather than presenting a path", func(t *testing.T) {
		caption := RedteamGenerationPathCaption(map[string]string{RedteamGenerationPathMetadataKey: "plan_b"})
		if !strings.Contains(caption, "非法") {
			t.Errorf("caption %q does not flag the corrupt value", caption)
		}
	})
}

func TestEngineReportMetadataCarriesGenerationPath(t *testing.T) {
	record := &EngineRunRecord{ID: "run-1", Purpose: "p"}

	t.Run("both keys are written when the engine sent them", func(t *testing.T) {
		metadata := engineReportMetadata(record, &EngineSafeResult{
			GenerationPath:    GenerationPathPlanC,
			NumTestsEffective: 50,
		})
		if metadata[RedteamGenerationPathMetadataKey] != GenerationPathPlanC {
			t.Errorf("generation_path = %q, want plan_c", metadata[RedteamGenerationPathMetadataKey])
		}
		if metadata[RedteamNumTestsEffectiveMetadataKey] != "50" {
			t.Errorf("num_tests_effective = %q, want 50", metadata[RedteamNumTestsEffectiveMetadataKey])
		}
	})

	t.Run("an engine that sends nothing leaves both keys absent", func(t *testing.T) {
		// Absence is honest ("not recorded"); a default would be a guess published
		// under a heading that claims to explain the numbers.
		metadata := engineReportMetadata(record, &EngineSafeResult{})
		if _, ok := metadata[RedteamGenerationPathMetadataKey]; ok {
			t.Errorf("generation_path was defaulted: %q", metadata[RedteamGenerationPathMetadataKey])
		}
		if _, ok := metadata[RedteamNumTestsEffectiveMetadataKey]; ok {
			t.Errorf("num_tests_effective was defaulted: %q", metadata[RedteamNumTestsEffectiveMetadataKey])
		}
	})

	t.Run("an uninterpretable path from the engine is dropped, not stored", func(t *testing.T) {
		// The write path is fail-closed, so storing it would refuse the whole report
		// later. Dropping it degrades to "not recorded", which is accurate.
		metadata := engineReportMetadata(record, &EngineSafeResult{
			GenerationPath:    "plan_b",
			NumTestsEffective: 50,
		})
		if got := metadata[RedteamGenerationPathMetadataKey]; got != "" {
			t.Errorf("generation_path = %q, want it absent", got)
		}
		if err := ValidateReportGenerationMetadata(metadata); err != nil {
			t.Errorf("the resulting metadata must still be storable: %v", err)
		}
	})
}

// TestGenerationPathCaptionMatchesFrontendMirror pins the cross-language contract.
//
// The report card (frontend `chatDisplay.ts`) and the PDF (`RedteamGenerationPathCaption`)
// are two renderings of ONE statement. The sibling caption for judge_track pins its
// literals in Go and warns in a comment that the frontend must match; that warning is
// only as reliable as the next reader. This reads the frontend source and asserts
// every fragment verbatim, so a one-sided edit fails the backend suite instead of
// shipping two different explanations of the same number.
//
// Counter-proof: change a character on either side → red.
func TestGenerationPathCaptionMatchesFrontendMirror(t *testing.T) {
	raw, err := os.ReadFile(frontendChatDisplayPath(t))
	if err != nil {
		t.Fatalf("read frontend chatDisplay.ts: %v", err)
	}
	source := string(raw)

	// The fragments the frontend must carry, exactly as this side emits them.
	fragments := []string{
		"生成路径：平台合成用例（plan_a）",
		"生成路径：promptfoo 原生生成（plan_c）",
		"与平台合成用例（plan_a）的报告不可直接比较（探针数量与判定口径均不同）。",
		"生成路径：未记录（本报告产生于记录生成路径之前，或由其它路径生成）。",
		"生成路径：报告元数据中的生成路径字段非法，已忽略；请联系报告生成方核对。",
		"·实际配置探针数 ",
	}
	for _, fragment := range fragments {
		if !strings.Contains(source, fragment) {
			t.Errorf("frontend chatDisplay.ts is missing the fragment %q; the report card and the PDF would explain the same run differently", fragment)
		}
	}

	// And this side must actually emit them, so the assertion above is not merely
	// checking that both files contain the same dead string.
	for _, probe := range []map[string]string{
		{RedteamGenerationPathMetadataKey: GenerationPathPlanA},
		{RedteamGenerationPathMetadataKey: GenerationPathPlanC, RedteamNumTestsEffectiveMetadataKey: "7"},
		{},
		{RedteamGenerationPathMetadataKey: "plan_b"},
	} {
		caption := RedteamGenerationPathCaption(probe)
		matched := false
		for _, fragment := range fragments {
			if strings.Contains(caption, fragment) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("Go caption %q matches no pinned fragment", caption)
		}
	}
}

// frontendChatDisplayPath resolves the frontend source relative to this test file.
func frontendChatDisplayPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed to resolve the test file path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "frontend", "src", "pages", "enterprise", "chatDisplay.ts"))
}
