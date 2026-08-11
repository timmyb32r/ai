---
name: merge-readiness
description: "Evaluate whether a completed change is understandable and safe to merge using implementation and verification evidence. Use after coding, tests, and review when the user wants a final readiness gate."
---

# Merge Readiness

Inspect the diff, acceptance criteria, tests, review findings, operational changes, and migration or rollback needs.

Produce:
- what changed and why;
- important design decisions and rejected alternatives;
- behavior and compatibility impact;
- fresh verification evidence;
- security, data, rollout, and rollback risks;
- missing evidence and unresolved questions;
- verdict: READY, READY WITH CONDITIONS, or NOT READY.

Do not merge, commit, push, or open a PR unless the user explicitly asks for that action.
