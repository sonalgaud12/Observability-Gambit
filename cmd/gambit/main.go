// Command gambit plays, analyses and replays LLM chess games instrumented with
// OpenTelemetry.
//
//	gambit play     play games against Stockfish and emit one trace per agent move
//	gambit analyze  correlate trace features with engine-graded move quality
//	gambit serve    stage UI: replay games with the risk timeline and trace links
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
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
  gambit serve    [flags]   stage replay UI                   (gambit serve -h)
  gambit extract-traces --game ID   cut one game's traces out for replay
`)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func play(args []string) error {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	games := fs.Int("games", 10, "number of games to play")
	parallel := fs.Int("parallel", 2, "games played concurrently")
	mock := fs.Bool("mock", false, "use the offline mock model (rehearsal only: its correlations are synthetic)")
	baseURL := fs.String("base-url", envOr("GAMBIT_BASE_URL", "https://api.openai.com/v1"), "OpenAI-compatible API base URL (Ollama: http://localhost:11434/v1)")
	provider := fs.String("provider", envOr("GAMBIT_PROVIDER", "openai"), "gen_ai.provider.name for spans")
	model := fs.String("model", envOr("GAMBIT_MODEL", "gpt-4o-mini"), "model name")
	temperature := fs.Float64("temperature", 0.7, "sampling temperature (negative = provider default)")
	maxTokens := fs.Int("max-tokens", 1024, "max output tokens per call")
	retries := fs.Int("retries", 3, "illegal-move retries before a random fallback move")
	legalInPrompt := fs.Bool("legal-moves-in-prompt", false, "list legal moves in the prompt (fewer illegal moves, less signal)")
	tools := fs.Bool("tools", true, "offer the legal_moves and threats tools")
	stockfish := fs.String("stockfish", envOr("STOCKFISH_PATH", "stockfish"), "path to the Stockfish binary")
	opponent := fs.String("opponent", "stockfish", "opponent: stockfish | random")
	elo := fs.Int("opponent-elo", 1400, "Stockfish UCI_Elo (min 1320); 0 = full strength")
	moveMS := fs.Int("opponent-movetime", 100, "opponent think time per move, ms")
	depth := fs.Int("grader-depth", 14, "Stockfish depth used to grade agent moves")
	maxPlies := fs.Int("max-plies", 200, "stop games after this many plies")
	out := fs.String("out", "data", "dataset directory (appends)")
	seed := fs.Int64("seed", time.Now().UnixNano(), "random seed")
	mockBrain := fs.Int("mock-brain-skill", 6, "mock only: Stockfish Skill Level (0-20) the mock follows while calm; -1 = none")
	keepReasoning := fs.Bool("keep-reasoning", true, "store the model's final answer text in the dataset (shown in the UI)")
	fs.Parse(args)

	apiKey := envOr("GAMBIT_API_KEY", os.Getenv("OPENAI_API_KEY"))
	if !*mock && apiKey == "" && !strings.Contains(*baseURL, "localhost") && !strings.Contains(*baseURL, "127.0.0.1") {
		return fmt.Errorf("no API key: set OPENAI_API_KEY or GAMBIT_API_KEY, use a local --base-url, or pass --mock")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var extra []attribute.KeyValue
	providerName := *provider
	if *mock {
		providerName = "mock"
		extra = append(extra, sc.GambitMock.Bool(true))
		log.Print("MOCK MODE: telemetry and blunders share a hidden cause by design. Rehearsal only.")
	}
	shutdown, err := telemetry.Setup(ctx, extra...)
	if err != nil {
		return err
	}
	defer func() {
		sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if err := shutdown(sctx); err != nil {
			log.Printf("telemetry shutdown: %v", err)
		}
	}()

	w, err := dataset.NewWriter(*out)
	if err != nil {
		return err
	}
	defer w.Close()

	modelName := *model
	if *mock {
		modelName = "mock-1"
	}
	cfg := game.Config{
		Agent: agent.Config{
			Model: modelName, Temperature: *temperature, MaxTokens: *maxTokens, MaxRetries: *retries,
			LegalMovesInPrompt: *legalInPrompt, Tools: *tools,
		},
		NewClient: func(s int64, brain llm.Brain) llm.Client {
			if *mock {
				m := llm.NewMock(s)
				m.Brain = brain
				return llm.Instrument(m)
			}
			return llm.Instrument(llm.NewOpenAICompatible(*baseURL, apiKey, providerName))
		},
		StockfishPath: *stockfish, Opponent: *opponent, OpponentElo: *elo, OpponentMoveMS: *moveMS,
		MockBrainSkill: -1,
		GraderDepth:    *depth, MaxPlies: *maxPlies, Seed: *seed, Writer: w, KeepReasoning: *keepReasoning,
	}
	if *mock {
		cfg.MockBrainSkill = *mockBrain
	}
	log.Printf("playing %d games: %s/%s vs %s (elo %d), grader depth %d -> %s/",
		*games, providerName, modelName, *opponent, *elo, *depth, *out)
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
	fs.Parse(args)

	_, md, err := analysis.Run(analysis.Options{
		DataDir: *data, OutDir: *out, Label: *label, Model: *model,
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
	traceURL := fs.String("trace-url", envOr("GAMBIT_TRACE_URL", "http://localhost:16686/trace/{trace_id}"),
		"trace deep link; {trace_id} is replaced (Grafana/Tempo works too)")
	fs.Parse(args)
	return web.Serve(web.Options{Addr: *addr, DataDir: *data, ModelPath: *model, TraceURL: *traceURL})
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
