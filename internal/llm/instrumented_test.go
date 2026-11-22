package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/sonalgaud12/observability-gambit/internal/telemetry/telemetrytest"
)

// fakeClient is a remote-looking client with a default reasoning level.
type fakeClient struct {
	resp Response
	err  error
}

func (f *fakeClient) Provider() string                                { return "openai" }
func (f *fakeClient) Endpoint() (string, int)                         { return "api.example.com", 443 }
func (f *fakeClient) DefaultReasoningLevel() string                   { return "low" }
func (f *fakeClient) Chat(context.Context, Request) (Response, error) { return f.resp, f.err }

func testRequest() Request {
	seed := int64(7)
	return Request{
		Model: "m",
		Messages: []Message{
			{Role: "system", Content: "rules"},
			{Role: "user", Content: "your move"},
		},
		Tools:         []ToolDef{{Name: "legal_moves", Description: "List every legal move.", Parameters: map[string]any{"type": "object"}}},
		Temperature:   0.2,
		MaxTokens:     256,
		TopP:          0.9,
		Seed:          &seed,
		StopSequences: []string{"\n\n"},
		PromptName:    "chess-move",
		PromptVersion: "abc123",
	}
}

func testResponse() Response {
	return Response{
		ID: "r1", Model: "m-2026", FinishReason: "stop",
		Message:     Message{Role: "assistant", Content: "MOVE: e4", Reasoning: "e4 takes the centre."},
		InputTokens: 100, OutputTokens: 40, ReasoningTokens: 25,
		CacheReadTokens: 60, CacheWriteTokens: 10,
	}
}

func TestInferenceSpanFollowsTheConventions(t *testing.T) {
	rec := telemetrytest.Install(t)
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "false")
	c := Instrument(&fakeClient{resp: testResponse()})
	if _, err := c.Chat(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	spans := rec.SpanAttrs("chat m")
	if len(spans) != 1 {
		t.Fatalf("want one chat span, got %d", len(spans))
	}
	a := spans[0]
	want := map[string]any{
		"gen_ai.operation.name":                 "chat",
		"gen_ai.provider.name":                  "openai",
		"gen_ai.request.model":                  "m",
		"server.address":                        "api.example.com",
		"server.port":                           int64(443),
		"gen_ai.request.top_p":                  0.9,
		"gen_ai.request.seed":                   int64(7),
		"gen_ai.request.reasoning.level":        "low", // from the client's default
		"gen_ai.prompt.name":                    "chess-move",
		"gen_ai.prompt.version":                 "abc123",
		"gen_ai.response.id":                    "r1",
		"gen_ai.usage.input_tokens":             int64(100),
		"gen_ai.usage.output_tokens":            int64(40),
		"gen_ai.usage.reasoning.output_tokens":  int64(25),
		"gen_ai.usage.cache_read.input_tokens":  int64(60),
		"gen_ai.usage.cache_write.input_tokens": int64(10),
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
	if got := a["gen_ai.request.stop_sequences"].AsStringSlice(); len(got) != 1 || got[0] != "\n\n" {
		t.Errorf("stop_sequences = %q", got)
	}
	// Not streaming: neither key may appear. Content capture is off: no content.
	for _, k := range []string{
		"gen_ai.request.stream", "gen_ai.response.time_to_first_chunk", "gen_ai.output.type",
		"gen_ai.system_instructions", "gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.tool.definitions",
		"gambit.usage.reasoning_tokens",
	} {
		if _, ok := a[k]; ok {
			t.Errorf("chat span should not have %s", k)
		}
	}
	if ev := rec.Events("gen_ai.client.inference.operation.details"); len(ev) != 0 {
		t.Errorf("details event emitted without content capture")
	}
}

func TestInferenceContentCapture(t *testing.T) {
	rec := telemetrytest.Install(t)
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "true")
	c := Instrument(&fakeClient{resp: testResponse()})
	if _, err := c.Chat(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	a := rec.SpanAttrs("chat m")[0]

	if got := a["gen_ai.system_instructions"].AsString(); got != `[{"type":"text","content":"rules"}]` {
		t.Errorf("system_instructions = %s", got)
	}
	var in []encMsg
	if err := json.Unmarshal([]byte(a["gen_ai.input.messages"].AsString()), &in); err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].Role != "user" {
		t.Errorf("the system prompt should be split out of input.messages: %+v", in)
	}
	var defs []map[string]any
	if err := json.Unmarshal([]byte(a["gen_ai.tool.definitions"].AsString()), &defs); err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0]["type"] != "function" || defs[0]["name"] != "legal_moves" {
		t.Errorf("tool.definitions = %v", defs)
	}
	var out []encMsg
	if err := json.Unmarshal([]byte(a["gen_ai.output.messages"].AsString()), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Parts) != 2 || out[0].Parts[0].Type != "reasoning" || out[0].Parts[1].Type != "text" {
		t.Errorf("output.messages should carry a reasoning part then the text: %+v", out)
	}

	ev := rec.Events("gen_ai.client.inference.operation.details")
	if len(ev) != 1 {
		t.Fatalf("want one details event, got %d", len(ev))
	}
	span, _ := rec.Span("chat m")
	if ev[0].Severity != log.SeverityDebug || ev[0].SpanID != span.SpanID() || ev[0].TraceID != span.TraceID() {
		t.Errorf("details event: severity %v, span %s (want DEBUG, %s)", ev[0].Severity, ev[0].SpanID, span.SpanID())
	}
	if !strings.Contains(ev[0].Attrs["gen_ai.output.messages"].AsString(), "MOVE: e4") {
		t.Errorf("details event lacks the answer")
	}
}

func TestInferenceMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	c := Instrument(&fakeClient{resp: testResponse()})
	if _, err := c.Chat(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}

	m, ok := rec.Metric(t, "gen_ai.client.inference.duration")
	if !ok {
		t.Fatal("no gen_ai.client.inference.duration")
	}
	h := m.Data.(metricdata.Histogram[float64])
	if m.Unit != "s" || len(h.DataPoints) != 1 || h.DataPoints[0].Count != 1 {
		t.Fatalf("inference.duration: unit %q, points %+v", m.Unit, h.DataPoints)
	}
	if b := h.DataPoints[0].Bounds; len(b) != 14 || b[0] != 0.01 || b[13] != 81.92 {
		t.Errorf("inference.duration buckets = %v", b)
	}
	if v, _ := h.DataPoints[0].Attributes.Value("gen_ai.response.model"); v.AsString() != "m-2026" {
		t.Errorf("inference.duration lacks gen_ai.response.model")
	}

	sums := map[string]int64{
		"gen_ai.client.inference.usage.input_tokens":             100,
		"gen_ai.client.inference.usage.output_tokens":            40,
		"gen_ai.client.inference.usage.cache_read.input_tokens":  60,
		"gen_ai.client.inference.usage.cache_write.input_tokens": 10,
		"gen_ai.client.inference.usage.reasoning.output_tokens":  25,
	}
	for name, want := range sums {
		m, ok := rec.Metric(t, name)
		if !ok {
			t.Errorf("no %s", name)
			continue
		}
		dp := m.Data.(metricdata.Sum[int64]).DataPoints
		if len(dp) != 1 || dp[0].Value != want {
			t.Errorf("%s = %+v, want %d", name, dp, want)
			continue
		}
		mod, has := dp[0].Attributes.Value("gen_ai.token.modality")
		if strings.Contains(name, "reasoning") {
			if has {
				t.Errorf("%s should not carry gen_ai.token.modality", name)
			}
		} else if mod.AsString() != "text" {
			t.Errorf("%s: gen_ai.token.modality = %q", name, mod.AsString())
		}
	}

	m, ok = rec.Metric(t, "gen_ai.client.inference.operation.input_tokens")
	if !ok {
		t.Fatal("no gen_ai.client.inference.operation.input_tokens")
	}
	if dp := m.Data.(metricdata.Histogram[int64]).DataPoints; len(dp) != 1 || dp[0].Sum != 100 {
		t.Errorf("operation.input_tokens = %+v", dp)
	}

	if _, ok := rec.Metric(t, "gen_ai.client.operation.duration"); ok {
		t.Errorf("gen_ai.client.operation.duration must not be reported for inference")
	}
}

func TestInferenceErrorEmitsTheExceptionEvent(t *testing.T) {
	rec := telemetrytest.Install(t)
	c := Instrument(&fakeClient{err: &HTTPError{Status: 429, Body: "slow down"}})
	if _, err := c.Chat(context.Background(), testRequest()); err == nil {
		t.Fatal("want an error")
	}

	a := rec.SpanAttrs("chat m")[0]
	if a["error.type"].AsString() != "429" {
		t.Errorf("error.type = %v", a["error.type"].AsInterface())
	}
	ev := rec.Events("gen_ai.client.inference.exception")
	if len(ev) != 1 {
		t.Fatalf("want one exception event, got %d", len(ev))
	}
	span, _ := rec.Span("chat m")
	if ev[0].Severity != log.SeverityWarn || ev[0].SpanID != span.SpanID() {
		t.Errorf("exception event: severity %v, span %s, want WARN on %s", ev[0].Severity, ev[0].SpanID, span.SpanID())
	}
	if ev[0].Attrs["exception.type"].AsString() != "*llm.HTTPError" || !strings.Contains(ev[0].Attrs["exception.message"].AsString(), "429") {
		t.Errorf("exception attrs = %v", ev[0].Attrs)
	}

	m, _ := rec.Metric(t, "gen_ai.client.inference.duration")
	dp := m.Data.(metricdata.Histogram[float64]).DataPoints
	if v, _ := dp[0].Attributes.Value("error.type"); v.AsString() != "429" {
		t.Errorf("inference.duration lacks error.type on failure")
	}
	m, _ = rec.Metric(t, "gen_ai.client.inference.operation.input_tokens")
	if ip := m.Data.(metricdata.Histogram[int64]).DataPoints; len(ip) != 1 || ip[0].Count != 1 || ip[0].Sum != 0 {
		t.Errorf("a failed call should record 0 input tokens: %+v", ip)
	}
	if _, ok := rec.Metric(t, "gen_ai.client.inference.usage.input_tokens"); ok {
		t.Errorf("usage counters only take strictly positive contributions")
	}
}

func TestExceptionTypeIsTheGoType(t *testing.T) {
	if got := exceptionType(errors.New("x")); got != "*errors.errorString" {
		t.Errorf("exceptionType = %s", got)
	}
}
