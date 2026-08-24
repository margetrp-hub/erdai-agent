package main

import (
	"context"
	"encoding/json"
	"strings"
)

type mcpBridgeRoute struct {
	ServerID       string
	ToolName       string
	Authority      string
	Approved       bool
	TimeoutSeconds int
}

func (a *AgentRuntime) discoverCoreMCPTools(
	ctx context.Context,
	policy runtimeToolPolicy,
	isAdmin bool,
	message string,
) ([]map[string]any, map[string]mcpBridgeRoute) {
	routes := make(map[string]mcpBridgeRoute)
	definitions := make([]map[string]any, 0)
	authority := "member"
	if isAdmin {
		authority = "admin"
	}
	if policy.Authority != authority {
		return definitions, routes
	}
	for _, server := range policy.MCPServers {
		approved, allowed := mcpServerPermitted(server, isAdmin, message)
		if !allowed || server.Transport != "http" || strings.TrimSpace(server.ID) == "" {
			continue
		}
		discovered, err := a.discoverNativeMCPTools(ctx, server, authority, approved)
		if err != nil {
			continue
		}
		for _, tool := range discovered {
			if strings.TrimSpace(tool.Name) == "" {
				continue
			}
			modelName := mcpModelToolName(server.ToolPrefix, tool.Name)
			if modelName == "" {
				continue
			}
			if _, exists := routes[modelName]; exists {
				continue
			}
			schema := tool.InputSchema
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			definitions = append(definitions, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        modelName,
					"description": tool.Description,
					"parameters":  schema,
				},
			})
			routes[modelName] = mcpBridgeRoute{
				ServerID: server.ID, ToolName: tool.Name,
				Authority: authority, Approved: approved,
				TimeoutSeconds: server.TimeoutSeconds,
			}
		}
	}
	return definitions, routes
}

func mcpServerPermitted(server runtimeMCPServer, isAdmin bool, message string) (bool, bool) {
	switch server.ApprovalMode {
	case "auto":
		return true, true
	case "admin_only":
		return isAdmin, isAdmin
	case "confirm":
		normalized := strings.ToLower(strings.TrimSpace(message))
		terms := append([]string{server.Name, server.ID, server.ToolPrefix}, server.AllowedTools...)
		for _, term := range terms {
			term = strings.ToLower(strings.Trim(strings.TrimSpace(term), "_-"))
			if term != "" && strings.Contains(normalized, term) {
				return true, true
			}
		}
		return false, false
	default:
		return false, false
	}
}

func mcpModelToolName(prefix, name string) string {
	raw := strings.TrimSpace(prefix) + strings.TrimSpace(name)
	var builder strings.Builder
	for _, char := range raw {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
		if builder.Len() >= 64 {
			break
		}
	}
	return strings.Trim(builder.String(), "_-")
}

func (a *AgentRuntime) callCoreMCP(ctx context.Context, route mcpBridgeRoute, rawArguments string) toolResult {
	fail := func(code string) toolResult {
		encoded, _ := json.Marshal(map[string]any{"ok": false, "error": code})
		return toolResult{Content: string(encoded)}
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(rawArguments), &arguments); err != nil {
		return fail("invalid_arguments")
	}
	result, err := a.callNativeMCPTool(ctx, route, arguments)
	if err != nil {
		return fail("mcp_call_failed")
	}
	encoded, _ := json.Marshal(map[string]any{
		"ok": true, "untrusted": true, "result": result,
	})
	return toolResult{Content: string(encoded)}
}
