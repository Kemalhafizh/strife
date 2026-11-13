# Support & Community

**Strife** is an open-source distributed systems testing and verification project created and maintained by [Muhammad Kemal Hafizh](https://github.com/Kemalhafizh) under the [Apache License 2.0](LICENSE).

## Documentation & Interactive Tools

Before opening an issue, check the documentation, changelog, and browser-based tools:

- **Documentation & CLI Reference:** [https://kemalhafizh.github.io/strife/docs/](https://kemalhafizh.github.io/strife/docs/)
- **Interactive WebAssembly Playground:** [https://kemalhafizh.github.io/strife/try/](https://kemalhafizh.github.io/strife/try/)
- **Timeline & Counterexample Studio:** [https://kemalhafizh.github.io/strife/studio/](https://kemalhafizh.github.io/strife/studio/)
- **Client-Side Attestation Verifier:** [https://kemalhafizh.github.io/strife/verify/](https://kemalhafizh.github.io/strife/verify/)
- **Self-Hosted Run Registry (`strife serve`):** [`docs/server.md`](docs/server.md)
- **GitHub Action & CI Recipes:** [`docs/github-action.md`](docs/github-action.md)
- **Benchmarks & Empirical Validation:** [`docs/BENCHMARKS.md`](docs/BENCHMARKS.md)
- **Release Notes:** [`CHANGELOG.md`](CHANGELOG.md) · **Governance:** [`GOVERNANCE.md`](GOVERNANCE.md)

## Troubleshooting Tips

- **`unknown` verdict (`exit code 3`):** The checker exhausted `--max-steps` or `--check-timeout`, or a live run recorded zero successful operations (`OK`). Re-run `strife check` with a higher `--max-steps`, shrink the trace with `strife shrink --history`, or run `strife doctor --config strife.json` to inspect cluster reachability.
- **Fault injection permissions:** If `docker` or `cgroup v2` faults are unavailable on your host, run `strife doctor` and consider the user-space `proxy` transport (`transport.type: "proxy"`).

## Getting Help & Participating

- **Questions & Usage Help:** Open a [Question / Discussion issue](https://github.com/Kemalhafizh/strife/issues/new?template=question.yml) if you need help configuring a target cluster, writing a workload, or reading a counterexample witness.
- **Bug Reports:** Open a [Bug Report](https://github.com/Kemalhafizh/strife/issues/new?template=bug_report.yml) with the exact command, `--seed`, `schedule.json`, or `history.jsonl` / `.edn` trace.
- **Feature Requests & New Models:** Open a [Feature Request](https://github.com/Kemalhafizh/strife/issues/new?template=feature_request.yml) or submit a pull request following [`CONTRIBUTING.md`](CONTRIBUTING.md).
- **Security Vulnerabilities:** Please report security issues privately via [`SECURITY.md`](SECURITY.md).
