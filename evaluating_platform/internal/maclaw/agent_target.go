package maclaw

// agent_target.go — custom-HTTP agent target support (target kind=agent).
//
// An agent target lets enterprises plug any stateless conversational endpoint
// (an existing customer-service agent, a RAG gateway, an MCP-backed
// assistant...) into the platform as the system-under-test, mirroring
// promptfoo's `http` provider model:
//
//	agent_endpoint        the conversation endpoint URL
//	agent_method          HTTP method (default POST)
//	agent_headers_template  JSON object of static headers; values may contain
//	                       the {{api_key}} placeholder
//	agent_body_template     JSON request body; string values may contain the
//	                       {{prompt}} and {{api_key}} placeholders
//	agent_response_path     dot path into the JSON response holding the agent
//	                       reply text, e.g. `reply.text` or
//	                       `choices[0].message.content`
//
// Placeholders:
//	{{prompt}}  substituted per call with the attack prompt
//	{{api_key}}  substituted server-side with the encrypted credential secret
//
// The credential secret is write-only from the browser's perspective: it is
// stored encrypted (TargetConfigService) and only ever substituted inside
// server-side request builders (ProbeTarget, callStoredTarget) or forwarded
// one-shot to the engine. It never appears in summaries, logs, engine run
// records, or browser responses.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Metadata keys for agent target configuration (stored in target metadata;
// none contain the sanitizer's blocked substrings so they round-trip safely).
const (
	TargetMetadataAgentEndpoint     = "agent_endpoint"
	TargetMetadataAgentMethod       = "agent_method"
	TargetMetadataAgentHeadersTmpl  = "agent_headers_template"
	TargetMetadataAgentBodyTmpl     = "agent_body_template"
	TargetMetadataAgentResponsePath = "agent_response_path"
)

const agentAPIKeyPlaceholder = "{{api_key}}"
const agentPromptPlaceholder = "{{prompt}}"

// TargetIsAgent reports whether the target is a custom-HTTP agent target.
func TargetIsAgent(target EvaluationTargetInput) bool {
	return target.Kind == EvaluationTargetKindAgent
}

// ValidateAgentTargetInput checks the browser-supplied agent target fields.
// Returns a plain error whose message is safe to show to the user.
func ValidateAgentTargetInput(in EvaluationTargetInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("agent target name is required")
	}
	if strings.TrimSpace(in.Metadata[TargetMetadataAgentEndpoint]) == "" {
		return errors.New("agent endpoint url is required")
	}
	if headers := strings.TrimSpace(in.Metadata[TargetMetadataAgentHeadersTmpl]); headers != "" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(headers), &parsed); err != nil {
			return errors.New("agent headers template must be a JSON object of header name to value")
		}
	}
	if body := strings.TrimSpace(in.Metadata[TargetMetadataAgentBodyTmpl]); body != "" {
		var parsed json.RawMessage
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			return errors.New("agent body template must be valid JSON")
		}
	}
	if method := strings.TrimSpace(in.Metadata[TargetMetadataAgentMethod]); method != "" {
		normalized := strings.ToUpper(method)
		switch normalized {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch:
		default:
			return errors.New("agent method must be one of GET, POST, PUT, PATCH")
		}
	}
	return nil
}

// substituteAgentAPIKey replaces {{api_key}} in a template with the
// credential secret. Server-side only; never log the result.
func substituteAgentAPIKey(template, apiKey string) string {
	if template == "" || apiKey == "" {
		return template
	}
	return strings.ReplaceAll(template, agentAPIKeyPlaceholder, apiKey)
}

// agentTargetRequestSpec is the fully-substituted request shape for one call.
type agentTargetRequestSpec struct {
	Endpoint string
	Method   string
	Header   http.Header
	Body     []byte
}

// buildAgentTargetRequestSpec renders the agent target templates for one
// attack prompt. The credential secret is substituted here and must not
// escape the caller. Invalid templates yield an error the caller maps to a
// failed call with a safe error class.
func buildAgentTargetRequestSpec(target EvaluationTargetInput, prompt string) (agentTargetRequestSpec, error) {
	spec := agentTargetRequestSpec{}
	apiKey := strings.TrimSpace(target.CredentialSecret)

	endpoint := strings.TrimSpace(target.Metadata[TargetMetadataAgentEndpoint])
	if endpoint == "" {
		return spec, errors.New("agent endpoint url is required")
	}
	spec.Endpoint = substituteAgentAPIKey(endpoint, apiKey)

	method := strings.ToUpper(strings.TrimSpace(target.Metadata[TargetMetadataAgentMethod]))
	if method == "" {
		method = http.MethodPost
	}
	spec.Method = method

	// Headers: user template ({{api_key}} substituted) + safe defaults.
	headers := map[string]string{}
	if raw := strings.TrimSpace(target.Metadata[TargetMetadataAgentHeadersTmpl]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			return spec, errors.New("agent headers template must be a JSON object of header name to value")
		}
	}
	header := http.Header{}
	usedAPIKey := false
	for name, value := range headers {
		trimmedName := strings.TrimSpace(name)
		if trimmedName == "" {
			continue
		}
		rendered := substituteAgentAPIKey(value, apiKey)
		if strings.Contains(value, agentAPIKeyPlaceholder) {
			usedAPIKey = true
		}
		if strings.TrimSpace(rendered) == "" {
			continue
		}
		header.Set(trimmedName, rendered)
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	// Zero-config auth: when the credential secret is set but the templates
	// never reference {{api_key}}, default to a bearer Authorization header so
	// OpenAI-compatible agent gateways work without header boilerplate.
	if apiKey != "" && !usedAPIKey && header.Get("Authorization") == "" {
		header.Set("Authorization", "Bearer "+apiKey)
	}
	spec.Header = header

	// Body: user template ({{api_key}} substituted server-side, {{prompt}}
	// substituted per call) with a sane default.
	bodyTemplate := strings.TrimSpace(target.Metadata[TargetMetadataAgentBodyTmpl])
	if bodyTemplate == "" {
		bodyTemplate = `{"prompt":"{{prompt}}"}`
	}
	rendered, err := renderAgentBodyTemplate(bodyTemplate, prompt, apiKey)
	if err != nil {
		return spec, err
	}
	spec.Body = rendered
	return spec, nil
}

// renderAgentBodyTemplate parses the body template JSON and substitutes the
// placeholders inside every string value: {{api_key}} with the credential
// secret (server-side) and {{prompt}} with the per-call attack prompt. The
// prompt is inserted as a plain string value (already JSON-escaped by
// re-marshalling the tree), so quotes/newlines in the prompt cannot break the
// JSON structure.
func renderAgentBodyTemplate(bodyTemplate, prompt, apiKey string) ([]byte, error) {
	var tree interface{}
	if err := json.Unmarshal([]byte(bodyTemplate), &tree); err != nil {
		return nil, errors.New("agent body template must be valid JSON")
	}
	rendered := substituteAgentTemplateTree(tree, prompt, apiKey)
	out, err := json.Marshal(rendered)
	if err != nil {
		return nil, errors.New("agent body template could not be rendered")
	}
	return out, nil
}

func substituteAgentTemplateTree(node interface{}, prompt, apiKey string) interface{} {
	switch typed := node.(type) {
	case string:
		value := typed
		if strings.Contains(value, agentPromptPlaceholder) {
			value = strings.ReplaceAll(value, agentPromptPlaceholder, prompt)
		}
		if strings.Contains(value, agentAPIKeyPlaceholder) {
			value = strings.ReplaceAll(value, agentAPIKeyPlaceholder, apiKey)
		}
		return value
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, value := range typed {
			out[key] = substituteAgentTemplateTree(value, prompt, apiKey)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for index, value := range typed {
			out[index] = substituteAgentTemplateTree(value, prompt, apiKey)
		}
		return out
	default:
		return node
	}
}

// agentTargetResponsePath returns the configured response extraction path.
func agentTargetResponsePath(target EvaluationTargetInput) string {
	return strings.TrimSpace(target.Metadata[TargetMetadataAgentResponsePath])
}

// extractAgentResponseText walks the configured dot path (e.g.
// `reply.text`, `choices[0].message.content`) into the JSON response body and
// returns the string found there. Empty when the path is unset or does not
// resolve to a string.
func extractAgentResponseText(data []byte, responsePath string) string {
	path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(responsePath), "json."))
	if path == "" {
		return ""
	}
	var current interface{}
	if err := json.Unmarshal(data, &current); err != nil {
		return ""
	}
	for _, segment := range splitAgentResponsePath(path) {
		switch typed := current.(type) {
		case map[string]interface{}:
			next, ok := typed[segment.key]
			if !ok {
				return ""
			}
			current = next
		case []interface{}:
			if segment.index < 0 || segment.index >= len(typed) {
				return ""
			}
			current = typed[segment.index]
		default:
			return ""
		}
	}
	if text, ok := current.(string); ok {
		return text
	}
	return ""
}

type agentResponsePathSegment struct {
	key   string
	index int
}

func splitAgentResponsePath(path string) []agentResponsePathSegment {
	segments := make([]agentResponsePathSegment, 0, 8)
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Support `name[0]` and `name[0][1]` suffixes on object keys.
		if open := strings.IndexByte(part, '['); open > 0 && strings.HasSuffix(part, "]") {
			key := part[:open]
			segments = append(segments, agentResponsePathSegment{key: key})
			rest := part[open:]
			for len(rest) > 1 {
				if !strings.HasPrefix(rest, "[") {
					break
				}
				closeIdx := strings.IndexByte(rest, ']')
				if closeIdx < 0 {
					break
				}
				index, err := parseIndexString(rest[1:closeIdx])
				if err != nil {
					return nil
				}
				segments = append(segments, agentResponsePathSegment{index: index})
				rest = rest[closeIdx+1:]
			}
			continue
		}
		if index, err := parseIndexString(part); err == nil {
			segments = append(segments, agentResponsePathSegment{index: index})
			continue
		}
		segments = append(segments, agentResponsePathSegment{key: part})
	}
	return segments
}

func parseIndexString(raw string) (int, error) {
	index, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || index < 0 {
		return 0, errors.New("invalid index")
	}
	return index, nil
}
