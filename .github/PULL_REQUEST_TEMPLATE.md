## Summary

<!-- Briefly describe what this pull request changes and why. -->

## Type of Change

- [ ] Bug fix / checker soundness or parser hardening
- [ ] New consistency model, simulated system, or live client
- [ ] Fault injection, transport, agent, or diagnostic probe improvement
- [ ] Run registry (`strife serve`), compliance dossier, SARIF, or attestation
- [ ] Documentation, repository governance, examples, or `site/` website
- [ ] CI / release workflow

## Related Issue

<!-- Link any related issue, e.g. Fixes #123 -->

## Engineering Checklist

- [ ] **Zero External Dependencies:** `go.mod` remains stdlib-only (no `require` directives).
- [ ] **Formatting, Vet & Lint:** `gofmt -l .` is empty and `make lint` (`go vet`, `shellcheck`, `actionlint`) passes.
- [ ] **Tests:** `go test -race -short ./...` and `go test ./...` pass locally.
- [ ] **Simulation Determinism:** Any changes in `pkg/sim`, `pkg/explore`, `pkg/nemesis`, or `pkg/workload` preserve bit-for-bit reproducibility (`strife sim --system raft --seeds 1..50 --determinism --out ""`).
- [ ] **Output Contracts:** `go test ./schemas` passes (no breaking changes to `report.v1.json`, `history.v1.json`, or `dossier.v1.json`).
- [ ] **Website / Docs (if touched):** `site/wasm` (`GOOS=js GOARCH=wasm go vet . && go test ./...`), internal links, and `check-wording.sh` guidelines are respected.
- [ ] **Changelog:** User-facing changes are noted under `## [Unreleased]` in `CHANGELOG.md`.
