---
name: autoresearch
description: "Run a bounded evaluator-driven improvement loop with durable decisions and explicit stop conditions. Use when the user supplies a research or optimization mission, evaluator, and runtime or iteration ceiling."
---

# Autoresearch

Require a mission, measurable evaluator, and maximum runtime or iteration count. If any is absent and cannot be inferred safely, ask for it.

Store run artifacts under `.agents/autoresearch/<run-id>/`: mission, baseline, experiment log, evaluator outputs, and final result.

For each iteration:
1. Measure the unchanged baseline or current best.
2. Form one falsifiable hypothesis.
3. Make one bounded change or experiment.
4. Run the same evaluator and record raw evidence.
5. Keep the candidate only if it improves the declared objective without violating constraints; otherwise revert only the experiment's own changes.
6. Update the decision log and choose the next hypothesis.

Stop on success, ceiling exhaustion, explicit cancellation, or an unrecoverable blocker. Never redefine the evaluator after seeing a bad result.
