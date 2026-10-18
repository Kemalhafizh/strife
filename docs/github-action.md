# Strife in CI

Strife plugs into CI in two layers:

1. **Report formats** that any CI system understands. They are available on
   `sim`, `shrink`, `check` and `run`:
   - `--junit FILE` writes a JUnit XML report.
   - `--summary FILE` appends a Markdown digest.
2. **A GitHub Action** (`action.yml` at the root of this repository) that
   builds Strife, runs it, and does the following:
   - publishes the digest on the job summary page,
   - uploads the HTML/JSON reports and the JUnit file as an artifact,
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
      - uses: kemalhafizh/strife@main   # pin a tag or commit SHA in production
        with:
          command: sim --system raft --seeds 1..500 --determinism --expect linearizable
```

### Inputs

| Input | Default | Meaning |
|---|---|---|
| `command` | (required) | The strife command line without the leading `strife`. It is split on whitespace and never evaluated by a shell, so quoting and globbing do not apply. |
| `out` | `strife-out` | Report directory (`""` to disable). |
| `junit` | `strife-junit.xml` | JUnit XML path (`""` to disable). |
| `summary` | `true` | Append a Markdown digest to the job summary. |
| `upload` | `true` | Upload `out` and `junit` as an artifact. The upload also runs when Strife fails. |
| `artifact-name` | `strife-reports` | Artifact name. It must be unique per workflow run when you use the action twice. |
| `fail` | `true` | Fail the step when Strife exits non-zero. |
| `go-version` | Strife's `go.mod` | Go version used to build Strife. |
| `push-server` | `""` | URL of a [`strife server`](server.md). When set, the run's reports (or its `campaign.json`) are pushed there after the run, with the commit, branch and a link to the job, also when Strife failed. |
| `push-project` | `""` | Project on the server. |
| `push-token-env` | `STRIFE_TOKEN` | Name of the environment variable that holds the project's write token. |

### Outputs

| Output | Meaning |
|---|---|
| `exit-code` | `0` passed, `1` violation, `2` usage error, `3` inconclusive, `4` error |
| `outcome` | `passed`, `violation`, `inconclusive`, `usage-error` or `error` |
| `junit` | Path of the JUnit file |
| `report-dir` | Path of the report directory |

### Recipes

**Regression gate.** Every seed of the correct system must stay
linearizable, and every replay must be identical:

```yaml
- uses: kemalhafizh/strife@main
  with:
    command: sim --system raft --seeds 1..1000 --determinism --expect linearizable
```

**Bug hunt that must succeed.** This is a single passing test case when any
seed violates. `--shrink` attaches a minimal reproducer to the reports:

```yaml
- uses: kemalhafizh/strife@main
  with:
    command: sim --system raft-unsafe-fsync --faults kill,io-stall --seeds 1..300 --expect violation --shrink
```

**Check a history your own test suite recorded:**

```yaml
- run: ./run-my-jepsen-style-test --history-out ops.jsonl
- uses: kemalhafizh/strife@main
  with:
    command: check --model kv ops.jsonl
```

**React to the verdict yourself:**

```yaml
- id: strife
  uses: kemalhafizh/strife@main
  with:
    command: sim --system raft-stale-reads --faults partition --seeds 1..50
    fail: "false"
- if: steps.strife.outputs.outcome == 'violation'
  run: echo "found one, see ${{ steps.strife.outputs.report-dir }}"
```

**Keep every run and flag regressions across commits.** Push to your
[`strife server`](server.md). The server verifies each run, compares it with
the previous run of its series, and calls its webhook on a regression; the
step annotates the job with an error when the server reports one:

```yaml
- uses: kemalhafizh/strife@main
  env:
    STRIFE_TOKEN: ${{ secrets.STRIFE_TOKEN }}
  with:
    command: campaign --system raft --seeds 1..200
    push-server: https://strife.internal
    push-project: payments
```

Outside GitHub, run `strife push` yourself after the run; it exits 1 on a
regression:

```sh
STRIFE_TOKEN=... strife push --server https://strife.internal --project payments strife-out/campaign.json
```

**Show results in the PR's Checks tab.** Feed the JUnit file to any JUnit
reporter action, for example:

```yaml
- uses: mikepenz/action-junit-report@v4
  if: always()
  with:
    report_paths: strife-junit.xml
```

## What the JUnit report contains

The `testcase` elements depend on the command:

| Command | `testcase` elements | Failure means |
|---|---|---|
| `sim --seed N`, `shrink`, `check` | one per key of the history | that key's history is not linearizable; the body holds the counterexample and the reproduction command |
| `sim --seeds A..B` | one per seed | that seed violates; the body holds its explanation and reproduction command |
| `sim --seeds A..B --expect violation` | one: `finds a violation` | no seed in the range violated (finding the bug *is* the test) |
| `sim --seed N --expect …` | one: `expect …` | the verdict differs from the expectation |
| `run` | one per key, plus `fault injection` | as above; `fault injection` is an error when fault commands failed, because the cluster did not see the planned schedule |

Outcomes map as follows:

- An inconclusive check (search budget exhausted) is a JUnit `error`, not a
  pass.
- A run that cannot complete at all (bad config, unreachable cluster,
  nondeterminism detected) is reported as a single `error` case, so CI never
  shows green for a run that did not happen.

Suite `properties` carry the audit trail:

- the Strife version,
- the seed,
- the verdict,
- the report fingerprint,
- the SHA-256 of the schedule, history and trace,
- the reproduction command.

## Other CI systems

The report flags are plain files, so any CI system can use them:

```sh
strife sim --system raft --seeds 1..500 --expect linearizable \
  --junit strife-junit.xml --summary strife-summary.md
```

- **GitLab:** `artifacts: reports: junit: strife-junit.xml`
- **Jenkins:** `junit 'strife-junit.xml'`
- **CircleCI:** `store_test_results`
- **Azure Pipelines:** `PublishTestResults@2`
- **Buildkite:** the test-collector plugin
