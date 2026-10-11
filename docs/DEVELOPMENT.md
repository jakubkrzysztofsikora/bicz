# Development

Moved out of the top-level README, which links here.

```sh
make build && make test          # or: go vet ./... && go test -race ./...
scripts/test_install.sh          # installer fixture tests (local HTTP server)
pwsh scripts/test_install.ps1    # same for install.ps1
scripts/smoke.sh                 # real daemon + real hook CLI + TS mock harness
scripts/build_release.sh vX.Y.Z  # all six archives + SHA256SUMS into dist/
```

Evidence and exact commands are in [`VERIFICATION.md`](VERIFICATION.md). Design, contracts
and review notes: [`DESIGN.md`](../DESIGN.md), [`CONTRACTS.md`](CONTRACTS.md), [`SCHEMA.md`](SCHEMA.md),
[`PRESSURE_MECHANICS.md`](../PRESSURE_MECHANICS.md), `HOSTILE_*_REVIEW.md` in the repository root.
Security notes: [`SECURITY.md`](../SECURITY.md). History: [`CHANGELOG.md`](../CHANGELOG.md).
License: [MIT](../LICENSE).
