---
name: ultragoal
description: "Break a large brief into durable ordered goals with evidence checkpoints. Use when the user explicitly asks for an ultragoal or a multi-session, multi-goal execution ledger."
---

# Ultragoal

Create `.agents/ultragoal/<slug>/plan.md` and an append-only `ledger.md` only when durable state is useful.

1. Convert the brief into ordered goals with dependencies, acceptance criteria, and verification evidence.
2. Keep exactly one active goal unless independent goals are explicitly parallelized.
3. Append start, checkpoint, blocker, failure, retry, and completion events; never rewrite history to hide failures.
4. Resume from artifacts and current repository evidence, not from stale claims.
5. Mark a goal complete only after its acceptance evidence exists.
6. Report current goal, progress, next action, and blockers concisely.
