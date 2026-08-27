---
name: terran-enroll
description: Guide Terran-specific enrollment for a new machine or new user-facing Command Center; do not use for generic onboarding, machine setup, or unrelated installers.
---

# Terran enrollment

If `terran` is absent, use a trusted, inspectable release installation or local source build from Terran's README. A skill cannot bootstrap itself when it is absent; stop rather than inventing a URL or command.

After bootstrap, inspect `terran --help`, then use `terran enroll --repo PATH [--name NAME]`, `terran plan`, `terran apply`, `terran status`, and `terran doctor` in that order. `plan` is read-only; `apply` is the explicit routine mutation boundary. Never use `--replace` unless replacing a different enrollment is the user's intent. The default catalog's full plan has 15 items: 12 skill links, two global instruction files, and one global OpenCode config.

Before applying, inspect every kind, source, fixed destination, action, and reason, including create, adopt, update, replace, restore, remove, or block. Stop on drift and unsafe collisions. On a human terminal only, `terran apply` may ask to replace a safe differing unowned instruction or config with Terran's complete version and a private restoration backup, keep it unowned, or abort before mutation; non-TTY and JSON apply remain fail-closed and never prompt. Terran v0.1 manages enrollment, named skill symlinks, fixed Claude/OpenCode global instruction copies, and the fixed OpenCode config; it does not manage profiles, services, hooks, packages, secrets, or remote repositories.
