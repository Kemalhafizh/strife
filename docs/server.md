# strife server: the self-hosted run registry

`strife server` keeps every Strife run your CI produces, in your own network:

- **verifies** what it receives: the report's fingerprint must match its
  content, the history must hash to what the report says, and a signature
  must come from a key the project trusts. A run that does not verify is
  refused, never stored with a warning;
- **compares** each run with the previous run of its series (same kind,
  target and fault kinds) and records a `regression` or a `fix`; campaigns
  are compared seed by seed;
- **alerts** a webhook (Slack-compatible) on regressions and fixes;
- **serves** a dashboard, the original artifacts, the interactive report
  timeline, and compliance dossiers generated on demand with the signature
  re-verified.

It is one binary with no database: runs live in a directory, one
sub-directory per run, written atomically.

## Run it

```sh
export STRIFE_PAYMENTS_TOKEN=$(openssl rand -hex 24)
strife server --config server.json                      # http://:8080
strife server --config server.json --tls-cert c.pem --tls-key k.pem
docker run -p 8080:8080 -v $PWD:/etc/strife -v strife-data:/var/lib/strife \
  ghcr.io/kemalhafizh/strife:latest server --config /etc/strife/server.json
```

`server.json` ([example](../examples/server/server.json)):

| Field | Meaning |
|---|---|
| `listen` | address, default `:8080` |
| `data` | data directory, default `./strife-data` |
| `public_url` | how users reach the server, for links in webhooks and push results |
| `public_read` | let anyone read every project without a token (trusted networks only) |
| `max_upload` | largest accepted push in bytes, default 256 MiB |
| `projects[].name` | lowercase letters, digits, `-` and `_` |
| `projects[].token` | write token (push and read), at least 16 characters; `env:NAME` or `file:PATH`. Optional when an `oidc` rule grants write access |
| `projects[].read_token` | read-only token, for dashboards and auditors |
| `projects[].oidc` | CI jobs admitted by their OIDC identity token instead of a stored token ([below](#push-without-a-stored-token-oidc)) |
| `projects[].trusted_keys` | Ed25519 public keys from `strife keygen` whose signatures make runs *attested* |
| `projects[].require_signature` | refuse runs that are not signed by a trusted key |
| `projects[].webhook` | URL that receives `{"text", "run", "url"}` on regressions and fixes |
| `projects[].retain` | keep at most this many runs; the oldest are deleted first |

Tokens are compared in constant time and never logged. Put the server
behind TLS (directly or through your proxy): tokens travel in the
`Authorization` header.

## Push from CI

```sh
strife sign --report strife-out/run/report.json --key ci.key --signer ci --out strife-out/run/bundle.json
STRIFE_TOKEN=... strife push --server https://strife.internal --project payments strife-out/run

strife sign --campaign strife-campaign/campaign.json --key env:STRIFE_SIGNING_KEY --signer ci
STRIFE_TOKEN=... strife push --server https://strife.internal --project payments strife-campaign/campaign.json
```

`strife push` sends `report.json` with `history.jsonl` and `bundle.json`
when they are next to it, or a `campaign.json` with `campaign-bundle.json`
when it is next to it. A campaign's signature covers every seed's verdict
and hashes through the campaign fingerprint; the server accepts it only if
a trusted key signed that very campaign (its fingerprint, system and
verdict), and `require_signature` applies to campaigns as to reports. Commit, branch and a link to
the CI job are taken from the usual CI variables (`GITHUB_SHA`,
`GITHUB_REF_NAME`, `CI_COMMIT_SHA`, …) unless given with `--commit`,
`--branch`, `--source`. It exits 1 when the server reports a regression
(`--fail-on regression`, the default), or on any violation
(`--fail-on violation`), or never (`--fail-on none`).

With the GitHub Action:

```yaml
- uses: kemalhafizh/strife@v0.2.0
  env:
    STRIFE_TOKEN: ${{ secrets.STRIFE_TOKEN }}
    STRIFE_SIGNING_KEY: ${{ secrets.STRIFE_SIGNING_KEY }} # the PEM from strife keygen
  with:
    command: campaign --system raft --seeds 1..200
    push-server: https://strife.internal
    push-project: payments
    sign-key-env: STRIFE_SIGNING_KEY
```

With `sign-key-env`, the Action signs the reports (or the campaign) before
it uploads and pushes them, as `github-actions:<owner>/<repo>`.

### Push without a stored token (OIDC)

A CI job can prove who it is with the short-lived OIDC token its platform
mints for it, so no write token is stored in CI at all. The project names
the issuer, the audience the token must be minted for, and the claims that
identify the jobs it admits:

```json
{
  "name": "payments",
  "read_token": "env:PAYMENTS_READ_TOKEN",
  "oidc": [{
    "issuer": "https://token.actions.githubusercontent.com",
    "audience": "strife",
    "claims": { "repository_id": "123456789", "ref": "refs/heads/main" }
  }]
}
```

| Rule field | Meaning |
|---|---|
| `issuer` | the token's `iss`, an `https://` URL; its keys are found through `/.well-known/openid-configuration` |
| `audience` | the token's `aud` must contain it |
| `claims` | claim name to a [`path.Match`](https://pkg.go.dev/path#Match) pattern; all must match. At least one is required |
| `access` | `write` (push and read, the default) or `read` |
| `jwks` | the issuer's keys from an `https://` URL or `file:PATH` instead of discovery, for a registry that cannot reach the issuer |

Patterns match whole values, and `*` stops at `/`: `refs/heads/*` admits
`refs/heads/main` but not `refs/heads/feature/x`. Prefer GitHub's
`repository_id` and `repository_owner_id` to `repository` or `sub`: the IDs
survive a rename and cannot be taken over by whoever registers a deleted
name, and `sub` changes shape when an owner opts into immutable identifiers
(`repo:acme@1234/payments@5678:ref:…`).
Tokens must be signed with RS256 or ES256 by a key the issuer publishes,
be unexpired (a minute of clock skew is allowed) and be minted for the
audience. The key set is cached and fetched again for a key it does not
know, at most once a minute. GitLab CI (`https://gitlab.com`, its
`project_id` and `ref` claims) and other OIDC issuers work the same way.

In GitHub Actions, give the job `id-token: write` and name the audience:

```yaml
permissions:
  contents: read
  id-token: write
steps:
  - uses: kemalhafizh/strife@v0.2.0
    with:
      command: campaign --system raft --seeds 1..200
      push-server: https://strife.internal
      push-project: payments
      push-oidc-audience: strife
```

Outside the Action, `strife push --token github-oidc:strife` asks GitHub for
the token. Each run records who pushed it (`pusher`): `token`, or the
token's subject and issuer, e.g.
`repo:acme/payments:ref:refs/heads/main (token.actions.githubusercontent.com)`.

## Read

The dashboard is at `/` (projects you can read) and `/ui/<project>`; each
series has a trend page (`/ui/<project>/series?name=<series>`) charting its
latest 60 runs: the verdict of each run, violating seeds per campaign and
operations per run, with every run in a table. The charts are SVG drawn on
the server, so the dashboard still runs no script; a verdict reads from the
column's height as well as its color. Browsers
sign in with HTTP Basic auth: any user name, the read or write token as the
password. The API takes `Authorization: Bearer <token>`:

| Endpoint | Returns |
|---|---|
| `GET /api/v1/projects` | the projects your token can read, with run, series and failure counts |
| `POST /api/v1/projects/{p}/runs` | push (write token or OIDC identity); `201` stored, `200` already stored, `422` did not verify |
| `GET /api/v1/projects/{p}/runs` | runs, newest first; filters `series`, `target`, `kind`, `verdict`, `change`, `branch`, `limit` |
| `GET /api/v1/projects/{p}/series` | each series with its latest run, recent verdicts and how long it has been failing |
| `GET /api/v1/projects/{p}/runs/{id}` | one run's metadata |
| `GET /api/v1/projects/{p}/runs/{id}/files/{name}` | `report.json`, `campaign.json`, `history.jsonl`, `bundle.json` |
| `GET /api/v1/projects/{p}/runs/{id}/report.html` | the interactive timeline |
| `GET /api/v1/projects/{p}/runs/{id}/dossier?frameworks=dora,soc2&format=html` | a compliance dossier (`json`, `md` or `html`) |
| `GET /healthz` | `ok` |

A run's ID is its fingerprint: pushing the same run twice stores it once.

## What it is not (yet)

- Not multi-node: one server, one data directory. Back the directory up.
- No user accounts or SSO for people: they read with per-project tokens
  (CI jobs can push with their OIDC identity instead). Put SSO in front with
  your proxy if you need named users.
- It stores what CI pushes; it does not run tests itself.
