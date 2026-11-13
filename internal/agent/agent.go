// Package agent is the LLM chess player. Every model call, tool call and move
// validation becomes a span under the caller's "invoke_agent" span.
package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"

	"github.com/notnil/chess"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/sonalgaud12/observability-gambit/internal/llm"
	sc "github.com/sonalgaud12/observability-gambit/internal/semconv"
)

const instrumentationName = "github.com/sonalgaud12/observability-gambit/internal/agent"

// Name is the gen_ai.agent.name value.
const Name = "chess-agent"

// Config controls how the agent plays.
type Config struct {
	Model              string
	Temperature        float64 // < 0 means "provider default"
	MaxTokens          int
	MaxRetries         int  // illegal-move retries before falling back
	MaxToolRounds      int  // tool-call rounds per attempt
	LegalMovesInPrompt bool // give the model the legal move list up front
	Tools              bool // offer legal_moves / threats tools
}

// Agent plays one side of a game.
type Agent struct {
	cfg    Config
	llm    llm.Client
	tracer trace.Tracer
	rng    *rand.Rand

	// OnProgress, if set, is called with the running stats of the current
	// decision after every chat call and every rejected move, so a live UI can
	// show the agent working. It runs on the deciding goroutine: keep it quick.
	OnProgress func(Stats)
}

// New builds an agent. The client should already be llm.Instrument-ed.
func New(cfg Config, client llm.Client, seed int64) *Agent {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.MaxToolRounds == 0 {
		cfg.MaxToolRounds = 2
	}
	return &Agent{cfg: cfg, llm: client, tracer: otel.Tracer(instrumentationName), rng: rand.New(rand.NewSource(seed))}
}

// Stats are the trace-derived features of one move decision.
type Stats struct {
	ChatCalls       int
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
	ToolCalls       int
	IllegalAttempts int
	FinishLength    bool
	Fallback        bool
	TimedOut        bool // the context deadline ended the decision; implies Fallback
	LLMErrors       int
	Reasoning       string // the final answer text
}

// Choose asks the model for a move in the game's current position.
// It always returns a legal move: after MaxRetries it falls back to a random
// legal move and flags it, so one bad response never kills a game. If ctx has
// a deadline and it passes first, the same fallback applies and Stats.TimedOut
// is set; any other cancellation is returned as an error.
func (a *Agent) Choose(ctx context.Context, g *chess.Game) (*chess.Move, Stats, error) {
	pos := g.Position()
	legal := pos.ValidMoves()
	var st Stats

	msgs := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: a.userPrompt(g)},
	}
	var tools []llm.ToolDef
	if a.cfg.Tools {
		tools = toolDefs
	}

	for attempt := 0; attempt <= a.cfg.MaxRetries && ctx.Err() == nil; attempt++ {
		resp, err := a.complete(ctx, &msgs, tools, pos, &st)
		if err != nil {
			st.LLMErrors++
			a.progress(&st)
			if ctx.Err() == nil {
				sleep(ctx, time.Duration(attempt+1)*500*time.Millisecond)
			}
			continue
		}
		st.Reasoning = resp.Message.Content
		if resp.FinishReason == "length" {
			st.FinishLength = true
		}

		candidate := extractMove(resp.Message.Content)
		mv := a.validate(ctx, pos, candidate, attempt)
		if mv != nil {
			return mv, st, nil
		}
		st.IllegalAttempts++
		a.progress(&st)
		msgs = append(msgs, resp.Message, llm.Message{
			Role: "user",
			Content: fmt.Sprintf("%q is illegal or unreadable in this position. Legal moves: %s\nReply again, ending with a line `MOVE: <SAN>`.",
				candidate, sanList(pos, legal)),
		})
	}

	if err := ctx.Err(); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			return nil, st, err
		}
		st.TimedOut = true
	}

	// Fallback: keep the game going, but make the failure visible.
	st.Fallback = true
	mv := legal[a.rng.Intn(len(legal))]
	trace.SpanFromContext(ctx).AddEvent("chess.fallback_move", trace.WithAttributes(
		sc.ChessMoveSAN.String(chess.AlgebraicNotation{}.Encode(pos, mv))))
	return mv, st, nil
}

// complete runs one attempt: a chat call, plus any tool rounds it triggers.
func (a *Agent) complete(ctx context.Context, msgs *[]llm.Message, tools []llm.ToolDef, pos *chess.Position, st *Stats) (llm.Response, error) {
	for round := 0; ; round++ {
		offer := tools
		if round >= a.cfg.MaxToolRounds {
			offer = nil // force an answer
		}
		resp, err := a.llm.Chat(ctx, llm.Request{
			Model: a.cfg.Model, Messages: *msgs, Tools: offer,
			Temperature: a.cfg.Temperature, MaxTokens: a.cfg.MaxTokens,
		})
		st.ChatCalls++
		if err != nil {
			return resp, err
		}
		st.InputTokens += resp.InputTokens
		st.OutputTokens += resp.OutputTokens
		st.ReasoningTokens += resp.ReasoningTokens
		a.progress(st)

		if len(resp.Message.ToolCalls) == 0 {
			return resp, nil
		}
		*msgs = append(*msgs, resp.Message)
		for _, tc := range resp.Message.ToolCalls {
			st.ToolCalls++
			*msgs = append(*msgs, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: a.runTool(ctx, tc, pos)})
		}
	}
}

func (a *Agent) progress(st *Stats) {
	if a.OnProgress != nil {
		a.OnProgress(*st)
	}
}

// sleep waits for d or until ctx ends, whichever comes first.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func (a *Agent) runTool(ctx context.Context, tc llm.ToolCall, pos *chess.Position) string {
	_, span := a.tracer.Start(ctx, sc.OpExecuteTool+" "+tc.Name, trace.WithAttributes(
		sc.GenAIOperationName.String(sc.OpExecuteTool),
		sc.GenAIToolName.String(tc.Name),
		sc.GenAIToolCallID.String(tc.ID),
		sc.GenAIToolType.String("function"),
	))
	defer span.End()

	switch tc.Name {
	case "legal_moves":
		return sanList(pos, pos.ValidMoves())
	case "threats":
		return threats(pos)
	default:
		span.SetStatus(codes.Error, "unknown tool")
		span.SetAttributes(sc.ErrorType.String("unknown_tool"))
		return "error: unknown tool " + tc.Name
	}
}

func (a *Agent) validate(ctx context.Context, pos *chess.Position, candidate string, attempt int) *chess.Move {
	_, span := a.tracer.Start(ctx, "chess.validate_move", trace.WithAttributes(
		sc.ChessCandidate.String(candidate),
		sc.GambitRetryAttempt.Int(attempt),
	))
	defer span.End()

	mv := parseMove(pos, candidate)
	span.SetAttributes(sc.ChessLegal.Bool(mv != nil))
	if mv == nil {
		span.SetStatus(codes.Error, "illegal move")
		span.SetAttributes(sc.ErrorType.String("illegal_move"))
	}
	return mv
}

var moveLine = regexp.MustCompile(`(?i)move\s*[:=]\s*\**\s*([A-Za-z0-9=+#\-]+)`)

// extractMove pulls the move out of a free-form answer.
func extractMove(s string) string {
	if all := moveLine.FindAllStringSubmatch(s, -1); len(all) > 0 {
		return all[len(all)-1][1]
	}
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return strings.Trim(f[len(f)-1], ".,;:*`'\"")
}

// ParseMove reads a move in SAN (with or without check marks) or UCI and
// returns the matching legal move in pos, or nil if there is none. The agent
// and the human-mode UI share it, so both accept the same notations.
func ParseMove(pos *chess.Position, s string) *chess.Move { return parseMove(pos, s) }

// parseMove accepts SAN (with or without check marks) or UCI.
func parseMove(pos *chess.Position, s string) *chess.Move {
	s = strings.TrimSpace(strings.Trim(s, ".,;:*`'\"!?"))
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "0-0-0", "O-O-O")
	s = strings.ReplaceAll(s, "0-0", "O-O")
	try := []string{s, strings.TrimRight(s, "+#")}
	for _, t := range try {
		if mv, err := (chess.AlgebraicNotation{}).Decode(pos, t); err == nil {
			return mv
		}
		// Decode is lenient about check suffixes; re-add them if the move gives check.
		if mv, err := (chess.AlgebraicNotation{}).Decode(pos, t+"+"); err == nil {
			return mv
		}
		if mv, err := (chess.AlgebraicNotation{}).Decode(pos, t+"#"); err == nil {
			return mv
		}
	}
	if mv, err := (chess.UCINotation{}).Decode(pos, strings.ToLower(s)); err == nil {
		for _, v := range pos.ValidMoves() {
			if v.S1() == mv.S1() && v.S2() == mv.S2() && v.Promo() == mv.Promo() {
				return v
			}
		}
	}
	return nil
}

func sanList(pos *chess.Position, moves []*chess.Move) string {
	out := make([]string, len(moves))
	for i, m := range moves {
		out[i] = chess.AlgebraicNotation{}.Encode(pos, m)
	}
	return strings.Join(out, ", ")
}

// Attrs returns the trace-feature attributes for the invoke_agent span.
func (s Stats) Attrs() []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		sc.GenAIUsageInputTokens.Int(s.InputTokens),
		sc.GenAIUsageOutputTokens.Int(s.OutputTokens),
		sc.GambitUsageReasoningTokens.Int(s.ReasoningTokens),
		sc.ChessIllegalAttempts.Int(s.IllegalAttempts),
		sc.ChessFallback.Bool(s.Fallback),
		attribute.Int("gambit.chat_calls", s.ChatCalls),
		attribute.Int("gambit.tool_calls", s.ToolCalls),
	}
	if s.TimedOut {
		attrs = append(attrs, sc.GambitTimedOut.Bool(true))
	}
	return attrs
}
