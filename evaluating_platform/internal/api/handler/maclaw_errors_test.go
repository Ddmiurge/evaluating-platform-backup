package handler

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"evaluating_platform/internal/maclaw"
)

// A write naming an id the caller does not own must not read like a runtime
// outage.
//
// Measured in the P10 verification: the ownership check refused the write
// correctly, but the response was `{"code":"maclaw_unavailable","error":"maclaw
// runtime is unavailable"}` — an expert would go looking for a broken
// deployment instead of their own input. 403 + a specific code says what
// happened.
func TestClassifyMaclawErrorResourceNotOwned(t *testing.T) {
	status, code, message := classifyMaclawError(maclaw.ErrResourceNotOwned)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", status)
	}
	if code != "resource_not_owned" {
		t.Errorf("code = %q, want resource_not_owned", code)
	}
	if message == "" {
		t.Error("message is empty")
	}
	// Wrapped errors must still classify: callers add context.
	wrapped := fmt.Errorf("save resource: %w", maclaw.ErrResourceNotOwned)
	if status, _, _ := classifyMaclawError(wrapped); status != http.StatusForbidden {
		t.Errorf("wrapped status = %d, want 403", status)
	}
}

// The fallback for an unclassified error is 503 / maclaw_unavailable — this is
// literally the branch the ownership error used to fall into, and the reason the
// P10 report described the refusal as a runtime outage.
func TestClassifyMaclawErrorUnknownFallsBackToUnavailable(t *testing.T) {
	status, code, _ := classifyMaclawError(errors.New("boom"))
	if status != http.StatusServiceUnavailable || code != "maclaw_unavailable" {
		t.Errorf("status/code = %d/%q, want 503/maclaw_unavailable", status, code)
	}
}
