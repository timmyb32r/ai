---
name: team
description: "Coordinate multiple bounded agents over independent work items and synthesize their results. Use when the user explicitly asks for a team, sub-agents, delegation, or parallel agent work."
---

# Team

1. Decompose work into independent tasks with explicit inputs, outputs, ownership, and verification.
2. Respect the active session's concurrency limit; keep one slot for coordination when useful.
3. Give each agent minimum sufficient context and forbid scope expansion.
4. Avoid assigning overlapping writes to shared files. Prefer read-only research, isolated files, or explicit sequencing.
5. Monitor through available collaboration tools without busy polling; relay new evidence or course corrections promptly.
6. Review every returned result against the original request. The lead owns integration and final correctness.
7. Interrupt obsolete work and wait for all required tasks before claiming completion.

Delegation does not expand authorization.
