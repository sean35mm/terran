# Changelog

All notable changes are documented here. Terran follows semantic versioning.

## 0.4.4 - 2026-09-30

- Add `terran forget ITEM_ID [--json]`: drops the receipt entry and hold for one item without touching its destination or backups; use it to clear stale holds, or `forget` then `apply --decide ID=replace` to take the catalog version over drift.
- The plan digest now covers file contents inside a colliding skill directory, so editing one after review fails `apply --expect` with `plan_changed`.
- `terran status` and `status --summary --json` report uncommitted, ahead, and behind counts for the catalog and overlay checkouts (`catalog_dirty`, `catalog_ahead`, `catalog_behind`, `overlay_*`; no fetch; omitted when unknown). A 0.4.3 machine shows a 0.4.4 machine as `incompatible terran` until its own binary is updated; update binaries first.
- `terran doctor` `binary_path` says when terran is installed at `~/.local/bin/terran` and only the shell's PATH lacks it.
- A test checks that every `terran` command and flag named in the skills and README exists.
- Skills: `terran-fleet` gains "Remove a machine from the fleet", harness version comparison, and overlay push-conflict handling; `terran-curate-skills` gains "Sync this machine into the catalog"; `terran-diagnose` documents `forget`; every Terran skill tells agents to follow the freshly fetched catalog copy of a skill when the installed one lags.
- Default catalog: manage the global Codex instructions (`codex-global`).

## 0.4.3 - 2026-09-30

- New `ssh-config` (`~/.ssh/terran-fleet.conf`) and `ssh-authorized-keys` (`~/.ssh/authorized_keys`) config targets, mode 0600, accepted only from a private overlay catalog. `ssh-config` sources may hold only literal `Host` blocks with `HostName`, `User`, `Port`, `IdentityFile ~/.ssh/<name>`, and `IdentitiesOnly`; `ssh-authorized-keys` sources only public key lines that each carry a literal `from="..."` limit and at most `restrict` or `no-*` flags. Private keys, commands, wildcards, and absolute machine paths are rejected.
- `command-centers.json` accepts an optional `user` per machine; `terran status` passes it to ssh as `-o User=`.
- Skills: joining the fleet is agent-only after the machine is on Tailscale. Fleet public keys, the ssh config fragment, and authorized keys live in the private overlay and reach every machine through plan and apply; agents may commit and push fleet-membership changes to the private overlay; provisioning and fleet runs support an unattended mode that stops only for logins, secrets, destructive steps, and differing collisions.

## 0.4.2 - 2026-09-30

- New skill `terran-dispatch`: start an agent in a fresh worktree on another Command Center through Herdr's saved machines (`herdr --machine`), watch it with local notifications, message it, show every machine's agents in one table, bring its branch back with `git fetch` over SSH, and hand a task off to another machine with a note.
- `terran-fleet` gains "Connect the fleet": SSH and Herdr links between every pair of Command Centers, including per-host usernames and a dedicated fleet key for Macs limited to Tailscale addresses. `terran-provision` runs it when a machine joins, so new machines are linked both ways.

## 0.4.1 - 2026-09-29

- An owned instruction, config, file, or settings key whose destination already matches the catalog plans `update` with the reason `destination already matches catalog; record it`, and apply writes only the receipt. Editing on one machine and syncing the edit into the catalog no longer leaves that machine at `blocked_drift`.
- New `opencode-settings` json-keys target for `opencode.json`, so Terran owns chosen top-level keys while Naru and per-machine entries (`agents`, `plugins`, `skills`, `mcp`) stay untouched. It cannot be combined with `opencode-config`; a machine that owns the whole-file `opencode-config` plans `release` for it when its catalog switches to `opencode-settings`, keeping the file.
- Default catalog: sync the global Claude and OpenCode instructions from cc1; replace the whole-file `opencode-config` with `opencode-settings` (`$schema`, `agent`, `command`, `default_agent`, `experimental`, `permission`, `providers`, `shell`); drop the obsolete `naru-runtime` config (Naru 0.11 no longer uses it).

## 0.4.0 - 2026-09-29

Breaking:

- Remove the interactive guided workflow (bare `terran` wizard) and every terminal prompt. Terran is now agent-only: it never prompts, detects terminals, or reads stdin. Collision decisions are flags: `terran apply --decide ITEM_ID=replace|keep`.
- Manifest, enrollment, and receipt schema version 2. Schema 1 files are still read and upgraded in memory, then saved as schema 2. JSON output reports `schema_version: 2`. The default catalog's `terran.json` is itself schema version 2, so Terran 0.3 binaries cannot read it at all: upgrade the binary on every machine to 0.4 before pulling this catalog.
- Remove the `terran-enroll` skill; `terran-provision` replaces it.
- Skills are managed directory copies instead of live symlinks, so skill content changes only through a reviewed `terran plan` and `terran apply`. The first 0.4 apply converts every exact receipt-owned 0.3 symlink to an identical copy (planned as `update`, `convert live symlink to managed copy`). Skill receipts record the applied tree hash; an edited, missing, or re-moded copy is `blocked_drift`. Skill sources may contain only regular files and directories, at most 2000 entries and 32 MiB per skill. An existing symlink at a skill destination is no longer adopted; an identical real directory is, and is released (kept) when the skill leaves the catalog.

Added:

- `--json` on every command, with stable error objects (`code`, `message` with the full error text, `next`) and distinct exit codes: 0 ok, 1 operational, 2 usage, 3 blocked. `partial_apply` reports an apply that committed but could not save the holds for `keep` decisions.
- Reviewed applies: `terran plan --json` reports a digest and `terran apply --expect DIGEST` fails with `plan_changed` if anything moved.
- New targets: `codex-global`, `mise-config`, `mise-lock`, `claude-settings`, `t3-settings`, and named files for `claude-agent`, `claude-command`, `claude-hook`, `opencode-plugin`, `opencode-tool`, and `opencode-command`.
- Catalog sections `files`, `json_keys` (per-key ownership of shared JSON settings files, preserving every other key), `tools` (checked by `terran doctor`), and per-item `platforms` (other-platform items plan as `excluded`; adding `platforms` to an item a machine already owns plans `remove`, `restore`, or `release` there instead).
- A private overlay catalog (`terran enroll --overlay`) that can only add items, with `overlay_unavailable` failing closed.
- `terran hold` and `terran unhold` to pin items on one machine, and the `held`, `excluded`, and `release` actions.
- `terran capture` to list unmanaged agent setup on this machine without printing values.
- Fleet status: `command-centers.json` in the overlay, `terran status` (table or JSON), `terran status NAME` (read-only remote item status), `terran status --local`, and `terran status --summary`, using a fixed `ssh` command. Summaries report `tools_missing`, shown as `, N tools missing` without marking the machine unhealthy; a remote JSON error shows as `<code>: <message>`.
- New skill `terran-provision` (fresh and existing machines, macOS and Omarchy references). Rewritten `terran-curate-skills`, `terran-diagnose`, and `terran-update` for the agent-first workflow.
- New skill `terran-fleet`: an agent updates, diagnoses, and fixes other Command Centers over SSH with the same plan, approval, and `--expect` loop, never pushing to git. There are no `terran cc` wrapper commands in 0.4.
- Agent-first README and documentation site; expanded `SECURITY.md`.

## 0.3.0 - 2026-09-22

- Remove the `plainspoken-writing` skill from the default catalog.
- Sync curated OpenCode and global instruction defaults; manage the Naru runtime configuration as a fixed OpenCode target.

## 0.2.0 - 2026-08-27

- Add strict, fixed-target whole-file management for a sanitized global OpenCode `opencode.json`, with distinct config receipts and the same collision, drift, adoption, backup, restoration, status, doctor, and rollback guarantees as global instructions.
- Preserve Naru agent selection without treating Naru as an npm plugin or coupling its separately managed version to Terran's catalog version.
- Add terminal-only replace/keep/quit resolution for safely backable unowned instruction and config collisions, with adopted restoration metadata, all-decisions preflight, and fail-closed noninteractive behavior.
- Add a state-aware guided workflow for humans running bare `terran`, with trusted local catalog enrollment, grouped review, locked final confirmation, and post-apply verification while preserving deterministic advanced and JSON commands.

## 0.1.0 - Initial release

- Add strict local catalog enrollment for a Command Center.
- Add safe named symlink planning, adoption, application, status, and removal for agents and Claude skill targets.
- Add fixed-target whole-file management for Claude and OpenCode global instructions, including exact adoption backups, drift-safe updates, restoration, cleanup, and transactional rollback with skill mutations.
- Add receipts, advisory locking, XDG state, diagnostics, verified release installation, bundled skills, and harness-specific global instruction sources.
