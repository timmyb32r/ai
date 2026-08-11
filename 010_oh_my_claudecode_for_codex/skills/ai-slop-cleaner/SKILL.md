---
name: ai-slop-cleaner
description: "Clean AI-generated code bloat with a regression-safe, deletion-first workflow. Use when the user asks to deslop, remove needless abstractions, deduplicate, or simplify generated code without changing behavior."
---

# AI Slop Cleaner

1. Establish the exact behavioral baseline with focused tests or reproducible checks.
2. Identify duplication, dead paths, wrapper-only abstractions, speculative configurability, and comments that restate code.
3. Write a bounded cleanup plan before editing.
4. Prefer deletion, reuse, and local simplification. Do not broaden scope or add dependencies.
5. Apply small reversible edits; preserve public APIs unless explicitly authorized.
6. Run the baseline checks plus lint, type checks, and static analysis relevant to changed files.
7. Report deleted complexity, behavior evidence, and remaining risks.

For review-only requests, inspect and report findings without modifying files.
