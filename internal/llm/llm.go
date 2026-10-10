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

// Request is one chat completion call.
type Request struct {
	Model       string
	Messages    []Message
	Tools       []ToolDef
	Temperature float64
	MaxTokens   int
}

// Response is the model's answer plus usage.
type Response struct {
	ID              string
	Model           string
	Message         Message
	FinishReason    string
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
}

// Client is anything that can complete a chat.
type Client interface {
	Chat(ctx context.Context, req Request) (Response, error)
	// Provider is the gen_ai.provider.name value, e.g. "openai".
	Provider() string
}
