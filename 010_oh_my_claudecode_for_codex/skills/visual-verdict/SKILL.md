---
name: visual-verdict
description: "Compare generated UI screenshots against reference images and return actionable visual QA findings. Use when the user requests visual fidelity review, screenshot comparison, or a strict pass/fail verdict."
---

# Visual Verdict

Inspect every target and reference image at sufficient resolution.

Evaluate geometry, hierarchy, typography, spacing, colors, borders, shadows, assets, responsive behavior, clipping, and interaction states. Separate structural mismatches from cosmetic ones.

Return:
- verdict: PASS or FAIL;
- confidence from 0 to 1;
- prioritized mismatches with evidence and approximate location;
- smallest concrete edits likely to close the gap;
- items that cannot be judged from supplied images.

Do not claim pixel equivalence without an actual image comparison.
