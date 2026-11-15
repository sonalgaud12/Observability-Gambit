package game

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"

	"github.com/notnil/chess"

	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/engine"
)

// Opponent plays the side the agent is not playing.
type Opponent interface {
	// Kind names the opponent: "stockfish", "random" or "human".
	Kind() string
	// NewGame resets any per-game state.
	NewGame() error
	// Move returns a legal move for the side to move in g. It may block (a
	// human takes as long as they like) and must return when ctx ends.
	Move(ctx context.Context, g *chess.Game) (*chess.Move, error)
	Close()
}

// engineOpponent is Stockfish at a fixed think time and (optionally) Elo.
type engineOpponent struct {
	eng    *engine.Engine
	moveMS int
}

func (o *engineOpponent) Kind() string   { return "stockfish" }
func (o *engineOpponent) NewGame() error { return o.eng.NewGame() }
func (o *engineOpponent) Close()         { o.eng.Close() }

func (o *engineOpponent) Move(ctx context.Context, g *chess.Game) (*chess.Move, error) {
	ms := o.moveMS
	if ms <= 0 {
		ms = 100
	}
	pos := g.Position()
	r, err := o.eng.Search(ctx, pos.String(), fmt.Sprintf("movetime %d", ms))
	if err != nil {
		return nil, err
	}
	mv, err := chess.UCINotation{}.Decode(pos, r.BestMove)
	if err != nil {
		return nil, fmt.Errorf("opponent move %q: %w", r.BestMove, err)
	}
	return mv, nil
}

// randomOpponent plays a uniformly random legal move.
type randomOpponent struct{ rng *rand.Rand }

func (o *randomOpponent) Kind() string   { return "random" }
func (o *randomOpponent) NewGame() error { return nil }
func (o *randomOpponent) Close()         {}

func (o *randomOpponent) Move(_ context.Context, g *chess.Game) (*chess.Move, error) {
	moves := g.Position().ValidMoves()
	return moves[o.rng.Intn(len(moves))], nil
}

// Errors returned by HumanOpponent.Submit.
var (
	ErrNotYourTurn = errors.New("it is not your turn")
	ErrIllegalMove = errors.New("illegal move")
	// ErrResigned is returned by HumanOpponent.Move after Resign.
	ErrResigned = errors.New("the human resigned")
)

// HumanOpponent is fed moves from outside (the HTTP API): Move blocks until
// Submit supplies a legal move for the position being waited on.
type HumanOpponent struct {
	mu   sync.Mutex
	pos  *chess.Position // the position awaiting a move; nil when it is not the human's turn
	in   chan humanInput
	done bool
}

type humanInput struct {
	mv     *chess.Move
	resign bool
}

// NewHumanOpponent returns an opponent that waits for Submit.
func NewHumanOpponent() *HumanOpponent {
	return &HumanOpponent{in: make(chan humanInput, 1)}
}

func (h *HumanOpponent) Kind() string   { return "human" }
func (h *HumanOpponent) NewGame() error { return nil }
func (h *HumanOpponent) Close()         {}

// Move implements Opponent.
func (h *HumanOpponent) Move(ctx context.Context, g *chess.Game) (*chess.Move, error) {
	h.mu.Lock()
	if h.done {
		h.mu.Unlock()
		return nil, ErrResigned
	}
	h.pos = g.Position()
	select { // drop an input that arrived for an earlier turn
	case <-h.in:
	default:
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		h.pos = nil
		h.mu.Unlock()
	}()
	select {
	case in := <-h.in:
		if in.resign {
			return nil, ErrResigned
		}
		return in.mv, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// HumanMove is an accepted human move in both notations.
type HumanMove struct{ SAN, UCI string }

// Submit validates a move (UCI like "e2e4" or "e7e8q", or SAN like "Nf3")
// against the position the game is waiting on and hands it to Move. A move
// that is illegal, or sent when it is not the human's turn, is rejected and
// the game keeps waiting.
func (h *HumanOpponent) Submit(s string) (HumanMove, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pos == nil {
		return HumanMove{}, ErrNotYourTurn
	}
	mv := agent.ParseMove(h.pos, s)
	if mv == nil {
		return HumanMove{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
	}
	played := HumanMove{
		SAN: chess.AlgebraicNotation{}.Encode(h.pos, mv),
		UCI: chess.UCINotation{}.Encode(h.pos, mv),
	}
	h.pos = nil // one move per turn
	h.in <- humanInput{mv: mv}
	return played, nil
}

// Resign ends the wait with ErrResigned. It is a no-op when it is not the
// human's turn; the game loop checks for a resignation at its next turn.
func (h *HumanOpponent) Resign() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.done = true
	select {
	case h.in <- humanInput{resign: true}:
	default:
	}
}

// Resigned reports whether Resign was called.
func (h *HumanOpponent) Resigned() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.done
}

// Waiting reports whether a move is being awaited right now.
func (h *HumanOpponent) Waiting() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pos != nil
}
