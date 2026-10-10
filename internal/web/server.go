// Package web serves the on-stage replay UI: board, risk timeline, and a
// one-click jump from any move into its trace.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/notnil/chess"

	"github.com/sonalgaud12/observability-gambit/internal/analysis"
	"github.com/sonalgaud12/observability-gambit/internal/dataset"
)

//go:embed static
var static embed.FS

// Options for the server.
type Options struct {
	Addr      string
	DataDir   string
	ModelPath string
	TraceURL  string // e.g. http://localhost:16686/trace/{trace_id}
}

type server struct {
	opts   Options
	games  map[string]dataset.GameRecord
	moves  map[string][]dataset.MoveRecord
	bundle *analysis.Bundle
}

// Serve blocks serving the UI.
func Serve(o Options) error {
	gs, err := dataset.LoadGames(o.DataDir)
	if err != nil {
		return err
	}
	ms, err := dataset.LoadMoves(o.DataDir)
	if err != nil {
		return err
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
	log.Printf("demo UI on http://%s (%d games)", o.Addr, len(s.games))
	return http.ListenAndServe(o.Addr, mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) config(w http.ResponseWriter, _ *http.Request) {
	cfg := map[string]any{"trace_url": s.opts.TraceURL, "models": nil}
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
	g, ok := s.games[id]
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

	moves := s.moves[id]
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
