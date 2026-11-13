package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/notnil/chess"

	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/dataset"
	"github.com/sonalgaud12/observability-gambit/internal/game"
	"github.com/sonalgaud12/observability-gambit/internal/llm"
)

// fakeGrader stands in for Stockfish so the test needs no engine installed.
type fakeGrader struct{}

func (fakeGrader) NewGame() error { return nil }
func (fakeGrader) Close()         {}
func (fakeGrader) Grade(context.Context, *chess.Position, *chess.Move, *chess.Game) (game.Grade, error) {
	return game.Grade{EvalBeforeCP: 15, EvalAfterCP: 5, CPLoss: 10, WinChanceDrop: 0.01, Class: game.ClassGood, BestMove: "Nf3", Depth: 1}, nil
}

type liveServer struct {
	*httptest.Server
	dir  string
	live *game.Live
}

// newLiveServer runs the real handlers with the mock model, no API key and no Stockfish.
func newLiveServer(t *testing.T) *liveServer {
	t.Helper()
	t.Setenv("GAMBIT_MOCK_LATENCY_SCALE", "0.001")
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	live, err := game.NewLive(game.LiveConfig{
		Config: game.Config{
			Agent: agent.Config{Model: "mock-1", Temperature: -1, MaxTokens: 256, MaxRetries: 3, Tools: true},
			NewClient: func(seed int64, brain llm.Brain) llm.Client {
				m := llm.NewMock(seed)
				m.Brain = brain
				return llm.Instrument(m)
			},
			MockBrainSkill: -1, GraderDepth: 1, MaxPlies: 200, Seed: 3, Writer: w, KeepReasoning: true,
			Grader: fakeGrader{},
		},
		MoveTimeout: 5 * time.Second,
		Script:      []string{"e4", "Nf3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(live.Close)
	_, h, err := newServer(Options{Addr: "x", DataDir: dir, TraceURL: "http://jaeger/trace/{trace_id}", Live: live, TraceDelay: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &liveServer{Server: srv, dir: dir, live: live}
}

func (s *liveServer) post(t *testing.T, path string, body any, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, s.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (s *liveServer) get(t *testing.T, path string, into any) int {
	t.Helper()
	resp, err := http.Get(s.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
	}
	return resp.StatusCode
}

type stateEvent struct {
	ID   int            `json:"id"`
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// waitEvent polls the event log until an event of typ (matching pred) appears.
func (s *liveServer) waitEvent(t *testing.T, id, typ string, pred func(map[string]any) bool) stateEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var st struct {
			Events []stateEvent `json:"events"`
		}
		s.get(t, "/api/live/state?id="+id, &st)
		for _, e := range st.Events {
			if e.Type == typ && (pred == nil || pred(e.Data)) {
				return e
			}
		}
		if time.Now().After(deadline) {
			var got []string
			for _, e := range st.Events {
				got = append(got, e.Type)
			}
			t.Fatalf("timed out waiting for %s; events: %v", typ, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func atPly(ply int) func(map[string]any) bool {
	return func(d map[string]any) bool { return d["ply"] == float64(ply) }
}

// TestHumanGameOverHTTP plays a short game through the HTTP API with the mock
// model, the way the stage UI does, then replays it from the recorded data.
func TestHumanGameOverHTTP(t *testing.T) {
	s := newLiveServer(t)

	// The UI learns from /api/config that human mode is on.
	var cfg struct {
		Live struct {
			Info struct {
				Model         string   `json:"model"`
				Mock          bool     `json:"mock"`
				MoveTimeoutMS int      `json:"move_timeout_ms"`
				Script        []string `json:"script"`
			} `json:"info"`
			TraceDelayMS int `json:"trace_delay_ms"`
		} `json:"live"`
	}
	s.get(t, "/api/config", &cfg)
	if !cfg.Live.Info.Mock || cfg.Live.Info.MoveTimeoutMS != 5000 || cfg.Live.TraceDelayMS != 20000 || len(cfg.Live.Info.Script) != 2 {
		t.Errorf("config = %+v", cfg)
	}

	code, out := s.post(t, "/api/live/new", map[string]any{"human_color": "white"}, nil)
	if code != 200 {
		t.Fatalf("new: %d %v", code, out)
	}
	id := out["id"].(string)

	start := s.waitEvent(t, id, "game_started", nil)
	if start.Data["human_color"] != "white" || start.Data["model"] != "mock-1" || start.Data["move_timeout_ms"] != float64(5000) {
		t.Errorf("game_started = %v", start.Data)
	}
	turn := s.waitEvent(t, id, "human_turn", atPly(1))
	if turn.Data["scripted"] != "e4" {
		t.Errorf("scripted = %v", turn.Data["scripted"])
	}

	// Illegal moves, and moves for the wrong game, are refused with a reason.
	if code, out := s.post(t, "/api/live/move", map[string]any{"id": id, "move": "e2e5"}, nil); code != 422 || out["error"] == nil {
		t.Errorf("illegal move: %d %v", code, out)
	}
	if code, _ := s.post(t, "/api/live/move", map[string]any{"id": "nope", "move": "e2e4"}, nil); code != 404 {
		t.Errorf("unknown game: %d", code)
	}
	if code, _ := s.post(t, "/api/live/move", "not an object", nil); code != 400 {
		t.Errorf("bad body: %d", code)
	}

	// The scripted move, through the normal path.
	code, out = s.post(t, "/api/live/move", map[string]any{"id": id, "move": "e4"}, nil)
	if code != 200 || out["san"] != "e4" || out["uci"] != "e2e4" {
		t.Fatalf("move: %d %v", code, out)
	}
	if code, _ := s.post(t, "/api/live/move", map[string]any{"id": id, "move": "d2d4"}, nil); code != 409 {
		t.Errorf("a second move before the agent replied: %d, want 409", code)
	}

	moved := s.waitEvent(t, id, "agent_moved", atPly(2))
	if moved.Data["trace_id"] == "" || moved.Data["chat_calls"].(float64) < 1 || moved.Data["fen_after"] == "" {
		t.Errorf("agent_moved = %v", moved.Data)
	}
	graded := s.waitEvent(t, id, "agent_graded", atPly(2))
	if graded.Data["class"] != game.ClassGood {
		t.Errorf("agent_graded = %v", graded.Data)
	}

	// Ply 3: the human plays a second move, the agent answers, then the human resigns.
	s.waitEvent(t, id, "human_turn", atPly(3))
	if code, out := s.post(t, "/api/live/move", map[string]any{"id": id, "move": "Nf3"}, nil); code != 200 {
		t.Fatalf("move 2: %d %v", code, out)
	}
	s.waitEvent(t, id, "agent_moved", atPly(4))
	s.waitEvent(t, id, "human_turn", atPly(5))
	if code, _ := s.post(t, "/api/live/resign", map[string]any{"id": id}, nil); code != 200 {
		t.Fatalf("resign: %d", code)
	}
	over := s.waitEvent(t, id, "game_over", nil)
	if over.Data["result"] != "0-1" || over.Data["method"] != "Resignation" || over.Data["plies"] != float64(4) {
		t.Errorf("game_over = %v", over.Data)
	}
	saved := s.waitEvent(t, id, "game_saved", nil)
	if saved.Data["saved"] != true {
		t.Fatalf("game_saved = %v", saved.Data)
	}

	// The game is in the dataset, marked as human, with both agent moves graded.
	moves, err := dataset.LoadMoves(s.dir)
	if err != nil || len(moves) != 2 {
		t.Fatalf("moves: %v, %v", moves, err)
	}
	for _, m := range moves {
		if m.Mode != dataset.ModeHuman || m.GameID != id || m.Class != game.ClassGood || m.TraceID == "" {
			t.Errorf("move record = %+v", m)
		}
	}

	// ...and the replay UI can play it back straight away.
	var list []struct {
		GameID string `json:"game_id"`
		Mode   string `json:"mode"`
	}
	s.get(t, "/api/games", &list)
	if len(list) != 1 || list[0].GameID != id || list[0].Mode != "human" {
		t.Errorf("games = %+v", list)
	}
	var replay struct {
		Plies []struct {
			SAN     string `json:"san"`
			ByAgent bool   `json:"by_agent"`
		} `json:"plies"`
	}
	if code := s.get(t, "/api/game?id="+id, &replay); code != 200 || len(replay.Plies) != 4 {
		t.Fatalf("replay: %d %+v", code, replay)
	}
	if replay.Plies[0].SAN != "e4" || replay.Plies[0].ByAgent || !replay.Plies[1].ByAgent || !replay.Plies[3].ByAgent {
		t.Errorf("replay plies = %+v", replay.Plies)
	}
}

type sseEvent struct {
	id, typ string
	data    map[string]any
}

// readSSE reads a stream until the server closes it.
func readSSE(t *testing.T, req *http.Request) []sseEvent {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var out []sseEvent
	var cur sseEvent
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.typ != "" {
				out = append(out, cur)
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, "id: "):
			cur.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			cur.typ = line[7:]
		case strings.HasPrefix(line, "data: "):
			_ = json.Unmarshal([]byte(line[6:]), &cur.data)
		}
	}
	return out
}

// TestEventStreamResumes: the stream is the same log the state endpoint
// shows, ids count up from 1, and a reconnect with Last-Event-ID gets exactly
// the events it missed.
func TestEventStreamResumes(t *testing.T) {
	s := newLiveServer(t)
	_, out := s.post(t, "/api/live/new", map[string]any{}, nil)
	id := out["id"].(string)
	s.waitEvent(t, id, "human_turn", atPly(1))
	s.post(t, "/api/live/move", map[string]any{"id": id, "move": "e2e4"}, nil)
	s.waitEvent(t, id, "human_turn", atPly(3))
	s.post(t, "/api/live/resign", map[string]any{"id": id}, nil)
	s.waitEvent(t, id, "game_saved", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	newReq := func(last string) *http.Request {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/api/live/events?id="+id, nil)
		if last != "" {
			req.Header.Set("Last-Event-ID", last)
		}
		return req
	}
	all := readSSE(t, newReq(""))
	if len(all) < 8 || all[0].typ != "game_started" || all[len(all)-1].typ != "game_saved" {
		t.Fatalf("stream = %v", all)
	}
	for i, e := range all {
		if e.id != strconv.Itoa(i+1) {
			t.Fatalf("event %d has id %s", i+1, e.id)
		}
	}
	rest := readSSE(t, newReq("3"))
	if len(rest) != len(all)-3 || rest[0].id != "4" || rest[0].typ != all[3].typ {
		t.Errorf("resume from 3 gave %d events starting at %v, want %d starting at %s", len(rest), rest[0].id, len(all)-3, all[3].typ)
	}
	if none := readSSE(t, newReq(strconv.Itoa(len(all)))); len(none) != 0 {
		t.Errorf("resuming at the end gave %d events", len(none))
	}

	// Unknown game.
	resp, _ := http.Get(s.URL + "/api/live/events?id=nope")
	if resp.StatusCode != 404 {
		t.Errorf("unknown game: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestLiveAPIRefusesOtherOrigins: a page on another origin must not be able to
// start games (and spend model credits) on the stage laptop.
func TestLiveAPIRefusesOtherOrigins(t *testing.T) {
	s := newLiveServer(t)
	if code, _ := s.post(t, "/api/live/new", map[string]any{}, map[string]string{"Origin": "http://evil.example"}); code != 403 {
		t.Errorf("cross-origin new: %d, want 403", code)
	}
	if code, _ := s.post(t, "/api/live/new", map[string]any{}, map[string]string{"Origin": s.URL}); code != 200 {
		t.Errorf("same-origin new: %d, want 200", code)
	}
	resp, err := http.Get(s.URL + "/api/live/new")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET new: %d, want 405", resp.StatusCode)
	}
	if code, _ := s.post(t, "/api/live/new", map[string]any{"human_color": "purple"}, nil); code != 400 {
		t.Errorf("bad colour: %d, want 400", code)
	}
}

// Without --live there are no live routes at all, and replay is unchanged.
func TestReplayOnlyServerHasNoLiveAPI(t *testing.T) {
	dir := t.TempDir()
	w, _ := dataset.NewWriter(dir)
	w.Close()
	_, h, err := newServer(Options{Addr: "x", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/api/live/new", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 404 {
		t.Errorf("live API without --live: %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	var cfg map[string]any
	resp, _ = http.Get(srv.URL + "/api/config")
	_ = json.NewDecoder(resp.Body).Decode(&cfg)
	resp.Body.Close()
	if cfg["live"] != nil {
		t.Errorf("config advertises human mode: %v", cfg["live"])
	}
}
