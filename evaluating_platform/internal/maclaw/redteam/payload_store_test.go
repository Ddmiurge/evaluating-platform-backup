package redteam

import "testing"

// The platform's own report metadata keys must be REGISTERED, not lucky.
//
// `isUnsafeMetadataKey` is a key-SUBSTRING blacklist, and three platform keys
// have already been stripped by it: `generation_path` (hit "path", which
// produced a report the platform refused to write), `num_tests_effective`, and
// `prompt_redacted_by_engine` (hit "prompt", which silently removed the
// 「编码前原文」 note from every report).
//
// `target_answer_missing` and `target_call_error` avoid every blacklisted
// substring today — which is exactly why a behavioural test cannot guard them:
// deleting their registration leaves such a test green, and the next rename (or
// a new blacklist entry) would drop them without a red test anywhere. So this
// asserts the registration itself.
//
// The literals are duplicated on purpose: this package cannot import maclaw
// (maclaw depends on redteam). The maclaw-side tests write these keys through
// the real constants (`RedteamTargetAnswerMissingMetadataKey` /
// `RedteamTargetCallErrorMetadataKey`), so the two ends cannot drift apart
// silently — a rename there turns up as a dropped key there.
func TestPlatformReportMetadataKeysAreRegistered(t *testing.T) {
	for _, key := range []string{
		"generation_path",
		"num_tests_effective",
		"plugin_stats_json",
		"strategy_stats_json",
		"prompt_redacted_by_engine",
		"target_answer_missing",
		"target_call_error",
	} {
		if !isSafePayloadMetadataKey(key) {
			t.Errorf("%s is not registered in isSafePayloadMetadataKey — the report cannot read it", key)
			continue
		}
		if out := SanitizeMetadata(map[string]string{key: "v"}); out[key] != "v" {
			t.Errorf("SanitizeMetadata dropped the registered key %s", key)
		}
	}
}
