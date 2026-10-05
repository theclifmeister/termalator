# Contributing

termilator is pre-alpha and changes quickly; [docs/SPEC.md](docs/SPEC.md) is the plan it follows. Issues and pull requests are welcome. For anything larger than a fix, open an issue first so we can agree on the approach before you write the code.

## Building and testing

The [README](README.md#requirements) lists the tools you need. Then:

```sh
make            # libghostty-vt into .build/, then bin/tm
make vet
make test       # unit and integration tests, with -race
make e2e-smoke  # the end-to-end smoke set that CI runs on every PR
```

For plain `go` commands or gopls, run `eval "$(make env)"` first. `make test-claude` runs the end-to-end suite against a real, logged-in Claude Code. It costs a few cents and CI doesn't run it, so run it yourself when you change how tm drives Claude.

## Pull requests

- Keep a PR to one change, with tests: a unit test for logic, an `internal/e2e` scenario for anything a user sees.
- Run `gofmt`; CI checks it, together with `make vet`, `make test` and `make e2e-smoke` on macOS and Ubuntu.
- If you change behaviour that docs/SPEC.md or the README describes, update them in the same PR.
- Don't commit machine-specific paths, logs or screen captures that show your home directory or account.

By contributing, you agree that your contributions are licensed under the [MIT licence](LICENSE).

## Security issues

Don't open a public issue for a vulnerability; see [SECURITY.md](SECURITY.md).
