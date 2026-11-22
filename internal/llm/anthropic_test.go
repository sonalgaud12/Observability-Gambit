package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sonalgaud12/observability-gambit/internal/telemetry/telemetrytest"
)

// No test here calls the real API: every response is canned Messages API JSON
// served by httptest.

const thinkingAnswer = `{
  "id": "msg_01",
  "type": "message",
  "role": "assistant",
  "model": "claude-haiku-5-5",
  "stop_reason": "end_turn",
  "content": [
    {"type": "thinking", "thinking": "The centre is open, so e4.", "signature": "sig-abc"},
    {"type": "text", "text": "MOVE: e4"}
  ],
  "usage": {
    "input_tokens": 20,
    "cache_read_input_tokens": 70,
    "cache_creation_input_tokens": 10,
    "output_tokens": 120,
    "output_tokens_details": {"thinking_tokens": 90}
  }
}`

const toolUseAnswer = `{
  "id": "msg_02",
  "type": "message",
  "role": "assistant",
  "model": "claude-haiku-5-5",
  "stop_reason": "tool_use",
  "content": [
    {"type": "thinking", "thinking": "Let me list the legal moves.", "signature": "sig-tool"},
    {"type": "tool_use", "id": "toolu_1", "name": "legal_moves", "input": {"square": "e2"}}
  ],
  "usage": {"input_tokens": 30, "output_tokens": 50, "output_tokens_details": {"thinking_tokens": 40}}
}`

// serve answers every request with status and body, and keeps the requests.
func serve(t *testing.T, status int, body string) (*Anthropic, *[]map[string]any, *[]http.Header) {
	t.Helper()
	var bodies []map[string]any
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Errorf("bad body: %v", err)
		}
		bodies, headers = append(bodies, m), append(headers, r.Header.Clone())
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c := NewAnthropic(srv.URL, "test-key")
	return c, &bodies, &headers
}

func hello() Request {
	return Request{Model: "claude-haiku-5-5", Temperature: -1, MaxTokens: 4096,
		Messages: []Message{{Role: "system", Content: "You play chess."}, {Role: "user", Content: "hi"}}}
}

func TestAnthropicRequestShape(t *testing.T) {
	c, bodies, headers := serve(t, 200, thinkingAnswer)
	c.Effort = "high"
	req := hello()
	req.Tools = []ToolDef{{Name: "legal_moves", Description: "List moves.", Parameters: map[string]any{"type": "object"}}}
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got, h := (*bodies)[0], (*headers)[0]
	if h.Get("x-api-key") != "test-key" || h.Get("anthropic-version") != "2023-06-01" || h.Get("Authorization") != "" {
		t.Errorf("headers = %v", h)
	}
	if got["system"] != "You play chess." || got["max_tokens"] != float64(4096) || got["model"] != "claude-haiku-5-5" {
		t.Errorf("body = %v", got)
	}
	// Adaptive thinking with the summary on; never budget_tokens or disabled.
	th := got["thinking"].(map[string]any)
	if th["type"] != "adaptive" || th["display"] != "summarized" || th["budget_tokens"] != nil {
		t.Errorf("thinking = %v", th)
	}
	if oc := got["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Errorf("output_config = %v", oc)
	}
	if _, ok := got["temperature"]; ok {
		t.Errorf("temperature was sent for Temperature < 0: %v", got["temperature"])
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" || msgs[0].(map[string]any)["content"] != "hi" {
		t.Errorf("messages = %v", msgs)
	}
	tool := got["tools"].([]any)[0].(map[string]any)
	if tool["name"] != "legal_moves" || tool["input_schema"] == nil {
		t.Errorf("tool = %v", tool)
	}
}

func TestAnthropicOptionalSettings(t *testing.T) {
	c, bodies, _ := serve(t, 200, thinkingAnswer)
	req := hello()
	req.Temperature, req.TopP, req.StopSequences, req.ReasoningLevel = 1, 0.99, []string{"END"}, "low"
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got := (*bodies)[0]
	if got["temperature"] != float64(1) || got["top_p"] != 0.99 || got["stop_sequences"].([]any)[0] != "END" {
		t.Errorf("body = %v", got)
	}
	if got["output_config"].(map[string]any)["effort"] != "low" { // the request overrides the client
		t.Errorf("output_config = %v", got["output_config"])
	}
	// No effort anywhere: no output_config at all.
	c2, bodies2, _ := serve(t, 200, thinkingAnswer)
	if _, err := c2.Chat(context.Background(), hello()); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*bodies2)[0]["output_config"]; ok {
		t.Errorf("output_config sent with no effort: %v", (*bodies2)[0]["output_config"])
	}
}

func TestAnthropicMaxTokensIsRequired(t *testing.T) {
	c, bodies, _ := serve(t, 200, thinkingAnswer)
	req := hello()
	req.MaxTokens = 0
	if _, err := c.Chat(context.Background(), req); err == nil || len(*bodies) != 0 {
		t.Errorf("err = %v, requests = %d", err, len(*bodies))
	}
}

func TestAnthropicThinkingAnswer(t *testing.T) {
	c, _, _ := serve(t, 200, thinkingAnswer)
	resp, err := c.Chat(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "MOVE: e4" || resp.Message.Reasoning != "The centre is open, so e4." {
		t.Errorf("message = %+v", resp.Message)
	}
	if resp.ID != "msg_01" || resp.Model != "claude-haiku-5-5" || resp.FinishReason != "end_turn" {
		t.Errorf("response = %+v", resp)
	}
	// Anthropic's input_tokens leaves the cached tokens out; ours include them.
	if resp.InputTokens != 100 || resp.CacheReadTokens != 70 || resp.CacheWriteTokens != 10 {
		t.Errorf("input tokens = %d (read %d, write %d)", resp.InputTokens, resp.CacheReadTokens, resp.CacheWriteTokens)
	}
	if resp.OutputTokens != 120 || resp.ReasoningTokens != 90 {
		t.Errorf("output = %d, reasoning = %d", resp.OutputTokens, resp.ReasoningTokens)
	}
}

func TestAnthropicWithoutThinkingDetails(t *testing.T) {
	// Older or proxied responses carry no output_tokens_details: no reasoning
	// count is invented.
	c, _, _ := serve(t, 200, `{"id":"m","model":"x","stop_reason":"end_turn","content":[{"type":"text","text":"e4"}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	resp, err := c.Chat(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	if resp.ReasoningTokens != 0 || resp.Message.Reasoning != "" || resp.Message.Content != "e4" {
		t.Errorf("response = %+v", resp)
	}
}

func TestAnthropicOmittedThinkingKeepsTheBlock(t *testing.T) {
	// display "omitted" returns an empty thinking string and a signature. No
	// reasoning text, but the block still has to go back unchanged.
	answer := `{"id":"m","model":"x","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"","signature":"sig-empty"},{"type":"text","text":"MOVE: d4"}],"usage":{"input_tokens":3,"output_tokens":9,"output_tokens_details":{"thinking_tokens":6}}}`
	c, bodies, _ := serve(t, 200, answer)
	resp, err := c.Chat(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Reasoning != "" || resp.ReasoningTokens != 6 {
		t.Errorf("response = %+v", resp)
	}
	req := hello()
	req.Messages = append(req.Messages, resp.Message, Message{Role: "user", Content: "again"})
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	sent := (*bodies)[1]["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if sent[0].(map[string]any)["signature"] != "sig-empty" {
		t.Errorf("thinking block not sent back: %v", sent)
	}
}

// A tool round trip: the assistant turn comes back with its thinking block
// and tool_use, and the next request carries them unchanged, followed by one
// user message holding every tool_result.
func TestAnthropicToolRoundTrip(t *testing.T) {
	c, bodies, _ := serve(t, 200, toolUseAnswer)
	req := hello()
	req.Tools = []ToolDef{{Name: "legal_moves", Parameters: map[string]any{"type": "object"}}}
	resp, err := c.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "tool_use" || resp.Message.Reasoning != "Let me list the legal moves." {
		t.Errorf("response = %+v", resp)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Name != "legal_moves" || tc.Arguments != `{"square": "e2"}` {
		t.Errorf("tool call = %+v", tc)
	}

	req.Messages = append(req.Messages, resp.Message,
		Message{Role: "tool", ToolCallID: "toolu_1", Content: "e3, e4"},
		Message{Role: "tool", ToolCallID: "toolu_other", Content: "nothing"})
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	msgs := (*bodies)[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want user, assistant, user(tool results); got %d: %v", len(msgs), msgs)
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Errorf("role = %v", asst["role"])
	}
	blocks := asst["content"].([]any)
	if len(blocks) != 2 || blocks[0].(map[string]any)["type"] != "thinking" || blocks[0].(map[string]any)["signature"] != "sig-tool" ||
		blocks[1].(map[string]any)["type"] != "tool_use" {
		t.Errorf("assistant content = %v", blocks)
	}
	results := msgs[2].(map[string]any)["content"].([]any)
	if msgs[2].(map[string]any)["role"] != "user" || len(results) != 2 {
		t.Fatalf("tool results = %v", msgs[2])
	}
	r0 := results[0].(map[string]any)
	if r0["type"] != "tool_result" || r0["tool_use_id"] != "toolu_1" || r0["content"] != "e3, e4" {
		t.Errorf("result 0 = %v", r0)
	}
}

// The raw content is sent back byte for byte, so nothing in a block can be
// altered by a decode and re-encode.
func TestAnthropicSendsContentBackUnchanged(t *testing.T) {
	answer := `{"id":"m","model":"x","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"a < b","signature":"s","extra_field":{"k":1}},{"type":"text","text":"x"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
		io.WriteString(w, answer)
	}))
	defer srv.Close()
	c := NewAnthropic(srv.URL, "k")
	resp, _ := c.Chat(context.Background(), hello())
	req := hello()
	req.Messages = append(req.Messages, resp.Message)
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent, `[{"type":"thinking","thinking":"a < b","signature":"s","extra_field":{"k":1}},{"type":"text","text":"x"}]`) {
		t.Errorf("assistant content was altered: %s", sent)
	}
}

func TestAnthropicHistoryWithoutProviderContent(t *testing.T) {
	// A history that did not come from this client (the mock, another model)
	// is rebuilt from text and tool calls.
	c, bodies, _ := serve(t, 200, thinkingAnswer)
	req := hello()
	req.Messages = append(req.Messages,
		Message{Role: "assistant", Content: "checking", ToolCalls: []ToolCall{{ID: "t1", Name: "threats", Arguments: "not json"}}},
		Message{Role: "tool", ToolCallID: "t1", Content: "none"},
		Message{Role: "assistant", Content: "MOVE: e4"},
		Message{Role: "user", Content: "illegal"})
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	msgs := (*bodies)[0]["messages"].([]any)
	b0 := msgs[1].(map[string]any)["content"].([]any)
	if b0[0].(map[string]any)["text"] != "checking" || b0[1].(map[string]any)["type"] != "tool_use" ||
		len(b0[1].(map[string]any)["input"].(map[string]any)) != 0 {
		t.Errorf("assistant blocks = %v", b0)
	}
	if len(msgs) != 5 {
		t.Errorf("messages = %d", len(msgs))
	}
}

func TestAnthropicRefusal(t *testing.T) {
	// A refusal is a normal 200 with stop_reason "refusal": no error, the
	// reason comes through for gen_ai.response.finish_reasons.
	c, _, _ := serve(t, 200, `{"id":"m","model":"claude-haiku-5-5","stop_reason":"refusal","stop_details":{"type":"refusal","category":"general_harms"},"content":[],"usage":{"input_tokens":5,"output_tokens":0}}`)
	resp, err := c.Chat(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "refusal" || resp.Message.Content != "" || len(resp.Message.ToolCalls) != 0 {
		t.Errorf("response = %+v", resp)
	}
}

func TestAnthropicMaxTokensStop(t *testing.T) {
	// Thinking can use the whole cap: stop_reason max_tokens, no text.
	c, _, _ := serve(t, 200, `{"id":"m","model":"x","stop_reason":"max_tokens","content":[{"type":"thinking","thinking":"hmm","signature":"s"}],"usage":{"input_tokens":5,"output_tokens":1024,"output_tokens_details":{"thinking_tokens":1024}}}`)
	resp, err := c.Chat(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "max_tokens" || resp.Message.Content != "" || resp.Message.Reasoning != "hmm" {
		t.Errorf("response = %+v", resp)
	}
}

func TestAnthropicErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking.type.enabled is not supported"}}`},
		{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`},
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
	} {
		c, _, _ := serve(t, tc.status, tc.body)
		_, err := c.Chat(context.Background(), hello())
		var he *HTTPError
		if !errors.As(err, &he) || he.Status != tc.status || !strings.Contains(he.Body, "_error") {
			t.Errorf("status %d: err = %v", tc.status, err)
		}
	}
	// An error object inside a 200, and a body that is not JSON.
	c, _, _ := serve(t, 200, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	if _, err := c.Chat(context.Background(), hello()); err == nil || !strings.Contains(err.Error(), "overloaded_error") {
		t.Errorf("err = %v", err)
	}
	c, _, _ = serve(t, 200, `<html>`)
	if _, err := c.Chat(context.Background(), hello()); err == nil {
		t.Error("garbage body accepted")
	}
}

func TestAnthropicTimeout(t *testing.T) {
	if NewAnthropic("", "").HTTP.Timeout != 10*time.Minute {
		t.Errorf("default timeout = %v", NewAnthropic("", "").HTTP.Timeout)
	}
	if NewOpenAICompatible("", "", "").HTTP.Timeout != 3*time.Minute {
		t.Error("the OpenAI-compatible default changed")
	}
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	c := NewAnthropic(srv.URL, "k")
	c.SetTimeout(50 * time.Millisecond)
	if _, err := c.Chat(context.Background(), hello()); err == nil {
		t.Error("no timeout error")
	}
}

func TestAnthropicURLAndEndpoint(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.anthropic.com":     "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/":    "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1":  "https://api.anthropic.com/v1/messages",
		"https://gw.example.com/claude": "https://gw.example.com/claude/v1/messages",
	} {
		if got := NewAnthropic(base, "").messagesURL(); got != want {
			t.Errorf("%s -> %s, want %s", base, got, want)
		}
	}
	host, port := NewAnthropic("", "").Endpoint()
	if host != "api.anthropic.com" || port != 443 {
		t.Errorf("endpoint = %s:%d", host, port)
	}
	if c := NewAnthropic("", ""); c.Provider() != "anthropic" {
		t.Errorf("provider = %s", c.Provider())
	}
}

// Through the instrumented wrapper: the thinking summary, the thinking-token
// count, the cache counts and the provider land on the span; refusals and
// errors are visible too.
func TestAnthropicTelemetry(t *testing.T) {
	rec := telemetrytest.Install(t)
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT", "SPAN_ONLY")
	c, _, _ := serve(t, 200, thinkingAnswer)
	c.Effort = "high"
	if _, err := Instrument(c).Chat(context.Background(), hello()); err != nil {
		t.Fatal(err)
	}
	spans := rec.SpanAttrs("chat claude-haiku-5-5")
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	a := spans[0]
	if a["gen_ai.provider.name"].AsString() != "anthropic" || a["gen_ai.request.reasoning.level"].AsString() != "high" {
		t.Errorf("attrs = %v", a)
	}
	if a["gen_ai.usage.reasoning.output_tokens"].AsInt64() != 90 || a["gen_ai.usage.output_tokens"].AsInt64() != 120 ||
		a["gen_ai.usage.input_tokens"].AsInt64() != 100 || a["gen_ai.usage.cache_read.input_tokens"].AsInt64() != 70 ||
		a["gen_ai.usage.cache_write.input_tokens"].AsInt64() != 10 {
		t.Errorf("usage attrs = %v", a)
	}
	if got := a["gen_ai.response.finish_reasons"].AsStringSlice(); len(got) != 1 || got[0] != "end_turn" {
		t.Errorf("finish reasons = %v", got)
	}
	if _, ok := a["server.address"]; !ok {
		t.Error("no server.address")
	}
	var out []struct {
		Parts []struct{ Type, Content string }
	}
	if err := json.Unmarshal([]byte(a["gen_ai.output.messages"].AsString()), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Parts) != 2 || out[0].Parts[0].Type != "reasoning" || out[0].Parts[0].Content != "The centre is open, so e4." ||
		out[0].Parts[1].Type != "text" {
		t.Errorf("output messages = %+v", out)
	}

	// An HTTP error is recorded as error.type = status.
	rec2 := telemetrytest.Install(t)
	bad, _, _ := serve(t, 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	if _, err := Instrument(bad).Chat(context.Background(), hello()); err == nil {
		t.Fatal("expected an error")
	}
	if e := rec2.SpanAttrs("chat claude-haiku-5-5")[0]["error.type"].AsString(); e != "529" {
		t.Errorf("error.type = %q", e)
	}
}
