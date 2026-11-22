package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"

	sc "github.com/sonalgaud12/observability-gambit/internal/semconv"
	"github.com/sonalgaud12/observability-gambit/internal/telemetry"
)

const instrumentationName = "github.com/sonalgaud12/observability-gambit/internal/llm"

// Instrumented wraps a Client and emits one CLIENT span per call, named
// "chat {model}" (the convention's gen_ai.client.inference span), with
// gen_ai.* attributes, plus the inference metrics and events.
type Instrumented struct {
	Inner  Client
	tracer trace.Tracer
	genai  *telemetry.GenAI
}

// Instrument wraps c. Call it after telemetry.Setup.
func Instrument(c Client) *Instrumented {
	return &Instrumented{Inner: c, tracer: otel.Tracer(instrumentationName), genai: telemetry.NewGenAI(instrumentationName)}
}

func (i *Instrumented) Provider() string { return i.Inner.Provider() }

// Chat implements Client.
func (i *Instrumented) Chat(ctx context.Context, req Request) (Response, error) {
	// The low-cardinality attributes, shared by the span and the metrics.
	base := []attribute.KeyValue{
		sc.GenAIOperationName.String(sc.OpChat),
		sc.GenAIProviderName.String(i.Inner.Provider()),
		sc.GenAIRequestModel.String(req.Model),
	}
	if ep, ok := i.Inner.(Endpoint); ok {
		if host, port := ep.Endpoint(); host != "" {
			base = append(base, sc.ServerAddress.String(host))
			if port > 0 {
				base = append(base, sc.ServerPort.Int(port))
			}
		}
	}

	attrs := append([]attribute.KeyValue{}, base...)
	attrs = append(attrs, requestAttrs(req, i.Inner)...)
	// The game id, when the caller put one in ctx. Not on the metrics: an id
	// per game would be unbounded cardinality there.
	if id := telemetry.ConversationID(ctx); id != "" {
		attrs = append(attrs, sc.GenAIConversationID.String(id))
	}

	start := time.Now()
	ctx, span := i.tracer.Start(ctx, sc.OpChat+" "+req.Model,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()

	capture := telemetry.Capture()
	var content []contentDoc
	if capture != telemetry.NoContent {
		content = inputContent(req)
	}
	if capture.OnSpans() {
		span.SetAttributes(spanContent(content)...)
	}

	resp, err := i.Inner.Chat(ctx, req)
	if err != nil {
		et := errorType(err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(sc.ErrorType.String(et))
		i.genai.InferenceDone(ctx, base, et, time.Since(start), telemetry.Usage{})
		i.genai.Exception(ctx, sc.EventInferenceException, exceptionType(err), err)
		return resp, err
	}

	respAttrs := responseAttrs(resp)
	span.SetAttributes(respAttrs...)
	if capture != telemetry.NoContent {
		content = append(content, contentDoc{sc.GenAIOutputMessages, encodeOutput(resp)})
	}
	if capture.OnSpans() {
		span.SetAttributes(sc.GenAIOutputMessages.String(content[len(content)-1].json))
	}

	mbase := base
	if resp.Model != "" {
		mbase = append(append([]attribute.KeyValue{}, base...), sc.GenAIResponseModel.String(resp.Model))
	}
	i.genai.InferenceDone(ctx, mbase, "", time.Since(start), telemetry.Usage{
		Input: resp.InputTokens, Output: resp.OutputTokens,
		CacheRead: resp.CacheReadTokens, CacheWrite: resp.CacheWriteTokens,
		Reasoning: resp.ReasoningTokens,
	})
	if capture.OnEvents() {
		// The same call as a log event, for backends that keep logs but
		// strip content from stored spans (Grafana Cloud does). Only when
		// content goes to events: without content it would just repeat the
		// span. The content is structured (maps and slices), not JSON text.
		ev := append(append([]attribute.KeyValue{}, attrs...), respAttrs...)
		i.genai.EventStructured(ctx, sc.EventInferenceDetails, log.SeverityDebug, ev, eventContent(content))
	}
	return resp, nil
}

// requestAttrs are the request settings actually sent. Unset fields are left
// out, so the span says what the provider received, not what it defaulted to.
func requestAttrs(req Request, c Client) []attribute.KeyValue {
	var a []attribute.KeyValue
	if req.Temperature >= 0 {
		a = append(a, sc.GenAIRequestTemperature.Float64(req.Temperature))
	}
	if req.MaxTokens > 0 {
		a = append(a, sc.GenAIRequestMaxTokens.Int(req.MaxTokens))
	}
	if req.TopP > 0 {
		a = append(a, sc.GenAIRequestTopP.Float64(req.TopP))
	}
	if req.Seed != nil {
		a = append(a, sc.GenAIRequestSeed.Int64(*req.Seed))
	}
	if len(req.StopSequences) > 0 {
		a = append(a, sc.GenAIRequestStopSequences.StringSlice(req.StopSequences))
	}
	if req.OutputType != "" {
		a = append(a, sc.GenAIOutputType.String(req.OutputType))
	}
	level := req.ReasoningLevel
	if level == "" {
		if d, ok := c.(Defaults); ok {
			level = d.DefaultReasoningLevel()
		}
	}
	if level != "" {
		a = append(a, sc.GenAIRequestReasoningLevel.String(level))
	}
	// gen_ai.request.stream is only set for streaming requests ("if unset,
	// the request is assumed to be non-streaming"), and this client never
	// streams, so neither it nor gen_ai.response.time_to_first_chunk appear.
	if req.PromptName != "" {
		a = append(a, sc.GenAIPromptName.String(req.PromptName))
		if req.PromptVersion != "" {
			a = append(a, sc.GenAIPromptVersion.String(req.PromptVersion))
		}
	}
	return a
}

// responseAttrs are the response and usage attributes of a successful call.
func responseAttrs(resp Response) []attribute.KeyValue {
	a := []attribute.KeyValue{
		sc.GenAIResponseID.String(resp.ID),
		sc.GenAIResponseModel.String(resp.Model),
		sc.GenAIResponseFinishReasons.StringSlice([]string{resp.FinishReason}),
		sc.GenAIUsageInputTokens.Int(resp.InputTokens),
		sc.GenAIUsageOutputTokens.Int(resp.OutputTokens),
	}
	// "Recommended when available and non-zero."
	if resp.ReasoningTokens > 0 {
		a = append(a, sc.GenAIUsageReasoningOutputTokens.Int(resp.ReasoningTokens))
	}
	if resp.CacheReadTokens > 0 {
		a = append(a, sc.GenAIUsageCacheReadInputTokens.Int(resp.CacheReadTokens))
	}
	if resp.CacheWriteTokens > 0 {
		a = append(a, sc.GenAIUsageCacheWriteInputTokens.Int(resp.CacheWriteTokens))
	}
	return a
}

// contentDoc is one piece of opt-in content: the attribute it is recorded
// under and its value as a JSON document.
//
// On events the document becomes a structured value, as v0.1.0 requires. On
// spans it stays a JSON string: the Go OpenTelemetry SDK's span attributes
// (go.opentelemetry.io/otel/attribute, v1.35.0 in go.mod) hold only scalars and
// slices of scalars, never maps or nested lists. See docs/semconv-coverage.md.
type contentDoc struct {
	key  attribute.Key
	json string
}

func spanContent(docs []contentDoc) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.key.String(d.json))
	}
	return out
}

func eventContent(docs []contentDoc) []log.KeyValue {
	out := make([]log.KeyValue, 0, len(docs))
	for _, d := range docs {
		out = append(out, log.KeyValue{Key: string(d.key), Value: telemetry.StructuredValue(d.json)})
	}
	return out
}

// inputContent is the opt-in content of a request: the system prompt as
// gen_ai.system_instructions, the rest of the chat as gen_ai.input.messages,
// and the tools offered as gen_ai.tool.definitions.
//
// Splitting the system prompt out is a judgment call: the conventions use
// gen_ai.system_instructions when the API takes instructions separately from
// the chat history, and Chat Completions carries them as a "system" message
// in the history. We split the leading system messages anyway, because the
// template is fixed per prompt version and reading it apart from the moves is
// what makes the prompt comparable. See docs/semconv-coverage.md.
func inputContent(req Request) []contentDoc {
	var system []string
	rest := req.Messages
	for len(rest) > 0 && rest[0].Role == "system" {
		system = append(system, rest[0].Content)
		rest = rest[1:]
	}
	var a []contentDoc
	if len(system) > 0 {
		a = append(a, contentDoc{sc.GenAISystemInstructions, encodeInstructions(system)})
	}
	a = append(a, contentDoc{sc.GenAIInputMessages, encodeMessages(rest)})
	if len(req.Tools) > 0 {
		a = append(a, contentDoc{sc.GenAIToolDefinitions, EncodeToolDefinitions(req.Tools)})
	}
	return a
}

// exceptionType is the exception.type of an error: its Go type, e.g. "*llm.HTTPError".
func exceptionType(err error) string {
	return fmt.Sprintf("%T", err)
}

func errorType(err error) string {
	var he *HTTPError
	if errors.As(err, &he) {
		return fmt.Sprintf("%d", he.Status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "_OTHER"
}

// Message encoding follows the GenAI "parts" shape:
// [{"role":"user","parts":[{"type":"text","content":"..."}]}]

type part struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Response  string          `json:"response,omitempty"`
}

type encMsg struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

func toEnc(m Message) encMsg {
	e := encMsg{Role: m.Role}
	switch {
	case m.Role == "tool":
		e.Parts = append(e.Parts, part{Type: "tool_call_response", ID: m.ToolCallID, Response: m.Content})
	default:
		if m.Reasoning != "" {
			e.Parts = append(e.Parts, part{Type: "reasoning", Content: m.Reasoning})
		}
		if m.Content != "" {
			e.Parts = append(e.Parts, part{Type: "text", Content: m.Content})
		}
		for _, tc := range m.ToolCalls {
			args := json.RawMessage(tc.Arguments)
			if !json.Valid(args) {
				args, _ = json.Marshal(tc.Arguments)
			}
			e.Parts = append(e.Parts, part{Type: "tool_call", ID: tc.ID, Name: tc.Name, Arguments: args})
		}
	}
	return e
}

func encodeMessages(ms []Message) string {
	out := make([]encMsg, 0, len(ms))
	for _, m := range ms {
		out = append(out, toEnc(m))
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func encodeOutput(r Response) string {
	// No finish_reason in the message: v0.1.0 deprecated it in favour of the
	// gen_ai.response.finish_reasons attribute.
	b, _ := json.Marshal([]encMsg{toEnc(r.Message)})
	return string(b)
}

// encodeInstructions renders gen_ai.system_instructions: a list of parts,
// [{"type":"text","content":"..."}].
func encodeInstructions(texts []string) string {
	ps := make([]part, 0, len(texts))
	for _, t := range texts {
		ps = append(ps, part{Type: "text", Content: t})
	}
	b, _ := json.Marshal(ps)
	return string(b)
}

type toolDefinition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// EncodeToolDefinitions renders gen_ai.tool.definitions:
// [{"type":"function","name":"...","description":"...","parameters":{...}}].
func EncodeToolDefinitions(tools []ToolDef) string {
	out := make([]toolDefinition, 0, len(tools))
	for _, t := range tools {
		out = append(out, toolDefinition{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	b, _ := json.Marshal(out)
	return string(b)
}
