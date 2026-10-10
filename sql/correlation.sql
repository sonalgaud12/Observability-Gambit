-- The reusable pattern from the talk: attach a ground-truth label to the
-- trace, then join it back to the trace's own telemetry by TraceId.
--
-- Works against the ClickHouse exporter's default schema (otel.otel_traces).
-- Run with:  make sql          (or paste into Grafana Explore / clickhouse client)

-- 1. One row per agent move: label + trace features.
CREATE OR REPLACE VIEW otel.gambit_moves AS
WITH
  moves AS (
    SELECT
      TraceId,
      Timestamp,
      SpanAttributes['chess.game.id']                          AS game_id,
      toUInt32OrZero(SpanAttributes['chess.move.number'])      AS move_number,
      SpanAttributes['gen_ai.request.model']                   AS model,
      toUInt32OrZero(SpanAttributes['chess.legal_move_count']) AS legal_moves,
      toInt32OrZero(SpanAttributes['chess.material_balance'])  AS material_balance,
      toInt32OrZero(SpanAttributes['chess.eval.before_cp'])    AS eval_before_cp,
      SpanAttributes['chess.move.san']                         AS played,
      SpanAttributes['chess.eval.best_move']                   AS best,
      toFloat64OrZero(SpanAttributes['chess.eval.win_chance_drop']) AS win_chance_drop,
      SpanAttributes['chess.move.class']                       AS class
    FROM otel.otel_traces
    WHERE SpanName = 'chess.move'
  ),
  decisions AS (
    SELECT TraceId,
           Duration / 1e6 AS latency_ms,
           toUInt32OrZero(SpanAttributes['chess.illegal_attempts']) AS illegal_attempts,
           SpanAttributes['chess.fallback'] = 'true' AS fallback
    FROM otel.otel_traces
    WHERE SpanName LIKE 'invoke_agent %'
  ),
  chats AS (
    SELECT TraceId,
           count()                                                         AS chat_calls,
           sum(toUInt64OrZero(SpanAttributes['gen_ai.usage.input_tokens']))  AS input_tokens,
           sum(toUInt64OrZero(SpanAttributes['gen_ai.usage.output_tokens'])) AS output_tokens,
           sum(toUInt64OrZero(SpanAttributes['gambit.usage.reasoning_tokens'])) AS reasoning_tokens,
           countIf(position(SpanAttributes['gen_ai.response.finish_reasons'], 'length') > 0) AS truncated_calls,
           max(Duration) / 1e6                                             AS slowest_call_ms
    FROM otel.otel_traces
    WHERE SpanName LIKE 'chat %'
    GROUP BY TraceId
  ),
  tools AS (
    SELECT TraceId, count() AS tool_calls
    FROM otel.otel_traces
    WHERE SpanName LIKE 'execute_tool %'
    GROUP BY TraceId
  )
SELECT *
FROM moves
LEFT JOIN decisions USING (TraceId)
LEFT JOIN chats     USING (TraceId)
LEFT JOIN tools     USING (TraceId);

-- 2. Blunder rate by any feature, here illegal attempts. Contested positions
--    only: in a decided game no move can register as a blunder.
SELECT illegal_attempts,
       count()                              AS moves,
       round(avg(class = 'blunder') * 100, 1) AS blunder_pct
FROM otel.gambit_moves
WHERE abs(eval_before_cp) <= 400
GROUP BY illegal_attempts
ORDER BY illegal_attempts;

-- 3. The "before move 23" question in SQL: do the previous three decisions'
--    latency and retries go up before a blunder?
SELECT class,
       count() AS moves,
       round(avg(prev3_latency_ms)) AS avg_prev3_latency_ms,
       round(avg(prev3_illegal), 3) AS avg_prev3_illegal
FROM (
  SELECT class, eval_before_cp,
         avg(latency_ms)       OVER w AS prev3_latency_ms,
         avg(illegal_attempts) OVER w AS prev3_illegal
  FROM otel.gambit_moves
  WINDOW w AS (PARTITION BY game_id ORDER BY move_number ROWS BETWEEN 3 PRECEDING AND 1 PRECEDING)
)
WHERE abs(eval_before_cp) <= 400
  AND isFinite(prev3_latency_ms) -- a game's first move has no history
GROUP BY class
ORDER BY moves DESC;
