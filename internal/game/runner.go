// Package game runs agent-vs-engine games, grades every agent move, and emits
// one trace per agent move.
//
// Trace shape (one trace per agent move, tied together by gen_ai.conversation.id):
//
//	chess.move                       root: chess.* context + the label (for tail sampling)
//	├── invoke_agent chess-agent     the decision; its duration is the latency feature
//	│   ├── chat {model}             gen_ai.* attributes, tokens, finish reason
//	│   ├── execute_tool threats     optional tool calls
//	│   ├── chess.validate_move      illegal? -> another chat span follows
//	│   └── ...
//	└── chess.evaluate               Stockfish grading at fixed depth (label only)
package game

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/notnil/chess"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/dataset"
	"github.com/sonalgaud12/observability-gambit/internal/engine"
	"github.com/sonalgaud12/observability-gambit/internal/llm"
	sc "github.com/sonalgaud12/observability-gambit/internal/semconv"
)

const instrumentationName = "github.com/sonalgaud12/observability-gambit/internal/game"

// Config for a batch of games.
type Config struct {
	Agent          agent.Config
	NewClient      func(seed int64, brain llm.Brain) llm.Client // returns an instrumented client
	MockBrainSkill int                                          // Stockfish "Skill Level" for the mock's brain; <0 disables
	StockfishPath  string
	Opponent       string // "stockfish" or "random"
	OpponentElo    int
	OpponentMoveMS int
	GraderDepth    int
	MaxPlies       int
	Seed           int64
	Writer         *dataset.Writer
	KeepReasoning  bool
}

// Run plays n games using `parallel` workers.
func Run(ctx context.Context, cfg Config, n, parallel int) error {
	if parallel < 1 {
		parallel = 1
	}
	jobs := make(chan int)
	errs := make(chan error, parallel)
	var done atomic.Int64
	var wg sync.WaitGroup

	for w := 0; w < parallel; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			p, err := newPlayer(cfg, w)
			if err != nil {
				errs <- err
				for range jobs { // drain so the producer never blocks
				}
				return
			}
			defer p.close()
			for i := range jobs {
				if err := p.play(ctx, i); err != nil {
					if ctx.Err() != nil {
						return
					}
					log.Printf("game %d failed: %v", i, err)
					continue
				}
				log.Printf("[%d/%d] games done", done.Add(1), n)
			}
		}(w)
	}

feed:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	return ctx.Err()
}

type player struct {
	cfg      Config
	opponent *engine.Engine
	grader   *Grader
	brain    *engine.Engine
	tracer   trace.Tracer
	rng      *rand.Rand
	worker   int
}

func newPlayer(cfg Config, worker int) (*player, error) {
	p := &player{cfg: cfg, tracer: otel.Tracer(instrumentationName), worker: worker,
		rng: rand.New(rand.NewSource(cfg.Seed + int64(worker)*7919))}

	g, err := engine.Start(cfg.StockfishPath, map[string]string{"Threads": "1", "Hash": "64"})
	if err != nil {
		return nil, err
	}
	p.grader = &Grader{Eng: g, Depth: cfg.GraderDepth}

	if cfg.MockBrainSkill >= 0 {
		b, err := engine.Start(cfg.StockfishPath, map[string]string{"Threads": "1", "Hash": "16", "Skill Level": strconv.Itoa(cfg.MockBrainSkill)})
		if err != nil {
			g.Close()
			return nil, err
		}
		p.brain = b
	}

	if cfg.Opponent == "stockfish" {
		opts := map[string]string{"Threads": "1", "Hash": "16"}
		if cfg.OpponentElo > 0 {
			opts["UCI_LimitStrength"] = "true"
			opts["UCI_Elo"] = strconv.Itoa(cfg.OpponentElo)
		}
		o, err := engine.Start(cfg.StockfishPath, opts)
		if err != nil {
			g.Close()
			return nil, err
		}
		p.opponent = o
	}
	return p, nil
}

func (p *player) close() {
	p.grader.Eng.Close()
	if p.brain != nil {
		p.brain.Close()
	}
	if p.opponent != nil {
		p.opponent.Close()
	}
}

func (p *player) play(ctx context.Context, idx int) error {
	seed := p.cfg.Seed + int64(idx)*104729
	var brain llm.Brain
	if p.brain != nil {
		brain = engineBrain{p.brain}
	}
	client := p.cfg.NewClient(seed, brain)
	ag := agent.New(p.cfg.Agent, client, seed)

	gameID := fmt.Sprintf("g%s-%04d", time.Now().UTC().Format("20060102T150405"), idx)
	agentColor := chess.White
	if idx%2 == 1 {
		agentColor = chess.Black
	}
	if err := p.grader.Eng.NewGame(); err != nil {
		return err
	}
	if p.opponent != nil {
		if err := p.opponent.NewGame(); err != nil {
			return err
		}
	}

	g := chess.NewGame()
	var moves []dataset.MoveRecord
	started := time.Now().UTC()

	for g.Outcome() == chess.NoOutcome && len(g.Moves()) < p.cfg.MaxPlies {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if g.Position().Turn() == agentColor {
			rec, err := p.agentMove(ctx, g, ag, client.Provider(), gameID)
			if err != nil {
				return err
			}
			moves = append(moves, rec)
		} else if err := p.opponentMove(ctx, g); err != nil {
			return err
		}
	}

	res := dataset.GameRecord{
		GameID: gameID, Provider: client.Provider(), Model: p.cfg.Agent.Model,
		AgentColor: colorName(agentColor), OpponentElo: p.cfg.OpponentElo,
		Result: g.Outcome().String(), Method: g.Method().String(), Plies: len(g.Moves()),
		PGN: g.String(), StartedAt: started.Format(time.RFC3339),
	}
	if p.cfg.Opponent != "stockfish" {
		res.OpponentElo = 0
	}
	if g.Outcome() == chess.NoOutcome {
		res.Method = "MaxPlies"
	}
	for _, m := range moves {
		switch m.Class {
		case ClassBlunder:
			res.Blunders++
		case ClassMistake:
			res.Mistakes++
		}
	}
	log.Printf("game %s: agent=%s result=%s (%s) plies=%d blunders=%d mistakes=%d",
		gameID, res.AgentColor, res.Result, res.Method, res.Plies, res.Blunders, res.Mistakes)
	return p.cfg.Writer.WriteGame(res, moves)
}

func (p *player) agentMove(ctx context.Context, g *chess.Game, ag *agent.Agent, provider, gameID string) (dataset.MoveRecord, error) {
	before := g.Position()
	ply := len(g.Moves()) + 1
	moveNo := (ply + 1) / 2
	legal := before.ValidMoves()
	inCheck := lastMoveChecks(g)
	material := materialBalance(before, before.Turn())

	ctx, root := p.tracer.Start(ctx, "chess.move", trace.WithAttributes(
		sc.GenAIConversationID.String(gameID),
		sc.ChessGameID.String(gameID),
		sc.ChessPly.Int(ply),
		sc.ChessMoveNumber.Int(moveNo),
		sc.ChessColor.String(colorName(before.Turn())),
		sc.ChessFEN.String(before.String()),
		sc.ChessLegalMoveCount.Int(len(legal)),
		sc.ChessInCheck.Bool(inCheck),
		sc.ChessMaterialBalance.Int(material),
		sc.GenAIRequestModel.String(p.cfg.Agent.Model),
	))
	defer root.End()
	if provider == "mock" {
		root.SetAttributes(sc.GambitMock.Bool(true))
	}

	// --- the decision ---
	actx, span := p.tracer.Start(ctx, sc.OpInvokeAgent+" "+agent.Name, trace.WithAttributes(
		sc.GenAIOperationName.String(sc.OpInvokeAgent),
		sc.GenAIAgentName.String(agent.Name),
		sc.GenAIProviderName.String(provider),
		sc.GenAIRequestModel.String(p.cfg.Agent.Model),
		sc.GenAIConversationID.String(gameID),
	))
	start := time.Now()
	mv, st, err := ag.Choose(actx, g)
	latency := time.Since(start)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.End()
		return dataset.MoveRecord{}, err
	}
	san := chess.AlgebraicNotation{}.Encode(before, mv)
	uci := chess.UCINotation{}.Encode(before, mv)
	span.SetAttributes(st.Attrs()...)
	span.SetAttributes(sc.ChessMoveSAN.String(san), sc.ChessMoveUCI.String(uci))
	if st.Fallback {
		span.SetStatus(codes.Error, "fell back to a random legal move")
		span.SetAttributes(sc.ErrorType.String("fallback_move"))
	}
	span.End()

	if err := g.Move(mv); err != nil {
		return dataset.MoveRecord{}, fmt.Errorf("apply %s: %w", san, err)
	}

	// --- the label ---
	ectx, ev := p.tracer.Start(ctx, "chess.evaluate", trace.WithAttributes(sc.ChessEngineDepth.Int(p.cfg.GraderDepth)))
	gr, err := p.grader.Grade(ectx, before, mv, g)
	if err != nil {
		ev.RecordError(err)
		ev.SetStatus(codes.Error, err.Error())
		ev.End()
		return dataset.MoveRecord{}, err
	}
	ev.SetAttributes(
		sc.ChessCPLoss.Int(gr.CPLoss),
		sc.ChessWinChanceDrop.Float64(gr.WinChanceDrop),
		sc.ChessMoveClass.String(gr.Class),
		sc.ChessBestMove.String(gr.BestMove),
	)
	ev.End()

	root.SetAttributes(
		sc.ChessMoveSAN.String(san),
		sc.ChessMoveUCI.String(uci),
		sc.ChessIllegalAttempts.Int(st.IllegalAttempts),
		sc.ChessFallback.Bool(st.Fallback),
		sc.ChessEvalBeforeCP.Int(gr.EvalBeforeCP),
		sc.ChessEvalAfterCP.Int(gr.EvalAfterCP),
		sc.ChessCPLoss.Int(gr.CPLoss),
		sc.ChessWinChanceDrop.Float64(gr.WinChanceDrop),
		sc.ChessMoveClass.String(gr.Class),
		sc.ChessBestMove.String(gr.BestMove),
	)

	rec := dataset.MoveRecord{
		GameID: gameID, Ply: ply, MoveNumber: moveNo, Color: colorName(before.Turn()),
		Provider: provider, Model: p.cfg.Agent.Model,
		TraceID:   root.SpanContext().TraceID().String(),
		FENBefore: before.String(), SAN: san, UCI: uci,

		LegalMoveCount: len(legal), InCheck: inCheck, MaterialBalance: material,

		LatencyMS: float64(latency.Microseconds()) / 1000, ChatCalls: st.ChatCalls,
		InputTokens: st.InputTokens, OutputTokens: st.OutputTokens, ReasoningTokens: st.ReasoningTokens,
		ToolCalls: st.ToolCalls, IllegalAttempts: st.IllegalAttempts,
		FinishLength: st.FinishLength, Fallback: st.Fallback, LLMErrors: st.LLMErrors,

		EvalBeforeCP: gr.EvalBeforeCP, EvalAfterCP: gr.EvalAfterCP, CPLoss: gr.CPLoss,
		WinChanceDrop: gr.WinChanceDrop, Class: gr.Class, BestMove: gr.BestMove,
	}
	if p.cfg.KeepReasoning {
		rec.Reasoning = truncate(st.Reasoning, 4000)
	}
	return rec, nil
}

func (p *player) opponentMove(ctx context.Context, g *chess.Game) error {
	pos := g.Position()
	if p.opponent == nil {
		moves := pos.ValidMoves()
		return g.Move(moves[p.rng.Intn(len(moves))])
	}
	ms := p.cfg.OpponentMoveMS
	if ms <= 0 {
		ms = 100
	}
	r, err := p.opponent.Search(ctx, pos.String(), fmt.Sprintf("movetime %d", ms))
	if err != nil {
		return err
	}
	mv, err := chess.UCINotation{}.Decode(pos, r.BestMove)
	if err != nil {
		return fmt.Errorf("opponent move %q: %w", r.BestMove, err)
	}
	return g.Move(mv)
}

// engineBrain adapts a weakened engine to llm.Brain for the mock model.
type engineBrain struct{ e *engine.Engine }

func (b engineBrain) Move(ctx context.Context, fen string) (string, error) {
	r, err := b.e.Search(ctx, fen, "movetime 30")
	return r.BestMove, err
}

var values = map[chess.PieceType]int{chess.Pawn: 1, chess.Knight: 3, chess.Bishop: 3, chess.Rook: 5, chess.Queen: 9}

func materialBalance(pos *chess.Position, side chess.Color) int {
	bal := 0
	for _, pc := range pos.Board().SquareMap() {
		v := values[pc.Type()]
		if pc.Color() == side {
			bal += v
		} else {
			bal -= v
		}
	}
	return bal
}

func lastMoveChecks(g *chess.Game) bool {
	ms := g.Moves()
	return len(ms) > 0 && ms[len(ms)-1].HasTag(chess.Check)
}

func colorName(c chess.Color) string {
	if c == chess.Black {
		return "black"
	}
	return "white"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
