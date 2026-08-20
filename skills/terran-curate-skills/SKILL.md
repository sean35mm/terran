---
name: terran-curate-skills
description: Add, adapt, remove, or audit skills owned or projected by Terran; do not use for ordinary prompt editing or unrelated agent configuration.
---

# Curate Terran skills

Edit the canonical skill source under `skills/<name>` and its projection in `terran.json`. Also review the complete canonical global instruction sources under `instructions/` whenever catalog behavior or agent policy changes. The manifest uses `schema_version`, `id`, `version`, skill projections, and only the fixed `claude-global` and `opencode-global` instruction targets. Never bulk-copy an upstream skill tree or homogenize harness-specific policies.

For every candidate, review source provenance and revision, license and notice duties, supported platforms, runtime dependencies, secret and network boundaries, trigger precision, and overlap with existing skills. Prefer a small attributed adaptation over bulk-copying an upstream skill. Preserve canonical-source and projection conventions found in the repository.

Remove a skill only with direct user intent or stronger evidence than zero observed calls; absence of telemetry does not prove it is unused. Run `terran plan`, inspect additions, removals, trigger and policy changes, instruction copies, collisions, and drift, then use `terran apply` only when authorized. Finish with `terran status` and `terran doctor`. Skills are live named symlinks; global instructions are fixed whole-file copies changed only by apply.
