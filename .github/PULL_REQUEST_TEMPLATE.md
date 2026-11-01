## Summary

<!-- Briefly describe what this pull request changes and why. -->

## Related Issue

<!-- Link any related issue, e.g. Fixes #123 -->

## Engineering Checklist

- [ ] **Zero External Dependencies:** `go.mod` remains stdlib-only (no `require` directives).
- [ ] **Formatting & Static Analysis:** `gofmt -s -w .` and `go vet ./...` pass with zero warnings.
- [ ] **Simulation Determinism:** Any changes in `pkg/sim`, `pkg/explore`, `pkg/nemesis`, or `pkg/workload` preserve bit-for-bit reproducibility (`strife sim --system raft --seeds 1..50 --determinism`).
- [ ] **Output Contracts:** `go test ./schemas` passes (no breaking changes to `report.v1.json`, `history.v1.json`, or `dossier.v1.json`).
- [ ] **Tests:** Unit and regression tests (`go test ./...`) pass.
