package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// anthropicVersion is the API version header every Messages request carries.
const anthropicVersion = "2023-06-01"

// DefaultAnthropicTimeout bounds one Messages call. It is deliberately longer
// than the OpenAI-compatible client's 3 minutes: the model thinks before it
// answers, and thinking counts against the same call.
const DefaultAnthropicTimeout = 10 * time.Minute

// Anthropic talks to Anthropic's native Messages API (POST /v1/messages).
//
// Why not the OpenAI-compatible endpoint? It returns no thinking text and no
// separate thinking-token count, so the reasoning attributes stay empty for
// Claude. The native API returns a summary of the model's reasoning as
// `thinking` blocks and reports the thinking tokens in
// usage.output_tokens_details.thinking_tokens.
type Anthropic struct {
	// BaseURL is the API origin, e.g. https://api.anthropic.com. A trailing
	// /v1 is accepted (https://api.anthropic.com/v1 is what the OpenAI-compatible
	// client was pointed at), so existing --base-url values keep working.
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	// Effort is sent as output_config.effort (low, medium, high, xhigh, max).
	// "" leaves it to the model's default (medium on Claude Haiku 5.5).
	Effort string
	// ThinkingDisplay is thinking.display. The API default is "omitted" (empty
	// thinking text) on the Claude 5 family, so this client asks for
	// "summarized" unless told otherwise.
	ThinkingDisplay string
}

// NewAnthropic returns a client with thinking summaries on and a 10 minute timeout.
func NewAnthropic(baseURL, apiKey string) *Anthropic {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &Anthropic{
		BaseURL:         strings.TrimRight(baseURL, "/"),
		APIKey:          apiKey,
		HTTP:            &http.Client{Timeout: DefaultAnthropicTimeout},
		ThinkingDisplay: "summarized",
	}
}

// SetTimeout replaces the per-call HTTP timeout. It is the whole exchange:
// sending the request, the model thinking and answering, and reading the reply.
func (c *Anthropic) SetTimeout(d time.Duration) {
	c.HTTP = &http.Client{Timeout: d}
}

func (c *Anthropic) Provider() string { return "anthropic" }

// Endpoint implements Endpoint.
func (c *Anthropic) Endpoint() (string, int) { return endpointOf(c.BaseURL) }

// DefaultReasoningLevel implements Defaults: the effort setting.
func (c *Anthropic) DefaultReasoningLevel() string { return c.Effort }

// messagesURL is BaseURL + /v1/messages, without doubling a /v1 already in BaseURL.
func (c *Anthropic) messagesURL() string {
	base := strings.TrimRight(c.BaseURL, "/")
	if u, err := url.Parse(base); err == nil && strings.HasSuffix(u.Path, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

type anTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// anBlock is a content block we build ourselves: text, tool_use, tool_result.
// Assistant blocks the API returned are sent back as raw JSON instead.
type anBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anThinking struct {
	Type    string `json:"type"`
	Display string `json:"display,omitempty"`
}

type anOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	System        string          `json:"system,omitempty"`
	Messages      []anMessage     `json:"messages"`
	Tools         []anTool        `json:"tools,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Thinking      *anThinking     `json:"thinking,omitempty"`
	OutputConfig  *anOutputConfig `json:"output_config,omitempty"`
}

type anResponseBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

type anResponse struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokensDetails      struct {
			ThinkingTokens int `json:"thinking_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Chat implements Client.
func (c *Anthropic) Chat(ctx context.Context, req Request) (Response, error) {
	if req.MaxTokens <= 0 {
		return Response{}, fmt.Errorf("anthropic: max_tokens is required (the Messages API has no default)")
	}
	body := anRequest{
		Model: req.Model, MaxTokens: req.MaxTokens, StopSequences: req.StopSequences,
		// Claude 5 models reject budget_tokens and disabled thinking, so the
		// only thinking setting is adaptive, with the summary switched on.
		Thinking: &anThinking{Type: "adaptive", Display: c.ThinkingDisplay},
	}
	effort := c.Effort
	if req.ReasoningLevel != "" {
		effort = req.ReasoningLevel
	}
	if effort != "" {
		body.OutputConfig = &anOutputConfig{Effort: effort}
	}
	if req.Temperature >= 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	if req.TopP > 0 {
		p := req.TopP
		body.TopP = &p
	}
	system, msgs, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return Response{}, err
	}
	body.System, body.Messages = system, msgs
	for _, t := range req.Tools {
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		body.Tools = append(body.Tools, anTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}

	// No HTML escaping: the thinking blocks go back byte for byte, and Go's
	// default would rewrite "<" inside them as \u003c.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.messagesURL(), &buf)
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if c.APIKey != "" {
		httpReq.Header.Set("x-api-key", c.APIKey)
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

	var ar anResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	if ar.Error != nil || ar.Type == "error" {
		typ, msg := "", ""
		if ar.Error != nil {
			typ, msg = ar.Error.Type, ar.Error.Message
		}
		return Response{}, fmt.Errorf("llm error (%s): %s", typ, msg)
	}
	var blocks []anResponseBlock
	if err := json.Unmarshal(ar.Content, &blocks); err != nil {
		return Response{}, fmt.Errorf("decode content: %w", err)
	}

	u := ar.Usage
	out := Response{
		ID: ar.ID, Model: ar.Model, FinishReason: ar.StopReason,
		// Anthropic's input_tokens leaves out the cached tokens; the
		// conventions count them in (cache read/write are subsets of input).
		InputTokens:      u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		OutputTokens:     u.OutputTokens,
		ReasoningTokens:  u.OutputTokensDetails.ThinkingTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		// The blocks exactly as returned, thinking signatures included, so
		// the next request can send them back unchanged.
		Message: Message{Role: "assistant", ProviderContent: ar.Content},
	}
	var text, thinking []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "thinking":
			if b.Thinking != "" {
				thinking = append(thinking, b.Thinking)
			}
		case "tool_use":
			args := string(b.Input)
			if args == "" || args == "null" {
				args = "{}"
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Arguments: args})
		}
	}
	out.Message.Content = strings.Join(text, "")
	out.Message.Reasoning = strings.Join(thinking, "\n\n")
	return out, nil
}

// toAnthropicMessages splits the chat into the top-level system prompt and the
// alternating user/assistant messages the API wants.
func toAnthropicMessages(ms []Message) (string, []anMessage, error) {
	var system []string
	var out []anMessage
	// Tool results must all come back in one user message, after the assistant
	// turn that asked for them.
	var results []anBlock
	flush := func() error {
		if len(results) == 0 {
			return nil
		}
		b, err := json.Marshal(results)
		if err != nil {
			return err
		}
		out = append(out, anMessage{Role: "user", Content: b})
		results = nil
		return nil
	}
	for _, m := range ms {
		if m.Role != "tool" {
			if err := flush(); err != nil {
				return "", nil, err
			}
		}
		switch m.Role {
		case "system":
			system = append(system, m.Content)
		case "tool":
			results = append(results, anBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
		case "assistant":
			if len(m.ProviderContent) > 0 {
				out = append(out, anMessage{Role: "assistant", Content: m.ProviderContent})
				continue
			}
			var blocks []anBlock
			if m.Content != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				in := json.RawMessage(tc.Arguments)
				if !json.Valid(in) || len(bytes.TrimSpace(in)) == 0 || bytes.TrimSpace(in)[0] != '{' {
					in = json.RawMessage("{}")
				}
				blocks = append(blocks, anBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: in})
			}
			if len(blocks) == 0 {
				continue // the API rejects an empty assistant message
			}
			b, err := json.Marshal(blocks)
			if err != nil {
				return "", nil, err
			}
			out = append(out, anMessage{Role: "assistant", Content: b})
		default:
			b, err := json.Marshal(m.Content)
			if err != nil {
				return "", nil, err
			}
			out = append(out, anMessage{Role: "user", Content: b})
		}
	}
	if err := flush(); err != nil {
		return "", nil, err
	}
	return strings.Join(system, "\n\n"), out, nil
}
