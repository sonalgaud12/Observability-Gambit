package game

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/notnil/chess"
)

// startWaiting runs h.Move in the background and returns once it is waiting.
func startWaiting(t *testing.T, h *HumanOpponent, g *chess.Game) (<-chan moveResult, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan moveResult, 1)
	go func() {
		mv, err := h.Move(ctx, g)
		out <- moveResult{mv, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !h.Waiting() {
		if time.Now().After(deadline) {
			t.Fatal("Move never started waiting")
		}
		time.Sleep(time.Millisecond)
	}
	return out, cancel
}

type moveResult struct {
	mv  *chess.Move
	err error
}

func TestHumanOpponentRejectsBeforeItsTurn(t *testing.T) {
	h := NewHumanOpponent()
	if _, err := h.Submit("e2e4"); !errors.Is(err, ErrNotYourTurn) {
		t.Fatalf("err = %v, want ErrNotYourTurn", err)
	}
}

func TestHumanOpponentValidatesMoves(t *testing.T) {
	g := chess.NewGame()
	h := NewHumanOpponent()
	out, cancel := startWaiting(t, h, g)
	defer cancel()

	for _, bad := range []string{"", "e2e5", "Qh5", "e7e5", "nonsense", "e1e2"} {
		if _, err := h.Submit(bad); !errors.Is(err, ErrIllegalMove) {
			t.Errorf("Submit(%q) err = %v, want ErrIllegalMove", bad, err)
		}
	}
	if !h.Waiting() {
		t.Fatal("an illegal move must leave the game waiting")
	}

	played, err := h.Submit("e2e4")
	if err != nil {
		t.Fatal(err)
	}
	if played.SAN != "e4" || played.UCI != "e2e4" {
		t.Errorf("played = %+v", played)
	}
	r := <-out
	if r.err != nil || r.mv == nil || r.mv.String() != "e2e4" {
		t.Fatalf("Move returned %v, %v", r.mv, r.err)
	}
	if err := g.Move(r.mv); err != nil {
		t.Fatalf("the returned move must be playable: %v", err)
	}
	if _, err := h.Submit("d2d4"); !errors.Is(err, ErrNotYourTurn) {
		t.Errorf("a second move in one turn: err = %v, want ErrNotYourTurn", err)
	}
}

func TestHumanOpponentAcceptsSANAndPromotion(t *testing.T) {
	h := NewHumanOpponent()
	out, cancel := startWaiting(t, h, chess.NewGame())
	defer cancel()
	if played, err := h.Submit("Nf3"); err != nil || played.UCI != "g1f3" {
		t.Fatalf("SAN: %+v, %v", played, err)
	}
	<-out

	// A pawn on e7 about to promote: the piece must be named.
	opt, err := chess.FEN("8/4P1k1/8/8/8/8/6K1/8 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	h2 := NewHumanOpponent()
	out2, cancel2 := startWaiting(t, h2, chess.NewGame(opt))
	defer cancel2()
	if _, err := h2.Submit("e7e8"); !errors.Is(err, ErrIllegalMove) {
		t.Errorf("promotion without a piece: err = %v, want ErrIllegalMove", err)
	}
	played, err := h2.Submit("e7e8q")
	if err != nil || played.SAN != "e8=Q" {
		t.Fatalf("promotion: %+v, %v", played, err)
	}
	if r := <-out2; r.err != nil || r.mv.Promo() != chess.Queen {
		t.Fatalf("Move returned %v, %v", r.mv, r.err)
	}
}

func TestHumanOpponentStopsWithContextAndResign(t *testing.T) {
	h := NewHumanOpponent()
	out, cancel := startWaiting(t, h, chess.NewGame())
	cancel()
	if r := <-out; !errors.Is(r.err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", r.err)
	}
	if h.Waiting() {
		t.Error("still waiting after cancel")
	}

	h2 := NewHumanOpponent()
	out2, cancel2 := startWaiting(t, h2, chess.NewGame())
	defer cancel2()
	h2.Resign()
	if r := <-out2; !errors.Is(r.err, ErrResigned) {
		t.Errorf("err = %v, want ErrResigned", r.err)
	}
	if !h2.Resigned() {
		t.Error("Resigned() = false")
	}
	// A resignation while the agent is thinking is still honoured at the next turn.
	h3 := NewHumanOpponent()
	h3.Resign()
	if _, err := h3.Move(context.Background(), chess.NewGame()); !errors.Is(err, ErrResigned) {
		t.Errorf("err = %v, want ErrResigned", err)
	}
}

func TestRandomOpponentPlaysLegalMoves(t *testing.T) {
	o := &randomOpponent{rng: rand.New(rand.NewSource(1))}
	g := chess.NewGame()
	for i := 0; i < 20 && g.Outcome() == chess.NoOutcome; i++ {
		mv, err := o.Move(context.Background(), g)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Move(mv); err != nil {
			t.Fatalf("illegal move %s: %v", mv, err)
		}
	}
	var _ Opponent = o
	var _ Opponent = &engineOpponent{}
	var _ Opponent = NewHumanOpponent()
}
