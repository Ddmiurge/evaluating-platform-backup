package redteam

// payload_store.go — 通用小工具与元数据脱敏。
//
// 这里没有任何红队业务语义：它是 handle 生成、载荷存取、metadata 脱敏
// 三类底层操作。sanitizeMetadata / isUnsafeMetadataKey 是安全边界
// （防止 secret/token/payload/path 外泄到浏览器与报告），改动前请先读
// redteam_tool_bridge_test.go:266 TestRedteamToolBridgeReturnsHandlesWithoutSensitiveMetadata。
//
// 本文件由 U3 Step 2 从 redteam_tool_bridge.go 机械剪出，仅重命名导出面
// （safeErrorSummary -> SafeErrorSummary），逻辑零改动。

import (
	"encoding/json"
	"strconv"
	"strings"
)

// SafeErrorSummary 是父包 maclaw.SafeErrorSummary 的实际实现（跨包调用入口）。
func SafeErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	for _, marker := range []string{"secret", "token", "credential", "payload", "prompt", "response", "request", "content", "body", "path"} {
		if strings.Contains(strings.ToLower(text), marker) {
			return "execution failed"
		}
	}
	if len(text) > 180 {
		return text[:180]
	}
	return text
}

func MergeStringMetadata(dst, src map[string]string) {
	if dst == nil || len(src) == 0 {
		return
	}
	for key, value := range src {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		dst[key] = value
	}
}

func IntString(value int) string {
	return strconv.Itoa(value)
}

func Int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}

func SafeTextSnippet(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.Join(strings.Fields(value), " ")
	return TailRunes(value, limit)
}

func StringFromAnyForRedteam(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return IntString(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func TailRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return "..." + string(runes[len(runes)-limit:])
}

func MetadataFlag(metadata map[string]string, keys ...string) bool {
	for _, key := range keys {
		for actual, value := range metadata {
			if !strings.EqualFold(strings.TrimSpace(actual), key) {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "1", "true", "yes", "y", "success", "unsafe", "violated":
				return true
			}
		}
	}
	return false
}

func ContainsAnyFold(value string, markers []string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, marker := range markers {
		marker = strings.ToLower(strings.TrimSpace(marker))
		if marker != "" && strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func SanitizeMetadataMapFromAny(value any) map[string]string {
	switch typed := value.(type) {
	case map[string]string:
		return SanitizeMetadata(typed)
	case map[string]any:
		raw := make(map[string]string, len(typed))
		for key, value := range typed {
			if text := StringFromAnyForRedteam(value); strings.TrimSpace(text) != "" {
				raw[key] = text
			}
		}
		return SanitizeMetadata(raw)
	default:
		return nil
	}
}

func SanitizeMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		lower := strings.ToLower(strings.TrimSpace(key))
		if strings.Contains(lower, "sha256") || strings.Contains(lower, "hash") {
			out[strings.TrimSpace(key)] = strings.TrimSpace(value)
			continue
		}
		if isSafePayloadMetadataKey(lower) {
			out[strings.TrimSpace(key)] = strings.TrimSpace(value)
			continue
		}
		if lower == "" || isUnsafeMetadataKey(lower) {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

func isSafePayloadMetadataKey(lower string) bool {
	switch lower {
	case "payload_modality",
		"payload_kind",
		"payload_source",
		"payload_index",
		"payload_count":
		return true
	default:
		return false
	}
}

func isUnsafeMetadataKey(lower string) bool {
	if strings.Contains(lower, "secret") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "credential") ||
		strings.Contains(lower, "grant") ||
		strings.Contains(lower, "payload") ||
		strings.Contains(lower, "prompt") ||
		strings.Contains(lower, "response") ||
		strings.Contains(lower, "request") ||
		strings.Contains(lower, "content") ||
		strings.Contains(lower, "body") ||
		strings.Contains(lower, "path") {
		return true
	}
	if lower == "key" ||
		strings.HasSuffix(lower, "_key") ||
		strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "apikey") ||
		strings.Contains(lower, "access_key") ||
		strings.Contains(lower, "private_key") ||
		strings.Contains(lower, "auth_key") ||
		strings.Contains(lower, "llm_key") {
		return true
	}
	return false
}
