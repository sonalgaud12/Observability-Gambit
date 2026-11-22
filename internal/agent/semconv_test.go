package agent

import (
	"context"
	"testing"

	"github.com/notnil/chess"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/sonalgaud12/observability-gambit/internal/llm"
	"github.com/sonalgaud12/observability-gambit/internal/telemetry/telemetrytest"
)

// scripted answers with a fixed list of responses, one per call.
type scripted struct {
	resps []llm.Response
	reqs  []llm.Request
}

func (s *scripted) Provider() string { return "scripted" }
func (s *scripted) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	s.reqs = append(s.reqs, req)
	r := s.resps[0]
	if len(s.resps) > 1 {
		s.resps = s.resps[1:]
	}
	return r, nil
}

func toolThenMove(tool string) *scripted {
	return &scripted{resps: []llm.Response{
		{ID: "r1", FinishReason: "tool_calls", Message: llm.Message{Role: "assistant",
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: tool, Arguments: "{}"}}}},
		{ID: "r2", FinishReason: "stop", Message: llm.Message{Role: "assistant", Content: "MOVE: e4"}},
	}}
}

func TestToolSpanFollowsTheConventions(t *testing.T) {
	for _, capture := range []bool{false, true} {
		rec := telemetrytest.Install(t)
		if capture {
			t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "true")
		} else {
			t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "false")
		}
		ag := New(Config{Model: "m", Tools: true}, llm.Instrument(toolThenMove("threats")), 1)
		_, st, err := ag.Choose(context.Background(), chess.NewGame())
		if err != nil {
			t.Fatal(err)
		}
		if st.ResponseID != "r2" || st.FinishReason != "stop" {
			t.Errorf("stats lack the producing response: %+v", st)
		}

		spans := rec.SpanAttrs("execute_tool threats")
		if len(spans) != 1 {
			t.Fatalf("want one tool span, got %d", len(spans))
		}
		a := spans[0]
		for k, v := range map[string]string{
			"gen_ai.operation.name":   "execute_tool",
			"gen_ai.tool.name":        "threats",
			"gen_ai.tool.type":        "function",
			"gen_ai.tool.call.id":     "c1",
			"gen_ai.agent.name":       Name,
			"gen_ai.tool.description": toolDescription("threats"),
		} {
			if a[k].AsString() != v {
				t.Errorf("tool span %s = %q, want %q", k, a[k].AsString(), v)
			}
		}
		_, hasArgs := a["gen_ai.tool.call.arguments"]
		_, hasResult := a["gen_ai.tool.call.result"]
		if hasArgs != capture || hasResult != capture {
			t.Errorf("capture=%v: arguments %v, result %v", capture, hasArgs, hasResult)
		}
		if capture && a["gen_ai.tool.call.arguments"].AsString() != "{}" {
			t.Errorf("arguments = %q", a["gen_ai.tool.call.arguments"].AsString())
		}

		m, ok := rec.Metric(t, "gen_ai.execute_tool.duration")
		if !ok {
			t.Fatal("no gen_ai.execute_tool.duration")
		}
		dp := m.Data.(metricdata.Histogram[float64]).DataPoints
		if v, _ := dp[0].Attributes.Value("gen_ai.tool.name"); len(dp) != 1 || v.AsString() != "threats" {
			t.Errorf("execute_tool.duration points = %+v", dp)
		}
	}
}

func TestChatCallsNameThePrompt(t *testing.T) {
	rec := telemetrytest.Install(t)
	sc := toolThenMove("legal_moves")
	ag := New(Config{Model: "m", Tools: true}, llm.Instrument(sc), 1)
	if _, _, err := ag.Choose(context.Background(), chess.NewGame()); err != nil {
		t.Fatal(err)
	}
	v := ag.PromptVersion()
	if len(v) != 12 {
		t.Fatalf("PromptVersion = %q", v)
	}
	for _, a := range rec.SpanAttrs("chat m") {
		if a["gen_ai.prompt.name"].AsString() != PromptName || a["gen_ai.prompt.version"].AsString() != v {
			t.Errorf("chat span prompt = %v/%v", a["gen_ai.prompt.name"].AsInterface(), a["gen_ai.prompt.version"].AsInterface())
		}
	}
	// A different prompt variant is a different version.
	if other := New(Config{Model: "m", Tools: false}, sc, 1).PromptVersion(); other == v {
		t.Errorf("prompt variants share version %s", v)
	}
}

func TestUnknownToolEmitsTheExceptionEvent(t *testing.T) {
	rec := telemetrytest.Install(t)
	ag := New(Config{Model: "m", Tools: true}, llm.Instrument(toolThenMove("teleport")), 1)
	if _, _, err := ag.Choose(context.Background(), chess.NewGame()); err != nil {
		t.Fatal(err)
	}
	ev := rec.Events("gen_ai.execute_tool.exception")
	if len(ev) != 1 {
		t.Fatalf("want one exception event, got %d", len(ev))
	}
	span, _ := rec.Span("execute_tool teleport")
	if ev[0].Severity != log.SeverityWarn || ev[0].SpanID != span.SpanID() || ev[0].Attrs["exception.type"].AsString() != "unknown_tool" {
		t.Errorf("exception event = %+v", ev[0])
	}
	if a := rec.SpanAttrs("execute_tool teleport")[0]; a["error.type"].AsString() != "unknown_tool" {
		t.Errorf("tool span error.type = %v", a["error.type"].AsInterface())
	}
}

func TestAgentSpanAttributes(t *testing.T) {
	ag := New(Config{Model: "m", Temperature: 0.3, MaxTokens: 512}, &scripted{}, 1)
	got := map[string]any{}
	for _, kv := range ag.SpanAttrs() {
		got[string(kv.Key)] = kv.Value.AsInterface()
	}
	if got["gen_ai.agent.version"] != Version || got["gen_ai.agent.description"] != Description ||
		got["gen_ai.request.temperature"] != 0.3 || got["gen_ai.request.max_tokens"] != int64(512) {
		t.Errorf("SpanAttrs = %v", got)
	}

	st := Stats{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 3, ChatCalls: 2, FinishReason: "stop"}
	keys := map[string]bool{}
	for _, kv := range st.Attrs() {
		keys[string(kv.Key)] = true
	}
	for _, k := range []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "gen_ai.response.finish_reasons", "gambit.chat_calls"} {
		if !keys[k] {
			t.Errorf("Stats.Attrs missing %s", k)
		}
	}
	if keys["gambit.usage.reasoning_tokens"] {
		t.Errorf("the custom reasoning key should be gone")
	}
}
