package billing

import (
	"testing"
)

// ─── constants sanity ─────────────────────────────────────────────────────────

func TestConstants_SanityCheck(t *testing.T) {
	if MinAssessmentBalance <= 0 {
		t.Error("MinAssessmentBalance must be positive")
	}
	if TokenCostPer1K <= 0 {
		t.Error("TokenCostPer1K must be positive")
	}
}
