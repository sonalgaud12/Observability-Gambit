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

// Canned Responses API bodies, shaped like real replies (ids and the
// encrypted blob are made up). No test calls the real API.

// A reasoning model answering in text: a reasoning item with a summary, then
// the message.
const rsAnswer = `{
  "id": "resp_1", "object": "response", "status": "completed", "model": "gpt-5.4-mini-2026-03-17",
  "output": [
    {"id": "rs_1", "type": "reasoning", "encrypted_content": "gAAAA-blob",
     "summary": [
       {"type": "summary_text", "text": "**Checking 391** It is 17 times 23."},
       {"type": "summary_text", "text": "**Answering** So it is not prime."}
     ]},
    {"id": "msg_1", "type": "message", "status": "completed", "role": "assistant",
     "content": [{"type": "output_text", "text": "391 is not prime.", "annotations": []}]}
  ],
  "usage": {"input_tokens": 120, "input_tokens_details": {"cached_tokens": 64},
            "output_tokens": 55, "output_tokens_details": {"reasoning_tokens": 33}, "total_tokens": 175}
}`

// The model asks for a tool: reasoning item, then a function call.
const rsToolCall = `{
  "id": "resp_2", "status": "completed", "model": "gpt-5.4-mini-2026-03-17",
  "output": [
    {"id": "rs_2", "type": "reasoning", "encrypted_content": "gAAAA-blob2", "summary": []},
    {"id": "fc_2", "type": "function_call", "status": "completed", "call_id": "call_abc",
     "name": "legal_moves", "arguments": "{\"square\":\"e2\"}"}
  ],
  "usage": {"input_tokens": 90, "output_tokens": 20, "output_tokens_details": {"reasoning_tokens": 12}}
}`

// Out of output tokens while reasoning.
const rsIncomplete = `{
  "id": "resp_3", "status": "incomplete", "incomplete_details": {"reason": "max_output_tokens"},
  "model": "gpt-5.4-mini-2026-03-17",
  "output": [
    {"id": "rs_3", "type": "reasoning", "encrypted_content": "gAAAA-blob3",
     "summary": [{"type": "summary_text", "text": "Still thinking about the opening."}]}
  ],
  "usage": {"input_tokens": 80, "output_tokens": 256, "output_tokens_details": {"reasoning_tokens": 256}}
}`

// serveResponses answers every request with status and body, and records the requests.
func serveResponses(t *testing.T, status int, body string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		got = append(got, m)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func newResponses(srv *httptest.Server) *OpenAIResponses {
	return NewOpenAIResponses(srv.URL+"/v1", "k")
}

func TestResponsesReasoningSummaryAndUsage(t *testing.T) {
	srv, _ := serveResponses(t, 200, rsAnswer)
	resp, err := newResponses(srv).Chat(context.Background(), Request{
		Model: "gpt-5.4-mini", Temperature: -1, MaxTokens: 800,
		Messages: []Message{{Role: "user", Content: "Is 391 prime?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "391 is not prime." {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if want := "**Checking 391** It is 17 times 23.\n\n**Answering** So it is not prime."; resp.Message.Reasoning != want {
		t.Errorf("reasoning = %q, want %q", resp.Message.Reasoning, want)
	}
	if resp.ID != "resp_1" || resp.Model != "gpt-5.4-mini-2026-03-17" || resp.FinishReason != "stop" {
		t.Errorf("response = %+v", resp)
	}
	if resp.InputTokens != 120 || resp.OutputTokens != 55 || resp.ReasoningTokens != 33 || resp.CacheReadTokens != 64 || resp.CacheWriteTokens != 0 {
		t.Errorf("usage = %+v", resp)
	}
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("tool calls = %+v", resp.Message.ToolCalls)
	}
	// The output items are kept, encrypted reasoning included, to send back.
	if !strings.Contains(string(resp.Message.ProviderContent), "gAAAA-blob") {
		t.Errorf("provider content = %s", resp.Message.ProviderContent)
	}
}

func TestResponsesRequestShape(t *testing.T) {
	srv, got := serveResponses(t, 200, rsAnswer)
	c := newResponses(srv)
	c.Effort = "low"
	_, err := c.Chat(context.Background(), Request{
		Model: "gpt-5.4-mini", Temperature: -1, MaxTokens: 800,
		Messages: []Message{
			{Role: "system", Content: "You play chess."},
			{Role: "user", Content: "Your move."},
		},
		Tools: []ToolDef{{Name: "legal_moves", Description: "List legal moves.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"square": map[string]any{"type": "string"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := (*got)[0]
	if b["model"] != "gpt-5.4-mini" || b["instructions"] != "You play chess." || b["max_output_tokens"] != float64(800) {
		t.Errorf("body = %v", b)
	}
	// Stateless: nothing stored, and the reasoning comes back encrypted.
	if b["store"] != false {
		t.Errorf("store = %v, want false", b["store"])
	}
	if inc, _ := b["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", b["include"])
	}
	if r, _ := b["reasoning"].(map[string]any); r["effort"] != "low" || r["summary"] != "auto" {
		t.Errorf("reasoning = %v", b["reasoning"])
	}
	for _, k := range []string{"temperature", "top_p", "text", "max_tokens", "messages", "seed", "stop"} {
		if _, ok := b[k]; ok {
			t.Errorf("%s was sent: %v", k, b[k])
		}
	}
	in, _ := b["input"].([]any)
	if len(in) != 1 {
		t.Fatalf("input = %v (the system prompt belongs in instructions)", b["input"])
	}
	if m, _ := in[0].(map[string]any); m["role"] != "user" || m["content"] != "Your move." {
		t.Errorf("input[0] = %v", in[0])
	}
	tools, _ := b["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", b["tools"])
	}
	// The flat Responses shape, with strict off so loose schemas still work.
	if tl, _ := tools[0].(map[string]any); tl["type"] != "function" || tl["name"] != "legal_moves" || tl["strict"] != false || tl["parameters"] == nil {
		t.Errorf("tool = %v", tools[0])
	}
}

func TestResponsesOptionalRequestFields(t *testing.T) {
	srv, got := serveResponses(t, 200, rsAnswer)
	c := newResponses(srv)
	c.Effort = "low"
	_, err := c.Chat(context.Background(), Request{
		Model: "m", Temperature: 0.3, MaxTokens: 100, TopP: 0.9, OutputType: "json",
		ReasoningLevel: "high", // the request wins over the client's default
		Messages:       []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := (*got)[0]
	if b["temperature"] != 0.3 || b["top_p"] != 0.9 {
		t.Errorf("sampling = %v %v", b["temperature"], b["top_p"])
	}
	if r, _ := b["reasoning"].(map[string]any); r["effort"] != "high" {
		t.Errorf("reasoning = %v", b["reasoning"])
	}
	if tx, _ := b["text"].(map[string]any); tx["format"].(map[string]any)["type"] != "json_object" {
		t.Errorf("text = %v", b["text"])
	}
}

func TestResponsesNoSummaryWhenReasoningIsOff(t *testing.T) {
	srv, got := serveResponses(t, 200, rsAnswer)
	c := newResponses(srv)
	c.Effort = "none"
	if _, err := c.Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if r, _ := (*got)[0]["reasoning"].(map[string]any); r["effort"] != "none" || r["summary"] != nil {
		t.Errorf("reasoning = %v", (*got)[0]["reasoning"])
	}

	// And with no effort and no summary the field is left out entirely.
	srv2, got2 := serveResponses(t, 200, rsAnswer)
	c2 := newResponses(srv2)
	c2.Summary = ""
	if _, err := c2.Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*got2)[0]["reasoning"]; ok {
		t.Errorf("reasoning was sent: %v", (*got2)[0]["reasoning"])
	}
}

func TestResponsesFunctionCallRoundTrip(t *testing.T) {
	srv, got := serveResponses(t, 200, rsToolCall)
	c := newResponses(srv)
	first, err := c.Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{
		{Role: "system", Content: "sys"}, {Role: "user", Content: "move?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if first.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q", first.FinishReason)
	}
	want := []ToolCall{{ID: "call_abc", Name: "legal_moves", Arguments: `{"square":"e2"}`}}
	if len(first.Message.ToolCalls) != 1 || first.Message.ToolCalls[0] != want[0] {
		t.Fatalf("tool calls = %+v", first.Message.ToolCalls)
	}

	// Second turn: the assistant turn (as the agent keeps it) plus the tool result.
	_, err = c.Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{
		{Role: "system", Content: "sys"}, {Role: "user", Content: "move?"},
		first.Message,
		{Role: "tool", ToolCallID: "call_abc", Content: "e3, e4"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := (*got)[1]["input"].([]any)
	if len(in) != 4 {
		t.Fatalf("input = %v", in)
	}
	item := func(i int) map[string]any { m, _ := in[i].(map[string]any); return m }
	// The reasoning item goes back untouched, ahead of its function call.
	if item(1)["type"] != "reasoning" || item(1)["id"] != "rs_2" || item(1)["encrypted_content"] != "gAAAA-blob2" {
		t.Errorf("reasoning item = %v", in[1])
	}
	if item(2)["type"] != "function_call" || item(2)["call_id"] != "call_abc" || item(2)["arguments"] != `{"square":"e2"}` {
		t.Errorf("function_call item = %v", in[2])
	}
	if item(3)["type"] != "function_call_output" || item(3)["call_id"] != "call_abc" || item(3)["output"] != "e3, e4" {
		t.Errorf("function_call_output item = %v", in[3])
	}
}

// An assistant turn that did not come from this client (no provider content)
// still becomes valid input: text as a message, calls as function_call items.
func TestResponsesInputFromPlainMessages(t *testing.T) {
	instr, in, err := toResponsesInput([]Message{
		{Role: "system", Content: "a"}, {Role: "system", Content: "b"},
		{Role: "user", Content: "u"},
		{Role: "assistant", Content: "thinking out loud", ToolCalls: []ToolCall{{ID: "c1", Name: "t", Arguments: ""}}},
		{Role: "tool", ToolCallID: "c1", Content: "r"},
		{Role: "assistant"}, // empty: dropped, the API rejects it
		{Role: "user", Content: "again"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if instr != "a\n\nb" {
		t.Errorf("instructions = %q", instr)
	}
	var kinds []string
	for _, raw := range in {
		var m map[string]any
		json.Unmarshal(raw, &m)
		k, _ := m["type"].(string)
		if k == "" {
			k, _ = m["role"].(string)
		}
		kinds = append(kinds, k)
	}
	if got := strings.Join(kinds, ","); got != "user,assistant,function_call,function_call_output,user" {
		t.Errorf("items = %s", got)
	}
	var call map[string]any
	json.Unmarshal(in[2], &call)
	if call["arguments"] != "{}" {
		t.Errorf("empty arguments = %v, want {}", call["arguments"])
	}
}

func TestResponsesIncompleteIsALengthStop(t *testing.T) {
	srv, _ := serveResponses(t, 200, rsIncomplete)
	resp, err := newResponses(srv).Chat(context.Background(), Request{Model: "m", Temperature: -1, MaxTokens: 256,
		Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	// "length", as Chat Completions says it, so the agent flags the move.
	if resp.FinishReason != "length" {
		t.Errorf("finish reason = %q", resp.FinishReason)
	}
	if resp.Message.Content != "" || resp.Message.Reasoning != "Still thinking about the opening." {
		t.Errorf("message = %+v", resp.Message)
	}
	if resp.ReasoningTokens != 256 || resp.OutputTokens != 256 {
		t.Errorf("usage = %+v", resp)
	}
	// Ends on a reasoning item, which the API will not take back alone.
	if len(resp.Message.ProviderContent) != 0 {
		t.Errorf("provider content kept: %s", resp.Message.ProviderContent)
	}
}

func TestResponsesIncompleteReasons(t *testing.T) {
	for reason, want := range map[string]string{
		"max_output_tokens": "length", "content_filter": "content_filter", "": "incomplete", "other": "other",
	} {
		body := `{"id":"r","status":"incomplete","model":"m","output":[],"usage":{}`
		if reason != "" {
			body += `,"incomplete_details":{"reason":"` + reason + `"}`
		}
		srv, _ := serveResponses(t, 200, body+`}`)
		resp, err := newResponses(srv).Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}})
		if err != nil || resp.FinishReason != want {
			t.Errorf("reason %q: finish = %q, err = %v, want %q", reason, resp.FinishReason, err, want)
		}
	}
}

// A call cut off mid-arguments is not offered to the agent as a tool call.
func TestResponsesIncompleteFunctionCallIsDropped(t *testing.T) {
	srv, _ := serveResponses(t, 200, `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"model":"m",
	  "output":[{"type":"function_call","status":"incomplete","call_id":"c","name":"t","arguments":"{\"sq"}],"usage":{}}`)
	resp, err := newResponses(srv).Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 0 || resp.FinishReason != "length" {
		t.Errorf("response = %+v", resp)
	}
}

func TestResponsesRefusalIsTheAnswer(t *testing.T) {
	srv, _ := serveResponses(t, 200, `{"id":"r","status":"completed","model":"m","usage":{},
	  "output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I can't help with that."}]}]}`)
	resp, err := newResponses(srv).Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil || resp.Message.Content != "I can't help with that." {
		t.Errorf("resp = %+v, err = %v", resp, err)
	}
}

func TestResponsesErrors(t *testing.T) {
	t.Run("HTTP status", func(t *testing.T) {
		srv, _ := serveResponses(t, 429, `{"error":{"message":"Rate limit reached","type":"rate_limit_exceeded","code":"rate_limit_exceeded"}}`)
		_, err := newResponses(srv).Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}})
		var he *HTTPError
		if !errors.As(err, &he) || he.Status != 429 || !strings.Contains(he.Body, "Rate limit") {
			t.Errorf("err = %v", err)
		}
		if got := errorType(err); got != "429" {
			t.Errorf("error.type = %q, want 429", got)
		}
	})
	t.Run("failed response", func(t *testing.T) {
		srv, _ := serveResponses(t, 200, `{"id":"r","status":"failed","model":"m","output":[],"error":{"code":"server_error","message":"The model failed."}}`)
		_, err := newResponses(srv).Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}})
		if err == nil || !strings.Contains(err.Error(), "server_error") || !strings.Contains(err.Error(), "The model failed.") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("failed without detail", func(t *testing.T) {
		srv, _ := serveResponses(t, 200, `{"id":"r","status":"failed","model":"m","output":[]}`)
		if _, err := newResponses(srv).Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}}); err == nil {
			t.Error("a failed response was not an error")
		}
	})
	t.Run("never finished", func(t *testing.T) {
		srv, _ := serveResponses(t, 200, `{"id":"r","status":"in_progress","model":"m","output":[]}`)
		if _, err := newResponses(srv).Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}}); err == nil {
			t.Error("an in_progress response was not an error")
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		srv, _ := serveResponses(t, 200, `<html>`)
		if _, err := newResponses(srv).Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}}); err == nil {
			t.Error("garbage was not an error")
		}
	})
}

// The timeout is the whole call, and it is configurable so reasoning is not cut off.
func TestResponsesTimeout(t *testing.T) {
	c := NewOpenAIResponses("", "k")
	if c.HTTP.Timeout != DefaultResponsesTimeout || DefaultResponsesTimeout < 10*time.Minute {
		t.Errorf("default timeout = %v", c.HTTP.Timeout)
	}
	c.SetTimeout(90 * time.Second)
	if c.HTTP.Timeout != 90*time.Second {
		t.Errorf("timeout = %v", c.HTTP.Timeout)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, rsAnswer)
	}))
	defer slow.Close()
	c = NewOpenAIResponses(slow.URL+"/v1", "k")
	c.SetTimeout(50 * time.Millisecond)
	_, err := c.Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("no timeout")
	}
	c.SetTimeout(5 * time.Second)
	if _, err := c.Chat(context.Background(), Request{Model: "m", Temperature: -1, Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Errorf("a longer timeout should let a slow answer through: %v", err)
	}
}

func TestResponsesClientIdentity(t *testing.T) {
	c := NewOpenAIResponses("", "k")
	if c.Provider() != "openai" {
		t.Errorf("provider = %q", c.Provider())
	}
	if h, p := c.Endpoint(); h != "api.openai.com" || p != 443 {
		t.Errorf("endpoint = %s:%d", h, p)
	}
	c.Effort = "low"
	if c.DefaultReasoningLevel() != "low" {
		t.Errorf("default reasoning level = %q", c.DefaultReasoningLevel())
	}
	// Both clients are what the instrumentation asks for.
	var _ Client = c
	var _ Endpoint = c
	var _ Defaults = c
}

// End to end through the instrumented wrapper: the span says openai, carries
// the reasoning-token count, and the output message has a reasoning part.
func TestResponsesTelemetry(t *testing.T) {
	rec := telemetrytest.Install(t)
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT", "SPAN_ONLY")
	srv, _ := serveResponses(t, 200, rsAnswer)
	c := newResponses(srv)
	c.Effort = "low"
	_, err := Instrument(c).Chat(context.Background(), Request{Model: "gpt-5.4-mini", Temperature: -1, MaxTokens: 800,
		Messages: []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "Is 391 prime?"}}})
	if err != nil {
		t.Fatal(err)
	}
	spans := rec.SpanAttrs("chat gpt-5.4-mini")
	if len(spans) != 1 {
		t.Fatalf("want one chat span, got %d", len(spans))
	}
	a := spans[0]
	want := map[string]any{
		"gen_ai.operation.name":                "chat",
		"gen_ai.provider.name":                 "openai",
		"server.address":                       "127.0.0.1",
		"gen_ai.request.reasoning.level":       "low",
		"gen_ai.request.max_tokens":            int64(800),
		"gen_ai.response.id":                   "resp_1",
		"gen_ai.response.model":                "gpt-5.4-mini-2026-03-17",
		"gen_ai.usage.input_tokens":            int64(120),
		"gen_ai.usage.output_tokens":           int64(55),
		"gen_ai.usage.reasoning.output_tokens": int64(33),
		"gen_ai.usage.cache_read.input_tokens": int64(64),
	}
	for k, v := range want {
		got, ok := a[k]
		if !ok {
			t.Errorf("chat span missing %s", k)
			continue
		}
		if got.AsInterface() != v {
			t.Errorf("%s = %v, want %v", k, got.AsInterface(), v)
		}
	}
	if fr := a["gen_ai.response.finish_reasons"].AsStringSlice(); len(fr) != 1 || fr[0] != "stop" {
		t.Errorf("finish_reasons = %v", fr)
	}
	var out []encMsg
	if err := json.Unmarshal([]byte(a["gen_ai.output.messages"].AsString()), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Parts) != 2 || out[0].Parts[0].Type != "reasoning" ||
		!strings.Contains(out[0].Parts[0].Content, "17 times 23") || out[0].Parts[1].Type != "text" {
		t.Errorf("output messages = %+v", out)
	}
	if _, ok := a["gen_ai.input.messages"]; !ok {
		t.Error("no input messages")
	}
}

func TestResponsesErrorSpan(t *testing.T) {
	rec := telemetrytest.Install(t)
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT", "NO_CONTENT")
	srv, _ := serveResponses(t, 400, `{"error":{"message":"Unsupported parameter: 'temperature'","type":"invalid_request_error"}}`)
	_, err := Instrument(newResponses(srv)).Chat(context.Background(), Request{Model: "m", Temperature: 0.7,
		Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("want an error")
	}
	a := rec.SpanAttrs("chat m")[0]
	if a["error.type"].AsString() != "400" || a["gen_ai.provider.name"].AsString() != "openai" {
		t.Errorf("error span = %v", a)
	}
}
