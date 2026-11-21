package game

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notnil/chess"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/dataset"
	"github.com/sonalgaud12/observability-gambit/internal/llm"
)

var recorder = tracetest.NewSpanRecorder()

func TestMain(m *testing.M) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	os.Exit(m.Run())
}

// fakeGrader stands in for Stockfish: every move is "good", except that
// blunderSAN, if set, is graded a blunder.
type fakeGrader struct{ blunderSAN, bestSAN string }

func (f fakeGrader) NewGame() error { return nil }
func (f fakeGrader) Close()         {}
func (f fakeGrader) Grade(_ context.Context, before *chess.Position, played *chess.Move, _ *chess.Game) (Grade, error) {
	gr := Grade{EvalBeforeCP: 20, EvalAfterCP: 10, CPLoss: 10, WinChanceDrop: 0.01, Class: ClassGood, BestMove: "e4", Depth: 1}
	san := chess.AlgebraicNotation{}.Encode(before, played)
	if f.bestSAN != "" {
		gr.BestMove = f.bestSAN
	}
	if f.blunderSAN != "" && san == f.blunderSAN {
		gr.Class, gr.WinChanceDrop, gr.CPLoss = ClassBlunder, 0.5, 400
	}
	return gr, nil
}

// hangClient never answers: a model that is stuck.
type hangClient struct{}

func (hangClient) Provider() string { return "hang" }
func (hangClient) Chat(ctx context.Context, _ llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

type liveFixture struct {
	live  *Live
	dir   string
	saved chan dataset.GameRecord
}

func newLive(t *testing.T, mutate func(*LiveConfig)) *liveFixture {
	t.Helper()
	t.Setenv("GAMBIT_MOCK_LATENCY_SCALE", "0.001")
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	cfg := LiveConfig{
		Config: Config{
			Agent: agent.Config{Model: "mock-1", Temperature: -1, MaxTokens: 256, MaxRetries: 3, Tools: true},
			NewClient: func(seed int64, brain llm.Brain) llm.Client {
				m := llm.NewMock(seed)
				m.Brain = brain
				return llm.Instrument(m)
			},
			MockBrainSkill: -1,
			GraderDepth:    1,
			MaxPlies:       200,
			Seed:           7,
			Writer:         w,
			KeepReasoning:  true,
			Grader:         fakeGrader{},
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	l, err := NewLive(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f := &liveFixture{live: l, dir: dir, saved: make(chan dataset.GameRecord, 8)}
	l.SetOnGame(func(g dataset.GameRecord, _ []dataset.MoveRecord) { f.saved <- g })
	t.Cleanup(l.Close)
	return f
}

type evt struct {
	Event
	data map[string]any
}

// waitFor returns the first event after `after` matching typ (and pred, if set).
func waitFor(t *testing.T, s *Session, after int, typ string, pred func(map[string]any) bool) evt {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		evs, more, _ := s.Events(after)
		for _, e := range evs {
			if e.Type != typ {
				continue
			}
			var d map[string]any
			if err := json.Unmarshal(e.Data, &d); err != nil {
				t.Fatalf("%s: bad data %s", e.Type, e.Data)
			}
			if pred == nil || pred(d) {
				return evt{e, d}
			}
		}
		select {
		case <-more:
		case <-deadline:
			all, _, _ := s.Events(0)
			var types []string
			for _, e := range all {
				types = append(types, e.Type)
			}
			t.Fatalf("timed out waiting for %q; events so far: %v", typ, types)
		}
	}
}

func types(s *Session) []string {
	evs, _, _ := s.Events(0)
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func TestLiveHumanGame(t *testing.T) {
	f := newLive(t, nil)
	s, err := f.live.Start(StartOptions{})
	if err != nil {
		t.Fatal(err)
	}

	turn := waitFor(t, s, 0, "human_turn", nil)
	if got := len(turn.data["legal"].([]any)); got != 20 {
		t.Errorf("legal moves at the start = %d, want 20", got)
	}
	// An illegal move is refused and the game waits.
	if _, err := s.Submit("e2e5"); err == nil {
		t.Fatal("an illegal move was accepted")
	}
	if _, err := s.Submit("e2e4"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit("d2d4"); err == nil {
		t.Fatal("a second move in the same turn was accepted")
	}

	moved := waitFor(t, s, turn.ID, "agent_moved", nil)
	if moved.data["trace_id"] == "" || moved.data["san"] == "" || moved.data["fen_after"] == "" {
		t.Errorf("agent_moved is missing fields: %v", moved.data)
	}
	if moved.data["chat_calls"].(float64) < 1 {
		t.Errorf("no chat calls recorded: %v", moved.data)
	}
	graded := waitFor(t, s, moved.ID, "agent_graded", nil)
	if graded.data["class"] != ClassGood {
		t.Errorf("class = %v", graded.data["class"])
	}
	turn2 := waitFor(t, s, moved.ID, "human_turn", nil)
	if turn2.data["ply"].(float64) != 3 {
		t.Errorf("second human turn is ply %v, want 3", turn2.data["ply"])
	}

	s.Resign()
	over := waitFor(t, s, turn2.ID, "game_over", nil)
	if over.data["method"] != "Resignation" || over.data["result"] != "0-1" {
		t.Errorf("game_over = %v", over.data)
	}
	waitFor(t, s, over.ID, "game_saved", func(d map[string]any) bool { return d["saved"] == true })
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session never finished")
	}
	if _, _, complete := s.Events(0); !complete {
		t.Error("the log is not marked complete")
	}

	// The event order a UI relies on.
	var got []string
	for _, ty := range types(s) {
		// the grade of the human's move, and the coach's remarks on it, arrive whenever they are done
		if ty != "human_graded" && !strings.HasPrefix(ty, "coach_") {
			got = append(got, ty)
		}
	}
	want := []string{"game_started", "human_turn", "human_moved", "agent_thinking"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("events = %v, want a prefix of %v", got, want)
		}
	}

	// The game landed in the same dataset files, marked as human.
	games, err := dataset.LoadGames(f.dir)
	if err != nil || len(games) != 1 {
		t.Fatalf("games: %v, %v", games, err)
	}
	g := games[0]
	if g.Mode != dataset.ModeHuman || g.AgentColor != "black" || g.Result != "0-1" || g.Plies != 2 {
		t.Errorf("game record = %+v", g)
	}
	moves, err := dataset.LoadMoves(f.dir)
	if err != nil || len(moves) != 1 {
		t.Fatalf("moves: %v, %v", moves, err)
	}
	m := moves[0]
	if m.Mode != dataset.ModeHuman || m.GameID != g.GameID || m.TraceID != moved.data["trace_id"] || m.Class != ClassGood || m.Ply != 2 {
		t.Errorf("move record = %+v", m)
	}
	select {
	case sg := <-f.saved:
		if sg.GameID != g.GameID {
			t.Errorf("OnGame got %s, want %s", sg.GameID, g.GameID)
		}
	case <-time.After(time.Second):
		t.Error("OnGame was not called")
	}
}

// A human game is traced like any other game, plus the human markers.
func TestLiveTracesLikeBatchGames(t *testing.T) {
	f := newLive(t, nil)
	s, _ := f.live.Start(StartOptions{})
	turn := waitFor(t, s, 0, "human_turn", nil)
	if _, err := s.Submit("d2d4"); err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, s, turn.ID, "agent_moved", nil)
	waitFor(t, s, moved.ID, "agent_graded", nil)
	s.Abort()
	<-s.Done()

	traceID := moved.data["trace_id"].(string)
	names := map[string]int{}
	var root, decision sdktrace.ReadOnlySpan
	for _, sp := range recorder.Ended() {
		if sp.SpanContext().TraceID().String() != traceID {
			continue
		}
		names[sp.Name()]++
		switch sp.Name() {
		case "chess.move":
			root = sp
		case "invoke_agent " + agent.Name:
			decision = sp
		}
	}
	for _, n := range []string{"chess.move", "invoke_agent " + agent.Name, "chess.evaluate", "chess.validate_move"} {
		if names[n] == 0 {
			t.Errorf("trace has no %q span: %v", n, names)
		}
	}
	chats := 0
	for n, c := range names {
		if len(n) > 5 && n[:5] == "chat " {
			chats += c
		}
	}
	if chats == 0 {
		t.Errorf("trace has no chat span: %v", names)
	}
	if root == nil || decision == nil {
		t.Fatal("no root or decision span")
	}
	attrs := func(sp sdktrace.ReadOnlySpan) map[string]string {
		m := map[string]string{}
		for _, kv := range sp.Attributes() {
			m[string(kv.Key)] = kv.Value.Emit()
		}
		return m
	}
	ra := attrs(root)
	for k, v := range map[string]string{
		"gambit.run.mode": "human", "chess.opponent.type": "human",
		"chess.game.id": s.ID, "gen_ai.conversation.id": s.ID, "chess.color": "black", "chess.ply": "2",
		"chess.move.class": ClassGood, "gambit.llm.mock": "true",
	} {
		if ra[k] != v {
			t.Errorf("root %s = %q, want %q", k, ra[k], v)
		}
	}
	for _, k := range []string{"chess.fen", "chess.move.san", "chess.move.uci", "chess.eval.cp_loss", "chess.eval.best_move", "chess.illegal_attempts"} {
		if _, ok := ra[k]; !ok {
			t.Errorf("root span lacks %s", k)
		}
	}
	if attrs(decision)["gambit.run.mode"] != "human" {
		t.Error("the decision span lacks gambit.run.mode, so a tail sampler could not see it early")
	}
}

func TestLiveMoveTimeoutPlaysAFlaggedFallback(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) {
		c.NewClient = func(int64, llm.Brain) llm.Client { return llm.Instrument(hangClient{}) }
		c.MoveTimeout = 200 * time.Millisecond
	})
	s, _ := f.live.Start(StartOptions{})
	turn := waitFor(t, s, 0, "human_turn", nil)
	if _, err := s.Submit("e2e4"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	thinking := waitFor(t, s, turn.ID, "agent_thinking", nil)
	if thinking.data["timeout_ms"].(float64) != 200 {
		t.Errorf("timeout_ms = %v", thinking.data["timeout_ms"])
	}
	moved := waitFor(t, s, thinking.ID, "agent_moved", nil)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("a stuck model froze the board for %s", took)
	}
	if moved.data["timed_out"] != true || moved.data["fallback"] != true {
		t.Errorf("timed_out/fallback not flagged: %v", moved.data)
	}
	// The game goes on: it is the human's turn again.
	waitFor(t, s, moved.ID, "human_turn", func(d map[string]any) bool { return d["ply"].(float64) == 3 })
	graded := waitFor(t, s, moved.ID, "agent_graded", nil)

	s.Resign()
	waitFor(t, s, graded.ID, "game_saved", func(d map[string]any) bool { return d["saved"] == true })

	moves, _ := dataset.LoadMoves(f.dir)
	if len(moves) != 1 || !moves[0].TimedOut || !moves[0].Fallback || moves[0].Mode != dataset.ModeHuman {
		t.Errorf("move record = %+v", moves)
	}

	var decision sdktrace.ReadOnlySpan
	for _, sp := range recorder.Ended() {
		if sp.SpanContext().TraceID().String() == moved.data["trace_id"] && sp.Name() == "invoke_agent "+agent.Name {
			decision = sp
		}
	}
	if decision == nil {
		t.Fatal("no decision span")
	}
	got := map[string]string{}
	for _, kv := range decision.Attributes() {
		got[string(kv.Key)] = kv.Value.Emit()
	}
	if got["error.type"] != "timeout" || got["gambit.timed_out"] != "true" || got["chess.fallback"] != "true" {
		t.Errorf("decision span attributes = %v", got)
	}
}

func TestLiveScriptedOpening(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.Script = []string{"e4", "Nf3", "Bc4"} })
	s, _ := f.live.Start(StartOptions{})

	turn := waitFor(t, s, 0, "human_turn", nil)
	if turn.data["scripted"] != "e4" {
		t.Fatalf("first scripted move = %v", turn.data["scripted"])
	}
	// The scripted move goes through the same path as any other move.
	if _, err := s.Submit(turn.data["scripted"].(string)); err != nil {
		t.Fatal(err)
	}
	turn2 := waitFor(t, s, turn.ID, "human_turn", nil)
	if turn2.data["scripted"] != "Nf3" {
		t.Fatalf("second scripted move = %v", turn2.data["scripted"])
	}
	// Leaving the script switches the suggestions off for the rest of the game.
	if _, err := s.Submit("d2d3"); err != nil {
		t.Fatal(err)
	}
	turn3 := waitFor(t, s, turn2.ID, "human_turn", nil)
	if turn3.data["scripted"] != "" {
		t.Errorf("after leaving the script, scripted = %v", turn3.data["scripted"])
	}
	s.Abort()
	<-s.Done()
}

func TestLiveScriptWithIllegalMoveIsDropped(t *testing.T) {
	f := newLive(t, nil)
	s, _ := f.live.Start(StartOptions{Script: []string{"Qh5", "e4"}})
	turn := waitFor(t, s, 0, "human_turn", nil)
	if turn.data["scripted"] != "" {
		t.Errorf("an illegal scripted move was offered: %v", turn.data["scripted"])
	}
	n := waitFor(t, s, 0, "notice", nil)
	if n.data["level"] != "info" {
		t.Errorf("notice = %v", n.data)
	}
	s.Abort()
	<-s.Done()

	// An empty (non-nil) script turns the configured default off.
	f2 := newLive(t, func(c *LiveConfig) { c.Script = []string{"e4"} })
	s2, _ := f2.live.Start(StartOptions{Script: []string{}})
	if t2 := waitFor(t, s2, 0, "human_turn", nil); t2.data["scripted"] != "" {
		t.Errorf("scripted = %v with the script switched off", t2.data["scripted"])
	}
	s2.Abort()
	<-s2.Done()
}

func TestLiveHumanAsBlackLetsTheAgentOpen(t *testing.T) {
	f := newLive(t, nil)
	s, err := f.live.Start(StartOptions{HumanColor: "black"})
	if err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, s, 0, "agent_moved", nil)
	if moved.data["color"] != "white" || moved.data["ply"].(float64) != 1 {
		t.Errorf("agent_moved = %v", moved.data)
	}
	turn := waitFor(t, s, moved.ID, "human_turn", nil)
	if turn.data["ply"].(float64) != 2 {
		t.Errorf("human_turn = %v", turn.data)
	}
	s.Abort()
	<-s.Done()

	if _, err := f.live.Start(StartOptions{HumanColor: "green"}); err == nil {
		t.Error("an unknown colour was accepted")
	}
}

func TestLiveNewGameAbandonsTheRunningOne(t *testing.T) {
	f := newLive(t, nil)
	first, _ := f.live.Start(StartOptions{})
	waitFor(t, first, 0, "human_turn", nil)

	second, err := f.live.Start(StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.Done():
	default:
		t.Fatal("the first game is still running")
	}
	over := waitFor(t, first, 0, "game_over", nil)
	if over.data["method"] != "Abandoned" {
		t.Errorf("game_over = %v", over.data)
	}
	if f.live.Session(first.ID) != first || f.live.Session(second.ID) != second {
		t.Error("sessions are not retrievable by ID")
	}
	// Nothing was graded, so nothing is written for the abandoned game.
	if games, _ := dataset.LoadGames(f.dir); len(games) != 0 {
		t.Errorf("an empty abandoned game was saved: %v", games)
	}
	if _, err := first.Submit("e2e4"); err == nil {
		t.Error("the abandoned game accepted a move")
	}
	waitFor(t, second, 0, "human_turn", nil)
	second.Abort()
	<-second.Done()
}

// A game played to the end: the fool's mate, with a model that knows its lines.
func TestLiveGameToCheckmate(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) {
		c.NewClient = func(int64, llm.Brain) llm.Client {
			return llm.Instrument(&scriptedClient{moves: []string{"e5", "Qh4#"}})
		}
		c.Agent.Tools = false
	})
	s, _ := f.live.Start(StartOptions{})
	for _, step := range []struct { // in order: a map would visit ply 3 first and wait forever
		ply int
		mv  string
	}{{1, "f3"}, {3, "g4"}} {
		waitFor(t, s, 0, "human_turn", func(d map[string]any) bool { return d["ply"].(float64) == float64(step.ply) })
		if _, err := s.Submit(step.mv); err != nil {
			t.Fatal(err)
		}
	}
	over := waitFor(t, s, 0, "game_over", nil)
	if over.data["result"] != "0-1" || over.data["method"] != "Checkmate" {
		t.Errorf("game_over = %v", over.data)
	}
	saved := waitFor(t, s, over.ID, "game_saved", nil)
	if saved.data["saved"] != true {
		t.Errorf("game_saved = %v", saved.data)
	}
	moves, _ := dataset.LoadMoves(f.dir)
	if len(moves) != 2 || moves[0].Ply != 2 || moves[1].Ply != 4 || moves[1].SAN != "Qh4#" {
		t.Errorf("moves = %+v", moves)
	}
}

// scriptedClient answers each chat call with the next move in its list.
type scriptedClient struct {
	mu    sync.Mutex
	moves []string
	n     int
}

func (c *scriptedClient) Provider() string { return "scripted" }
func (c *scriptedClient) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	mv := c.moves[c.n%len(c.moves)]
	c.n++
	return llm.Response{
		Message:      llm.Message{Role: "assistant", Content: "MOVE: " + mv},
		FinishReason: "stop", InputTokens: 10, OutputTokens: 3,
	}, nil
}

// --- limits for a shared deployment ---

func TestLiveExclusiveRefusesASecondGame(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.Exclusive = true })
	first, err := f.live.Start(StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	turn := waitFor(t, first, 0, "human_turn", nil)

	if _, err := f.live.Start(StartOptions{}); !errors.Is(err, ErrGameInProgress) {
		t.Fatalf("second Start err = %v, want ErrGameInProgress", err)
	}
	// The refused request did not touch the first game: it still takes moves.
	if _, err := first.Submit("e2e4"); err != nil {
		t.Fatalf("the running game was disturbed: %v", err)
	}
	waitFor(t, first, turn.ID, "agent_moved", nil)

	// Once the game is over (here: resigned) the slot is free again.
	first.Resign()
	waitFor(t, first, 0, "game_over", nil)
	<-first.Done()
	second, err := f.live.Start(StartOptions{})
	if err != nil {
		t.Fatalf("Start after the game ended: %v", err)
	}
	second.Abort()
	<-second.Done()
}

func TestLiveNotExclusiveByDefault(t *testing.T) {
	// TestLiveNewGameAbandonsTheRunningOne covers the behaviour; this pins that
	// the zero config has no lock and no limits.
	f := newLive(t, nil)
	if i := f.live.Info(); i.Exclusive || i.IdleTimeoutMS != 0 || i.GameTokenBudget != 0 {
		t.Errorf("limits are on by default: %+v", i)
	}
}

func TestLiveRateLimits(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.MaxGamesPerHour, c.MaxGamesPerDay = 2, 3 })
	now := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	f.live.now = func() time.Time { return now }

	start := func() error {
		s, err := f.live.Start(StartOptions{})
		if err == nil {
			s.Abort()
			<-s.Done()
		}
		return err
	}
	for i := 0; i < 2; i++ {
		if err := start(); err != nil {
			t.Fatalf("game %d: %v", i+1, err)
		}
	}
	err := start()
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("third game in the hour: err = %v, want ErrRateLimited", err)
	}
	if !strings.Contains(err.Error(), "per hour") {
		t.Errorf("the error does not say which limit: %v", err)
	}

	now = now.Add(61 * time.Minute) // the hourly window has moved on
	if err := start(); err != nil {
		t.Fatalf("game in the next hour: %v", err)
	}
	now = now.Add(61 * time.Minute)
	err = start() // 4th game inside 24 h
	if !errors.Is(err, ErrRateLimited) || !strings.Contains(err.Error(), "per day") {
		t.Fatalf("daily limit: err = %v", err)
	}
	now = now.Add(24 * time.Hour)
	if err := start(); err != nil {
		t.Fatalf("game the next day: %v", err)
	}
}

func TestLiveRefusedStartsDoNotCountTowardTheLimit(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.Exclusive, c.MaxGamesPerHour = true, 2 })
	first, _ := f.live.Start(StartOptions{})
	waitFor(t, first, 0, "human_turn", nil)
	for i := 0; i < 5; i++ {
		if _, err := f.live.Start(StartOptions{}); !errors.Is(err, ErrGameInProgress) {
			t.Fatalf("err = %v", err)
		}
	}
	first.Abort()
	<-first.Done()
	second, err := f.live.Start(StartOptions{})
	if err != nil {
		t.Fatalf("refused starts used up the hourly allowance: %v", err)
	}
	second.Abort()
	<-second.Done()
}

func TestLiveIdleTimeoutEndsAnAbandonedGame(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.Exclusive, c.IdleTimeout = true, 200*time.Millisecond })
	s, err := f.live.Start(StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, "human_turn", nil)
	over := waitFor(t, s, 0, "game_over", nil) // the human never moves
	if over.data["method"] != "IdleTimeout" {
		t.Errorf("game_over = %v", over.data)
	}
	<-s.Done()
	if !s.Ended() {
		t.Error("session not marked ended")
	}
	next, err := f.live.Start(StartOptions{})
	if err != nil {
		t.Fatalf("the idle game did not free the slot: %v", err)
	}
	next.Abort()
	<-next.Done()
}

func TestLiveIdleTimerRestartsEveryTurn(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.IdleTimeout = 1500 * time.Millisecond })
	s, _ := f.live.Start(StartOptions{})
	turn := waitFor(t, s, 0, "human_turn", nil)
	for _, mv := range []string{"e2e4", "d2d4"} {
		time.Sleep(900 * time.Millisecond) // longer than half the budget, twice: only a per-turn timer survives
		if _, err := s.Submit(mv); err != nil {
			t.Fatalf("%s: %v", mv, err)
		}
		moved := waitFor(t, s, turn.ID, "agent_moved", nil)
		turn = waitFor(t, s, moved.ID, "human_turn", nil)
	}
	s.Abort()
	<-s.Done()
}

func TestLiveTokenBudgetEndsTheGame(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.GameTokenBudget = 1 }) // any agent move exceeds it
	s, _ := f.live.Start(StartOptions{})
	turn := waitFor(t, s, 0, "human_turn", nil)
	if _, err := s.Submit("e2e4"); err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, s, turn.ID, "agent_moved", nil) // the first agent move is always allowed
	waitFor(t, s, moved.ID, "agent_graded", nil)
	turn = waitFor(t, s, moved.ID, "human_turn", nil)
	if _, err := s.Submit("d2d4"); err != nil {
		t.Fatal(err)
	}
	over := waitFor(t, s, turn.ID, "game_over", nil)
	if over.data["method"] != "TokenBudget" {
		t.Errorf("game_over = %v", over.data)
	}
	<-s.Done()
	// The one graded move is still saved.
	select {
	case g := <-f.saved:
		if g.Method != "TokenBudget" || g.Blunders+g.Mistakes < 0 {
			t.Errorf("saved game = %+v", g)
		}
	case <-time.After(5 * time.Second):
		t.Error("the budget-limited game was not saved")
	}
}

// --- model choice and player label ---

func twoModels() []ModelChoice {
	mk := func(id, label, model string) ModelChoice {
		return ModelChoice{
			ID: id, Label: label,
			Agent: agent.Config{Model: model, Temperature: -1, MaxTokens: 256, MaxRetries: 3, Tools: true},
			NewClient: func(seed int64, brain llm.Brain) llm.Client {
				m := llm.NewMock(seed)
				m.Brain = brain
				return llm.Instrument(m)
			},
		}
	}
	return []ModelChoice{mk("haiku-5.5", "Claude Haiku 5.5", "mock-claude-haiku-5-5"), mk("luna-6", "GPT-6 Luna", "mock-gpt-6-luna")}
}

func TestLiveModelChoiceAndPlayerLabelReachTheTrace(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.Models = twoModels() })
	info := f.live.Info()
	if len(info.Models) != 2 || info.DefaultModel != "haiku-5.5" || info.Model != "mock-claude-haiku-5-5" {
		t.Fatalf("info = %+v", info)
	}

	for _, tc := range []struct{ id, wantModel string }{
		{"luna-6", "mock-gpt-6-luna"}, {"", "mock-claude-haiku-5-5"},
	} {
		s, err := f.live.Start(StartOptions{Model: tc.id, Player: "sonal"})
		if err != nil {
			t.Fatal(err)
		}
		started := waitFor(t, s, 0, "game_started", nil)
		if started.data["model"] != tc.wantModel || started.data["player"] != "sonal" {
			t.Errorf("game_started = %v", started.data)
		}
		turn := waitFor(t, s, 0, "human_turn", nil)
		if _, err := s.Submit("e2e4"); err != nil {
			t.Fatal(err)
		}
		moved := waitFor(t, s, turn.ID, "agent_moved", nil)
		waitFor(t, s, moved.ID, "agent_graded", nil)
		s.Abort()
		<-s.Done()

		traceID := moved.data["trace_id"].(string)
		seen := map[string]map[string]string{}
		for _, sp := range recorder.Ended() {
			if sp.SpanContext().TraceID().String() != traceID {
				continue
			}
			m := map[string]string{}
			for _, kv := range sp.Attributes() {
				m[string(kv.Key)] = kv.Value.Emit()
			}
			seen[sp.Name()] = m
		}
		for _, name := range []string{"chess.move", "invoke_agent " + agent.Name, "chat " + tc.wantModel} {
			a, ok := seen[name]
			if !ok {
				t.Fatalf("model %q: no span %q in %v", tc.id, name, seen)
			}
			if a["gen_ai.request.model"] != tc.wantModel {
				t.Errorf("%s: gen_ai.request.model = %q, want %q", name, a["gen_ai.request.model"], tc.wantModel)
			}
		}
		for _, name := range []string{"chess.move", "invoke_agent " + agent.Name} {
			if seen[name]["gambit.player.label"] != "sonal" {
				t.Errorf("%s: gambit.player.label = %q", name, seen[name]["gambit.player.label"])
			}
		}
		select {
		case g := <-f.saved:
			if g.Model != tc.wantModel || g.Player != "sonal" {
				t.Errorf("saved game = %+v", g)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("game not saved")
		}
	}
}

func TestLiveRejectsAModelOutsideTheAllowList(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.Models = twoModels() })
	if _, err := f.live.Start(StartOptions{Model: "gpt-4o"}); !errors.Is(err, ErrUnknownModel) {
		t.Errorf("err = %v, want ErrUnknownModel", err)
	}
}

func TestNewLiveValidatesTheModelList(t *testing.T) {
	_, err := NewLive(LiveConfig{Config: Config{Grader: fakeGrader{}, NewClient: twoModels()[0].NewClient}, Models: twoModels(), DefaultModel: "nope"})
	if err == nil {
		t.Error("an unknown default model was accepted")
	}
	dup := append(twoModels(), twoModels()[0])
	_, err = NewLive(LiveConfig{Config: Config{Grader: fakeGrader{}}, Models: dup})
	if err == nil {
		t.Error("a duplicate model ID was accepted")
	}
}

// failingGrader fails on one SAN and otherwise behaves like fakeGrader.
type failingGrader struct {
	fakeGrader
	failSAN string
}

func (f failingGrader) Grade(ctx context.Context, before *chess.Position, played *chess.Move, after *chess.Game) (Grade, error) {
	if (chess.AlgebraicNotation{}).Encode(before, played) == f.failSAN {
		return Grade{}, errors.New("stockfish went away")
	}
	return f.fakeGrader.Grade(ctx, before, played, after)
}

// The human's move is graded with the same grader and classes, the advice is
// shown, and none of it reaches the dataset or the agent's tallies.
func TestLiveGradesTheHumansMove(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.Grader = fakeGrader{blunderSAN: "f3"} })
	s, _ := f.live.Start(StartOptions{})
	turn := waitFor(t, s, 0, "human_turn", nil)
	if _, err := s.Submit("f2f3"); err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, s, turn.ID, "human_moved", nil)
	ex := moved.data["explain"].(map[string]any)
	if ex["text"] != "Pawn moves to f3" || ex["from"] != "f2" || ex["to"] != "f3" {
		t.Errorf("human_moved explain = %v", ex)
	}
	g := waitFor(t, s, moved.ID, "human_graded", nil)
	if g.data["ply"].(float64) != 1 || g.data["class"] != ClassBlunder || g.data["win_pct_lost"].(float64) != 25 {
		t.Errorf("human_graded = %v", g.data)
	}
	coach := g.data["coach"].(map[string]any)
	if coach["best_san"] != "e4" || coach["advice"] == "" || coach["best"].(map[string]any)["text"] != "Pawn moves to e4" {
		t.Errorf("coach note = %v", coach)
	}
	// The agent still answers, and its own grade is a separate event.
	am := waitFor(t, s, moved.ID, "agent_moved", nil)
	if am.data["explain"].(map[string]any)["text"] == "" {
		t.Errorf("agent_moved has no explanation: %v", am.data)
	}
	waitFor(t, s, am.ID, "agent_graded", nil)
	s.Resign()
	over := waitFor(t, s, am.ID, "game_over", nil)
	waitFor(t, s, over.ID, "game_saved", nil)

	// Not in the dataset: only the agent's move is a row, and the tallies ignore the human's blunder.
	moves, _ := dataset.LoadMoves(f.dir)
	if len(moves) != 1 || moves[0].Ply != 2 {
		t.Errorf("dataset moves = %+v, want only the agent's ply 2", moves)
	}
	games, _ := dataset.LoadGames(f.dir)
	if len(games) != 1 || games[0].Blunders != 0 {
		t.Errorf("games = %+v, the human's blunder must not count", games)
	}

	// Traced as the human's, in its own trace, and nothing like an agent span.
	var human sdktrace.ReadOnlySpan
	for _, sp := range recorder.Ended() {
		if sp.Name() != "chess.human_move" {
			continue
		}
		for _, kv := range sp.Attributes() {
			if kv.Key == "chess.game.id" && kv.Value.Emit() == s.ID {
				human = sp
			}
		}
	}
	if human == nil {
		t.Fatal("no chess.human_move span for the game")
	}
	attrs := map[string]string{}
	for _, kv := range human.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	for k, v := range map[string]string{
		"chess.player.type": "human", "gambit.run.mode": "human", "chess.human.move.class": ClassBlunder,
		"chess.move.san": "f3", "chess.ply": "1", "chess.human.eval.best_move": "e4",
	} {
		if attrs[k] != v {
			t.Errorf("human span %s = %q, want %q", k, attrs[k], v)
		}
	}
	for k := range attrs {
		if strings.HasPrefix(k, "gen_ai.") || k == "chess.move.class" || strings.HasPrefix(k, "chess.eval.") {
			t.Errorf("human span carries the agent's attribute %s", k)
		}
	}
	for _, sp := range recorder.Ended() {
		if sp.SpanContext().TraceID() == human.SpanContext().TraceID() && sp.Name() != "chess.human_move" {
			t.Errorf("the human's trace also holds %q", sp.Name())
		}
	}
}

// A grader failure only hides the grade: the game goes on and the dataset is unaffected.
func TestLiveGraderFailureOnAHumanMoveOnlyHidesTheGrade(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) { c.Grader = failingGrader{failSAN: "e4"} })
	s, _ := f.live.Start(StartOptions{})
	turn := waitFor(t, s, 0, "human_turn", nil)
	if _, err := s.Submit("e2e4"); err != nil {
		t.Fatal(err)
	}
	g := waitFor(t, s, turn.ID, "human_graded", nil)
	if g.data["failed"] != true || g.data["class"] != nil {
		t.Errorf("human_graded = %v, want failed and no class", g.data)
	}
	am := waitFor(t, s, turn.ID, "agent_moved", nil)
	waitFor(t, s, am.ID, "agent_graded", nil)
	waitFor(t, s, am.ID, "human_turn", nil) // the game carries on
	s.Resign()
	over := waitFor(t, s, am.ID, "game_over", nil)
	saved := waitFor(t, s, over.ID, "game_saved", nil)
	if saved.data["saved"] != true {
		t.Errorf("game_saved = %v", saved.data)
	}
}

// The agent's grade comes with the same plain-language note.
func TestLiveAgentBlunderComesWithACoachNote(t *testing.T) {
	f := newLive(t, func(c *LiveConfig) {
		c.Grader = fakeGrader{blunderSAN: "e5", bestSAN: "d5"}
		c.NewClient = func(int64, llm.Brain) llm.Client { return llm.Instrument(&scriptedClient{moves: []string{"e5"}}) }
		c.Agent.Tools = false
	})
	s, _ := f.live.Start(StartOptions{})
	turn := waitFor(t, s, 0, "human_turn", nil)
	s.Submit("e2e4")
	am := waitFor(t, s, turn.ID, "agent_moved", nil)
	gr := waitFor(t, s, am.ID, "agent_graded", nil)
	if gr.data["class"] != ClassBlunder || gr.data["win_pct_lost"].(float64) != 25 {
		t.Errorf("agent_graded = %v", gr.data)
	}
	if gr.data["coach"] == nil {
		t.Errorf("no coach note on the agent's blunder: %v", gr.data)
	}
	s.Abort()
	<-s.Done()
}
