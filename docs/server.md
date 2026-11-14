# Run Registry (`strife serve`)

`strife serve` runs a self-hosted HTTP daemon that stores single-seed reports (`report.json`) and multi-seed campaigns (`campaign.json`) in an append-only directory on your own network. On arrival, every report or campaign has its SHA-256 fingerprint recomputed, campaigns are automatically compared against the previous baseline for the same `(tenant, target)` pair, and reports can be signed automatically into Ed25519 `in-toto` v0.1 bundles.

The daemon is built into the `strife` binary with zero external dependencies and serves both a JSON REST API and an embedded dark-theme web dashboard.

> [!NOTE]
> `strife serve` speaks plaintext HTTP (default `127.0.0.1:8080`). When exposing it beyond loopback, place it behind a TLS-terminating reverse proxy and enable `--token`.

## Starting the Server

```sh
# Listen on localhost with default store directory (./strife-registry)
strife serve

# Production / staging setup with Bearer token auth, per-tenant Ed25519 keys, SLA gates, and webhook alerts
strife keygen --out registry-key
strife keygen --out tenant-payments
strife serve \
  --listen 127.0.0.1:8080 \
  --store /var/lib/strife/registry \
  --token "$STRIFE_REGISTRY_TOKEN" \
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
| `--token` | *(empty)* | Optional Bearer token required for `POST /api/v1/ingest` (compared in constant time) |
| `--key` | *(empty)* | Optional default PKCS#8 Ed25519 private key (`.key`) used to sign ingested reports |
| `--tenant-keys` | *(empty)* | Optional comma-separated `tenant=path.key` pairs for per-tenant Ed25519 signing (`signer:<tenant>`) |
| `--signer` | `strife-serve` | Signer identity recorded inside automatic `in-toto` attestations |
| `--webhook` | *(empty)* | Comma-separated webhook URLs (Slack, Discord, or generic JSON) notified on violations, regressions, and SLA breaches |
| `--max-mttr` | `0s` | Optional post-heal MTTR P95 threshold (e.g. `500ms`) that triggers `report.sla_breach` webhook alerts |
| `--min-availability` | `0` | Optional minimum availability ratio in `[0,1]` (e.g. `0.99`) that triggers `report.sla_breach` webhook alerts |

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

Returns server status, engine version, uptime, total stored runs, known targets and tenants, signing status, configured SLA thresholds (when non-zero), and webhook count.

```sh
curl -fsSL http://127.0.0.1:8080/api/v1/health
```

```json
{
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

Returns the number of configured webhook endpoints and the recent delivery log (`run_id`, `event`, `format`, redacted `url`, `status_code`, `success`, `duration_ms`). Webhook tokens, query credentials, and Slack/Discord path secrets are redacted before returning.

```sh
curl -fsSL http://127.0.0.1:8080/api/v1/webhooks
```

### `GET /api/v1/runs`

Lists indexed runs ordered newest-first. Supports optional query parameters:

- `tenant=<name>` — filter by tenant namespace (defaults to matching all tenants when omitted).
- `target=<name>` — filter by system or cluster name (for example, `raft` or `postgres-ha`).
- `kind=<report|campaign>` — filter by artifact kind.

```sh
curl -fsSL "http://127.0.0.1:8080/api/v1/runs?tenant=payments&target=raft&kind=campaign"
```

### `GET /api/v1/runs/<id>`

Returns the indexed `entry` metadata together with the stored raw JSON artifacts (`report` or `campaign`, plus `bundle` when signed).

```sh
curl -fsSL http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4
```

### `GET /api/v1/runs/<id>/dossier`

Evaluates and returns a regulatory compliance dossier (`dora`, `soc2`, `iso27001`, `bi-ojk`, `pci-dss`, `bi-sp`) for the stored run or campaign. If the run was signed with Ed25519 (`bundle.json`), the signature is verified against the tenant's signing key (falling back to the default `--key`) and recorded in the dossier.

- `?frameworks=dora,bi-ojk,pci-dss,bi-sp` — filter evaluated frameworks (default `all`)
- `?format=html` — render the self-contained print-ready HTML5 compliance dossier instead of JSON (also selected automatically when `Accept: text/html` is sent)
- `?max_mttr=<duration>` — override the server's `--max-mttr` SLA gate for this evaluation (for example, `500ms`)
- `?min_availability=<ratio>` — override the server's `--min-availability` SLA gate in `[0,1]` (for example, `0.99`)

```sh
curl -fsSL "http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4/dossier?frameworks=dora,bi-ojk,pci-dss,bi-sp&max_mttr=500ms"
curl -fsSL "http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4/dossier?format=html" -o dossier.html
```

### `GET /api/v1/series` & `GET /api/v1/trends`

`GET /api/v1/series` returns aggregated time-series reliability and post-heal MTTR rollups per `(tenant, target)` across all stored runs and campaigns (`pass_rate`, `avg_availability`, `latest_mttr_p95_ms`, `max_mttr_p95_ms`, and chronological `points`).

`GET /api/v1/trends` returns chronological resilience trend points (`run_id`, `target`, `tenant`, `commit`, `verdict`, `overall_availability`, `mttr_p50_ms`, `mttr_p95_ms`, `mttr_max_ms`, `sla_breached`) together with aggregate averages (`avg_availability`, `avg_mttr_p95_ms`, `max_mttr_p95_ms`, `sla_breaches`). Supports `?tenant=<name>` and `?target=<name>` filters.

```sh
curl -fsSL "http://127.0.0.1:8080/api/v1/series?tenant=default"
curl -fsSL "http://127.0.0.1:8080/api/v1/trends?tenant=payments&target=outbox-settlement"
```

### Pull-Based Remote Worker Queue (`/api/v1/jobs` & `strife worker`)

For private VPCs and isolated staging networks that cannot expose inbound ports to the control plane, `strife serve` provides a pull-based job queue persisted in `jobs.json`:

- **`POST /api/v1/jobs`** — Enqueue a `sim`, `campaign`, or `run` job (`spec.kind`, `spec.system`, `spec.seed` / `spec.seeds`, `spec.faults`, `spec.workload`, or `spec.run_config`, plus optional `tenant`, `commit`, `branch`).
- **`GET /api/v1/jobs`** & **`GET /api/v1/jobs/<id>`** — List or inspect queued, running, completed, or failed jobs (filterable by `?tenant=` and `?status=`).
- **`POST /api/v1/jobs/claim`** — Atomically claim the oldest `pending` job (optionally filtered by `tenant`) for a remote `worker_id`.
- **`POST /api/v1/jobs/<id>/complete`** — Submit the completed `report` or `campaign` (automatically verified, ingested into `registry.jsonl`, signed with Ed25519, and checked against SLA/regression webhooks) or record an execution error.

```sh
# 1. Enqueue a simulation or live database chaos job
curl -fsSL -X POST http://127.0.0.1:8080/api/v1/jobs \
  -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"tenant":"payments","commit":"a35dc51","spec":{"kind":"sim","system":"outbox-settlement","seed":7}}'

# 2. Run a remote worker inside the private staging VPC (continuous polling or single-shot --once)
strife worker --server http://127.0.0.1:8080 --token "$STRIFE_REGISTRY_TOKEN" --tenant payments
```

### `POST /api/v1/ingest`

Ingests a report or campaign into the registry. If `--token` is set on the server, the request must include `Authorization: Bearer <token>`.

The request body (capped at 32 MiB) can be:

1. **A raw `report.json`** produced by `strife sim`, `strife run`, `strife check`, or `strife shrink`. Optional `tenant`, `commit`, and `branch` metadata can be passed in the query string (`/api/v1/ingest?tenant=payments&commit=abc1234&branch=main`).
2. **A raw `campaign.json`** produced by `strife campaign`.
3. **An envelope object** combining tenant and git metadata with either payload (envelope values take precedence over query parameters):

   ```json
   {
     "tenant": "payments",
     "commit": "714fbd6",
     "branch": "main",
     "report": { ... }
   }
   ```

#### What happens on ingest

- **Fingerprint integrity check:** `strife serve` recomputes `report.Fingerprint` or `campaign.ComputeFingerprint()`. If the payload carries a fingerprint that does not match its contents, ingestion is rejected with `400 Bad Request`.
- **Automatic Ed25519 signing (reports):** When `--tenant-keys` matches the normalized tenant (or `--key` is configured), `strife serve` signs the verified report into an `in-toto` v0.1 attestation bundle (`bundle.json`), records `signed: true`, `signer`, and `key_id` in the registry entry, and returns the `bundle` in the `201 Created` response.
- **Automatic baseline regression diffing (campaigns):** When a campaign is ingested for `(tenant, target)`, `strife serve` compares it against the most recent stored campaign for the same tenant and target using `campaign.Compare`, recording `regressions`, `fixes`, and `trace_drifts` on the registry entry and returning the full `comparison` object in the response.
- **Automatic webhook alerting (`--webhook`):** `strife serve` dispatches HTTP POST notifications to all configured webhook URLs when:
  - an ingested report violates consistency (`report.violation`),
  - an ingested report breaches `--max-mttr` or `--min-availability` (`report.sla_breach`), or
  - an ingested campaign introduces regressions (`campaign.regression`) or violations (`campaign.violation`).
  Format auto-detection supports Slack (`hooks.slack.com` or `?format=slack`), Discord (`discord.com/api/webhooks` or `?format=discord`), and generic JSON endpoints.

## Publishing from CI

`strife check` supports pushing reports directly with `--push`, `--push-token`, and `--tenant`:

```sh
strife check --model settlement --history history.jsonl \
  --push http://strife-registry.internal:8080 \
  --push-token "$STRIFE_REGISTRY_TOKEN" \
  --tenant payments
```

For `sim`, `campaign`, and `run` outputs, post `report.json` or `campaign.json` to `/api/v1/ingest`:

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
