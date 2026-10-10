package agent

import (
	"fmt"
	"strings"

	"github.com/notnil/chess"

	"github.com/sonalgaud12/observability-gambit/internal/llm"
)

const systemPrompt = `You are a chess player. You will be shown the current position as a FEN string and the moves played so far.
Think briefly about threats against your pieces and your best candidate moves, then choose one legal move.
End your reply with exactly one line of the form:
MOVE: <move in standard algebraic notation, e.g. Nf3, exd5, O-O, e8=Q>`

func (a *Agent) userPrompt(g *chess.Game) string {
	pos := g.Position()
	var b strings.Builder
	side := "White"
	if pos.Turn() == chess.Black {
		side = "Black"
	}
	fmt.Fprintf(&b, "You are playing %s. It is your move.\n", side)
	fmt.Fprintf(&b, "FEN: %s\n", pos.String())
	fmt.Fprintf(&b, "Moves so far: %s\n", movetext(g))
	if a.cfg.LegalMovesInPrompt {
		fmt.Fprintf(&b, "Legal moves: %s\n", sanList(pos, pos.ValidMoves()))
	}
	if a.cfg.Tools {
		b.WriteString("You may call the legal_moves or threats tools before answering.\n")
	}
	return b.String()
}

// movetext renders the game so far as "1. e4 e5 2. Nf3 ...".
func movetext(g *chess.Game) string {
	moves := g.Moves()
	positions := g.Positions()
	if len(moves) == 0 {
		return "(none, this is the first move)"
	}
	var b strings.Builder
	for i, m := range moves {
		if i%2 == 0 {
			fmt.Fprintf(&b, "%d. ", i/2+1)
		}
		b.WriteString(chess.AlgebraicNotation{}.Encode(positions[i], m))
		b.WriteByte(' ')
	}
	return strings.TrimSpace(b.String())
}

var toolDefs = []llm.ToolDef{
	{
		Name:        "legal_moves",
		Description: "List every legal move in the current position, in SAN.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "threats",
		Description: "List the captures your opponent could make right now if it were their turn, i.e. which of your pieces are attacked.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	},
}

var pieceNames = map[chess.PieceType]string{
	chess.Pawn: "pawn", chess.Knight: "knight", chess.Bishop: "bishop",
	chess.Rook: "rook", chess.Queen: "queen", chess.King: "king",
}

// threats lists the opponent's captures by evaluating the position with the
// side to move flipped.
func threats(pos *chess.Position) string {
	f := strings.Fields(pos.String())
	if len(f) != 6 {
		return "unavailable"
	}
	if f[1] == "w" {
		f[1] = "b"
	} else {
		f[1] = "w"
	}
	f[3] = "-"
	opt, err := chess.FEN(strings.Join(f, " "))
	if err != nil {
		return "unavailable"
	}
	flipped := chess.NewGame(opt).Position()
	board := flipped.Board()
	var out []string
	seen := map[string]bool{}
	for _, m := range flipped.ValidMoves() {
		victim := board.Piece(m.S2())
		if victim == chess.NoPiece || victim.Type() == chess.King {
			continue
		}
		attacker := board.Piece(m.S1())
		key := m.S1().String() + m.S2().String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, fmt.Sprintf("%s on %s can take your %s on %s",
			pieceNames[attacker.Type()], m.S1(), pieceNames[victim.Type()], m.S2()))
	}
	if len(out) == 0 {
		return "No captures available to your opponent."
	}
	return strings.Join(out, "\n")
}
