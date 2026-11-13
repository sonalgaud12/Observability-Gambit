package analysis

import (
	"math"
	"strings"
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

// writeMoves writes n games of 20 moves each, marked as human games or not.
func writeMoves(t *testing.T, dir, mode string, games int) {
	t.Helper()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for g := 0; g < games; g++ {
		id := mode + "-g" + string(rune('a'+g/26)) + string(rune('a'+g%26))
		var ms []dataset.MoveRecord
		for i := 0; i < 20; i++ {
			class := game.ClassGood
			if (g+i)%7 == 0 {
				class = game.ClassBlunder
			}
			ms = append(ms, dataset.MoveRecord{
				GameID: id, Ply: 2*i + 1, MoveNumber: i + 1, Color: "white", Provider: "openai", Model: "m",
				LegalMoveCount: 20 + i, LatencyMS: float64(100 + 40*((g+i)%7)), ChatCalls: 1 + (g+i)%3,
				Class: class, WinChanceDrop: map[string]float64{game.ClassBlunder: 0.5}[class], Mode: mode,
			})
		}
		if err := w.WriteGame(dataset.GameRecord{GameID: id, Mode: mode}, ms); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunSeparatesHumanGamesFromEngineGames(t *testing.T) {
	dir := t.TempDir()
	writeMoves(t, dir, "", 30)
	writeMoves(t, dir, dataset.ModeHuman, 30)
	count := func(mode string) string {
		_, md, err := Run(Options{DataDir: dir, OutDir: t.TempDir(), Label: "blunder", Mode: mode, TestFrac: 0.3, AlertRate: 0.15, L2: 0.01})
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		return md
	}
	engine, human, all := count(""), count("human"), count("all")
	if strings.Contains(engine, "Mode:") {
		t.Error("the default (engine) report should not carry a mode banner")
	}
	if !strings.Contains(human, "Mode: human") || !strings.Contains(all, "Mode: all") {
		t.Error("human and all reports must say which games they include")
	}
	games := func(md string) string { return strings.Fields(grep(md, "| Games |"))[3] }
	if games(engine) != "30" || games(human) != "30" || games(all) != "60" {
		t.Errorf("games analysed: engine %s, human %s, all %s; want 30, 30, 60", games(engine), games(human), games(all))
	}
}

func TestRunRejectsAnUnknownMode(t *testing.T) {
	dir := t.TempDir()
	writeMoves(t, dir, "", 3)
	if _, _, err := Run(Options{DataDir: dir, OutDir: t.TempDir(), Mode: "bogus"}); err == nil {
		t.Error("an unknown mode was accepted")
	}
	// Engine-only data has no human games to analyse.
	if _, _, err := Run(Options{DataDir: dir, OutDir: t.TempDir(), Mode: "human"}); err == nil {
		t.Error("expected an error when there are no human games")
	}
}

func grep(s, prefix string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}
