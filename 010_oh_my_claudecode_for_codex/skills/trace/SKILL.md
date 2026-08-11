---
name: trace
description: "Explain an observed result using competing causal hypotheses and concrete evidence. Use when the user asks why something happened, requests tracing, or needs uncertainty-aware root-cause analysis without an immediate fix."
---

# Trace

1. State the observation exactly and separate it from interpretations.
2. Generate at least two plausible hypotheses, including a mundane alternative.
3. For each, list predicted evidence and disconfirming evidence.
4. Gather evidence from primary artifacts: code, logs, data, history, reproducible probes, then authoritative docs.
5. Rank hypotheses by evidence strength, not narrative appeal.
6. State uncertainty and the critical missing fact.
7. Recommend the lowest-cost discriminating probe.

Do not implement a fix unless the user asks for one.
