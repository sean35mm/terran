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
```

Update `CHANGELOG.md`, documentation, licenses, and third-party notices when applicable. Canonical global policies under `instructions/` are complete harness-specific files, not repository guidance; review authorization behavior and portability before changing them. Never include credentials, private machine state, generated binaries, email addresses, local URLs, or personal absolute paths. Contributions are accepted under the repository's MIT License unless a file states another license.

For a release, update `terran.json` and `CHANGELOG.md`, review skill provenance and both instruction sources, rerun the uncached tests/race check, vet, syntax/format/security scans, and all four platform builds, and create a `vX.Y.Z` tag that exactly matches the manifest. For tags that publish `install.sh`, all platform archives, and checksums, the installer syntax is `sh install.sh vX.Y.Z [destination-directory]`; verify those assets and the pinned install instructions in GitHub. Never move a published tag; correct release mistakes transparently with a new release.
