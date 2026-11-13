// Package dataset defines the per-move table that joins trace features with
// the engine label, plus JSONL read/write helpers.
//
// The same data also lives in the traces themselves (and in ClickHouse via the
// Collector); the JSONL copy makes the analysis reproducible without a
// running backend.
package dataset

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ModeHuman is the Mode value of games (and their moves) played by a person
// from the stage UI. Every other game leaves Mode empty: agent vs. an engine.
const ModeHuman = "human"

// MoveRecord is one agent move: features known before the move is played,
// plus the label computed after.
type MoveRecord struct {
	GameID     string `json:"game_id"`
	Ply        int    `json:"ply"`
	MoveNumber int    `json:"move_number"`
	Color      string `json:"color"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	TraceID    string `json:"trace_id"`
	FENBefore  string `json:"fen_before"`
	SAN        string `json:"san"`
	UCI        string `json:"uci"`

	// Position features (a no-telemetry baseline).
	LegalMoveCount  int  `json:"legal_move_count"`
	InCheck         bool `json:"in_check"`
	MaterialBalance int  `json:"material_balance"`

	// Trace features (what observability gives you).
	LatencyMS       float64 `json:"latency_ms"`
	ChatCalls       int     `json:"chat_calls"`
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	ReasoningTokens int     `json:"reasoning_tokens"`
	ToolCalls       int     `json:"tool_calls"`
	IllegalAttempts int     `json:"illegal_attempts"`
	FinishLength    bool    `json:"finish_length"`
	Fallback        bool    `json:"fallback"`
	TimedOut        bool    `json:"timed_out,omitempty"` // the per-move deadline hit (live games); also Fallback
	LLMErrors       int     `json:"llm_errors"`
	Reasoning       string  `json:"reasoning,omitempty"`
	Mode            string  `json:"mode,omitempty"` // "" (engine opponent) or ModeHuman

	// Label (ground truth from the engine). Never a feature.
	EvalBeforeCP  int     `json:"eval_before_cp"`
	EvalAfterCP   int     `json:"eval_after_cp"`
	CPLoss        int     `json:"cp_loss"`
	WinChanceDrop float64 `json:"win_chance_drop"`
	Class         string  `json:"class"`
	BestMove      string  `json:"best_move"`
}

// GameRecord summarizes one finished game.
type GameRecord struct {
	GameID      string `json:"game_id"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	AgentColor  string `json:"agent_color"`
	OpponentElo int    `json:"opponent_elo"`
	Result      string `json:"result"`
	Method      string `json:"method"`
	Plies       int    `json:"plies"`
	PGN         string `json:"pgn"`
	StartedAt   string `json:"started_at"`
	Blunders    int    `json:"blunders"`
	Mistakes    int    `json:"mistakes"`
	Mode        string `json:"mode,omitempty"` // "" (engine opponent) or ModeHuman
}

// Writer appends records to moves.jsonl and games.jsonl in a directory.
type Writer struct {
	mu    sync.Mutex
	moves *os.File
	games *os.File
}

// NewWriter opens (or creates) the dataset files in dir.
func NewWriter(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m, err := os.OpenFile(filepath.Join(dir, "moves.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	g, err := os.OpenFile(filepath.Join(dir, "games.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{moves: m, games: g}, nil
}

// WriteGame writes a game and all its moves atomically with respect to other
// workers, so a crash never leaves moves without their game.
func (w *Writer) WriteGame(g GameRecord, moves []MoveRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	enc := json.NewEncoder(w.moves)
	for _, m := range moves {
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	return json.NewEncoder(w.games).Encode(g)
}

// Close closes both files.
func (w *Writer) Close() error {
	e1 := w.moves.Close()
	e2 := w.games.Close()
	if e1 != nil {
		return e1
	}
	return e2
}

// LoadMoves reads moves.jsonl from dir.
func LoadMoves(dir string) ([]MoveRecord, error) {
	return loadJSONL[MoveRecord](filepath.Join(dir, "moves.jsonl"))
}

// LoadGames reads games.jsonl from dir.
func LoadGames(dir string) ([]GameRecord, error) {
	return loadJSONL[GameRecord](filepath.Join(dir, "games.jsonl"))
}

func loadJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	line := 0
	for sc.Scan() {
		line++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}
