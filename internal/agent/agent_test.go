package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/notnil/chess"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/sonalgaud12/observability-gambit/internal/llm"
)

func TestExtractAndParseMove(t *testing.T) {
	pos := chess.NewGame().Position()
	cases := map[string]string{
		"I like the center.\nMOVE: e4":  "e2e4",
		"move: **Nf3**":                 "g1f3",
		"Final answer -> MOVE = e2e4":   "e2e4",
		"Considering d4. MOVE: d4+":     "d2d4", // spurious check mark is tolerated
		"Thinking about e4 and Nc3 Nc3": "b1c3", // no MOVE line: last token
	}
	for in, want := range cases {
		mv := parseMove(pos, extractMove(in))
		if mv == nil {
			t.Fatalf("%q: no move parsed", in)
		}
		if got := (chess.UCINotation{}).Encode(pos, mv); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
	if mv := parseMove(pos, "Qh5"); mv != nil {
		t.Errorf("Qh5 should be illegal in the start position")
	}
}

func TestThreats(t *testing.T) {
	// White queen on d4 attacked by a black knight on c6 (Black just played Nc6).
	opt, _ := chess.FEN("r1bqkbnr/pppp1ppp/2n5/8/3Q4/8/PPP1PPPP/RNB1KBNR w KQkq - 1 4")
	got := threats(chess.NewGame(opt).Position())
	if !strings.Contains(got, "knight on c6 can take your queen on d4") {
		t.Errorf("threats missed the attacked queen:\n%s", got)
	}
}

// TestOpenAICompatibleLoop drives the real HTTP client through an illegal
// move, a tool call and a final legal move, and checks the emitted spans.
func TestOpenAICompatibleLoop(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad request", 400)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		n := calls.Add(1)
		usage := `"usage":{"prompt_tokens":100,"completion_tokens":40,"completion_tokens_details":{"reasoning_tokens":25}}`
		switch n {
		case 1: // illegal
			w.Write([]byte(`{"id":"r1","model":"m-2026","choices":[{"message":{"role":"assistant","content":"MOVE: Qh5"},"finish_reason":"stop"}],` + usage + `}`))
		case 2: // tool call
			if len(req["tools"].([]any)) != 2 {
				t.Errorf("tools not offered")
			}
			w.Write([]byte(`{"id":"r2","model":"m-2026","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"legal_moves","arguments":"{}"}}]},"finish_reason":"tool_calls"}],` + usage + `}`))
		default: // legal
			msgs := req["messages"].([]any)
			last := msgs[len(msgs)-1].(map[string]any)
			if last["role"] != "tool" || !strings.Contains(last["content"].(string), "Nf3") {
				t.Errorf("tool result not sent back: %v", last)
			}
			w.Write([]byte(`{"id":"r3","model":"m-2026","choices":[{"message":{"role":"assistant","content":"Develop.\nMOVE: Nf3"},"finish_reason":"stop"}],` + usage + `}`))
		}
	}))
	defer srv.Close()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "true")

	client := llm.Instrument(llm.NewOpenAICompatible(srv.URL+"/v1", "k", "openai"))
	ag := New(Config{Model: "m", Temperature: 0, MaxRetries: 2, Tools: true}, client, 1)
	mv, st, err := ag.Choose(context.Background(), chess.NewGame())
	if err != nil {
		t.Fatal(err)
	}
	if got := (chess.UCINotation{}).Encode(nil, mv); got != "g1f3" {
		t.Fatalf("move = %s", got)
	}
	if st.IllegalAttempts != 1 || st.ToolCalls != 1 || st.ChatCalls != 3 || st.ReasoningTokens != 75 || st.Fallback {
		t.Errorf("unexpected stats: %+v", st)
	}

	names := map[string]int{}
	for _, s := range rec.Ended() {
		names[s.Name()]++
		if s.Name() == "chat m" {
			attrs := map[string]bool{}
			for _, kv := range s.Attributes() {
				attrs[string(kv.Key)] = true
			}
			for _, k := range []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model",
				"gen_ai.response.model", "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
				"gen_ai.response.finish_reasons", "gen_ai.input.messages", "gen_ai.output.messages"} {
				if !attrs[k] {
					t.Errorf("chat span missing %s", k)
				}
			}
		}
	}
	if names["chat m"] != 3 || names["execute_tool legal_moves"] != 1 || names["chess.validate_move"] != 2 {
		t.Errorf("unexpected spans: %v", names)
	}
}
