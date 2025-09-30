package config

import (
	"errors"
	"os"
	"strconv"
)

type Config struct {
	Token          string
	EncodingAESKey string
	ReceiveID      string
	Port           string
	// LLM settings (OpenAI-compatible)
	LLMBaseURL string
	LLMAPIKey  string
	LLMModel   string
	// Optional: if nil, do not send temperature param (lets provider default)
	LLMTemperature *float64

	// MCP HTTP server (streamable-http) for fetching
	MCPHTTPURL  string // e.g., http://localhost:8080/mcp
	MCPToolName string // e.g., "http"

	// Readwise Reader
	ReadwiseToken string

	// Optional local cache for fetched HTML (testing convenience)
	ReaderCacheDir string // env: READER_CACHE_DIR (default .reader-cache)

	// Optional trace logs per URL processing (testing convenience)
	ReaderLogDir string // env: READER_LOG_DIR (default .reader-logs)
}

func FromEnv() (*Config, error) {
	cfg := &Config{
		Token:          os.Getenv("WECOM_TOKEN"),
		EncodingAESKey: os.Getenv("WECOM_ENCODING_AES_KEY"),
		ReceiveID:      os.Getenv("WECOM_RECEIVE_ID"),
		Port:           os.Getenv("PORT"),
		// Prefer LLM_*, but fall back to EXAMPLE_* for compatibility
		LLMBaseURL:     firstNonEmpty(os.Getenv("LLM_BASE_URL"), os.Getenv("EXAMPLE_BASE_URL")),
		LLMAPIKey:      firstNonEmpty(os.Getenv("LLM_API_KEY"), os.Getenv("EXAMPLE_API_KEY")),
		LLMModel:       firstNonEmpty(os.Getenv("LLM_MODEL"), os.Getenv("EXAMPLE_MODEL_NAME")),
		MCPHTTPURL:     os.Getenv("MCP_HTTP_URL"),
		MCPToolName:    os.Getenv("MCP_TOOL_NAME"),
		ReadwiseToken:  os.Getenv("READWISE_API_TOKEN"),
		ReaderCacheDir: os.Getenv("READER_CACHE_DIR"),
		ReaderLogDir:   os.Getenv("READER_LOG_DIR"),
	}
	// Optional temperature; if unset or invalid, keep nil to avoid sending the param
	if v := os.Getenv("LLM_TEMPERATURE"); v != "" {
		if f64, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.LLMTemperature = &f64
		}
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.MCPToolName == "" {
		cfg.MCPToolName = "http"
	}
	if cfg.ReaderCacheDir == "" {
		cfg.ReaderCacheDir = ".reader-cache"
	}
	if cfg.ReaderLogDir == "" {
		cfg.ReaderLogDir = ".reader-logs"
	}
	if cfg.Token == "" || cfg.EncodingAESKey == "" {
		return nil, errors.New("必须设置 WECOM_TOKEN 与 WECOM_ENCODING_AES_KEY")
	}
	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
