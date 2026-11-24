package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/notnil/chess"
	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/llm"
	"github.com/sonalgaud12/observability-gambit/internal/telemetry/telemetrytest"
)

// Canned Responses API reply (made up, no real call): a reasoning item with a
// summary, then the move as the answer.
const lunaReply = `{
  "id": "resp_luna", "status": "completed", "model": "gpt-6-luna",
  "output": [
    {"id": "rs_1", "type": "reasoning", "encrypted_content": "blob",
     "summary": [{"type": "summary_text", "text": "**Opening** Take the centre with e4."}]},
    {"id": "msg_1", "type": "message", "status": "completed", "role": "assistant",
     "content": [{"type": "output_text", "text": "MOVE: e2e4"}]}
  ],
  "usage": {"input_tokens": 100, "output_tokens": 40, "output_tokens_details": {"reasoning_tokens": 25}}
}`

// lunaClient builds the luna-6 option's client against a fake Responses
// endpoint and returns the request bodies it received.
func lunaClient(t *testing.T) (llm.Client, *[]map[string]any, string) {
	t.Helper()
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("%s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got = append(got, m)
		io.WriteString(w, lunaReply)
	}))
	t.Cleanup(srv.Close)
	old := presets["luna-6"]
	p := old
	p.BaseURL = srv.URL + "/v1"
	presets["luna-6"] = p
	t.Cleanup(func() { presets["luna-6"] = old })
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")
	cs, err := newFlags(t).modelChoices([]string{"luna-6"})
	if err != nil {
		t.Fatal(err)
	}
	return cs[0].NewClient(1, nil), &got, cs[0].Agent.Model
}

// The live luna-6 option keeps its id, label, model id and key, and now goes
// through the Responses client at effort low (still reported as provider openai).
func TestLunaPresetUsesTheResponsesClient(t *testing.T) {
	p := presets["luna-6"]
	if p.Label != "GPT-6 Luna" || p.Model != "gpt-6-luna" || p.KeyEnv != "OPENAI_API_KEY" || p.Provider != "openai" || !p.Responses || p.Effort != "low" {
		t.Errorf("preset = %+v", p)
	}
	if h := presets["haiku-5.5"]; h.Responses || h.Provider != "anthropic" || h.Effort != "" {
		t.Errorf("haiku preset changed: %+v", h)
	}
	c, _, _ := lunaClient(t)
	r, ok := c.(*llm.Instrumented).Inner.(*llm.OpenAIResponses)
	if !ok || r.Provider() != "openai" || r.Effort != "low" || r.Summary != "auto" {
		t.Errorf("client = %#v", c)
	}
}

// The request has reasoning on with a summary, tools, and none of the Chat
// Completions fields; the summary lands on the span, and the agent's
// agent_moved "reasoning" field (Stats.Reasoning) stays the answer text, as it
// is for Haiku.
func TestLunaRequestShapeSpanAndMove(t *testing.T) {
	rec := telemetrytest.Install(t)
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT", "SPAN_ONLY")
	c, got, model := lunaClient(t)
	ag := agent.New(agent.Config{Model: model, Temperature: -1, MaxTokens: 4096, Tools: true}, c, 1)
	mv, st, err := ag.Choose(context.Background(), chess.NewGame())
	if err != nil || mv == nil || mv.String() != "e2e4" {
		t.Fatalf("move %v, err %v", mv, err)
	}
	b := (*got)[0]
	if b["model"] != "gpt-6-luna" || b["max_output_tokens"] != float64(4096) || b["store"] != false {
		t.Errorf("body = %v", b)
	}
	if r, _ := b["reasoning"].(map[string]any); r["effort"] != "low" || r["summary"] != "auto" {
		t.Errorf("reasoning = %v", b["reasoning"])
	}
	if tools, _ := b["tools"].([]any); len(tools) == 0 {
		t.Error("no tools sent")
	}
	for _, k := range []string{"reasoning_effort", "max_completion_tokens", "max_tokens", "messages", "temperature"} {
		if _, ok := b[k]; ok {
			t.Errorf("Chat Completions field %s was sent", k)
		}
	}
	spans := rec.SpanAttrs("chat gpt-6-luna")
	if len(spans) != 1 {
		t.Fatalf("chat spans = %d", len(spans))
	}
	a := spans[0]
	if a["gen_ai.provider.name"].AsString() != "openai" || a["gen_ai.usage.reasoning.output_tokens"].AsInt64() != 25 {
		t.Errorf("span = %v", a)
	}
	if out := a["gen_ai.output.messages"].AsString(); !strings.Contains(out, `"type":"reasoning"`) || !strings.Contains(out, "Take the centre with e4") {
		t.Errorf("output messages = %s", out)
	}
	if st.ReasoningTokens != 25 || st.Reasoning != "MOVE: e2e4" {
		t.Errorf("stats = %+v", st)
	}
}
