---
name: terran-diagnose
description: Diagnose Terran enrollment, projection, PATH, drift, held or excluded items, fleet status, or health failures; do not use for generic shell, application, or network troubleshooting.
---

# Terran diagnosis

Diagnosis is read-only. `terran apply` is the only mutation, and only with the user's approval. Never delete, move, or overwrite a blocked item by hand to make status clean. Never display or copy credentials. Redact private absolute paths before sharing output.

## Order

```sh
terran doctor --json          # exit 1 when unhealthy
terran status --local --json  # item-level state, exit 1 when not clean
terran plan --json            # read-only repair preview, exit 3 when blocked
```

Add `--target agents|claude|opencode|codex|mise|t3` to narrow. `doctor` also reports `tool:<name>` failures for tools the catalog requires; the fix is the tools step of `terran-provision`.

## Item states

`terran status --local` maps plan actions to statuses. Plan actions:

| Action | Meaning | Tell the user |
| --- | --- | --- |
| `noop` | up to date | nothing to do |
| `create` | missing, will be created | safe pending work |
| `adopt` | identical item already exists | Terran takes ownership, active file untouched |
| `update` | catalog changed | safe pending work; the reason `convert live symlink to managed copy` is the one-time upgrade of a 0.3 skill link; `recover interrupted apply (content already matches catalog)` only records a copy an interrupted apply already installed; `destination already matches catalog; record it` only records an owned file or settings key whose local edit was synced into the catalog |
| `replace` | a collision decided `replace` | the original is backed up privately |
| `remove` / `restore` | no longer in the catalog | created items are deleted; adopted originals are restored |
| `release` | adopted settings key or skill left the catalog | ownership dropped, the value or directory stays |
| `held` | pinned on this machine | never inspected or changed; `terran unhold <id>` to release |
| `excluded` | catalog item for another platform (reason is like `darwin-only`) | expected; not an error |
| `blocked_collision` | something differs and Terran does not own it | ask the user: replace or keep, after showing both versions |
| `blocked_drift` | Terran-owned content changed or vanished (for a skill copy: an edited, missing, or re-moded file) | stop; ask what outcome they want |

Statuses: `ok`, `missing`, `pending`, `orphaned`, `collision`, `drift`, `held`, `excluded`. Only `held` and `excluded` do not make status non-clean.

Stale records and drift: `terran forget <id>` drops Terran's record of one item (its receipt entry and hold) and leaves the destination and any backup as they are. Use it for a hold or record whose item left the catalog long ago (a deleted skill still held, a removed config still held). For drift where the user wants the catalog version, `terran forget <id>`, then `terran plan --json` shows the item as `blocked_collision`, then `terran apply --expect <digest> --decide <id>=replace` installs the catalog version with a private backup of the drifted file. Forgetting an adopted item also drops the reference to its original backup; report the backup path `forget` prints. Only with the user's choice, since it is how drift gets overwritten.

Resolving a collision: `terran apply --expect <digest> --decide <id>=replace|keep` (repeatable). `replace` backs up the original privately and installs the catalog version (a skill directory or symlink is moved into Terran's private backups, replaced by a managed copy, and not restored on removal); `keep` holds the item. Only with the user's choice. A replace Terran cannot do stays `blocked_collision` with a reason starting `replace not possible:`.

An `interrupted_apply` doctor warning names a leftover `.terran-tmp-*`, `.terran-old-*`, or `.terran-quarantine-*` entry; Terran never deletes it. A leftover quarantine blocks every item for its directory with `leftover Terran quarantine found at <path>`: it may hold the only copy of displaced bytes, so the user inspects and removes it, not you.

Two reasons need the user rather than a decision: `destination path contains a symlink` (a directory below `HOME`, `XDG_CONFIG_HOME`, or `CODEX_HOME` on the way to the destination is a symlink) and `CODEX_HOME changed` (drift on `codex-global` after `CODEX_HOME` moved).

## Error codes

JSON errors look like `{"error":{"code":"...","message":"...","next":"..."}}`; `message` is the command context plus the full error text. Exit codes: 0 ok, 1 operational, 2 usage, 3 blocked.

| Code | Cause | Next step |
| --- | --- | --- |
| `not_enrolled` | no enrollment on this machine | `terran-provision` (`terran enroll --repo ... --name ...`) |
| `manifest_invalid` | bad `terran.json`, source (including a skill file or directory that is not world-readable), duplicate item across catalogs, or two items resolving to one destination (such as `CODEX_HOME` set to the OpenCode directory) | fix the named catalog or environment variable, then `terran plan` |
| `receipt_invalid` | receipt unreadable or inconsistent | do not edit state; report to the user; `terran doctor` |
| `unsafe_state` | enrollment or state files have unsafe ownership, mode, or content | do not edit state; report to the user |
| `repository_mismatch` | enrolled catalog changed, or an overlay change while it owns applied items | restore the catalog or remove overlay-owned items and apply first |
| `overlay_unavailable` | enrolled private overlay is missing, moved, or invalid | clone it back to the recorded path (shown in `next`) or re-enroll; plan fails closed so nothing is removed |
| `plan_changed` | `--expect` digest no longer matches (including a colliding destination edited after review), or a settings file changed after planning or while apply wrote it | `terran plan --json` again, show the user, get approval again |
| `partial_apply` | apply committed its changes and receipt, but the holds for `keep` decisions were not saved | tell the user the changes are in place; `terran hold <id>` for each kept item named in `message`, then `terran plan --json` |
| `unknown_item` | `hold` or `forget` with an item id that the plan, receipt, and holds do not contain | list ids with `terran plan --json` and `terran status --local --json` |
| `unreachable` | `terran status <name>` could not reach the machine over SSH | see fleet below |
| `usage` | bad flags, or `--decide` for an item that is not a `blocked_collision` | fix the command (exit 2) |
| `operational` | anything else | read `message` and `next` |

## Fleet

```sh
terran status                 # one row per Command Center
terran status <name>          # that machine's item-level status, read-only
terran status --summary --json  # this machine's one-line summary (what other machines run over ssh)
```

Rows: `CC`, `PLATFORM`, `TERRAN` (version), `CATALOG` and `OVERLAY` (short commits), `STATE`. `STATE` is `clean`, `clean (N held)`, `drift: N`, `blocked: N`, `unhealthy`, or a failure: `offline` (ssh failed), `terran not found` (nothing at `~/.local/bin/terran` there), `incompatible terran` (usually an older binary, such as 0.3, which has no `status --summary`; plan a `terran-update` of that machine's binary instead of stopping), or `<code>: <message>` (the remote Terran returned that error, for example `not_enrolled: ...`). Unreachable machines are rows, not errors. A reachable row may end with `, N tools missing` (`tools_missing` in JSON): catalog tools not on that machine's non-interactive ssh `PATH`. That alone is not unhealthy; check on the machine itself before running the `terran-provision` tools step.

Remote status is read-only in this release. To fix a remote machine, work on that machine (ask the user to open an agent there) or use `terran-provision`. Mismatched `CATALOG` or `OVERLAY` commits mean the clone on that machine is behind; updating it is a `terran-update` task.

Common causes: ssh not enabled or not authorized on the remote (`offline`), binary not installed at `~/.local/bin/terran`, Tailscale name differs from the `ssh` value in `command-centers.json`, or the overlay clone missing on that machine (`overlay_unavailable`).

## Should trigger

- "terran doctor says unhealthy, why?"
- "What's wrong with this machine?"
- "cc3 shows drift in the fleet table."
- "Why is this skill shown as excluded or held?"
- "apply failed with plan_changed."

## Should not trigger

- "My network is slow."
- "Debug this app's crash."
- "Install a new tool." (use `terran-provision`)
- "Add a skill to the catalog." (use `terran-curate-skills`)
