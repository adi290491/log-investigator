package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func ConnectMCP(ctx context.Context, serverPath string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "log-investigator-client", Version: "v1.0.0"}, nil)
	transport := &mcp.CommandTransport{
		Command: exec.Command(serverPath),
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server %q: %w", serverPath, err)
	}
	return session, nil
}

func MCPToolsToAnthropicTools(mcpTools []*mcp.Tool) ([]anthropic.ToolUnionParam, error) {
	var tools []anthropic.ToolUnionParam
	for _, t := range mcpTools {
		raw, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal schema error for %s: %w", t.Name, err)
		}

		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, fmt.Errorf("parse schema error for %s: %w", t.Name, err)
		}

		tools = append(tools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: schema.Properties,
					Required:   schema.Required,
				},
			},
		})
	}
	return tools, nil
}

func TextContent(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}
