---
name: ultrawork
description: "Execute multiple independent work items with high-throughput parallelism. Use when the user explicitly asks for ultrawork, parallel execution, or concurrent sub-agent work."
---

# Ultrawork

1. Ground scope and acceptance criteria before parallelizing.
2. Build a dependency graph and separate independent from sequential work.
3. Batch independent tool calls. Use sub-agents only because this skill was explicitly invoked and only when collaboration tools are available.
4. Assign disjoint write ownership and precise return contracts.
5. Keep critical-path work local when coordination would cost more than execution.
6. Integrate returned results, resolve conflicts, and run end-to-end verification.
7. Report parallel work, integration decisions, evidence, and unfinished dependencies.
