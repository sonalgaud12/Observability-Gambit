package analysis

import (
	"math"
	"testing"

	"github.com/sonalgaud12/observability-gambit/internal/dataset"
	"github.com/sonalgaud12/observability-gambit/internal/game"
)

func TestAUC(t *testing.T) {
	if a := AUC([]float64{0.1, 0.4, 0.35, 0.8}, []float64{0, 0, 1, 1}); math.Abs(a-0.75) > 1e-9 {
		t.Errorf("AUC = %v, want 0.75", a)
	}
	if a := AUC([]float64{0.5, 0.5}, []float64{0, 1}); a != 0.5 {
		t.Errorf("ties: AUC = %v, want 0.5", a)
	}
}

func TestHistoryFeaturesNeverSeeTheCurrentMove(t *testing.T) {
	moves := []dataset.MoveRecord{{IllegalAttempts: 0}, {IllegalAttempts: 2}, {IllegalAttempts: 99}}
	f, _ := FeatureByName("hist_illegal_mean")
	if got := f.Fn(moves, 2); got != 1 {
		t.Errorf("hist_illegal_mean at move 2 = %v, want 1 (mean of 0 and 2, excluding the current 99)", got)
	}
	if got := f.Fn(moves, 0); got != 0 {
		t.Errorf("first move should have empty history, got %v", got)
	}
}

func TestFitSeparatesSignal(t *testing.T) {
	var X [][]float64
	var y []float64
	for i := 0; i < 200; i++ {
		v := float64(i%20) / 20
		X = append(X, []float64{v, 0.5})
		y = append(y, b2f(v > 0.7))
	}
	m := Fit("t", "", []string{"a", "b"}, X, y, 0.001)
	scores := make([]float64, len(X))
	for i := range X {
		scores[i] = m.Predict(X[i])
	}
	if a := AUC(scores, y); a < 0.99 {
		t.Errorf("AUC = %v on separable data", a)
	}
}

func TestClassify(t *testing.T) {
	if c := game.Classify(game.WinningChances(300)-game.WinningChances(-600), false); c != game.ClassBlunder {
		t.Errorf("+3 -> -6 should be a blunder, got %s", c)
	}
	// Already lost: dropping from -9 to -15 pawns barely moves winning chances.
	if c := game.Classify(game.WinningChances(-900)-game.WinningChances(-1500), false); c == game.ClassBlunder {
		t.Errorf("a drop inside a lost position should not be a blunder")
	}
}
