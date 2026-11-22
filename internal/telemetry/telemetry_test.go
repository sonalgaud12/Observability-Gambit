package telemetry_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/sonalgaud12/observability-gambit/internal/telemetry"
	"github.com/sonalgaud12/observability-gambit/internal/telemetry/telemetrytest"
)

func TestCaptureMode(t *testing.T) {
	for _, tc := range []struct {
		newVar, oldVar string
		want           telemetry.CaptureMode
	}{
		{"", "", telemetry.NoContent},
		{"NO_CONTENT", "", telemetry.NoContent},
		{"SPAN_ONLY", "", telemetry.SpanOnly},
		{"event_only", "", telemetry.EventOnly},
		{" SPAN_AND_EVENT ", "", telemetry.SpanAndEvent},
		{"true", "", telemetry.NoContent}, // not a v0.1.0 value
		{"", "true", telemetry.SpanAndEvent},
		{"", "TRUE", telemetry.SpanAndEvent},
		{"", "false", telemetry.NoContent},
		{"SPAN_ONLY", "true", telemetry.SpanOnly},
		{"NO_CONTENT", "true", telemetry.NoContent},
	} {
		t.Setenv(telemetry.CaptureContentEnv, tc.newVar)
		t.Setenv(telemetry.LegacyCaptureEnv, tc.oldVar)
		if got := telemetry.Capture(); got != tc.want {
			t.Errorf("new=%q old=%q: %v, want %v", tc.newVar, tc.oldVar, got, tc.want)
		}
	}
	if !telemetry.SpanOnly.OnSpans() || telemetry.SpanOnly.OnEvents() || telemetry.EventOnly.OnSpans() || !telemetry.EventOnly.OnEvents() {
		t.Error("OnSpans/OnEvents wrong")
	}
}

func TestStructuredValue(t *testing.T) {
	v := telemetry.StructuredValue(`[{"role":"user","n":3,"x":1.5,"ok":true,"parts":[{"type":"text"}]}]`)
	if v.Kind() != log.KindSlice || len(v.AsSlice()) != 1 {
		t.Fatalf("got %v", v)
	}
	got := map[string]log.Value{}
	for _, kv := range v.AsSlice()[0].AsMap() {
		got[kv.Key] = kv.Value
	}
	if got["role"].AsString() != "user" || got["n"].AsInt64() != 3 || got["x"].AsFloat64() != 1.5 ||
		!got["ok"].AsBool() || got["parts"].Kind() != log.KindSlice {
		t.Errorf("decoded %v", got)
	}
	if s := telemetry.StructuredValue("not json"); s.Kind() != log.KindString {
		t.Errorf("invalid JSON should stay a string, got %v", s)
	}
}

func TestEventCarriesConversationID(t *testing.T) {
	rec := telemetrytest.Install(t)
	g := telemetry.NewGenAI("test")
	ctx := telemetry.WithConversationID(context.Background(), "game-1")
	g.Event(ctx, "gen_ai.evaluation.result", log.SeverityInfo)
	g.Event(context.Background(), "gen_ai.evaluation.result", log.SeverityInfo)
	ev := rec.Events("gen_ai.evaluation.result")
	if len(ev) != 2 || ev[0].Attrs["gen_ai.conversation.id"].AsString() != "game-1" {
		t.Fatalf("events = %+v", ev)
	}
	if _, ok := ev[1].Attrs["gen_ai.conversation.id"]; ok {
		t.Errorf("no conversation id was set, none should be added")
	}
}

// An agent that cannot call tools reports no tool_calls at all, not zero.
func TestAgentDoneWithoutTools(t *testing.T) {
	rec := telemetrytest.Install(t)
	g := telemetry.NewGenAI("test")
	g.AgentDone(context.Background(), "coach", "m", "", time.Second, 1, telemetry.NoToolCalls)
	if _, ok := rec.Metric(t, "gen_ai.invoke_agent.tool_calls"); ok {
		t.Error("tool_calls reported for an agent that cannot call tools")
	}
	if m, ok := rec.Metric(t, "gen_ai.invoke_agent.inference_calls"); !ok || m.Data.(metricdata.Histogram[int64]).DataPoints[0].Sum != 1 {
		t.Errorf("inference_calls = %+v", m)
	}
	g.AgentDone(context.Background(), "chess-agent", "m", "", time.Second, 1, 0)
	if _, ok := rec.Metric(t, "gen_ai.invoke_agent.tool_calls"); !ok {
		t.Error("a tool-capable agent with zero calls must still report 0")
	}
}
