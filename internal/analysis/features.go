// Package analysis turns the per-move table into the talk's answer: do trace
// features predict engine-graded blunders, and can they do it *before* the
// blunder move is even requested?
package analysis

import (
	"math"
	"sort"

	"github.com/sonalgaud12/observability-gambit/internal/dataset"
)

// Feature extracts one number for move i of a game's agent moves (sorted by
// ply). Extractors may only look at moves[0..i] and, for history features,
// only at moves[0..i-1] trace data. Labels are never read here.
type Feature struct {
	Name  string
	Group string // position | trace | history
	Fn    func(moves []dataset.MoveRecord, i int) float64
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func logp(x float64) float64 { return math.Log1p(math.Max(0, x)) }

// historyWindow is how many of the agent's previous moves history features see.
const historyWindow = 3

func window(moves []dataset.MoveRecord, i int) []dataset.MoveRecord {
	lo := i - historyWindow
	if lo < 0 {
		lo = 0
	}
	return moves[lo:i]
}

func meanOf(ms []dataset.MoveRecord, f func(dataset.MoveRecord) float64) float64 {
	if len(ms) == 0 {
		return 0
	}
	s := 0.0
	for _, m := range ms {
		s += f(m)
	}
	return s / float64(len(ms))
}

// slope is the least-squares slope of f over the window (per move).
func slope(ms []dataset.MoveRecord, f func(dataset.MoveRecord) float64) float64 {
	n := float64(len(ms))
	if n < 2 {
		return 0
	}
	var sx, sy, sxx, sxy float64
	for k, m := range ms {
		x, y := float64(k), f(m)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	d := n*sxx - sx*sx
	if d == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / d
}

var (
	fLatency = func(m dataset.MoveRecord) float64 { return logp(m.LatencyMS) }
	fOut     = func(m dataset.MoveRecord) float64 { return logp(float64(m.OutputTokens)) }
)

// All features, grouped.
var All = []Feature{
	// Position: a no-telemetry baseline. What a chess player sees.
	{"move_number", "position", func(ms []dataset.MoveRecord, i int) float64 { return float64(ms[i].MoveNumber) }},
	{"log_legal_moves", "position", func(ms []dataset.MoveRecord, i int) float64 { return logp(float64(ms[i].LegalMoveCount)) }},
	{"in_check", "position", func(ms []dataset.MoveRecord, i int) float64 { return b2f(ms[i].InCheck) }},
	{"material_balance", "position", func(ms []dataset.MoveRecord, i int) float64 { return float64(ms[i].MaterialBalance) }},

	// Trace: this move's own decision trace, available before the move is played.
	{"log_latency_ms", "trace", func(ms []dataset.MoveRecord, i int) float64 { return logp(ms[i].LatencyMS) }},
	{"log_input_tokens", "trace", func(ms []dataset.MoveRecord, i int) float64 { return logp(float64(ms[i].InputTokens)) }},
	{"log_output_tokens", "trace", func(ms []dataset.MoveRecord, i int) float64 { return logp(float64(ms[i].OutputTokens)) }},
	{"log_reasoning_tokens", "trace", func(ms []dataset.MoveRecord, i int) float64 { return logp(float64(ms[i].ReasoningTokens)) }},
	{"chat_calls", "trace", func(ms []dataset.MoveRecord, i int) float64 { return float64(ms[i].ChatCalls) }},
	{"tool_calls", "trace", func(ms []dataset.MoveRecord, i int) float64 { return float64(ms[i].ToolCalls) }},
	{"illegal_attempts", "trace", func(ms []dataset.MoveRecord, i int) float64 { return float64(ms[i].IllegalAttempts) }},
	{"finish_length", "trace", func(ms []dataset.MoveRecord, i int) float64 { return b2f(ms[i].FinishLength) }},
	{"fallback", "trace", func(ms []dataset.MoveRecord, i int) float64 { return b2f(ms[i].Fallback) }},

	// History: only the agent's PREVIOUS moves' traces. Available before move i is requested.
	{"hist_illegal_mean", "history", func(ms []dataset.MoveRecord, i int) float64 {
		return meanOf(window(ms, i), func(m dataset.MoveRecord) float64 { return float64(m.IllegalAttempts) })
	}},
	{"hist_log_latency_mean", "history", func(ms []dataset.MoveRecord, i int) float64 { return meanOf(window(ms, i), fLatency) }},
	{"hist_log_latency_slope", "history", func(ms []dataset.MoveRecord, i int) float64 { return slope(window(ms, i), fLatency) }},
	{"hist_log_output_mean", "history", func(ms []dataset.MoveRecord, i int) float64 { return meanOf(window(ms, i), fOut) }},
	{"hist_log_output_slope", "history", func(ms []dataset.MoveRecord, i int) float64 { return slope(window(ms, i), fOut) }},
	{"hist_tool_calls_mean", "history", func(ms []dataset.MoveRecord, i int) float64 {
		return meanOf(window(ms, i), func(m dataset.MoveRecord) float64 { return float64(m.ToolCalls) })
	}},
	{"hist_chat_calls_mean", "history", func(ms []dataset.MoveRecord, i int) float64 {
		return meanOf(window(ms, i), func(m dataset.MoveRecord) float64 { return float64(m.ChatCalls) })
	}},
	{"hist_finish_length", "history", func(ms []dataset.MoveRecord, i int) float64 {
		return meanOf(window(ms, i), func(m dataset.MoveRecord) float64 { return b2f(m.FinishLength) })
	}},
	{"hist_len", "history", func(ms []dataset.MoveRecord, i int) float64 { return float64(len(window(ms, i))) }},
}

// ModelSpec names a model and the feature groups it may use.
type ModelSpec struct {
	Name        string
	Groups      []string
	Description string
}

// Specs are the models compared in the report.
var Specs = []ModelSpec{
	{"position", []string{"position"}, "Baseline: position only, no telemetry"},
	{"trace", []string{"trace"}, "This move's trace only"},
	{"at_move", []string{"position", "trace"}, "Position + this move's trace: can it veto the move before it hits the board?"},
	{"forecast", []string{"position", "history"}, "Position + previous moves' traces: can it warn before the move is even requested?"},
}

// FeaturesFor returns the features a spec may use.
func FeaturesFor(spec ModelSpec) []Feature {
	var out []Feature
	for _, f := range All {
		for _, g := range spec.Groups {
			if f.Group == g {
				out = append(out, f)
			}
		}
	}
	return out
}

// FeatureByName looks up a feature.
func FeatureByName(name string) (Feature, bool) {
	for _, f := range All {
		if f.Name == name {
			return f, true
		}
	}
	return Feature{}, false
}

// ByGame groups records per game, each sorted by ply.
func ByGame(recs []dataset.MoveRecord) map[string][]dataset.MoveRecord {
	out := map[string][]dataset.MoveRecord{}
	for _, r := range recs {
		out[r.GameID] = append(out[r.GameID], r)
	}
	for _, ms := range out {
		sort.Slice(ms, func(a, b int) bool { return ms[a].Ply < ms[b].Ply })
	}
	return out
}
