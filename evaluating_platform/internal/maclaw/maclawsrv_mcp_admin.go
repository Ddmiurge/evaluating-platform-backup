package maclaw

import (
	"context"
	"errors"
	"strings"
)

func normalizeMCPServerInput(in MCPServerInput) MCPServerInput {
	in.Kind = strings.TrimSpace(strings.ToLower(in.Kind))
	in.Name = strings.TrimSpace(in.Name)
	in.EndpointURL = strings.TrimSpace(in.EndpointURL)
	in.AuthType = strings.TrimSpace(strings.ToLower(in.AuthType))
	in.AuthSecret = strings.TrimSpace(in.AuthSecret)
	return in
}

func validateMCPServerInput(in MCPServerInput) error {
	if in.Kind == "" {
		return errors.New("mcp kind is required")
	}
	if in.Name == "" {
		return errors.New("mcp name is required")
	}
	if in.Kind == "remote" && in.EndpointURL == "" {
		return errors.New("mcp endpoint url is required")
	}
	return nil
}

func mcpServerUpdateInputFromInput(in MCPServerInput) mcpServerUpdateInput {
	out := mcpServerUpdateInput{
		Headers: in.Headers,
		Env:     in.Env,
	}
	if strings.TrimSpace(in.Name) != "" {
		value := in.Name
		out.Name = &value
	}
	if strings.TrimSpace(in.EndpointURL) != "" {
		value := in.EndpointURL
		out.EndpointURL = &value
	}
	if strings.TrimSpace(in.AuthType) != "" {
		value := in.AuthType
		out.AuthType = &value
	}
	if strings.TrimSpace(in.AuthSecret) != "" {
		value := in.AuthSecret
		out.AuthSecret = &value
	}
	if strings.TrimSpace(in.Command) != "" {
		value := in.Command
		out.Command = &value
	}
	if in.Args != nil {
		value := in.Args
		out.Args = &value
	}
	disabled := in.Disabled
	out.Disabled = &disabled
	autoStart := in.AutoStart
	out.AutoStart = &autoStart
	return out
}

func (c *MaclawSrvClient) EnsureRedteamMCPBridge(ctx context.Context, in RedteamMCPBridgeInput) (*MCPServerView, error) {
	endpoint := strings.TrimSpace(in.EndpointURL)
	if endpoint == "" {
		return nil, errors.New("redteam mcp endpoint url is required")
	}
	headers := redteamMCPBridgeHeaders(in.Headers)
	input := MCPServerInput{
		Kind:        "remote",
		Name:        RedteamMCPBridgeName,
		EndpointURL: endpoint,
		AuthType:    "bearer",
		AuthSecret:  in.AuthSecret,
		Headers:     headers,
		AutoStart:   in.AutoStart,
	}
	servers, err := c.ListMCPServers(ctx, 100)
	if err != nil {
		return nil, err
	}
	for i := range servers {
		if strings.EqualFold(strings.TrimSpace(servers[i].Name), RedteamMCPBridgeName) {
			if redteamMCPBridgeNeedsUpdate(servers[i], input) {
				return c.UpdateMCPServer(ctx, servers[i].ID, input)
			}
			return &servers[i], nil
		}
	}
	return c.CreateMCPServer(ctx, input)
}

func redteamMCPBridgeHeaders(input map[string]string) map[string]string {
	headers := map[string]string{"X-Evaluating-Platform-Bridge": "redteam_v1"}
	for key, value := range input {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			headers[key] = value
		}
	}
	return headers
}

func redteamMCPBridgeNeedsUpdate(server MCPServerView, in MCPServerInput) bool {
	if strings.TrimSpace(server.ID) == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(server.Kind), in.Kind) {
		return true
	}
	if strings.TrimSpace(server.EndpointURL) != in.EndpointURL {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(server.AuthType), in.AuthType) {
		return true
	}
	if strings.TrimSpace(in.AuthSecret) != "" {
		return true
	}
	for name := range in.Headers {
		if !hasCaseInsensitiveString(server.HeaderNames, name) {
			return true
		}
	}
	if server.Disabled {
		return true
	}
	if server.AutoStart != in.AutoStart {
		return true
	}
	return false
}

func hasCaseInsensitiveString(items []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), want) {
			return true
		}
	}
	return false
}
