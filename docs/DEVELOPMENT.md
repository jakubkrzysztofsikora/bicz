# Development

Moved out of the top-level README, which links here.

```sh
make build && make test          # or: go vet ./... && go test -race ./...
scripts/test_install.sh          # installer fixture tests (local HTTP server)
pwsh scripts/test_install.ps1    # same for install.ps1
scripts/smoke.sh                 # real daemon + real hook CLI + TS mock harness
scripts/build_release.sh vX.Y.Z  # all six archives + SHA256SUMS into dist/
```

Layout notes: `internal/infer` is the opt-in external-endpoint completion of
missing `/bicz loss` parameters (enabled by the `inference` block of a harness's
bicz config; off by default; never called by the daemon).

Evidence and exact commands are in [`VERIFICATION.md`](VERIFICATION.md). Design and contracts:
[`DESIGN.md`](DESIGN.md), [`CONTRACTS.md`](CONTRACTS.md), [`SCHEMA.md`](SCHEMA.md),
[`PRESSURE_MECHANICS.md`](PRESSURE_MECHANICS.md).
Security notes: [`SECURITY.md`](../SECURITY.md). History: [`CHANGELOG.md`](../CHANGELOG.md).
License: [MIT](../LICENSE).
