package game

import (
	"context"
	"strings"
	"testing"

	"github.com/notnil/chess"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/llm"
	"github.com/sonalgaud12/observability-gambit/internal/telemetry/telemetrytest"
)

// oneMove always answers with the same move.
type oneMove struct{ san string }

func (oneMove) Provider() string { return "scripted" }
func (o oneMove) Chat(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{ID: "resp-1", Model: "m", FinishReason: "stop", InputTokens: 50, OutputTokens: 5,
		Message: llm.Message{Role: "assistant", Content: "MOVE: " + o.san}}, nil
}

func TestGradeIsAnEvaluationResultEvent(t *testing.T) {
	rec := telemetrytest.Install(t)
	p, err := newGradingPlayer(Config{Grader: fakeGrader{blunderSAN: "e4", bestSAN: "d4"}, MockBrainSkill: -1, GraderDepth: 12}, 0)
	if err != nil {
		t.Fatal(err)
	}
	ag := agent.New(agent.Config{Model: "m"}, llm.Instrument(oneMove{"e4"}), 1)
	pm, err := p.decide(context.Background(), chess.NewGame(), ag, "scripted", "g-test", moveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pm.finish(); err != nil {
		t.Fatal(err)
	}

	ev := rec.Events("gen_ai.evaluation.result")
	if len(ev) != 1 {
		t.Fatalf("want one evaluation event, got %d", len(ev))
	}
	e := ev[0]
	agentSpan, ok := rec.Span("invoke_agent " + agent.Name)
	if !ok {
		t.Fatal("no invoke_agent span")
	}
	if e.TraceID != agentSpan.TraceID() || e.SpanID != agentSpan.SpanID() {
		t.Errorf("evaluation not correlated with the invoke_agent span: %s/%s vs %s/%s",
			e.TraceID, e.SpanID, agentSpan.TraceID(), agentSpan.SpanID())
	}
	if e.Severity != log.SeverityInfo {
		t.Errorf("severity = %v", e.Severity)
	}
	if e.Attrs["gen_ai.evaluation.name"].AsString() != EvaluationName ||
		e.Attrs["gen_ai.evaluation.score.label"].AsString() != ClassBlunder ||
		e.Attrs["gen_ai.evaluation.score.value"].AsFloat64() != 0.5 ||
		e.Attrs["gen_ai.response.id"].AsString() != "resp-1" {
		t.Errorf("evaluation attrs = %v", e.Attrs)
	}
	if expl := e.Attrs["gen_ai.evaluation.explanation"].AsString(); !strings.Contains(expl, "best was d4") || !strings.Contains(expl, "depth 12") {
		t.Errorf("explanation = %q", expl)
	}

	a := rec.SpanAttrs("invoke_agent " + agent.Name)[0]
	if a["gambit.agent.version"].AsString() != agent.Version || a["gen_ai.agent.description"].AsString() != agent.Description {
		t.Errorf("invoke_agent span lacks version/description: %v", a)
	}
	// Removed from the invoke_agent span in v0.1.0; the version is under gambit.*.
	if _, ok := a["gen_ai.agent.version"]; ok {
		t.Errorf("invoke_agent span still has gen_ai.agent.version")
	}
	if e.Attrs["gen_ai.conversation.id"].AsString() != "g-test" {
		t.Errorf("evaluation event lacks the conversation id: %v", e.Attrs)
	}
	if a["gen_ai.usage.input_tokens"].AsInt64() != 50 {
		t.Errorf("invoke_agent token total = %v", a["gen_ai.usage.input_tokens"].AsInterface())
	}

	for name, want := range map[string]int64{
		"gen_ai.invoke_agent.inference_calls": 1,
		"gen_ai.invoke_agent.tool_calls":      0,
	} {
		m, ok := rec.Metric(t, name)
		if !ok {
			t.Errorf("no %s", name)
			continue
		}
		dp := m.Data.(metricdata.Histogram[int64]).DataPoints
		if len(dp) != 1 || dp[0].Count != 1 || dp[0].Sum != want {
			t.Errorf("%s = %+v, want one value %d", name, dp, want)
		}
	}
	m, ok := rec.Metric(t, "gen_ai.invoke_agent.duration")
	if !ok {
		t.Fatal("no gen_ai.invoke_agent.duration")
	}
	dp := m.Data.(metricdata.Histogram[float64]).DataPoints
	if b := dp[0].Bounds; len(b) != 20 || b[19] != 86400 {
		t.Errorf("invoke_agent.duration buckets = %v", b)
	}
}

// A fallback move was random, not the model's: its evaluation must not point
// at a model response.
func TestFallbackEvaluationHasNoResponseID(t *testing.T) {
	rec := telemetrytest.Install(t)
	p, err := newGradingPlayer(Config{Grader: fakeGrader{}, MockBrainSkill: -1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	ag := agent.New(agent.Config{Model: "m", MaxRetries: 1}, llm.Instrument(oneMove{"Qh5"}), 1) // always illegal
	pm, err := p.decide(context.Background(), chess.NewGame(), ag, "scripted", "g-test", moveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pm.finish(); err != nil {
		t.Fatal(err)
	}
	ev := rec.Events("gen_ai.evaluation.result")
	if len(ev) != 1 {
		t.Fatalf("want one evaluation event, got %d", len(ev))
	}
	if _, has := ev[0].Attrs["gen_ai.response.id"]; has {
		t.Errorf("fallback evaluation carries a response id")
	}
	if !ev[0].Attrs["chess.fallback"].AsBool() {
		t.Errorf("fallback evaluation not marked")
	}
}
