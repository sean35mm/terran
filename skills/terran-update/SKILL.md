---
name: terran-update
description: Plan a Terran binary or local catalog update; do not use for unrelated package upgrades or silently applying available updates.
---

# Terran updates

Terran v0.2 has no self-updater, clone, or fetch command. Review the pinned release, release notes, catalog version, `terran.json` diff, live skill source changes, copied global instruction changes, provenance, and licenses before installing a newer binary or changing the local repository. Skills change immediately with catalog checkout because they are symlinked; instructions change only through `terran apply`.

Keep the repository or binary update separate from projection changes. After an approved update, run `terran version`, `terran plan`, inspect every action, then run `terran apply`, `terran status`, and `terran doctor`. Do not silently resolve "latest" or apply available updates automatically.

Preserve pins outside the request. Terran does not manage packages, profiles, services, hooks, remote repositories, or secrets.
