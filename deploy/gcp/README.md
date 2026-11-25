# Deploy the Observability Gambit to the cloud

One small VM on Google Cloud runs the whole game for people with a link and a
passcode: the game server (`gambit serve --live`, Stockfish inside), the open-source
observability stack (Collector, Tempo, ClickHouse, Prometheus, Grafana), and Caddy
for HTTPS and passcodes. The Collector also sends every trace to the shared Grafana
Cloud stack (the same route as `make up-cloud`).

```
Internet ─443─▶ Caddy (Let's Encrypt, passcodes)
                  ├─ gambit.example.org        ─▶ gambit:8080   stage UI + human mode
                  ├─ dash.gambit.example.org   ─▶ grafana:3000  dashboards and the "Open trace" links
                  │                                             (Explore on the Tempo datasource)
                  └─ /healthz on the first host: no passcode (uptime check)
gambit ─OTLP─▶ Collector ─▶ ClickHouse, Tempo, Prometheus, file, and Grafana Cloud
gambit ─HTTPS─▶ OpenAI (gpt-6-luna) or Anthropic (claude-haiku-5-5), the player's pick
```

Everything below uses placeholders: `PROJECT`, `ZONE`, `DOMAIN`. Real values live in
your shell, Secret Manager and DNS, never in this repository.

## What players see

* A link (`https://gambit.example.org/live.html`) and a username + passcode prompt.
  Several passcodes can exist at once, each with a name (one per person or per
  audience). The name is the username.
* A start screen with a model picker: **Claude Haiku 5.5** (default) or **GPT-6 Luna**.
* One game at a time for the whole server. Someone who clicks "New game" while another
  game is running gets a polite refusal (HTTP 409), and the running game is untouched.

## Models

| Option | Provider | Model id | Key (Secret Manager) |
|---|---|---|---|
| Claude Haiku 5.5 (default) | Anthropic Messages API (native; shows the thinking summary) | `claude-haiku-5-5` | `og-anthropic-key` |
| GPT-6 Luna | OpenAI Responses API (native; shows the reasoning summary), effort `low` | `gpt-6-luna` | `og-openai-key` |

Verified on 2026-11-23 against OpenAI's model page (`developers.openai.com/api/docs/models/gpt-6-luna`)
and reasoning guide: the id is `gpt-6-luna` (no dated snapshot); `reasoning.effort` accepts `none`,
`low`, `medium` (default), `high`, `xhigh`, `max`; and **Chat Completions supports function calling
only with `reasoning_effort: "none"`**, which returns no reasoning, while the Responses API is the
documented route for function calling (no effort restriction is stated there). The game's agent
calls tools and the trace should show reasoning, so the `luna-6` option uses the native Responses
API (`/v1/responses`, stateless, `store: false`) at `reasoning.effort: "low"` with
`reasoning.summary: "auto"`: the smallest effort that still reasons, so the summary and the
reasoning-token count reach the trace. `--effort`/`GAMBIT_EFFORT` tunes Haiku only. **Not verified
without spending money:** that Luna returns a summary at `low`; the live Luna test checks it.
In the `agent_moved` event, `reasoning` is still the answer text (as for Haiku); the summary is in
the trace's chat span. The Haiku option calls Anthropic's native Messages API (`/v1/messages`) with adaptive thinking and
summaries on, so Claude's reasoning and its thinking-token count reach the trace (the
OpenAI-compatible endpoint returns neither). It leaves temperature to the provider and caps output at
4096 tokens, which thinking counts against. Thinking adds seconds to a move, so the per-move cap is
`--move-timeout ${GAMBIT_MOVE_TIMEOUT:-auto}` in `docker-compose.prod.yaml`: `auto` is 20s at
`GAMBIT_EFFORT=low`, 30s at `medium` (the model's default, so also when `GAMBIT_EFFORT` is empty),
60s at `high` for Haiku; Luna runs at `low`, so it needs 20s, and the cap is the longest of the offered models (20s if only Luna is offered). Set `GAMBIT_MOVE_TIMEOUT=45s` (or `0`) to
choose yourself. The trade-off is in the README's "Claude and its reasoning". Those numbers are
estimates: play a game on each effort before the talk and adjust. Pricing is listed at $0.10 / $0.50 per million
input / output tokens for GPT-6 Luna. **Not verified without spending money:** a real call to
either model. Do the two curl smoke tests below once before sharing the link.

The allow-list is `--models` (here `GAMBIT_MODELS`); only the ids `haiku-5.5` and `luna-6` exist,
and a request for anything else gets HTTP 400. Drop one from `GAMBIT_MODELS` to offer a single model.

## Limits

All set in `docker-compose.prod.yaml` from env vars, so a shared link cannot run away:

| Setting | Default | Effect |
|---|---|---|
| `GAMBIT_MAX_GAMES_PER_HOUR` | 12 | more starts get HTTP 429 with the wait |
| `GAMBIT_MAX_GAMES_PER_DAY` | 150 | same, rolling 24 h |
| `GAMBIT_IDLE_TIMEOUT` | 3m | a game whose human has not moved ends and frees the slot |
| `GAMBIT_GAME_TOKEN_BUDGET` | 300000 | the game ends once the agent used this many tokens |
| `--exclusive` (always on here) | | 409 instead of abandoning a running game |

The two game caps survive reboots and redeploys: the startup script rewrites
`/etc/gambit/gambit.env` each time, and takes them from instance metadata attributes
`gambit-max-games-per-hour` and `gambit-max-games-per-day` (plain integers, `0` = no cap).
An attribute that is not set keeps the default above. Do not edit the env file by hand; set
the attributes, then re-run the startup script (or reboot):

```sh
gcloud compute instances add-metadata <vm> --zone <zone> \
  --metadata=gambit-max-games-per-day=60,gambit-max-games-per-hour=0
gcloud compute ssh <vm> --zone <zone> -- sudo google_metadata_script_runner startup
```

Also set a monthly spend cap on both provider accounts (the plan assumes about $50 a month on
Anthropic): it is the backstop if everything else is wrong. Games on Haiku 5.5 cost about
$0.04 each (measured in the 200-game run).

## One-time setup

You need `gcloud` signed in, and `doctl` (or your DNS console) for the records.

1. **Check the plan.** Nothing is created by a dry run:

   ```bash
   export PROJECT=<your-project> DOMAIN=gambit.example.org ZONE=asia-northeast1-b
   export REF=<commit or tag to deploy>
   export GRAFANA_CLOUD_OTLP_ENDPOINT=https://otlp-gateway-<zone>.grafana.net/otlp
   export GRAFANA_CLOUD_INSTANCE_ID=<id>
   deploy/gcp/provision.sh        # prints every gcloud command
   ```

2. **Create the resources** (a custom VPC and firewall that allows only 80/443 and IAP ssh, a
   least-privilege service account, empty secrets, one static IP, the VM):

   ```bash
   deploy/gcp/provision.sh apply
   ```

   `asia-northeast1-b` is the default (close to Tokyo and India). `e2-standard-4` is a safe
   starting size; run `docker stats` on the VM during a few games and drop to `e2-standard-2`
   (`MACHINE=...`) only if the peak stays under about 5 GB.

3. **Put the secret values in**, from your own shell (nothing echoes them):

   ```bash
   printf '%s' "$ANTHROPIC_KEY" | gcloud secrets versions add og-anthropic-key    --project="$PROJECT" --data-file=-
   printf '%s' "$OPENAI_KEY"    | gcloud secrets versions add og-openai-key       --project="$PROJECT" --data-file=-
   printf '%s' "$GC_OTLP_TOKEN" | gcloud secrets versions add og-gcloud-otlp-token --project="$PROJECT" --data-file=-
   ```

4. **Add the first passcodes** (each prints its passcode once; send it to that person):

   ```bash
   deploy/gcp/rotate-passcode.sh add sonal
   deploy/gcp/rotate-passcode.sh add nic
   ```

5. **DNS.** Two A records to the reserved IP (`gcloud compute addresses describe og-play-ip --region asia-northeast1 --format='value(address)'`):
   `gambit` and `dash.gambit` under your zone (`trace.gambit` is no longer used; delete the record if you made one for the Jaeger version). With DigitalOcean DNS:

   ```bash
   doctl compute domain records create example.org --record-type A --record-name gambit       --record-data <IP> --record-ttl 300
   doctl compute domain records create example.org --record-type A --record-name dash.gambit  --record-data <IP> --record-ttl 300
   ```

   Caddy gets its certificates by itself once the names resolve.

6. **Restart so the VM picks the secrets up**, then check it:

   ```bash
   gcloud compute ssh og-play --project="$PROJECT" --zone="$ZONE" --tunnel-through-iap \
     --command='sudo systemctl restart gambit && systemctl status gambit --no-pager | head -5'
   curl -fsS https://gambit.example.org/healthz        # ok, no passcode needed
   ```

7. **Smoke-test both models with a real call** (a few cents) before sharing the link:

   ```bash
   curl -sS https://api.anthropic.com/v1/messages -H "x-api-key: $ANTHROPIC_KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
     -d '{"model":"claude-haiku-5-5","max_tokens":256,"thinking":{"type":"adaptive","display":"summarized"},"messages":[{"role":"user","content":"say ok"}]}' | head -c 300
   curl -sS https://api.openai.com/v1/responses -H "Authorization: Bearer $OPENAI_KEY" -H 'Content-Type: application/json' \
     -d '{"model":"gpt-6-luna","reasoning":{"effort":"low","summary":"auto"},"max_output_tokens":256,"store":false,"input":"say ok"}' | head -c 300
   ```

   Then play one game on each model from the browser and open its trace link.

8. **Monitoring** (after DNS resolves): an uptime check on `/healthz`, an alert policy (down,
   CPU over 90% for 10 min, disk over 80%) and an optional budget:

   ```bash
   ALERT_EMAIL=you@example.org BILLING_ACCOUNT=<id> BUDGET_USD=100 deploy/gcp/provision.sh monitoring apply
   ```

## Passcodes

One Secret Manager secret per passcode: `og-passcode-<name>`, label `og-role=passcode`.

```bash
deploy/gcp/rotate-passcode.sh list
deploy/gcp/rotate-passcode.sh add workshop-2     # a new named passcode; prints it once
deploy/gcp/rotate-passcode.sh rotate sonal --apply
deploy/gcp/rotate-passcode.sh remove workshop-2 --apply
```

`--apply` runs `fetch-secrets.sh auth` on the VM over IAP ssh, which rehashes the passcodes
(bcrypt, with Caddy's own `caddy hash-password`) into `/run/gambit/caddy/users.caddy` and reloads
Caddy: live in about ten seconds, no restart. Games already running keep going.

**Attribution.** Caddy logs the username (`user_id`) with every request and sends it to the game as the
`X-Gambit-User` header (always overwritten, a client cannot choose it). The game records it as
`gambit.player.label` on the `chess.move` and `invoke_agent` spans of that player's games, and as
`player` in `data/games.jsonl`. In Tempo (Grafana Explore) or Grafana Cloud filter by that attribute. The passcode itself
is never logged or traced (Caddy redacts the `Authorization` header and strips it before proxying).

Use a long passcode (the script makes 24 random characters): Caddy has no lockout.

## Day to day

```bash
# stop paying for the VM between sessions (the disk and IP still cost a little)
gcloud compute instances stop  og-play --project="$PROJECT" --zone="$ZONE"
gcloud compute instances start og-play --project="$PROJECT" --zone="$ZONE"   # the stack comes back by itself

# logs, from the VM (gcloud compute ssh ... --tunnel-through-iap)
cd /opt/gambit && sudo docker compose -p gambit logs -f gambit caddy

# deploy a new version: change REF in the instance metadata, then reboot or rerun the startup script
gcloud compute instances add-metadata og-play --project="$PROJECT" --zone="$ZONE" --metadata=gambit-ref=<new-ref>
gcloud compute ssh og-play --project="$PROJECT" --zone="$ZONE" --tunnel-through-iap --command='sudo google_metadata_script_runner startup'
```

Caveats worth knowing: Tempo keeps traces on the `tempo` named volume, so trace links survive restarts and
redeploys (`docker compose down -v` deletes the volume; a plain `down` does not).
The links open the trace in Grafana Explore at `dash.<domain>` with a time window around the move,
so the passcode that opens the game opens the trace. Every trace goes to Tempo (no sampling; the volume is a few hundred spans per game, well inside the
VM's disk at the 150-games-a-day cap), and a trace link appears about 5 s after a move is graded. Prompt capture is on, so players' moves and the model's replies are stored on the VM
and in Grafana Cloud: tell players.

## Test it on your laptop first

```bash
deploy/gcp/local-test.sh
```

Runs the production compose files with the mock model (no keys, no spend), a local Caddy certificate
on `gambit.localhost`, two passcodes, and checks passcode login, `/healthz`, SSE through the proxy,
the one-game lock from two logins, both model options, passcode labels in Tempo traces, trace links,
the idle timeout, the model allow-list and the same-origin rule. `make up-prod` starts the same stack
from a `.env.prod` (see `env.example`; it needs a passcode file made by `render-auth.sh`).

## Teardown

```bash
gcloud compute instances delete og-play --project="$PROJECT" --zone="$ZONE" --quiet
gcloud compute addresses delete og-play-ip --project="$PROJECT" --region=asia-northeast1 --quiet
gcloud compute firewall-rules delete og-play-web og-play-iap-ssh --project="$PROJECT" --quiet
gcloud compute networks subnets delete og-play-subnet-asia-northeast1 --project="$PROJECT" --region=asia-northeast1 --quiet
gcloud compute networks delete og-play-net --project="$PROJECT" --quiet
# secrets: og-openai-key og-anthropic-key og-gcloud-otlp-token and each og-passcode-<name>
for s in $(gcloud secrets list --project="$PROJECT" --filter='name~og-' --format='value(name)'); do gcloud secrets delete "$s" --project="$PROJECT" --quiet; done
```

Copy `/opt/gambit/data` (the human games) off the VM first, and remove the DNS records. Rotating or
removing the passcodes is enough to close the link without deleting anything.

## Files

| File | What it is |
|---|---|
| `docker-compose.prod.yaml` | override: game server, Caddy, ports on 127.0.0.1, Grafana anonymous Viewer, log rotation |
| `Caddyfile` | HTTPS, passcodes, `X-Gambit-User`, `/healthz`, SSE |
| `gambit.service` | systemd unit: fetch secrets, start the stack at every boot |
| `fetch-secrets.sh`, `render-auth.sh` | Secret Manager to `/run` (tmpfs); passcodes to Caddy's bcrypt file |
| `rotate-passcode.sh` | add, rotate, remove, list named passcodes |
| `startup-script.sh` | idempotent VM bootstrap: Docker, Ops Agent, checkout at `REF` |
| `provision.sh` | the gcloud commands (dry run by default) |
| `env.example` | placeholders for `.env.prod` |
| `local-test.sh`, `e2e.py` | the laptop end-to-end test |
