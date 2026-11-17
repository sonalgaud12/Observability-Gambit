package game

import (
	"fmt"
	"strings"

	"github.com/notnil/chess"
)

// Plain-language move text, for people who cannot read chess notation.
//
// Everything here is derived from the position and the move alone (no engine,
// no model), so it is the same for the human's moves and the agent's.

// MoveText is a move in words, plus the squares the UI highlights for it.
type MoveText struct {
	Text string `json:"text"` // "Knight moves to h3"
	From string `json:"from"` // "g1"
	To   string `json:"to"`   // "h3"
}

// pieceName is a piece's English name, lower case ("knight").
func pieceName(t chess.PieceType) string {
	switch t {
	case chess.King:
		return "king"
	case chess.Queen:
		return "queen"
	case chess.Rook:
		return "rook"
	case chess.Bishop:
		return "bishop"
	case chess.Knight:
		return "knight"
	case chess.Pawn:
		return "pawn"
	}
	return "piece"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Describe says what mv does in position before, for example "Knight moves to
// h3", "Pawn on e takes the knight on d5", "Castles kingside (the king moves
// two squares and the rook jumps over it)" or "Pawn moves to e8 and becomes a
// queen". A move that gives check or checkmate says so.
func Describe(before *chess.Position, mv *chess.Move) MoveText {
	from, to := mv.S1(), mv.S2()
	mover := before.Board().Piece(from)
	name := pieceName(mover.Type())

	var text string
	switch {
	case mv.HasTag(chess.KingSideCastle):
		text = "Castles kingside: the king moves two squares towards the corner and the rook jumps over it"
	case mv.HasTag(chess.QueenSideCastle):
		text = "Castles queenside: the king moves two squares towards the corner and the rook jumps over it"
	case mv.HasTag(chess.EnPassant):
		text = fmt.Sprintf("Pawn on %s takes the pawn that just moved two squares, landing on %s (a special pawn capture called en passant)", from.File(), to)
	case mv.HasTag(chess.Capture):
		victim := before.Board().Piece(to)
		text = fmt.Sprintf("%s on %s takes the %s on %s", capitalize(name), whereFrom(mover.Type(), from), pieceName(victim.Type()), to)
	default:
		text = fmt.Sprintf("%s moves to %s", capitalize(name), to)
	}
	if mv.Promo() != chess.NoPieceType {
		text += fmt.Sprintf(" and becomes a %s", pieceName(mv.Promo()))
	}
	switch after := before.Update(mv); {
	case after.Status() == chess.Checkmate:
		text += ". Checkmate: the king cannot escape, so the game is won"
	case mv.HasTag(chess.Check):
		text += ". This attacks the king (check)"
	}
	return MoveText{Text: text, From: from.String(), To: to.String()}
}

// whereFrom names where a capturing piece stands: pawns are known by their
// file ("Pawn on e takes ..."), other pieces by their square.
func whereFrom(t chess.PieceType, from chess.Square) string {
	if t == chess.Pawn {
		return from.File().String()
	}
	return from.String()
}

// DescribeSAN is Describe for a move written in SAN or UCI, as the UI
// receives it. ok is false when the text is not a legal move there.
func DescribeSAN(before *chess.Position, move string) (MoveText, bool) {
	for _, mv := range before.ValidMoves() {
		if (chess.AlgebraicNotation{}).Encode(before, mv) == move || (chess.UCINotation{}).Encode(before, mv) == move {
			return Describe(before, mv), true
		}
	}
	return MoveText{}, false
}
