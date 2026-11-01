# Contributing to Strife

Thank you for contributing to **Strife**. To keep the engine fast, auditable, and bit-for-bit reproducible, every contribution must follow four core engineering invariants.

---

## Core Engineering Invariants

1. **Zero External Dependencies (`stdlib` only)**
   - [`go.mod`](go.mod) must contain **zero** `require` directives.
   - Wire protocols, cryptographic primitives (`crypto/ed25519`, `crypto/sha256`), Netlink sockets, cgroup v2 controls, and the WebAssembly 1.0 VM are implemented directly against the Go standard library.
2. **Bit-for-Bit Simulation Determinism**
   - Inside `pkg/sim`, `pkg/sim/raft`, `pkg/explore`, `pkg/nemesis`, and `pkg/workload`, never read wall-clock time (`time.Now()`), `crypto/rand`, or `math/rand` global state.
   - Derive all randomness from `internal/rng.RNG` (`xoshiro256**` seeded via `SplitMix64`).
   - Never iterate over a Go `map` in a path that affects event scheduling, message delivery order, or operation history; sort keys first.
3. **Clean Code & No Overengineering**
   - Prefer concrete structs and small, single-purpose functions over speculative interfaces or deep abstraction layers.
   - Keep formatting and static analysis clean: `gofmt -s -w .` and `go vet ./...` must produce zero warnings.
4. **Backward-Compatible Output Schemas**
   - `report.json`, `history.jsonl`, and `dossier.json` are governed by versioned JSON Schemas in [`schemas/`](schemas).
   - Additive optional fields require updating the schema snapshot via `go test ./schemas -update`. Removing, renaming, or changing the type of an existing field is rejected by `TestCompatible`.

---

## Local Verification

Before opening a pull request, run the verification suite locally:

```sh
# 1. Format and static analysis
gofmt -s -w .
go vet ./...

# 2. Fast unit tests with the race detector
go test -race -short ./...

# 3. Full test suite (includes bug-finding regressions and JSON Schema checks)
go test ./...

# 4. Determinism & shrinking smoke check
go build -o strife ./cmd/strife
./strife sim --system raft --seeds 1..50 --determinism --expect linearizable --out ""
./strife sim --system raft-ledger --seeds 1..20 --determinism --expect linearizable --out ""
```

If you touch a parser of untrusted bytes (`pkg/client`, `pkg/history`, `pkg/workload`), run its fuzz target briefly:

```sh
go test -run '^$' -fuzz '^FuzzParseWasm$' -fuzztime 15s ./pkg/workload
```

---

## Where to Make Changes

- **Adding a sequential or transactional model (`pkg/model`):**
  Implement `model.Model` (and optionally `model.Diagnoser` or `model.Decider` for polynomial-time witness extraction), register the model in [`model.ByName`](pkg/model/model.go), and add unit + brute-force cross-check tests in `pkg/model`.
- **Adding a simulated system or bug mode (`pkg/sim`, `pkg/explore`):**
  Register the system in [`explore.Systems()`](pkg/explore/explore.go) with its `Expect` verdict and recommended faults, and cover it in [`pkg/explore/explore_test.go`](pkg/explore/explore_test.go).
- **Adding or updating a live database client (`pkg/client`):**
  Implement `runner.TargetClient` using standard library `net` / `crypto/tls`, add a fuzz target for its wire decoder, and include a reproducible Docker Compose harness under [`examples/`](examples).

---

## Pull Request Checklist

- [ ] `go.mod` has no external dependencies.
- [ ] `gofmt -l .` is empty and `go vet ./...` passes.
- [ ] `go test ./...` passes (including `./schemas` and `./pkg/explore` determinism checks).
- [ ] New features or bug fixes include deterministic unit tests.

By submitting a contribution, you agree that your work is licensed under the [Apache License 2.0](LICENSE).
