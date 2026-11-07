# Run Registry (`strife serve`)

`strife serve` runs a self-hosted HTTP daemon that stores single-seed reports (`report.json`) and multi-seed campaigns (`campaign.json`) in an append-only directory on your own network. On arrival, every report or campaign has its SHA-256 fingerprint recomputed, campaigns are automatically compared against the previous baseline for the same target system, and reports can be signed automatically into Ed25519 `in-toto` v0.1 bundles.

The daemon is built into the `strife` binary with zero external dependencies and serves both a JSON REST API and an embedded dark-theme web dashboard.

## Starting the Server

```sh
# Listen on localhost with default store directory (./strife-registry)
strife serve

# Production / staging setup with Bearer token auth and automatic Ed25519 signing
strife keygen --out registry-key
strife serve \
  --listen 0.0.0.0:8080 \
  --store /var/lib/strife/registry \
  --token "$STRIFE_REGISTRY_TOKEN" \
  --key registry-key.key \
  --signer "staging-run-registry"
```

### Flags

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--listen` | `127.0.0.1:8080` | TCP address for the HTTP server and web UI |
| `--store` | `strife-registry` | Directory for the append-only registry index and run artifacts |
| `--token` | *(empty)* | Optional Bearer token required for `POST /api/v1/ingest` (compared in constant time) |
| `--key` | *(empty)* | Optional PKCS#8 Ed25519 private key (`.key`) used to sign every ingested `report.json` |
| `--signer` | `strife-serve` | Signer identity recorded inside automatic `in-toto` attestations |

## On-Disk Store Layout

The store directory is human-inspectable and backed by a newline-delimited JSON index (`registry.jsonl`) plus per-run artifact folders:

```text
strife-registry/
├── registry.jsonl                 # Append-only index of every ingested run and campaign
└── runs/
    ├── run-0001-a1b2c3d4/
    │   ├── report.json            # Verified single-seed report
    │   └── bundle.json            # Present when --key is configured (in-toto v0.1 Ed25519 bundle)
    └── camp-0002-e5f60718/
        └── campaign.json          # Multi-seed campaign manifest (with baseline Comparison attached)
```

When `strife serve` restarts, it replays `registry.jsonl` and reloads the latest campaign per target system into memory so baseline regression tracking continues across restarts.

## HTTP API Reference

### `GET /api/v1/health`

Returns server status, engine version, uptime, total stored runs, known target systems, and active signing key metadata.

```sh
curl -fsSL http://127.0.0.1:8080/api/v1/health
```

```json
{
  "key_id": "9f86d081884c7d65",
  "signing_active": true,
  "status": "ok",
  "stored_runs": 14,
  "targets": ["etcd-cluster", "raft", "raft-ledger"],
  "uptime_seconds": 3620,
  "version": "0.2.0"
}
```

### `GET /api/v1/runs`

Lists indexed runs ordered newest-first. Supports optional query parameters:

- `target=<name>` — filter by system or cluster name (for example, `raft` or `postgres-ha`).
- `kind=<report|campaign>` — filter by artifact kind.

```sh
curl -fsSL "http://127.0.0.1:8080/api/v1/runs?target=raft&kind=campaign"
```

### `GET /api/v1/runs/<id>`

Returns the indexed `entry` metadata together with the stored raw JSON artifacts (`report` or `campaign`, plus `bundle` when signed).

```sh
curl -fsSL http://127.0.0.1:8080/api/v1/runs/run-0001-a1b2c3d4
```

### `POST /api/v1/ingest`

Ingests a report or campaign into the registry. If `--token` is set on the server, the request must include `Authorization: Bearer <token>`.

The request body (up to 32 MiB) can be:

1. **A raw `report.json`** produced by `strife sim`, `strife run`, `strife check`, or `strife shrink`. Optional `commit` and `branch` metadata can be passed in the query string (`/api/v1/ingest?commit=abc1234&branch=main`).
2. **A raw `campaign.json`** produced by `strife campaign`.
3. **An envelope object** combining git metadata with either payload:
   ```json
   {
     "commit": "714fbd6",
     "branch": "main",
     "report": { ... }
   }
   ```

#### What happens on ingest

- **Fingerprint integrity check:** `strife serve` recomputes `report.Fingerprint` or `campaign.ComputeFingerprint()`. If the payload carries a fingerprint that does not match its contents, ingestion is rejected with `400 Bad Request`.
- **Automatic Ed25519 signing (reports):** When `--key` is configured, `strife serve` signs the verified report into an `in-toto` v0.1 attestation bundle (`bundle.json`), records `signed: true`, `signer`, and `key_id` in the registry entry, and returns the `bundle` in the `201 Created` response.
- **Automatic baseline regression diffing (campaigns):** When a campaign is ingested for target system `S`, `strife serve` compares it against the most recent stored campaign for `S` using `campaign.Compare`, recording `regressions`, `fixes`, and `trace_drifts` on the registry entry and returning the full `comparison` object in the response.

## Publishing from CI

After running a simulation, live test, or campaign in CI, push the JSON artifact to your registry with `curl`:

```sh
# Ingest a single-seed report with git metadata
curl -fsSL -X POST \
  -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @strife-out/raft-seed7/report.json \
  "http://strife-registry.internal:8080/api/v1/ingest?commit=$(git rev-parse --short HEAD)&branch=$GITHUB_REF_NAME"

# Ingest a multi-seed regression campaign
curl -fsSL -X POST \
  -H "Authorization: Bearer $STRIFE_REGISTRY_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @strife-campaign/campaign.json \
  "http://strife-registry.internal:8080/api/v1/ingest"
```

## Embedded Web Dashboard

Opening `http://<listen-address>/` in a browser renders a self-contained dashboard with:

- **KPI summary cards:** total stored runs, passing runs, violating runs, active regressions, and cryptographic signing status.
- **Interactive filtering:** filter by target system, artifact kind (`report` vs `campaign`), or free-text search across run IDs, targets, commits, branches, and SHA-256 fingerprints.
- **Artifact inspection:** click any run row to inspect its full `entry`, `report` / `campaign`, and `in-toto` signature `bundle` JSON.
