---
name: deepinit
description: "Create or update hierarchical AGENTS.md guidance for a codebase. Use when the user asks to initialize agent documentation or map repository conventions for future agents."
---

# Deep Init

1. Read every existing applicable `AGENTS.md` before writing.
2. Map the repository with bounded searches; skip vendored, generated, cache, build, and dependency directories.
3. Create a root document for global commands, architecture, conventions, safety, and verification.
4. Add nested `AGENTS.md` only where a subtree has genuinely different instructions.
5. Keep child guidance local; avoid copying parent content.
6. Cite real paths and verified commands. Never invent build or test instructions.
7. Validate hierarchy, contradictions, stale references, and document size.
