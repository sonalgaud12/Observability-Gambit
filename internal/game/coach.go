package game

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/notnil/chess"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/sonalgaud12/observability-gambit/internal/llm"
	sc "github.com/sonalgaud12/observability-gambit/internal/semconv"
)

// The chess coach (human mode only).
//
// After each move the human plays, and whenever the player presses "Ask the
// coach why" on any move (the agent's included), a second model call explains
// the move in plain language for someone who may never have played chess.
//
// The coach never works out chess itself. Stockfish's grade, its preferred
// move, the winning chance the move cost, and the rule-based reason from
// advice.go are computed first; the prompt hands the model only those facts,
// each move already spelled out in words, and tells it to add nothing. The
// reply is then checked for bare notation (Nf3, exd5, O-O) and dropped if it
// contains any.
//
// The coach is off the game's critical path: it runs in its own goroutine after
// the move has been graded, with its own timeout, and any failure (timeout,
// error, budget, an unreadable answer) only means there is no comment. It uses
// the same model, client and token budget as the game's agent, and is traced as
// its own agent, "coach", in the same gen_ai.conversation.id as the player.

// CoachAgentName is the gen_ai.agent.name of the coach, as distinct from the
// player's agent.Name.
const CoachAgentName = "coach"

// Coach kinds, on the events and in gambit.coach.kind.
const (
	CoachComment = "comment" // the automatic remark after the human's move
	CoachWhy     = "why"     // "Ask the coach why", on any move
)

const (
	coachMaxTokens     = 400              // per call: a few short sentences
	coachTimeout       = 25 * time.Second // per call, retries excluded (there are none)
	coachMaxInFlight   = 2                // concurrent coach calls per game
	coachMaxChars      = 700              // longer answers are cut at a sentence
	coachBudgetDivisor = 2                // the coach may use at most budget/2, so the game keeps room to play
)

// Errors returned by AskCoach.
var (
	// ErrCoachOff: the coach is turned off for this server.
	ErrCoachOff = errors.New("the coach is turned off")
	// ErrUnknownPly: the game has no such move.
	ErrUnknownPly = errors.New("there is no such move in this game")
	// ErrNotGraded: Stockfish has not finished checking the move, and the coach
	// only speaks from its verdict.
	ErrNotGraded = errors.New("Stockfish is still checking that move; try again in a moment")
	// ErrCoachBudget: the coach has used its share of the game's token budget.
	ErrCoachBudget = errors.New("the coach has used its share of this game's token budget")
	// ErrCoachBusy: the coach is already answering other questions.
	ErrCoachBusy = errors.New("the coach is busy answering; try again in a moment")
)

// plyFacts is what the coach may be asked about: one move, once graded.
type plyFacts struct {
	ply    int
	before *chess.Position
	mv     *chess.Move
	human  bool // the human's move (false: the agent's)
	grade  *Grade
}

// coachState is a session's coach. The zero value is "off".
type coachState struct {
	client llm.Client
	model  string
	temp   float64
	sem    chan struct{}
	ctx    context.Context
	stop   context.CancelFunc

	mu      sync.Mutex
	plies   map[int]*plyFacts
	started map[string]bool // "kind:ply": asked already, so a repeat costs nothing
}

func (s *Session) coachOn() bool { return s.coach != nil && s.coach.client != nil }

// newCoachState gives a session its own client for the game's model, so the
// coach's calls are separate from the agent's.
func (l *Live) newCoachState(m modelEntry, seed int64) *coachState {
	if l.cfg.NoCoach {
		return nil
	}
	ctx, stop := context.WithCancel(context.Background())
	return &coachState{
		client:  m.NewClient(seed, nil),
		model:   m.Agent.Model,
		temp:    m.Agent.Temperature,
		sem:     make(chan struct{}, coachMaxInFlight),
		ctx:     ctx,
		stop:    stop,
		plies:   map[int]*plyFacts{},
		started: map[string]bool{},
	}
}

// rememberMove records a move the coach may later be asked about.
func (s *Session) rememberMove(ply int, before *chess.Position, mv *chess.Move, human bool) {
	if !s.coachOn() {
		return
	}
	s.coach.mu.Lock()
	s.coach.plies[ply] = &plyFacts{ply: ply, before: before, mv: mv, human: human}
	s.coach.mu.Unlock()
}

// rememberGrade records Stockfish's verdict on a remembered move.
func (s *Session) rememberGrade(ply int, gr Grade) {
	if !s.coachOn() {
		return
	}
	s.coach.mu.Lock()
	if f := s.coach.plies[ply]; f != nil {
		f.grade = &gr
	}
	s.coach.mu.Unlock()
}

// addTokens counts model tokens against the game's budget; coach says whether
// they were the coach's.
func (s *Session) addTokens(n int, coach bool) {
	s.usageMu.Lock()
	s.tokensUsed += n
	if coach {
		s.coachTokens += n
	}
	s.usageMu.Unlock()
}

func (s *Session) tokenCounts() (used, coach int) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	return s.tokensUsed, s.coachTokens
}

// coachWithinBudget: the game's budget is shared with the agent, and the coach
// may spend at most half of it, so that coaching can never be what ends a game.
func (s *Session) coachWithinBudget() bool {
	b := s.live.cfg.GameTokenBudget
	if b <= 0 {
		return true
	}
	used, coach := s.tokenCounts()
	return used < b && coach < b/coachBudgetDivisor
}

// AskCoach answers "Ask the coach why" for a move: the answer arrives as a
// "coach_comment" event. Asking again about the same move costs nothing.
func (s *Session) AskCoach(ply int) error {
	if !s.coachOn() {
		return ErrCoachOff
	}
	c := s.coach
	c.mu.Lock()
	f := c.plies[ply]
	switch {
	case f == nil:
		c.mu.Unlock()
		return ErrUnknownPly
	case f.grade == nil:
		c.mu.Unlock()
		return ErrNotGraded
	case c.started[coachKey(CoachWhy, ply)]:
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	if !s.coachWithinBudget() {
		return ErrCoachBudget
	}
	if !s.startCoach(ply, CoachWhy, true) {
		return ErrCoachBusy
	}
	return nil
}

func coachKey(kind string, ply int) string { return fmt.Sprintf("%s:%d", kind, ply) }

// startCoach runs a coach call in the background, unless one for the same move
// and kind has run, the coach is at its concurrency limit, or the budget is
// spent. It reports whether the call was started (or had already been).
func (s *Session) startCoach(ply int, kind string, explicit bool) bool {
	c := s.coach
	select {
	case c.sem <- struct{}{}:
	default:
		return false
	}
	c.mu.Lock()
	key := coachKey(kind, ply)
	f := c.plies[ply]
	if f == nil || f.grade == nil || c.started[key] {
		again := f != nil && c.started[key]
		c.mu.Unlock()
		<-c.sem
		return again
	}
	c.started[key] = true
	facts := *f
	c.mu.Unlock()

	go func() {
		defer func() { <-c.sem }()
		s.runCoach(ply, kind, facts, explicit)
	}()
	return true
}

// coachAfterHumanGrade is called once the human's move has its grade: the
// automatic comment. It never blocks and never reports a failure to the player.
func (s *Session) coachAfterHumanGrade(ply int) {
	if !s.coachOn() || !s.coachWithinBudget() {
		return
	}
	s.startCoach(ply, CoachComment, false)
}

// runCoach makes one traced coach call and publishes the answer.
func (s *Session) runCoach(ply int, kind string, f plyFacts, explicit bool) {
	c := s.coach
	ctx, cancel := context.WithTimeout(c.ctx, coachTimeout)
	defer cancel()
	s.emit("coach_thinking", map[string]any{"ply": ply, "kind": kind})

	text, st, err := s.askCoachModel(ctx, ply, kind, f)
	s.addTokens(st.in+st.out, true)
	if err != nil {
		// A failed comment is simply absent; a failed "why" says so, once.
		reason := "error"
		switch {
		case errors.Is(err, errUnreadable):
			reason = "unreadable"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timeout"
		case errors.Is(err, context.Canceled):
			return // the game was abandoned
		}
		log.Printf("live %s: coach %s for ply %d: %v", s.ID, kind, ply, err)
		c.mu.Lock()
		delete(c.started, coachKey(kind, ply)) // let the player try again
		c.mu.Unlock()
		s.emit("coach_unavailable", map[string]any{"ply": ply, "kind": kind, "reason": reason, "explicit": explicit})
		return
	}
	s.emit("coach_comment", map[string]any{
		"ply": ply, "kind": kind, "text": text, "trace_id": st.traceID,
		"model": c.model, "mock": c.client.Provider() == "mock",
		"input_tokens": st.in, "output_tokens": st.out,
	})
}

var errUnreadable = errors.New("the coach's answer was empty or used chess notation")

type coachUsage struct {
	in, out int
	traceID string
}

// askCoachModel is the traced part: a "chess.coach" trace holding an
// "invoke_agent coach" span, which holds the client's own "chat {model}" span.
func (s *Session) askCoachModel(ctx context.Context, ply int, kind string, f plyFacts) (string, coachUsage, error) {
	c := s.coach
	p := s.live.p
	provider := c.client.Provider()
	subject := "agent"
	if f.human {
		subject = "human"
	}
	ctx, root := p.tracer.Start(ctx, "chess.coach", trace.WithAttributes(
		sc.GenAIConversationID.String(s.ID),
		sc.ChessGameID.String(s.ID),
		sc.ChessPly.Int(ply),
		sc.ChessMoveNumber.Int((ply+1)/2),
		sc.ChessColor.String(colorName(f.before.Turn())),
		sc.GenAIRequestModel.String(c.model),
		sc.GambitRunMode.String("human"),
		sc.GambitCoachKind.String(kind),
		sc.GambitCoachSubject.String(subject),
	))
	defer root.End()
	if provider == "mock" {
		root.SetAttributes(sc.GambitMock.Bool(true))
	}
	if s.player != "" {
		root.SetAttributes(sc.GambitPlayerLabel.String(s.player))
	}

	actx, span := p.tracer.Start(ctx, sc.OpInvokeAgent+" "+CoachAgentName, trace.WithAttributes(
		sc.GenAIOperationName.String(sc.OpInvokeAgent),
		sc.GenAIAgentName.String(CoachAgentName),
		sc.GenAIProviderName.String(provider),
		sc.GenAIRequestModel.String(c.model),
		sc.GenAIConversationID.String(s.ID),
		sc.GambitRunMode.String("human"),
		sc.GambitCoachKind.String(kind),
		sc.GambitCoachSubject.String(subject),
	))
	defer span.End()
	if s.player != "" {
		span.SetAttributes(sc.GambitPlayerLabel.String(s.player))
	}
	usage := coachUsage{traceID: root.SpanContext().TraceID().String()}

	fail := func(err error) (string, coachUsage, error) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		root.SetStatus(codes.Error, err.Error())
		return "", usage, err
	}

	resp, err := c.client.Chat(actx, llm.Request{
		Model: c.model, Messages: coachMessages(f, kind),
		Temperature: c.temp, MaxTokens: coachMaxTokens,
	})
	if err != nil {
		return fail(err)
	}
	usage.in, usage.out = resp.InputTokens, resp.OutputTokens
	span.SetAttributes(
		sc.GenAIUsageInputTokens.Int(resp.InputTokens),
		sc.GenAIUsageOutputTokens.Int(resp.OutputTokens),
	)
	text, ok := cleanCoachText(resp.Message.Content)
	if !ok {
		span.SetAttributes(sc.ErrorType.String("unreadable_answer"))
		return fail(errUnreadable)
	}
	return text, usage, nil
}

// --- the prompt --------------------------------------------------------------

const coachSystemPrompt = `You are a friendly chess coach talking to a complete beginner who may never have played chess before. This is a live demo, so keep it short and kind.

You are given FACTS about one move. They were checked by the Stockfish chess engine, so they are correct. Rules:
- Use only the facts. Do not invent moves, lines, threats, scores or numbers, and do not say anything the facts do not support.
- Say every move in plain words, the way the facts do ("your knight moves to f3", "the pawn takes the knight on d5"). Never write chess notation such as Nf3, exd5, O-O or e2e4.
- Use everyday words. If you use a chess word (check, checkmate, castling, capture), say what it means in a few words the first time.
- Be encouraging. For a mistake, say what went wrong without blame, and what the better move was.
- Do not use lists, headings, bold or emoji. Write 2 to 3 short sentences (up to 4 if you were asked to explain why).`

// gradeMeaning is what each grade means, in the same words the page shows.
var gradeMeaning = map[string]string{
	ClassBest:       "the strongest move Stockfish could find",
	ClassGood:       "a sound move; a slightly better one existed but nothing was lost",
	ClassInaccuracy: "a small slip; a better move existed and this one gave away a little of the chance to win",
	ClassMistake:    "a clear error that gave the opponent a real advantage",
	ClassBlunder:    "a serious error, like leaving a piece to be taken for free or missing a checkmate",
}

// coachMessages builds the prompt: the system rules, then the facts. Every move
// in it is already in words.
func coachMessages(f plyFacts, kind string) []llm.Message {
	gr := *f.grade
	played := Describe(f.before, f.mv)
	ply := f.ply
	mover := colorName(f.before.Turn())

	var b strings.Builder
	b.WriteString("FACTS (checked by Stockfish)\n")
	if f.human {
		fmt.Fprintf(&b, "Who moved: the player you are coaching, playing %s\n", mover)
	} else {
		fmt.Fprintf(&b, "Who moved: the AI opponent (a language model), playing %s. The player you are coaching plays the other side\n", mover)
	}
	fmt.Fprintf(&b, "Move number: %d\n", (ply+1)/2)
	fmt.Fprintf(&b, "Move played: %s\n", played.Text)
	fmt.Fprintf(&b, "Stockfish's verdict: %s (%s)\n", gr.Class, gradeMeaning[gr.Class])
	if lost := WinPercentLost(gr); lost >= 1 {
		fmt.Fprintf(&b, "Winning chance lost by this move: about %d percentage points\n", int(lost+0.5))
	} else {
		b.WriteString("Winning chance lost by this move: less than 1 percentage point\n")
	}
	if best := bestMoveOf(f.before, gr); best != nil && gr.Class != ClassBest {
		fmt.Fprintf(&b, "The move Stockfish preferred: %s\n", Describe(f.before, best).Text)
	}
	if note, ok := Coach(f.before, f.mv, gr, f.human); ok && note.Advice != "" {
		fmt.Fprintf(&b, "Why the preferred move was better: %s\n", note.Advice)
	}
	b.WriteString("\n")
	switch kind {
	case CoachWhy:
		b.WriteString("The player pressed \"Ask the coach why\" on this move. In plain words, explain what the move does and why it was a good or a poor choice, using only the facts above.")
	default:
		if f.human {
			b.WriteString("Write your comment on the player's move now. Start with how it went, then the one thing worth knowing.")
		} else {
			b.WriteString("Write a short comment on the opponent's move now.")
		}
	}
	return []llm.Message{
		{Role: "system", Content: coachSystemPrompt},
		{Role: "user", Content: b.String()},
	}
}

// bestMoveOf reads Stockfish's preferred move back as a legal move.
func bestMoveOf(before *chess.Position, gr Grade) *chess.Move {
	if gr.BestUCI != "" {
		if mv, err := (chess.UCINotation{}).Decode(before, gr.BestUCI); err == nil {
			return mv
		}
	}
	if gr.BestMove != "" {
		if mv, err := (chess.AlgebraicNotation{}).Decode(before, gr.BestMove); err == nil {
			return mv
		}
	}
	return nil
}

// --- the answer --------------------------------------------------------------

// notation matches chess notation a beginner cannot read: castling (O-O),
// piece moves (Nf3, Bxe5+), pawn captures (exd5), promotions (e8=Q) and long
// moves (e2e4). Squares on their own ("on d5") are fine: the facts use them.
var notation = regexp.MustCompile(`\b(?:[Oo0]-[Oo0](?:-[Oo0])?|[KQRBN][a-h]?[1-8]?x?[a-h][1-8][+#]?|[a-h]x[a-h][1-8]|[a-h][1-8]=[QRBN]|[a-h][1-8][a-h][1-8][qrbn]?)\b`)

var markup = strings.NewReplacer("**", "", "__", "", "`", "", "# ", "")

// cleanCoachText tidies a model answer for the page and refuses one that uses
// chess notation. ok is false when nothing usable is left.
func cleanCoachText(s string) (string, bool) {
	s = markup.Replace(s)
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "Coach:"))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || notation.MatchString(s) {
		return "", false
	}
	if r := []rune(s); len(r) > coachMaxChars {
		cut := string(r[:coachMaxChars])
		if i := strings.LastIndexAny(cut, ".!?"); i > coachMaxChars/2 {
			cut = cut[:i+1]
		} else {
			cut += "…"
		}
		s = cut
	}
	return s, true
}
