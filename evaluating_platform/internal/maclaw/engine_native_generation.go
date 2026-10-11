package maclaw

import "strings"

// engine_native_generation.go — U7 · assemble the inputs promptfoo's own redteam
// generation needs, from data the platform already holds.
//
// # What this adds and why
//
// Plan A asked the generation LLM for "N attack prompts covering these plugin
// categories". That made attacks generic: the generator knew the attack family
// but nothing about the system being attacked. promptfoo's native generation
// instead asks a much richer question, and to answer it well it needs to know
// what the application *is* — what it can reach, what it must never reach, who
// uses it, and what its security obligations are.
//
// `applicationDefinition` is promptfoo's own name for that input. Per `14` §4.2
// the platform was to supply its six sub-fields; they arrive here from the
// enterprise target configuration's free-form metadata, so an operator fills them
// in one place and every engine run benefits.
//
// # A discrepancy worth recording
//
// promptfoo's *library* entrypoint (`redteam.run`, the one this platform calls)
// does not read an `applicationDefinition` key: it appears only in the CLI
// (`dist/src/main.js`) and `RedteamConfigSchema` does not declare it, so passing
// it through the config document would have zod silently drop it. The engine
// therefore renders these fields into promptfoo's `purpose` string, which is the
// channel the library does honour.
//
// The JSON tags below are snake_case because that is the engine's wire
// convention throughout (`base_url`, `platform_user_id`, `category_label`); the
// engine's TypeScript `ApplicationDefinition` mirrors them exactly. A camelCase
// spelling here would deserialize as `undefined` on the other side rather than
// fail loudly, so the two declarations must be edited together.

// NativeGenerationMetadataKeys are the target-config metadata keys an operator
// can set to enrich native generation. All optional; an absent key simply
// contributes nothing.
const (
	NativeGenMetaPurpose              = "redteam_purpose"
	NativeGenMetaFeatures             = "redteam_features"
	NativeGenMetaHasAccessTo          = "redteam_has_access_to"
	NativeGenMetaDoesNotHaveAccessTo  = "redteam_does_not_have_access_to"
	NativeGenMetaUserTypes            = "redteam_user_types"
	NativeGenMetaSecurityRequirements = "redteam_security_requirements"
	// NativeGenMetaTestGenerationInstructions carries free-form extra
	// constraints applied to every plugin's generation call.
	NativeGenMetaTestGenerationInstructions = "redteam_test_generation_instructions"
	// NativeGenMetaInjectVar overrides the prompt variable promptfoo injects
	// generated payloads into.
	NativeGenMetaInjectVar = "redteam_inject_var"
)

// EngineDefaultInjectVar matches the engine's single-variable prompt template.
const EngineDefaultInjectVar = "prompt"

// maxNativeGenFieldLen bounds each free-form field. These strings are handed to
// an LLM as generation context; an unbounded operator paste would inflate token
// cost and let a single field dominate the purpose.
const maxNativeGenFieldLen = 2000

// BuildEngineApplicationDefinition derives the six application-description
// fields from a target config's metadata.
//
// Returns nil when the operator supplied none of them, which keeps the wire
// payload byte-identical to pre-U7 for every existing target — no empty object
// on the wire, nothing for the engine to render.
func BuildEngineApplicationDefinition(metadata map[string]string) *EngineApplicationDefinition {
	if len(metadata) == 0 {
		return nil
	}
	def := EngineApplicationDefinition{
		Purpose:              nativeGenField(metadata, NativeGenMetaPurpose),
		Features:             nativeGenField(metadata, NativeGenMetaFeatures),
		HasAccessTo:          nativeGenField(metadata, NativeGenMetaHasAccessTo),
		DoesNotHaveAccessTo:  nativeGenField(metadata, NativeGenMetaDoesNotHaveAccessTo),
		UserTypes:            nativeGenField(metadata, NativeGenMetaUserTypes),
		SecurityRequirements: nativeGenField(metadata, NativeGenMetaSecurityRequirements),
	}
	if def == (EngineApplicationDefinition{}) {
		return nil
	}
	return &def
}

// IsEmpty reports whether no field carries content.
func (d EngineApplicationDefinition) IsEmpty() bool {
	return d == EngineApplicationDefinition{}
}

// nativeGenField reads, trims and length-bounds one metadata value.
func nativeGenField(metadata map[string]string, key string) string {
	return truncateRunes(strings.TrimSpace(metadata[key]), maxNativeGenFieldLen)
}

// truncateRunes bounds a string by runes (not bytes) so a multi-byte operator
// paste cannot be cut mid-character and produce invalid UTF-8 on the wire.
func truncateRunes(s string, limit int) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// ResolveEngineNativeGenerationInputs fills the three U7 fields on a run request
// from the target config metadata. Mutates and returns req for chaining.
//
// Kept separate from credential materialization on purpose: this carries no
// secrets, so it is safe to log at debug level, whereas the credentials that sit
// beside it in the same struct are not.
func ResolveEngineNativeGenerationInputs(req *EngineRunRequest, targetMetadata map[string]string) *EngineRunRequest {
	if req == nil {
		return req
	}
	req.ApplicationDefinition = BuildEngineApplicationDefinition(targetMetadata)
	req.TestGenerationInstructions = nativeGenField(targetMetadata, NativeGenMetaTestGenerationInstructions)
	req.InjectVar = nativeGenField(targetMetadata, NativeGenMetaInjectVar)
	return req
}

// NativeGenerationOverrides carries the per-request form of the same three
// inputs, as supplied by a one-off run request. A nil field means "not supplied,
// keep the target-level value".
//
// Note the two halves differ on purpose, because they answer different questions:
// TestGenerationInstructions and InjectVar *replace* (one instruction string,
// one variable name — there is nothing to merge), while ApplicationDefinition
// *merges field by field* (six independent descriptions an operator may want to
// override one at a time). A blank sub-field of the definition therefore means
// "inherit the target-level value", so an all-blank definition object is a
// no-op rather than a way to erase the target's description.
type NativeGenerationOverrides struct {
	ApplicationDefinition      *EngineApplicationDefinition
	TestGenerationInstructions *string
	InjectVar                  *string
}

// ApplyNativeGenerationOverrides layers per-request values on top of whatever
// ResolveEngineNativeGenerationInputs already put on the request.
//
// Field-level merge for the definition, not whole-object replacement: an operator
// who set `redteam_features` on the target but overrides only `userTypes` for one
// run should keep the features.
func ApplyNativeGenerationOverrides(req *EngineRunRequest, ov *NativeGenerationOverrides) *EngineRunRequest {
	if req == nil || ov == nil {
		return req
	}
	if ov.ApplicationDefinition != nil {
		merged := *ov.ApplicationDefinition
		if base := req.ApplicationDefinition; base != nil {
			if merged.Purpose == "" {
				merged.Purpose = base.Purpose
			}
			if merged.Features == "" {
				merged.Features = base.Features
			}
			if merged.HasAccessTo == "" {
				merged.HasAccessTo = base.HasAccessTo
			}
			if merged.DoesNotHaveAccessTo == "" {
				merged.DoesNotHaveAccessTo = base.DoesNotHaveAccessTo
			}
			if merged.UserTypes == "" {
				merged.UserTypes = base.UserTypes
			}
			if merged.SecurityRequirements == "" {
				merged.SecurityRequirements = base.SecurityRequirements
			}
		}
		merged.Purpose = truncateRunes(strings.TrimSpace(merged.Purpose), maxNativeGenFieldLen)
		merged.Features = truncateRunes(strings.TrimSpace(merged.Features), maxNativeGenFieldLen)
		merged.HasAccessTo = truncateRunes(strings.TrimSpace(merged.HasAccessTo), maxNativeGenFieldLen)
		merged.DoesNotHaveAccessTo = truncateRunes(strings.TrimSpace(merged.DoesNotHaveAccessTo), maxNativeGenFieldLen)
		merged.UserTypes = truncateRunes(strings.TrimSpace(merged.UserTypes), maxNativeGenFieldLen)
		merged.SecurityRequirements = truncateRunes(strings.TrimSpace(merged.SecurityRequirements), maxNativeGenFieldLen)
		if merged.IsEmpty() {
			req.ApplicationDefinition = nil
		} else {
			req.ApplicationDefinition = &merged
		}
	}
	if ov.TestGenerationInstructions != nil {
		req.TestGenerationInstructions = truncateRunes(strings.TrimSpace(*ov.TestGenerationInstructions), maxNativeGenFieldLen)
	}
	if ov.InjectVar != nil {
		req.InjectVar = truncateRunes(strings.TrimSpace(*ov.InjectVar), maxNativeGenFieldLen)
	}
	return req
}
