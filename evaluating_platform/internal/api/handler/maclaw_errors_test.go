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

// The "capability is off in this deployment" answer must match the engine's.
//
// Record 42 §4.3: the engine returned 409 `code_strategy_disabled` while the BFF
// returned 400 for the same code. Same fact, two statuses — a submitter cannot
// tell which one to act on.
func TestClassifyMaclawErrorDisabledCodeStrategyIs409(t *testing.T) {
	status, code, _ := classifyMaclawError(&maclaw.PromptfooCustomPluginValidationError{
		Code:    maclaw.CustomStrategyErrDisabled,
		Message: "本部署未开放策略代码包。",
	})
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409 (the engine answers 409 for the same code)", status)
	}
	if code != maclaw.CustomStrategyErrDisabled {
		t.Errorf("code = %q, want %q", code, maclaw.CustomStrategyErrDisabled)
	}

	// Every other validation failure is still the package's fault → 400.
	status, _, _ = classifyMaclawError(&maclaw.PromptfooCustomPluginValidationError{
		Code:    maclaw.CustomStrategyErrCodeUnsafe,
		Message: "命中禁止项 process.env",
	})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d for an unsafe package, want 400", status)
	}
}
