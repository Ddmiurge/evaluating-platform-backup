package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	redteamExecutionGrantMetadataKey = "evaluation_execution_grant"
	redteamExecutionGrantTTL         = 2 * time.Hour
)

type redteamExecutionGrantPayload struct {
	UserID                 string   `json:"user_id"`
	SessionID              string   `json:"session_id,omitempty"`
	TestCount              int      `json:"test_count,omitempty"`
	SelectedCapabilityRefs []string `json:"selected_capability_refs,omitempty"`
	SelectedSkillNames     []string `json:"selected_skill_names,omitempty"`
	SelectionStrategy      string   `json:"selection_strategy,omitempty"`
	ExpiresAt              int64    `json:"expires_at"`
}

type redteamExecutionGrantContext struct {
	TestCount              int
	SelectedCapabilityRefs []string
	SelectedSkillNames     []string
	SelectionStrategy      string
}

func mintRedteamExecutionGrant(secret, userID, sessionID string, expiresAt time.Time, testCount ...int) string {
	ctx := redteamExecutionGrantContext{}
	if len(testCount) > 0 && testCount[0] > 0 {
		ctx.TestCount = testCount[0]
	}
	return mintRedteamExecutionGrantWithContext(secret, userID, sessionID, expiresAt, ctx)
}

func mintRedteamExecutionGrantWithContext(secret, userID, sessionID string, expiresAt time.Time, ctx redteamExecutionGrantContext) string {
	secret = strings.TrimSpace(secret)
	userID = strings.TrimSpace(userID)
	if secret == "" || userID == "" || expiresAt.IsZero() {
		return ""
	}
	payload := redteamExecutionGrantPayload{
		UserID:                 userID,
		SessionID:              strings.TrimSpace(sessionID),
		TestCount:              ctx.TestCount,
		SelectedCapabilityRefs: uniqueNonEmptyStrings(ctx.SelectedCapabilityRefs),
		SelectedSkillNames:     uniqueNonEmptyStrings(ctx.SelectedSkillNames),
		SelectionStrategy:      strings.TrimSpace(ctx.SelectionStrategy),
		ExpiresAt:              expiresAt.UTC().Unix(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verifyRedteamExecutionGrantPayload(secret, token, userID string, now time.Time) (redteamExecutionGrantPayload, error) {
	secret = strings.TrimSpace(secret)
	token = strings.TrimSpace(token)
	userID = strings.TrimSpace(userID)
	if secret == "" {
		return redteamExecutionGrantPayload{}, errors.New("execution grant verifier is not configured")
	}
	if token == "" {
		return redteamExecutionGrantPayload{}, errors.New("execution grant is required")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return redteamExecutionGrantPayload{}, errors.New("execution grant is invalid")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return redteamExecutionGrantPayload{}, errors.New("execution grant is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return redteamExecutionGrantPayload{}, errors.New("execution grant is invalid")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(data)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return redteamExecutionGrantPayload{}, errors.New("execution grant is invalid")
	}
	var payload redteamExecutionGrantPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return redteamExecutionGrantPayload{}, errors.New("execution grant is invalid")
	}
	if strings.TrimSpace(payload.UserID) == "" || !strings.EqualFold(strings.TrimSpace(payload.UserID), userID) {
		return redteamExecutionGrantPayload{}, errors.New("execution grant user mismatch")
	}
	if payload.ExpiresAt <= 0 || !now.UTC().Before(time.Unix(payload.ExpiresAt, 0).UTC()) {
		return redteamExecutionGrantPayload{}, errors.New("execution grant has expired")
	}
	return payload, nil
}

