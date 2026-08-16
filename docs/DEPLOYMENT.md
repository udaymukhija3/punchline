# Punchline Deployment Guide

## Deployment status

The repository is production-shaped for one region on Fly.io: one container
serves the React app, REST API, WebSockets, health/readiness, metrics, the daily
worker, and the admin desk. Managed Postgres is required in production — it
holds room leases, daily groups, and the card content itself. The release
command runs forward-only, checksum-guarded migrations before a new app image
starts.

Deployment is `READY_WITH_MANUAL_STEPS`: the code and automated gates are in
place, while database provisioning/backups, secrets, DNS, dashboards, alerts,
and a restore drill must be completed by the operator.

## What a release must get right

Two failure modes are specific to this app and worth knowing before you start.

**A silent seed-deck fallback.** If migration 005 has not applied, or moderation
has retired so many cards that the deck falls below its playable minimum, the
app falls back to `seed/cards.json` and keeps serving a perfectly playable game.
But seed cards carry no database row, so card telemetry stops counting and the
in-game report button hides itself. `deploy-check.mjs` asserts
`deck_source: "database"` for exactly this reason. Do not skip it.

**A desk nobody can open.** With `PUNCHLINE_ADMIN_TOKEN` unset, every admin
route returns 404. That is the correct default for an unmoderated deployment,
but if you intend to moderate, set the secret *before* the first deploy.

## 1. Verify locally

For a disposable local database created by an older revision that mounted SQL
files directly, remove that local volume once before using the current Compose
flow. This deletes only the local Compose database:

```bash
docker compose down -v
```

Build the production image, start Postgres, run the release migration, and start
the app:

```bash
docker compose up --build -d app
```

Compose sets `PUNCHLINE_ADMIN_TOKEN=local-admin-token`, so the desk is reachable
at `http://127.0.0.1:8080/admin` locally. Run the full gate:

```bash
PUNCHLINE_ADMIN_TOKEN=local-admin-token node scripts/deploy-check.mjs http://127.0.0.1:8080
```

The gate validates the built React assets, readiness, the deck source, the
health flags, every required metric, a full three-player live WebSocket round,
the daily create/join/submit/privacy flow, and the reporting plus admin queues.
Temporary data it creates is cleaned up.

Inspect logs and stop the stack:

```bash
docker compose logs --tail=200 app migrate postgres
docker compose down
```

## 2. Create the Fly app

Install and authenticate the Fly CLI, choose the organization, and create the
app from the existing configuration without deploying yet:

```bash
fly auth login
fly launch --copy-config --no-deploy
```

Change the placeholder `app = "punchline"` in `fly.toml` if the name is already
taken. Keep all app machines and Postgres in the same primary region for the
first release.

## 3. Provision managed Postgres

Create a production Postgres database in the app's primary region. Before
deploying, enable automated backups and point-in-time recovery when the provider
offers it. Obtain a TLS connection string and confirm the provider connection
budget can support:

```txt
machine count × DB_MAX_OPEN_CONNS + migration/administrative connections
```

The default app pool cap is 10 connections per machine. Do not add replicas,
Redis, or a queue for this release.

For Fly Managed Postgres, create and attach the cluster interactively (review the
selected paid plan before confirming):

```bash
fly mpg create --name punchline-db --region iad
fly mpg list
fly mpg attach <cluster-id> --app <your-app-name>
```

The attach command creates the app's `DATABASE_URL` secret. With another
provider, use its TLS connection string in the secret step below.

## 4. Set production secrets

Generate tokens with a password manager or cryptographic generator. Do not
commit them, and avoid shell history on a shared machine.

```bash
fly secrets set \
  PUNCHLINE_ALLOWED_ORIGINS='https://play.example.com' \
  PUNCHLINE_METRICS_TOKEN='replace-with-random-secret' \
  PUNCHLINE_ADMIN_TOKEN='replace-with-a-different-random-secret'
```

Generate a strong admin token with `openssl rand -base64 32`. Use a different
value from the metrics token. To rotate later, set the secret again and
redeploy; operators re-enter it in the desk and no session survives rotation.

Omit `PUNCHLINE_ADMIN_TOKEN` deliberately if nobody will moderate this
deployment. The desk then does not exist, which is safer than an unattended one.

If Managed Postgres was not attached with `fly mpg attach`, include
`DATABASE_URL='postgres://...TLS-connection-string...'` in that command.

`PUNCHLINE_REQUIRE_DATABASE=true` is already set in `fly.toml`, so both release
migrations and app startup fail closed if the database secret is missing.

Decide how the Fly edge supplies client IPs. Prefer an explicit
`PUNCHLINE_TRUSTED_PROXY_CIDRS` value verified against the current platform
network. Set `PUNCHLINE_TRUST_PROXY_HEADERS=true` only if the immediate edge is
known to overwrite spoofed forwarded headers and no stable CIDR list exists.
This also decides how card reports are deduplicated, since a reporter is
identified by client address.

### Decide the deck-source policy

`fly.toml` ships `PUNCHLINE_DECK_SOURCE` unset, which means `auto`: prefer the
database deck, fall back to the seed file if it is unusable. That keeps the game
up during a content problem at the cost of silently disabling telemetry and
reporting.

Set `PUNCHLINE_DECK_SOURCE=database` instead if you would rather a bad content
state fail the deploy than serve a degraded product. The post-deploy gate
catches the fallback either way; this decides whether the app refuses to boot.

## 5. Preflight the release

Run the exact repository gates before pushing:

```bash
cd backend
test -z "$(gofmt -l .)"
go vet ./...
go test ./... -timeout 60s
go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...

cd ../frontend
npm ci
npm run build

cd ..
node --check scripts/deploy-check.mjs
node --check scripts/smoke-realtime.mjs
node --check scripts/smoke-daily.mjs
node --check scripts/smoke-content.mjs
node scripts/generate-card-seed-migration.mjs && git diff --exit-code migrations/005_seed_official_pack.sql
docker build -t punchline-production-check .
git diff --check
```

`govulncheck` reports standard-library findings against your *local* toolchain.
What ships is the `golang:1.25-alpine` builder in the Dockerfile; verify that
image is current if the report lists stdlib advisories.

Confirm CI is green, including the Postgres job that runs the daily, cards,
content, and telemetry integration tests under `-race` plus all three smokes.

## 6. Deploy one machine

Deploy the image. Fly runs `/app/migrate` as the release command; the migration
runner serializes concurrent releases with a Postgres advisory lock and refuses
edited migration history.

```bash
fly deploy
fly status
fly scale count 1
```

The first release applies six migrations. `005_seed_official_pack.sql` loads 96
prompts and 180 answers into the content tables, and `006_content_platform.sql`
adds the reporting and candidate tables. Both are additive.

If the release migration fails, stop. Do not bypass it and do not automatically
roll the database backward. Fix forward or restore in a controlled window.

## 7. Run the post-deploy gate

```bash
PUNCHLINE_METRICS_TOKEN='same-token-as-the-secret' \
PUNCHLINE_ADMIN_TOKEN='same-admin-token-as-the-secret' \
  node scripts/deploy-check.mjs https://your-app.fly.dev
```

Confirm the health line reports `deck_source: "database"`. If it reports `seed`,
the app is serving the launch deck off disk: check that migration 005 applied
and that the deck still meets its playable minimum. The game will look fine, so
this line is the only easy signal.

Then manually verify from two browsers or phones:

1. Open `/`, create a live room, join three players, and finish a round.
2. Use the "Report card" control under the prompt once, and confirm it
   acknowledges.
3. Open `/daily`, create a group, join from the invite link in another browser,
   submit both answers, and confirm neither browser can see the other's answer
   before reveal.
4. Open `/admin`, enter the admin token, and confirm the reported card appears
   in the queue. Dismiss it to return the card to rotation.
5. Confirm `/readyz` returns `200` and the metrics scrape succeeds with the
   bearer token.

## 8. Configure domain, TLS, and caching

Attach the production domain and point DNS at Fly. Keep WebSocket upgrades
enabled. If a CDN/proxy is added, cache only immutable `/assets/*` responses;
bypass `/api/*`, `/ws/*`, `/healthz`, `/readyz`, and `/metrics`.

Update the origin secret after the domain is live:

```bash
fly secrets set PUNCHLINE_ALLOWED_ORIGINS='https://play.example.com'
```

Run the deploy check against the custom domain again.

## 9. Add dashboards and alerts

Scrape `/metrics` with `Authorization: Bearer <token>` and probe `/readyz`.
Create alerts for:

- readiness failures;
- daily worker errors or a last-success time older than two intervals;
- room state save/load failures;
- registry latency and error growth;
- DB connections near the configured pool limit or rising pool waits;
- heap/goroutine growth and WebSocket disconnect spikes;
- `punchline_cards_auto_retired_total` rising, which is either a bad content
  batch or report abuse — check the admin queue before restoring anything;
- `punchline_card_telemetry_events_total{outcome="dropped"}` or `"failed"`
  rising, which means card counts are drifting low;
- platform bandwidth, Postgres storage, and backup failures.

## 10. Moderation readiness

Before inviting real players, decide who watches the desk and how often. A card
retires automatically at three distinct reporters, so the queue is a review
surface rather than the first line of defense — but nothing else removes bad
content, and the candidate queue only grows.

Check `/admin` after the first week of real daily play: candidates arrive as
rounds finalize, and an unreviewed queue means the deck never grows.

## 11. Rehearse rollback and restore

For an application rollback, select the previous Fly release/image only when it
is compatible with the additive database migrations, then rerun the deploy
check. Never edit or delete an applied migration — the runner refuses altered
history by checksum, which will block the next deploy.

Before calling the service production-ready, restore a recent managed backup to
an isolated database, run `/app/migrate`, start a temporary app against it, and
run `scripts/deploy-check.mjs`. Record the restore time and any manual steps.

## 12. Scale only after observation

After the one-machine release, smoke, dashboards, and restore drill are healthy,
scale to two machines in the same region:

```bash
fly scale count 2
fly status
```

Run the deploy gate again. Live rooms use owner leases and Fly replay routing.
The daily worker is safe on every machine: transitions are transactional and
idempotent, and an advisory lock means only one instance scans per tick.
Ungraceful live-room recovery can still wait up to `ROOM_LEASE_TTL`; this is not
a zero-downtime guarantee.

## Current Fly references

- [Create and deploy an app](https://fly.io/docs/launch/create/)
- [Deploy with `fly deploy`](https://fly.io/docs/launch/deploy/)
- [Set application secrets](https://fly.io/docs/apps/secrets/)
- [Scale the number of Machines](https://fly.io/docs/launch/scale-count/)
- [Managed Postgres](https://fly.io/docs/mpg/)
