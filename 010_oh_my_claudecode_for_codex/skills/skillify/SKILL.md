---
name: skillify
description: "Extract a reusable Codex skill from a successful workflow in the current conversation. Use when the user asks to skillify, capture, or turn a repeatable process into a skill."
---

# Skillify

Use the system `skill-creator` instructions as the source of truth.

1. Identify a workflow that was actually successful and likely to recur.
2. Separate general procedure from task-specific facts, secrets, and accidental details.
3. Define concrete trigger examples and non-triggers.
4. Keep `SKILL.md` concise; move only necessary scripts, references, or assets into resource folders.
5. Use only `name` and `description` in frontmatter and generate `agents/openai.yaml` with the official helper.
6. Test bundled scripts and run `quick_validate.py`.
7. Do not extract generic advice, one-off fixes, or unverified guesses.
