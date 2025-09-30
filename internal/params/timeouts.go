package params

import "time"

// Centralized timeout controls (not env-configurable).
// Adjust here to change behavior across the system.

// PipelineTimeout caps the entire URL processing flow.
var PipelineTimeout = 10 * time.Minute

// StepTimeout applies to each major pipeline step: fetch, extract, save.
var StepTimeout = 5 * time.Minute

// HTTPClientTimeout is used by generic outbound HTTP clients (e.g., Readwise API).
var HTTPClientTimeout = StepTimeout

// MCPTransportTimeout bounds the MCP streamable HTTP transport layer.
var MCPTransportTimeout = StepTimeout
