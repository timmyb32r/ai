---
name: autopilot
description: "Drive a well-scoped idea from requirements through implementation and verification. Use when the user explicitly asks for autopilot, full autonomous execution, or an end-to-end build with permission to modify the project."
---

# Autopilot

Treat the request as authorization for normal in-scope implementation, not for unrelated external actions.

1. Inspect project instructions and relevant code before deciding architecture.
2. Resolve only choices that materially change scope; infer safe local details.
3. Define acceptance criteria and a short dependency-aware plan.
4. Implement the smallest complete vertical slice, then finish remaining slices.
5. Parallelize independent read-only checks; use sub-agents only when explicitly requested and available.
6. Run proportional tests, lint, type checks, builds, and a real smoke test when practical.
7. Iterate on failures until acceptance criteria pass or a genuine authority/input blocker remains.
8. Finish with outcome, changed files, verification evidence, and residual risks.
