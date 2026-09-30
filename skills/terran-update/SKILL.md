---
name: terran-update
description: Plan a Terran binary or catalog update across one or more Command Centers; do not use for unrelated package upgrades or silently applying available updates.
---

# Terran updates

Terran has no self-updater and never fetches a catalog. Two things update separately, each with the user's approval, and neither is applied silently:

1. **The binary.** Install an exact release the user names (`install.sh vX.Y.Z`, after reading the script and verifying it downloads `SHA256SUMS`) or build a reviewed source revision. Never resolve "latest" yourself.
2. **The catalogs** (public and private overlay). `git fetch`, then review the diff before checking out: `terran.json`, every changed `SKILL.md`, instruction and config sources, provenance, licenses. A checkout changes nothing on the machine: skills (managed directory copies), instructions, configs, files, and settings keys change only through `terran apply`. The first 0.4 apply on a machine applied with 0.3 converts its skill symlinks to copies (`update`, `convert live symlink to managed copy`). Never push.

Installed skill copies lag the catalog: a machine that has not applied the new catalog still carries the old text of this skill. After fetching a catalog, read the skill from the fetched checkout (`<catalog>/skills/<name>/SKILL.md`) and follow that copy when it differs from the installed one.

## Schema v2

Catalogs at schema version 2 can use `files`, `json_keys`, `tools`, `platforms`, more targets (`codex-global`, `mise-config`, `mise-lock`, `claude-settings`, `t3-settings`, `opencode-settings` (0.4.1), and file targets), and a private overlay. Terran 0.4 still reads schema 1 manifests, enrollments, and receipts and writes schema 2 when it next saves them; older binaries cannot read schema 2 state.

Therefore: every machine needs Terran 0.4 or newer before a catalog that uses v2 features reaches it. Update binaries first, catalogs second.

## Fleet order

1. `terran status` lists each machine's Terran version in the `TERRAN` column. Machines showing `incompatible terran` or `terran not found` have an old or missing binary.
2. Update the binary on each machine that needs it: on that machine, or from this one over SSH with the `terran-fleet` skill (download `install.sh` for the exact release to a temporary file, run it, delete it, then `.local/bin/terran version --json`).
3. Only then check out the new catalog revision on each machine.
4. On each machine: `terran version`, `terran plan --json`, show the user every action, get approval, `terran apply --expect <digest>`, then `terran status --local` and `terran doctor`.
5. Finish with `terran status` and confirm every row shows the expected version, matching catalog commits, and `clean`.

Harness and tool versions (OpenCode, Naru, Herdr, Claude Code, Codex, mise tools) are not Terran's. Compare them across machines with `terran-fleet` orientation and update them with the tool's own installer or `mise`, with the user's approval.

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
