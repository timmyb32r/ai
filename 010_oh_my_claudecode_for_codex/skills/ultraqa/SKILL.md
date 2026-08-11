---
name: ultraqa
description: "Cycle through testing, diagnosis, fixing, and retesting until a declared quality goal passes. Use when the user explicitly requests ultraqa, autonomous QA cycling, or repeated verification and repair."
---

# UltraQA

1. Define the quality target and select the narrowest authoritative checks.
2. Run an unchanged baseline and capture exact failures.
3. Group failures by root cause; fix the smallest causal issue first.
4. Re-run the focused failing check, then broaden to related regression checks.
5. Repeat with a bounded cycle count; do not suppress tests, weaken assertions, or redefine success to get green.
6. On success, run the full proportionate gate and a real smoke test when practical.
7. Stop on success, explicit cancellation, ceiling exhaustion, or a genuine blocker; report remaining failures precisely.
