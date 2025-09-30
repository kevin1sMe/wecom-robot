//go:build integration

package mcpclient

import (
	"context"
	"os"
	"testing"
	"time"
)

// Integration test against a real MCP HTTP server.
//
// Usage:
//
//	MCP_HTTP_URL=https://wechat-scraper.gameapp.club/mcp \
//	MCP_TOOL_NAME=http \
//	go test ./internal/mcpclient -tags=integration -run TestFetchURL_WeChat -v -timeout 2m
func TestFetchURL_WeChat(t *testing.T) {
	endpoint := os.Getenv("MCP_HTTP_URL")
	if endpoint == "" {
		endpoint = "https://wechat-scraper.gameapp.club/mcp"
	}
	tool := os.Getenv("MCP_TOOL_NAME")
	if tool == "" {
		tool = "scrape_wechat_article"
	}

	c := New(endpoint, tool)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	url := "https://mp.weixin.qq.com/s/R5t8xJW1CnjJjZoeOgX_Rg"
	content, err := c.FetchURL(ctx, url)
	if err != nil {
		t.Fatalf("FetchURL error: %v", err)
	}
	if n := len(content); n < 500 {
		t.Fatalf("unexpected short content: got %d bytes", n)
	}
	t.Logf("fetched %d bytes from %s via %s", len(content), url, endpoint)
}
