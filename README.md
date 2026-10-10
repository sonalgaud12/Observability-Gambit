# The Observability Gambit

**Can OpenTelemetry traces predict when an LLM blunders?**

An LLM agent plays chess against Stockfish. Every move is a trace, instrumented
with the `gen_ai.*` semantic conventions. A second Stockfish grades every move
at fixed depth, which gives observability something it rarely has: an
objective scoreboard. A vendor-neutral Collector pipeline joins the two, and
the analysis asks whether the traces saw the blunder coming.

Everything is Go, and everything runs locally.

```
                 ┌─────────────── gambit play ───────────────┐
 OpenAI-compat.  │  agent ──chat/tools/retries──▶ LLM         │
 LLM endpoint ◀──┤  Stockfish (opponent)  Stockfish (grader)  │
                 └──────────────┬────────────────────────────┘
                                │ OTLP (one trace per agent move)
                       ┌────────▼─────────┐
                       │  OTel Collector  │
                       └─┬──────┬──────┬──┘
     traces/all ─────────┘      │      └──────── traces/sampled
     ClickHouse + OTLP-JSON     │                tail_sampling ─▶ Jaeger
     (SQL correlation, replay)  │                (blunders, retries, errors,
                                │                 slow decisions, 10% baseline)
                     spanmetrics ─▶ Prometheus (exemplars ─▶ Jaeger)
                                │
                             Grafana
```

## Trace shape

One trace per agent move, tied together per game by `gen_ai.conversation.id`:

```
chess.move                         root: chess.* context + the label
├── invoke_agent chess-agent       the decision; its duration is the latency feature
│   ├── chat gpt-4o-mini           gen_ai.* request/response/usage, finish reason
│   ├── chess.validate_move        candidate "Qh5" → illegal (error.type=illegal_move)
│   ├── chat gpt-4o-mini           the retry
│   ├── execute_tool threats       "knight on c6 can take your queen on d4"
│   ├── chat gpt-4o-mini
│   └── chess.validate_move        candidate "Qd1" → legal
└── chess.evaluate                 Stockfish depth 14: cp loss, win-chance drop, class
```

Grading is a **sibling** of the decision, not a child, so decision latency never
includes engine time. The label lives on the root span, so the Collector's tail
sampler can keep every blunder.

## Quick start (no API key)

Needs Go 1.23+, Docker, and Stockfish (`brew install stockfish` / `apt install stockfish`).

```bash
make up          # Collector, Jaeger, ClickHouse, Prometheus, Grafana
make rehearse    # 50 games with the offline mock model
make analyze     # out/report.md + out/model.json
make serve       # stage UI on http://localhost:8080
```

Then open Grafana (http://localhost:3000), Jaeger (http://localhost:16686) and the UI.

> **About the mock model.** It has a hidden "stress" variable that rises during
> the game and drives *both* its telemetry and its blunders, so every
> correlation in mock data was put there on purpose. It exists to rehearse the
> pipeline and the stage flow. Mock spans carry `gambit.llm.mock=true`, the
> report and UI are stamped "mock", and none of it is a finding.

## Real runs

Any OpenAI-compatible endpoint works.

```bash
# OpenAI
export OPENAI_API_KEY=sk-...
make play GAMES=200 MODEL=gpt-4o-mini

# Ollama, fully local
make play GAMES=200 MODEL=llama3.1:8b BASE_URL=http://localhost:11434/v1 PROVIDER=ollama

# Any other compatible gateway
./bin/gambit play --base-url https://<gateway>/v1 --provider <name> --model <model> --games 100
```

Useful knobs (`./bin/gambit play -h` for all):

| Flag | Default | Why it matters |
|---|---|---|
| `--opponent-elo` | 1400 | Pick it so the model loses slowly. Too strong and every game is decided by move 15. |
| `--tools` | true | `legal_moves` and `threats` tools add `execute_tool` spans, a possible signal. |
| `--legal-moves-in-prompt` | false | Fewer illegal moves, but retries are one of the signals you're testing. |
| `--grader-depth` | 14 | Higher is a better label, slower runs. 14 is plenty for club-level blunders. |
| `--temperature` | 0.7 | Some variance makes per-move differences visible. |

**Budget before you run.** A game is about 40 agent moves, each 1–4 chat calls
of roughly 500–1,500 input tokens. Run 5 games first and read the token totals
in `data/moves.jsonl` before launching hundreds.

## Analysis

```bash
./bin/gambit analyze --label blunder        # or --label mistake+ if blunders are rare
```

It compares four models on held-out games:

| Model | Features | Question it answers |
|---|---|---|
| `position` | move number, legal moves, check, material | Baseline: no telemetry at all |
| `trace` | this move's latency, tokens, tool calls, retries, finish reason | Is there signal in a single trace? |
| `at_move` | position + this move's trace | Could we veto the move **before it hits the board**? |
| `forecast` | position + the previous 3 moves' traces | Could we warn **before the move is even requested**? |

`forecast` is the "before move 23" question. Telemetry earns its keep only where
it beats `position` by more than the bootstrap confidence interval.

### Methodology traps, already handled

- **Decided positions can't blunder.** Once a side is lost, winning chances are
  pinned near zero and no move registers as a blunder. Unfiltered, "the losing
  side never blunders" looks like a great predictor. The mock data showed it
  plainly: material balance hit AUC 0.90. `--max-abs-eval 400` (the default)
  analyses only contested positions. It's a row filter on the label's own
  scale, never a feature.
- **Split by game, not by move.** Moves in one game aren't independent; a
  random move split leaks. The bootstrap also resamples whole games.
- **No label leakage.** Feature extractors never read `chess.eval.*` or
  `chess.move.class`, and history features only see earlier moves (unit-tested).
- **Win chances, not raw centipawns.** Classes use Lichess-style thresholds on
  the drop in winning chances (0.1 / 0.2 / 0.3).

The same join works in SQL against ClickHouse: `make sql` runs
`sql/correlation.sql`, which builds a per-move feature/label view from raw spans
with nothing but `TraceId` joins. That's the reusable takeaway: attach a
ground-truth label to the trace, then join it back to the trace's own telemetry.

## Stage runbook

**Before the talk**
1. Collect real games a week ahead, then `make analyze`.
2. In the UI, games are sorted by their worst blunder. Pick the one with the best
   story (ideally a hanging queen with risk climbing in the moves before it).
3. `make replay GAME=<id>` so that game's traces are fresh in Jaeger, and check
   that the UI's "Open trace" links resolve.
4. Record a screen capture of the whole flow as a fallback.

**On stage** (no live LLM calls, no Wi-Fi dependency)
1. `make serve`, open `http://localhost:8080/?game=<id>`, press <kbd>space</kbd>.
   Playback pauses automatically on the blunder.
2. The moves before it: the **Incoming** panel shows the forecast risk for the
   agent's next move, computed from previous traces only.
3. The blunder lands. The engine's preferred move is outlined in green.
4. **Open trace**: the retries, the tool calls, the model's own reasoning (content
   capture is on).
5. Grafana: blunder rate by latency decile from ClickHouse, then click an
   exemplar dot on the latency panel to land on a real trace.
6. Close with the report's model table: where telemetry beat the baseline, and where it didn't.

Keyboard: <kbd>←</kbd>/<kbd>→</kbd> step, <kbd>space</kbd> play, <kbd>b</kbd> next blunder.
Every state is a URL (`?game=…&ply=…`), so you can bookmark exact moments.

## Where `gen_ai.*` held up, and where it didn't

What I ran into while building this. Check each point against the semconv
version you present; these conventions are moving.

**Held up**
- `chat {model}` spans with request model, response model, usage tokens and
  finish reasons map cleanly onto the OpenAI-compatible response shape.
- `invoke_agent` / `execute_tool` give the agent loop a readable tree.
- `gen_ai.conversation.id` is a natural way to tie per-move traces into a game.
- Opt-in content capture (`gen_ai.input.messages` / `gen_ai.output.messages`,
  parts format) is what makes the blunder trace explainable on stage.

**Gaps**
- **No outcome or quality signal.** The label arrives after `invoke_agent` has
  ended. That forces a root span to carry it, and it means spanmetrics cannot
  slice *decision* latency by move class; you have to join in the backend.
- **Reasoning tokens** have no stable key (here: `gambit.usage.reasoning_tokens`).
- **Invalid agent output** has no convention. "The model answered, but with an
  illegal action" is modeled as `chess.validate_move` spans with
  `error.type=illegal_move` plus a retry count.
- **`gen_ai.response.finish_reasons` is an array.** It's awkward as a metrics
  dimension, and it lands in ClickHouse as a serialized string (the SQL uses
  `position(...)` to find `length`).
- **Churn.** `gen_ai.system` became `gen_ai.provider.name`. All keys live in
  `internal/semconv/attrs.go`, so a rename is a one-file change.
- **Content capture is heavy.** Full prompts on every span are large and
  potentially sensitive; in production you'd keep them only on the analysis path.

## Repo layout

```
cmd/gambit/              CLI: play, analyze, serve, extract-traces
internal/agent/          the LLM player: prompts, tools, retries, validation spans
internal/llm/            OpenAI-compatible client, gen_ai instrumentation, mock model
internal/engine/         minimal UCI client for Stockfish
internal/game/           game runner, trace shape, grading and classification
internal/analysis/       features, logistic regression, AUC + bootstrap, report
internal/web/            stage UI (embedded single page)
internal/semconv/        every attribute key in one place
internal/otlpjson/       cut one game's traces out of a recording
collector/               Collector pipeline + replay config
deploy/                  Prometheus, Grafana (datasources, dashboard), ClickHouse init
sql/correlation.sql      the label ⨝ trace join in pure SQL
```

## Telemetry configuration

Standard OTel environment variables apply: `OTEL_EXPORTER_OTLP_ENDPOINT`
(default `http://localhost:4317`), `OTEL_SERVICE_NAME` (default `chess-agent`),
`OTEL_RESOURCE_ATTRIBUTES`, `OTEL_SDK_DISABLED`, and
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true` for prompt/completion
capture (the Makefile turns it on).
