# Strife in CI

Strife plugs into CI in two layers:

1. **Report formats** that any CI system understands. They are available on
   `sim`, `campaign`, `shrink`, `check`, `run` and `compliance`:
   - `--junit FILE` writes a JUnit XML report.
   - `--sarif FILE` writes a SARIF 2.1.0 report for GitHub Code Scanning.
   - `--summary FILE` appends a Markdown digest.
2. **A GitHub Action** (`action.yml` at the root of this repository) that
   downloads a checksum-verified release binary when pinned to a release tag
   (or builds from source), runs Strife, and does the following:
   - publishes the digest on the job summary page,
   - uploads the HTML/JSON reports, JUnit XML, and SARIF 2.1.0 files as an artifact,
   - optionally pushes reports to a `strife serve` registry when running subcommands with `--push` support,
   - annotates the run,
   - fails the job when Strife finds a problem.

## GitHub Action

```yaml
name: consistency
on: [push, pull_request]

jobs:
  strife:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: kemalhafizh/strife@v1.0.0   # pin a release tag or commit SHA
        with:
          command: sim --system raft --seeds 1..500 --determinism --expect linearizable
```

### Inputs

| Input | Default | Meaning |
| --- | --- | --- |
| `command` | (required) | The strife command line without the leading `strife`. It is split on whitespace and never evaluated by a shell, so quoting and globbing do not apply. |
| `out` | `strife-out` | Report directory (`""` to disable). |
| `junit` | `strife-junit.xml` | JUnit XML path (`""` to disable). |
| `sarif` | `""` | SARIF 2.1.0 report path (`""` to disable). |
| `summary` | `true` | Append a Markdown digest to the job summary. |
| `push` | `""` | Registry URL passed via `--push` (currently used by `scripts/github-action.sh` on subcommands that accept `--push`; for `sim`/`campaign`/`run`, see [`docs/server.md`](server.md#publishing-from-ci)). |
| `push-token` | `""` | Optional Bearer token for `--push-token`. |
| `tenant` | `default` | Tenant identifier for `--tenant`. |
| `upload` | `true` | Upload `out`, `junit` and `sarif` as an artifact. The upload also runs when Strife fails. |
| `artifact-name` | `strife-reports` | Artifact name. It must be unique per workflow run when you use the action twice. |
| `fail` | `true` | Fail the step when Strife exits non-zero. |
| `go-version` | Strife's `go.mod` | Go version used when building Strife from source. |

### Outputs

| Output | Meaning |
| --- | --- |
| `exit-code` | `0` passed, `1` violation, `2` usage error, `3` inconclusive, `4` error |
| `outcome` | `passed`, `violation`, `inconclusive`, `usage-error` or `error` |
| `junit` | Path of the JUnit file |
| `sarif` | Path of the SARIF 2.1.0 file (if enabled) |
| `report-dir` | Path of the report directory |

### Recipes

**Regression gate.** Every seed of the correct system must stay
linearizable, and every replay must be identical:

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system raft --seeds 1..1000 --determinism --expect linearizable
```

**Bug hunt that must succeed.** This is a single passing test case when any
seed violates. `--shrink` attaches a minimal reproducer to the reports:

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system raft-unsafe-fsync --faults kill,io-stall --seeds 1..300 --expect violation --shrink
```

**Multi-seed campaign gate with baseline regression diff:**

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: campaign --system raft --seeds 1..200 --baseline baseline.json --expect no-regression
```

**Check a Strife `.jsonl`, Jepsen `.edn`, or OpenTelemetry Span `.json` trace your own test suite recorded:**

```yaml
- run: ./run-my-test-suite --otel-out traces.json
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: check --model kv --max-mttr 500ms --min-availability 0.99 traces.json
```

**Generate a compliance dossier (`dora,soc2,iso27001,bi-ojk,pci-dss,bi-sp`) with SLA gates and SARIF rules:**

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system outbox-settlement --seed 7 --expect linearizable
    out: settle-out
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: compliance --report settle-out/outbox-settlement-seed7/report.json --frameworks dora,soc2,iso27001,bi-ojk,pci-dss,bi-sp --max-mttr 500ms --min-availability 0.95
    out: settle-compliance
    sarif: compliance.sarif.json
```

**Comparative Pull Request Gate & Digest (`strife ci-report`):**

```yaml
- run: |
    strife ci-report \
      --report candidate/report.json \
      --baseline-report baseline/report.json \
      --dossier settle-compliance/dossier.json \
      --max-mttr 500ms --min-availability 0.99 \
      --out "$GITHUB_STEP_SUMMARY" --json pr-verdict.json
```

**React to the verdict yourself:**

```yaml
- id: strife
  uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system raft-stale-reads --faults partition --seeds 1..50
    fail: "false"
- if: steps.strife.outputs.outcome == 'violation'
  run: echo "found one, see ${{ steps.strife.outputs.report-dir }}"
```

**Show results in the PR's Checks tab.** Feed the JUnit file to any JUnit
reporter action, for example:

```yaml
- uses: mikepenz/action-junit-report@v4
  if: always()
  with:
    report_paths: strife-junit.xml
```

**Publish SARIF 2.1.0 findings to GitHub Code Scanning:**

```yaml
- uses: kemalhafizh/strife@v1.0.0
  with:
    command: sim --system outbox-settlement --seeds 1..100 --expect linearizable
    sarif: strife.sarif.json
- uses: github/codeql-action/upload-sarif@v3
  if: always()
  with:
    sarif_file: strife.sarif.json
```

> [!NOTE]
> For publishing `sim`, `campaign`, or `run` JSON artifacts directly to `strife serve`, post `report.json` or `campaign.json` to `/api/v1/ingest` as shown in [`docs/server.md`](server.md#publishing-from-ci). On CLI directly, `strife check --push ... --push-token ... --tenant ...` pushes checked histories in one command.

## What the JUnit report contains

The `testcase` elements depend on the command:

| Command | `testcase` elements | Failure means |
| --- | --- | --- |
| `sim --seed N`, `shrink`, `check` | one per key of the history | that key's history violates the model; the body holds the counterexample and reproduction command |
| `sim --seeds A..B` | one per seed | that seed violates; the body holds its explanation and reproduction command |
| `sim --seeds A..B --expect violation` | one: `finds a violation` | no seed in the range violated (finding the bug *is* the test) |
| `sim --seed N --expect …` | one: `expect …` | the verdict differs from the expectation |
| `campaign` | one per seed (`seed N`), or `finds a violation` with `--expect violation` | that seed violated or regressed; under `--expect no-regression`, known baseline failures are recorded as `skipped` |
| `run` | one per key, plus `fault injection` | as above; `fault injection` is an error when fault commands failed, because the cluster did not see the planned schedule |
| `compliance` | one per evaluated control (`[FW] control_id — Title`) | the control evaluated to `FAIL` (or `PARTIAL` when `--strict` is set) |

Outcomes map as follows:

- An inconclusive check (search budget exhausted) is a JUnit `error`, not a
  pass.
- A run that cannot complete at all (bad config, unreachable cluster,
  nondeterminism detected) is reported as a single `error` case, so CI never
  shows green for a run that did not happen.

Suite `properties` carry the audit trail:

- the Strife version,
- the seed (or campaign seed count & trace counts),
- the verdict (or compliance overall status),
- the report, campaign, or dossier SHA-256 fingerprint,
- the SHA-256 of the schedule, history and trace (for simulation/live runs),
- the reproduction command.

## Other CI systems

The report flags are plain files, so any CI system can use them:

```sh
strife sim --system raft --seeds 1..500 --expect linearizable \
  --junit strife-junit.xml --sarif strife.sarif.json --summary strife-summary.md
```

- **GitLab:** `artifacts: reports: junit: strife-junit.xml`
- **Jenkins:** `junit 'strife-junit.xml'`
- **CircleCI:** `store_test_results`
- **Azure Pipelines:** `PublishTestResults@2`
- **Buildkite:** the test-collector plugin
