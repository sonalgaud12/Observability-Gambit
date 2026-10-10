package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	sc "github.com/sonalgaud12/observability-gambit/internal/semconv"
	"github.com/sonalgaud12/observability-gambit/internal/telemetry"
)

const instrumentationName = "github.com/sonalgaud12/observability-gambit/internal/llm"

// Instrumented wraps a Client and emits one CLIENT span per call, named
// "chat {model}", with gen_ai.* attributes.
type Instrumented struct {
	Inner  Client
	tracer trace.Tracer
}

// Instrument wraps c.
func Instrument(c Client) *Instrumented {
	return &Instrumented{Inner: c, tracer: otel.Tracer(instrumentationName)}
}

func (i *Instrumented) Provider() string { return i.Inner.Provider() }

// Chat implements Client.
func (i *Instrumented) Chat(ctx context.Context, req Request) (Response, error) {
	attrs := []attribute.KeyValue{
		sc.GenAIOperationName.String(sc.OpChat),
		sc.GenAIProviderName.String(i.Inner.Provider()),
		sc.GenAIRequestModel.String(req.Model),
	}
	if req.Temperature >= 0 {
		attrs = append(attrs, sc.GenAIRequestTemperature.Float64(req.Temperature))
	}
	if req.MaxTokens > 0 {
		attrs = append(attrs, sc.GenAIRequestMaxTokens.Int(req.MaxTokens))
	}

	ctx, span := i.tracer.Start(ctx, sc.OpChat+" "+req.Model,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()

	capture := telemetry.CaptureContent()
	if capture {
		span.SetAttributes(sc.GenAIInputMessages.String(encodeMessages(req.Messages)))
	}

	resp, err := i.Inner.Chat(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(sc.ErrorType.String(errorType(err)))
		return resp, err
	}

	span.SetAttributes(
		sc.GenAIResponseID.String(resp.ID),
		sc.GenAIResponseModel.String(resp.Model),
		sc.GenAIResponseFinishReasons.StringSlice([]string{resp.FinishReason}),
		sc.GenAIUsageInputTokens.Int(resp.InputTokens),
		sc.GenAIUsageOutputTokens.Int(resp.OutputTokens),
	)
	if resp.ReasoningTokens > 0 {
		span.SetAttributes(sc.GambitUsageReasoningTokens.Int(resp.ReasoningTokens))
	}
	if capture {
		span.SetAttributes(sc.GenAIOutputMessages.String(encodeOutput(resp)))
	}
	return resp, nil
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
	Role         string `json:"role"`
	Parts        []part `json:"parts"`
	FinishReason string `json:"finish_reason,omitempty"`
}

func toEnc(m Message) encMsg {
	e := encMsg{Role: m.Role}
	switch {
	case m.Role == "tool":
		e.Parts = append(e.Parts, part{Type: "tool_call_response", ID: m.ToolCallID, Response: m.Content})
	default:
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
	e := toEnc(r.Message)
	e.FinishReason = r.FinishReason
	b, _ := json.Marshal([]encMsg{e})
	return string(b)
}
