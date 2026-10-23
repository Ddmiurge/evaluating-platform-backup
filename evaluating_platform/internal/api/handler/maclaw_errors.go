package handler

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"

	"evaluating_platform/internal/maclaw"
)

func writeMaclawError(c *gin.Context, err error) {
	status, code, message := classifyMaclawError(err)
	c.JSON(status, gin.H{"error": message, "code": code})
}

func classifyMaclawError(err error) (int, string, string) {
	if err == nil {
		return http.StatusBadGateway, "maclaw_proxy_error", "maclaw request failed"
	}
	if errors.Is(err, maclaw.ErrNotConfigured) {
		return http.StatusServiceUnavailable, "maclaw_not_configured", "maclaw runtime is not configured"
	}
	// A write naming an id that is not the caller's. Classified before the
	// upstream fallbacks because otherwise it surfaced as
	// `maclaw_unavailable` / 503 and read like a runtime outage (measured in the
	// P10 verification). 403 rather than 404: the caller did name a specific id,
	// and "not yours" is the actionable fact.
	if errors.Is(err, maclaw.ErrResourceNotOwned) {
		return http.StatusForbidden, "resource_not_owned", "资源不存在或不属于当前账号，无法更新。"
	}

	// U10: a rejected expert plugin package is the submitter's input being
	// wrong, not an upstream failure. Classified before the generic fallbacks
	// so it surfaces as 400 with the validator's own message — the DoD requires
	// "JS 形态被拒绝并有清晰提示（不是 500 错误）", and the generic branch would
	// have turned it into a 503 with "maclaw runtime is not configured".
	var validation *maclaw.PromptfooCustomPluginValidationError
	if errors.As(err, &validation) {
		return http.StatusBadRequest, validation.Code, validation.Error()
	}
	// U10: a `pf_plugin:` reference that cannot be resolved (admin disabled it,
	// it was archived, or its content is gone) is likewise the caller's problem
	// to act on, and the message explains which.
	var refErr *maclaw.CustomPluginRefError
	if errors.As(err, &refErr) {
		return http.StatusConflict, refErr.Code, refErr.Message
	}
	// U6's remote-dependency guard surfaces through the same path when a run
	// names a gated capability. Kept as its own case so the status reflects
	// "this run cannot proceed as specified" rather than a bad-request on input.
	var remoteDep *maclaw.EngineRemoteDepGuardError
	if errors.As(err, &remoteDep) {
		return http.StatusConflict, remoteDep.Code, remoteDep.Error()
	}

	var upstream *maclaw.UpstreamError
	if errors.As(err, &upstream) {
		switch upstream.StatusCode {
		case http.StatusBadRequest:
			return http.StatusBadRequest, "maclaw_bad_request", "maclaw rejected the request"
		case http.StatusUnauthorized, http.StatusForbidden:
			return http.StatusBadGateway, "maclaw_auth_failed", "maclaw authentication failed"
		case http.StatusNotFound:
			return http.StatusNotFound, "maclaw_not_found", "requested maclaw resource was not found"
		case http.StatusConflict:
			return http.StatusConflict, "maclaw_conflict", "maclaw resource conflict"
		case http.StatusUnprocessableEntity:
			return http.StatusUnprocessableEntity, "maclaw_validation_failed", "maclaw validation failed"
		case http.StatusTooManyRequests:
			return http.StatusTooManyRequests, "maclaw_rate_limited", "maclaw rate limit exceeded"
		case http.StatusServiceUnavailable:
			return http.StatusServiceUnavailable, "maclaw_unavailable", "maclaw runtime is unavailable"
		default:
			if upstream.StatusCode >= 500 {
				return http.StatusBadGateway, "maclaw_upstream_failed", "maclaw runtime request failed"
			}
			return http.StatusBadGateway, "maclaw_proxy_error", "maclaw request failed"
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "maclaw_timeout", "maclaw runtime request timed out"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return http.StatusGatewayTimeout, "maclaw_timeout", "maclaw runtime request timed out"
	}
	return http.StatusServiceUnavailable, "maclaw_unavailable", "maclaw runtime is unavailable"
}
