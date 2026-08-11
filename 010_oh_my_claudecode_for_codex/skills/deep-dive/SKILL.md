---
name: deep-dive
description: "Investigate an ambiguous problem causally, then crystallize unresolved product or technical requirements. Use when the user asks for a deep dive combining evidence gathering with targeted clarification."
---

# Deep Dive

Phase 1 — trace:
1. Restate the observation separately from interpretations.
2. Generate competing hypotheses and the evidence that would support or falsify each.
3. Inspect code, logs, history, data, and authoritative docs as applicable.
4. Rank hypotheses by evidence strength and identify the highest-value discriminating probe.

Phase 2 — crystallize:
1. Ask only questions whose answers materially change scope, behavior, or acceptance criteria.
2. Challenge hidden assumptions and enumerate non-goals.
3. Produce a concise spec under `.agents/specs/` only if the user wants a durable artifact.

Do not implement a fix unless the user also asks for implementation.
