package game

import (
	"context"
	"fmt"
	"math"

	"github.com/notnil/chess"

	"github.com/sonalgaud12/observability-gambit/internal/engine"
)

// Move classes, ordered from best to worst.
const (
	ClassBest       = "best"
	ClassGood       = "good"
	ClassInaccuracy = "inaccuracy"
	ClassMistake    = "mistake"
	ClassBlunder    = "blunder"
)

// Grade is the ground-truth label for one move.
type Grade struct {
	EvalBeforeCP  int     `json:"eval_before_cp"` // mover's perspective
	EvalAfterCP   int     `json:"eval_after_cp"`  // mover's perspective
	CPLoss        int     `json:"cp_loss"`
	WinChanceDrop float64 `json:"win_chance_drop"` // drop on the [-1,1] winning-chances scale
	Class         string  `json:"class"`
	BestMove      string  `json:"best_move"` // SAN
	Depth         int     `json:"depth"`
}

// WinningChances maps centipawns to [-1, 1] using the same logistic curve
// Lichess uses for move annotations.
func WinningChances(cp int) float64 {
	c := math.Max(-1000, math.Min(1000, float64(cp)))
	return 2/(1+math.Exp(-0.00368208*c)) - 1
}

// Classify uses Lichess-style thresholds on the drop in winning chances,
// which behaves better than raw centipawn loss in already-decided positions.
func Classify(drop float64, playedBest bool) string {
	switch {
	case drop >= 0.3:
		return ClassBlunder
	case drop >= 0.2:
		return ClassMistake
	case drop >= 0.1:
		return ClassInaccuracy
	case playedBest:
		return ClassBest
	default:
		return ClassGood
	}
}

// Grader evaluates moves with a full-strength engine at a fixed depth.
type Grader struct {
	Eng   *engine.Engine
	Depth int
}

// Grade scores the move that took the game from `before` to `after`.
func (g *Grader) Grade(ctx context.Context, before *chess.Position, played *chess.Move, after *chess.Game) (Grade, error) {
	goArgs := fmt.Sprintf("depth %d", g.Depth)
	rb, err := g.Eng.Search(ctx, before.String(), goArgs)
	if err != nil {
		return Grade{}, fmt.Errorf("grade before: %w", err)
	}

	var afterCP int
	switch after.Method() {
	case chess.Checkmate:
		afterCP = engine.MateCP
	case chess.Stalemate, chess.InsufficientMaterial, chess.ThreefoldRepetition,
		chess.FivefoldRepetition, chess.FiftyMoveRule, chess.SeventyFiveMoveRule:
		afterCP = 0
	default:
		ra, err := g.Eng.Search(ctx, after.Position().String(), goArgs)
		if err != nil {
			return Grade{}, fmt.Errorf("grade after: %w", err)
		}
		afterCP = -ra.CP // flip to the mover's perspective
	}

	gr := Grade{EvalBeforeCP: rb.CP, EvalAfterCP: afterCP, Depth: rb.Depth}
	gr.CPLoss = max(0, rb.CP-afterCP)
	gr.WinChanceDrop = math.Max(0, WinningChances(rb.CP)-WinningChances(afterCP))

	playedUCI := chess.UCINotation{}.Encode(before, played)
	if bm, err := (chess.UCINotation{}).Decode(before, rb.BestMove); err == nil && rb.BestMove != "" {
		gr.BestMove = chess.AlgebraicNotation{}.Encode(before, bm)
	}
	gr.Class = Classify(gr.WinChanceDrop, playedUCI == rb.BestMove)
	return gr, nil
}
