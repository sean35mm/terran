---
name: terran-diagnose
description: Diagnose Terran enrollment, skill projection, PATH, drift, or health failures; do not use for generic shell, application, or network troubleshooting.
---

# Terran diagnosis

Start with `terran status [--target all|claude|agents|opencode]`, then `terran doctor`. Use `--json` when structured evidence is useful, but redact private absolute paths before sharing it. `status` distinguishes pending safe work from collision and drift; `doctor` also checks fixed instruction destinations, active/source hashes, receipt integrity, and private mode-0600 adoption backups.

Use `terran plan` for a read-only repair preview. Keep diagnosis separate from `terran apply`, the explicit mutation boundary. Never delete or replace a blocked leaf by hand merely to make status clean.

Preserve the secrets boundary: do not display, copy, persist, rotate, or "test" credentials unless the user explicitly authorizes the exact safe operation. Redact sensitive output. Do not delete drift or reset a machine merely to make a check pass.
