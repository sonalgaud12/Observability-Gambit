package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatible talks to any server that implements the OpenAI
// /chat/completions API: OpenAI itself, Ollama (/v1), vLLM, LM Studio,
// OpenRouter, and others. That keeps the agent vendor-neutral.
type OpenAICompatible struct {
	BaseURL      string // e.g. https://api.openai.com/v1 or http://localhost:11434/v1
	APIKey       string
	ProviderName string // gen_ai.provider.name, e.g. "openai", "ollama"
	HTTP         *http.Client
}

// NewOpenAICompatible returns a client with sane defaults.
func NewOpenAICompatible(baseURL, apiKey, provider string) *OpenAICompatible {
	if provider == "" {
		provider = "openai"
	}
	return &OpenAICompatible{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		APIKey:       apiKey,
		ProviderName: provider,
		HTTP:         &http.Client{Timeout: 3 * time.Minute},
	}
}

func (c *OpenAICompatible) Provider() string { return c.ProviderName }

type oaTool struct {
	Type     string  `json:"type"`
	Function ToolDef `json:"function"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaRequest struct {
	Model       string      `json:"model"`
	Messages    []oaMessage `json:"messages"`
	Tools       []oaTool    `json:"tools,omitempty"`
	Temperature *float64    `json:"temperature,omitempty"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
}

type oaResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens            int `json:"prompt_tokens"`
		CompletionTokens        int `json:"completion_tokens"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// HTTPError carries the status code so callers can set error.type.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("llm http %d: %s", e.Status, e.Body) }

// Chat implements Client.
func (c *OpenAICompatible) Chat(ctx context.Context, req Request) (Response, error) {
	body := oaRequest{Model: req.Model, MaxTokens: req.MaxTokens}
	if req.Temperature >= 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	for _, m := range req.Messages {
		om := oaMessage{Role: m.Role, ToolCallID: m.ToolCallID}
		content := m.Content
		om.Content = &content
		for _, tc := range m.ToolCalls {
			otc := oaToolCall{ID: tc.ID, Type: "function"}
			otc.Function.Name = tc.Name
			otc.Function.Arguments = tc.Arguments
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		body.Messages = append(body.Messages, om)
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, oaTool{Type: "function", Function: t})
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return Response{}, &HTTPError{Status: resp.StatusCode, Body: truncate(string(raw), 500)}
	}

	var or oaResponse
	if err := json.Unmarshal(raw, &or); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	if or.Error != nil {
		return Response{}, fmt.Errorf("llm error (%s): %s", or.Error.Type, or.Error.Message)
	}
	if len(or.Choices) == 0 {
		return Response{}, fmt.Errorf("llm returned no choices")
	}

	ch := or.Choices[0]
	out := Response{
		ID:              or.ID,
		Model:           or.Model,
		FinishReason:    ch.FinishReason,
		InputTokens:     or.Usage.PromptTokens,
		OutputTokens:    or.Usage.CompletionTokens,
		ReasoningTokens: or.Usage.CompletionTokensDetails.ReasoningTokens,
		Message:         Message{Role: "assistant"},
	}
	if ch.Message.Content != nil {
		out.Message.Content = *ch.Message.Content
	}
	for _, tc := range ch.Message.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
		})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
