# Machine that is already partly set up

Fill gaps only. Do not reinstall, overwrite, or reorganize what works.

## Start with facts

```sh
terran doctor --json
terran capture --json
terran status --local --json
```

- `doctor` shows what is missing or unhealthy: enrollment, tools (`tool:<name>` checks), permissions, receipt, binary version.
- `capture` lists agent setup on this machine that no catalog owns (`unmanaged_entry`, `unowned_key`) with item ids. It never prints values.
- `status --local` shows which catalog items are pending, drifting, held, or excluded.

If `terran` itself is missing or not enrolled, switch to `fresh.md` from stage 4.

## Fill gaps in order

1. Missing base tools: `darwin.md` or `omarchy.md`, with approval per command.
2. Old binary: use `terran-update`. Do not skip versions on a machine that is part of a fleet.
3. Not enrolled, or enrolled without the overlay: `terran enroll --repo <public> --overlay <private> --name <name>`. Re-enrolling the same catalog keeps holds and can rename the machine or add an overlay. Dropping or changing an overlay that owns applied items fails with `repository_mismatch`; do not work around it.
4. Pending catalog changes: `terran plan --json`, show the user, approval, `terran apply --expect <digest>`.
5. Missing tools: `mise install`, then the missing items in the overlay's `provision/checklist.md`. Skip items that already work; verify them instead (`claude mcp list`, `codex mcp list`).
6. Fleet access: confirm the machine appears in `command-centers.json`; add it if not (see `fresh.md` stage 9).

## Existing items that collide

An existing skill, file, config, or settings key with the same destination as a catalog item shows up as `blocked_collision`. If the bytes are identical Terran plans `adopt` instead and changes nothing. If they differ, show the user both versions and ask; only then pass `--decide <id>=replace` (private backup, catalog version installed) or `--decide <id>=keep` (item is held on this machine).

## Items that should live in a catalog

For each `capture` item the user wants on every machine, hand off to `terran-curate-skills`: it decides public versus private, platform tags, and edits the right `terran.json`. Machine-local items that should stay local: leave them, or `terran hold <id>` when a catalog item must not touch them.

## Do not

- Delete, move, or rewrite existing files to make a plan clean.
- Edit Terran-managed destinations directly.
- Print or copy credentials from existing config files.
- Push anything.
