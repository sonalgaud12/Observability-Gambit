package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"

	"go.opentelemetry.io/otel/log"
)

// Environment variables that choose where prompt and completion content goes.
const (
	// CaptureContentEnv is the switch the GenAI conventions (v0.1.0) define.
	CaptureContentEnv = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT"
	// LegacyCaptureEnv is the boolean switch earlier versions of this demo
	// read. It is still honoured when CaptureContentEnv is not set: "true"
	// means SPAN_AND_EVENT, anything else NO_CONTENT.
	LegacyCaptureEnv = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"
)

// CaptureMode says where opt-in content (messages, system instructions, tool
// definitions, tool arguments and results) is recorded.
type CaptureMode int

// The values of OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT.
const (
	NoContent    CaptureMode = iota // default: no content anywhere
	SpanOnly                        // as span attributes only
	EventOnly                       // on the details log event only
	SpanAndEvent                    // both
)

func (m CaptureMode) String() string {
	return [...]string{"NO_CONTENT", "SPAN_ONLY", "EVENT_ONLY", "SPAN_AND_EVENT"}[m]
}

// OnSpans reports whether content goes on span attributes.
func (m CaptureMode) OnSpans() bool { return m == SpanOnly || m == SpanAndEvent }

// OnEvents reports whether content goes on log events.
func (m CaptureMode) OnEvents() bool { return m == EventOnly || m == SpanAndEvent }

// ParseCaptureMode reads a CaptureContentEnv value, ignoring case. An unknown
// value means NoContent: content is opt-in, so a typo must not turn it on.
func ParseCaptureMode(v string) CaptureMode {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "SPAN_ONLY":
		return SpanOnly
	case "EVENT_ONLY":
		return EventOnly
	case "SPAN_AND_EVENT":
		return SpanAndEvent
	}
	return NoContent
}

// Capture returns the content capture mode from the environment. The
// conventions' variable wins; the legacy boolean one is only a fallback.
func Capture() CaptureMode {
	if v := os.Getenv(CaptureContentEnv); strings.TrimSpace(v) != "" {
		return ParseCaptureMode(v)
	}
	if strings.EqualFold(os.Getenv(LegacyCaptureEnv), "true") {
		return SpanAndEvent
	}
	return NoContent
}

type conversationKey struct{}

// WithConversationID returns a context that carries the gen_ai.conversation.id
// (the game id). Chat spans, tool spans and GenAI events started under it
// pick it up, so every signal of a game can be grouped.
func WithConversationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, conversationKey{}, id)
}

// ConversationID returns the id set by WithConversationID, or "".
func ConversationID(ctx context.Context) string {
	id, _ := ctx.Value(conversationKey{}).(string)
	return id
}

// StructuredValue turns a JSON document into a log value (map, slice or
// scalar), for the GenAI event attributes that v0.1.0 wants as structured
// values rather than JSON strings. Invalid JSON is kept as a string.
func StructuredValue(doc string) log.Value {
	dec := json.NewDecoder(bytes.NewReader([]byte(doc)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return log.StringValue(doc)
	}
	return toLogValue(v)
}

func toLogValue(v any) log.Value {
	switch x := v.(type) {
	case nil:
		return log.Value{}
	case bool:
		return log.BoolValue(x)
	case string:
		return log.StringValue(x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return log.Int64Value(i)
		}
		f, _ := x.Float64()
		return log.Float64Value(f)
	case []any:
		vs := make([]log.Value, len(x))
		for i, e := range x {
			vs[i] = toLogValue(e)
		}
		return log.SliceValue(vs...)
	case map[string]any:
		kvs := make([]log.KeyValue, 0, len(x))
		for k, e := range x {
			kvs = append(kvs, log.KeyValue{Key: k, Value: toLogValue(e)})
		}
		return log.MapValue(kvs...)
	}
	return log.StringValue("")
}
