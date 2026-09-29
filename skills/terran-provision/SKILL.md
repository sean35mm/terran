---
name: terran-provision
description: Set up or bring up to date a machine as a Terran Command Center (fresh or partly set up); do not use for generic OS setup unrelated to Terran.
---

# Provision a Terran Command Center

A Command Center is one machine enrolled in Terran. Terran projects the public catalog and the user's optional private overlay onto fixed destinations; everything else on the machine (packages, logins, MCP servers) is set up by you, one approved step at a time.

Command Center names are a convention the user chooses (for example `cc1`, `cc2`). Never assume a scheme; propose the next free name only when the private inventory shows one, and let the user confirm or change it.

## Rules

- Every install command needs the user's approval before you run it. Show the exact command first.
- Never push to git. Commit in the overlay only when the user asks, then tell them to push.
- Never edit Terran-managed destinations (`~/.claude/CLAUDE.md`, `~/.claude/settings.json` keys Terran owns, skill links, `mise` config) directly. Change the catalog and apply.
- Never read, print, or store secrets, tokens, or private keys. The user performs every login.
- Always `terran plan --json`, show the user the plan, get explicit approval, then `terran apply --expect <digest>`. Use `--decide <id>=replace|keep` only for a `blocked_collision` after the user has seen both versions and chosen.
- Explain every `excluded` item to the user (it does not apply to this platform) and every `held` item (pinned on this machine).
- Use only catalog and overlay URLs the user supplied. Never invent a URL, tag, or version.

## Route

1. Run `terran doctor --json`. If `terran` is missing, or doctor reports `not_enrolled` or missing tools, the machine is fresh or partial.
2. Fresh machine (no `terran`, or not enrolled): follow `references/fresh.md`.
3. Partly set up (enrolled, or other agent setup already present): follow `references/existing.md`.
4. Platform commands live in `references/darwin.md` (macOS) and `references/omarchy.md` (Omarchy and other Arch Linux).

## Stages

1. Preflight: OS and architecture, next free Command Center name from the private inventory if present, Tailscale.
2. Base tools: darwin installs Xcode Command Line Tools then mise; Arch installs `base-devel git openssh tailscale` with pacman then mise. mise installs every CLI tool. Homebrew is only for macOS GUI casks.
3. Clone the public catalog and the user's private overlay.
4. Install Terran from a release with `install.sh`, or build from source.
5. `terran enroll --repo <public> --overlay <private> --name <name>`.
6. `terran plan --json`, show the user, get approval, `terran apply --expect <digest>` (with `--decide` only as the user decides).
7. `mise install`, then the private checklist `provision/checklist.md` in the overlay (harness plugin installers such as Naru, Claude plugins, MCP servers via `claude mcp add --scope user` and `codex mcp add`, Codex `config.toml` entries).
8. Logins, done by the user.
9. Fleet access: the Tailscale machine name equals the Command Center name; macOS Remote Login; Linux `tailscale up --ssh`; add the machine to `command-centers.json` in the overlay, commit, ask the user to push.
10. Verify: `terran doctor`, `terran status`, `claude mcp list`, `codex mcp list`, and a smoke prompt per harness.

Items that should flow back into a catalog go through `terran-curate-skills`. Failures go through `terran-diagnose`.

## Should trigger

- "Set this machine up as a Terran Command Center."
- "Bring cc2 up to date with the catalog."
- "This laptop is half set up; finish provisioning it with Terran."
- "Add this new Mac to my Terran fleet."

## Should not trigger

- "Install Homebrew" or other OS setup with no Terran involved.
- "Why does `terran doctor` fail?" (use `terran-diagnose`).
- "Add this skill to the catalog." (use `terran-curate-skills`).
- "Upgrade the Terran binary." (use `terran-update`).
