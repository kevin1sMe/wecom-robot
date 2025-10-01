package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"wecom-robot/internal/params"
)

// Client wraps an MCP HTTP endpoint and a tool name (default "http").
type Client struct {
	Endpoint string
	ToolName string
}

// New returns a new Client.
func New(endpoint, toolName string) *Client {
	if toolName == "" {
		toolName = "http"
	}
	return &Client{Endpoint: endpoint, ToolName: toolName}
}

// FetchURL invokes the MCP tool with { url, method: GET } and returns aggregated text content.
func (c *Client) FetchURL(ctx context.Context, url string) (string, error) {
	if c.Endpoint == "" {
		return "", errors.New("empty MCP endpoint")
	}
	trans, err := transport.NewStreamableHTTP(c.Endpoint, transport.WithHTTPTimeout(params.MCPTransportTimeout))
	if err != nil {
		return "", fmt.Errorf("new streamable http: %w", err)
	}
	cli := mcpclient.NewClient(trans)
	// ensure we have a reasonable timeout if caller didn't set one
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, params.StepTimeout)
		defer cancel()
	}
	if err := cli.Start(ctx); err != nil {
		return "", fmt.Errorf("mcp start: %w", err)
	}
	defer func() { _ = cli.Close() }()

	if _, err := cli.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "wecom-robot", Version: "0.1.0"},
			Capabilities:    mcp.ClientCapabilities{},
		},
	}); err != nil {
		return "", fmt.Errorf("mcp initialize: %w", err)
	}

	// Build arguments based on known tool conventions
	var args any
	switch strings.ToLower(c.ToolName) {
	case "", "http":
		args = map[string]any{"url": url, "method": "GET"}
	case "scrape_wechat_article":
		// See provided tool schema: we request only HTML for our pipeline
		args = map[string]any{
			"url":          url,
			"formats":      []string{"html"},
			"sessionTTL":   180,
			"proxyCountry": "CN",
			// sessionName: omitted by default
		}
	default:
		// Best-effort: send url only
		args = map[string]any{"url": url}
	}

	result, err := cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      c.ToolName,
			Arguments: args,
		},
	})
	if err != nil {
		return "", fmt.Errorf("mcp call tool: %w", err)
	}
	// Prefer typed content
	if result != nil && len(result.Content) > 0 {
		var sb strings.Builder
		for _, part := range result.Content {
			switch v := part.(type) {
			case mcp.TextContent:
				sb.WriteString(v.Text)
			default:
				// ignore non-text parts here
			}
		}
		if sb.Len() > 0 {
			return sb.String(), nil
		}
	}
	// Prefer structuredContent.html when available
	if result != nil && result.StructuredContent != nil {
		// try map first
		if m, ok := result.StructuredContent.(map[string]any); ok {
			if s, _ := m["html"].(string); s != "" {
				return s, nil
			}
			if s, _ := m["content"].(string); s != "" {
				return s, nil
			}
		} else {
			// try to marshal/unmarshal to map
			if b, err := json.Marshal(result.StructuredContent); err == nil {
				var mv map[string]any
				if json.Unmarshal(b, &mv) == nil {
					if s, _ := mv["html"].(string); s != "" {
						return s, nil
					}
					if s, _ := mv["content"].(string); s != "" {
						return s, nil
					}
				}
			}
		}
	}
	// Fallback to JSON parse
	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b), nil
	}
	if s := extractTextFromMap(v); s != "" {
		return s, nil
	}
	return string(b), nil
}

func extractTextFromMap(m map[string]any) string {
	if c, ok := m["content"].([]any); ok {
		if s := collectText(c); s != "" {
			return s
		}
	}
	if result, ok := m["result"].(map[string]any); ok {
		if c, ok := result["content"].([]any); ok {
			if s := collectText(c); s != "" {
				return s
			}
		}
		if s, ok := result["content"].(string); ok && s != "" {
			return s
		}
	}
	if s, ok := m["text"].(string); ok && s != "" {
		return s
	}
	if s, ok := m["value"].(string); ok && s != "" {
		return s
	}
	return ""
}

func collectText(parts []any) string {
	var sb strings.Builder
	for _, it := range parts {
		if m, ok := it.(map[string]any); ok {
			if t, _ := m["type"].(string); t != "" {
				switch t {
				case "text", "string", "log":
					if s, _ := m["text"].(string); s != "" {
						sb.WriteString(s)
					}
					if s, _ := m["value"].(string); s != "" {
						sb.WriteString(s)
					}
				case "text_delta", "text-delta", "delta":
					if s, _ := m["text"].(string); s != "" {
						sb.WriteString(s)
					}
					if s, _ := m["delta"].(string); s != "" {
						sb.WriteString(s)
					}
				}
			} else {
				if s, _ := m["text"].(string); s != "" {
					sb.WriteString(s)
				}
				if s, _ := m["value"].(string); s != "" {
					sb.WriteString(s)
				}
			}
		}
	}
	return sb.String()
}
