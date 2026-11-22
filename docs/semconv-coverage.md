# GenAI semantic conventions: what this demo emits

Checked against [open-telemetry/semantic-conventions-genai](https://github.com/open-telemetry/semantic-conventions-genai)
as of 21 Nov 2026. The GenAI conventions moved there from the main
semantic-conventions repo, whose `docs/gen-ai/` pages now only point to the new
repo. Everything below is **development** stability.

All keys are defined once, in [`internal/semconv/attrs.go`](../internal/semconv/attrs.go).
"Opt-in" means it is only recorded when
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT` asks for it: `NO_CONTENT` (the
default), `SPAN_ONLY`, `EVENT_ONLY` or `SPAN_AND_EVENT`. Opt-in span attributes
need `SPAN_ONLY` or `SPAN_AND_EVENT`; the opt-in content on the details event
needs `EVENT_ONLY` or `SPAN_AND_EVENT`. The older
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true` is still read when the
new variable is unset, and means `SPAN_AND_EVENT`. An unknown value means
`NO_CONTENT`.

## Spans

### `chat {model}`: the convention's `gen_ai.client.inference` span (CLIENT)

| Key | When | Notes |
|---|---|---|
| `gen_ai.operation.name` | always | `chat` |
| `gen_ai.conversation.id` | when the caller has one | the game id, carried in the context (`telemetry.WithConversationID`); also on the tool spans and every GenAI event, never on metrics |
| `gen_ai.provider.name` | always | `openai`, `anthropic`, `ollama`, `mock`, ... |
| `gen_ai.request.model` | always | |
| `server.address`, `server.port` | remote clients | from the client's base URL; port defaults to 443/80 (`api.anthropic.com:443` for the Anthropic client) |
| `gen_ai.request.temperature` | when sent | |
| `gen_ai.request.max_tokens` | when sent | |
| `gen_ai.request.top_p` | when sent | plumbed through `llm.Request`; the agent does not set it today |
| `gen_ai.request.seed` | when sent | plumbed through; not set today |
| `gen_ai.request.stop_sequences` | when sent | plumbed through; not set today |
| `gen_ai.request.reasoning.level` | when sent | the request's level, or the client's default (set by the model presets in `cmd/gambit/models.go`, e.g. `none` for OpenAI GPT-6, or by `--effort`: for Anthropic it is `output_config.effort`) |
| `gen_ai.output.type` | when the request asks for a format | `json` sends `response_format: json_object`; plain text requests leave it unset |
| `gen_ai.prompt.name`, `gen_ai.prompt.version` | always (agent and coach) | `chess-move` / `chess-coach`; the version is a hash of the prompt, see below |
| `gen_ai.response.id`, `gen_ai.response.model`, `gen_ai.response.finish_reasons` | on success | the provider's own value: OpenAI `stop`/`length`/`tool_calls`; Anthropic `end_turn`/`tool_use`/`max_tokens`/`stop_sequence`/`refusal` |
| `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` | on success | Anthropic's `input_tokens` leaves out cached tokens, so the client reports input + cache read + cache write, keeping the cache counts subsets of the input count |
| `gen_ai.usage.reasoning.output_tokens` | when reported and > 0 | replaces `gambit.usage.reasoning_tokens`. OpenAI `completion_tokens_details.reasoning_tokens`; Anthropic `usage.output_tokens_details.thinking_tokens` (the raw reasoning, not the summary's length; absent from the response means unset here, never estimated) |
| `gen_ai.usage.cache_read.input_tokens` | when reported and > 0 | OpenAI `prompt_tokens_details.cached_tokens`; Anthropic `cache_read_input_tokens` |
| `gen_ai.usage.cache_write.input_tokens` | when reported and > 0 | OpenAI does not report it; OpenRouter's `cache_write_tokens` and Anthropic's `cache_creation_input_tokens` do |
| `error.type` | on failure | HTTP status, `timeout`, or `_OTHER` |
| `gen_ai.system_instructions` | opt-in | the leading system message(s), as `[{"type":"text","content":...}]` (see "Judgment calls") |
| `gen_ai.input.messages` | opt-in | the rest of the chat, in the parts format |
| `gen_ai.output.messages` | opt-in | includes a `reasoning` part when the server returns visible reasoning (`reasoning_content` / `reasoning` on Chat Completions; the summarized `thinking` blocks of the Anthropic Messages API). Messages carry no `finish_reason` (deprecated in v0.1.0; it is in `gen_ai.response.finish_reasons`) |
| `gen_ai.tool.definitions` | opt-in | `[{"type":"function","name":...,"description":...,"parameters":...}]` |

Not emitted, on purpose:

- `gen_ai.request.stream` and `gen_ai.response.time_to_first_chunk`: the client
  never streams, and the conventions say to leave `stream` unset for
  non-streaming requests.

### `invoke_agent chess-agent` / `invoke_agent coach`: `gen_ai.invoke_agent.internal` (INTERNAL)

| Key | Notes |
|---|---|
| `gen_ai.operation.name`, `gen_ai.agent.name`, `gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.conversation.id` | as before |
| `gambit.agent.version` | the demo's release (`telemetry.Version`). `gen_ai.agent.version` was removed from this span in v0.1.0, so it lives under our own key (the release is also `service.version` on the resource) |
| `gen_ai.agent.description` | |
| `gen_ai.request.temperature`, `gen_ai.request.max_tokens` | the agent's configured settings |
| `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` | totals across every model call in the decision |
| `gen_ai.response.finish_reasons` | of the call that produced the move (not set on fallback moves) |
| `error.type` | `timeout`, `fallback_move`, `_OTHER` |

### `execute_tool {name}`: `gen_ai.execute_tool` (INTERNAL)

| Key | Notes |
|---|---|
| `gen_ai.operation.name`, `gen_ai.tool.name`, `gen_ai.tool.type` | `execute_tool`, `legal_moves`/`threats`, `function` |
| `gen_ai.tool.call.id` | only when the model supplied one |
| `gen_ai.tool.description` | the description the model was given |
| `gen_ai.agent.name` | |
| `gen_ai.conversation.id` | the game id, when the caller has one |
| `gen_ai.tool.call.arguments`, `gen_ai.tool.call.result` | opt-in |
| `error.type` | `unknown_tool` |

## Metrics

All carry only the low-cardinality attributes the conventions allow: no
conversation id, no content.

| Metric | Instrument | Buckets | Attributes |
|---|---|---|---|
| `gen_ai.client.inference.duration` | histogram, s | 0.01 … 81.92 | operation, provider, request/response model, server address/port, `error.type` |
| `gen_ai.client.inference.operation.input_tokens` / `.output_tokens` | histogram, {token} | 1 … 67108864 | as above; failed calls record 0 |
| `gen_ai.client.inference.usage.input_tokens` / `.output_tokens` | counter, {token} | | + `gen_ai.token.modality=text`; only positive values |
| `gen_ai.client.inference.usage.cache_read.input_tokens` / `.cache_write.input_tokens` | counter, {token} | | + `gen_ai.token.modality=text` |
| `gen_ai.client.inference.usage.reasoning.output_tokens` | counter, {token} | | no modality (the spec has none on this one) |
| `gen_ai.invoke_agent.duration` | histogram, s | 0.1 … 86400 | operation, agent name, request model, `error.type` |
| `gen_ai.invoke_agent.inference_calls` / `.tool_calls` | histogram | 1 … 128 | operation, agent name. An agent that cannot call tools (the coach, or the chess agent with `-tools=false`) reports no `tool_calls` |
| `gen_ai.execute_tool.duration` | histogram, s | 0.01 … 81.92 | operation, tool name/type, agent name, `error.type` |

Not emitted, on purpose:

- `gen_ai.client.operation.duration`: the spec now says it SHOULD NOT be
  reported for inference, tool or agent operations, which have their own
  duration metrics above.
- `gen_ai.client.token.usage`: replaced in the spec by the
  `gen_ai.client.inference.usage.*` counters and
  `gen_ai.client.inference.operation.*` histograms.

## Events (log records with an event name)

| Event | Severity | Correlated with | Attributes |
|---|---|---|---|
| `gen_ai.evaluation.result` | INFO | the (ended) `invoke_agent` span | `gen_ai.evaluation.name=stockfish_move_quality`, `.score.value` = winning-chance drop (0 = best), `.score.label` = best/good/inaccuracy/mistake/blunder, `.explanation`, `gen_ai.response.id` (not on fallback moves), plus `chess.game.id`, `chess.ply`, `chess.fallback` |
| `gen_ai.client.inference.operation.details` | DEBUG | the chat span | request, response and content attributes, with `gen_ai.conversation.id`. Only when content goes to events (`EVENT_ONLY`, `SPAN_AND_EVENT`) |
| `gen_ai.client.inference.exception` | WARN | the chat span | `exception.type` (Go type), `exception.message` |
| `gen_ai.invoke_agent.exception` | WARN | the `invoke_agent` span | same |
| `gen_ai.execute_tool.exception` | WARN | the tool span | same |

`gen_ai.client.invoke_agent.exception` is for agents behind a remote service.
This agent runs in-process, so it uses `gen_ai.invoke_agent.exception`.

Every event carries `gen_ai.conversation.id` (the game id) when one is known.

## Structured values

v0.1.0 wants `gen_ai.input.messages`, `gen_ai.output.messages`,
`gen_ai.system_instructions`, `gen_ai.tool.definitions` and the tool call
arguments and result as structured values, not JSON strings.

- **On events: structured.** Log record attributes can hold maps and lists
  (`go.opentelemetry.io/otel/log` v0.11.0), so the details event carries the
  messages, system instructions and tool definitions as lists of maps.
- **On spans: still JSON strings, because the SDK cannot do otherwise.** The Go
  OpenTelemetry SDK in `go.mod` (v1.35.0) has span attribute types for bool,
  int64, float64, string and slices of those only: there is no map or nested
  list value. Until `go.opentelemetry.io/otel/attribute` gains one, span
  attributes keep the JSON text. The Collector copies on the Cloud path
  (`gambit.prompt.*`) are strings for the same reason.
- **Tool call arguments and result** exist only as span attributes (there is no
  tool event), so they stay strings as well.

## Judgment calls

- **Anthropic: system prompt and thinking blocks.** The Messages API takes the
  system prompt separately from the chat (`system`), so for Claude
  `gen_ai.system_instructions` is exactly what the spec describes. The model's
  reasoning is a *summary* (`thinking.display: "summarized"`; the raw chain of
  thought is never returned), recorded as a `reasoning` part. Its length is not
  `gen_ai.usage.reasoning.output_tokens`, which counts the raw thinking tokens
  the API bills. With `display: "omitted"` (the API's default on Claude 5 models,
  and not what this client asks for) the part would be empty and so is not
  emitted, but the token count still is. Thinking blocks and their signatures
  are kept verbatim on the message and sent back unchanged; they are not part of
  the telemetry's `gen_ai.input.messages` beyond the same `reasoning` part.
- **Anthropic: finish reasons are not mapped.** We record `end_turn`,
  `tool_use`, `max_tokens`, `stop_sequence` and `refusal` as the API sends
  them rather than translating them to OpenAI's `stop`/`tool_calls`/`length`:
  the attribute says what the provider said. The agent's `finish_length` feature
  treats both `length` and `max_tokens` as a truncated answer. A `refusal`
  (HTTP 200) is not an error, so there is no `error.type`; it shows as
  `finish_reasons=["refusal"]` and an unreadable move.
- **Anthropic: no new spans, no `gen_ai.request.seed`/`output.type`.** The call
  is the same `chat {model}` span through the same wrapper. The Messages API
  has no seed or JSON mode, so those request settings, which the agent does not
  set, are not sent and would not be recorded as sent.
- **`gen_ai.provider.name` is `anthropic`** for the native client and for the
  old OpenAI-compatible route alike, so dashboards keyed on it keep working;
  what changes is that the reasoning attributes are now filled.
- **System instructions on Chat Completions.** The spec uses
  `gen_ai.system_instructions` when the API takes instructions *separately*
  from the chat history; Chat Completions sends them as a `system` message
  *in* the history, which by the letter of the spec belongs in
  `gen_ai.input.messages`. We split the leading system messages out anyway,
  so the fixed template can be read (and compared by prompt version) apart
  from the moves. The spec doesn't say what to do when the system prompt is
  part of the history but is really a template.
- **Prompt version is a hash.** `gen_ai.prompt.version` is the first 12 hex
  digits of a SHA-256 of the system prompt, the prompt variant
  (legal moves in prompt, tools on/off) and the tool definitions. It changes
  whenever the prompt does, without anyone remembering to bump a number.
- **Fallback moves.** When the model never produced a legal move, a random
  move is played. Its evaluation is still emitted (it is still a graded move
  in the game), but without `gen_ai.response.id` and with `chess.fallback=true`,
  so model-quality views can leave it out.

## Gaps: what the conventions do not cover

| Need | What we use | Why |
|---|---|---|
| The outcome *on* the decision span | `chess.move.class` etc. on the root span; `gen_ai.evaluation.result` event | The grade exists only after `invoke_agent` ends. The event can carry the span's IDs, but nothing puts the outcome back onto the span, so spanmetrics and tail sampling still need our own keys on a span that is still open |
| Reasoning-token total for a decision | `gen_ai.usage.reasoning.output_tokens` on each chat span only | `invoke_agent` has `gen_ai.usage.input_tokens`/`output_tokens` but no reasoning total |
| Calls per decision as a span attribute | `gambit.chat_calls`, `gambit.tool_calls` | Only exist as histograms (`gen_ai.invoke_agent.inference_calls`/`tool_calls`) |
| "The model answered, but with an invalid action" | `chess.validate_move` spans with `error.type=illegal_move`, `gambit.retry.attempt` | No convention for validating agent output |
| Fallback / timeout of an agent decision | `chess.fallback`, `gambit.timed_out`, `error.type=fallback_move`/`timeout` | No convention |
| Run mode, player label, coach kind | `gambit.run.mode`, `gambit.player.label`, `gambit.coach.*` | Application-specific |
| Grafana Cloud keeping prompts | `gambit.prompt.*` copies (Collector) and the details event | Cloud strips `gen_ai.input.messages`/`output.messages` from stored spans by design |
