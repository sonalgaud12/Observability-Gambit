package game

import (
	"fmt"
	"strings"

	"github.com/notnil/chess"
)

// Advice turns "Stockfish preferred another move" into a sentence a beginner
// can use. It is computed from the position alone, with plain rules on top of
// the engine's choice (no model call): was something left to be captured, was
// a checkmate missed or allowed, what does the better move win or attack.

var pieceValue = map[chess.PieceType]int{
	chess.Pawn: 1, chess.Knight: 3, chess.Bishop: 3, chess.Rook: 5, chess.Queen: 9, chess.King: 100,
}

// capture is one legal capture and what it wins on balance.
type capture struct {
	from, to chess.Square
	victim   chess.PieceType
	attacker chess.PieceType
	gain     int // value of the victim, minus the attacker if it can be retaken
	free     bool
	mate     bool
}

// withTurn returns pos with the other side to move, or nil if that position is
// not valid. It answers "what could the side that just moved capture next?".
func withTurn(pos *chess.Position, c chess.Color) *chess.Position {
	parts := strings.Fields(pos.String())
	if len(parts) < 4 {
		return nil
	}
	parts[1] = "w"
	if c == chess.Black {
		parts[1] = "b"
	}
	parts[3] = "-"
	opt, err := chess.FEN(strings.Join(parts, " "))
	if err != nil {
		return nil
	}
	return chess.NewGame(opt).Position()
}

// captures lists what the side to move in pos can win by capturing: pieces
// that would be taken for nothing, or that are worth more than the piece that
// takes them. Best first.
func captures(pos *chess.Position) []capture {
	if pos == nil {
		return nil
	}
	var out []capture
	for _, mv := range pos.ValidMoves() {
		if !mv.HasTag(chess.Capture) || mv.HasTag(chess.EnPassant) {
			continue
		}
		victim := pos.Board().Piece(mv.S2()).Type()
		att := pos.Board().Piece(mv.S1()).Type()
		if victim == chess.King {
			continue
		}
		after := pos.Update(mv)
		retaken := false
		for _, re := range after.ValidMoves() {
			if re.S2() == mv.S2() && re.HasTag(chess.Capture) {
				retaken = true
				break
			}
		}
		gain := pieceValue[victim]
		if retaken {
			gain -= pieceValue[att]
		}
		if gain <= 0 {
			continue
		}
		out = append(out, capture{from: mv.S1(), to: mv.S2(), victim: victim, attacker: att, gain: gain, free: !retaken, mate: after.Status() == chess.Checkmate})
	}
	// best first: biggest gain, then the first found (stable)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].gain > out[j-1].gain; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// mateInOne returns a move of pos's side to move that checkmates, if any.
func mateInOne(pos *chess.Position) *chess.Move {
	if pos == nil {
		return nil
	}
	for _, mv := range pos.ValidMoves() {
		if pos.Update(mv).Status() == chess.Checkmate {
			return mv
		}
	}
	return nil
}

func article(name string) string {
	if strings.ContainsAny(name[:1], "aeiou") {
		return "an " + name
	}
	return "a " + name
}

// voice is who the advice is about: the human ("you") or the agent.
type voice struct {
	You, Your, Obj string // "You", "your", "you" / "The agent", "the agent's", "the agent"
	Opp, Theirs    string // "your opponent", "their" / "the human", "the human's"
	Was            string // "were" / "was"
}

var (
	toHuman = voice{You: "You", Your: "your", Obj: "you", Opp: "your opponent", Theirs: "their", Was: "were"}
	toAgent = voice{You: "The agent", Your: "the agent's", Obj: "the agent", Opp: "the human", Theirs: "the human's", Was: "was"}
)

// Advice says, in one or two short sentences, why best was better than played
// in position before, from the point of view of the side that played. It
// returns "" when none of its rules explain it; Coach then falls back on the
// engine's evaluation.
func Advice(before *chess.Position, played, best *chess.Move, v voice) string {
	if best == nil || played == nil {
		return ""
	}
	mover := before.Turn()
	afterPlayed := before.Update(played)
	afterBest := before.Update(best)

	// 1. A checkmate was there.
	if afterBest.Status() == chess.Checkmate {
		return v.You + " could have checkmated and won the game at once. " + Describe(before, best).Text + "."
	}
	// 2. The move allows a checkmate next turn that the better move avoids.
	if m := mateInOne(afterPlayed); m != nil && mateInOne(afterBest) == nil {
		return fmt.Sprintf("It lets %s checkmate %s next move. %s.", v.Opp, v.Obj, Describe(afterPlayed, m).Text)
	}
	// 3. A piece is left to be captured that the better move would not leave.
	stillHanging := captures(afterBest)
	for _, c := range captures(afterPlayed) {
		if hasTarget(stillHanging, c.to) {
			continue
		}
		name := pieceName(c.victim)
		if c.free {
			return fmt.Sprintf("It leaves %s %s on %s with nothing protecting it, so %s can take it for free.", v.Your, name, c.to, v.Opp)
		}
		return fmt.Sprintf("%s %s on %s can be taken by %s, which is worth less.", capitalize(v.Your), name, c.to, article(pieceName(c.attacker)))
	}
	// 4. The better move wins material.
	for _, c := range captures(before) {
		if c.from == best.S1() && c.to == best.S2() {
			if c.free {
				return fmt.Sprintf("%s %s could take %s %s on %s, and it could not be taken back.", capitalize(v.Your), pieceName(c.attacker), v.Theirs, pieceName(c.victim), c.to)
			}
			return fmt.Sprintf("%s %s could take %s %s on %s, which is worth more.", capitalize(v.Your), pieceName(c.attacker), v.Theirs, pieceName(c.victim), c.to)
		}
	}
	// 5. A piece was under attack and the better move saves it.
	remaining := captures(withTurn(afterBest, opposite(mover)))
	stillAfterPlayed := captures(withTurn(afterPlayed, opposite(mover)))
	for _, t := range captures(withTurn(before, opposite(mover))) {
		if !hasTarget(remaining, t.to) && hasTarget(stillAfterPlayed, t.to) {
			return fmt.Sprintf("%s %s on %s was under attack. %s keeps it safe.", capitalize(v.Your), pieceName(t.victim), t.to, bestShort(before, best))
		}
	}
	// 6. The better move attacks something.
	if atk := captures(withTurn(afterBest, mover)); len(atk) > 0 {
		a := atk[0]
		return fmt.Sprintf("%s attacks %s %s on %s.", bestShort(before, best), v.Theirs, pieceName(a.victim), a.to)
	}
	if best.HasTag(chess.Check) {
		return bestShort(before, best) + " attacks the king and keeps the pressure on."
	}
	return ""
}

// evalAdvice is the fallback: say what the move did to the score, in words.
// before and after are Stockfish's scores from the mover's side, in centipawns.
func evalAdvice(before, after int, v voice) string {
	switch {
	case after <= -150 && before > -50:
		return fmt.Sprintf("The game was fairly balanced, but this move gave %s the better position.", v.Opp)
	case after <= -150:
		return fmt.Sprintf("%s %s already struggling, and this move made it worse.", v.You, v.Was)
	case before >= 150 && after < 50:
		return fmt.Sprintf("%s %s ahead, and this move let most of that advantage slip away.", v.You, v.Was)
	case before >= 150:
		return fmt.Sprintf("%s %s ahead, and this move gave some of that advantage back.", v.You, v.Was)
	default:
		return "The better move kept the position in better shape."
	}
}

func opposite(c chess.Color) chess.Color {
	if c == chess.White {
		return chess.Black
	}
	return chess.White
}

func hasTarget(cs []capture, sq chess.Square) bool {
	for _, c := range cs {
		if c.to == sq {
			return true
		}
	}
	return false
}

// bestShort is "Knight to f3" style, for use inside a sentence.
func bestShort(before *chess.Position, mv *chess.Move) string {
	p := pieceName(before.Board().Piece(mv.S1()).Type())
	if mv.HasTag(chess.KingSideCastle) || mv.HasTag(chess.QueenSideCastle) {
		return "Castling"
	}
	if mv.HasTag(chess.Capture) {
		return fmt.Sprintf("%s takes on %s", capitalize(p), mv.S2())
	}
	return fmt.Sprintf("%s to %s", capitalize(p), mv.S2())
}

// WinPercentLost is how much winning chance a graded move cost, in percentage
// points. The grader's drop is on a -1..1 scale, so 1.0 of it is 50 points.
func WinPercentLost(gr Grade) float64 { return gr.WinChanceDrop * 50 }

// CoachNote is what a player learns from a graded move: the move Stockfish
// preferred, in words, and why it was better.
type CoachNote struct {
	BestSAN string   `json:"best_san"`
	BestUCI string   `json:"best_uci"`
	Best    MoveText `json:"best"`
	Advice  string   `json:"advice"`
}

// Coach explains a graded move. Only an inaccuracy, a mistake or a blunder
// gets a note; for a move that was fine there is nothing to correct. ok is
// false when there is none, or the engine's best move cannot be read back.
func Coach(before *chess.Position, played *chess.Move, gr Grade, human bool) (CoachNote, bool) {
	switch gr.Class {
	case ClassInaccuracy, ClassMistake, ClassBlunder:
	default:
		return CoachNote{}, false
	}
	var best *chess.Move
	if gr.BestUCI != "" {
		best, _ = chess.UCINotation{}.Decode(before, gr.BestUCI)
	}
	if best == nil && gr.BestMove != "" {
		best, _ = chess.AlgebraicNotation{}.Decode(before, gr.BestMove)
	}
	if best == nil {
		return CoachNote{}, false
	}
	v := toAgent
	if human {
		v = toHuman
	}
	advice := Advice(before, played, best, v)
	if advice == "" {
		advice = evalAdvice(gr.EvalBeforeCP, gr.EvalAfterCP, v)
	}
	return CoachNote{
		BestSAN: chess.AlgebraicNotation{}.Encode(before, best),
		BestUCI: chess.UCINotation{}.Encode(before, best),
		Best:    Describe(before, best),
		Advice:  advice,
	}, true
}
