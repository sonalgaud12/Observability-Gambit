package web

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/notnil/chess"
)

// The Tutorial's chess lessons highlight the squares a piece can move to. The
// browser does not generate those moves: it reads them from the JSON block
// #chess-lessons in static/tutorial.html. That block is produced by the Go
// move generator this project already uses (github.com/notnil/chess), so the
// rules shown to a beginner are exactly the rules the game server enforces.
//
//	go test ./internal/web -run TestTutorialLessonData -update-lessons
//
// rewrites the "moves" of every position from its "fen". Without the flag the
// same test fails if the page and the generator disagree.
var updateLessons = flag.Bool("update-lessons", false, "rewrite the chess-lessons block in static/tutorial.html")

type lessonMove struct {
	To      string            `json:"to"`
	Kind    string            `json:"kind"` // move, castle, enpassant, promotion
	Capture bool              `json:"capture,omitempty"`
	Check   bool              `json:"check,omitempty"`
	After   string            `json:"after,omitempty"`
	Promo   map[string]string `json:"promo,omitempty"` // piece letter -> position after promoting to it
}

type lessonPos struct {
	FEN    string                  `json:"fen"`
	Select string                  `json:"select,omitempty"` // piece selected when the lesson opens
	Check  string                  `json:"check,omitempty"`  // square of the king in check
	Danger []string                `json:"danger,omitempty"` // king lesson: squares next to the king that are attacked
	Moves  map[string][]lessonMove `json:"moves"`
}

type lessonFile struct {
	Positions map[string]*lessonPos `json:"positions"`
}

var lessonBlock = regexp.MustCompile(`(?s)(<script type="application/json" id="chess-lessons">\n)(.*?)(\n</script>)`)

// kingInCheck reports whether the side to move has its king attacked. The
// engine only lists legal moves, so ask the other side whether it could take it.
func kingInCheck(t *testing.T, fen string) (string, bool) {
	t.Helper()
	parts := strings.Fields(fen)
	if parts[1] == "w" {
		parts[1] = "b"
	} else {
		parts[1] = "w"
	}
	parts[3] = "-"
	opt, err := chess.FEN(strings.Join(parts, " "))
	if err != nil {
		t.Fatalf("flipped %q: %v", fen, err)
	}
	pos := chess.NewGame(opt).Position()
	var king chess.Square = chess.NoSquare
	want := chess.White
	if parts[1] == "w" { // flipped to white to move, so black is the side being asked about
		want = chess.Black
	}
	for sq, p := range pos.Board().SquareMap() {
		if p.Type() == chess.King && p.Color() == want {
			king = sq
		}
	}
	for _, m := range pos.ValidMoves() {
		if m.S2() == king {
			return king.String(), true
		}
	}
	return "", false
}

// generateMoves lists, for every piece of the side to move, its legal moves.
func generateMoves(t *testing.T, fen string) (map[string][]lessonMove, chess.Method) {
	t.Helper()
	opt, err := chess.FEN(fen)
	if err != nil {
		t.Fatalf("bad FEN %q: %v", fen, err)
	}
	g := chess.NewGame(opt)
	pos := g.Position()
	out := map[string][]lessonMove{}
	for sq, p := range pos.Board().SquareMap() {
		if p.Color() == pos.Turn() {
			out[sq.String()] = []lessonMove{}
		}
	}
	for _, m := range pos.ValidMoves() {
		from, to := m.S1().String(), m.S2().String()
		after := pos.Update(m)
		lm := lessonMove{To: to, Kind: "move", Capture: m.HasTag(chess.Capture) || m.HasTag(chess.EnPassant), Check: m.HasTag(chess.Check)}
		switch {
		case m.HasTag(chess.KingSideCastle) || m.HasTag(chess.QueenSideCastle):
			lm.Kind = "castle"
		case m.HasTag(chess.EnPassant):
			lm.Kind = "enpassant"
		}
		if m.Promo() != chess.NoPieceType {
			lm.Kind = "promotion"
			list := out[from]
			var existing *lessonMove
			for i := range list {
				if list[i].To == to {
					existing = &list[i]
				}
			}
			if existing == nil {
				lm.Promo = map[string]string{}
				out[from] = append(out[from], lm)
				existing = &out[from][len(out[from])-1]
			}
			existing.Promo[strings.ToLower(m.Promo().String())] = after.String()
			continue
		}
		lm.After = after.String()
		out[from] = append(out[from], lm)
	}
	for _, l := range out {
		sort.Slice(l, func(i, j int) bool { return l[i].To < l[j].To })
	}
	return out, pos.Status()
}

func marshalLessons(f lessonFile) string {
	ids := make([]string, 0, len(f.Positions))
	for id := range f.Positions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("{\"positions\": {\n")
	for i, id := range ids {
		js, err := json.Marshal(f.Positions[id])
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(&b, " %q: %s", id, js)
		if i < len(ids)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}}")
	return b.String()
}

func TestTutorialLessonData(t *testing.T) {
	var raw string
	if *updateLessons {
		b, err := os.ReadFile("static/tutorial.html")
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	} else {
		_, raw = getBody(t, replayOnly(t).URL+"/tutorial.html")
	}
	m := lessonBlock.FindStringSubmatch(raw)
	if m == nil {
		t.Fatal("no #chess-lessons block in tutorial.html")
	}
	var f lessonFile
	if err := json.Unmarshal([]byte(m[2]), &f); err != nil {
		t.Fatalf("chess-lessons is not valid JSON: %v", err)
	}
	if len(f.Positions) < 12 {
		t.Fatalf("only %d lesson positions", len(f.Positions))
	}

	for id, p := range f.Positions {
		want, status := generateMoves(t, p.FEN)

		// The position itself must be legal: the side that just moved cannot be left attacking the king.
		opt, _ := chess.FEN(p.FEN)
		pos := chess.NewGame(opt).Position()
		for _, mv := range pos.ValidMoves() {
			if pos.Board().Piece(mv.S2()).Type() == chess.King {
				t.Errorf("%s: the side to move could capture the king, so the position is illegal", id)
			}
		}

		// "check" is set exactly when the side to move is in check, and names that king.
		sq, in := kingInCheck(t, p.FEN)
		if in != (p.Check != "") || sq != p.Check {
			t.Errorf("%s: check = %q, but the engine says in-check=%v on %q", id, p.Check, in, sq)
		}
		if p.Select != "" {
			if _, ok := want[p.Select]; !ok {
				t.Errorf("%s: select %q is not a piece of the side to move", id, p.Select)
			}
		}
		if len(p.Danger) > 0 {
			// Every neighbour of the selected king is a legal move, holds the king's own piece, or is listed as attacked.
			var wantDanger []string
			legal := map[string]bool{}
			for _, mv := range want[p.Select] {
				legal[mv.To] = true
			}
			board := pos.Board()
			ks := squareOf(t, p.Select)
			for _, n := range neighbours(ks) {
				occ := board.Piece(n)
				if !legal[n.String()] && !(occ != chess.NoPiece && occ.Color() == pos.Turn()) {
					wantDanger = append(wantDanger, n.String())
				}
			}
			sort.Strings(wantDanger)
			got := append([]string(nil), p.Danger...)
			sort.Strings(got)
			if !reflect.DeepEqual(got, wantDanger) {
				t.Errorf("%s: danger = %v, want %v", id, got, wantDanger)
			}
		}
		_ = status

		if *updateLessons {
			p.Moves = want
			continue
		}
		if !reflect.DeepEqual(p.Moves, want) {
			gj, _ := json.Marshal(p.Moves)
			wj, _ := json.Marshal(want)
			t.Errorf("%s: moves in the page differ from the Go move generator; run `go test ./internal/web -run TestTutorialLessonData -update-lessons`\n page: %s\n want: %s", id, gj, wj)
		}
	}

	if *updateLessons {
		out := lessonBlock.ReplaceAllString(raw, "${1}"+strings.ReplaceAll(marshalLessons(f), "$", "$$")+"${3}")
		if err := os.WriteFile("static/tutorial.html", []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func squareOf(t *testing.T, s string) chess.Square {
	t.Helper()
	for sq := chess.A1; sq <= chess.H8; sq++ {
		if sq.String() == s {
			return sq
		}
	}
	t.Fatalf("bad square %q", s)
	return chess.NoSquare
}

func neighbours(s chess.Square) []chess.Square {
	var out []chess.Square
	f, r := int(s.File()), int(s.Rank())
	for df := -1; df <= 1; df++ {
		for dr := -1; dr <= 1; dr++ {
			nf, nr := f+df, r+dr
			if (df == 0 && dr == 0) || nf < 0 || nf > 7 || nr < 0 || nr > 7 {
				continue
			}
			out = append(out, chess.NewSquare(chess.File(nf), chess.Rank(nr)))
		}
	}
	return out
}

// targets returns the sorted destination squares of one piece, for the explicit-expectation tests below.
func targets(moves map[string][]lessonMove, from string) []string {
	var out []string
	for _, m := range moves[from] {
		out = append(out, m.To)
	}
	sort.Strings(out)
	return out
}

func lessonByID(t *testing.T, id string) *lessonPos {
	t.Helper()
	_, raw := getBody(t, replayOnly(t).URL+"/tutorial.html")
	m := lessonBlock.FindStringSubmatch(raw)
	if m == nil {
		t.Fatal("no #chess-lessons block")
	}
	var f lessonFile
	if err := json.Unmarshal([]byte(m[2]), &f); err != nil {
		t.Fatal(err)
	}
	p, ok := f.Positions[id]
	if !ok {
		t.Fatalf("no lesson position %q", id)
	}
	return p
}

// The highlighting logic, checked against rules written out by hand rather than
// against the generator, for each kind of move a beginner meets.
func TestTutorialLessonRules(t *testing.T) {
	cases := []struct {
		id, from string
		want     []string
	}{
		// Rook: straight lines, stops at its own pawn (d2), may take the black pawns on d7 and b4.
		{"rook", "d4", []string{"b4", "c4", "d3", "d5", "d6", "d7", "e4", "f4", "g4", "h4"}},
		// Bishop: diagonals only, stops before its own pawn (b2) and takes the pawn on f6, not beyond.
		{"bishop", "d4", []string{"a7", "b6", "c3", "c5", "e3", "e5", "f2", "f6", "g1"}},
		// Queen: rook and bishop lines; stops at its own pawn (b2), takes d7 and b6.
		{"queen", "d4", []string{"a4", "b4", "b6", "c3", "c4", "c5", "d1", "d2", "d3", "d5", "d6", "d7", "e3", "e4", "e5", "f2", "f4", "f6", "g1", "g4", "g7", "h4", "h8"}},
		// Knight: jumps over neighbours; cannot land on its own pawn (f5); may take c6.
		{"knight", "d4", []string{"b3", "b5", "c2", "c6", "e2", "e6", "f3"}},
		// King: one step; e4 and e2 are guarded by black pawns, so they are not offered.
		{"king", "e3", []string{"d2", "d3", "d4", "f2", "f3", "f4"}},
		// Pawns: two steps from the start square, one step after, diagonal captures only, blocked head-on.
		{"pawn", "e2", []string{"e3", "e4"}},
		{"pawn", "c4", []string{"b5", "c5", "d5"}},
		{"pawn", "g2", []string{"h3"}},
		// In check, only moves that end the check are legal: step aside, block on e3, or take the rook.
		{"check", "e1", []string{"d1", "d2", "f1", "f2"}},
		{"check", "c1", []string{"e3"}},
		{"check", "a8", []string{"e8"}},
		// Castling: both ways when the squares are free and safe ...
		{"castle", "e1", []string{"c1", "d1", "d2", "e2", "f1", "f2", "g1"}},
		// ... but not kingside when the black rook on f8 attacks the whole f-file (f1 is crossed, f2 is attacked too).
		{"castle-blocked", "e1", []string{"c1", "d1", "d2", "e2"}},
		// En passant: the pawn may take d5 by moving to d6; one move later the chance is gone.
		{"enpassant", "e5", []string{"d6", "e6"}},
		{"enpassant-late", "e5", []string{"e6"}},
		// Promotion, by pushing and by capturing.
		{"promotion", "e7", []string{"e8"}},
		{"promotion-capture", "e7", []string{"d8", "e8"}},
		// Checkmate and stalemate: nothing can move at all.
		{"checkmate", "g8", nil},
		{"stalemate", "h8", nil},
	}
	for _, c := range cases {
		p := lessonByID(t, c.id)
		if got := targets(p.Moves, c.from); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %s: moves %v, want %v", c.id, c.from, got, c.want)
		}
	}

	moveOf := func(id, from, to string) lessonMove {
		for _, m := range lessonByID(t, id).Moves[from] {
			if m.To == to {
				return m
			}
		}
		t.Fatalf("%s: no move %s%s", id, from, to)
		return lessonMove{}
	}

	// Castling moves the rook too.
	if m := moveOf("castle", "e1", "g1"); m.Kind != "castle" || !strings.HasPrefix(m.After, "r3k2r/8/8/8/8/8/8/R4RK1 b") {
		t.Errorf("kingside castle: %+v", m)
	}
	if m := moveOf("castle", "e1", "c1"); m.Kind != "castle" || !strings.HasPrefix(m.After, "r3k2r/8/8/8/8/8/8/2KR3R b") {
		t.Errorf("queenside castle: %+v", m)
	}
	// En passant removes the pawn that is not on the destination square.
	if m := moveOf("enpassant", "e5", "d6"); m.Kind != "enpassant" || !m.Capture || !strings.HasPrefix(m.After, "4k3/8/3P4/8/8/8/8/4K3 b") {
		t.Errorf("en passant: %+v", m)
	}
	// Promotion offers queen, rook, bishop and knight, and nothing else.
	m := moveOf("promotion", "e7", "e8")
	var keys []string
	for k := range m.Promo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if m.Kind != "promotion" || !reflect.DeepEqual(keys, []string{"b", "n", "q", "r"}) {
		t.Errorf("promotion choices: %+v", m)
	}
	if !strings.HasPrefix(m.Promo["q"], "4Q2k/8/8/8/8/8/8/4K3 b") || !strings.HasPrefix(m.Promo["n"], "4N2k/8/8/8/8/8/8/4K3 b") {
		t.Errorf("promotion results: %+v", m.Promo)
	}
	if c := moveOf("promotion-capture", "e7", "d8"); !c.Capture || c.Kind != "promotion" {
		t.Errorf("promotion by capture: %+v", c)
	}

	// The end-of-game lessons really are those results, and the check lesson really is a check.
	for id, want := range map[string]chess.Method{"checkmate": chess.Checkmate, "stalemate": chess.Stalemate} {
		if _, got := generateMoves(t, lessonByID(t, id).FEN); got != want {
			t.Errorf("%s: status %v, want %v", id, got, want)
		}
	}
	if c := lessonByID(t, "checkmate").Check; c != "g8" {
		t.Errorf("checkmate: king in check on %q, want g8", c)
	}
	if c := lessonByID(t, "stalemate").Check; c != "" {
		t.Errorf("stalemate: the king must not be in check, got %q", c)
	}
}

// The routing, language toggle and lesson-data lookups live in static/tutorial-core.js
// and have Node tests in jstest/. They run here so `go test` covers them too.
func TestTutorialJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; run `node --test internal/web/jstest/tutorial.test.mjs` where it is")
	}
	out, err := exec.Command(node, "--test", "jstest/tutorial.test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("node tests failed: %v\n%s", err, out)
	}
}
