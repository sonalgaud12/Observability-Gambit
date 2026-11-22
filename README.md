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

Alongside the traces, the SDK sends the GenAI convention **metrics**
(`gen_ai.client.inference.*`, `gen_ai.invoke_agent.*`,
`gen_ai.execute_tool.duration`) and **events** as log records: Stockfish's grade
becomes a `gen_ai.evaluation.result` event correlated with the
`invoke_agent` span. [docs/semconv-coverage.md](docs/semconv-coverage.md)
lists every key, metric and event, and what the conventions do not cover.

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

## Send to Grafana Cloud (optional)

By default nothing leaves your machine. To also send the traces to a Grafana
Cloud stack, copy `.env.cloud.example` to `.env.cloud` (git-ignored), fill in
the OTLP endpoint, instance ID and token, then use `make up-cloud` instead of
`make up`. The local pipelines (ClickHouse, Jaeger, file, spanmetrics) are
unchanged; `collector/cloud.yaml` adds a trace, a metrics and a logs pipeline
beside them.

It does three things on the Cloud copy only:

- Copies `gen_ai.input.messages` / `gen_ai.output.messages` /
  `gen_ai.system_instructions` to `gambit.prompt.input` / `gambit.prompt.output`
  / `gambit.prompt.system`. Grafana Cloud intentionally strips the original
  attributes from stored spans (open-source Tempo keeps them), so this keeps the
  prompts queryable. With content going to events (`EVENT_ONLY` or
  `SPAN_AND_EVENT`), the same content is also sent as the
  `gen_ai.client.inference.operation.details` log event, which is expected to
  show up in Loki. That path has not been tested against a real Grafana Cloud
  stack yet, so treat it as expected behaviour rather than something verified.
- Sets `agento11y.record=true`, so conversations also show up in Grafana Cloud
  Agent Observability.
- Adds `gambit.run` and `gambit.model` resource attributes (from `GAMBIT_RUN`
  and `GAMBIT_MODEL`, defaulting to `local` and `unknown`) so dashboards can tell
  runs apart.

If any of the three required settings is missing, `make up-cloud` stops with an
error rather than starting a Collector that quietly drops spans. If the
endpoint or token is wrong, the Collector logs `Exporting failed` for
`otlphttp/grafana` (`make logs`), while the local stores keep working.

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

## Human mode: play the agent

`gambit serve --live` adds a second page, `/live.html`, where a person plays the
agent instead of Stockfish. The audience watches the agent think (a clock, chat
calls, tokens and retries ticking up), sees its move land, then watches
Stockfish grade it and opens that move's trace. The recorded-game replay is
untouched: `/` still serves it, and `/live.html` links back to it.

```bash
make live-mock                     # offline mock model, no API key (rehearsal)
make live MODEL=gpt-4o-mini        # a real model, same variables as `make play`
make live MODEL=llama3.1:8b BASE_URL=http://localhost:11434/v1 PROVIDER=ollama
# then open http://localhost:8080/live.html
```

It needs Stockfish (it grades every move, the person's and the agent's) and the usual Collector stack if
you want the traces (`make up`). Without a Collector the game still plays and is
still recorded to `data/`. Flags (`./bin/gambit serve -h`; pass extras with
`LIVE_ARGS='...'`): the model flags are the ones `play` takes, except that the
model is `--agent-model` here because `--model` is the analysis bundle.

| Flag | Default | What it does |
|---|---|---|
| `--live` | off | Turn human mode on. Without it, `serve` is the replay UI only. |
| `--move-timeout` | 15s | Hard cap on one agent move, retries and tool calls included. `0` = no cap. |
| `--script` | none | Scripted opening: the human's moves, e.g. `"e4 Nf3 Bc4"`. Editable in the UI. |
| `--trace-delay` | 20s | How long after a move is graded its "Open trace" link is offered (see below). |
| `--grader-depth` | 14 | Same label as batch games. |

For a shared deployment (see [`deploy/gcp/README.md`](deploy/gcp/README.md)) there are more
flags. All are off by default, so on a laptop nothing changes:

| Flag | What it does |
|---|---|
| `--models haiku-5.5,luna-6` | Offer exactly these models; the player picks one per game on the start screen. The first is the default (`--default-model`). Each needs its key (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) and replaces `--agent-model`, `--base-url`, `--provider`. |
| `--exclusive` | A new game while one is running gets HTTP 409 instead of abandoning it. |
| `--max-games-per-hour`, `--max-games-per-day` | Cap game starts; beyond it HTTP 429 with a `Retry-After` header. |
| `--idle-timeout` | End a game whose human has not moved for this long. |
| `--game-token-budget` | End a game once the agent used this many input+output tokens. The coach has its own allowance of half that, counted apart, so it never ends a game. |
| `--no-coach` | Turn the coach off. |

`GET /healthz` answers `ok` (for uptime checks). A reverse proxy that authenticates players can pass
the name in an `X-Gambit-User` header; it becomes `gambit.player.label` on that game's traces and
`player` in `games.jsonl`.

### On stage

1. Start with `make live` (or `live-mock`). Press **New game**, pick a colour.
2. Play by clicking a piece, then a highlighted square. Illegal moves are never
   sent: the UI only offers legal squares, and the server re-checks every move
   and refuses a bad one with a reason. Pawns that reach the last rank ask for
   the piece.
3. **Scripted opening.** The box under the board holds the human's prepared line
   (`e4 Nf3 Bc4`). The board outlines the next scripted move; **space** (or the
   button) plays it through the normal move path, so it is validated like any
   other move. Play something else, or a scripted move stops being legal, and the
   script switches itself off for the rest of the game. Clear the box to play freely.
4. The agent's turn shows a live clock against the move timeout, plus chat calls,
   tokens, tool calls and illegal tries so far. When it moves, its panel shows the
   same chips as the replay and the model's answer; a second later the Stockfish
   grade (best … blunder) appears, and the **Open trace** link follows.
5. Click any agent move in the move list to bring its panel and trace back.
6. When the game ends the status box links to its replay, with the risk timeline.

### What happens when things go wrong

| Situation | What the audience sees | What is recorded |
|---|---|---|
| The model is slow or stuck | The clock turns amber, then red, against the timeout. At the timeout a **random legal move** is played and the panel says **Timed out**; the game goes on. | The decision span has `error.type=timeout`, `gambit.timed_out=true`, `chess.fallback=true`. A deliberately bad trace to open. |
| The model keeps answering with illegal moves | "illegal tries" climbs; after `--retries` a random legal move is played. | Same as a batch game: `chess.validate_move` spans, `chess.fallback=true`. |
| The human tries an illegal move | A toast; nothing is sent. The server also rejects it (HTTP 422). | Nothing. |
| The browser loses the connection | A banner, and the browser reconnects by itself and replays exactly the events it missed (every event has an id; `Last-Event-ID` resumes). | Nothing lost: the game lives on the server. |
| The server is overdue | If an answer is more than 4s past the timeout, a banner says so instead of freezing. | |
| Stockfish cannot grade a move | A warning; the game goes on. | That move is left out of the dataset (its trace is marked as an error). |

**Fallback: a recorded game.** Human mode is the riskiest part of the talk, so
keep the replay ready. The week before, play a rehearsal through `make live-mock`
or `make live` and pick the game you like: every finished game is written to
`data/` like any other, so `make serve` replays it with the risk timeline, and
`make replay GAME=<id>` puts its traces back into the stack. On stage, if the live
game misbehaves, press **Recorded games ▸** (or open `/?game=<id>` directly): it
is the same board and panels, driven by the recording instead of the model. Also
keep a screen capture.

### What gets recorded

An agent move in a human game is traced exactly like one in a batch game (same
spans, same attributes, graded by the same second Stockfish), with these extras:

- `gambit.run.mode=human` and `chess.opponent.type=human` on the root span (the
  mode is also on `invoke_agent`, so the tail sampler can see it early). Batch games
  carry neither.
- `data/moves.jsonl` and `data/games.jsonl` rows with `"mode":"human"`, and
  `"timed_out":true` on a move cut off by the timeout. The game id starts with `h`.
- `gambit analyze --mode engine|human|all` picks which games the analysis uses.
  The default, `engine`, leaves human games out, so a stage game never changes the
  headline numbers; reports for the other modes say so at the top.
- In ClickHouse, `otel.gambit_moves` has a `mode` column (`''` or `'human'`).
  Dashboards and `make sql` include human games unless you filter on it.
- The Grafana Cloud route (`make up-cloud`) is unchanged and sends human traces too.

### Feedback for the person playing

The person's own moves are graded too, so a beginner learns while they play:

- Each of their moves is graded by the same Stockfish grader, with the same five
  classes (best, good, inaccuracy, mistake, blunder), in the background: the
  board and the agent's turn never wait for it. If the grader fails, only the
  grade is missing.
- After an inaccuracy, mistake or blunder the card under the board shows the move
  Stockfish preferred, a short reason, and how much winning chance the move cost,
  in percent. The reason comes from Stockfish's choice plus plain rules (a piece
  left to be taken, a checkmate missed or allowed, what the better move wins or
  attacks) and, if none applies, the change in the score. That reason needs no model call; the coach below is separate.
- Every move, the person's and the agent's, can be read without chess notation:
  hover, focus or tap a move to see "Knight moves to h3" or "Pawn on e takes the
  knight on d5" (castling, promotion, en passant, check and checkmate included),
  with the squares lit on the board. Grade labels and piece names are explained
  next to where they appear.
- The agent's graded moves get the same better-move note, written about the agent.
- The grade of a human move is not an agent result: it is not written to
  `moves.jsonl`, is not counted in a game's blunders and mistakes, and so never
  reaches `gambit analyze`.
- In traces it is a separate small trace named `chess.human_move` (one per human
  move), with `chess.player.type=human`, `gambit.run.mode=human`, and the grade in
  `chess.human.move.class`, `chess.human.eval.*`. It has no `gen_ai.*` attributes
  and no `chess.move.class`, so dashboards built on the agent's attributes ignore
  it; the tail sampler keeps it with the other human-game traces.

### The coach

For people who have never played chess, a coach (a second model call) talks
in plain words, in addition to the grade and the rule-based reason above.

- After each of the person's moves, good or bad, the coach adds a short comment
  once Stockfish has graded it. **Ask the coach why** on any move, the agent's
  included, explains that move on request (once per move).
- It uses the game's model (the one the model switch picked) through the same
  client, so the mock model gives a canned, clearly mock coach.
- It is grounded, not trusted: the prompt holds only what Stockfish and the
  rules already found (grade, the better move, winning chance lost, the reason),
  with every move written in words. A reply that contains chess notation
  (`Nf3`, `exd5`, `O-O`) is thrown away.
- It is off the critical path. A coach error, timeout (25s) or unreadable
  answer only means no comment; the board and the game never wait for it.
- Its tokens do not count toward ending a game: `--game-token-budget` is checked
  against the agent's tokens only. The coach has its own allowance of half the
  budget (worst case reserved before each call), and when that is spent it just
  stops commenting. `--no-coach` turns it off.
- In traces it is its own agent in the game's `gen_ai.conversation.id`: a
  separate `chess.coach` trace per call holding `invoke_agent coach`
  (`gen_ai.agent.name=coach`, `gambit.coach.kind` = `comment` or `why`,
  `gambit.coach.subject` = `human` or `agent`) and its `chat {model}` span. The
  player's spans keep `chess-agent`. Prompt and answer are captured on the chat
  span under the same `OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT`
  switch. Coach calls are not in the dataset, so `gambit analyze` never sees them.
  The dashboards' decision-latency panels select `invoke_agent chess-agent`.

### Trace links and the Collector

The Collector's tail sampler holds a trace for `decision_wait` (15s) before
Jaeger gets it, so a trace is not there the instant the move lands. The link
therefore appears `--trace-delay` (20s) after the grade, with a countdown. Three
things keep it honest:

- A `keep-human-games` policy keeps every human trace, instead of the 10% baseline.
- The sampler decides `decision_wait` after a trace's first span arrives, so a move
  whose spans straggle in later than that (a long chain of retries, a raised
  `--move-timeout`) can reach Jaeger incomplete or not at all. The default 15s cap
  fits; if you raise it, raise `decision_wait` in `collector/otel-collector.yaml` too.
- If `--trace-url` points at a backend without tail sampling (Grafana Cloud or Tempo),
  pass `--trace-delay 0`.

### API

The page is a thin client over a small API, so a rehearsal can also be scripted:

| | |
|---|---|
| `POST /api/live/new` `{"human_color":"white","script":["e4"],"model":"luna-6"}` | start a game (abandons a running one, or 409 with `--exclusive`; 429 with `Retry-After` over the game limits; `model` only with `--models`); returns `{"id"}` |
| `POST /api/live/move` `{"id","move"}` | the human's move, UCI (`e2e4`, `e7e8q`) or SAN; 422 if illegal, 409 if it is not their turn |
| `POST /api/live/resign` `{"id"}` | |
| `POST /api/live/coach` `{"id","ply"}` | "Ask the coach why" on any graded move; the answer is a `coach_comment` event (404 coach off or no such move, 409 not graded yet or coach budget used, 429 busy) |
| `GET /api/live/events?id=` | server-sent events: `game_started`, `human_turn`, `human_moved`, `agent_thinking`, `agent_progress`, `agent_moved`, `human_graded`, `agent_graded`, `coach_thinking`, `coach_comment`, `coach_unavailable`, `game_over`, `game_saved`, `notice`, `error` |
| `GET /api/live/state?id=` | the whole event log as JSON |

POSTs must be same-origin JSON: a page on another origin cannot start games on
the stage laptop. Serve on `localhost` (the default), not on a shared network.

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
- **No outcome on the span.** The label arrives after `invoke_agent` has
  ended. `gen_ai.evaluation.result` fits Stockfish's grade exactly, but it is an
  event: we emit it with the ended `invoke_agent` span's IDs, and the span
  itself never learns the outcome. So a root span still carries the label, and
  spanmetrics cannot slice *decision* latency by move class; you have to join
  in the backend.
- **Reasoning tokens** now have a key, `gen_ai.usage.reasoning.output_tokens`
  (it replaced our `gambit.usage.reasoning_tokens`), but only on the chat span:
  the `invoke_agent` span has no reasoning-token total.
- **The conventions moved repositories.** They now live in
  [semantic-conventions-genai](https://github.com/open-telemetry/semantic-conventions-genai);
  the gen-ai pages in the main repo are stubs that point there. On the way,
  `gen_ai.client.token.usage` was replaced by `gen_ai.client.inference.usage.*`
  counters, and `gen_ai.client.operation.duration` became "do not use for
  inference": model calls report `gen_ai.client.inference.duration` instead.
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
cmd/gambit/              CLI: play, analyze, serve (replay + human mode), extract-traces
internal/agent/          the LLM player: prompts, tools, retries, validation spans
internal/llm/            OpenAI-compatible client, gen_ai instrumentation, mock model
internal/engine/         minimal UCI client for Stockfish
internal/game/           game runner, trace shape, grading and classification, opponents, human-mode sessions
internal/analysis/       features, logistic regression, AUC + bootstrap, report
internal/web/            stage UI (embedded pages: replay and human mode) and the live API
internal/semconv/        every attribute key in one place
internal/otlpjson/       cut one game's traces out of a recording
collector/               Collector pipeline + replay config
deploy/                  Prometheus, Grafana (datasources, dashboard), ClickHouse init
deploy/gcp/              the cloud play deployment: compose override, Caddy, passcodes, VM scripts, runbook
sql/correlation.sql      the label ⨝ trace join in pure SQL
```

## Telemetry configuration

Standard OTel environment variables apply: `OTEL_EXPORTER_OTLP_ENDPOINT`
(default `http://localhost:4317`), `OTEL_SERVICE_NAME` (default `chess-agent`),
`OTEL_RESOURCE_ATTRIBUTES`, `OTEL_SDK_DISABLED`, and
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT` for prompt/completion capture. It
takes the values the GenAI conventions define: `NO_CONTENT` (the default),
`SPAN_ONLY`, `EVENT_ONLY` or `SPAN_AND_EVENT`. The Makefile and the Compose
files default to `SPAN_AND_EVENT`. The older
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` is still read when the new
variable is not set: `true` means `SPAN_AND_EVENT`, and `false` still turns
capture off under `make` and Compose. If both are set, the new one wins.

Traces, metrics and logs all go to the same OTLP endpoint. To send traces only
(for a Collector without metrics or logs pipelines), set
`OTEL_METRICS_EXPORTER=none` and/or `OTEL_LOGS_EXPORTER=none`.

Content capture covers every opt-in key: `gen_ai.system_instructions`,
`gen_ai.input.messages`, `gen_ai.output.messages` (including `reasoning` parts
when the server returns visible reasoning), `gen_ai.tool.definitions`,
`gen_ai.tool.call.arguments` and `gen_ai.tool.call.result`, plus the
`gen_ai.client.inference.operation.details` event. With it off, none of them
are recorded.
