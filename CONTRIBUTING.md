# Contributing

terminatr is pre-alpha and changes quickly; [docs/SPEC.md](docs/SPEC.md) is the plan it follows. Issues and pull requests are welcome. For anything larger than a fix, open an issue first so we can agree on the approach before you write the code.

## Building and testing

The [README](README.md#requirements) lists the tools you need. Then:

```sh
make            # libghostty-vt into .build/, then bin/tm
make vet        # go vet and staticcheck
make test       # unit and integration tests
make test-race  # the same with -race (CI runs it on main)
make e2e-smoke  # the end-to-end smoke set that CI runs on every PR; E2E_SHARD=1/2 runs half of it
```

For plain `go` commands or gopls, run `eval "$(make env)"` first. `make test-claude` runs the end-to-end suite against a real, logged-in Claude Code. It costs a few cents and CI doesn't run it, so run it yourself when you change how tm drives Claude.

## Pull requests

- Keep a PR to one change, with tests: a unit test for logic, an `internal/e2e` scenario for anything a user sees.
- Run `gofmt`; CI is kept lean (GitHub bills every job). A pull request runs one Ubuntu job, `test`: gofmt, `make build`, `make vet`, `make test` (no `-race`), `selftest` and the e2e smoke set; a failure uploads the screens and logs as `e2e-artifacts-linux`. A change to only `*.md` or `docs/` skips it. A newer push to the PR cancels the run in flight. A push to main runs `test` with `-race` (`make test-race`, `make e2e-smoke-race`) and a `macos` job (build, `make test`, smoke set); release tags wait for it, and pull requests never run macOS. The release snapshot runs on a pull request only when it changes release files (`.goreleaser.yaml`, `Makefile`, `go.mod`, `scripts/release/`, `Formula/`, the workflows, `internal/emu`, `internal/version`, `cmd/tm/main.go`); the `claude-mod` job (`claude plugin validate` and `test` on terminatr's mod) likewise only when it changes `internal/agent/claude/`. The weekly workflow (`weekly.yml`, also by hand; it skips when main hasn't changed since its last successful run) runs `make test-race`, the whole e2e set and a race-built smoke set on Ubuntu and macOS, and the fuzzers (1 minute per target, about 20 minutes). The release snapshot moved off main pushes: the release workflow runs it on every tag, before anything is signed or uploaded.
- End-to-end scenarios act only on a settled screen: wait for the text or state you expect (the harness's waits keep the last whole frame), and for a save or a `working…` note to clear, before the next key or click; never a fixed sleep. A flaky test is fixed at its cause (a wait, or a real bug), never retried or skipped. To find one, run it many times under load (`go test -c`, then several copies with `-test.count`, plus CPU hogs) and replay the failing run's `window-N.raw` frame by frame.
- If you change behaviour that docs/SPEC.md or the README describes, update them in the same PR.
- Don't commit machine-specific paths, logs or screen captures that show your home directory or account.

By contributing, you agree that your contributions are licensed under the [MIT licence](LICENSE).

## Security issues

Don't open a public issue for a vulnerability; see [SECURITY.md](SECURITY.md).
