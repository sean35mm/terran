# Contributing

Open an issue before broad or compatibility-changing work. Keep Terran dependency-free and within its documented scope. Changes should be focused, portable across supported macOS/Linux architectures, and accompanied by tests where behavior changes.

Before submitting a pull request:

```sh
gofmt -w cmd internal
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
mkdir -p tmp
go build -o tmp/terran-build-check ./cmd/terran
sh -n install.sh
sh tests/install_leaf.sh
python3 scripts/validate-docs.py
```

Terran is used by AI agents and never prompts; keep every command scriptable with `--json` and flag-based decisions. Tests must use temporary `HOME`, `XDG_CONFIG_HOME`, and `XDG_STATE_HOME` roots only. Skills under `skills/` are declared in `terran.json`, and the repository catalog must keep loading (the test suite checks it).

Update `CHANGELOG.md`, documentation, licenses, and third-party notices when applicable. Canonical global policies under `instructions/` are complete harness-specific files, not repository guidance; review authorization behavior and portability before changing them. Never include credentials, private machine state, generated binaries, email addresses, local URLs, or personal absolute paths. Contributions are accepted under the repository's MIT License unless a file states another license.

For a release, set the new version in `terran.json` and date its `CHANGELOG.md` heading (`## X.Y.Z - YYYY-MM-DD`), review skill provenance and both instruction sources, rerun the uncached tests/race check, vet, syntax/format/security scans, and all four platform builds, and push to `main`. The Release workflow publishes `vX.Y.Z` from that commit once, with `install.sh`, all platform archives, and checksums; an undated `Unreleased` heading or an already released version publishes nothing. The installer syntax is `sh install.sh vX.Y.Z [destination-directory]`; verify those assets and the pinned install instructions in GitHub. Never move a published tag; correct release mistakes transparently with a new release.
