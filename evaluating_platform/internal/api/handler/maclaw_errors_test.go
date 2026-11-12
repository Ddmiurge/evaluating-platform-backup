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

// P28 (record 54 §6.2): an embedded package that fails the gates used to fall
// through to the generic 503 「maclaw runtime is unavailable」 even though the
// state row recorded `rejected` with an actionable reason. The package is the
// submitter's input; only the probe codes are genuine runtime unavailability.
func TestClassifyMaclawErrorEmbeddedValidation(t *testing.T) {
	status, code, message := classifyMaclawError(&maclaw.EmbeddedPluginValidationError{
		Code:    "embedded_entry_contract",
		Message: "manifest.entry 必须默认导出 { action } 函数。",
	})
	if status != http.StatusBadRequest || code != "embedded_entry_contract" {
		t.Errorf("status/code = %d/%q, want 400/embedded_entry_contract", status, code)
	}
	if message == "" {
		t.Error("message is empty — the validator's actionable reason must survive")
	}
	// Wrapped (the store adds context) must classify the same way.
	wrapped := fmt.Errorf("save resource: %w", &maclaw.EmbeddedPluginValidationError{
		Code: "embedded_tests_failed", Message: "包内测试未通过。",
	})
	if status, _, _ := classifyMaclawError(wrapped); status != http.StatusBadRequest {
		t.Errorf("wrapped status = %d, want 400", status)
	}

	// Runtime unavailability keeps its 503 — telling an expert to fix a package
	// whose tests never ran would be the opposite mistake.
	for _, code := range []string{"embedded_probe_unavailable", "embedded_probe_unreachable"} {
		status, got, _ := classifyMaclawError(&maclaw.EmbeddedPluginValidationError{Code: code, Message: "引擎不可达"})
		if status != http.StatusServiceUnavailable || got != code {
			t.Errorf("%s: status/code = %d/%q, want 503/%s", code, status, got, code)
		}
	}
}

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
