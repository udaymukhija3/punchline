# Punchline Deployment Guide

## Deployment status

The repository is production-shaped for one region on Fly.io: one container
serves the React app, REST API, WebSockets, health/readiness, metrics, and the
daily worker. Managed Postgres is required in production. The release command
runs forward-only, checksum-guarded migrations before a new app image starts.

Deployment is `READY_WITH_MANUAL_STEPS`: the code and automated gates are in
place, while database provisioning/backups, secrets, DNS, dashboards, alerts,
and a restore drill must be completed by the operator.

## 1. Verify locally

For a disposable local database created by an older revision that mounted SQL
files directly, remove that local volume once before using the current Compose
flow. This deletes only the local Compose database:

```bash
docker compose down -v
```

Build the production image, start Postgres, run the release migration, and
start the app:

```bash
docker compose up --build -d app
docker compose ps
node scripts/deploy-check.mjs http://127.0.0.1:8080
```

The deploy check validates the built React assets, readiness, metrics, a full
three-player live WebSocket round, and the daily create/join/submit/privacy
flow. The temporary daily group is deleted during cleanup.

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

For Fly Managed Postgres, create and attach the cluster interactively (review
the selected paid plan before confirming):

```bash
fly mpg create --name punchline-db --region iad
fly mpg list
fly mpg attach <cluster-id> --app <your-app-name>
```

The attach command creates the app's `DATABASE_URL` secret. With another
provider, use its TLS connection string in the secret step below.

```txt
machine count × DB_MAX_OPEN_CONNS + migration/administrative connections
```

The default app pool cap is 10 connections per machine. Do not add replicas,
Redis, or a queue for this release.

## 4. Set production secrets

Generate the metrics token with a password manager or cryptographic secret
generator; do not commit it or put it in shell history on a shared machine.

```bash
fly secrets set \
  PUNCHLINE_ALLOWED_ORIGINS='https://play.example.com' \
  PUNCHLINE_METRICS_TOKEN='replace-with-random-secret'
```

If Managed Postgres was not attached with `fly mpg attach`, include
`DATABASE_URL='postgres://...TLS-connection-string...'` in that command.

`PUNCHLINE_REQUIRE_DATABASE=true` is already set in `fly.toml`, so both release
migrations and app startup fail closed if the database secret is missing.

Decide how the Fly edge supplies client IPs. Prefer an explicit
`PUNCHLINE_TRUSTED_PROXY_CIDRS` value verified against the current platform
network. Set `PUNCHLINE_TRUST_PROXY_HEADERS=true` only if the immediate edge is
known to overwrite spoofed forwarded headers and no stable CIDR list exists.

## 5. Preflight the release

Run the exact repository gates before pushing:

```bash
cd backend
go vet ./...
go test ./... -timeout 30s
go test -race ./...

cd ../frontend
npm ci
npm run build

cd ..
node --check scripts/deploy-check.mjs
node --check scripts/smoke-realtime.mjs
node --check scripts/smoke-daily.mjs
docker build -t punchline-production-check .
git diff --check
```

Confirm CI's Postgres job passed the daily concurrency integration test and
both smoke scripts.

## 6. Deploy one machine

Deploy the image. Fly runs `/app/migrate` as the release command; the migration
runner serializes concurrent releases with a Postgres advisory lock and refuses
edited migration history.

```bash
fly deploy
fly status
fly scale count 1
```

If the release migration fails, stop. Do not bypass it and do not automatically
roll the database backward. Fix forward or restore in a controlled window.

## 7. Run the post-deploy gate

```bash
PUNCHLINE_METRICS_TOKEN='same-token-as-the-secret' \
  node scripts/deploy-check.mjs https://your-app.fly.dev
```

Then manually verify from two browsers or phones:

1. Open `/`, create a live room, join three players, and finish a round.
2. Open `/daily`, create a group, join from the invite link in another browser,
   submit both answers, and confirm that neither browser can see the other's
   answer before reveal.
3. Confirm `/readyz` returns `200` and the metrics scrape succeeds with the
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
- platform bandwidth, Postgres storage, and backup failures.

## 10. Rehearse rollback and restore

For an application rollback, select the previous Fly release/image only when it
is compatible with the additive database migration, then rerun the deploy
check. Never edit or delete an applied migration.

Before calling the service production-ready, restore a recent managed backup to
an isolated database, run `/app/migrate`, start a temporary app against it, and
run `scripts/deploy-check.mjs`. Record the restore time and any manual steps.

## 11. Scale only after observation

After the one-machine release, smoke, dashboards, and restore drill are healthy,
scale to two machines in the same region:

```bash
fly scale count 2
fly status
```

Run the deploy gate again. Live rooms use owner leases and Fly replay routing;
the daily worker is safe on every machine because its transitions are
transactional and idempotent. Ungraceful live-room recovery can still wait up
to `ROOM_LEASE_TTL`; this is not a zero-downtime guarantee.

## Current Fly references

- [Create and deploy an app](https://fly.io/docs/launch/create/)
- [Deploy with `fly deploy`](https://fly.io/docs/launch/deploy/)
- [Set application secrets](https://fly.io/docs/apps/secrets/)
- [Scale the number of Machines](https://fly.io/docs/launch/scale-count/)
- [Managed Postgres](https://fly.io/docs/mpg/)
