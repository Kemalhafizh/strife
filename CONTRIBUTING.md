# Contributing to Strife

Thank you for contributing to **Strife**. To keep the engine fast, auditable, and bit-for-bit reproducible, every contribution must follow four core engineering invariants described here and in [`GOVERNANCE.md`](GOVERNANCE.md).

---

## Core Engineering Invariants

1. **Zero External Dependencies (`stdlib` only)**
   - [`go.mod`](go.mod) must contain **zero** `require` directives.
   - Wire protocols, cryptographic primitives (`crypto/ed25519`, `crypto/sha256`), Netlink sockets, cgroup v2 controls, HTTP daemon/dashboard (`strife serve`), and the WebAssembly 1.0 VM are implemented directly against the Go standard library.
2. **Bit-for-Bit Simulation Determinism**
   - Inside `pkg/sim`, `pkg/sim/raft`, `pkg/explore`, `pkg/nemesis`, and `pkg/workload`, never read wall-clock time (`time.Now()`), `crypto/rand`, or `math/rand` global state.
   - Derive all randomness from `internal/rng.RNG` (`xoshiro256**` seeded via `SplitMix64`).
   - Never iterate over a Go `map` in a path that affects event scheduling, message delivery order, or operation history; sort keys first.
3. **Clean Code & No Overengineering**
   - Prefer concrete structs and small, single-purpose functions over speculative interfaces or deep abstraction layers.
   - Keep formatting, shell scripts, workflows, and static analysis clean: `make lint` must pass with zero warnings.
4. **Backward-Compatible Output Schemas**
   - `report.json`, `history.jsonl`, and `dossier.json` are governed by versioned JSON Schemas in [`schemas/`](schemas).
   - Additive optional fields require updating the schema snapshot via `go test ./schemas -update`. Removing, renaming, or changing the type of an existing field is rejected by `TestCompatible`.

---

## Local Verification

Before opening a pull request, run the verification suite locally:

```sh
# 1. Format, vet, shellcheck, and workflow linting
gofmt -s -w .
make lint

# 2. Fast unit tests with the race detector
go test -race -short ./...

# 3. Full test suite (includes bug-finding regressions and JSON Schema checks)
go test ./...

# 4. Determinism & shrinking smoke check
go build -o bin/strife ./cmd/strife
./bin/strife sim --system raft --seeds 1..50 --determinism --expect linearizable --out ""
./bin/strife sim --system raft-ledger --seeds 1..20 --determinism --expect linearizable --out ""
./bin/strife sim --system outbox-settlement --seeds 1..20 --determinism --expect linearizable --out ""
```

### Website & WebAssembly Bridge Checks

[`site/`](site) builds directly from `README.md`, `CHANGELOG.md`, `docs/*.md`, `examples/*/README.md`, and compiles core engine packages (`pkg/explore`, `pkg/checker`, `pkg/report`, `pkg/attest`, `pkg/compliance`) to WebAssembly via `site/wasm`. When touching those files:

```sh
# Check the WebAssembly bridge module (required by pages.yml)
(cd site/wasm && test -z "$(gofmt -l .)" && go vet ./... && GOOS=js GOARCH=wasm go vet . && go test ./...)

# Full static site build + link checker + wording validator (requires Hugo 0.148.2 + Node 20+)
OFFLINE=1 site/build.sh
```

> [!NOTE]
> - [`site/layouts/_partials/docs-adapter.html`](site/layouts/_partials/docs-adapter.html) splits [`README.md`](README.md) on top-level `## ` headings. If you add or rename a `## ` section in `README.md`, register its slug and metadata in `docs-adapter.html`.
> - Preserve the `## Wording guidelines` table in [`docs/ROADMAP.md`](docs/ROADMAP.md); [`site/scripts/check-wording.sh`](site/scripts/check-wording.sh) reads the "Avoid" column and fails CI if disallowed wording or regulatory certification claims appear in generated HTML.

### Fuzzing Parsers of Untrusted Bytes

If you touch a parser or soundness checker, run its fuzz target briefly before opening a PR (`ci.yml` runs all eight targets for 45s each):

```sh
go test -run '^$' -fuzz '^FuzzParseWasm$' -fuzztime 15s ./pkg/workload
go test -run '^$' -fuzz '^FuzzReadEDN$' -fuzztime 15s ./pkg/history
go test -run '^$' -fuzz '^FuzzReadJSONL$' -fuzztime 15s ./pkg/history
go test -run '^$' -fuzz '^FuzzCheckAppendSound$' -fuzztime 15s ./pkg/txn
go test -run '^$' -fuzz '^FuzzRESP$' -fuzztime 15s ./pkg/client
go test -run '^$' -fuzz '^FuzzPostgresMessages$' -fuzztime 15s ./pkg/client
go test -run '^$' -fuzz '^FuzzMySQLPackets$' -fuzztime 15s ./pkg/client
go test -run '^$' -fuzz '^FuzzKafkaRecords$' -fuzztime 15s ./pkg/client
```

---

## Where to Make Changes

- **Consistency models (`pkg/model`, `pkg/checker`, `pkg/txn`):**
  Implement `model.Model` (and optionally `model.Diagnoser`, `model.Decider`, or `model.ReadOnly` for direct witness extraction or eager read linearization), register the model in [`model.ByName`](pkg/model/model.go) and `model.Names()`, and add unit + brute-force cross-check tests in `pkg/model` and `pkg/txn`.
- **Simulated systems or bug modes (`pkg/sim`, `pkg/sim/raft`, `pkg/explore`):**
  Register the system in [`explore.Systems()`](pkg/explore/explore.go) with its `Expect` verdict and recommended faults, add regression tests in [`pkg/explore/explore_test.go`](pkg/explore/explore_test.go), and record empirical measurements in [`docs/BENCHMARKS.md`](docs/BENCHMARKS.md).
- **Live database clients (`pkg/client`, `pkg/runner`):**
  Implement `runner.TargetClient` using standard library `net` / `crypto/tls`, add a fuzz target for its wire decoder, classify refused vs indeterminate (`info`) errors soundly, and provide a Docker Compose harness under [`examples/`](examples).
- **Transports, faults & telemetry (`pkg/transport`, `pkg/agent`, `cmd/strife-agent`, `pkg/probe`):**
  Support idempotent inject/heal semantics, update `strife doctor` capability checks if adding host dependencies, and add correlation rules cleanly in `pkg/probe`.
- **Reports, SARIF, compliance & run registry (`pkg/report`, `pkg/compliance`, `pkg/attest`, `pkg/serve`):**
  Ensure any new report/dossier JSON fields are additive and verified against [`schemas/`](schemas) (`go test ./schemas`). Never claim legal certification in compliance mappings.
- **Custom workloads (`pkg/workload`):**
  Preserve WebAssembly 1.0 determinism, fuel metering (`DefaultMaxFuel`), stack-depth caps, and linear memory bounds.

---

## Pull Request Guidelines

- Review [`GOVERNANCE.md`](GOVERNANCE.md) before starting changes that alter schemas, CLI exit codes, determinism, or core dependencies.
- Keep pull requests focused on one coherent change. Avoid drive-by formatting edits unrelated to your change.
- For user-facing changes, add an entry under `## [Unreleased]` in [`CHANGELOG.md`](CHANGELOG.md).

## Pull Request Checklist

- [ ] [`go.mod`](go.mod) has no external dependencies.
- [ ] `gofmt -l .` is empty and `make lint` (`go vet`, `shellcheck`, `actionlint`) passes.
- [ ] `go test -race -short ./...` and `go test ./...` pass (including `./schemas` and `./pkg/explore`).
- [ ] New features or bug fixes include deterministic unit tests (and `site/wasm` / doc link checks if applicable).

By submitting a contribution, you agree that your work is licensed under the [Apache License 2.0](LICENSE).
