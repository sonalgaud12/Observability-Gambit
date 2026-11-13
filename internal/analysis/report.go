package analysis

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sonalgaud12/observability-gambit/internal/dataset"
)

// Options for an analysis run.
type Options struct {
	DataDir string
	OutDir  string
	Label   string // "blunder" or "mistake+"
	Model   string // optional: only analyse moves from this model
	// Mode picks which games to analyse: "engine" (the default: agent vs.
	// Stockfish), "human" (played from the stage UI) or "all".
	Mode      string
	TestFrac  float64 // fraction of games held out
	AlertRate float64 // fraction of moves the alert threshold flags
	L2        float64
	// MaxAbsEval keeps only moves played when |eval before| <= this many
	// centipawns (0 = keep all). In a decided position winning chances are
	// saturated and no move can register as a blunder, which makes "the side
	// that is already lost never blunders" look like a prediction. This is a
	// row filter on the label's own scale, never a feature.
	MaxAbsEval int
}

// Bundle is what gets written to model.json and loaded by the demo UI.
type Bundle struct {
	CreatedAt string            `json:"created_at"`
	Label     string            `json:"label"`
	Mock      bool              `json:"mock"`
	Models    map[string]*Model `json:"models"`
}

// IsPositive applies the label definition.
func IsPositive(label, class string) bool {
	switch label {
	case "mistake+":
		return class == "blunder" || class == "mistake"
	default:
		return class == "blunder"
	}
}

func isTest(gameID string, frac float64) bool {
	h := fnv.New32a()
	h.Write([]byte(gameID))
	return float64(h.Sum32()%1000)/1000 < frac
}

type split struct {
	X      [][]float64
	y      []float64
	recs   []dataset.MoveRecord
	groups [][]int // row indexes per game, for the bootstrap
}

func (o Options) keep(r dataset.MoveRecord) bool {
	if o.MaxAbsEval <= 0 {
		return true
	}
	e := r.EvalBeforeCP
	if e < 0 {
		e = -e
	}
	return e <= o.MaxAbsEval
}

// build computes features over each game's FULL move sequence (history
// features need earlier moves even if those rows are filtered out), then keeps
// only the rows that pass the filter.
func build(games map[string][]dataset.MoveRecord, ids []string, feats []Feature, o Options) split {
	label := o.Label
	var s split
	for _, id := range ids {
		ms := games[id]
		var g []int
		for i := range ms {
			if !o.keep(ms[i]) {
				continue
			}
			row := make([]float64, len(feats))
			for j, f := range feats {
				row[j] = f.Fn(ms, i)
			}
			g = append(g, len(s.X))
			s.X = append(s.X, row)
			s.y = append(s.y, b2f(IsPositive(label, ms[i].Class)))
			s.recs = append(s.recs, ms[i])
		}
		if len(g) > 0 {
			s.groups = append(s.groups, g)
		}
	}
	return s
}

func modeName(m string) string {
	if m == "" {
		return "engine"
	}
	return m
}

// Run executes the analysis and writes report.md and model.json.
func Run(o Options) (*Bundle, string, error) {
	recs, err := dataset.LoadMoves(o.DataDir)
	if err != nil {
		return nil, "", err
	}
	if o.Model != "" {
		var f []dataset.MoveRecord
		for _, r := range recs {
			if r.Model == o.Model {
				f = append(f, r)
			}
		}
		recs = f
	}
	switch o.Mode {
	case "", "engine", "human", "all":
	default:
		return nil, "", fmt.Errorf("unknown mode %q: use engine, human or all", o.Mode)
	}
	if o.Mode != "all" {
		wantHuman := o.Mode == "human"
		var f []dataset.MoveRecord
		for _, r := range recs {
			if (r.Mode == dataset.ModeHuman) == wantHuman {
				f = append(f, r)
			}
		}
		recs = f
	}
	if len(recs) == 0 {
		return nil, "", fmt.Errorf("no moves found in %s (mode %q)", o.DataDir, modeName(o.Mode))
	}

	mock := false
	models := map[string]int{}
	for _, r := range recs {
		if r.Provider == "mock" {
			mock = true
		}
		models[r.Provider+"/"+r.Model]++
	}

	games := ByGame(recs)
	var trainIDs, testIDs []string
	for id := range games {
		if isTest(id, o.TestFrac) {
			testIDs = append(testIDs, id)
		} else {
			trainIDs = append(trainIDs, id)
		}
	}
	sort.Strings(trainIDs)
	sort.Strings(testIDs)

	var md strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&md, format, a...) }

	w("# The Observability Gambit: analysis report\n\n")
	w("Generated %s from `%s`.\n\n", time.Now().UTC().Format(time.RFC3339), o.DataDir)
	if m := modeName(o.Mode); m != "engine" {
		w("> **Mode: %s.** This report includes games played by a person from the stage UI, so the opponent is not the usual Stockfish. Use `--mode engine` (the default) for the headline numbers.\n\n", m)
	}
	if mock {
		w("> **Mock data.** Some or all moves come from the offline mock model, whose telemetry and move quality share a hidden cause by design. Use this report to rehearse the pipeline, never as a finding.\n\n")
	}

	// --- dataset overview ---
	var kept []dataset.MoveRecord
	for _, r := range recs {
		if o.keep(r) {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return nil, "", fmt.Errorf("no moves left after --max-abs-eval %d", o.MaxAbsEval)
	}
	classes := map[string]int{}
	pos := 0
	for _, r := range kept {
		classes[r.Class]++
		if IsPositive(o.Label, r.Class) {
			pos++
		}
	}
	w("## Dataset\n\n")
	w("| | |\n|---|---|\n")
	w("| Games | %d (train %d, test %d, split by game) |\n", len(games), len(trainIDs), len(testIDs))
	if o.MaxAbsEval > 0 {
		w("| Agent moves | %d analysed of %d (contested positions only: \\|eval before\\| ≤ %d cp) |\n", len(kept), len(recs), o.MaxAbsEval)
	} else {
		w("| Agent moves | %d (all positions, including decided ones) |\n", len(recs))
	}
	var mk []string
	for k, v := range models {
		mk = append(mk, fmt.Sprintf("%s (%d)", k, v))
	}
	sort.Strings(mk)
	w("| Models | %s |\n", strings.Join(mk, ", "))
	w("| Label | `%s`: %d positives (%.1f%% of analysed moves) |\n", o.Label, pos, 100*float64(pos)/float64(len(kept)))
	w("| Classes | best %d · good %d · inaccuracy %d · mistake %d · blunder %d |\n\n",
		classes["best"], classes["good"], classes["inaccuracy"], classes["mistake"], classes["blunder"])
	if pos < 30 {
		w("> Only %d positive moves. AUCs below have wide intervals; play more games before drawing conclusions.\n\n", pos)
	}

	// --- blunder rate by phase ---
	w("## When do blunders happen?\n\n| Moves | Agent moves | Positive rate |\n|---|---|---|\n")
	buckets := []struct{ lo, hi int }{{1, 10}, {11, 20}, {21, 30}, {31, 40}, {41, 60}, {61, 1000}}
	for _, b := range buckets {
		n, p := 0, 0
		for _, r := range kept {
			if r.MoveNumber >= b.lo && r.MoveNumber <= b.hi {
				n++
				if IsPositive(o.Label, r.Class) {
					p++
				}
			}
		}
		if n > 0 {
			hi := fmt.Sprint(b.hi)
			if b.hi >= 1000 {
				hi = "+"
			}
			w("| %d–%s | %d | %.1f%% |\n", b.lo, hi, n, 100*float64(p)/float64(n))
		}
	}
	w("\n")

	// --- univariate ---
	allIDs := append(append([]string{}, trainIDs...), testIDs...)
	full := build(games, allIDs, All, o)
	w("## Single features (all moves)\n\n")
	w("AUC 0.5 means no signal; above 0.5 means higher values go with %ss, below 0.5 the opposite.\n\n", o.Label)
	w("| Feature | Group | Mean (positive) | Mean (other) | AUC |\n|---|---|---|---|---|\n")
	type uni struct {
		f   Feature
		auc float64
		mp  float64
		mn  float64
	}
	var unis []uni
	for j, f := range All {
		col := make([]float64, len(full.X))
		var sp, sn, np, nn float64
		for i := range full.X {
			col[i] = full.X[i][j]
			if full.y[i] == 1 {
				sp += col[i]
				np++
			} else {
				sn += col[i]
				nn++
			}
		}
		unis = append(unis, uni{f, AUC(col, full.y), sp / math.Max(np, 1), sn / math.Max(nn, 1)})
	}
	sort.Slice(unis, func(a, b int) bool {
		return math.Abs(unis[a].auc-0.5) > math.Abs(unis[b].auc-0.5)
	})
	for _, u := range unis {
		w("| `%s` | %s | %.3f | %.3f | %s |\n", u.f.Name, u.f.Group, u.mp, u.mn, fmtF(u.auc))
	}
	w("\n")

	// --- models ---
	bundle := &Bundle{CreatedAt: time.Now().UTC().Format(time.RFC3339), Label: o.Label, Mock: mock, Models: map[string]*Model{}}
	w("## Models (held-out games)\n\n")
	w("Logistic regression, class-weighted, trained on %d games and scored on %d unseen games. ", len(trainIDs), len(testIDs))
	w("Alerts fire on the riskiest %.0f%% of training moves.\n\n", 100*o.AlertRate)
	w("| Model | Uses | Test AUC (95%% CI) | Avg precision | Alert recall | Alert precision |\n|---|---|---|---|---|---|\n")

	type evalOut struct {
		m      *Model
		scores []float64
		test   split
	}
	evals := map[string]evalOut{}
	for _, spec := range Specs {
		feats := FeaturesFor(spec)
		names := make([]string, len(feats))
		for i, f := range feats {
			names[i] = f.Name
		}
		tr := build(games, trainIDs, feats, o)
		te := build(games, testIDs, feats, o)
		if len(tr.X) == 0 || len(te.X) == 0 {
			return nil, "", fmt.Errorf("not enough games to split (train %d, test %d); play more games", len(trainIDs), len(testIDs))
		}
		m := Fit(spec.Name, spec.Description, names, tr.X, tr.y, o.L2)

		trScores := make([]float64, len(tr.X))
		for i := range tr.X {
			trScores[i] = m.Predict(tr.X[i])
		}
		m.AlertRate = o.AlertRate
		m.Threshold = quantile(trScores, 1-o.AlertRate)

		scores := make([]float64, len(te.X))
		for i := range te.X {
			scores[i] = m.Predict(te.X[i])
		}
		m.TestAUC = AUC(scores, te.y)
		lo, hi := BootstrapAUC(te.groups, scores, te.y, 500, 7)
		ap := AveragePrecision(scores, te.y)

		var tp, fp, fn float64
		for i, s := range scores {
			alert := s >= m.Threshold
			switch {
			case alert && te.y[i] == 1:
				tp++
			case alert:
				fp++
			case te.y[i] == 1:
				fn++
			}
		}
		w("| **%s** | %s | %s (%s–%s) | %s | %s | %s |\n", spec.Name, strings.Join(spec.Groups, " + "),
			fmtF(m.TestAUC), fmtF(lo), fmtF(hi), fmtF(ap), pct(tp, tp+fn), pct(tp, tp+fp))
		bundle.Models[spec.Name] = m
		evals[spec.Name] = evalOut{m, scores, te}
	}
	w("\nBase rate in test set (what random alerts would get for precision): %s.\n\n", pct(sum(evals["position"].test.y), float64(len(evals["position"].test.y))))

	// --- the headline question ---
	w("## Can it tell before move 23?\n\n")
	pb, at, fc := bundle.Models["position"], bundle.Models["at_move"], bundle.Models["forecast"]
	w("- **Before the move is requested** (`forecast`, previous traces only): test AUC %s vs %s for the position-only baseline.\n", fmtF(fc.TestAUC), fmtF(pb.TestAUC))
	w("- **Before the move hits the board** (`at_move`, this move's trace): test AUC %s.\n", fmtF(at.TestAUC))
	w("- Telemetry adds signal over the baseline only if the gap is larger than the confidence intervals above.\n\n")

	ev := evals["forecast"]
	w("### Test-set %ss, as the forecast model saw them\n\n", o.Label)
	w("| Game | Move | Played | Best | Win-chance drop | Forecast risk | Alert? | Trace |\n|---|---|---|---|---|---|---|---|\n")
	type row struct {
		r    dataset.MoveRecord
		risk float64
	}
	var rows []row
	for i, r := range ev.test.recs {
		if ev.test.y[i] == 1 {
			rows = append(rows, row{r, ev.scores[i]})
		}
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].r.WinChanceDrop > rows[b].r.WinChanceDrop })
	for k, rw := range rows {
		if k >= 15 {
			w("| … | %d more | | | | | | |\n", len(rows)-k)
			break
		}
		alert := ""
		if rw.risk >= fc.Threshold {
			alert = "yes"
		}
		w("| `%s` | %d | %s | %s | %.2f | %.2f | %s | `%s` |\n", rw.r.GameID, rw.r.MoveNumber, rw.r.SAN, rw.r.BestMove,
			rw.r.WinChanceDrop, rw.risk, alert, shortID(rw.r.TraceID))
	}
	w("\n")

	// --- weights ---
	w("## What the models weigh\n\nStandardized coefficients (positive = more risk).\n\n")
	for _, spec := range Specs {
		m := bundle.Models[spec.Name]
		type fw struct {
			n string
			w float64
		}
		var fws []fw
		for j, n := range m.Features {
			fws = append(fws, fw{n, m.Weights[j]})
		}
		sort.Slice(fws, func(a, b int) bool { return math.Abs(fws[a].w) > math.Abs(fws[b].w) })
		var parts []string
		for _, f := range fws {
			parts = append(parts, fmt.Sprintf("`%s` %+.2f", f.n, f.w))
		}
		w("- **%s**: %s\n", spec.Name, strings.Join(parts, ", "))
	}
	w("\n")

	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(filepath.Join(o.OutDir, "report.md"), []byte(md.String()), 0o644); err != nil {
		return nil, "", err
	}
	b, _ := json.MarshalIndent(bundle, "", "  ")
	if err := os.WriteFile(filepath.Join(o.OutDir, "model.json"), b, 0o644); err != nil {
		return nil, "", err
	}
	return bundle, md.String(), nil
}

// LoadBundle reads model.json.
func LoadBundle(path string) (*Bundle, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out Bundle
	return &out, json.Unmarshal(b, &out)
}

func fmtF(x float64) string {
	if math.IsNaN(x) {
		return "n/a"
	}
	return fmt.Sprintf("%.3f", x)
}

func pct(a, b float64) string {
	if b == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*a/b)
}

func sum(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
