package maclaw

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// U7 · the three inputs promptfoo's own generation needs and Plan A never used.
//
// These tests pin behaviour, not just presence: the wire shape has to stay
// byte-identical for pre-U7 targets (no empty object on the wire), the
// operator-supplied text has to be bounded, and the per-request override has to
// merge field-by-field rather than replacing the whole definition.

func TestBuildEngineApplicationDefinition(t *testing.T) {
	t.Run("nil when no metadata", func(t *testing.T) {
		if got := BuildEngineApplicationDefinition(nil); got != nil {
			t.Fatalf("expected nil for nil metadata, got %+v", got)
		}
		if got := BuildEngineApplicationDefinition(map[string]string{"unrelated": "x"}); got != nil {
			t.Fatalf("expected nil when no redteam_* keys are present, got %+v", got)
		}
	})

	t.Run("nil when every redteam key is blank", func(t *testing.T) {
		meta := map[string]string{
			NativeGenMetaPurpose:   "   ",
			NativeGenMetaUserTypes: "",
		}
		if got := BuildEngineApplicationDefinition(meta); got != nil {
			t.Fatalf("expected nil for all-blank values, got %+v", got)
		}
	})

	t.Run("maps all six fields and trims them", func(t *testing.T) {
		meta := map[string]string{
			NativeGenMetaPurpose:              "  retail banking assistant  ",
			NativeGenMetaFeatures:             " balance, transfers ",
			NativeGenMetaHasAccessTo:          " customer balances ",
			NativeGenMetaDoesNotHaveAccessTo:  " the ledgers database ",
			NativeGenMetaUserTypes:            " retail customers ",
			NativeGenMetaSecurityRequirements: " PCI scope ",
		}
		got := BuildEngineApplicationDefinition(meta)
		if got == nil {
			t.Fatal("expected a definition")
		}
		if got.Purpose != "retail banking assistant" {
			t.Errorf("Purpose = %q, want trimmed", got.Purpose)
		}
		if got.Features != "balance, transfers" {
			t.Errorf("Features = %q", got.Features)
		}
		if got.HasAccessTo != "customer balances" {
			t.Errorf("HasAccessTo = %q", got.HasAccessTo)
		}
		if got.DoesNotHaveAccessTo != "the ledgers database" {
			t.Errorf("DoesNotHaveAccessTo = %q", got.DoesNotHaveAccessTo)
		}
		if got.UserTypes != "retail customers" {
			t.Errorf("UserTypes = %q", got.UserTypes)
		}
		if got.SecurityRequirements != "PCI scope" {
			t.Errorf("SecurityRequirements = %q", got.SecurityRequirements)
		}
	})

	// An operator paste is operator input; it must not be able to inflate token
	// cost without limit or dominate the purpose.
	t.Run("bounds each field", func(t *testing.T) {
		huge := strings.Repeat("界", maxNativeGenFieldLen+500)
		got := BuildEngineApplicationDefinition(map[string]string{NativeGenMetaFeatures: huge})
		if got == nil {
			t.Fatal("expected a definition")
		}
		if n := len([]rune(got.Features)); n != maxNativeGenFieldLen {
			t.Fatalf("Features length = %d runes, want %d", n, maxNativeGenFieldLen)
		}
		if !isValidUTF8(got.Features) {
			t.Error("truncation produced invalid UTF-8")
		}
	})
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

func TestResolveEngineNativeGenerationInputs(t *testing.T) {
	t.Run("nil request is a no-op", func(t *testing.T) {
		if got := ResolveEngineNativeGenerationInputs(nil, map[string]string{NativeGenMetaPurpose: "p"}); got != nil {
			t.Fatal("expected nil")
		}
	})

	t.Run("populates all three U7 fields from metadata", func(t *testing.T) {
		req := ResolveEngineNativeGenerationInputs(&EngineRunRequest{}, map[string]string{
			NativeGenMetaPurpose:                    "bank chatbot",
			NativeGenMetaUserTypes:                  "customers",
			NativeGenMetaTestGenerationInstructions: "只用中文",
			NativeGenMetaInjectVar:                  "user_input",
		})
		if req.ApplicationDefinition == nil || req.ApplicationDefinition.Purpose != "bank chatbot" {
			t.Fatalf("ApplicationDefinition = %+v", req.ApplicationDefinition)
		}
		if req.ApplicationDefinition.UserTypes != "customers" {
			t.Errorf("UserTypes = %q", req.ApplicationDefinition.UserTypes)
		}
		if req.TestGenerationInstructions != "只用中文" {
			t.Errorf("TestGenerationInstructions = %q", req.TestGenerationInstructions)
		}
		if req.InjectVar != "user_input" {
			t.Errorf("InjectVar = %q", req.InjectVar)
		}
	})

	// Backward compatibility: a pre-U7 target has none of this metadata, and its
	// request body must be byte-identical to what U6 produced.
	t.Run("omits every U7 field for a pre-U7 target", func(t *testing.T) {
		req := ResolveEngineNativeGenerationInputs(&EngineRunRequest{}, map[string]string{"health_url": "https://x"})
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		body := string(raw)
		for _, key := range []string{
			"application_definition",
			"test_generation_instructions",
			"inject_var",
		} {
			if strings.Contains(body, key) {
				t.Errorf("pre-U7 payload should omit %s, got %s", key, body)
			}
		}
	})
}

func TestApplyNativeGenerationOverrides(t *testing.T) {
	base := func() *EngineRunRequest {
		return ResolveEngineNativeGenerationInputs(&EngineRunRequest{}, map[string]string{
			NativeGenMetaPurpose:                    "target-level purpose",
			NativeGenMetaFeatures:                   "target-level features",
			NativeGenMetaUserTypes:                  "target-level users",
			NativeGenMetaInjectVar:                  "target_var",
			NativeGenMetaTestGenerationInstructions: "target-level instructions",
		})
	}

	t.Run("nil overrides leave the request untouched", func(t *testing.T) {
		req := base()
		before := *req.ApplicationDefinition
		ApplyNativeGenerationOverrides(req, nil)
		if *req.ApplicationDefinition != before {
			t.Error("nil overrides must not change the definition")
		}
		if req.InjectVar != "target_var" {
			t.Errorf("InjectVar = %q, want unchanged", req.InjectVar)
		}
	})

	// Field-level merge: overriding one field must not wipe the others the
	// operator set on the target.
	t.Run("merges field by field", func(t *testing.T) {
		req := base()
		ApplyNativeGenerationOverrides(req, &NativeGenerationOverrides{
			ApplicationDefinition: &EngineApplicationDefinition{UserTypes: "per-run users"},
		})
		def := req.ApplicationDefinition
		if def == nil {
			t.Fatal("expected a definition")
		}
		if def.UserTypes != "per-run users" {
			t.Errorf("UserTypes = %q, want the override", def.UserTypes)
		}
		if def.Purpose != "target-level purpose" {
			t.Errorf("Purpose = %q, want the target-level value to survive", def.Purpose)
		}
		if def.Features != "target-level features" {
			t.Errorf("Features = %q, want the target-level value to survive", def.Features)
		}
	})

	t.Run("overrides instructions and inject var", func(t *testing.T) {
		req := base()
		instr := "per-run instructions"
		iv := "per_run_var"
		ApplyNativeGenerationOverrides(req, &NativeGenerationOverrides{
			TestGenerationInstructions: &instr,
			InjectVar:                  &iv,
		})
		if req.TestGenerationInstructions != instr {
			t.Errorf("TestGenerationInstructions = %q", req.TestGenerationInstructions)
		}
		if req.InjectVar != iv {
			t.Errorf("InjectVar = %q", req.InjectVar)
		}
	})

	// Blank means "inherit", so an all-blank definition object is a no-op — not
	// a way to silently erase the operator's target-level description.
	t.Run("an all-blank override inherits every field", func(t *testing.T) {
		req := base()
		ApplyNativeGenerationOverrides(req, &NativeGenerationOverrides{
			ApplicationDefinition: &EngineApplicationDefinition{},
		})
		def := req.ApplicationDefinition
		if def == nil {
			t.Fatal("expected the target-level definition to survive")
		}
		if def.Purpose != "target-level purpose" || def.Features != "target-level features" || def.UserTypes != "target-level users" {
			t.Errorf("definition = %+v, want all target-level values preserved", def)
		}
	})

	// An override on a request that never had target-level metadata stands alone.
	t.Run("an override with no target-level base is used verbatim", func(t *testing.T) {
		req := ResolveEngineNativeGenerationInputs(&EngineRunRequest{}, nil)
		ApplyNativeGenerationOverrides(req, &NativeGenerationOverrides{
			ApplicationDefinition: &EngineApplicationDefinition{Purpose: "only-this"},
		})
		if req.ApplicationDefinition == nil || req.ApplicationDefinition.Purpose != "only-this" {
			t.Fatalf("ApplicationDefinition = %+v", req.ApplicationDefinition)
		}
	})

	t.Run("bounds overridden values", func(t *testing.T) {
		req := base()
		huge := strings.Repeat("x", maxNativeGenFieldLen+100)
		ApplyNativeGenerationOverrides(req, &NativeGenerationOverrides{
			TestGenerationInstructions: &huge,
		})
		if n := len([]rune(req.TestGenerationInstructions)); n != maxNativeGenFieldLen {
			t.Fatalf("length = %d, want %d", n, maxNativeGenFieldLen)
		}
	})
}

func TestEngineApplicationDefinitionIsEmpty(t *testing.T) {
	if !(EngineApplicationDefinition{}).IsEmpty() {
		t.Error("zero value must be empty")
	}
	if (EngineApplicationDefinition{Purpose: "x"}).IsEmpty() {
		t.Error("a definition with content must not be empty")
	}
}

// The engine's default inject var must match the single-variable prompt template
// it ships, or every strategy transform would write to a variable no template
// renders.
func TestEngineDefaultInjectVarMatchesEnginePromptVariable(t *testing.T) {
	if EngineDefaultInjectVar != "prompt" {
		t.Fatalf("EngineDefaultInjectVar = %q, want %q", EngineDefaultInjectVar, "prompt")
	}
}

// TestEngineNativeGenerationWireContractIsSnakeCase pins the BFF→engine field
// names for the three U7 inputs.
//
// This exists because a camelCase spelling on one side and snake_case on the
// other does not error — it deserializes as `undefined`, so the application
// description silently never reaches the generator and attacks stay generic.
// That is the exact failure U7 exists to remove, reintroduced through a typo.
// The engine's TypeScript `ApplicationDefinition` (promptfoo_engine/src/types.ts)
// must list the same six keys.
func TestEngineNativeGenerationWireContractIsSnakeCase(t *testing.T) {
	raw, err := json.Marshal(EngineRunRequest{
		ApplicationDefinition: &EngineApplicationDefinition{
			Purpose:              "p",
			Features:             "f",
			HasAccessTo:          "a",
			DoesNotHaveAccessTo:  "n",
			UserTypes:            "u",
			SecurityRequirements: "s",
		},
		TestGenerationInstructions: "t",
		InjectVar:                  "v",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	def, ok := body["application_definition"].(map[string]any)
	if !ok {
		t.Fatalf("application_definition missing or not an object: %s", raw)
	}
	for _, key := range []string{
		"purpose",
		"features",
		"has_access_to",
		"does_not_have_access_to",
		"user_types",
		"security_requirements",
	} {
		if _, present := def[key]; !present {
			t.Errorf("application_definition is missing %q; got keys %v", key, keysOf(def))
		}
	}
	if _, ok := body["test_generation_instructions"]; !ok {
		t.Error("test_generation_instructions missing from the wire contract")
	}
	if _, ok := body["inject_var"]; !ok {
		t.Error("inject_var missing from the wire contract")
	}
	// And the camelCase spellings must NOT appear, or one side will drift back.
	for _, key := range []string{"hasAccessTo", "userTypes", "securityRequirements"} {
		if _, present := def[key]; present {
			t.Errorf("application_definition must not use camelCase key %q", key)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
