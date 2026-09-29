# Changelog

All notable changes are documented here. Terran follows semantic versioning.

## 0.4.0 - Unreleased

Breaking:

- Remove the interactive guided workflow (bare `terran` wizard) and every terminal prompt. Terran is now agent-only: it never prompts, detects terminals, or reads stdin. Collision decisions are flags: `terran apply --decide ITEM_ID=replace|keep`.
- Manifest, enrollment, and receipt schema version 2. Schema 1 files are still read and upgraded in memory, then saved as schema 2. JSON output reports `schema_version: 2`. The default catalog's `terran.json` is itself schema version 2, so Terran 0.3 binaries cannot read it at all: upgrade the binary on every machine to 0.4 before pulling this catalog.
- Remove the `terran-enroll` skill; `terran-provision` replaces it.

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
