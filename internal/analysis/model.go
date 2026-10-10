package analysis

import (
	"math"
	"math/rand"
	"sort"

	"github.com/sonalgaud12/observability-gambit/internal/dataset"
)

// Model is a standardized logistic regression, serializable to JSON so the
// demo UI can score moves with exactly the model the report evaluated.
type Model struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Features    []string  `json:"features"`
	Mean        []float64 `json:"mean"`
	Std         []float64 `json:"std"`
	Weights     []float64 `json:"weights"`
	Bias        float64   `json:"bias"`
	// Threshold flags the riskiest AlertRate fraction of training moves.
	Threshold float64 `json:"threshold"`
	AlertRate float64 `json:"alert_rate"`
	TestAUC   float64 `json:"test_auc"`
}

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

// Predict returns P(label) for one raw feature vector.
func (m *Model) Predict(x []float64) float64 {
	z := m.Bias
	for j, v := range x {
		z += m.Weights[j] * (v - m.Mean[j]) / m.Std[j]
	}
	return sigmoid(z)
}

// ScoreGame returns the risk for every move of one game (sorted by ply).
func (m *Model) ScoreGame(moves []dataset.MoveRecord) []float64 {
	feats := make([]Feature, len(m.Features))
	for j, n := range m.Features {
		feats[j], _ = FeatureByName(n)
	}
	out := make([]float64, len(moves))
	x := make([]float64, len(feats))
	for i := range moves {
		for j, f := range feats {
			x[j] = f.Fn(moves, i)
		}
		out[i] = m.Predict(x)
	}
	return out
}

// Fit trains with full-batch gradient descent, L2 regularization and class
// weighting (blunders are rare). Deliberately simple: the talk's point is the
// pipeline, and a linear model's weights are explainable on a slide.
func Fit(name, desc string, featureNames []string, X [][]float64, y []float64, l2 float64) *Model {
	n, d := len(X), len(featureNames)
	m := &Model{Name: name, Description: desc, Features: featureNames,
		Mean: make([]float64, d), Std: make([]float64, d), Weights: make([]float64, d)}

	for j := 0; j < d; j++ {
		var s, ss float64
		for i := 0; i < n; i++ {
			s += X[i][j]
		}
		mu := s / float64(n)
		for i := 0; i < n; i++ {
			ss += (X[i][j] - mu) * (X[i][j] - mu)
		}
		sd := math.Sqrt(ss / float64(n))
		if sd < 1e-9 {
			sd = 1
		}
		m.Mean[j], m.Std[j] = mu, sd
	}
	Z := make([][]float64, n)
	for i := range X {
		Z[i] = make([]float64, d)
		for j := range X[i] {
			Z[i][j] = (X[i][j] - m.Mean[j]) / m.Std[j]
		}
	}

	pos := 0.0
	for _, v := range y {
		pos += v
	}
	wPos, wNeg := 1.0, 1.0
	if pos > 0 && pos < float64(n) {
		wPos = float64(n) / (2 * pos)
		wNeg = float64(n) / (2 * (float64(n) - pos))
	}

	lr := 0.1
	grad := make([]float64, d)
	for iter := 0; iter < 3000; iter++ {
		for j := range grad {
			grad[j] = 0
		}
		gb, wsum := 0.0, 0.0
		for i := 0; i < n; i++ {
			z := m.Bias
			for j := 0; j < d; j++ {
				z += m.Weights[j] * Z[i][j]
			}
			w := wNeg
			if y[i] == 1 {
				w = wPos
			}
			e := w * (sigmoid(z) - y[i])
			for j := 0; j < d; j++ {
				grad[j] += e * Z[i][j]
			}
			gb += e
			wsum += w
		}
		for j := 0; j < d; j++ {
			m.Weights[j] -= lr * (grad[j]/wsum + l2*m.Weights[j])
		}
		m.Bias -= lr * gb / wsum
	}
	return m
}

// AUC is the probability a random positive outranks a random negative
// (Mann-Whitney U, ties count half). Returns NaN if a class is missing.
func AUC(scores, y []float64) float64 {
	type sy struct{ s, y float64 }
	v := make([]sy, len(scores))
	for i := range scores {
		v[i] = sy{scores[i], y[i]}
	}
	sort.Slice(v, func(a, b int) bool { return v[a].s < v[b].s })
	var rankSumPos, nPos, nNeg float64
	for i := 0; i < len(v); {
		j := i
		for j < len(v) && v[j].s == v[i].s {
			j++
		}
		avgRank := float64(i+j+1) / 2 // 1-based average rank of the tie group
		for k := i; k < j; k++ {
			if v[k].y == 1 {
				rankSumPos += avgRank
				nPos++
			} else {
				nNeg++
			}
		}
		i = j
	}
	if nPos == 0 || nNeg == 0 {
		return math.NaN()
	}
	return (rankSumPos - nPos*(nPos+1)/2) / (nPos * nNeg)
}

// AveragePrecision summarizes the precision-recall curve.
func AveragePrecision(scores, y []float64) float64 {
	idx := make([]int, len(scores))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	var tp, total, ap float64
	for _, v := range y {
		total += v
	}
	if total == 0 {
		return math.NaN()
	}
	for k, i := range idx {
		if y[i] == 1 {
			tp++
			ap += tp / float64(k+1)
		}
	}
	return ap / total
}

// BootstrapAUC returns a 95% interval by resampling games (moves within a game
// are not independent, so we resample whole games).
func BootstrapAUC(groups [][]int, scores, y []float64, iters int, seed int64) (lo, hi float64) {
	rng := rand.New(rand.NewSource(seed))
	var aucs []float64
	for it := 0; it < iters; it++ {
		var s, t []float64
		for range groups {
			g := groups[rng.Intn(len(groups))]
			for _, i := range g {
				s = append(s, scores[i])
				t = append(t, y[i])
			}
		}
		if a := AUC(s, t); !math.IsNaN(a) {
			aucs = append(aucs, a)
		}
	}
	if len(aucs) < 10 {
		return math.NaN(), math.NaN()
	}
	sort.Float64s(aucs)
	return aucs[int(0.025*float64(len(aucs)))], aucs[int(0.975*float64(len(aucs)-1))]
}

// quantile of a copy of xs.
func quantile(xs []float64, q float64) float64 {
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	if len(c) == 0 {
		return math.NaN()
	}
	return c[int(q*float64(len(c)-1))]
}
