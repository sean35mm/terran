---
name: terran-update
description: Plan a Terran binary or catalog update across one or more Command Centers; do not use for unrelated package upgrades or silently applying available updates.
---

# Terran updates

Terran has no self-updater and never fetches a catalog. Two things update separately, each with the user's approval, and neither is applied silently:

1. **The binary.** Install an exact release the user names (`install.sh vX.Y.Z`, after reading the script and verifying it downloads `SHA256SUMS`) or build a reviewed source revision. Never resolve "latest" yourself.
2. **The catalogs** (public and private overlay). `git fetch`, then review the diff before checking out: `terran.json`, every changed `SKILL.md`, instruction and config sources, provenance, licenses. Skills are live symlinks, so a checkout changes them immediately; instructions, configs, files, and settings keys change only through `terran apply`. Never push.

## Schema v2

Catalogs at schema version 2 can use `files`, `json_keys`, `tools`, `platforms`, more targets (`codex-global`, `mise-config`, `mise-lock`, `claude-settings`, `t3-settings`, and file targets), and a private overlay. Terran 0.4 still reads schema 1 manifests, enrollments, and receipts and writes schema 2 when it next saves them; older binaries cannot read schema 2 state.

Therefore: every machine needs Terran 0.4 or newer before a catalog that uses v2 features reaches it. Update binaries first, catalogs second.

## Fleet order

1. `terran status` lists each machine's Terran version in the `TERRAN` column. Machines showing `incompatible terran` or `terran not found` have an old or missing binary.
2. Update the binary on each machine that needs it. Remote status is read-only, so do this on that machine or through the user.
3. Only then check out the new catalog revision on each machine.
4. On each machine: `terran version`, `terran plan --json`, show the user every action, get approval, `terran apply --expect <digest>`, then `terran status --local` and `terran doctor`.
5. Finish with `terran status` and confirm every row shows the expected version, matching catalog commits, and `clean`.

`terran doctor` warns when the binary version differs from the catalog version. A source build named `X.Y.Z-dev` matches catalog `X.Y.Z`.

Preserve pins outside the request. Terran does not manage packages beyond its fixed targets, remote repositories, or secrets; use `terran-provision` for tools and logins.

## Should trigger

- "Update Terran on all my machines."
- "Pull the catalog changes and show me what would change."
- "Is every Command Center on the same Terran version?"

## Should not trigger

- "Upgrade my system packages."
- "Update this npm dependency."
- "Set up a new machine." (use `terran-provision`)
