// Package web serves the on-stage UI: a replay of recorded games (board, risk
// timeline, a one-click jump from any move into its trace) and, when enabled,
// human mode, where a person plays the agent live.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/notnil/chess"

	"github.com/sonalgaud12/observability-gambit/internal/analysis"
	"github.com/sonalgaud12/observability-gambit/internal/dataset"
	"github.com/sonalgaud12/observability-gambit/internal/game"
)

//go:embed static
var static embed.FS

// Options for the server.
type Options struct {
	Addr      string
	DataDir   string
	ModelPath string
	TraceURL  string // e.g. http://localhost:16686/trace/{trace_id}

	// Live, if set, enables human mode (see internal/game/live.go). The
	// caller owns it and closes it after Serve returns.
	Live *game.Live
	// TraceDelay is how long after a live move is graded its trace link is
	// offered: with the default Collector, a trace reaches Jaeger only after
	// the tail sampler's decision_wait.
	TraceDelay time.Duration
	// Ctx, if set, stops the server when it is done.
	Ctx context.Context
}

type server struct {
	opts   Options
	bundle *analysis.Bundle

	mu    sync.RWMutex // guards games and moves: human mode adds to them while serving
	games map[string]dataset.GameRecord
	moves map[string][]dataset.MoveRecord
}

// Serve blocks serving the UI.
func Serve(o Options) error {
	s, h, err := newServer(o)
	if err != nil {
		return err
	}
	log.Printf("demo UI on http://%s (%d games)", o.Addr, len(s.games))
	if o.Live != nil {
		log.Printf("human mode on http://%s/live.html", o.Addr)
	}
	srv := &http.Server{Addr: o.Addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if o.Ctx != nil {
		go func() {
			<-o.Ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
	}
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newServer(o Options) (*server, http.Handler, error) {
	gs, err := dataset.LoadGames(o.DataDir)
	if err != nil && !(o.Live != nil && errors.Is(err, os.ErrNotExist)) {
		return nil, nil, err // a live setup may start with no recorded games
	}
	ms, err := dataset.LoadMoves(o.DataDir)
	if err != nil && !(o.Live != nil && errors.Is(err, os.ErrNotExist)) {
		return nil, nil, err
	}
	s := &server{opts: o, games: map[string]dataset.GameRecord{}, moves: analysis.ByGame(ms)}
	for _, g := range gs {
		s.games[g.GameID] = g
	}
	if o.ModelPath != "" {
		if b, err := analysis.LoadBundle(o.ModelPath); err == nil {
			s.bundle = b
		} else {
			log.Printf("no model loaded (%v): risk scores disabled; run `gambit analyze` first", err)
		}
	}

	sub, _ := fs.Sub(static, "static")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/config", s.config)
	mux.HandleFunc("/api/games", s.listGames)
	mux.HandleFunc("/api/game", s.game)
	if o.Live != nil {
		o.Live.SetOnGame(s.addGame)
		s.liveRoutes(mux)
	}
	return s, mux, nil
}

// addGame makes a game finished in human mode available to the replay UI.
func (s *server) addGame(g dataset.GameRecord, moves []dataset.MoveRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.games[g.GameID] = g
	s.moves[g.GameID] = moves
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) config(w http.ResponseWriter, _ *http.Request) {
	cfg := map[string]any{"trace_url": s.opts.TraceURL, "models": nil, "live": nil}
	if s.opts.Live != nil {
		cfg["live"] = map[string]any{"info": s.opts.Live.Info(), "trace_delay_ms": s.opts.TraceDelay.Milliseconds()}
	}
	if s.bundle != nil {
		th := map[string]any{}
		for name, m := range s.bundle.Models {
			th[name] = map[string]any{"threshold": m.Threshold, "test_auc": m.TestAUC, "description": m.Description}
		}
		cfg["models"] = th
		cfg["label"] = s.bundle.Label
		cfg["mock"] = s.bundle.Mock
	}
	writeJSON(w, cfg)
}

type gameSummary struct {
	dataset.GameRecord
	WorstDrop float64 `json:"worst_drop"`
	WorstMove string  `json:"worst_move"`
	WorstAt   int     `json:"worst_at"`
}

func (s *server) listGames(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]gameSummary, 0, len(s.games))
	for id, g := range s.games {
		gs := gameSummary{GameRecord: g}
		gs.PGN = ""
		for _, m := range s.moves[id] {
			if m.WinChanceDrop > gs.WorstDrop {
				gs.WorstDrop, gs.WorstMove, gs.WorstAt = m.WinChanceDrop, m.SAN, m.MoveNumber
			}
		}
		out = append(out, gs)
	}
	// Most dramatic first: the best stage material.
	sort.Slice(out, func(a, b int) bool { return out[a].WorstDrop > out[b].WorstDrop })
	writeJSON(w, out)
}

type plyView struct {
	Ply      int     `json:"ply"`
	SAN      string  `json:"san"`
	UCI      string  `json:"uci"`
	FENAfter string  `json:"fen_after"`
	ByAgent  bool    `json:"by_agent"`
	Agent    *agentV `json:"agent,omitempty"`
}

type agentV struct {
	dataset.MoveRecord
	Risk map[string]float64 `json:"risk"`
}

func (s *server) game(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s.mu.RLock()
	g, ok := s.games[id]
	moves := s.moves[id]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown game", http.StatusNotFound)
		return
	}
	pgnOpt, err := chess.PGN(strings.NewReader(g.PGN))
	if err != nil {
		http.Error(w, fmt.Sprintf("bad pgn: %v", err), http.StatusInternalServerError)
		return
	}
	cg := chess.NewGame(pgnOpt)

	risk := map[string][]float64{}
	if s.bundle != nil {
		for name, m := range s.bundle.Models {
			risk[name] = m.ScoreGame(moves)
		}
	}
	byPly := map[int]int{}
	for i, m := range moves {
		byPly[m.Ply] = i
	}

	positions := cg.Positions()
	var plies []plyView
	for i, mv := range cg.Moves() {
		pv := plyView{
			Ply:      i + 1,
			SAN:      chess.AlgebraicNotation{}.Encode(positions[i], mv),
			UCI:      chess.UCINotation{}.Encode(positions[i], mv),
			FENAfter: positions[i+1].String(),
		}
		if k, ok := byPly[i+1]; ok {
			av := &agentV{MoveRecord: moves[k], Risk: map[string]float64{}}
			for name, rs := range risk {
				av.Risk[name] = rs[k]
			}
			pv.ByAgent, pv.Agent = true, av
		}
		plies = append(plies, pv)
	}
	g.PGN = ""
	writeJSON(w, map[string]any{"game": g, "start_fen": positions[0].String(), "plies": plies})
}
