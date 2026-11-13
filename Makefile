# The Observability Gambit
#
#   make up          start the backend stack (Collector, Jaeger, ClickHouse, Prometheus, Grafana)
#   make up-cloud    same, plus an optional Grafana Cloud route (README: Send to Grafana Cloud)
#   make rehearse    50 mock games, no API key (synthetic correlations: rehearsal only)
#   make play        real games against an OpenAI-compatible endpoint (see README)
#   make analyze     correlation report + model -> out/
#   make serve       stage UI on http://localhost:8080
#   make live        same UI, plus human mode: play the agent (README: Human mode)
#   make live-mock   human mode with the offline mock model, no API key
#   make replay GAME=<id>   re-send one recorded game's traces into the stack

GAMES      ?= 50
PARALLEL   ?= 4
MODEL      ?= gpt-4o-mini
BASE_URL   ?= https://api.openai.com/v1
PROVIDER   ?= openai
STOCKFISH  ?= stockfish
GAME       ?=
LIVE_ARGS  ?=

export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT ?= true
export STOCKFISH_PATH ?= $(STOCKFISH)

.PHONY: build test up up-cloud down logs rehearse play analyze serve live live-mock replay sql clean

build:
	go mod tidy
	go build -o bin/gambit ./cmd/gambit

test:
	go test ./...

up:
	mkdir -p data/traces data/replay out
	docker compose up -d
	@echo "Grafana    http://localhost:3000"
	@echo "Jaeger     http://localhost:16686"
	@echo "Prometheus http://localhost:9090"

# Same stack plus the Grafana Cloud route. Needs .env.cloud (see .env.cloud.example).
up-cloud:
	@test -f .env.cloud || { echo "missing .env.cloud: copy .env.cloud.example and fill it in"; exit 1; }
	mkdir -p data/traces data/replay out
	docker compose --env-file .env.cloud -f docker-compose.yaml -f docker-compose.cloud.yaml up -d

down:
	docker compose down

logs:
	docker compose logs -f otel-collector

rehearse: build
	./bin/gambit play --mock --games $(GAMES) --parallel $(PARALLEL)

play: build
	./bin/gambit play --games $(GAMES) --parallel $(PARALLEL) \
		--model $(MODEL) --base-url $(BASE_URL) --provider $(PROVIDER)

analyze: build
	./bin/gambit analyze --data data --out out

serve: build
	./bin/gambit serve --data data --model out/model.json

# Human mode: you play the agent from http://localhost:8080/live.html.
# Extra flags go in LIVE_ARGS, e.g. LIVE_ARGS='--move-timeout 10s --script "e4 Nf3 Bc4"'.
live: build
	./bin/gambit serve --live --agent-model $(MODEL) --base-url $(BASE_URL) --provider $(PROVIDER) $(LIVE_ARGS)

live-mock: build
	./bin/gambit serve --live --mock $(LIVE_ARGS)

# Replays a whole recording, or a single game with GAME=<id>.
replay: build
	mkdir -p data/replay
	rm -f data/replay/*.jsonl
	docker compose --profile replay rm -sf replay
	@if [ -n "$(GAME)" ]; then \
		./bin/gambit extract-traces --game $(GAME); \
	else \
		cp data/traces/traces.jsonl data/replay/all.jsonl; \
	fi
	docker compose --profile replay up -d replay
	@echo "Replaying into the Collector; traces appear in Jaeger after the tail-sampling wait (~15s)."

sql:
	docker compose exec -T clickhouse clickhouse-client --multiquery --format PrettyCompact < sql/correlation.sql

clean:
	rm -rf bin out
