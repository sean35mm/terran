# Terran repository guidance

Terran is a security-sensitive, dependency-free Go CLI for projecting catalog
skills and fixed global instruction files. Make the smallest change that
preserves its collision, drift, ownership, and receipt guarantees.

## Repository boundaries

- Canonical skills live under `skills/<name>/SKILL.md` and are declared in
  `terran.json`.
- Canonical managed global instructions live under `instructions/`. They are
  complete harness-specific user policies projected by Terran; they are not
  repository-local guidance and are not interchangeable with this file.
- `internal/terran` owns validation, planning, application, receipts, and
  diagnostics. `cmd/terran` owns CLI parsing and presentation.
- Do not add arbitrary catalog destinations, commands, modes, or projection
  strategies. Instruction destinations are fixed in code by target ID.
- Never include credentials, tokens, private machine state, personal absolute
  paths, email addresses, local-only URLs, or generated state/config files.
  Treat instruction prose and skill content as public source data.

## Working safely

Read `README.md`, `terran.json`, and the relevant source and tests before
editing. Preserve strict JSON decoding and schema version 1 until a released
migration requires otherwise. Tests must use temporary HOME and XDG roots;
never point Terran tests at a real home directory. Do not inspect or modify the
developer's actual Terran state except when the user explicitly requests a
machine migration.

Run the narrowest relevant check while iterating. Before delivery, run:

```sh
gofmt -w cmd internal
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
sh -n install.sh
```

Also validate `terran.json`, scan public content for secrets and private paths,
and build all supported Darwin/Linux amd64/arm64 targets for release-affecting
changes. Do not commit, tag, publish, or replace release assets unless the user
explicitly requests that delivery step.
