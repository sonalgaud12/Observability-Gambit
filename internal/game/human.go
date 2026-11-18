package game

import (
	"context"
	"log"

	"github.com/notnil/chess"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	sc "github.com/sonalgaud12/observability-gambit/internal/semconv"
)

// gradeHuman grades one move the human played and publishes "human_graded".
//
// It runs in the background so the board never waits for Stockfish. It uses
// the same grader and classes as the agent's moves, but nothing else about an
// agent move: the grade is not written to the dataset (so the analysis never
// counts it) and it gets its own small trace, `chess.human_move`, marked
// chess.player.type=human. That trace has no gen_ai.* attributes and no
// chess.move.class, so it is not an agent span and not an agent result.
//
// A grader failure is logged and published as {failed: true}; the move itself
// is unaffected.
func (s *Session) gradeHuman(ctx context.Context, before *chess.Position, mv *chess.Move, after *chess.Game, ply int, san, uci string) {
	p := s.live.p
	gctx, span := p.tracer.Start(ctx, "chess.human_move", trace.WithAttributes(
		sc.ChessGameID.String(s.ID),
		sc.ChessPly.Int(ply),
		sc.ChessMoveNumber.Int((ply+1)/2),
		sc.ChessColor.String(colorName(before.Turn())),
		sc.ChessFEN.String(before.String()),
		sc.ChessMoveSAN.String(san),
		sc.ChessMoveUCI.String(uci),
		sc.ChessPlayerType.String("human"),
		sc.GambitRunMode.String("human"),
	))
	defer span.End()
	if s.player != "" {
		span.SetAttributes(sc.GambitPlayerLabel.String(s.player))
	}

	gr, err := p.grader.Grade(gctx, before, mv, after)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if ctx.Err() == nil {
			log.Printf("live %s: grading your move %d: %v", s.ID, ply, err)
			s.emit("human_graded", map[string]any{"ply": ply, "failed": true})
		}
		return
	}
	span.SetAttributes(
		sc.ChessHumanMoveClass.String(gr.Class),
		sc.ChessHumanCPLoss.Int(gr.CPLoss),
		sc.ChessHumanWinChanceDrop.Float64(gr.WinChanceDrop),
		sc.ChessHumanBestMove.String(gr.BestMove),
	)
	if ctx.Err() != nil {
		return
	}
	ev := map[string]any{
		"ply": ply, "class": gr.Class, "cp_loss": gr.CPLoss,
		"win_chance_drop": gr.WinChanceDrop, "win_pct_lost": WinPercentLost(gr),
		"best_move": gr.BestMove, "eval_before_cp": gr.EvalBeforeCP, "eval_after_cp": gr.EvalAfterCP,
	}
	if note, ok := Coach(before, mv, gr, true); ok {
		ev["coach"] = note
	}
	s.rememberGrade(ply, gr)
	s.emit("human_graded", ev)
	// The coach comments once the grade is out, off the game's critical path.
	s.coachAfterHumanGrade(ply)
}
