package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEnterpriseWelcomeAgentKindReturnsAgentCards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/welcome", NewEnterpriseWelcomeHandler().ListCapabilities)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/welcome?target_kind=agent&limit=6", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Items []WelcomeCapability `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 6 {
		t.Fatalf("items = %d, want 6: %#v", len(body.Items), body.Items)
	}
	labels := strings.Join([]string{
		body.Items[0].Label,
		body.Items[1].Label,
		body.Items[2].Label,
		body.Items[3].Label,
		body.Items[4].Label,
		body.Items[5].Label,
	}, "|")
	for _, want := range []string{"智能体提示注入测试", "工具越权调用测试", "目标劫持测试", "系统提示泄露测试", "智能体合规安全测试", "智能体拒答质量测试"} {
		if !strings.Contains(labels, want) {
			t.Fatalf("missing label %q in %s", want, labels)
		}
	}
	for _, item := range body.Items {
		combined := item.Label + " " + item.Title + " " + item.Description + " " + item.Prompt
		for _, forbidden := range []string{"MaClaw", "Skill 检索", "执行计划卡必须", "未安装或未同步", "shadow resource"} {
			if strings.Contains(combined, forbidden) {
				t.Fatalf("agent welcome capability should not expose internal instruction %q: %#v", forbidden, item)
			}
		}
		if !strings.Contains(item.Prompt, "请") || !strings.Contains(item.Prompt, "当前被测智能体") {
			t.Fatalf("agent welcome prompt should be an actionable Chinese request tied to current agent target: %#v", item)
		}
	}
}

func TestEnterpriseWelcomeAgentKindDiffersFromDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/welcome", NewEnterpriseWelcomeHandler().ListCapabilities)

	fetch := func(url string) []WelcomeCapability {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		var body struct {
			Items []WelcomeCapability `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.Items
	}

	defaultItems := fetch("/welcome")
	agentItems := fetch("/welcome?target_kind=agent")
	unknownKindItems := fetch("/welcome?target_kind=other")
	if len(defaultItems) == 0 || len(agentItems) == 0 {
		t.Fatalf("expected non-empty card lists: default=%d agent=%d", len(defaultItems), len(agentItems))
	}
	if defaultItems[0].ID == agentItems[0].ID {
		t.Fatalf("agent cards should differ from default cards: %s", defaultItems[0].ID)
	}
	if defaultItems[0].ID != unknownKindItems[0].ID {
		t.Fatalf("unknown target_kind should fall back to default cards: %s vs %s", unknownKindItems[0].ID, defaultItems[0].ID)
	}
}

func TestEnterpriseWelcomeCapabilitiesAreChineseAndSixItems(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/welcome", NewEnterpriseWelcomeHandler().ListCapabilities)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/welcome?limit=6", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Items []WelcomeCapability `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 6 {
		t.Fatalf("items = %d, want 6: %#v", len(body.Items), body.Items)
	}
	labels := strings.Join([]string{
		body.Items[0].Label,
		body.Items[1].Label,
		body.Items[2].Label,
		body.Items[3].Label,
		body.Items[4].Label,
		body.Items[5].Label,
	}, "|")
	for _, want := range []string{"合规安全测试", "文言文越狱测试", "提示注入检验", "模板样本组合评估", "已组合攻击回归测试", "内容拒答能力测试"} {
		if !strings.Contains(labels, want) {
			t.Fatalf("missing label %q in %s", want, labels)
		}
	}
	for _, item := range body.Items {
		combined := item.Label + " " + item.Title + " " + item.Description + " " + item.Prompt
		if strings.Contains(item.Label, "CCBOS") || strings.Contains(item.Description, "shadow resource") || strings.Contains(strings.ToLower(item.Description), "maclaw") {
			t.Fatalf("welcome capability should be product-facing Chinese text: %#v", item)
		}
		for _, forbidden := range []string{"MaClaw", "Skill 检索", "执行计划卡必须", "未安装或未同步", "shadow resource"} {
			if strings.Contains(combined, forbidden) {
				t.Fatalf("welcome capability should not expose internal instruction %q: %#v", forbidden, item)
			}
		}
		if !strings.Contains(item.Prompt, "请") || !strings.Contains(item.Prompt, "当前被测模型") {
			t.Fatalf("welcome prompt should be an actionable Chinese request tied to current target: %#v", item)
		}
	}
}
