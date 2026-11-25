#!/usr/bin/env bash
# GCE startup script for the play VM (Debian 12). provision.sh sets it as instance
# metadata; it runs as root at every boot and is idempotent: the first boot
# installs Docker and the Ops Agent and clones the repo, later boots only
# refresh the checkout and the config. Reads its settings from instance metadata:
#
#   gambit-domain, gambit-repo, gambit-ref, grafana-cloud-endpoint, grafana-cloud-instance-id
#
# Optional attributes (absent means the compose defaults apply):
#
#   gambit-max-games-per-hour, gambit-max-games-per-day
#
# Secrets are NOT here: fetch-secrets.sh reads them from Secret Manager when the
# gambit service starts.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

md() { curl -fsS -H 'Metadata-Flavor: Google' "http://metadata.google.internal/computeMetadata/v1/instance/attributes/$1"; }
DOMAIN="$(md gambit-domain)"
REPO="$(md gambit-repo)"
REF="$(md gambit-ref)"
GC_ENDPOINT="$(md grafana-cloud-endpoint)"
GC_INSTANCE="$(md grafana-cloud-instance-id)"
# Optional: empty when the attribute is not set.
md_opt() { md "$1" 2>/dev/null || true; }
MAX_PER_HOUR="$(md_opt gambit-max-games-per-hour)"
MAX_PER_DAY="$(md_opt gambit-max-games-per-day)"

# --- Docker (from Docker's apt repository: compose v2 with `!override` support) ---
if ! command -v docker >/dev/null 2>&1; then
  apt-get update
  apt-get install -y --no-install-recommends ca-certificates curl git gnupg jq
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    >/etc/apt/sources.list.d/docker.list
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
install -d /etc/docker
cat >/etc/docker/daemon.json <<'JSON'
{ "log-driver": "json-file", "log-opts": { "max-size": "10m", "max-file": "3" } }
JSON
systemctl enable --now docker

# --- Ops Agent: system metrics and logs to Cloud Monitoring / Logging ---
if ! systemctl list-unit-files | grep -q '^google-cloud-ops-agent'; then
  curl -fsS https://dl.google.com/cloudagents/add-google-cloud-ops-agent-repo.sh -o /tmp/add-ops-agent.sh
  bash /tmp/add-ops-agent.sh --also-install
fi

# --- the code, pinned to REF ---
if [ ! -d /opt/gambit/.git ]; then
  git clone "$REPO" /opt/gambit
fi
git -C /opt/gambit fetch --tags --quiet origin || echo "startup: fetch failed, keeping the current checkout"
git -C /opt/gambit checkout --quiet --detach "$REF"

# --- non-secret settings ---
install -d -m 0755 /etc/gambit
cat >/etc/gambit/gambit.env <<ENV
GAMBIT_DOMAIN=$DOMAIN
GRAFANA_CLOUD_OTLP_ENDPOINT=$GC_ENDPOINT
GRAFANA_CLOUD_INSTANCE_ID=$GC_INSTANCE
GAMBIT_RUN=cloud
ENV
# Caps are written only when set, so an unset attribute keeps the compose default.
# They must be plain integers (0 = no cap): the file is read by systemd and compose.
for pair in "GAMBIT_MAX_GAMES_PER_HOUR=$MAX_PER_HOUR" "GAMBIT_MAX_GAMES_PER_DAY=$MAX_PER_DAY"; do
  [ -n "${pair#*=}" ] || continue
  [[ "${pair#*=}" =~ ^[0-9]+$ ]] || { echo "startup: ignoring non-numeric ${pair%%=*}"; continue; }
  echo "$pair" >>/etc/gambit/gambit.env
done

# --- the service ---
install -m 0644 /opt/gambit/deploy/gcp/gambit.service /etc/systemd/system/gambit.service
systemctl daemon-reload
systemctl enable gambit.service
systemctl restart gambit.service || echo "startup: gambit.service failed to start (secrets not added yet?): journalctl -u gambit"
