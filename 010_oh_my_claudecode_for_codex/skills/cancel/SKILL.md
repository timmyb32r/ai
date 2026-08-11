---
name: cancel
description: "Stop active Codex workflow work safely. Use when the user explicitly asks to cancel, stop, or abort ongoing delegated or iterative work."
---

# Cancel Workflow

1. Stop scheduling new work immediately.
2. If collaboration tools are active, list running agents and interrupt only agents belonging to the current task.
3. Terminate owned long-running command sessions gracefully; do not kill unrelated processes.
4. Preserve useful artifacts and partial changes unless the user explicitly requests their removal.
5. Do not delete branches, worktrees, state, or user files merely to cancel execution.
6. Report what stopped, what remains on disk, and any process that could not be stopped.
