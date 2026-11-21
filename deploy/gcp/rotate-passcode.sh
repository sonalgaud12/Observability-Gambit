#!/usr/bin/env bash
# Add, rotate or remove one named passcode, or list them. Run from your own
# shell (where gcloud is signed in), not on the VM.
#
#   PROJECT=my-project ./rotate-passcode.sh list
#   PROJECT=my-project ./rotate-passcode.sh add sonal             # prints a new passcode once
#   PROJECT=my-project ./rotate-passcode.sh rotate sonal --apply  # new passcode, live in ~10 s
#   PROJECT=my-project ./rotate-passcode.sh remove workshop-1 --apply
#
# Each passcode is its own Secret Manager secret, og-passcode-<name>, labelled
# og-role=passcode. The <name> is the username people type with their passcode,
# what Caddy logs on every request, and the gambit.player.label on that
# person's (or campaign's) games, so use one per person or per audience.
# Names: lowercase letters, digits, - and _, up to 32 characters.
#
# Flags
#   --apply        also push the change to the running VM: ssh over IAP, rerun
#                  fetch-secrets.sh auth, reload Caddy. Without it the command to run is printed.
#   --from-stdin   take the passcode from stdin instead of generating one (add/rotate).
#
# Environment: PROJECT (required), ZONE (default asia-northeast1-b), VM (default og-play).
# The passcode is printed to your terminal once and never written anywhere else.
set -euo pipefail

: "${PROJECT:?set PROJECT}"
ZONE="${ZONE:-asia-northeast1-b}"
VM="${VM:-og-play}"
name_re='^[a-z0-9][a-z0-9_-]{0,31}$'

usage() { sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

cmd="${1:-}"
[ -n "$cmd" ] || usage
shift
name=""
apply=0
from_stdin=0
for a in "$@"; do
  case "$a" in
    --apply) apply=1 ;;
    --from-stdin) from_stdin=1 ;;
    -h | --help) usage ;;
    -*) echo "unknown flag $a" >&2; usage ;;
    *) name="$a" ;;
  esac
done

need_name() {
  [ -n "$name" ] || { echo "$cmd needs a name" >&2; usage; }
  [[ "$name" =~ $name_re ]] || { echo "bad name '$name': lowercase letters, digits, - and _, up to 32" >&2; exit 1; }
}
secret="og-passcode-$name"
exists() { gcloud secrets describe "$secret" --project="$PROJECT" >/dev/null 2>&1; }

new_passcode() {
  local p
  if [ "$from_stdin" = 1 ]; then
    IFS= read -r p
  else
    p="$(openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | head -c 24)"
  fi
  [ "${#p}" -ge 16 ] || { echo "a passcode needs at least 16 characters (Caddy has no lockout)" >&2; exit 1; }
  printf '%s' "$p"
}

push() {
  local remote="sudo /opt/gambit/deploy/gcp/fetch-secrets.sh auth"
  if [ "$apply" = 1 ]; then
    gcloud compute ssh "$VM" --project="$PROJECT" --zone="$ZONE" --tunnel-through-iap --command="$remote"
  else
    echo
    echo "Not live yet. Apply with:  gcloud compute ssh $VM --project=$PROJECT --zone=$ZONE --tunnel-through-iap --command='$remote'"
    echo "(or rerun this command with --apply). The VM also picks it up at the next boot."
  fi
}

case "$cmd" in
  list)
    gcloud secrets list --project="$PROJECT" --filter='labels.og-role=passcode' --format='value(name)' \
      | sed -e 's#.*/##' -e 's#^og-passcode-##' | sort
    ;;
  add)
    need_name
    exists && { echo "$secret already exists; use rotate" >&2; exit 1; }
    pass="$(new_passcode)"
    printf '%s' "$pass" | gcloud secrets create "$secret" --project="$PROJECT" \
      --replication-policy=automatic --labels=og-role=passcode --data-file=- >/dev/null
    echo "Added passcode '$name'. Username: $name   Passcode (shown once): $pass"
    push
    ;;
  rotate)
    need_name
    exists || { echo "$secret does not exist; use add" >&2; exit 1; }
    pass="$(new_passcode)"
    printf '%s' "$pass" | gcloud secrets versions add "$secret" --project="$PROJECT" --data-file=- >/dev/null
    echo "Rotated passcode '$name'. Username: $name   Passcode (shown once): $pass"
    push
    ;;
  remove)
    need_name
    exists || { echo "$secret does not exist" >&2; exit 1; }
    gcloud secrets delete "$secret" --project="$PROJECT" --quiet
    echo "Removed passcode '$name'."
    push
    ;;
  *) usage ;;
esac
