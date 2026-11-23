package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/notnil/chess"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/sonalgaud12/observability-gambit/internal/llm"
	"github.com/sonalgaud12/observability-gambit/internal/telemetry"
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
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT", "SPAN_AND_EVENT")

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

// hangs never answers until its context ends.
type hangs struct{}

func (hangs) Provider() string { return "hang" }
func (hangs) Chat(ctx context.Context, _ llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestChooseDeadlineFallsBackToARandomMove(t *testing.T) {
	ag := New(Config{Model: "m", MaxRetries: 3}, hangs{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	mv, st, err := ag.Choose(ctx, chess.NewGame())
	if err != nil {
		t.Fatalf("a deadline must not be an error: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s: the deadline did not cut the decision short", time.Since(start))
	}
	if mv == nil || !st.Fallback || !st.TimedOut || st.LLMErrors == 0 {
		t.Errorf("mv=%v stats=%+v", mv, st)
	}
	if mv == nil || parseMove(chess.NewGame().Position(), (chess.UCINotation{}).Encode(chess.NewGame().Position(), mv)) == nil {
		t.Errorf("the fallback move is not legal: %v", mv)
	}
}

func TestChooseCancellationIsStillAnError(t *testing.T) {
	ag := New(Config{Model: "m", MaxRetries: 3}, hangs{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, _, err := ag.Choose(ctx, chess.NewGame()); err == nil {
		t.Fatal("cancelling a game must not look like a fallback move")
	}
}

func TestChooseReportsProgress(t *testing.T) {
	var seen []Stats
	client := llm.NewMock(3)
	t.Setenv("GAMBIT_MOCK_LATENCY_SCALE", "0")
	ag := New(Config{Model: "m", MaxRetries: 3}, llm.Instrument(client), 1)
	ag.OnProgress = func(s Stats) { seen = append(seen, s) }
	_, st, err := ag.Choose(context.Background(), chess.NewGame())
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 || seen[len(seen)-1].ChatCalls == 0 {
		t.Fatalf("no progress reported: %+v", seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i].ChatCalls < seen[i-1].ChatCalls {
			t.Errorf("progress went backwards: %+v", seen)
		}
	}
	if seen[len(seen)-1].ChatCalls != st.ChatCalls {
		t.Errorf("last progress %+v does not match the final stats %+v", seen[len(seen)-1], st)
	}
}

func TestParseMoveIsExported(t *testing.T) {
	pos := chess.NewGame().Position()
	if mv := ParseMove(pos, "e2e4"); mv == nil || mv.String() != "e2e4" {
		t.Errorf("UCI: %v", mv)
	}
	if mv := ParseMove(pos, "Nf3"); mv == nil || mv.String() != "g1f3" {
		t.Errorf("SAN: %v", mv)
	}
	if ParseMove(pos, "e2e5") != nil {
		t.Error("illegal move parsed")
	}
}

// Without tools the agent cannot call any, so it reports no tool_calls point;
// with tools it reports the count, including 0.
func TestMetricToolCalls(t *testing.T) {
	st := Stats{ToolCalls: 0}
	if got := New(Config{Model: "m", Tools: false}, nil, 1).MetricToolCalls(st); got != telemetry.NoToolCalls {
		t.Errorf("no tools: got %d, want NoToolCalls", got)
	}
	if got := New(Config{Model: "m", Tools: true}, nil, 1).MetricToolCalls(st); got != 0 {
		t.Errorf("tools, none called: got %d, want 0", got)
	}
	if got := New(Config{Model: "m", Tools: true}, nil, 1).MetricToolCalls(Stats{ToolCalls: 2}); got != 2 {
		t.Errorf("tools, two called: got %d, want 2", got)
	}
}

// The same loop as TestOpenAICompatibleLoop, over the native Messages API:
// an illegal move, a tool call and a legal move, each answer carrying a
// thinking block that must come back unchanged on the next request.
func TestAnthropicLoopKeepsThinkingAcrossTurns(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k" {
			http.Error(w, "bad request", 400)
			return
		}
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1]
		usage := `"usage":{"input_tokens":100,"output_tokens":40,"output_tokens_details":{"thinking_tokens":25}}`
		switch calls.Add(1) {
		case 1: // illegal
			w.Write([]byte(`{"id":"r1","model":"claude-haiku-5-5","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"Queen out early.","signature":"s1"},{"type":"text","text":"MOVE: Qh5"}],` + usage + `}`))
		case 2: // the illegal-move reply: the first answer, thinking block first
			if !strings.Contains(string(req.Messages[len(req.Messages)-2].Content), `"signature":"s1"`) || last.Role != "user" {
				t.Errorf("thinking block not sent back after an illegal move: %+v", req.Messages)
			}
			w.Write([]byte(`{"id":"r2","model":"claude-haiku-5-5","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"Check the legal moves.","signature":"s2"},{"type":"tool_use","id":"toolu_1","name":"legal_moves","input":{}}],` + usage + `}`))
		default:
			if last.Role != "user" || !strings.Contains(string(last.Content), `"tool_result"`) || !strings.Contains(string(last.Content), "Nf3") ||
				!strings.Contains(string(req.Messages[len(req.Messages)-2].Content), `"signature":"s2"`) {
				t.Errorf("tool result or thinking block not sent back: %+v", req.Messages)
			}
			w.Write([]byte(`{"id":"r3","model":"claude-haiku-5-5","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"Develop a knight.","signature":"s3"},{"type":"text","text":"Develop.\nMOVE: Nf3"}],` + usage + `}`))
		}
	}))
	defer srv.Close()

	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT", "SPAN_ONLY")

	ag := New(Config{Model: "claude-haiku-5-5", Temperature: -1, MaxTokens: 4096, MaxRetries: 2, Tools: true},
		llm.Instrument(llm.NewAnthropic(srv.URL, "k")), 1)
	mv, st, err := ag.Choose(context.Background(), chess.NewGame())
	if err != nil {
		t.Fatal(err)
	}
	if got := (chess.UCINotation{}).Encode(nil, mv); got != "g1f3" {
		t.Fatalf("move = %s", got)
	}
	if st.IllegalAttempts != 1 || st.ToolCalls != 1 || st.ChatCalls != 3 || st.ReasoningTokens != 75 || st.Fallback || st.FinishReason != "end_turn" {
		t.Errorf("unexpected stats: %+v", st)
	}
	chats := 0
	for _, s := range rec.Ended() {
		if s.Name() != "chat claude-haiku-5-5" {
			continue
		}
		chats++
		for _, kv := range s.Attributes() {
			switch string(kv.Key) {
			case "gen_ai.provider.name":
				if kv.Value.AsString() != "anthropic" {
					t.Errorf("provider = %s", kv.Value.AsString())
				}
			case "gen_ai.usage.reasoning.output_tokens":
				if kv.Value.AsInt64() != 25 {
					t.Errorf("reasoning tokens = %d", kv.Value.AsInt64())
				}
			}
		}
	}
	if chats != 3 {
		t.Errorf("chat spans = %d", chats)
	}
}

// Thinking can use the whole output cap; Anthropic calls that max_tokens.
func TestFinishLengthCountsAnthropicMaxTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"r","model":"m","stop_reason":"max_tokens","content":[{"type":"thinking","thinking":"hmm","signature":"s"}],"usage":{"input_tokens":5,"output_tokens":1024}}`))
	}))
	defer srv.Close()
	ag := New(Config{Model: "m", Temperature: -1, MaxTokens: 1024, MaxRetries: 0}, llm.NewAnthropic(srv.URL, "k"), 1)
	_, st, err := ag.Choose(context.Background(), chess.NewGame())
	if err != nil {
		t.Fatal(err)
	}
	if !st.FinishLength || !st.Fallback {
		t.Errorf("stats = %+v", st)
	}
}
