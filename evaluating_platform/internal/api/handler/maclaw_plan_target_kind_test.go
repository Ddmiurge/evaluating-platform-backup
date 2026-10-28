package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"
	"evaluating_platform/internal/maclaw"
	"evaluating_platform/pkg/config"
)

// The plan card's 「目标类型」 must follow the target the platform actually has.
//
// maclaw's planner writes llm for an agent target — it sees the target at most as
// a name, and its fast path hardcodes the kind. The kind is a platform fact
// (`maclaw_target_configs.kind`) and decides which endpoint the run calls, so a
// card that says llm for an agent is not a cosmetic error: it tells the user the
// wrong thing about what is about to be tested (reported 2026-10-28).
func TestPlanCardTargetKindComesFromThePlatform(t *testing.T) {
	keyStore, err := appcrypto.NewKeyStore(config.CryptoConfig{MasterKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	targets := maclaw.NewTargetConfigService(&handlerTargetConfigStore{}, keyStore)
	userID := uuid.New()
	if _, err := targets.SaveTarget(context.Background(), userID, maclaw.EvaluationTargetInput{
		Name:             "客服 Agent",
		Kind:             maclaw.EvaluationTargetKindAgent,
		CredentialSecret: "sk-agent",
		Enabled:          true,
		Metadata: map[string]string{
			maclaw.TargetMetadataAgentEndpoint: "https://agent.example.com/chat",
		},
	}); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}

	plan := `{"response_source":"plan_confirm","target_summary":{"target_type":"llm"},"target_type":"llm","risk_types":["prompt_injection"],"test_count":3}`

	t.Run("session snapshot", func(t *testing.T) {
		gateway := &fakeMaclawRuntimeGateway{
			enabled: true,
			messages: []maclaw.RuntimeMessage{
				{Role: "user", Content: "测一下我的 agent"},
				{ID: "plan-1", Role: "assistant", Content: plan, Metadata: map[string]string{"response_source": "plan_confirm"}},
			},
		}
		h := NewMaclawRuntimeHandler(gateway, "inst_1")
		h.SetTargetConfigService(targets)
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("user_role", "enterprise")
			c.Set("user_id", userID.String())
			c.Next()
		})
		router.GET("/maclaw/evaluation/sessions/:id", h.GetSession)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/sessions/sess_1", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		// The plan payload is JSON *inside* the message content, so decode rather
		// than substring-match the escaped outer document.
		var snapshot struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		var plan map[string]any
		if err := json.Unmarshal([]byte(snapshot.Messages[1].Content), &plan); err != nil {
			t.Fatalf("decode plan: %v body=%s", err, snapshot.Messages[1].Content)
		}
		if plan["target_type"] != "agent" {
			t.Fatalf("plan target_type = %v, want agent (the platform's kind)", plan["target_type"])
		}
		if summary, ok := plan["target_summary"].(map[string]any); ok && summary["target_type"] != "agent" {
			t.Errorf("target_summary.target_type = %v, want agent", summary["target_type"])
		}
		// The claim is kept so a planner drift stays visible in the stored message.
		if plan["target_type_claimed"] != "llm" {
			t.Errorf("the planner's original claim was dropped instead of recorded: %v", plan["target_type_claimed"])
		}
	})
}

// Unit-level: the rewrite touches only the kind, keeps both shapes of
// target_summary in agreement, and leaves the planner's value alone when the
// platform does not know the target.
func TestApplyPlatformTargetFacts(t *testing.T) {
	t.Run("object summary", func(t *testing.T) {
		body := map[string]any{"target_type": "llm", "target_summary": map[string]any{"target_type": "llm", "name": "x"}}
		applyPlatformTargetFacts(body, "agent")
		if body["target_type"] != "agent" {
			t.Errorf("target_type = %v", body["target_type"])
		}
		if body["target_summary"].(map[string]any)["target_type"] != "agent" {
			t.Errorf("summary not updated: %v", body["target_summary"])
		}
		if body["target_summary"].(map[string]any)["name"] != "x" {
			t.Error("unrelated summary fields were touched")
		}
	})

	t.Run("string summary", func(t *testing.T) {
		body := map[string]any{"target_type": "llm", "target_summary": `{"target_type":"llm","model":"m"}`}
		applyPlatformTargetFacts(body, "agent")
		var summary map[string]any
		if err := json.Unmarshal([]byte(body["target_summary"].(string)), &summary); err != nil {
			t.Fatalf("summary is not valid JSON: %v", err)
		}
		if summary["target_type"] != "agent" || summary["model"] != "m" {
			t.Errorf("summary = %v, want the kind rewritten and the model kept", summary)
		}
	})

	t.Run("unknown kind leaves the card alone", func(t *testing.T) {
		body := map[string]any{"target_type": "llm"}
		applyPlatformTargetFacts(body, "")
		if body["target_type"] != "llm" {
			t.Errorf("target_type = %v, want the planner's value untouched", body["target_type"])
		}
		if _, ok := body["target_type_claimed"]; ok {
			t.Error("a claim was recorded when nothing was overridden")
		}
	})

	t.Run("an agreeing claim is not recorded", func(t *testing.T) {
		body := map[string]any{"target_type": "agent"}
		applyPlatformTargetFacts(body, "agent")
		if _, ok := body["target_type_claimed"]; ok {
			t.Error("target_type_claimed recorded although the planner already agreed")
		}
	})
}
