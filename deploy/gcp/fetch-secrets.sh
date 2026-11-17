#!/usr/bin/env bash
# Reads the deployment's secrets from Secret Manager into tmpfs (/run), never
# onto the disk. Runs on the VM as root, as gambit.service's ExecStartPre, and
# by hand after a passcode change.
#
#   fetch-secrets.sh          model keys + Grafana Cloud token + passcodes (boot)
#   fetch-secrets.sh auth     only the passcodes, then reload Caddy (no restart)
#
# Writes
#   /run/gambit/env             0600  OPENAI_API_KEY, ANTHROPIC_API_KEY, GRAFANA_CLOUD_OTLP_TOKEN
#   /run/gambit/caddy/users.caddy     passcode names + bcrypt hashes (render-auth.sh)
#
# Secret Manager names (see README): og-openai-key, og-anthropic-key,
# og-gcloud-otlp-token, and one og-passcode-<name> per passcode (label og-role=passcode).
# Authenticates as the VM's service account (metadata server); no key file.
set -euo pipefail

mode="${1:-all}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
run_dir="${GAMBIT_RUN_DIR:-/run/gambit}"
project="${PROJECT:-$(curl -fsS -H 'Metadata-Flavor: Google' http://metadata.google.internal/computeMetadata/v1/project/project-id)}"

secret() { gcloud secrets versions access latest --secret="$1" --project="$project"; }

install -d -m 0755 "$run_dir" "$run_dir/caddy"

render_passcodes() {
  local ids id name
  ids="$(gcloud secrets list --project="$project" --filter='labels.og-role=passcode' --format='value(name)')"
  if [ -z "$ids" ]; then
    echo "fetch-secrets: no passcode secrets (label og-role=passcode). Add one: deploy/gcp/rotate-passcode.sh add <name>" >&2
    return 1
  fi
  for id in $ids; do
    name="${id##*/og-passcode-}"
    printf '%s:%s\n' "$name" "$(secret "og-passcode-$name")"
  done | "$here/render-auth.sh" "$run_dir/caddy" | sed 's/^/fetch-secrets: passcode for /'
}

if [ "$mode" = "auth" ]; then
  render_passcodes
  if docker ps --format '{{.Names}}' | grep -q 'caddy'; then
    docker exec "$(docker ps --format '{{.Names}}' | grep 'caddy' | head -n1)" caddy reload --config /etc/caddy/Caddyfile
    echo "fetch-secrets: Caddy reloaded"
  fi
  exit 0
fi

umask 077
tmp="$(mktemp "$run_dir/.env.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
{
  printf 'OPENAI_API_KEY=%s\n' "$(secret og-openai-key)"
  printf 'ANTHROPIC_API_KEY=%s\n' "$(secret og-anthropic-key)"
  printf 'GRAFANA_CLOUD_OTLP_TOKEN=%s\n' "$(secret og-gcloud-otlp-token)"
} >"$tmp"
chmod 0600 "$tmp"
mv -f "$tmp" "$run_dir/env"
trap - EXIT
echo "fetch-secrets: wrote $run_dir/env"

render_passcodes
