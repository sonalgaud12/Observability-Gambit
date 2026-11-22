package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"

	sc "github.com/sonalgaud12/observability-gambit/internal/semconv"
)

// Bucket boundaries the GenAI conventions ask for (ExplicitBucketBoundaries
// advice in semantic-conventions-genai docs/gen-ai/*.md).
var (
	// gen_ai.client.inference.duration, gen_ai.execute_tool.duration.
	durationBuckets = []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}
	// gen_ai.invoke_agent.duration: agents can run for hours.
	agentDurationBuckets = []float64{0.1, 0.2, 0.4, 0.8, 1.6, 3.2, 6.4, 12.8, 25.6, 51.2, 102.4, 204.8, 409.6, 900, 1800, 3600, 7200, 14400, 43200, 86400}
	// gen_ai.client.inference.operation.{input,output}_tokens.
	tokenBuckets = []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864}
	// gen_ai.invoke_agent.{inference,tool}_calls.
	callBuckets = []float64{1, 2, 4, 8, 16, 32, 64, 128}
)

// GenAI records the GenAI semantic-convention metrics and events for one
// instrumentation scope. Build it after telemetry.Setup (or after a test has
// installed its own providers): it binds to the global providers at
// construction.
type GenAI struct {
	inferDur                            metric.Float64Histogram
	usageIn, usageOut                   metric.Int64Counter
	usageCacheRead, usageCacheWrite     metric.Int64Counter
	usageReasoning                      metric.Int64Counter
	opIn, opOut                         metric.Int64Histogram
	agentDur, toolDur                   metric.Float64Histogram
	agentInferenceCalls, agentToolCalls metric.Int64Histogram
	logger                              log.Logger
}

// NewGenAI creates the instruments under the given instrumentation scope.
func NewGenAI(scope string) *GenAI {
	m := otel.GetMeterProvider().Meter(scope)
	g := &GenAI{logger: logglobal.Logger(scope)}

	g.inferDur = f64Hist(m, sc.MetricInferenceDuration, "s", "GenAI client inference operation duration.", durationBuckets)
	g.toolDur = f64Hist(m, sc.MetricExecuteToolDuration, "s", "The duration of a single tool execution.", durationBuckets)
	g.agentDur = f64Hist(m, sc.MetricInvokeAgentDuration, "s", "The end-to-end duration of a single in-process agent invocation.", agentDurationBuckets)

	g.opIn = i64Hist(m, sc.MetricInferenceOperationInput, "{token}", "The number of input (prompt) tokens used per inference operation.", tokenBuckets)
	g.opOut = i64Hist(m, sc.MetricInferenceOperationOutput, "{token}", "The number of output (completion) tokens used per inference operation.", tokenBuckets)
	g.agentInferenceCalls = i64Hist(m, sc.MetricInvokeAgentInferenceCalls, "{inference_call}", "The number of inference (model) calls an agent makes during a single invocation.", callBuckets)
	g.agentToolCalls = i64Hist(m, sc.MetricInvokeAgentToolCalls, "{tool_call}", "The number of tool calls an agent makes during a single invocation.", callBuckets)

	g.usageIn = i64Counter(m, sc.MetricInferenceUsageInput, "The number of input (prompt) tokens used, including cached tokens.")
	g.usageOut = i64Counter(m, sc.MetricInferenceUsageOutput, "The number of output (completion) tokens used, including reasoning tokens.")
	g.usageCacheRead = i64Counter(m, sc.MetricInferenceUsageCacheRead, "The number of input tokens served from a provider-managed cache.")
	g.usageCacheWrite = i64Counter(m, sc.MetricInferenceUsageCacheWrite, "The number of input tokens written to a provider-managed cache.")
	g.usageReasoning = i64Counter(m, sc.MetricInferenceUsageReasoning, "The number of output tokens used for reasoning.")
	return g
}

func f64Hist(m metric.Meter, name, unit, desc string, b []float64) metric.Float64Histogram {
	h, err := m.Float64Histogram(name, metric.WithUnit(unit), metric.WithDescription(desc), metric.WithExplicitBucketBoundaries(b...))
	if err != nil {
		otel.Handle(err)
	}
	return h
}

func i64Hist(m metric.Meter, name, unit, desc string, b []float64) metric.Int64Histogram {
	h, err := m.Int64Histogram(name, metric.WithUnit(unit), metric.WithDescription(desc), metric.WithExplicitBucketBoundaries(b...))
	if err != nil {
		otel.Handle(err)
	}
	return h
}

func i64Counter(m metric.Meter, name, desc string) metric.Int64Counter {
	c, err := m.Int64Counter(name, metric.WithUnit("{token}"), metric.WithDescription(desc))
	if err != nil {
		otel.Handle(err)
	}
	return c
}

// Usage is the token usage of one inference operation.
type Usage struct {
	Input, Output, CacheRead, CacheWrite, Reasoning int
}

// InferenceDone records the metrics for one model call. base holds the
// low-cardinality attributes the conventions allow on inference metrics
// (operation, provider, request/response model, server address and port);
// errType is "" for a successful call.
func (g *GenAI) InferenceDone(ctx context.Context, base []attribute.KeyValue, errType string, d time.Duration, u Usage) {
	withErr := base
	if errType != "" {
		withErr = append(append([]attribute.KeyValue{}, base...), sc.ErrorType.String(errType))
	}
	g.inferDur.Record(ctx, d.Seconds(), metric.WithAttributes(withErr...))
	// Recorded for failed calls too, as 0, as the conventions ask.
	g.opIn.Record(ctx, int64(u.Input), metric.WithAttributes(withErr...))
	g.opOut.Record(ctx, int64(u.Output), metric.WithAttributes(withErr...))

	// The usage counters take only strictly positive contributions, split
	// by modality. This agent is text-only.
	text := metric.WithAttributes(append(append([]attribute.KeyValue{}, base...), sc.GenAITokenModality.String("text"))...)
	add := func(c metric.Int64Counter, n int, opt metric.AddOption) {
		if n > 0 {
			c.Add(ctx, int64(n), opt)
		}
	}
	add(g.usageIn, u.Input, text)
	add(g.usageOut, u.Output, text)
	add(g.usageCacheRead, u.CacheRead, text)
	add(g.usageCacheWrite, u.CacheWrite, text)
	add(g.usageReasoning, u.Reasoning, metric.WithAttributes(base...)) // no modality on this one
}

// NoToolCalls is the toolCalls argument of AgentDone for an agent that cannot
// call tools: gen_ai.invoke_agent.tool_calls is then not reported at all.
const NoToolCalls = -1

// AgentDone records the metrics for one in-process agent invocation.
func (g *GenAI) AgentDone(ctx context.Context, agentName, model, errType string, d time.Duration, inferenceCalls, toolCalls int) {
	durAttrs := []attribute.KeyValue{
		sc.GenAIOperationName.String(sc.OpInvokeAgent),
		sc.GenAIAgentName.String(agentName),
	}
	if model != "" {
		durAttrs = append(durAttrs, sc.GenAIRequestModel.String(model))
	}
	if errType != "" {
		durAttrs = append(durAttrs, sc.ErrorType.String(errType))
	}
	g.agentDur.Record(ctx, d.Seconds(), metric.WithAttributes(durAttrs...))

	calls := metric.WithAttributes(
		sc.GenAIOperationName.String(sc.OpInvokeAgent),
		sc.GenAIAgentName.String(agentName),
	)
	g.agentInferenceCalls.Record(ctx, int64(inferenceCalls), calls)
	// The conventions say not to report the tool call count for agents that
	// cannot call tools.
	if toolCalls != NoToolCalls {
		g.agentToolCalls.Record(ctx, int64(toolCalls), calls)
	}
}

// ToolDone records gen_ai.execute_tool.duration for one tool execution.
func (g *GenAI) ToolDone(ctx context.Context, toolName, toolType, agentName, errType string, d time.Duration) {
	attrs := []attribute.KeyValue{
		sc.GenAIOperationName.String(sc.OpExecuteTool),
		sc.GenAIToolName.String(toolName),
	}
	if toolType != "" {
		attrs = append(attrs, sc.GenAIToolType.String(toolType))
	}
	if agentName != "" {
		attrs = append(attrs, sc.GenAIAgentName.String(agentName))
	}
	if errType != "" {
		attrs = append(attrs, sc.ErrorType.String(errType))
	}
	g.toolDur.Record(ctx, d.Seconds(), metric.WithAttributes(attrs...))
}

// Event emits a GenAI event: a log record with an event name. The record
// takes its trace and span IDs from ctx, so pass a context that carries the
// span the event belongs to (it may have ended already; see
// trace.ContextWithSpanContext). A conversation id set with
// WithConversationID is added as gen_ai.conversation.id.
func (g *GenAI) Event(ctx context.Context, name string, sev log.Severity, attrs ...attribute.KeyValue) {
	g.EventStructured(ctx, name, sev, attrs, nil)
}

// EventStructured is Event with extra attributes that carry structured values
// (maps and slices), such as gen_ai.input.messages.
func (g *GenAI) EventStructured(ctx context.Context, name string, sev log.Severity, attrs []attribute.KeyValue, structured []log.KeyValue) {
	var r log.Record
	r.SetEventName(name)
	r.SetTimestamp(time.Now())
	r.SetSeverity(sev)
	r.SetSeverityText(sev.String())
	kvs := make([]log.KeyValue, 0, len(attrs)+len(structured)+1)
	hasConv := false
	for _, a := range attrs {
		if a.Key == sc.GenAIConversationID {
			hasConv = true
		}
		kvs = append(kvs, log.KeyValueFromAttribute(a))
	}
	if id := ConversationID(ctx); id != "" && !hasConv {
		kvs = append(kvs, log.String(string(sc.GenAIConversationID), id))
	}
	kvs = append(kvs, structured...)
	r.AddAttributes(kvs...)
	g.logger.Emit(ctx, r)
}

// Exception emits one of the GenAI *.exception events at WARN, as the
// conventions ask.
func (g *GenAI) Exception(ctx context.Context, name, excType string, err error) {
	attrs := []attribute.KeyValue{sc.ExceptionType.String(excType)}
	if err != nil {
		attrs = append(attrs, sc.ExceptionMessage.String(err.Error()))
	}
	g.Event(ctx, name, log.SeverityWarn, attrs...)
}
