// Command gambit plays, analyses and replays LLM chess games instrumented with
// OpenTelemetry.
//
//	gambit play     play games against Stockfish and emit one trace per agent move
//	gambit analyze  correlate trace features with engine-graded move quality
//	gambit serve    stage UI: replay games with the risk timeline and trace links;
//	                with --live, also play the agent yourself (human mode)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/analysis"
	"github.com/sonalgaud12/observability-gambit/internal/dataset"
	"github.com/sonalgaud12/observability-gambit/internal/game"
	"github.com/sonalgaud12/observability-gambit/internal/llm"
	"github.com/sonalgaud12/observability-gambit/internal/otlpjson"
	sc "github.com/sonalgaud12/observability-gambit/internal/semconv"
	"github.com/sonalgaud12/observability-gambit/internal/telemetry"
	"github.com/sonalgaud12/observability-gambit/internal/web"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "play":
		err = play(os.Args[2:])
	case "analyze", "analyse":
		err = analyze(os.Args[2:])
	case "serve":
		err = serve(os.Args[2:])
	case "extract-traces":
		err = extractTraces(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `gambit: can OpenTelemetry traces predict when an LLM blunders?

Usage:
  gambit play     [flags]   play games and emit traces        (gambit play -h)
  gambit analyze  [flags]   correlate traces with move quality (gambit analyze -h)
  gambit serve    [flags]   stage UI: replay, or --live to play the agent (gambit serve -h)
  gambit extract-traces --game ID   cut one game's traces out for replay
`)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// modelFlags are the flags shared by `play` and `serve --live`: which model
// plays, how it is prompted, and how its moves are graded.
type modelFlags struct {
	mock          *bool
	baseURL       *string
	provider      *string
	model         *string
	temperature   *float64
	maxTokens     *int
	retries       *int
	legalInPrompt *bool
	tools         *bool
	stockfish     *string
	depth         *int
	maxPlies      *int
	seed          *int64
	mockBrain     *int
	keepReasoning *bool
	effort        *string
	llmTimeout    *time.Duration
	tempSet       bool
}

// registerModelFlags adds the shared flags to fs. modelFlag is the name of the
// model-name flag: `play` calls it "model", while `serve` already uses --model
// for the analysis bundle.
func registerModelFlags(fs *flag.FlagSet, modelFlag string) *modelFlags {
	return &modelFlags{
		mock:          fs.Bool("mock", false, "use the offline mock model (rehearsal only: its correlations are synthetic)"),
		baseURL:       fs.String("base-url", envOr("GAMBIT_BASE_URL", ""), "API base URL (default: https://api.openai.com/v1, or https://api.anthropic.com for --provider anthropic; Ollama: http://localhost:11434/v1)"),
		provider:      fs.String("provider", envOr("GAMBIT_PROVIDER", "openai"), "gen_ai.provider.name for spans; \"anthropic\" also selects the native Anthropic Messages API (shows Claude's thinking summary), and \"openai-responses\" OpenAI's Responses API (shows the model's reasoning summary; spans say provider openai), instead of the OpenAI-compatible Chat Completions client"),
		model:         fs.String(modelFlag, envOr("GAMBIT_MODEL", "gpt-4o-mini"), "model name"),
		temperature:   fs.Float64("temperature", 0.7, "sampling temperature (negative = provider default)"),
		maxTokens:     fs.Int("max-tokens", 1024, "max output tokens per call"),
		retries:       fs.Int("retries", 3, "illegal-move retries before a random fallback move"),
		legalInPrompt: fs.Bool("legal-moves-in-prompt", false, "list legal moves in the prompt (fewer illegal moves, less signal)"),
		tools:         fs.Bool("tools", true, "offer the legal_moves and threats tools"),
		stockfish:     fs.String("stockfish", envOr("STOCKFISH_PATH", "stockfish"), "path to the Stockfish binary"),
		depth:         fs.Int("grader-depth", 14, "Stockfish depth used to grade agent moves"),
		maxPlies:      fs.Int("max-plies", 200, "stop games after this many plies"),
		seed:          fs.Int64("seed", time.Now().UnixNano(), "random seed"),
		mockBrain:     fs.Int("mock-brain-skill", 6, "mock only: Stockfish Skill Level (0-20) the mock follows while calm; -1 = none"),
		keepReasoning: fs.Bool("keep-reasoning", true, "store the model's final answer text in the dataset (shown in the UI)"),
		effort:        fs.String("effort", envOr("GAMBIT_EFFORT", ""), "reasoning effort: low, medium, high, xhigh or max (anthropic: output_config.effort; \"\" = the model's default, medium on Claude Haiku 5.5); openai-responses: none, minimal, low, medium, high or xhigh (reasoning.effort)"),
		llmTimeout:    fs.Duration("llm-timeout", 0, "HTTP timeout for one model call, thinking included (0 = 10m for --provider anthropic and openai-responses, 3m otherwise)"),
	}
}

// finish applies defaults that depend on which flags the user actually set.
// Call it after fs.Parse.
func (m *modelFlags) finish(fs *flag.FlagSet) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	// Claude 5 models and OpenAI's reasoning models reject a non-default
	// temperature, so the 0.7 default is wrong for them: leave it to the
	// provider unless the user chose one.
	if !set["temperature"] && (m.native() || m.responses()) {
		*m.temperature = -1
	}
	// OpenAI's output cap includes the reasoning tokens, so the 1024 default
	// would end moves as "length" before the model answers.
	if !set["max-tokens"] && m.responses() {
		*m.maxTokens = 4096
	}
}

// moveTimeout resolves --move-timeout. "auto" is 15s for models that answer
// directly, and longer for Claude, whose thinking counts against the same cap:
// see autoMoveTimeout. ids are the --models presets offered, if any.
func (m *modelFlags) moveTimeout(flagValue string, ids []string) (time.Duration, error) {
	if flagValue != "auto" {
		d, err := time.ParseDuration(flagValue)
		if err != nil || d < 0 {
			return 0, fmt.Errorf("--move-timeout %q: want a duration such as 20s, 0 for no cap, or auto", flagValue)
		}
		return d, nil
	}
	claude := m.native() || m.responses() // models that reason before they answer
	for _, id := range ids {
		if presets[id].Provider == "anthropic" {
			claude = true
		}
	}
	if *m.mock || !claude {
		return autoMoveTimeout(false, ""), nil
	}
	return autoMoveTimeout(true, *m.effort), nil
}

// autoMoveTimeout is the per-move cap: 15s for a model that answers directly,
// and when a Claude model thinks, by effort ("" is the model's default, medium
// on Claude Haiku 5.5). These are estimates
// of how long a move with a few tool rounds and one illegal-move retry may
// take, not measurements: raise them if moves time out, or lower --effort.
// A longer cap means a hung model costs the player longer before the random
// fallback move.
func autoMoveTimeout(claude bool, effort string) time.Duration {
	if !claude {
		return 15 * time.Second
	}
	switch effort {
	case "low":
		return 20 * time.Second
	case "high":
		return 60 * time.Second
	case "xhigh":
		return 90 * time.Second
	case "max":
		return 120 * time.Second
	default: // medium
		return 30 * time.Second
	}
}

// native reports whether the model is called through Anthropic's native
// Messages API rather than an OpenAI-compatible endpoint.
func (m *modelFlags) native() bool { return !*m.mock && *m.provider == "anthropic" }

// responses reports whether the model is called through OpenAI's Responses API
// rather than Chat Completions.
func (m *modelFlags) responses() bool { return !*m.mock && *m.provider == "openai-responses" }

// newClient builds the (uninstrumented) client for the chosen provider.
func newClient(provider, baseURL, apiKey, effort string, timeout time.Duration) llm.Client {
	if provider == "anthropic" {
		c := llm.NewAnthropic(baseURL, apiKey)
		c.Effort = effort
		if timeout > 0 {
			c.SetTimeout(timeout)
		}
		return c
	}
	if provider == "openai-responses" {
		c := llm.NewOpenAIResponses(baseURL, apiKey)
		c.Effort = effort
		if timeout > 0 {
			c.SetTimeout(timeout)
		}
		return c
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	c := llm.NewOpenAICompatible(baseURL, apiKey, provider)
	c.ReasoningEffort = effort
	if timeout > 0 {
		c.HTTP.Timeout = timeout
	}
	return c
}

func (m *modelFlags) apiKey() string {
	if *m.provider == "anthropic" { // never send an OpenAI key to Anthropic
		return envOr("GAMBIT_API_KEY", os.Getenv("ANTHROPIC_API_KEY"))
	}
	return envOr("GAMBIT_API_KEY", os.Getenv("OPENAI_API_KEY"))
}

// check fails early when a real model is selected without a way to call it.
func (m *modelFlags) check() error {
	if m.native() && strings.Contains(*m.baseURL, "api.openai.com") {
		return fmt.Errorf("--provider anthropic with --base-url %s: that is OpenAI's API, and it would be sent the Anthropic key", *m.baseURL)
	}
	if !*m.mock && m.apiKey() == "" && !strings.Contains(*m.baseURL, "localhost") && !strings.Contains(*m.baseURL, "127.0.0.1") {
		keyEnv := "OPENAI_API_KEY"
		if *m.provider == "anthropic" {
			keyEnv = "ANTHROPIC_API_KEY"
		}
		return fmt.Errorf("no API key: set %s or GAMBIT_API_KEY, use a local --base-url, or pass --mock", keyEnv)
	}
	return nil
}

// resourceAttrs marks the whole process as mock when the mock model is used.
func (m *modelFlags) resourceAttrs() []attribute.KeyValue {
	if !*m.mock {
		return nil
	}
	log.Print("MOCK MODE: telemetry and blunders share a hidden cause by design. Rehearsal only.")
	return []attribute.KeyValue{sc.GambitMock.Bool(true)}
}

func (m *modelFlags) providerName() string {
	if *m.mock {
		return "mock"
	}
	return *m.provider
}

func (m *modelFlags) modelName() string {
	if *m.mock {
		return "mock-1"
	}
	return *m.model
}

// gameConfig builds the game configuration. Opponent settings are left to the caller.
func (m *modelFlags) gameConfig(w *dataset.Writer) game.Config {
	apiKey, providerName := m.apiKey(), m.providerName()
	cfg := game.Config{
		Agent: agent.Config{
			Model: m.modelName(), Temperature: *m.temperature, MaxTokens: *m.maxTokens, MaxRetries: *m.retries,
			LegalMovesInPrompt: *m.legalInPrompt, Tools: *m.tools,
		},
		NewClient: func(s int64, brain llm.Brain) llm.Client {
			if *m.mock {
				mm := llm.NewMock(s)
				mm.Brain = brain
				return llm.Instrument(mm)
			}
			return llm.Instrument(newClient(providerName, *m.baseURL, apiKey, *m.effort, *m.llmTimeout))
		},
		StockfishPath:  *m.stockfish,
		MockBrainSkill: -1,
		GraderDepth:    *m.depth, MaxPlies: *m.maxPlies, Seed: *m.seed, Writer: w, KeepReasoning: *m.keepReasoning,
	}
	if *m.mock {
		cfg.MockBrainSkill = *m.mockBrain
	}
	return cfg
}

// setupTelemetry installs the OTLP exporter; the returned func flushes it.
func setupTelemetry(ctx context.Context, extra []attribute.KeyValue) (func(), error) {
	shutdown, err := telemetry.Setup(ctx, extra...)
	if err != nil {
		return nil, err
	}
	return func() {
		sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if err := shutdown(sctx); err != nil {
			log.Printf("telemetry shutdown: %v", err)
		}
	}, nil
}

func play(args []string) error {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	games := fs.Int("games", 10, "number of games to play")
	parallel := fs.Int("parallel", 2, "games played concurrently")
	m := registerModelFlags(fs, "model")
	opponent := fs.String("opponent", "stockfish", "opponent: stockfish | random")
	elo := fs.Int("opponent-elo", 1400, "Stockfish UCI_Elo (min 1320); 0 = full strength")
	moveMS := fs.Int("opponent-movetime", 100, "opponent think time per move, ms")
	out := fs.String("out", "data", "dataset directory (appends)")
	fs.Parse(args)
	m.finish(fs)

	if err := m.check(); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	flush, err := setupTelemetry(ctx, m.resourceAttrs())
	if err != nil {
		return err
	}
	defer flush()

	w, err := dataset.NewWriter(*out)
	if err != nil {
		return err
	}
	defer w.Close()

	cfg := m.gameConfig(w)
	cfg.Opponent, cfg.OpponentElo, cfg.OpponentMoveMS = *opponent, *elo, *moveMS
	log.Printf("playing %d games: %s/%s vs %s (elo %d), grader depth %d -> %s/",
		*games, m.providerName(), m.modelName(), *opponent, *elo, *m.depth, *out)
	return game.Run(ctx, cfg, *games, *parallel)
}

func analyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	data := fs.String("data", "data", "dataset directory")
	out := fs.String("out", "out", "where to write report.md and model.json")
	label := fs.String("label", "blunder", "positive class: blunder | mistake+")
	model := fs.String("model", "", "only analyse moves from this model name")
	testFrac := fs.Float64("test-frac", 0.3, "fraction of games held out for testing")
	alert := fs.Float64("alert-rate", 0.15, "fraction of moves the alert threshold flags")
	l2 := fs.Float64("l2", 0.01, "L2 regularization")
	maxEval := fs.Int("max-abs-eval", 400, "analyse only contested positions, |eval before| <= N centipawns (0 = all)")
	mode := fs.String("mode", "engine", "which games: engine (agent vs Stockfish), human (played from the stage UI) or all")
	fs.Parse(args)

	_, md, err := analysis.Run(analysis.Options{
		DataDir: *data, OutDir: *out, Label: *label, Model: *model, Mode: *mode,
		TestFrac: *testFrac, AlertRate: *alert, L2: *l2, MaxAbsEval: *maxEval,
	})
	if err != nil {
		return err
	}
	fmt.Println(md)
	log.Printf("wrote %s/report.md and %s/model.json", *out, *out)
	return nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "localhost:8080", "listen address")
	data := fs.String("data", "data", "dataset directory")
	model := fs.String("model", "out/model.json", "model bundle from `gambit analyze`")
	traceURL := fs.String("trace-url", envOr("GAMBIT_TRACE_URL", web.ExploreTraceURL("http://localhost:3000")),
		"trace deep link; {trace_id} is replaced. Default: the trace in Grafana Explore on the Tempo datasource")
	live := fs.Bool("live", false, "human mode: play the agent from the stage UI (needs Stockfish and a model; see the model flags)")
	moveTimeoutFlag := fs.String("move-timeout", envOr("GAMBIT_MOVE_TIMEOUT", "auto"), "live: hard cap on one agent move; on timeout a random legal move is played and flagged. A duration (20s), 0 for no cap, or auto: 15s, or longer when Claude thinks (by --effort; see README)")
	script := fs.String("script", "", "live: scripted opening, the human's moves separated by spaces or commas, e.g. \"e4 Nf3 Bc4\"")
	traceDelay := fs.Duration("trace-delay", 5*time.Second, "live: wait this long after a move is graded before offering its trace link (the Collector batches spans for ~2s before Tempo gets them; 0 = offer it at once)")
	// These only matter with --live. `serve --model` is the analysis bundle,
	// so the live agent's model is --agent-model.
	m := registerModelFlags(fs, "agent-model")
	// Shared deployment (deploy/gcp/README.md). Every limit is off by
	// default, so a laptop behaves exactly as before.
	models := fs.String("models", "", "live: offer exactly these models, picked per game on the start screen (comma list of: "+knownPresets()+"; the first is the default). Each needs its key in OPENAI_API_KEY / ANTHROPIC_API_KEY and replaces --agent-model, --base-url, --provider")
	defaultModel := fs.String("default-model", "", "live: the --models entry preselected on the start screen (default: the first)")
	exclusive := fs.Bool("exclusive", false, "live: refuse a new game (HTTP 409) while one is in progress, instead of abandoning it")
	perHour := fs.Int("max-games-per-hour", 0, "live: games that may start per rolling hour (HTTP 429 beyond that; 0 = no limit)")
	perDay := fs.Int("max-games-per-day", 0, "live: games that may start per rolling 24 hours (0 = no limit)")
	idle := fs.Duration("idle-timeout", 0, "live: end a game whose human has not moved for this long (0 = never)")
	tokenBudget := fs.Int("game-token-budget", 0, "live: end a game once the agent has used this many input+output tokens (0 = no limit)")
	noCoach := fs.Bool("no-coach", false, "live: turn the chess coach off (by default a model call comments on each of the human's moves, and any move can be explained on request; it uses the game's model and has its own token allowance of half --game-token-budget)")
	fs.Parse(args)
	m.finish(fs)

	opts := web.Options{Addr: *addr, DataDir: *data, ModelPath: *model, TraceURL: *traceURL}
	if !*live {
		return web.Serve(opts)
	}

	var choices []game.ModelChoice
	var ids []string
	if *models != "" {
		var err error
		if ids, err = parseModelList(*models); err != nil {
			return err
		}
		if choices, err = m.modelChoices(ids); err != nil {
			return err
		}
	} else if err := m.check(); err != nil {
		return err
	}
	moveTimeout, err := m.moveTimeout(*moveTimeoutFlag, ids)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	flush, err := setupTelemetry(ctx, m.resourceAttrs())
	if err != nil {
		return err
	}
	defer flush()

	w, err := dataset.NewWriter(*data)
	if err != nil {
		return err
	}
	defer w.Close()

	lv, err := game.NewLive(game.LiveConfig{
		Config:      m.gameConfig(w),
		MoveTimeout: moveTimeout,
		Script:      strings.FieldsFunc(*script, func(r rune) bool { return r == ',' || r == ' ' }),
		Models:      choices, DefaultModel: *defaultModel,
		Exclusive: *exclusive, MaxGamesPerHour: *perHour, MaxGamesPerDay: *perDay,
		IdleTimeout: *idle, GameTokenBudget: *tokenBudget, NoCoach: *noCoach,
	})
	if err != nil {
		return err
	}
	defer lv.Close()

	warnIfExposed(*addr)
	opts.Live, opts.TraceDelay, opts.Ctx = lv, *traceDelay, ctx
	info := lv.Info()
	log.Printf("human mode: %s/%s, move timeout %s, grader depth %d, recording to %s/",
		info.Provider, info.Model, moveTimeout, *m.depth, *data)
	if len(info.Models) > 1 {
		for _, mi := range info.Models {
			log.Printf("  model option %s: %s/%s", mi.ID, mi.Provider, mi.Model)
		}
		log.Printf("  default model option: %s", info.DefaultModel)
	}
	if *exclusive || *perHour > 0 || *perDay > 0 || *idle > 0 || *tokenBudget > 0 {
		log.Printf("limits: exclusive=%t, per hour %d, per day %d, idle timeout %s, token budget %d (0 = off)",
			*exclusive, *perHour, *perDay, *idle, *tokenBudget)
	}
	return web.Serve(opts)
}

// warnIfExposed: human mode has no login and every game spends model calls.
func warnIfExposed(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return
	}
	log.Printf("WARNING: human mode has no login and every game spends model calls, but %s is reachable from other machines; use localhost on stage", addr)
}

func extractTraces(args []string) error {
	fs := flag.NewFlagSet("extract-traces", flag.ExitOnError)
	gameID := fs.String("game", "", "game ID to extract (see `gambit serve` or data/games.jsonl)")
	data := fs.String("data", "data", "dataset directory (to map the game to its trace IDs)")
	in := fs.String("in", "data/traces/traces.jsonl", "Collector OTLP-JSON file")
	out := fs.String("out", "", "output file (default data/replay/<game>.jsonl)")
	fs.Parse(args)
	if *gameID == "" {
		return fmt.Errorf("--game is required")
	}
	moves, err := dataset.LoadMoves(*data)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, m := range moves {
		if m.GameID == *gameID {
			keep[m.TraceID] = true
		}
	}
	if len(keep) == 0 {
		return fmt.Errorf("no moves for game %s in %s", *gameID, *data)
	}
	if *out == "" {
		*out = "data/replay/" + *gameID + ".jsonl"
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	src, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer dst.Close()
	n, err := otlpjson.Filter(src, dst, keep)
	if err != nil {
		return err
	}
	log.Printf("wrote %d spans from %d traces to %s", n, len(keep), *out)
	return nil
}
