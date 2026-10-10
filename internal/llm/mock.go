package llm

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/notnil/chess"
)

// Mock is an offline stand-in for a real model, for rehearsing the pipeline
// without API keys or cost.
//
// WARNING: Mock has a hidden "stress" variable that rises with game length and
// drives BOTH its telemetry (latency, tokens, retries, tool calls) AND its move
// quality. Any correlation you find in mock data was put there on purpose.
// Never present mock results as findings; every mock span carries
// gambit.llm.mock=true and gen_ai.provider.name="mock".
type Mock struct {
	Brain        Brain // optional: a sound move source the mock follows while calm
	mu           sync.Mutex
	rng          *rand.Rand
	persona      float64 // 0.6..1.4: how quickly this "model" degrades
	latencyScale float64
	calls        int
}

// NewMock returns a deterministic mock for the given seed.
func NewMock(seed int64) *Mock {
	r := rand.New(rand.NewSource(seed))
	scale := 0.1
	if v, err := strconv.ParseFloat(os.Getenv("GAMBIT_MOCK_LATENCY_SCALE"), 64); err == nil {
		scale = v
	}
	return &Mock{rng: r, persona: 0.6 + 0.8*r.Float64(), latencyScale: scale}
}

// Brain suggests a reasonable move (UCI) for a FEN, e.g. a weakened engine.
// It keeps mock games alive long enough to rehearse a late-game blunder.
type Brain interface {
	Move(ctx context.Context, fen string) (string, error)
}

func (m *Mock) Provider() string { return "mock" }

var fenLine = regexp.MustCompile(`(?m)^FEN:\s*(.+)$`)

// Chat implements Client.
func (m *Mock) Chat(ctx context.Context, req Request) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++

	var fen string
	for i := len(req.Messages) - 1; i >= 0 && fen == ""; i-- {
		if mm := fenLine.FindStringSubmatch(req.Messages[i].Content); mm != nil {
			fen = strings.TrimSpace(mm[1])
		}
	}
	opt, err := chess.FEN(fen)
	if err != nil {
		return Response{}, fmt.Errorf("mock: no FEN in prompt: %w", err)
	}
	pos := chess.NewGame(opt).Position()
	moves := pos.ValidMoves()

	fullmove := 1
	if f := strings.Fields(fen); len(f) == 6 {
		fullmove, _ = strconv.Atoi(f[5])
	}
	horizon := 55.0
	if m.Brain != nil {
		horizon = 38.0
	}
	stress := clamp01((float64(fullmove)/horizon)*m.persona + 0.15*m.rng.NormFloat64())

	last := req.Messages[len(req.Messages)-1]
	retrying := last.Role == "user" && strings.Contains(strings.ToLower(last.Content), "illegal")
	toolUsed := false
	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			toolUsed = true
		}
	}

	inputTokens := 0
	for _, msg := range req.Messages {
		inputTokens += len(msg.Content) / 4
	}

	// Simulated latency, proportional to how "hard" the model is working.
	work := 0.15 + 0.85*stress*m.rng.Float64() + float64(len(moves))/120
	sleep := time.Duration(work * float64(time.Second) * m.latencyScale)
	select {
	case <-time.After(sleep):
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}

	resp := Response{
		ID:          fmt.Sprintf("mock-%d", m.calls),
		Model:       req.Model,
		InputTokens: inputTokens,
		Message:     Message{Role: "assistant"},
	}

	// Maybe call a tool first.
	if len(req.Tools) > 0 && !toolUsed && m.rng.Float64() < 0.25+0.45*stress {
		name := req.Tools[m.rng.Intn(len(req.Tools))].Name
		resp.Message.ToolCalls = []ToolCall{{ID: fmt.Sprintf("call_%d", m.calls), Name: name, Arguments: "{}"}}
		resp.FinishReason = "tool_calls"
		resp.OutputTokens = 12 + m.rng.Intn(10)
		return resp, nil
	}

	var reasoning strings.Builder
	cands := m.rng.Perm(len(moves))
	nCands := 2 + int(stress*6)
	reasoning.WriteString("Looking at the position. ")
	for i := 0; i < nCands && i < len(cands); i++ {
		san := chess.AlgebraicNotation{}.Encode(pos, moves[cands[i]])
		fmt.Fprintf(&reasoning, "Considering %s; ", san)
	}

	resp.ReasoningTokens = int(40 + 500*stress*m.rng.Float64())

	// Ran out of tokens before answering.
	if m.rng.Float64() < 0.01+0.08*stress {
		resp.Message.Content = reasoning.String()
		resp.FinishReason = "length"
		resp.OutputTokens = req.MaxTokens
		if resp.OutputTokens == 0 {
			resp.OutputTokens = 1024
		}
		return resp, nil
	}

	var move string
	pIllegal := 0.02 + 0.30*stress*stress
	if retrying {
		pIllegal *= 0.5
	}
	if m.rng.Float64() < pIllegal {
		move = m.illegalMove(pos)
	} else {
		move = chess.AlgebraicNotation{}.Encode(pos, m.choose(ctx, pos, moves, stress))
	}
	fmt.Fprintf(&reasoning, "\nMOVE: %s", move)
	resp.Message.Content = reasoning.String()
	resp.FinishReason = "stop"
	resp.OutputTokens = len(resp.Message.Content)/4 + resp.ReasoningTokens
	return resp, nil
}

var pieceValue = map[chess.PieceType]float64{
	chess.Pawn: 1, chess.Knight: 3, chess.Bishop: 3, chess.Rook: 5, chess.Queen: 9,
}

// choose follows the brain while calm and falls back to pick under stress.
func (m *Mock) choose(ctx context.Context, pos *chess.Position, moves []*chess.Move, stress float64) *chess.Move {
	if m.Brain != nil && m.rng.Float64() > math.Pow(stress, 1.5) {
		if uci, err := m.Brain.Move(ctx, pos.String()); err == nil {
			for _, mv := range moves {
				if (chess.UCINotation{}).Encode(pos, mv) == uci {
					return mv
				}
			}
		}
	}
	return m.pick(pos, moves, stress)
}

// pick is a one-ply material player: it grabs material and, when calm, avoids
// leaving the moved piece en prise. Under stress it stops checking safety and
// sometimes plays at random, which is where its blunders come from.
func (m *Mock) pick(pos *chess.Position, moves []*chess.Move, stress float64) *chess.Move {
	if m.rng.Float64() < 0.02+0.35*stress*stress {
		return moves[m.rng.Intn(len(moves))]
	}
	checkSafety := m.rng.Float64() > stress*stress
	board := pos.Board()
	best, bestScore := moves[0], math.Inf(-1)
	for _, mv := range moves {
		s := m.rng.Float64() * 0.8
		gain := 0.0
		if captured := board.Piece(mv.S2()); captured != chess.NoPiece {
			gain = pieceValue[captured.Type()]
			s += gain
		}
		if checkSafety && hangs(pos, mv) {
			s -= pieceValue[board.Piece(mv.S1()).Type()]
		}
		if mv.HasTag(chess.Check) {
			s += 0.5
		}
		if mv.Promo() == chess.Queen {
			s += 8
		}
		if s > bestScore {
			best, bestScore = mv, s
		}
	}
	return best
}

// hangs reports whether the opponent can capture the moved piece right after mv.
func hangs(pos *chess.Position, mv *chess.Move) bool {
	next := pos.Update(mv)
	for _, reply := range next.ValidMoves() {
		if reply.S2() == mv.S2() {
			return true
		}
	}
	return false
}

func (m *Mock) illegalMove(pos *chess.Position) string {
	files := "abcdefgh"
	pieces := []string{"N", "B", "R", "Q", "K", ""}
	for i := 0; i < 50; i++ {
		s := fmt.Sprintf("%s%c%d", pieces[m.rng.Intn(len(pieces))], files[m.rng.Intn(8)], 1+m.rng.Intn(8))
		if _, err := (chess.AlgebraicNotation{}).Decode(pos, s); err != nil {
			return s
		}
	}
	return "Qz9"
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }
