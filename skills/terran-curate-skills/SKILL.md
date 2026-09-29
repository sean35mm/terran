---
name: terran-curate-skills
description: Add, adapt, remove, or audit skills and other items (files, settings keys, instructions, configs, tools) owned or projected by Terran catalogs; do not use for ordinary prompt editing or unrelated agent configuration.
---

# Curate Terran catalogs

Two catalogs can be enrolled on a machine: the public catalog and an optional private overlay. The overlay only adds items; it cannot redefine an item the public catalog declares. Treat everything in the public catalog as public source: no credentials, personal names, absolute paths, emails, local-only URLs, or machine state.

Never push. Never edit a Terran-managed destination directly; change the catalog source, then plan and apply.

## Start from what is on the machine

```sh
terran capture --json
```

Each item has an `id`, a `kind` (`unmanaged_entry` or `unowned_key`), a `target`, and a `name`. Values are never printed. Held, already-owned, hidden, and `naru-` prefixed entries are skipped. Read the item yourself from its normal location when you need its content, and never copy a secret into a catalog.

For each item decide:

1. **Public or private.** Public only if the license allows redistribution and nothing in it is personal, secret, or machine-specific. Everything else goes in the private overlay. When unsure, choose private and say why.
2. **Platform tags.** Add `"platforms": ["darwin"]` or `["linux"]` when the item only works on one. No `platforms` means every platform. Items for other platforms appear as `excluded`, which is expected.
3. **Keep, rewrite, or drop.** Keep as is when it is precise and safe; rewrite to fix trigger scope, provenance, or portability; drop when redundant or stale. Removing an item needs the user's intent, not just missing telemetry.

For every candidate review provenance and revision, license and notice duties (update `THIRD_PARTY_NOTICES.md` and keep the upstream license file with adapted skills), supported platforms, runtime dependencies, secret and network boundaries, and overlap with existing items. Prefer a small attributed adaptation to bulk-copying an upstream tree.

## Where each item goes

Edit `terran.json` in the chosen catalog. Sources are clean relative paths inside that catalog; destinations are fixed in Terran by target id and cannot be declared.

| Manifest key | Item | Targets |
| --- | --- | --- |
| `projections` | skill in `skills/<name>/SKILL.md` (frontmatter `name` must match) | `agents`, `claude` |
| `instructions` | complete global instruction file | `claude-global`, `opencode-global`, `codex-global` |
| `configs` | whole config file (strict, sanitized) | `opencode-config`, `naru-runtime`, `mise-config`, `mise-lock` |
| `files` (`target`, `name`, `source`) | one named file in a fixed directory | `claude-agent`, `claude-command`, `claude-hook`, `opencode-plugin`, `opencode-tool`, `opencode-command` |
| `json_keys` | owned top-level keys of a shared settings file | `claude-settings`, `t3-settings` |
| `tools` (`name`) | CLI that `terran doctor` requires on `PATH` | none |

Every entry except `tools` takes `source`; every entry can take `platforms`. For `json_keys`, the source is a JSON object holding only the keys Terran should own; Terran preserves every other key in the settings file. Use sorted keys in any JSON you write. Do not add targets, commands, or destinations to Terran itself.

Skill names match `^[a-z0-9][a-z0-9-]{0,63}$`. File names match `^[a-z0-9][a-z0-9._-]{0,127}$` with the extension the target allows.

Write skill descriptions as triggers, not summaries: say when to use the skill and when not to. End each `SKILL.md` with a short "Should trigger" and "Should not trigger" prompt list so overlap with other skills is visible.

## Apply the change

Follow the Agent guide in the Terran README:

1. `terran plan --json` and show the user every action. Expect `create` for new items, `adopt` when a machine already has identical content, `blocked_collision` when it has different content, `excluded` for other-platform items.
2. Get explicit approval.
3. `terran apply --expect <digest>`. Decide collisions with `--decide <id>=replace|keep` only after the user has seen both versions and chosen.
4. `terran status --local` and `terran doctor`.

Machine-local items that a catalog item must not touch: `terran hold <id>`; undo with `terran unhold <id>`.

Removing an item: delete its manifest entry and source, then plan. Expect `remove` for created items, `restore` for adopted ones, and `release` for adopted settings keys (ownership dropped, value kept). Skills are live symlinks into the catalog checkout; instructions, configs, files, and settings keys change only through apply. Commit in the catalog only when the user asks; then tell them to push.

## Should trigger

- "Add this skill to the catalog."
- "Which of the things on this machine should go into Terran?"
- "Make this MCP-related setting part of every machine, but keep the token private."
- "Drop the old skill and audit what the catalog still projects."

## Should not trigger

- "Rewrite the wording of this prompt."
- "Install this skill just for this session."
- "Configure my editor."
- "Set up a new machine." (use `terran-provision`)
