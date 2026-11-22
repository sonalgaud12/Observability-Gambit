// Package llm is a deliberately small chat-completion abstraction: just enough
// for an agent that talks, calls tools, and gets retried.
package llm

import "context"

// Message is one chat turn.
type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// Reasoning is the model's visible thinking, when an OpenAI-compatible
	// server returns it (reasoning_content / reasoning). Never sent back.
	Reasoning string `json:"-"`
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Request is one chat completion call. Zero values mean "not sent".
type Request struct {
	Model       string
	Messages    []Message
	Tools       []ToolDef
	Temperature float64
	MaxTokens   int

	TopP           float64  // > 0 to send
	Seed           *int64   // nil = not sent
	StopSequences  []string // empty = not sent
	ReasoningLevel string   // e.g. "low"; "" = the client's default, if any
	// OutputType is the requested output modality ("text", "json"). Only set
	// when the request actually asks for a format; otherwise "".
	OutputType string

	// The prompt template this request was built from (gen_ai.prompt.*).
	PromptName    string
	PromptVersion string
}

// Response is the model's answer plus usage.
type Response struct {
	ID              string
	Model           string
	Message         Message
	FinishReason    string
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int // a subset of OutputTokens
	CacheReadTokens int // a subset of InputTokens
	// CacheWriteTokens is a subset of InputTokens. OpenAI does not report it;
	// some OpenAI-compatible servers (e.g. OpenRouter) do.
	CacheWriteTokens int
}

// Client is anything that can complete a chat.
type Client interface {
	Chat(ctx context.Context, req Request) (Response, error)
	// Provider is the gen_ai.provider.name value, e.g. "openai".
	Provider() string
}

// Endpoint is implemented by clients that call a remote server, so the
// instrumentation can set server.address and server.port.
type Endpoint interface {
	Endpoint() (host string, port int)
}

// Defaults is implemented by clients that add request settings of their own,
// so the instrumentation can record what was actually sent.
type Defaults interface {
	// DefaultReasoningLevel is sent when Request.ReasoningLevel is empty.
	DefaultReasoningLevel() string
}
