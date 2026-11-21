#!/usr/bin/env bash
# Runs the whole production stack on this machine with the mock model (no API keys,
# no spend, nothing leaves the laptop) and checks it end to end with e2e.py:
# two passcodes, HTTPS with Caddy's local certificate, the one-game lock, SSE
# through the proxy, both model options, passcode labels on traces, trace links,
# the idle timeout. Needs Docker, python3 and openssl. Takes about 3 minutes.
#
#   deploy/gcp/local-test.sh          # leaves nothing running when it finishes
#   KEEP=1 deploy/gcp/local-test.sh   # keep the stack up afterwards
#
# Everything it writes (passcodes, the env file) goes to a temporary directory.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
tmp="$(mktemp -d)"
project=ogprodtest
port="${E2E_PORT:-28443}"

cat >"$tmp/test.env" <<ENV
GAMBIT_DOMAIN=gambit.localhost
CADDY_HTTP_PORT=$((port - 363))
CADDY_HTTPS_PORT=$port
GAMBIT_AUTH_DIR=$tmp/auth
GAMBIT_EXTRA_ARGS=--mock
GRAFANA_CLOUD_OTLP_ENDPOINT=http://host.docker.internal:1/otlp
GRAFANA_CLOUD_INSTANCE_ID=1
GRAFANA_CLOUD_OTLP_TOKEN=local-test-not-a-token
GAMBIT_MAX_GAMES_PER_HOUR=50
GAMBIT_IDLE_TIMEOUT=20s
ENV
# Other things may already use 3000, 9090 or 8080 on a laptop: move the loopback ports.
cat >"$tmp/ports.yaml" <<'YAML'
services:
  otel-collector: { ports: !override ["127.0.0.1:24317:4317", "127.0.0.1:24318:4318", "127.0.0.1:28889:8889", "127.0.0.1:23133:13133"] }
  jaeger:         { ports: !override ["127.0.0.1:26686:16686"] }
  clickhouse:     { ports: !override ["127.0.0.1:28123:8123", "127.0.0.1:29000:9000"] }
  prometheus:     { ports: !override ["127.0.0.1:29090:9090"] }
  grafana:        { ports: !override ["127.0.0.1:23000:3000"] }
  gambit:         { ports: !override ["127.0.0.1:28081:8080"] }
YAML
dc() {
  docker compose -p "$project" --env-file "$tmp/test.env" \
    -f docker-compose.yaml -f docker-compose.cloud.yaml -f deploy/gcp/docker-compose.prod.yaml -f "$tmp/ports.yaml" "$@"
}
cleanup() {
  if [ "${KEEP:-0}" != 1 ]; then dc down -v --remove-orphans >/dev/null 2>&1 || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT

mkdir -p data/traces data/replay out
openssl rand -hex 12 >"$tmp/pass.sonal"
openssl rand -hex 12 >"$tmp/pass.nic"
printf 'sonal:%s\nnic:%s\n' "$(cat "$tmp/pass.sonal")" "$(cat "$tmp/pass.nic")" | deploy/gcp/render-auth.sh "$tmp/auth"

dc up -d --build
echo "waiting for the stack..."
for _ in $(seq 1 60); do
  code="$(curl -sk --resolve "gambit.localhost:$port:127.0.0.1" -o /dev/null -w '%{http_code}' "https://gambit.localhost:$port/healthz" || true)"
  [ "$code" = 200 ] && break
  sleep 2
done
E2E_PORT="$port" python3 deploy/gcp/e2e.py "$tmp"
