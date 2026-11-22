#!/usr/bin/env bash
# The gcloud commands that create the cloud play deployment. Read it before you
# run it: it is meant to be reviewed, and by default it only PRINTS what it
# would do.
#
#   PROJECT=my-project DOMAIN=gambit.example.org REF=<commit-or-tag> \
#   GRAFANA_CLOUD_OTLP_ENDPOINT=https://otlp-gateway-<zone>.grafana.net/otlp \
#   GRAFANA_CLOUD_INSTANCE_ID=<id> \
#     ./provision.sh                    # dry run: prints every command, creates nothing
#     ./provision.sh apply              # creates network, firewall, service account, secrets, IP, VM
#     ./provision.sh monitoring         # dry run of the monitoring step
#     ./provision.sh monitoring apply   # AFTER DNS points at the IP: uptime check, alerts, budget
#
# Variables (no defaults for the first four: nothing here knows your project)
#   PROJECT       GCP project id
#   DOMAIN        public hostname of the game, e.g. gambit.example.org; the script also
#                 expects dash.$DOMAIN (both get the same IP)
#   REF           git commit or tag of the repo to run on the VM (pin it: boots are reproducible)
#   GRAFANA_CLOUD_OTLP_ENDPOINT, GRAFANA_CLOUD_INSTANCE_ID   the shared Grafana Cloud stack
#                 (not secrets; the token is a Secret Manager secret, below)
#   ZONE          default asia-northeast1-b (region asia-northeast1: close to Tokyo and India)
#   NAME          VM name, default og-play
#   MACHINE       default e2-standard-4; measure with `docker stats`, then maybe e2-standard-2
#   DISK_GB       default 50 (pd-balanced)
#   REPO_URL      default https://github.com/sonalgaud12/Observability-Gambit.git
#   ALERT_EMAIL   monitoring: who gets the alerts
#   BILLING_ACCOUNT, BUDGET_USD   monitoring: optional budget with 50/90/100 % alerts (default 100)
#
# What it makes: custom VPC og-play-net with a firewall that allows only tcp 80/443
# from anywhere and ssh from Google's IAP range; a service account that can read
# secrets whose names start with og-, write logs and metrics, and nothing else;
# empty secrets (YOU add the values, so keys never pass through a script or a chat);
# one reserved IPv4; one VM from the Debian 12 image running startup-script.sh.
#
# It does NOT touch DNS. Records are printed at the end.
set -euo pipefail

# Every mode only prints unless the last word is "apply".
what=infra; apply=0
for a in "$@"; do
  case "$a" in
    apply) apply=1 ;;
    monitoring) what=monitoring ;;
    plan | infra) ;;
    *) echo "usage: $0 [monitoring] [apply]" >&2; exit 2 ;;
  esac
done
mode="$what$([ "$apply" = 1 ] && echo ' (APPLYING)' || echo ' (dry run)')"

: "${PROJECT:?set PROJECT}"
: "${DOMAIN:?set DOMAIN}"
: "${REF:?set REF (a commit sha or tag)}"
: "${GRAFANA_CLOUD_OTLP_ENDPOINT:?set GRAFANA_CLOUD_OTLP_ENDPOINT}"
: "${GRAFANA_CLOUD_INSTANCE_ID:?set GRAFANA_CLOUD_INSTANCE_ID}"
ZONE="${ZONE:-asia-northeast1-b}"
REGION="${ZONE%-*}"
NAME="${NAME:-og-play}"
MACHINE="${MACHINE:-e2-standard-4}"
DISK_GB="${DISK_GB:-50}"
REPO_URL="${REPO_URL:-https://github.com/sonalgaud12/Observability-Gambit.git}"
BUDGET_USD="${BUDGET_USD:-100}"
NET="og-play-net"
SUBNET="og-play-subnet-$REGION"
SA_NAME="og-play-vm"
SA="$SA_NAME@$PROJECT.iam.gserviceaccount.com"
IP_NAME="$NAME-ip"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# run: print the command; execute it only when applying.
run() {
  if [ "$1" = g ]; then printf '+ gcloud --project=%s' "$PROJECT"; printf ' %q' "${@:2}"; else printf '+'; printf ' %q' "$@"; fi
  printf '\n'
  if [ "$apply" = 1 ]; then "$@"; fi
}
# exists: only meaningful when applying; a dry run never calls gcloud for reads.
exists() { [ "$apply" = 1 ] && "$@" >/dev/null 2>&1; }
g() { gcloud --project="$PROJECT" "$@"; }

echo "# project=$PROJECT zone=$ZONE domain=$DOMAIN ref=$REF mode=$mode"

if [ "$what" = infra ]; then
  run g services enable compute.googleapis.com secretmanager.googleapis.com logging.googleapis.com monitoring.googleapis.com iap.googleapis.com

  # --- network and firewall ---
  exists g compute networks describe "$NET" || run g compute networks create "$NET" --subnet-mode=custom
  exists g compute networks subnets describe "$SUBNET" --region="$REGION" ||
    run g compute networks subnets create "$SUBNET" --network="$NET" --region="$REGION" --range=10.20.0.0/24
  exists g compute firewall-rules describe og-play-web ||
    run g compute firewall-rules create og-play-web --network="$NET" --direction=INGRESS \
      --allow=tcp:80,tcp:443 --source-ranges=0.0.0.0/0 --target-tags=og-play
  exists g compute firewall-rules describe og-play-iap-ssh ||
    run g compute firewall-rules create og-play-iap-ssh --network="$NET" --direction=INGRESS \
      --allow=tcp:22 --source-ranges=35.235.240.0/20 --target-tags=og-play

  # --- service account: least privilege ---
  exists g iam service-accounts describe "$SA" ||
    run g iam service-accounts create "$SA_NAME" --display-name="Observability Gambit play VM"
  for role in roles/logging.logWriter roles/monitoring.metricWriter roles/secretmanager.viewer; do
    run g projects add-iam-policy-binding "$PROJECT" --member="serviceAccount:$SA" --role="$role" --condition=None --quiet
  done
  # Read secret VALUES only for secrets named og-*. (secretmanager.viewer above is
  # metadata only: it lets the VM list passcode names; it cannot read a value.)
  number="$([ "$apply" = 1 ] && g projects describe "$PROJECT" --format='value(projectNumber)' || echo PROJECT_NUMBER)"
  run g projects add-iam-policy-binding "$PROJECT" --member="serviceAccount:$SA" --role=roles/secretmanager.secretAccessor \
    --condition="title=og-secrets-only,expression=resource.name.startsWith(\"projects/$number/secrets/og-\")" --quiet

  # --- secrets: containers only; add the values yourself (see README) ---
  for s in og-openai-key og-anthropic-key og-gcloud-otlp-token; do
    exists g secrets describe "$s" || run g secrets create "$s" --replication-policy=automatic
  done

  # --- address and VM ---
  exists g compute addresses describe "$IP_NAME" --region="$REGION" ||
    run g compute addresses create "$IP_NAME" --region="$REGION"
  ip="$([ "$apply" = 1 ] && g compute addresses describe "$IP_NAME" --region="$REGION" --format='value(address)' || echo "<reserved-ip>")"

  exists g compute instances describe "$NAME" --zone="$ZONE" ||
    run g compute instances create "$NAME" --zone="$ZONE" --machine-type="$MACHINE" \
      --image-family=debian-12 --image-project=debian-cloud \
      --boot-disk-size="${DISK_GB}GB" --boot-disk-type=pd-balanced \
      --network-interface="subnet=$SUBNET,address=$ip" --tags=og-play \
      --service-account="$SA" --scopes=cloud-platform \
      --shielded-secure-boot --shielded-vtpm --shielded-integrity-monitoring \
      --metadata-from-file=startup-script="$here/startup-script.sh" \
      --metadata="gambit-domain=$DOMAIN,gambit-repo=$REPO_URL,gambit-ref=$REF,grafana-cloud-endpoint=$GRAFANA_CLOUD_OTLP_ENDPOINT,grafana-cloud-instance-id=$GRAFANA_CLOUD_INSTANCE_ID"

  cat <<EOF

# Next, by hand:
# 1. Put the secret values in (your own shell; nothing is echoed):
#      printf '%s' "\$OPENAI_KEY"      | gcloud secrets versions add og-openai-key       --project=$PROJECT --data-file=-
#      printf '%s' "\$ANTHROPIC_KEY"   | gcloud secrets versions add og-anthropic-key    --project=$PROJECT --data-file=-
#      printf '%s' "\$GC_OTLP_TOKEN"   | gcloud secrets versions add og-gcloud-otlp-token --project=$PROJECT --data-file=-
# 2. Add passcodes (each prints its passcode once):
#      PROJECT=$PROJECT ./rotate-passcode.sh add sonal
# 3. DNS: two A records to $ip for $DOMAIN and dash.$DOMAIN (README: DNS).
# 4. Reboot the VM (or run fetch-secrets.sh + systemctl restart gambit) so it picks up the secrets,
#    then ./provision.sh monitoring apply.
EOF
  exit 0
fi

# --- monitoring (after DNS resolves; the uptime check needs the real hostname) ---
run g monitoring uptime create og-play-healthz --resource-type=uptime-url \
  --resource-labels="host=$DOMAIN,project_id=$PROJECT" --protocol=https --port=443 --path=/healthz \
  --validate-ssl=true --status-classes=2xx --period=5

if [ -n "${ALERT_EMAIL:-}" ]; then
  api=https://monitoring.googleapis.com/v3/projects/$PROJECT
  echo "+ create email channel and alert policy for $ALERT_EMAIL through $api (REST; gcloud has no GA command)"
  if [ "$apply" = 1 ]; then
    auth="Authorization: Bearer $(gcloud auth print-access-token)"
    ch="$(curl -fsS -X POST -H "$auth" -H 'Content-Type: application/json' "$api/notificationChannels" \
      -d "{\"type\":\"email\",\"displayName\":\"og-play alerts\",\"labels\":{\"email_address\":\"$ALERT_EMAIL\"}}" | jq -r .name)"
    check_id="$(g monitoring uptime list-configs --filter='displayName=og-play-healthz' --format='value(name)' | head -n1 | sed 's#.*/##')"
    curl -fsS -X POST -H "$auth" -H 'Content-Type: application/json' "$api/alertPolicies" -d "$(jq -n --arg ch "$ch" --arg id "$check_id" '{
      displayName: "og-play: down, CPU or disk",
      combiner: "OR",
      notificationChannels: [$ch],
      conditions: [
        {displayName: "healthz failing (2 checks)", conditionThreshold: {
          filter: ("metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND metric.label.check_id=\"" + $id + "\" AND resource.type=\"uptime_url\""),
          comparison: "COMPARISON_GT", thresholdValue: 1, duration: "60s",
          aggregations: [{alignmentPeriod: "1200s", perSeriesAligner: "ALIGN_NEXT_OLDER", crossSeriesReducer: "REDUCE_COUNT_FALSE", groupByFields: ["resource.label.*"]}]}},
        {displayName: "CPU over 90% for 10 min", conditionThreshold: {
          filter: "metric.type=\"compute.googleapis.com/instance/cpu/utilization\" AND resource.type=\"gce_instance\"",
          comparison: "COMPARISON_GT", thresholdValue: 0.9, duration: "600s",
          aggregations: [{alignmentPeriod: "60s", perSeriesAligner: "ALIGN_MEAN"}]}},
        {displayName: "disk over 80% (Ops Agent)", conditionThreshold: {
          filter: "metric.type=\"agent.googleapis.com/disk/percent_used\" AND metric.label.state=\"used\" AND resource.type=\"gce_instance\"",
          comparison: "COMPARISON_GT", thresholdValue: 80, duration: "300s",
          aggregations: [{alignmentPeriod: "300s", perSeriesAligner: "ALIGN_MEAN"}]}}
      ]}')" >/dev/null
  fi
else
  echo "# set ALERT_EMAIL to also create the email channel and the alert policy"
fi

if [ -n "${BILLING_ACCOUNT:-}" ]; then
  run gcloud billing budgets create --billing-account="$BILLING_ACCOUNT" --display-name="og-play" \
    --filter-projects="projects/$PROJECT" --budget-amount="${BUDGET_USD}USD" \
    --threshold-rule=percent=0.5 --threshold-rule=percent=0.9 --threshold-rule=percent=1.0
else
  echo "# set BILLING_ACCOUNT (and BUDGET_USD) to also create the budget alert"
fi
