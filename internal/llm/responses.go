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

// DefaultResponsesTimeout bounds one Responses call. Like the Anthropic
// client's, it is longer than the Chat Completions client's 3 minutes: the
// model reasons before it answers, and the reasoning counts against the same
// call.
const DefaultResponsesTimeout = 10 * time.Minute

// OpenAIResponses talks to OpenAI's Responses API (POST /v1/responses).
//
// Why not Chat Completions? That API reports how many reasoning tokens a model
// spent but never the reasoning itself. The Responses API returns a summary of
// it as `reasoning` output items, which this client puts in Message.Reasoning,
// so it shows up as a `reasoning` part in the trace.
//
// The client is stateless: every request sends `store: false` and the whole
// conversation, and asks for `reasoning.encrypted_content` so the model's
// reasoning items can be sent back on the next request (see
// Message.ProviderContent). The alternative, `previous_response_id`, needs
// `store: true` (OpenAI keeps every response for 30 days) and chains requests
// server side, which does not fit an agent that rebuilds its message list for
// each retry. Stateless also works for organisations with zero data retention.
type OpenAIResponses struct {
	BaseURL string // e.g. https://api.openai.com/v1
	APIKey  string
	HTTP    *http.Client

	// Effort is sent as reasoning.effort (none, minimal, low, medium, high,
	// xhigh). "" leaves it to the model's default.
	Effort string
	// Summary is reasoning.summary. "auto" lets each model pick the most
	// detailed summarizer it supports; "" does not ask for a summary.
	Summary string
}

// NewOpenAIResponses returns a client that asks for reasoning summaries and
// has a 10 minute timeout.
func NewOpenAIResponses(baseURL, apiKey string) *OpenAIResponses {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIResponses{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: DefaultResponsesTimeout},
		Summary: "auto",
	}
}

// SetTimeout replaces the per-call HTTP timeout. It is the whole exchange:
// sending the request, the model reasoning and answering, and reading the reply.
func (c *OpenAIResponses) SetTimeout(d time.Duration) {
	c.HTTP = &http.Client{Timeout: d}
}

// Provider is gen_ai.provider.name. It is "openai" for this client and for the
// Chat Completions one: the span is about the provider, not the API used.
func (c *OpenAIResponses) Provider() string { return "openai" }

// Endpoint implements Endpoint.
func (c *OpenAIResponses) Endpoint() (string, int) { return endpointOf(c.BaseURL) }

// DefaultReasoningLevel implements Defaults: the effort setting.
func (c *OpenAIResponses) DefaultReasoningLevel() string { return c.Effort }

// rsTool is a function tool. strict is false so the schemas the Chat
// Completions client sends keep working: the Responses API defaults it to true,
// which demands additionalProperties:false and every property required.
type rsTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

// Input items we build ourselves. Output items the API returned are sent back
// as raw JSON instead (Message.ProviderContent).
type rsInputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type rsFunctionCall struct {
	Type      string `json:"type"` // function_call
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type rsFunctionCallOutput struct {
	Type   string `json:"type"` // function_call_output
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type rsReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type rsTextFormat struct {
	Type string `json:"type"`
}

type rsText struct {
	Format rsTextFormat `json:"format"`
}

type rsRequest struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           []json.RawMessage `json:"input"`
	Tools           []rsTool          `json:"tools,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	Reasoning       *rsReasoning      `json:"reasoning,omitempty"`
	Text            *rsText           `json:"text,omitempty"`
	// Store is always sent: false is not the zero value of the API's default.
	Store   bool     `json:"store"`
	Include []string `json:"include,omitempty"`
}

// rsOutputItem is one element of a response's output array. Only the fields
// this client reads are listed; the item itself is kept as raw JSON.
type rsOutputItem struct {
	Type      string `json:"type"` // reasoning | message | function_call | ...
	Status    string `json:"status"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Summary   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type    string `json:"type"` // output_text | refusal
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

type rsResponse struct {
	ID                string          `json:"id"`
	Status            string          `json:"status"`
	Model             string          `json:"model"`
	Output            json.RawMessage `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Chat implements Client.
func (c *OpenAIResponses) Chat(ctx context.Context, req Request) (Response, error) {
	body := rsRequest{
		Model: req.Model, MaxOutputTokens: req.MaxTokens,
		Store:   false,
		Include: []string{"reasoning.encrypted_content"},
	}
	// The Responses API has no seed or stop parameters; Request.Seed and
	// Request.StopSequences are not sent.
	effort := c.Effort
	if req.ReasoningLevel != "" {
		effort = req.ReasoningLevel
	}
	if effort != "" || c.Summary != "" {
		r := &rsReasoning{Effort: effort, Summary: c.Summary}
		if effort == "none" {
			r.Summary = "" // nothing to summarize
		}
		if r.Effort != "" || r.Summary != "" {
			body.Reasoning = r
		}
	}
	if req.Temperature >= 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	if req.TopP > 0 {
		p := req.TopP
		body.TopP = &p
	}
	if req.OutputType == "json" {
		body.Text = &rsText{Format: rsTextFormat{Type: "json_object"}}
	}
	instructions, input, err := toResponsesInput(req.Messages)
	if err != nil {
		return Response{}, err
	}
	body.Instructions, body.Input = instructions, input
	for _, t := range req.Tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		body.Tools = append(body.Tools, rsTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: params})
	}

	// No HTML escaping: the reasoning items go back as they came.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/responses", &buf)
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

	var rr rsResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	// A 200 can still carry a failed response.
	if rr.Error != nil {
		typ := rr.Error.Code
		if typ == "" {
			typ = rr.Error.Type
		}
		return Response{}, fmt.Errorf("llm error (%s): %s", typ, rr.Error.Message)
	}
	switch rr.Status {
	case "completed", "incomplete":
	case "failed", "cancelled":
		return Response{}, fmt.Errorf("llm response %s", rr.Status)
	default: // queued, in_progress: we never ask for background mode
		return Response{}, fmt.Errorf("llm response has unexpected status %q", rr.Status)
	}
	var items []rsOutputItem
	if err := json.Unmarshal(rr.Output, &items); err != nil {
		return Response{}, fmt.Errorf("decode output: %w", err)
	}

	u := rr.Usage
	out := Response{
		ID: rr.ID, Model: rr.Model,
		// input_tokens already includes the cached tokens, as the
		// conventions count them.
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
		CacheReadTokens: u.InputTokensDetails.CachedTokens,
		Message:         Message{Role: "assistant"},
	}
	var text, summaries []string
	for _, it := range items {
		switch it.Type {
		case "reasoning":
			for _, s := range it.Summary {
				if s.Text != "" {
					summaries = append(summaries, s.Text)
				}
			}
		case "message":
			for _, ct := range it.Content {
				switch ct.Type {
				case "output_text":
					text = append(text, ct.Text)
				case "refusal":
					text = append(text, ct.Refusal)
				}
			}
		case "function_call":
			if it.Status != "" && it.Status != "completed" {
				continue // cut off mid-call: the arguments are not usable
			}
			args := it.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: it.CallID, Name: it.Name, Arguments: args})
		}
	}
	out.Message.Content = strings.Join(text, "")
	out.Message.Reasoning = strings.Join(summaries, "\n\n")

	out.FinishReason = responsesFinishReason(rr, len(out.Message.ToolCalls) > 0)
	if rr.Status == "completed" {
		// The items exactly as returned, reasoning and its encrypted content
		// included, so the next request can send them back unchanged. Not kept
		// for an incomplete response: it may end in a reasoning item or a
		// half-written call, which the API refuses to take back.
		out.Message.ProviderContent = rr.Output
	}
	return out, nil
}

// responsesFinishReason maps the response status to the finish reasons the
// Chat Completions client reports (stop, tool_calls, length, content_filter),
// so the agent, the dataset and dashboards read both the same way.
func responsesFinishReason(rr rsResponse, toolCalls bool) string {
	if rr.Status == "incomplete" {
		reason := ""
		if rr.IncompleteDetails != nil {
			reason = rr.IncompleteDetails.Reason
		}
		switch reason {
		case "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		case "":
			return "incomplete"
		}
		return reason
	}
	if toolCalls {
		return "tool_calls"
	}
	return "stop"
}

// toResponsesInput splits the chat into the top-level instructions and the
// input items the API wants.
func toResponsesInput(ms []Message) (string, []json.RawMessage, error) {
	var instructions []string
	input := []json.RawMessage{}
	add := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		input = append(input, b)
		return nil
	}
	for _, m := range ms {
		switch m.Role {
		case "system":
			instructions = append(instructions, m.Content)
		case "tool":
			if err := add(rsFunctionCallOutput{Type: "function_call_output", CallID: m.ToolCallID, Output: m.Content}); err != nil {
				return "", nil, err
			}
		case "assistant":
			if pc := bytes.TrimSpace(m.ProviderContent); len(pc) > 0 {
				var items []json.RawMessage
				if err := json.Unmarshal(pc, &items); err != nil {
					return "", nil, fmt.Errorf("decode provider content: %w", err)
				}
				input = append(input, items...)
				continue
			}
			if m.Content != "" { // the API rejects an empty assistant message
				if err := add(rsInputMessage{Role: "assistant", Content: m.Content}); err != nil {
					return "", nil, err
				}
			}
			for _, tc := range m.ToolCalls {
				args := tc.Arguments
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				if err := add(rsFunctionCall{Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: args}); err != nil {
					return "", nil, err
				}
			}
		default:
			if err := add(rsInputMessage{Role: "user", Content: m.Content}); err != nil {
				return "", nil, err
			}
		}
	}
	return strings.Join(instructions, "\n\n"), input, nil
}
