package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"wecom-robot/internal/params"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// Page contains both HTML content and structured metadata from MCP response.
type Page struct {
	URL      string         `json:"url"`      // The URL of the page
	HTML     string         `json:"html"`     // The HTML content
	Metadata map[string]any `json:"metadata"` // Extracted metadata (title, author, published_date, etc.)
}

// Client wraps an MCP HTTP endpoint and tool name (only "scrape_wechat_article" is supported).
type Client struct {
	Endpoint string
	ToolName string
	// Optional bearer token. If set, we send Authorization: Bearer <token>
	AuthToken string
}

// New returns a new Client.
func New(endpoint, toolName string, authToken string) *Client {
	if toolName == "" {
		toolName = "scrape_wechat_article"
	}
	return &Client{Endpoint: endpoint, ToolName: toolName, AuthToken: strings.TrimSpace(authToken)}
}

// FetchURL invokes the scrape_wechat_article MCP tool and returns HTML and metadata.
func (c *Client) FetchURL(ctx context.Context, url string) (*Page, error) {
	if c.Endpoint == "" {
		return nil, errors.New("empty MCP endpoint")
	}
	opts := []transport.StreamableHTTPCOption{transport.WithHTTPTimeout(params.MCPTransportTimeout)}
	if tok := strings.TrimSpace(c.AuthToken); tok != "" {
		// Only prefix Bearer if not already provided
		hdr := tok
		low := strings.ToLower(tok)
		if !strings.HasPrefix(low, "bearer ") {
			hdr = "Bearer " + tok
		}
		opts = append(opts, transport.WithHTTPHeaders(map[string]string{
			"Authorization": hdr,
		}))
	}
	trans, err := transport.NewStreamableHTTP(c.Endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("new streamable http: %w", err)
	}
	cli := mcpclient.NewClient(trans)
	// ensure we have a reasonable timeout if caller didn't set one
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, params.StepTimeout)
		defer cancel()
	}
	if err := cli.Start(ctx); err != nil {
		return nil, fmt.Errorf("mcp start: %w", err)
	}
	defer func() { _ = cli.Close() }()

	// Only support scrape_wechat_article tool
	if strings.ToLower(c.ToolName) != "scrape_wechat_article" {
		return nil, fmt.Errorf("unsupported MCP tool: %s (only scrape_wechat_article is supported)", c.ToolName)
	}

	if _, err := cli.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "wecom-robot", Version: "0.1.0"},
			Capabilities:    mcp.ClientCapabilities{},
		},
	}); err != nil {
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}

	// Build arguments for scrape_wechat_article tool (per DOCS.md)
	args := map[string]any{
		"url":          url,
		"formats":      []string{"html"},
		"sessionTTL":   180,
		"proxyCountry": "CN",
		// sessionName: omitted by default
	}

	result, err := cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      c.ToolName,
			Arguments: args,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("mcp call tool: %w", err)
	}

	// Handle scrape_wechat_article deterministic structure (see DOCS.md)
	return extractPageFromWechatScraper(url, result)
}

// extractPageFromWechatScraper handles the deterministic structure from scrape_wechat_article tool.
// Per DOCS.md:
//
//	Success: content[{type: 'text', text: <JSON string with {status, url, metadata, html, ...}>}]
//	Error: content[{type: 'text', text: 'error message'}], isError: true
func extractPageFromWechatScraper(url string, result *mcp.CallToolResult) (*Page, error) {
	// Check for error response
	if result.IsError {
		errMsg := "scrape_wechat_article returned error"
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(mcp.TextContent); ok && tc.Text != "" {
				errMsg = tc.Text
			}
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	// Extract text content from MCP response
	if len(result.Content) == 0 {
		return nil, errors.New("empty content from scrape_wechat_article")
	}

	var textContent string
	for _, part := range result.Content {
		if tc, ok := part.(mcp.TextContent); ok {
			textContent = tc.Text
			break
		}
	}
	if textContent == "" {
		return nil, errors.New("no text content in scrape_wechat_article response")
	}

	// Parse the JSON payload (the inner JSON structure)
	var payload map[string]any
	if err := json.Unmarshal([]byte(textContent), &payload); err != nil {
		return nil, fmt.Errorf("parse scrape_wechat_article JSON: %w", err)
	}

	// Extract HTML from payload
	html, _ := payload["html"].(string)
	if html == "" {
		return nil, errors.New("no html field in scrape_wechat_article response")
	}

	// Extract metadata from payload
	metadata := make(map[string]any)
	if metaMap, ok := payload["metadata"].(map[string]any); ok {
		// Copy all metadata fields
		for k, v := range metaMap {
			metadata[k] = v
		}
	}

	// Also preserve top-level fields for traceability
	if url, ok := payload["url"].(string); ok && url != "" {
		metadata["source_url"] = url
	}
	if ts, ok := payload["timestamp"].(string); ok && ts != "" {
		metadata["fetch_timestamp"] = ts
	}
	if status, ok := payload["status"].(string); ok && status != "" {
		metadata["fetch_status"] = status
	}

	return &Page{
		URL:      url,
		HTML:     html,
		Metadata: metadata,
	}, nil
}
