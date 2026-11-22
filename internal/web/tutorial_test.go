package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/notnil/chess"

	"github.com/sonalgaud12/observability-gambit/internal/dataset"
	"github.com/sonalgaud12/observability-gambit/internal/game"
)

// replayOnly serves the UI the way `gambit serve` does without --live: no
// model, no Stockfish, and no recorded games. The tutorial must still work.
func replayOnly(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	_, h, err := newServer(Options{Addr: "x", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The Tutorial tab is served, /tutorial leads to it, and every page links to it.
func TestTutorialTabRouting(t *testing.T) {
	srv := replayOnly(t)

	code, page := getBody(t, srv.URL+"/tutorial.html")
	if code != 200 || page == "" {
		t.Fatalf("/tutorial.html: %d, want the tutorial page", code)
	}
	// /tutorial is a redirect (to tutorial.html); the default client follows it.
	if code, redirected := getBody(t, srv.URL+"/tutorial"); code != 200 || redirected != page {
		t.Errorf("/tutorial: %d, want the tutorial page", code)
	}

	for _, path := range []string{"/", "/live.html"} {
		if code, _ := getBody(t, srv.URL+path); code != 200 {
			t.Errorf("%s: %d", path, code)
		}
	}
}

type sampleGame struct {
	StartFEN  string  `json:"start_fen"`
	Threshold float64 `json:"threshold"`
	Plies     []struct {
		Ply      int    `json:"ply"`
		SAN      string `json:"san"`
		UCI      string `json:"uci"`
		FENAfter string `json:"fen_after"`
		ByAgent  bool   `json:"by_agent"`
		Agent    *struct {
			MoveNumber    int                `json:"move_number"`
			Class         string             `json:"class"`
			BestMove      string             `json:"best_move"`
			EvalBeforeCP  int                `json:"eval_before_cp"`
			EvalAfterCP   int                `json:"eval_after_cp"`
			WinChanceDrop float64            `json:"win_chance_drop"`
			Risk          map[string]float64 `json:"risk"`
		} `json:"agent"`
	} `json:"plies"`
}

// The sample game is embedded in the page, so the tutorial needs no backend.
// It must be a legal game whose recorded positions and grades are consistent.
func TestTutorialSampleGameLoads(t *testing.T) {
	_, page := getBody(t, replayOnly(t).URL+"/tutorial.html")
	m := regexp.MustCompile(`(?s)<script type="application/json" id="sample-game">(.*?)</script>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no embedded sample game in tutorial.html")
	}
	var s sampleGame
	if err := json.Unmarshal([]byte(m[1]), &s); err != nil {
		t.Fatalf("sample game is not valid JSON: %v", err)
	}
	if len(s.Plies) < 10 || s.Threshold <= 0 || s.Threshold >= 1 {
		t.Fatalf("sample game too short or no alert threshold: %d plies, threshold %v", len(s.Plies), s.Threshold)
	}

	g := chess.NewGame()
	if got := g.Position().String(); got != s.StartFEN {
		t.Fatalf("start_fen = %q, want %q", s.StartFEN, got)
	}
	blunders := 0
	for i, p := range s.Plies {
		if p.Ply != i+1 || p.ByAgent != (i%2 == 0) || (p.Agent != nil) != p.ByAgent {
			t.Fatalf("ply %d is malformed: %+v", i+1, p)
		}
		before := g.Position()
		mv, err := chess.AlgebraicNotation{}.Decode(before, p.SAN)
		if err != nil {
			t.Fatalf("ply %d %s is not legal: %v", p.Ply, p.SAN, err)
		}
		if uci := (chess.UCINotation{}).Encode(before, mv); uci != p.UCI {
			t.Errorf("ply %d: uci %s, want %s", p.Ply, p.UCI, uci)
		}
		if err := g.Move(mv); err != nil {
			t.Fatal(err)
		}
		if got := g.Position().String(); got != p.FENAfter {
			t.Errorf("ply %d: fen_after %q, want %q", p.Ply, p.FENAfter, got)
		}
		if a := p.Agent; a != nil {
			if a.MoveNumber != (p.Ply+1)/2 {
				t.Errorf("ply %d: move number %d", p.Ply, a.MoveNumber)
			}
			// The class must be the one the real grader would give.
			if want := game.Classify(a.WinChanceDrop, a.Class == game.ClassBest); want != a.Class {
				t.Errorf("ply %d: class %q, but a %.3f drop is %q", p.Ply, a.Class, a.WinChanceDrop, want)
			}
			if want := game.WinningChances(a.EvalBeforeCP) - game.WinningChances(a.EvalAfterCP); a.WinChanceDrop > 0 && (a.WinChanceDrop-want > 0.01 || want-a.WinChanceDrop > 0.01) {
				t.Errorf("ply %d: drop %.3f does not match evals (%.3f)", p.Ply, a.WinChanceDrop, want)
			}
			for _, k := range []string{"forecast", "at_move"} {
				if r, ok := a.Risk[k]; !ok || r < 0 || r > 1 {
					t.Errorf("ply %d: risk %q = %v", p.Ply, k, r)
				}
			}
			if a.Class == game.ClassBlunder {
				blunders++
				if a.Risk["forecast"] < s.Threshold {
					t.Errorf("the blunder's forecast %.2f is under the alert threshold %.2f: the tutorial says it warns", a.Risk["forecast"], s.Threshold)
				}
				if a.BestMove == "" || a.BestMove == p.SAN {
					t.Errorf("the blunder has best_move %q", a.BestMove)
				}
			}
		}
	}
	if blunders != 1 {
		t.Errorf("sample game has %d blunders, want exactly 1 for the walkthrough", blunders)
	}
}
