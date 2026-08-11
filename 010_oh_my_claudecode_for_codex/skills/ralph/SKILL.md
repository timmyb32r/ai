---
name: ralph
description: "Persist on a bounded task until explicit acceptance criteria are verified. Use when the user explicitly invokes ralph, says not to stop, or requests an iterative completion loop."
---

# Ralph

1. Convert the request into explicit acceptance criteria and a short checklist.
2. Inspect current state and choose the smallest next action that advances an unmet criterion.
3. Implement, test, and record evidence; do not count activity as progress.
4. On failure, diagnose the cause and change approach instead of repeating the same action blindly.
5. Continue while safe in-scope progress remains.
6. Finish only when every criterion has fresh verification evidence.
7. Stop for explicit cancellation, new authority, destructive ambiguity, or a genuine external blocker; report exact state and the smallest required user action.

Persistence does not broaden authorization.
