---
name: verify
description: "Verify that a feature, fix, refactor, artifact, or completion claim actually works. Use when the user asks to verify, validate, prove, check completion, or assess test adequacy."
---

# Verify

1. Translate the claim into observable acceptance criteria.
2. Inspect the implementation and identify the highest-risk failure modes.
3. Run the narrowest direct checks first, then related regression checks, static analysis, build, and runtime smoke tests as appropriate.
4. Prefer fresh raw output over existing reports or assumptions.
5. Exercise negative paths, boundaries, compatibility, and cleanup behavior proportional to risk.
6. Distinguish PASS, FAIL, and NOT VERIFIED; missing evidence is not a pass.
7. Report command/evidence, outcome, uncovered gaps, and residual risk without modifying code unless asked.
