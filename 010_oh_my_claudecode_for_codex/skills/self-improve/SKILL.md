---
name: self-improve
description: "Iteratively improve a codebase or process through measured candidate changes and tournament selection. Use only when the user explicitly requests autonomous self-improvement with a metric and stop condition."
---

# Self Improve

Require an objective metric, constraints, evaluation command, maximum iterations/runtime, and authorized change scope.

1. Capture baseline and repository state.
2. Generate a small set of distinct improvement hypotheses.
3. Evaluate candidates independently when isolation is available.
4. Reject any candidate that violates correctness, safety, compatibility, or declared constraints regardless of score.
5. Compare surviving candidates on the unchanged evaluator and keep only statistically credible improvements.
6. Record each hypothesis, diff, result, and decision under `.agents/self-improve/`.
7. Stop at the ceiling, target, cancellation, or genuine blocker and leave the best verified state.

Do not optimize the evaluator itself unless that is explicitly the mission.
