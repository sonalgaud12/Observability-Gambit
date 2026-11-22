// Package semconv collects every attribute key the demo emits in one place.
//
// The gen_ai.* keys follow the OpenTelemetry GenAI semantic conventions
// (development status). They are written as plain strings on purpose: the
// conventions are still moving, and keeping them here makes it a one-file
// change when a key is renamed (gen_ai.system -> gen_ai.provider.name already
// happened once). Keys under chess.* and gambit.* are this project's own:
// they are exactly the places where the GenAI conventions have no answer,
// which is one of the talk's findings.
package semconv

import "go.opentelemetry.io/otel/attribute"

// --- OpenTelemetry GenAI semantic conventions -------------------------------

const (
	GenAIOperationName         = attribute.Key("gen_ai.operation.name") // chat | invoke_agent | execute_tool
	GenAIProviderName          = attribute.Key("gen_ai.provider.name")
	GenAIRequestModel          = attribute.Key("gen_ai.request.model")
	GenAIRequestTemperature    = attribute.Key("gen_ai.request.temperature")
	GenAIRequestMaxTokens      = attribute.Key("gen_ai.request.max_tokens")
	GenAIResponseID            = attribute.Key("gen_ai.response.id")
	GenAIResponseModel         = attribute.Key("gen_ai.response.model")
	GenAIResponseFinishReasons = attribute.Key("gen_ai.response.finish_reasons")
	GenAIUsageInputTokens      = attribute.Key("gen_ai.usage.input_tokens")
	GenAIUsageOutputTokens     = attribute.Key("gen_ai.usage.output_tokens")
	GenAIAgentName             = attribute.Key("gen_ai.agent.name")
	GenAIAgentID               = attribute.Key("gen_ai.agent.id")
	GenAIConversationID        = attribute.Key("gen_ai.conversation.id")
	GenAIToolName              = attribute.Key("gen_ai.tool.name")
	GenAIToolCallID            = attribute.Key("gen_ai.tool.call.id")
	GenAIToolType              = attribute.Key("gen_ai.tool.type")
	GenAIInputMessages         = attribute.Key("gen_ai.input.messages")  // opt-in content capture
	GenAIOutputMessages        = attribute.Key("gen_ai.output.messages") // opt-in content capture
	ErrorType                  = attribute.Key("error.type")
)

// Added to follow github.com/open-telemetry/semantic-conventions-genai, where
// the GenAI conventions moved in 2026 (the gen-ai pages in the main
// semantic-conventions repo now only point there). Section names refer to
// that repo's model/gen-ai/*.yaml. All are "development" stability.
const (
	// spans.yaml, gen_ai.client.inference (the "chat {model}" span).
	GenAIRequestTopP                = attribute.Key("gen_ai.request.top_p")
	GenAIRequestSeed                = attribute.Key("gen_ai.request.seed")
	GenAIRequestStopSequences       = attribute.Key("gen_ai.request.stop_sequences")
	GenAIRequestStream              = attribute.Key("gen_ai.request.stream") // only set when true
	GenAIRequestReasoningLevel      = attribute.Key("gen_ai.request.reasoning.level")
	GenAIOutputType                 = attribute.Key("gen_ai.output.type")
	GenAIResponseTimeToFirstChunk   = attribute.Key("gen_ai.response.time_to_first_chunk") // streaming only
	GenAIUsageReasoningOutputTokens = attribute.Key("gen_ai.usage.reasoning.output_tokens")
	GenAIUsageCacheReadInputTokens  = attribute.Key("gen_ai.usage.cache_read.input_tokens")
	GenAIUsageCacheWriteInputTokens = attribute.Key("gen_ai.usage.cache_write.input_tokens")
	GenAIPromptName                 = attribute.Key("gen_ai.prompt.name")
	GenAIPromptVersion              = attribute.Key("gen_ai.prompt.version")
	GenAISystemInstructions         = attribute.Key("gen_ai.system_instructions") // opt-in content capture
	GenAIToolDefinitions            = attribute.Key("gen_ai.tool.definitions")    // opt-in content capture
	ServerAddress                   = attribute.Key("server.address")
	ServerPort                      = attribute.Key("server.port")

	// spans.yaml, gen_ai.invoke_agent.internal.
	GenAIAgentDescription = attribute.Key("gen_ai.agent.description")

	// spans.yaml, gen_ai.execute_tool.
	GenAIToolDescription   = attribute.Key("gen_ai.tool.description")
	GenAIToolCallArguments = attribute.Key("gen_ai.tool.call.arguments") // opt-in content capture
	GenAIToolCallResult    = attribute.Key("gen_ai.tool.call.result")    // opt-in content capture

	// token-metrics.yaml: required on the gen_ai.client.inference.usage.* counters.
	GenAITokenModality = attribute.Key("gen_ai.token.modality")

	// events.yaml, gen_ai.evaluation.result.
	GenAIEvaluationName        = attribute.Key("gen_ai.evaluation.name")
	GenAIEvaluationScoreValue  = attribute.Key("gen_ai.evaluation.score.value")
	GenAIEvaluationScoreLabel  = attribute.Key("gen_ai.evaluation.score.label")
	GenAIEvaluationExplanation = attribute.Key("gen_ai.evaluation.explanation")

	// Exception events (general exception.* conventions).
	ExceptionType    = attribute.Key("exception.type")
	ExceptionMessage = attribute.Key("exception.message")
)

// Operation names.
const (
	OpChat        = "chat"
	OpInvokeAgent = "invoke_agent"
	OpExecuteTool = "execute_tool"
)

// Metric names (metrics.yaml and token-metrics.yaml).
//
// Note gen_ai.client.operation.duration is NOT used: the spec now says it
// SHOULD NOT be reported for inference, tool or agent operations, which have
// their own duration metrics below.
const (
	MetricInferenceDuration         = "gen_ai.client.inference.duration"
	MetricInferenceUsageInput       = "gen_ai.client.inference.usage.input_tokens"
	MetricInferenceUsageOutput      = "gen_ai.client.inference.usage.output_tokens"
	MetricInferenceUsageCacheRead   = "gen_ai.client.inference.usage.cache_read.input_tokens"
	MetricInferenceUsageCacheWrite  = "gen_ai.client.inference.usage.cache_write.input_tokens"
	MetricInferenceUsageReasoning   = "gen_ai.client.inference.usage.reasoning.output_tokens"
	MetricInferenceOperationInput   = "gen_ai.client.inference.operation.input_tokens"
	MetricInferenceOperationOutput  = "gen_ai.client.inference.operation.output_tokens"
	MetricInvokeAgentDuration       = "gen_ai.invoke_agent.duration"
	MetricInvokeAgentInferenceCalls = "gen_ai.invoke_agent.inference_calls"
	MetricInvokeAgentToolCalls      = "gen_ai.invoke_agent.tool_calls"
	MetricExecuteToolDuration       = "gen_ai.execute_tool.duration"
)

// Event names (events.yaml). Events are log records with an event name.
const (
	EventInferenceDetails   = "gen_ai.client.inference.operation.details"
	EventEvaluationResult   = "gen_ai.evaluation.result"
	EventInferenceException = "gen_ai.client.inference.exception"
	// The agent runs in-process, so its span is gen_ai.invoke_agent.internal
	// and its exception event is gen_ai.invoke_agent.exception, not
	// gen_ai.client.invoke_agent.exception (that one is for remote agents).
	EventInvokeAgentException = "gen_ai.invoke_agent.exception"
	EventExecuteToolException = "gen_ai.execute_tool.exception"
)

// --- Gaps: things gen_ai.* has no key for ---------------------------------

const (
	// Number of model calls and tool calls in one decision. The spec has these
	// as histograms (gen_ai.invoke_agent.inference_calls / tool_calls) but no
	// span attribute, so they stay on the invoke_agent span under our names.
	GambitChatCalls = attribute.Key("gambit.chat_calls")
	GambitToolCalls = attribute.Key("gambit.tool_calls")
	// The agent's release. gen_ai.agent.version was removed from the
	// invoke_agent span in v0.1.0, so it lives under our own key.
	GambitAgentVersion = attribute.Key("gambit.agent.version")
	// Retry bookkeeping for an agent that must produce a *valid* action.
	GambitRetryAttempt = attribute.Key("gambit.retry.attempt")
	// Marks traces produced by the offline mock model, so they can never be
	// confused with real-model results in a backend.
	GambitMock = attribute.Key("gambit.llm.mock")
	// How the run was played. Absent for the usual agent-vs-Stockfish games;
	// "human" when a person played the other side from the stage UI, so
	// queries and dashboards can include or exclude those games.
	GambitRunMode = attribute.Key("gambit.run.mode")
	// Set when the per-move deadline cut the agent off and a random legal
	// move was played in its place (live games only).
	GambitTimedOut = attribute.Key("gambit.timed_out")
	// Who played a human-mode game: a short label the front proxy attached
	// to the request (one per passcode), for attributing plays. Never the
	// passcode itself.
	GambitPlayerLabel = attribute.Key("gambit.player.label")
	// What a human-mode coach call was for: "comment" (the automatic remark
	// after the human's move) or "why" (the player pressed "Ask the coach why").
	GambitCoachKind = attribute.Key("gambit.coach.kind")
	// Whose move the coach was talking about: "human" or "agent".
	GambitCoachSubject = attribute.Key("gambit.coach.subject")
)

// --- Domain: the chess game -------------------------------------------------

const (
	ChessGameID          = attribute.Key("chess.game.id")
	ChessPly             = attribute.Key("chess.ply")
	ChessMoveNumber      = attribute.Key("chess.move.number")
	ChessColor           = attribute.Key("chess.color")
	ChessFEN             = attribute.Key("chess.fen")
	ChessLegalMoveCount  = attribute.Key("chess.legal_move_count")
	ChessInCheck         = attribute.Key("chess.in_check")
	ChessMaterialBalance = attribute.Key("chess.material_balance")
	ChessMoveSAN         = attribute.Key("chess.move.san")
	ChessMoveUCI         = attribute.Key("chess.move.uci")
	ChessCandidate       = attribute.Key("chess.candidate")
	ChessLegal           = attribute.Key("chess.legal")
	ChessIllegalAttempts = attribute.Key("chess.illegal_attempts")
	ChessFallback        = attribute.Key("chess.fallback")
	ChessOpponentElo     = attribute.Key("chess.opponent.elo")
	ChessOpponentType    = attribute.Key("chess.opponent.type") // "human" in human games; absent otherwise
	// Set on the span of a move the human played in a human game ("human").
	// Those spans carry the chess.human.* keys below, never chess.move.class,
	// so the human's grades cannot be mixed into the agent's.
	ChessPlayerType = attribute.Key("chess.player.type")
)

// --- The human's graded moves (human mode only) -----------------------------
//
// Deliberately not the agent's chess.move.class / chess.eval.* keys: dashboards
// and the tail sampler read those as the agent's results.

const (
	ChessHumanMoveClass     = attribute.Key("chess.human.move.class")
	ChessHumanCPLoss        = attribute.Key("chess.human.eval.cp_loss")
	ChessHumanWinChanceDrop = attribute.Key("chess.human.eval.win_chance_drop")
	ChessHumanBestMove      = attribute.Key("chess.human.eval.best_move")
)

// --- Ground truth: the label. Never use these as predictive features. ------

const (
	ChessEvalBeforeCP  = attribute.Key("chess.eval.before_cp")
	ChessEvalAfterCP   = attribute.Key("chess.eval.after_cp")
	ChessCPLoss        = attribute.Key("chess.eval.cp_loss")
	ChessWinChanceDrop = attribute.Key("chess.eval.win_chance_drop")
	ChessMoveClass     = attribute.Key("chess.move.class") // best | good | inaccuracy | mistake | blunder
	ChessBestMove      = attribute.Key("chess.eval.best_move")
	ChessEngineDepth   = attribute.Key("chess.eval.depth")
)
