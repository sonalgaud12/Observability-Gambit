package game

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/llm"
)

// splitClient plays the agent's moves (e5 forever, so the agent is a scripted
// player) and, when it sees the coach's system prompt, answers as the coach.
// It records every coach request.
type splitClient struct {
	mu        sync.Mutex
	coachText string // what the coach says; "" means fail
	coachErr  error
	coachIn   int
	coachOut  int
	hang      bool // the coach never answers
	asked     []llm.Request
}

func (c *splitClient) Provider() string { return "split" }
func (c *splitClient) Chat(ctx context.Context, req llm.Request) (llm.Response, error) {
	if len(req.Messages) > 0 && req.Messages[0].Content == coachSystemPrompt {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.asked = append(c.asked, req)
		if c.hang {
			c.mu.Unlock()
			<-ctx.Done()
			c.mu.Lock()
			return llm.Response{}, ctx.Err()
		}
		if c.coachErr != nil {
			return llm.Response{}, c.coachErr
		}
		return llm.Response{
			Message: llm.Message{Role: "assistant", Content: c.coachText}, FinishReason: "stop",
			InputTokens: c.coachIn, OutputTokens: c.coachOut,
		}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "MOVE: e5"}, FinishReason: "stop", InputTokens: 10, OutputTokens: 3}, nil
}

func (c *splitClient) requests() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.Request(nil), c.asked...)
}

// coachFixture is a live setup whose model is sc, graded by gr.
func coachFixture(t *testing.T, sc *splitClient, gr MoveGrader, mutate func(*LiveConfig)) *liveFixture {
	t.Helper()
	return newLive(t, func(c *LiveConfig) {
		c.Grader = gr
		c.NewClient = func(int64, llm.Brain) llm.Client { return llm.Instrument(sc) }
		c.Agent = agent.Config{Model: "split-1", Temperature: -1, MaxTokens: 256, MaxRetries: 3}
		if mutate != nil {
			mutate(c)
		}
	})
}

func playE4(t *testing.T, s *Session) (turn evt) {
	t.Helper()
	turn = waitFor(t, s, 0, "human_turn", nil)
	if _, err := s.Submit("e2e4"); err != nil {
		t.Fatal(err)
	}
	return turn
}

// After a human move, with the mock model, the coach comments without being asked.
func TestCoachCommentsOnAHumanMove(t *testing.T) {
	f := newLive(t, nil)
	s, _ := f.live.Start(StartOptions{})
	playE4(t, s)
	c := waitFor(t, s, 0, "coach_comment", nil)
	if c.data["kind"] != CoachComment || c.data["ply"] != float64(1) {
		t.Errorf("coach_comment = %v", c.data)
	}
	text, _ := c.data["text"].(string)
	if text == "" || notation.MatchString(text) {
		t.Errorf("coach text = %q", text)
	}
	if c.data["mock"] != true {
		t.Errorf("a mock coach must say so: %v", c.data)
	}
	// It comes after the grade it is built on.
	g := waitFor(t, s, 0, "human_graded", nil)
	if g.ID > c.ID {
		t.Errorf("coach_comment (%d) before human_graded (%d)", c.ID, g.ID)
	}
	s.Abort()
	<-s.Done()
}

// The prompt is built from Stockfish's facts, with every move in words.
func TestCoachPromptIsGroundedInStockfishFacts(t *testing.T) {
	sc := &splitClient{coachText: "That left your pawn hanging, so the engine would rather have played the knight. Watch out for loose pieces!", coachIn: 100, coachOut: 30}
	f := coachFixture(t, sc, fakeGrader{blunderSAN: "e4", bestSAN: "Nf3"}, nil)
	s, _ := f.live.Start(StartOptions{})
	playE4(t, s)
	c := waitFor(t, s, 0, "coach_comment", nil)
	if !strings.Contains(c.data["text"].(string), "knight") {
		t.Errorf("coach text = %v", c.data["text"])
	}

	reqs := sc.requests()
	if len(reqs) != 1 {
		t.Fatalf("coach requests = %d, want 1", len(reqs))
	}
	r := reqs[0]
	if r.MaxTokens == 0 || r.MaxTokens > coachMaxTokens || r.Model != "split-1" {
		t.Errorf("coach request model/max tokens = %q/%d", r.Model, r.MaxTokens)
	}
	user := r.Messages[len(r.Messages)-1].Content
	for _, want := range []string{
		"Move played: Pawn moves to e4",
		"Stockfish's verdict: blunder",
		"Winning chance lost by this move: about 25 percentage points",
		"The move Stockfish preferred: Knight moves to f3",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt lacks %q:\n%s", want, user)
		}
	}
	if m := notation.FindString(user); m != "" {
		t.Errorf("the prompt contains notation %q:\n%s", m, user)
	}
	if !strings.Contains(r.Messages[0].Content, "Do not invent") || !strings.Contains(r.Messages[0].Content, "Never write chess notation") {
		t.Error("the system prompt lost its grounding rules")
	}
	s.Abort()
	<-s.Done()
}

// "Ask the coach why" works on the AI's move, once; asking again costs nothing.
func TestAskTheCoachWhyOnTheAgentsMove(t *testing.T) {
	sc := &splitClient{coachText: "The AI took a safe developing move. It keeps its pieces protected.", coachIn: 50, coachOut: 20}
	f := coachFixture(t, sc, fakeGrader{}, nil)
	s, _ := f.live.Start(StartOptions{})
	turn := playE4(t, s)
	moved := waitFor(t, s, turn.ID, "agent_moved", nil)
	waitFor(t, s, moved.ID, "agent_graded", nil)
	waitFor(t, s, 0, "coach_comment", func(d map[string]any) bool { return d["kind"] == CoachComment })

	if err := s.AskCoach(2); err != nil {
		t.Fatal(err)
	}
	why := waitFor(t, s, 0, "coach_comment", func(d map[string]any) bool { return d["kind"] == CoachWhy })
	if why.data["ply"] != float64(2) || why.data["text"] == "" {
		t.Errorf("why = %v", why.data)
	}
	var asked string
	for _, r := range sc.requests() {
		asked = r.Messages[len(r.Messages)-1].Content
	}
	for _, want := range []string{"the AI opponent", "Move played: Pawn moves to e5", "Ask the coach why"} {
		if !strings.Contains(asked, want) {
			t.Errorf("the why prompt lacks %q:\n%s", want, asked)
		}
	}
	before := len(sc.requests())
	if err := s.AskCoach(2); err != nil {
		t.Errorf("asking again: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(sc.requests()); n != before {
		t.Errorf("a repeated question made %d more model calls", n-before)
	}
	s.Abort()
	<-s.Done()
}

func TestAskCoachRefusals(t *testing.T) {
	f := newLive(t, nil)
	s, _ := f.live.Start(StartOptions{})
	playE4(t, s)
	if err := s.AskCoach(99); !errors.Is(err, ErrUnknownPly) {
		t.Errorf("unknown ply: %v", err)
	}
	s.Abort()
	<-s.Done()

	off := newLive(t, func(c *LiveConfig) { c.NoCoach = true })
	if off.live.Info().Coach {
		t.Error("Info.Coach is true with NoCoach")
	}
	s, _ = off.live.Start(StartOptions{})
	playE4(t, s)
	waitFor(t, s, 0, "human_graded", nil)
	if err := s.AskCoach(1); !errors.Is(err, ErrCoachOff) {
		t.Errorf("coach off: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	for _, ty := range types(s) {
		if strings.HasPrefix(ty, "coach_") {
			t.Errorf("a coach event with the coach off: %v", types(s))
		}
	}
	s.Abort()
	<-s.Done()
}

// A coach that errors, times out or rambles in notation only loses the comment.
func TestCoachFailureOnlyHidesTheComment(t *testing.T) {
	for name, sc := range map[string]*splitClient{
		"error":      {coachErr: errors.New("provider down")},
		"notation":   {coachText: "You should have played Nf3 instead."},
		"empty text": {coachText: "   "},
	} {
		t.Run(name, func(t *testing.T) {
			f := coachFixture(t, sc, fakeGrader{}, nil)
			s, _ := f.live.Start(StartOptions{})
			turn := playE4(t, s)
			un := waitFor(t, s, turn.ID, "coach_unavailable", nil)
			if un.data["kind"] != CoachComment || un.data["explicit"] != false {
				t.Errorf("coach_unavailable = %v", un.data)
			}
			// The game carries on regardless.
			moved := waitFor(t, s, turn.ID, "agent_moved", nil)
			next := waitFor(t, s, moved.ID, "human_turn", nil)
			if _, err := s.Submit("d2d4"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, s, next.ID, "agent_moved", nil)
			if err := s.AskCoach(1); err != nil {
				t.Errorf("asking after a failure: %v", err) // allowed: the player may retry
			}
			s.Abort()
			<-s.Done()
		})
	}
}

// A coach that never answers is cut off, and the board never waits for it.
func TestCoachTimeoutDoesNotBlockTheGame(t *testing.T) {
	sc := &splitClient{hang: true}
	f := coachFixture(t, sc, fakeGrader{}, nil)
	s, _ := f.live.Start(StartOptions{})
	turn := playE4(t, s)
	moved := waitFor(t, s, turn.ID, "agent_moved", nil)
	waitFor(t, s, moved.ID, "human_turn", nil) // the human can move again while the coach hangs
	s.Abort()
	<-s.Done()
	for _, ty := range types(s) {
		if ty == "coach_comment" {
			t.Error("a hung coach produced a comment")
		}
	}
}

// Coach tokens are kept apart from the agent's: they never end a game, and the
// coach stops at its own allowance (half the budget).
func TestCoachUsageNeverEndsAGame(t *testing.T) {
	sc := &splitClient{coachText: "Good start. The pawn opens lines for your bishop and queen.", coachIn: 2000, coachOut: 30}
	f := coachFixture(t, sc, fakeGrader{}, func(c *LiveConfig) { c.GameTokenBudget = 2400 })
	s, _ := f.live.Start(StartOptions{})
	turn := playE4(t, s)
	waitFor(t, s, 0, "coach_comment", nil)
	moved := waitFor(t, s, turn.ID, "agent_moved", nil)
	waitFor(t, s, moved.ID, "agent_graded", nil)
	used, coach := s.tokenCounts()
	if coach != 2030 {
		t.Errorf("coach tokens = %d, want 2030", coach)
	}
	if used >= 2400/2 || used < 13 {
		t.Errorf("agent tokens = %d: the coach's tokens must not be counted with them", used)
	}
	if err := s.AskCoach(2); !errors.Is(err, ErrCoachBudget) {
		t.Errorf("AskCoach over its allowance: %v", err)
	}
	if n := len(sc.requests()); n != 1 {
		t.Errorf("coach model calls = %d, want 1", n)
	}
	// The coach is far over its allowance, yet the game goes on: only the agent's
	// tokens decide when it ends.
	next := waitFor(t, s, moved.ID, "human_turn", nil)
	s.Submit("d2d4")
	waitFor(t, s, next.ID, "agent_moved", nil)
	for _, ty := range types(s) {
		if ty == "game_over" {
			t.Error("the coach's tokens ended the game")
		}
	}
}

// The agent's own tokens still end a game at the budget.
func TestAgentTokensStillEndAGame(t *testing.T) {
	sc := &splitClient{coachText: "Fine.", coachIn: 1, coachOut: 1}
	f := coachFixture(t, sc, fakeGrader{}, func(c *LiveConfig) { c.GameTokenBudget = 13 })
	s, _ := f.live.Start(StartOptions{})
	turn := playE4(t, s)
	moved := waitFor(t, s, turn.ID, "agent_moved", nil)
	next := waitFor(t, s, moved.ID, "human_turn", nil)
	s.Submit("d2d4")
	over := waitFor(t, s, next.ID, "game_over", nil)
	if over.data["method"] != "TokenBudget" {
		t.Errorf("game_over = %v", over.data)
	}
	<-s.Done()
}

// Calls in flight together cannot take the coach past its allowance.
func TestCoachConcurrentCallsStayWithinAllowance(t *testing.T) {
	sc := &splitClient{coachText: "Fine."}
	f := coachFixture(t, sc, fakeGrader{}, func(c *LiveConfig) { c.GameTokenBudget = 3000 })
	s, _ := f.live.Start(StartOptions{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.coachReserve(400) {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 3 { // 3 x 400 fits the 1500 allowance, a 4th does not
		t.Errorf("granted %d reservations of 400 against an allowance of 1500, want 3", granted)
	}
	s.coachSettle(400, 100)
	if !s.coachReserve(400) {
		t.Error("a settled call's unused reservation was not released")
	}
}

// The coach is its own agent in the game's conversation.
func TestCoachIsTracedAsItsOwnAgent(t *testing.T) {
	seen := len(recorder.Ended()) // game IDs are per second: only look at the spans this test makes
	f := newLive(t, nil)
	s, _ := f.live.Start(StartOptions{})
	turn := playE4(t, s)
	moved := waitFor(t, s, turn.ID, "agent_moved", nil)
	waitFor(t, s, moved.ID, "agent_graded", nil)
	waitFor(t, s, 0, "coach_comment", nil)
	if err := s.AskCoach(2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, "coach_comment", func(d map[string]any) bool { return d["kind"] == CoachWhy })
	s.Abort()
	<-s.Done()

	attrs := func(sp sdktrace.ReadOnlySpan) map[string]string {
		m := map[string]string{}
		for _, kv := range sp.Attributes() {
			m[string(kv.Key)] = kv.Value.Emit()
		}
		return m
	}
	type traceInfo struct {
		root, agentSpan, chat sdktrace.ReadOnlySpan
	}
	coachTraces := map[string]*traceInfo{} // by trace ID
	var playerAgents, coachAgents int
	for _, sp := range recorder.Ended()[seen:] {
		a := attrs(sp)
		if a["gen_ai.conversation.id"] != s.ID {
			// (chat spans of the coach are found through their trace below)
			continue
		}
		switch {
		case sp.Name() == "invoke_agent "+agent.Name:
			playerAgents++
		case sp.Name() == "invoke_agent "+CoachAgentName:
			coachAgents++
			coachTraces[sp.SpanContext().TraceID().String()] = &traceInfo{agentSpan: sp}
		}
	}
	for _, sp := range recorder.Ended()[seen:] {
		ti := coachTraces[sp.SpanContext().TraceID().String()]
		if ti == nil {
			continue
		}
		switch {
		case sp.Name() == "chess.coach":
			ti.root = sp
		case strings.HasPrefix(sp.Name(), "chat "):
			ti.chat = sp
			if attrs(sp)["gen_ai.conversation.id"] != s.ID {
				t.Errorf("coach chat span lacks the conversation id: %v", attrs(sp))
			}
		}
	}
	if playerAgents == 0 || coachAgents != 2 || len(coachTraces) != 2 {
		t.Fatalf("player invoke_agent spans = %d, coach invoke_agent spans = %d, coach traces = %d", playerAgents, coachAgents, len(coachTraces))
	}
	kinds := map[string]bool{}
	for id, ti := range coachTraces {
		if ti.root == nil || ti.chat == nil {
			t.Fatalf("coach trace %s lacks its root or chat span", id)
		}
		ra, aa, ca := attrs(ti.root), attrs(ti.agentSpan), attrs(ti.chat)
		kinds[aa["gambit.coach.kind"]] = true
		for k, v := range map[string]string{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "coach", "gen_ai.provider.name": "mock",
			"gen_ai.conversation.id": s.ID, "gambit.run.mode": "human", "gen_ai.request.model": "mock-1",
		} {
			if aa[k] != v {
				t.Errorf("coach invoke_agent %s = %q, want %q", k, aa[k], v)
			}
		}
		for k, v := range map[string]string{
			"gen_ai.conversation.id": s.ID, "chess.game.id": s.ID, "gambit.run.mode": "human", "gambit.llm.mock": "true",
		} {
			if ra[k] != v {
				t.Errorf("chess.coach %s = %q, want %q", k, ra[k], v)
			}
		}
		if ca["gen_ai.operation.name"] != "chat" || ca["gen_ai.usage.input_tokens"] == "" {
			t.Errorf("coach chat span = %v", ca)
		}
		if aa["gen_ai.usage.input_tokens"] == "" || aa["gen_ai.usage.output_tokens"] == "" {
			t.Errorf("coach invoke_agent lacks token usage: %v", aa)
		}
		if ti.chat.Parent().SpanID() != ti.agentSpan.SpanContext().SpanID() {
			t.Error("the coach's chat span is not a child of its invoke_agent span")
		}
		if aa["gambit.coach.subject"] != ra["gambit.coach.subject"] {
			t.Errorf("subject differs between spans: %q / %q", aa["gambit.coach.subject"], ra["gambit.coach.subject"])
		}
		if ra["gen_ai.agent.name"] != "" {
			t.Error("the chess.coach root must not look like an agent span")
		}
	}
	if !kinds[CoachComment] || !kinds[CoachWhy] {
		t.Errorf("coach kinds traced = %v", kinds)
	}
}

func TestCleanCoachText(t *testing.T) {
	good := map[string]string{
		"**Nice move!** Your knight moves to f3 and guards the pawn on e5.": "Nice move! Your knight moves to f3 and guards the pawn on e5.",
		"Coach:  Castling keeps\nthe king safe.":                            "Castling keeps the king safe.",
		"The pawn takes the knight on d5. That is a fair trade.":            "The pawn takes the knight on d5. That is a fair trade.",
		"The Bishop is strong. Knights jump. Be careful with the king.":     "The Bishop is strong. Knights jump. Be careful with the king.",
	}
	for in, want := range good {
		if got, ok := cleanCoachText(in); !ok || got != want {
			t.Errorf("cleanCoachText(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"", "   ", "Try Nf3 next time.", "Better was Bxe5+.", "exd5 wins a piece.", "Play O-O now.", "Castle with 0-0.",
		"Promote with e8=Q.", "e2e4 was the move.", "The Qh5 is strong.",
	} {
		if got, ok := cleanCoachText(in); ok {
			t.Errorf("cleanCoachText(%q) = %q, want it refused", in, got)
		}
	}
	long := strings.Repeat("This is a sentence about chess. ", 50)
	got, ok := cleanCoachText(long)
	if !ok || len([]rune(got)) > coachMaxChars+1 || !strings.HasSuffix(got, ".") {
		t.Errorf("long text: %d runes, ok=%v, ends %q", len([]rune(got)), ok, got[len(got)-10:])
	}
}

// On the Anthropic client the coach asks for low effort and a budget with room
// for the answer after thinking, and reads the text block past a thinking block.
func TestCoachOnAnthropicClient(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &m)
		sys, _ := m["system"].(string)
		if !strings.Contains(sys, "chess coach") {
			io.WriteString(w, `{"id":"m","model":"x","stop_reason":"end_turn","content":[{"type":"text","text":"MOVE: e5"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		got = m
		io.WriteString(w, `{"id":"m","model":"x","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"Nice start! You opened up the centre."}],"usage":{"input_tokens":50,"output_tokens":60,"output_tokens_details":{"thinking_tokens":30}}}`)
	}))
	defer srv.Close()
	f := newLive(t, func(c *LiveConfig) {
		c.NewClient = func(int64, llm.Brain) llm.Client { return llm.NewAnthropic(srv.URL, "k") }
		c.Agent = agent.Config{Model: "claude-haiku-5-5", Temperature: -1, MaxTokens: 256, MaxRetries: 3}
	})
	s, _ := f.live.Start(StartOptions{})
	playE4(t, s)
	c := waitFor(t, s, 0, "coach_comment", nil)
	if text, _ := c.data["text"].(string); !strings.Contains(text, "Nice start") {
		t.Errorf("coach text = %v", c.data)
	}
	if got["max_tokens"] != float64(coachThinkingMax) {
		t.Errorf("max_tokens = %v", got["max_tokens"])
	}
	if oc, _ := got["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Errorf("output_config = %v", got["output_config"])
	}
	s.Abort()
	<-s.Done()
}
