package game

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/notnil/chess"

	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/dataset"
	"github.com/sonalgaud12/observability-gambit/internal/llm"
)

// Human mode: a person plays the agent from the stage UI.
//
// A Live owns the engines (the grader, and the mock model's brain) and runs one
// Session at a time. A session is one game: it publishes an ordered log of
// events ("agent_thinking", "agent_moved", ...) that the UI follows over
// server-sent events. Every agent move is traced exactly like a batch game
// (same spans and attributes, plus gambit.run.mode=human) and graded by the
// grader; the game is appended to the dataset when it ends.

// LiveConfig configures human mode. The embedded Config supplies the model,
// the agent, the grader and the dataset writer; its opponent fields are unused.
type LiveConfig struct {
	Config
	// MoveTimeout is the hard cap on one agent move, retries and tool calls
	// included. When it passes, the agent's decision is cut off and a random
	// legal move is played in its place, flagged in the trace and the UI.
	// 0 means no cap.
	MoveTimeout time.Duration
	// Script is the default scripted opening: the human side's first moves
	// (SAN or UCI). The UI offers them one at a time through the normal move
	// path, so they are validated like any other move.
	Script []string

	// Models is the allow-list of models a player may pick. Empty: the one
	// model in Config.Agent and Config.NewClient. DefaultModel is the ID used
	// when a game does not name one (default: the first).
	Models       []ModelChoice
	DefaultModel string

	// Limits for a shared deployment. The zero value of each is "off", so a
	// laptop behaves exactly as before.
	//
	// Exclusive refuses a new game while another is in progress (ErrGameInProgress)
	// instead of abandoning it. MaxGamesPerHour and MaxGamesPerDay cap how many
	// games may start in a rolling window (ErrRateLimited). IdleTimeout ends a
	// game whose human has not moved for that long, so an abandoned tab frees
	// the slot. GameTokenBudget ends a game once the agent has used that many
	// input+output tokens (checked before each agent move).
	Exclusive       bool
	MaxGamesPerHour int
	MaxGamesPerDay  int
	IdleTimeout     time.Duration
	GameTokenBudget int
}

// ModelChoice is one model a player may pick: how it is called and how the
// agent is configured for it.
type ModelChoice struct {
	ID        string       // stable key the UI sends, e.g. "haiku-5.5"
	Label     string       // shown to players
	Agent     agent.Config // Model, Temperature and MaxTokens differ per model
	NewClient func(seed int64, brain llm.Brain) llm.Client
}

// ModelInfo is the UI's view of a ModelChoice.
type ModelInfo struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
}

// LiveInfo is the static description of a live setup, for the UI.
type LiveInfo struct {
	Model           string      `json:"model"` // the default model
	Provider        string      `json:"provider"`
	Mock            bool        `json:"mock"`
	MoveTimeoutMS   int64       `json:"move_timeout_ms"`
	Script          []string    `json:"script"`
	Models          []ModelInfo `json:"models"`
	DefaultModel    string      `json:"default_model"`
	Exclusive       bool        `json:"exclusive"`
	IdleTimeoutMS   int64       `json:"idle_timeout_ms"`
	GameTokenBudget int         `json:"game_token_budget"`
}

// StartOptions describe a new game.
type StartOptions struct {
	HumanColor string   // "white" (default) or "black"
	Script     []string // nil: use the configured script; empty non-nil: none
	Model      string   // a ModelChoice ID; "" is the default
	// Player labels who is playing (a passcode label from the front proxy),
	// recorded on the game and on its traces. Not a secret.
	Player string
}

// Errors returned by Start.
var (
	// ErrGameInProgress: Exclusive is set and another game is running.
	ErrGameInProgress = errors.New("a game is already in progress; try again when it ends")
	// ErrRateLimited: MaxGamesPerHour or MaxGamesPerDay is used up. The error
	// wraps this value and says which, and when to retry.
	ErrRateLimited = errors.New("game limit reached")
	// ErrUnknownModel: the model is not on the allow-list.
	ErrUnknownModel = errors.New("unknown model")
)

// Event is one entry in a session's log.
type Event struct {
	ID   int             `json:"id"` // 1-based, contiguous: the SSE event id
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Live runs human games, one at a time.
type Live struct {
	cfg    LiveConfig
	p      *player
	models []modelEntry
	byID   map[string]int

	startMu  sync.Mutex // serialises Start
	mu       sync.Mutex
	sessions map[string]*Session
	order    []string
	cur      *Session
	seq      int
	starts   []time.Time // when recent games started, for the rate limits
	now      func() time.Time
	onGame   func(dataset.GameRecord, []dataset.MoveRecord)
}

// modelEntry is a ModelChoice plus the provider its client reports.
type modelEntry struct {
	ModelChoice
	provider string
}

const keepSessions = 20

// NewLive starts the engines. It fails fast, so a missing Stockfish shows up
// at launch rather than at the first move on stage.
func NewLive(cfg LiveConfig) (*Live, error) {
	if cfg.MaxPlies <= 0 {
		cfg.MaxPlies = 200
	}
	if cfg.NewClient == nil {
		return nil, errors.New("live: NewClient is required")
	}
	choices := cfg.Models
	if len(choices) == 0 {
		choices = []ModelChoice{{ID: "default", Label: cfg.Agent.Model, Agent: cfg.Agent, NewClient: cfg.NewClient}}
	}
	l := &Live{cfg: cfg, byID: map[string]int{}, sessions: map[string]*Session{}, now: time.Now}
	for _, c := range choices {
		if c.ID == "" || c.NewClient == nil {
			return nil, errors.New("live: a model choice needs an ID and NewClient")
		}
		if _, dup := l.byID[c.ID]; dup {
			return nil, fmt.Errorf("live: model %q listed twice", c.ID)
		}
		l.byID[c.ID] = len(l.models)
		l.models = append(l.models, modelEntry{ModelChoice: c, provider: c.NewClient(cfg.Seed, nil).Provider()})
	}
	if cfg.DefaultModel == "" {
		l.cfg.DefaultModel = l.models[0].ID
	} else if _, ok := l.byID[cfg.DefaultModel]; !ok {
		return nil, fmt.Errorf("live: default model %q is not in the allow-list", cfg.DefaultModel)
	}
	p, err := newGradingPlayer(cfg.Config, 0)
	if err != nil {
		return nil, err
	}
	l.p = p
	return l, nil
}

// Info describes the setup.
func (l *Live) Info() LiveInfo {
	d := l.models[l.byID[l.cfg.DefaultModel]]
	info := LiveInfo{
		Model: d.Agent.Model, Provider: d.provider, Mock: d.provider == "mock",
		MoveTimeoutMS: l.cfg.MoveTimeout.Milliseconds(), Script: l.cfg.Script,
		DefaultModel: l.cfg.DefaultModel, Exclusive: l.cfg.Exclusive,
		IdleTimeoutMS: l.cfg.IdleTimeout.Milliseconds(), GameTokenBudget: l.cfg.GameTokenBudget,
	}
	for _, m := range l.models {
		info.Models = append(info.Models, ModelInfo{ID: m.ID, Label: m.Label, Model: m.Agent.Model, Provider: m.provider})
	}
	return info
}

// Healthy reports whether the engines are up (for /healthz).
func (l *Live) Healthy() bool { return l.p != nil && l.p.grader != nil }

// rateLimit checks the rolling-window limits and, when they allow a game,
// records its start. The caller holds l.mu.
func (l *Live) rateLimit() error {
	now := l.now()
	cut := now.Add(-24 * time.Hour)
	keep := l.starts[:0]
	for _, t := range l.starts {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.starts = keep
	check := func(limit int, window time.Duration, name string) error {
		if limit <= 0 {
			return nil
		}
		var n int
		var oldest time.Time
		for _, t := range l.starts {
			if t.After(now.Add(-window)) {
				if n == 0 {
					oldest = t
				}
				n++
			}
		}
		if n >= limit {
			wait := oldest.Add(window).Sub(now).Round(time.Minute)
			if wait < time.Minute {
				wait = time.Minute
			}
			return fmt.Errorf("%w: %d games per %s are allowed; try again in about %s", ErrRateLimited, limit, name, wait)
		}
		return nil
	}
	if err := check(l.cfg.MaxGamesPerHour, time.Hour, "hour"); err != nil {
		return err
	}
	return check(l.cfg.MaxGamesPerDay, 24*time.Hour, "day")
}

// SetOnGame registers f to be called after each game has been written to the
// dataset (the UI uses it to offer the game for replay).
func (l *Live) SetOnGame(f func(dataset.GameRecord, []dataset.MoveRecord)) {
	l.mu.Lock()
	l.onGame = f
	l.mu.Unlock()
}

// Session returns a session by ID, or nil.
func (l *Live) Session(id string) *Session {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessions[id]
}

// Start begins a new game. A game still in progress is abandoned (and saved as
// such if the agent had played graded moves).
func (l *Live) Start(o StartOptions) (*Session, error) {
	l.startMu.Lock()
	defer l.startMu.Unlock()

	humanColor := chess.White
	switch o.HumanColor {
	case "", "white":
	case "black":
		humanColor = chess.Black
	default:
		return nil, fmt.Errorf("human_color must be white or black, not %q", o.HumanColor)
	}
	script := o.Script
	if script == nil {
		script = l.cfg.Script
	}
	modelID := o.Model
	if modelID == "" {
		modelID = l.cfg.DefaultModel
	}
	mi, ok := l.byID[modelID]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownModel, o.Model)
	}
	model := l.models[mi]

	l.mu.Lock()
	prev := l.cur
	l.mu.Unlock()
	if prev != nil && l.cfg.Exclusive && !prev.Ended() {
		return nil, ErrGameInProgress
	}
	l.mu.Lock()
	err := l.rateLimit()
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if prev != nil {
		if !prev.Ended() {
			prev.Abort()
		}
		select {
		case <-prev.done:
		case <-time.After(15 * time.Second):
			log.Printf("live: previous game %s is slow to finish; starting the next one anyway", prev.ID)
		}
	}
	if err := l.p.grader.NewGame(); err != nil {
		return nil, err
	}

	l.mu.Lock()
	l.seq++
	seq := l.seq
	l.starts = append(l.starts, l.now())
	l.mu.Unlock()

	started := time.Now().UTC()
	s := &Session{
		ID:         fmt.Sprintf("h%s-%04d", started.Format("20060102T150405"), seq),
		live:       l,
		humanColor: humanColor,
		human:      NewHumanOpponent(),
		model:      model,
		player:     o.Player,
		script:     script,
		started:    started,
		done:       make(chan struct{}),
		changed:    make(chan struct{}),
	}
	s.agentColor = chess.Black
	if humanColor == chess.Black {
		s.agentColor = chess.White
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	l.mu.Lock()
	l.sessions[s.ID] = s
	l.order = append(l.order, s.ID)
	for len(l.order) > keepSessions {
		delete(l.sessions, l.order[0])
		l.order = l.order[1:]
	}
	l.cur = s
	l.mu.Unlock()

	s.emit("game_started", map[string]any{
		"id": s.ID, "human_color": colorName(humanColor), "agent_color": colorName(s.agentColor),
		"model": model.Agent.Model, "model_id": model.ID, "model_label": model.Label,
		"provider": model.provider, "mock": model.provider == "mock", "player": o.Player,
		"start_fen":       chess.NewGame().Position().String(),
		"move_timeout_ms": l.cfg.MoveTimeout.Milliseconds(),
		"idle_timeout_ms": l.cfg.IdleTimeout.Milliseconds(),
		"token_budget":    l.cfg.GameTokenBudget,
		"script":          script,
	})
	go s.run(ctx, seq)
	return s, nil
}

// Close abandons the current game and stops the engines.
func (l *Live) Close() {
	l.mu.Lock()
	cur := l.cur
	l.mu.Unlock()
	if cur != nil {
		cur.Abort()
		select {
		case <-cur.done:
		case <-time.After(15 * time.Second):
		}
	}
	l.p.close()
}

// Session is one human-vs-agent game.
type Session struct {
	ID         string
	live       *Live
	humanColor chess.Color
	agentColor chess.Color
	human      *HumanOpponent
	model      modelEntry
	player     string
	script     []string
	started    time.Time
	cancel     context.CancelFunc
	done       chan struct{}

	mu       sync.Mutex
	events   []Event
	changed  chan struct{} // closed and replaced on every event
	finished bool
	ended    bool // the game itself is over (game_over emitted); grading and saving may still run
}

// Submit plays the human's move. It is rejected, and the game keeps waiting,
// if it is not the human's turn or the move is illegal.
func (s *Session) Submit(move string) (HumanMove, error) { return s.human.Submit(move) }

// Resign gives the game up.
func (s *Session) Resign() { s.human.Resign() }

// Abort abandons the game.
func (s *Session) Abort() { s.cancel() }

// Ended reports whether the game is over. It may still be grading and saving.
func (s *Session) Ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// Done is closed once the game has ended and been saved.
func (s *Session) Done() <-chan struct{} { return s.done }

// Events returns the events after the one with ID `after` (0 for all), a
// channel that closes when more arrive, and whether the log is complete.
func (s *Session) Events(after int) (evs []Event, more <-chan struct{}, complete bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if after < 0 {
		after = 0
	}
	if after < len(s.events) {
		evs = append(evs, s.events[after:]...)
	}
	return evs, s.changed, s.finished
}

func (s *Session) emit(typ string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		b = []byte(`{}`)
	}
	s.mu.Lock()
	s.events = append(s.events, Event{ID: len(s.events) + 1, Type: typ, Data: b})
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *Session) finish() {
	s.mu.Lock()
	s.finished = true
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	close(s.done)
}

func (s *Session) notice(level, msg string) {
	s.emit("notice", map[string]any{"level": level, "message": msg})
}

// run plays the game: the human's turn waits for Submit, the agent's turn
// runs a traced decision.
func (s *Session) run(ctx context.Context, seq int) {
	defer s.finish()
	l := s.live
	p := l.p
	cfg := l.cfg

	seed := cfg.Seed + int64(seq)*104729
	var brain llm.Brain
	if p.brain != nil {
		brain = engineBrain{p.brain}
	}
	client := s.model.NewClient(seed, brain)
	ag := agent.New(s.model.Agent, client, seed)
	provider := client.Provider()
	modelName := s.model.Agent.Model

	g := chess.NewGame()
	var (
		wg      sync.WaitGroup
		recMu   sync.Mutex
		records []dataset.MoveRecord
		failure error
	)
	scriptIdx, scriptOK := 0, len(s.script) > 0
	var tokensUsed int
	var idled, overBudget bool

	for g.Outcome() == chess.NoOutcome && len(g.Moves()) < cfg.MaxPlies {
		if ctx.Err() != nil {
			break
		}
		if s.human.Resigned() {
			g.Resign(s.humanColor)
			break
		}
		pos := g.Position()

		if pos.Turn() == s.agentColor {
			if cfg.GameTokenBudget > 0 && tokensUsed >= cfg.GameTokenBudget {
				overBudget = true
				s.notice("warn", fmt.Sprintf("This game used its %d-token budget, so it ends here.", cfg.GameTokenBudget))
				break
			}
			ply := len(g.Moves()) + 1
			ag.OnProgress = func(st agent.Stats) {
				s.emit("agent_progress", map[string]any{
					"ply": ply, "chat_calls": st.ChatCalls, "tool_calls": st.ToolCalls,
					"input_tokens": st.InputTokens, "output_tokens": st.OutputTokens,
					"illegal_attempts": st.IllegalAttempts,
				})
			}
			pm, err := p.decide(ctx, g, ag, provider, s.ID, moveOpts{
				mode: dataset.ModeHuman, timeout: cfg.MoveTimeout, model: modelName, player: s.player,
				started: func(traceID string) {
					s.emit("agent_thinking", map[string]any{
						"ply": ply, "trace_id": traceID,
						"started_ms": time.Now().UnixMilli(), "timeout_ms": cfg.MoveTimeout.Milliseconds(),
					})
				},
				decided: func(d decision) {
					st := d.Stats
					tokensUsed += st.InputTokens + st.OutputTokens
					s.emit("agent_moved", map[string]any{
						"ply": d.Ply, "move_number": d.MoveNumber, "color": d.Color,
						"san": d.SAN, "uci": d.UCI, "fen_after": d.FENAfter, "trace_id": d.TraceID,
						"latency_ms": float64(d.Latency.Microseconds()) / 1000,
						"chat_calls": st.ChatCalls, "tool_calls": st.ToolCalls,
						"input_tokens": st.InputTokens, "output_tokens": st.OutputTokens,
						"reasoning_tokens": st.ReasoningTokens, "illegal_attempts": st.IllegalAttempts,
						"finish_length": st.FinishLength, "fallback": st.Fallback, "timed_out": st.TimedOut,
						"legal_move_count": d.LegalMoves, "reasoning": truncate(st.Reasoning, 4000),
					})
				},
			})
			if err != nil {
				if ctx.Err() == nil {
					failure = fmt.Errorf("agent move: %w", err)
				}
				break
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec, err := pm.finish()
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("live %s: grading ply %d: %v", s.ID, pm.ply, err)
						s.notice("warn", fmt.Sprintf("Stockfish could not grade move %d (%v); it is left out of the dataset.", pm.ply, err))
					}
					return
				}
				if !cfg.KeepReasoning {
					rec.Reasoning = ""
				}
				recMu.Lock()
				records = append(records, rec)
				recMu.Unlock()
				s.emit("agent_graded", map[string]any{
					"ply": rec.Ply, "class": rec.Class, "cp_loss": rec.CPLoss,
					"win_chance_drop": rec.WinChanceDrop, "best_move": rec.BestMove,
					"eval_before_cp": rec.EvalBeforeCP, "eval_after_cp": rec.EvalAfterCP,
				})
			}()
			continue
		}

		// The human's turn.
		var legal []string
		for _, m := range pos.ValidMoves() {
			legal = append(legal, chess.UCINotation{}.Encode(pos, m))
		}
		scripted, scriptedUCI := "", ""
		if scriptOK && scriptIdx < len(s.script) {
			if mv := agent.ParseMove(pos, s.script[scriptIdx]); mv != nil {
				scripted = chess.AlgebraicNotation{}.Encode(pos, mv)
				scriptedUCI = chess.UCINotation{}.Encode(pos, mv)
			} else {
				scriptOK = false
				s.notice("info", fmt.Sprintf("The scripted move %q is not legal in this position: play freely from here.", s.script[scriptIdx]))
			}
		}
		s.emit("human_turn", map[string]any{
			"ply": len(g.Moves()) + 1, "fen": pos.String(), "legal": legal,
			"scripted": scripted, "scripted_uci": scriptedUCI,
		})
		hctx, hcancel := ctx, context.CancelFunc(func() {})
		if cfg.IdleTimeout > 0 {
			hctx, hcancel = context.WithTimeout(ctx, cfg.IdleTimeout)
		}
		mv, err := s.human.Move(hctx, g)
		hcancel()
		if errors.Is(err, ErrResigned) {
			g.Resign(s.humanColor)
			break
		}
		if err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				idled = true
				s.notice("warn", fmt.Sprintf("No move for %s, so the game was ended.", cfg.IdleTimeout))
			}
			break // the game was abandoned or went idle
		}
		san := chess.AlgebraicNotation{}.Encode(pos, mv)
		uci := chess.UCINotation{}.Encode(pos, mv)
		if err := g.Move(mv); err != nil {
			failure = fmt.Errorf("apply %s: %w", san, err)
			break
		}
		if scriptOK {
			if scripted == "" || scripted != san {
				scriptOK = false // the presenter left the script
			}
			scriptIdx++
		}
		s.emit("human_moved", map[string]any{
			"ply": len(g.Moves()), "san": san, "uci": uci, "fen_after": g.Position().String(),
		})
	}

	// --- the end ---
	method := g.Method().String()
	switch {
	case failure != nil:
		method = "Error"
		log.Printf("live %s: %v", s.ID, failure)
		s.emit("error", map[string]any{"message": failure.Error()})
	case idled:
		method = "IdleTimeout"
	case overBudget:
		method = "TokenBudget"
	case ctx.Err() != nil:
		method = "Abandoned"
	case g.Outcome() == chess.NoOutcome:
		method = "MaxPlies"
	}
	s.emit("game_over", map[string]any{
		"result": g.Outcome().String(), "method": method, "plies": len(g.Moves()),
	})
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()

	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(20 * time.Second):
		s.notice("warn", "Grading is taking too long; saving the game with the moves graded so far.")
	}

	recMu.Lock()
	moves := append([]dataset.MoveRecord(nil), records...)
	recMu.Unlock()
	sort.Slice(moves, func(a, b int) bool { return moves[a].Ply < moves[b].Ply })

	res := dataset.GameRecord{
		GameID: s.ID, Provider: provider, Model: modelName, Player: s.player,
		AgentColor: colorName(s.agentColor),
		Result:     g.Outcome().String(), Method: method, Plies: len(g.Moves()),
		PGN: g.String(), StartedAt: s.started.Format(time.RFC3339),
		Mode: dataset.ModeHuman,
	}
	res.Blunders, res.Mistakes = tally(moves)
	saved := false
	if cfg.Writer != nil && len(moves) > 0 {
		if err := cfg.Writer.WriteGame(res, moves); err != nil {
			log.Printf("live %s: saving game: %v", s.ID, err)
			s.notice("warn", "Could not save the game to the dataset: "+err.Error())
		} else {
			saved = true
			l.mu.Lock()
			onGame := l.onGame
			l.mu.Unlock()
			if onGame != nil {
				onGame(res, moves)
			}
		}
	}
	log.Printf("live game %s: agent=%s result=%s (%s) plies=%d blunders=%d mistakes=%d saved=%t",
		s.ID, res.AgentColor, res.Result, res.Method, res.Plies, res.Blunders, res.Mistakes, saved)
	s.emit("game_saved", map[string]any{
		"game_id": s.ID, "saved": saved, "blunders": res.Blunders, "mistakes": res.Mistakes,
	})
}
