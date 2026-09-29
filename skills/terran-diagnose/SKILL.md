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
| `update` / `replace` | catalog changed | safe pending work; `replace` swaps a changed skill link |
| `remove` / `restore` | no longer in the catalog | created items are deleted; adopted originals are restored |
| `release` | adopted settings key left the catalog | ownership dropped, the value stays |
| `held` | pinned on this machine | never inspected or changed; `terran unhold <id>` to release |
| `excluded` | catalog item for another platform (reason is like `darwin-only`) | expected; not an error |
| `blocked_collision` | something differs and Terran does not own it | ask the user: replace or keep, after showing both versions |
| `blocked_drift` | Terran-owned content changed or vanished | stop; ask what outcome they want |

Statuses: `ok`, `missing`, `pending`, `orphaned`, `collision`, `drift`, `held`, `excluded`. Only `held` and `excluded` do not make status non-clean.

Resolving a collision: `terran apply --expect <digest> --decide <id>=replace|keep` (repeatable). `replace` backs up the original privately and installs the catalog version; `keep` holds the item. Only with the user's choice.

## Error codes

JSON errors look like `{"error":{"code":"...","message":"...","next":"..."}}`. Exit codes: 0 ok, 1 operational, 2 usage, 3 blocked.

| Code | Cause | Next step |
| --- | --- | --- |
| `not_enrolled` | no enrollment on this machine | `terran-provision` (`terran enroll --repo ... --name ...`) |
| `manifest_invalid` | bad `terran.json`, source, or duplicate item across catalogs | fix the named catalog, then `terran plan` |
| `receipt_invalid` | receipt unreadable or inconsistent | do not edit state; report to the user; `terran doctor` |
| `unsafe_state` | enrollment or state files have unsafe ownership, mode, or content | do not edit state; report to the user |
| `repository_mismatch` | enrolled catalog changed, or an overlay change while it owns applied items | restore the catalog or remove overlay-owned items and apply first |
| `overlay_unavailable` | enrolled private overlay is missing, moved, or invalid | clone it back to the recorded path (shown in `next`) or re-enroll; plan fails closed so nothing is removed |
| `plan_changed` | `--expect` digest no longer matches, or a settings file changed after planning | `terran plan --json` again, show the user, get approval again |
| `unknown_item` | `hold` or an item id that the plan does not contain | list ids with `terran plan --json` |
| `unreachable` | `terran status <name>` could not reach the machine over SSH | see fleet below |
| `usage` | bad flags, or `--decide` for an item that is not a `blocked_collision` | fix the command (exit 2) |
| `operational` | anything else | read `message` and `next` |

## Fleet

```sh
terran status                 # one row per Command Center
terran status <name>          # that machine's item-level status, read-only
terran status --summary --json  # this machine's one-line summary (what other machines run over ssh)
```

Rows: `CC`, `PLATFORM`, `TERRAN` (version), `CATALOG` and `OVERLAY` (short commits), `STATE`. `STATE` is `clean`, `clean (N held)`, `drift: N`, `blocked: N`, `unhealthy`, or a failure: `offline` (ssh failed), `terran not found` (nothing at `~/.local/bin/terran` there), `incompatible terran` (older binary or bad output). Unreachable machines are rows, not errors.

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
