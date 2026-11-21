#!/usr/bin/env bash
# Turns passcodes into Caddy's users.caddy (name + bcrypt hash per line).
#
#   printf '%s\n' 'sonal:<passcode>' 'nic:<passcode>' | render-auth.sh /run/gambit/caddy
#
# Reads "name:passcode" lines on stdin (so passcodes never appear in a process
# argument list), writes OUT_DIR/users.caddy with mode 0644 (hashes only), and
# prints the names it wrote. fetch-secrets.sh feeds it from Secret Manager;
# the local test feeds it throwaway values.
set -euo pipefail

out_dir="${1:?usage: render-auth.sh OUT_DIR  (reads name:passcode lines on stdin)}"
caddy_image="${CADDY_IMAGE:-caddy:2.8}"
name_re='^[a-z0-9][a-z0-9_-]{0,31}$'

mkdir -p "$out_dir"
tmp="$(mktemp "$out_dir/.users.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

echo "basic_auth {" >"$tmp"
count=0
while IFS= read -r line || [ -n "$line" ]; do
  [ -z "$line" ] && continue
  name="${line%%:*}"
  pass="${line#*:}"
  if [[ ! "$name" =~ $name_re ]]; then
    echo "render-auth: refusing passcode name '$name' (lowercase letters, digits, - and _, up to 32)" >&2
    exit 1
  fi
  if [ "${#pass}" -lt 12 ]; then
    echo "render-auth: the passcode for '$name' is shorter than 12 characters" >&2
    exit 1
  fi
  # hash-password reads the passcode from stdin when it is not a terminal.
  hash="$(printf '%s\n' "$pass" | docker run --rm -i "$caddy_image" caddy hash-password)"
  printf '\t%s %s\n' "$name" "$hash" >>"$tmp"
  echo "$name"
  count=$((count + 1))
done
echo "}" >>"$tmp"

if [ "$count" -eq 0 ]; then
  echo "render-auth: no passcodes given. Add one with rotate-passcode.sh add <name>." >&2
  exit 1
fi
chmod 0644 "$tmp"
mv -f "$tmp" "$out_dir/users.caddy"
trap - EXIT
