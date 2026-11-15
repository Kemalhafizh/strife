# Run Registry (`strife serve`)

> **Language:** English · [Bahasa Indonesia](id/server.md)

`strife serve` runs a self-hosted HTTP daemon that stores single-seed reports (`report.json`) and multi-seed campaigns (`campaign.json`) in an append-only directory on your own network. On arrival, every report or campaign has its SHA-256 fingerprint recomputed, campaigns are automatically compared against the previous baseline for the same `(tenant, target)` pair, and reports can be signed automatically into Ed25519 `in-toto` v0.1 bundles.

The daemon is built into the `strife` binary with zero external dependencies and serves both a JSON REST API and an embedded dark-theme web dashboard.

> [!NOTE]
> `strife serve` speaks plaintext HTTP (default `127.0.0.1:8080`). When exposing it beyond loopback, place it behind a TLS-terminating reverse proxy and configure `--token` and/or `--tenant-tokens`.

## Starting the Server

```sh
# Listen on localhost with default store directory (./strife-registry)
strife serve

# Production / staging setup with admin + tenant Bearer auth, per-tenant Ed25519 keys, leases, SLA gates, and webhook alerts
strife keygen --out registry-key
strife keygen --out tenant-payments
strife serve \
  --listen 127.0.0.1:8080 \
  --store /var/lib/strife/registry \
  --token "env:STRIFE_REGISTRY_TOKEN" \
  --tenant-tokens "payments=env:STRIFE_PAYMENTS_TOKEN" \
  --job-lease 2m \
  --key registry-key.key \
  --tenant-keys "payments=tenant-payments.key" \
  --signer "staging-run-registry" \
  --max-mttr 500ms \
  --min-availability 0.99 \
  --webhook "https://hooks.slack.com/services/T00/B00/X00,https://discord.com/api/webhooks/123/abc"
```

### Flags

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--listen` | `127.0.0.1:8080` | TCP address for the HTTP server and web UI |
| `--store` | `strife-registry` | Directory for the append-only registry index and run artifacts |
| `--token` | *(empty)* | Optional admin Bearer token required across the web UI (`/`) and API endpoints (`/api/v1/runs*`, `/api/v1/series`, `/api/v1/trends`, `/api/v1/webhooks`, `/api/v1/jobs*`, `/api/v1/ingest`), compared in constant time (`env:VAR` or `file:PATH` supported) |
| `--tenant-tokens` | *(empty)* | Optional comma-separated `tenant=token` pairs for tenant-scoped Bearer authentication (`token` supports `env:VAR` or `file:PATH`; tokens must be unique and isolated per tenant with `403 Forbidden` on cross-tenant requests or `/api/v1/webhooks`) |
| `--job-lease` | `2m` | Lease duration before an unrenewed `claimed` worker job becomes eligible for reclaim |
| `--key` | *(empty)* | Optional default PKCS#8 Ed25519 private key (`.key`) used to sign ingested reports |
| `--tenant-keys` | *(empty)* | Optional comma-separated `tenant=path.key` pairs for per-tenant Ed25519 signing (`signer:<tenant>`) |
| `--signer` | `strife-serve` | Signer identity recorded inside automatic `in-toto` attestations |
| `--webhook` | *(empty)* | Comma-separated webhook URLs (Slack, Discord, or generic JSON) notified on violations, regressions, and SLA breaches |
| `--max-mttr` | `0s` | Optional post-heal MTTR P95 threshold (e.g. `500ms`) that triggers `report.sla_breach` webhook alerts |
| `--min-availability` | `0` | Optional minimum availability ratio in `[0,1]` or percentage in `(1,100]` (e.g. `0.99` or `99`) that triggers `report.sla_breach` webhook alerts |

> [!IMPORTANT]
> When either `--token` or `--tenant-tokens` is set, authentication is **fail-closed**: `/`, `/api/v1/runs*`, `/api/v1/series`, `/api/v1/trends`, `/api/v1/webhooks`, `/api/v1/jobs*`, and `/api/v1/ingest` all require `Authorization: Bearer <token>` (`401 Unauthorized` otherwise). Only `GET /api/v1/keys` (public verification keys) and a redacted `GET /api/v1/health` remain readable without a token.

## On-Disk Store Layout

The store directory is human-inspectable and backed by a newline-delimited JSON index (`registry.jsonl`) plus per-run artifact folders:

```text
strife-registry/
├── registry.jsonl                 # Append-only index of every ingested run and campaign
└── runs/
    ├── run-0001-a1b2c3d4/
    │   ├── report.json            # Verified single-seed report
    │   └── bundle.json            # Present when --key or --tenant-keys is configured (in-toto v0.1 Ed25519 bundle)
    └── camp-0002-e5f60718/
        └── campaign.json          # Multi-seed campaign manifest (with baseline Comparison attached)
```

When `strife serve` restarts, it replays `registry.jsonl` and reloads the latest campaign per `(tenant, target)` into memory so baseline regression tracking continues across restarts.

## HTTP API Reference

### `GET /api/v1/health`

Returns server status, engine version, uptime, signing status, configured SLA thresholds (when non-zero), webhook count, and whether authentication is enabled (`auth_enabled`). When authenticated (or when no tokens are configured), it also includes `stored_runs`, `targets`, and `tenants` scoped to the caller's identity; unauthenticated callers omit those run/target/tenant fields.

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" http://127.0.0.1:8080/api/v1/health
```

```json
{
  "auth_enabled": true,
  "key_id": "9f86d081884c7d65",
  "signing_active": true,
  "sla_max_mttr_ms": 500,
  "sla_min_availability": 0.99,
  "status": "ok",
  "stored_runs": 14,
  "targets": ["etcd-cluster", "raft", "raft-ledger"],
  "tenant_keys": 1,
  "tenants": ["default", "payments"],
  "uptime_seconds": 3620,
  "version": "1.0.0",
  "webhooks_configured": 2
}
```

### `GET /api/v1/keys`

Returns all active Ed25519 verification keys (`default` from `--key` plus any per-tenant keys configured via `--tenant-keys`) in PKIX PEM format (`public_key_pem`, `key_id`, `tenant`, `signer`) so auditors and CI pipelines can verify `bundle.json` artifacts independently.

```sh
curl -fsSL http://127.0.0.1:8080/api/v1/keys
```

### `GET /api/v1/webhooks`

Admin-only endpoint (requires the `--token` admin Bearer token when authentication is active; tenant-scoped tokens receive `403 Forbidden`). Returns the number of configured webhook endpoints and the recent delivery log (`run_id`, `event`, `format`, redacted `url`, `status_code`, `success`, `duration_ms`). Webhook tokens, query credentials, and Slack/Discord path secrets are redacted before returning.

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" http://127.0.0.1:8080/api/v1/webhooks
```

### `GET /api/v1/runs`

Lists indexed runs ordered newest-first (requires Bearer auth when `--token` or `--tenant-tokens` is enabled; tenant-scoped tokens are restricted to their own tenant and receive `403 Forbidden` if requesting another tenant). Supports optional query parameters:

- `tenant=<name>` — filter by tenant namespace (defaults to matching all tenants for admin tokens, or the token's tenant for tenant-scoped tokens).
- `target=<name>` — filter by system or cluster name (for example, `raft` or `postgres-ha`).
- `kind=<report|campaign>` — filter by artifact kind.

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/runs?tenant=payments&target=raft&kind=campaign"
```

### `GET /api/v1/runs/<id>`

Returns the indexed `entry` metadata together with the stored raw JSON artifacts (`report` or `campaign`, plus `bundle` when signed). Requires Bearer auth and matches caller tenant ownership (`403 Forbidden` across tenants).

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4
```

### `GET /api/v1/runs/<id>/dossier`

Evaluates and returns a regulatory compliance dossier (`dora`, `soc2`, `iso27001`, `bi-ojk`, `pci-dss`, `bi-sp`) for the stored run or campaign (requires Bearer auth and tenant ownership when auth is enabled). If the run was signed with Ed25519 (`bundle.json`), the signature is verified against the tenant's signing key (falling back to the default `--key`) and recorded in the dossier.

- `?frameworks=dora,bi-ojk,pci-dss,bi-sp` — filter evaluated frameworks (default `all`)
- `?format=html` — render the self-contained print-ready HTML5 compliance dossier instead of JSON (also selected automatically when `Accept: text/html` is sent)
- `?max_mttr=<duration>` — override the server's `--max-mttr` SLA gate for this evaluation (for example, `500ms`)
- `?min_availability=<value>` — override the server's `--min-availability` SLA gate using a ratio in `[0,1]` or percentage in `(1,100]` (for example, `0.99` or `99`)

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4/dossier?frameworks=dora,bi-ojk,pci-dss,bi-sp&max_mttr=500ms"
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4/dossier?format=html" -o dossier.html
```

### `GET /api/v1/series` & `GET /api/v1/trends`

`GET /api/v1/series` returns aggregated time-series reliability and post-heal MTTR rollups per `(tenant, target)` across all stored runs and campaigns (`pass_rate`, `avg_availability`, `latest_mttr_p95_ms`, `max_mttr_p95_ms`, and chronological `points`).

`GET /api/v1/trends` returns `{target, tenant?, total, points}` with chronological (oldest-first) resilience trend points (`id`, `tenant`, `kind`, `target`, `model`, `commit`, `branch`, `timestamp`, `verdict`, `passed`, `violations`, `regressions`, `overall_availability`, `fault_availability`, `mttr_p95_ms`, `mttr_max_ms`). Both endpoints enforce Bearer authentication and per-tenant filtering (`?tenant=<name>` and `?target=<name>`).

```sh
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/series?tenant=default"
curl -fsSL -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" "http://127.0.0.1:8080/api/v1/trends?tenant=payments&target=outbox-settlement"
```

### Pull-Based Remote Worker Queue (`/api/v1/jobs` & `strife worker`)

For private VPCs and isolated staging networks that cannot expose inbound ports to the control plane, `strife serve` provides a pull-based job queue persisted atomically to `jobs.json` (sync-written via a temporary file and rename, mode `0600`):

- **`POST /api/v1/jobs`** — Enqueue a `sim`, `campaign`, or `run` job (`kind`, `system`, `seed` / `seeds`, `nodes`, `clients`, `duration`, `faults`, or `live_config`, plus optional `tenant`, `commit`, `branch`; capped at 4 MiB). Enforces caller tenant scope (`403 Forbidden` if a tenant token targets another tenant).
- **`GET /api/v1/jobs`** & **`GET /api/v1/jobs/<id>`** — List or inspect queued, claimed, completed, or failed jobs (filterable by `?tenant=` and `?status=`). Requires Bearer authentication when `--token` or `--tenant-tokens` is enabled, filters results to the caller's tenant (`403 Forbidden` across tenants), and strips secret `claim_token` values from responses.
- **`POST /api/v1/jobs/claim`** — Atomically claim the oldest `pending` job—or an active `claimed` job whose `lease_expires_at` deadline has elapsed—(optionally filtered by `tenant`) for a remote `worker_id`. Returns an opaque per-claim `claim_token` alongside `lease_expires_at` (`--job-lease`, default `2m`) and increments `attempts`.
- **`POST /api/v1/jobs/<id>/heartbeat`** — Renew a claimed job's lease by sending `{"worker_id":"...","claim_token":"..."}` (`409 Conflict` if the lease already expired, the job is no longer `claimed`, completion is already in progress, or `worker_id`/`claim_token` do not match). `strife worker` automatically sends periodic background heartbeats at ~1/3 of the lease duration (clamped to `[25ms, 30s]`) while executing a job.
- **`POST /api/v1/jobs/<id>/complete`** — Submit the completed `report` or `campaign` together with `worker_id` and `claim_token` (or record an execution `error`). The server guards concurrent completions (`409 Conflict` while already completing or already finished), validates `worker_id`/`claim_token` (`409 Conflict` on mismatch), rejects completions whose lease has already expired (`409 Conflict`, even before another worker reclaims the job), verifies fingerprints, deduplicates retries by `job_id` in `registry.jsonl` so failed `jobs.json` persists cannot create duplicate runs or webhooks, signs with the tenant's Ed25519 key, persists `jobs.json` atomically before dispatching SLA/regression webhooks, and reconciles any committed `job_id` run from `registry.jsonl` into `jobs.json` on startup.

```sh
# 1. Enqueue a simulation or live database chaos job
curl -fsSL -X POST http://127.0.0.1:8080/api/v1/jobs \
  -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"tenant":"payments","commit":"a35dc51","kind":"sim","system":"outbox-settlement","seed":7}'

# 2. Run a remote worker inside the private staging VPC (automatically sends lease heartbeats; supports --id/--worker-id, --tenant, --poll-interval/--poll, --once)
strife worker --server http://127.0.0.1:8080 --token "$STRIFE_PAYMENTS_TOKEN" --id staging-vpc-worker-1 --tenant payments
```

### `POST /api/v1/ingest`

Ingests a report or campaign into the registry. If `--token` is set on the server, the request must include `Authorization: Bearer <token>`.

The request body (capped at 32 MiB) can be:

1. **A raw `report.json`** produced by `strife sim`, `strife run`, `strife check`, or `strife shrink`. Optional `tenant`, `commit`, `branch`, and `idempotency_key` metadata can be passed in the query string (`/api/v1/ingest?tenant=payments&commit=abc1234&branch=main`), or via the `Idempotency-Key` HTTP header.
2. **A raw `campaign.json`** produced by `strife campaign`.
3. **An envelope object** combining tenant and git metadata with either payload (envelope values take precedence over query parameters):

   ```json
   {
     "tenant": "payments",
     "commit": "714fbd6",
     "branch": "main",
     "idempotency_key": "ci-run-42",
     "report": { ... }
   }
   ```

#### What happens on ingest

- **Fingerprint integrity check:** `strife serve` recomputes `report.Fingerprint` or `campaign.ComputeFingerprint()`. If the payload carries a fingerprint that does not match its contents, ingestion is rejected with `400 Bad Request`.
- **Idempotent replay protection (`Idempotency-Key`):** When `Idempotency-Key` (header, `?idempotency_key=`, or envelope `idempotency_key`) is supplied, deduplication is scoped per normalized tenant across server restarts. Repeating the same artifact returns `200 OK` with `"deduplicated": true` and the original registry entry without duplicating runs or re-firing webhooks; reusing the same key for a different artifact fails with `409 Conflict`.
- **Automatic Ed25519 signing (reports):** When `--tenant-keys` matches the normalized tenant (or `--key` is configured), `strife serve` signs the verified report into an `in-toto` v0.1 attestation bundle (`bundle.json`), records `signed: true`, `signer`, and `key_id` in the registry entry, and returns the `bundle` in the `201 Created` response.
- **Automatic baseline regression diffing (campaigns):** When a campaign is ingested for `(tenant, target)`, `strife serve` compares it against the most recent stored campaign for the same tenant and target using `campaign.Compare`, recording `regressions`, `fixes`, and `trace_drifts` on the registry entry and returning the full `comparison` object in the response.
- **Automatic webhook alerting (`--webhook`):** `strife serve` dispatches HTTP POST notifications to all configured webhook URLs when:
  - an ingested report violates consistency (`report.violation`),
  - an ingested report breaches `--max-mttr` or `--min-availability` (`report.sla_breach`), or
  - an ingested campaign introduces regressions (`campaign.regression`) or violations (`campaign.violation`).
  Format auto-detection supports Slack (`hooks.slack.com` or `?format=slack`), Discord (`discord.com/api/webhooks` or `?format=discord`), and generic JSON endpoints.

## Publishing from CI

`strife sim` (single `--seed`), `strife campaign`, `strife run`, and `strife check` all support pushing results directly with `--push`, `--push-token` (supporting `env:VAR` or `file:PATH`), and `--tenant`:

```sh
strife check --model settlement --history history.jsonl \
  --push http://strife-registry.internal:8080 \
  --push-token "$STRIFE_REGISTRY_TOKEN" \
  --tenant payments

strife campaign --system raft --seeds 1..100 \
  --commit "$(git rev-parse --short HEAD)" --branch "$GITHUB_REF_NAME" \
  --push http://strife-registry.internal:8080 \
  --push-token "$STRIFE_REGISTRY_TOKEN" \
  --tenant payments
```

For multi-seed `sim --seeds` sweeps or previously saved `report.json` / `campaign.json` artifacts, post the JSON directly to `/api/v1/ingest`:

```sh
# Ingest a single-seed report with tenant and git metadata
curl -fsSL -X POST \
  -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @strife-out/raft-seed7/report.json \
  "http://strife-registry.internal:8080/api/v1/ingest?tenant=payments&commit=$(git rev-parse --short HEAD)&branch=$GITHUB_REF_NAME"

# Ingest a multi-seed regression campaign
curl -fsSL -X POST \
  -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @strife-campaign/campaign.json \
  "http://strife-registry.internal:8080/api/v1/ingest?tenant=payments"
```

## Embedded Web Dashboard

Opening `http://<listen-address>/` in a browser renders a self-contained dark-theme dashboard with:

- **KPI summary cards:** stored runs and campaigns, linearizable vs violating counts, seed regressions detected, and Ed25519 signed artifacts.
- **Interactive filter pills:** filter by target system (`/?target=...`) or tenant namespace (`/?tenant=...`).
- **Target & Tenant Time-Series SLA Overview:** per-`(tenant, target)` rollups showing total runs, pass/fail/regression counts, pass rate, average availability, latest P95 MTTR, and peak P95 MTTR.
- **Run & Artifact table:** per-run links to the raw JSON payload (`/api/v1/runs/<id>`) and the on-demand HTML compliance dossier (`/api/v1/runs/<id>/dossier?format=html`), together with availability, MTTR P95, baseline delta, SHA-256 fingerprint, and signing key ID.
