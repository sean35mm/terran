---
name: terran-enroll
description: Guide Terran-specific enrollment for a new machine or new user-facing Command Center; do not use for generic onboarding, machine setup, or unrelated installers.
---

# Terran enrollment

If `terran` is absent, use a trusted, inspectable release installation or local source build from Terran's README. A skill cannot bootstrap itself when it is absent; stop rather than inventing a URL or command.

After bootstrap, inspect `terran --help`, then use `terran enroll --repo PATH [--name NAME]`, `terran plan`, `terran apply`, `terran status`, and `terran doctor` in that order. `plan` is read-only; `apply` is the explicit routine mutation boundary. Never use `--replace` unless replacing a different enrollment is the user's intent. The default catalog's full plan has 14 items: 12 skill links and two global instruction files.

Before applying, inspect every kind, source, fixed destination, action, and reason, including create, adopt, update, replace, restore, remove, or block. Stop on collision or drift rather than replacing unrelated content. Terran v0.1 manages enrollment, named skill symlinks, and fixed Claude/OpenCode global instruction copies; it does not manage profiles, services, hooks, packages, secrets, or remote repositories.
