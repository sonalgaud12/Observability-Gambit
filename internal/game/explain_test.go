package game

import (
	"strings"
	"testing"

	"github.com/notnil/chess"
)

func posFromFEN(t *testing.T, fen string) *chess.Position {
	t.Helper()
	opt, err := chess.FEN(fen)
	if err != nil {
		t.Fatal(err)
	}
	return chess.NewGame(opt).Position()
}

const startFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"

func TestDescribeInPlainLanguage(t *testing.T) {
	tests := []struct {
		name, fen, move, want string
		from, to              string
	}{
		{"pawn push", startFEN, "e4", "Pawn moves to e4", "e2", "e4"},
		{"knight", startFEN, "Nh3", "Knight moves to h3", "g1", "h3"},
		{"UCI is read too", startFEN, "g1f3", "Knight moves to f3", "g1", "f3"},
		{"pawn takes", "rnbqkbnr/ppp1pppp/8/3p4/4P3/8/PPPP1PPP/RNBQKBNR w KQkq - 0 2", "exd5",
			"Pawn on e takes the pawn on d5", "e4", "d5"},
		{"piece takes a different piece", "rnbqkbnr/pppp1ppp/8/4p3/8/5N2/PPPPPPPP/RNBQKB1R w KQkq - 0 2", "Nxe5",
			"Knight on f3 takes the pawn on e5", "f3", "e5"},
		{"queen takes with check", "rnbqkbnr/pppp1ppp/8/4p2Q/4P3/8/PPPP1PPP/RNB1KBNR w KQkq - 0 3", "Qxe5+",
			"Queen on h5 takes the pawn on e5. This attacks the king (check)", "h5", "e5"},
		{"castles kingside", "r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1", "O-O",
			"Castles kingside: the king moves two squares towards the corner and the rook jumps over it", "e1", "g1"},
		{"castles queenside", "r3k2r/8/8/8/8/8/8/R3K2R b KQkq - 0 1", "O-O-O",
			"Castles queenside: the king moves two squares towards the corner and the rook jumps over it", "e8", "c8"},
		{"promotion", "8/P6k/8/8/8/8/8/K7 w - - 0 1", "a8=Q", "Pawn moves to a8 and becomes a queen", "a7", "a8"},
		{"underpromotion", "8/P6k/8/8/8/8/8/K7 w - - 0 1", "a8=N", "Pawn moves to a8 and becomes a knight", "a7", "a8"},
		{"capture and promote", "1n5k/P7/8/8/8/8/8/K7 w - - 0 1", "axb8=R+", "Pawn on a takes the knight on b8 and becomes a rook. This attacks the king (check)", "a7", "b8"},
		{"en passant", "rnbqkbnr/ppp1p1pp/8/3pPp2/8/8/PPPP1PPP/RNBQKBNR w KQkq f6 0 3", "exf6",
			"Pawn on e takes the pawn that just moved two squares, landing on f6 (a special pawn capture called en passant)", "e5", "f6"},
		{"checkmate", "r1bqkbnr/pppp1ppp/2n5/4p2Q/2B1P3/8/PPPP1PPP/RNB1K1NR w KQkq - 2 3", "Qxf7#",
			"Queen on h5 takes the pawn on f7. Checkmate: the king cannot escape, so the game is won", "h5", "f7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DescribeSAN(posFromFEN(t, tc.fen), tc.move)
			if !ok {
				t.Fatalf("%q is not legal in %s", tc.move, tc.fen)
			}
			if got.Text != tc.want || got.From != tc.from || got.To != tc.to {
				t.Errorf("Describe(%q) = %+v\nwant text %q, %s→%s", tc.move, got, tc.want, tc.from, tc.to)
			}
		})
	}
}

func TestDescribeSaysWhenAMoveGivesCheck(t *testing.T) {
	tests := []struct{ fen, move, want string }{
		{"4k3/8/8/8/8/8/8/4KR2 w - - 0 1", "Rf8+", "Rook moves to f8. This attacks the king (check)"},
		{"4k3/8/8/8/8/8/8/4KR2 w - - 0 1", "Rf7", "Rook moves to f7"},
	}
	for _, tc := range tests {
		got, ok := DescribeSAN(posFromFEN(t, tc.fen), tc.move)
		if !ok || got.Text != tc.want {
			t.Errorf("Describe(%q) = %q (legal %v), want %q", tc.move, got.Text, ok, tc.want)
		}
	}
}

func TestDescribeSANRejectsIllegalMoves(t *testing.T) {
	if _, ok := DescribeSAN(posFromFEN(t, startFEN), "Nf6"); ok {
		t.Error("Nf6 is not legal for white at the start")
	}
}

func adviceFor(t *testing.T, fen, played, best string) string {
	t.Helper()
	pos := posFromFEN(t, fen)
	pm, err := chess.AlgebraicNotation{}.Decode(pos, played)
	if err != nil {
		t.Fatalf("played %s: %v", played, err)
	}
	bm, err := chess.AlgebraicNotation{}.Decode(pos, best)
	if err != nil {
		t.Fatalf("best %s: %v", best, err)
	}
	return Advice(pos, pm, bm, toHuman)
}

func TestAdviceSaysWhatWentWrong(t *testing.T) {
	tests := []struct {
		name, fen, played, best, want string
	}{
		{"missed a checkmate", "6k1/5ppp/8/8/8/8/8/R3K3 w - - 0 1", "Kd2", "Ra8#", "checkmated and won the game at once. Rook moves to a8."},
		{"allowed a checkmate", "r5k1/5ppp/8/8/8/8/5PPP/6K1 w - - 0 1", "Kh1", "h3", "lets your opponent checkmate you next move. Rook moves to a1."},
		{"left a piece to be taken for free", "4k3/8/2p5/8/8/8/8/3QK3 w - - 0 1", "Qd5", "Qh5+", "leaves your queen on d5 with nothing protecting it"},
		{"could have won a queen", "4k3/8/8/3q4/8/2N5/8/4K3 w - - 0 1", "Kf2", "Nxd5", "Your knight could take their queen on d5, and it could not be taken back."},
		{"better move attacks something", "4k3/8/8/3q4/8/8/8/R3K3 w - - 0 1", "Kf2", "Ra5", "Rook to a5 attacks their queen on d5."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := adviceFor(t, tc.fen, tc.played, tc.best)
			if !strings.Contains(got, tc.want) {
				t.Errorf("advice = %q\nwant it to contain %q", got, tc.want)
			}
		})
	}
}

func TestAdviceDoesNotBlameAPieceThatWouldHangAnyway(t *testing.T) {
	// The queen on d5 is attacked by the c6 pawn whatever White does with the
	// king, so "it leaves your queen..." would be a false reason.
	got := adviceFor(t, "4k3/8/2p5/3Q4/8/8/8/4K3 w - - 0 1", "Kf2", "Kd2")
	if strings.Contains(got, "nothing protecting") {
		t.Errorf("blamed the move for an existing problem: %q", got)
	}
}

func TestCoachOnlyForMovesWorthCorrecting(t *testing.T) {
	pos := posFromFEN(t, startFEN)
	played, _ := chess.AlgebraicNotation{}.Decode(pos, "f3")
	for _, class := range []string{ClassBest, ClassGood} {
		if _, ok := Coach(pos, played, Grade{Class: class, BestMove: "e4"}, true); ok {
			t.Errorf("a %s move got a coach note", class)
		}
	}
	for _, class := range []string{ClassInaccuracy, ClassMistake, ClassBlunder} {
		n, ok := Coach(pos, played, Grade{Class: class, BestMove: "e4"}, true)
		if !ok || n.Best.Text != "Pawn moves to e4" || n.BestUCI != "e2e4" || n.Best.From != "e2" || n.Best.To != "e4" {
			t.Errorf("%s: note = %+v, %v", class, n, ok)
		}
	}
	if _, ok := Coach(pos, played, Grade{Class: ClassBlunder, BestMove: "Qh9"}, true); ok {
		t.Error("an unreadable best move produced a note")
	}
	if got := WinPercentLost(Grade{WinChanceDrop: 0.5}); got != 25 {
		t.Errorf("WinPercentLost = %v, want 25", got)
	}
}

func TestAdviceSpeaksAboutTheAgentInTheThirdPerson(t *testing.T) {
	pos := posFromFEN(t, "4k3/8/2p5/8/8/8/8/3QK3 w - - 0 1")
	played, _ := chess.AlgebraicNotation{}.Decode(pos, "Qd5")
	best, _ := chess.AlgebraicNotation{}.Decode(pos, "Qh5+")
	got := Advice(pos, played, best, toAgent)
	want := "It leaves the agent's queen on d5 with nothing protecting it, so the human can take it for free."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCoachFallsBackOnTheScoreWhenNoRuleApplies(t *testing.T) {
	pos := posFromFEN(t, "4k3/8/8/8/8/8/8/R3K3 w - - 0 1")
	played, _ := chess.AlgebraicNotation{}.Decode(pos, "Rb1")
	tests := []struct {
		before, after int
		human         bool
		want          string
	}{
		{20, -300, true, "The game was fairly balanced, but this move gave your opponent the better position."},
		{-200, -500, true, "You were already struggling, and this move made it worse."},
		{400, 0, true, "You were ahead, and this move let most of that advantage slip away."},
		{600, 300, false, "The agent was ahead, and this move gave some of that advantage back."},
		{50, -100, true, "The better move kept the position in better shape."},
	}
	for _, tc := range tests {
		gr := Grade{Class: ClassMistake, BestMove: "Ra2", EvalBeforeCP: tc.before, EvalAfterCP: tc.after}
		n, ok := Coach(pos, played, gr, tc.human)
		if !ok || n.Advice != tc.want {
			t.Errorf("eval %d→%d: advice = %q (%v), want %q", tc.before, tc.after, n.Advice, ok, tc.want)
		}
	}
}
