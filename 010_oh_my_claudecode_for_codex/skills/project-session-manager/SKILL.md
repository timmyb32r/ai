---
name: project-session-manager
description: "Manage isolated development workspaces using safe git worktrees and optional terminal sessions. Use when the user asks to create, inspect, attach to, or clean up an issue, PR, or feature workspace."
---

# Project Session Manager

1. Resolve repository, base branch, target name, and exact worktree path with read-only checks.
2. Prefer `codex/` branch names unless the user requests another convention.
3. Never reuse a dirty branch or overwrite an existing worktree.
4. Create worktrees with non-interactive git commands; keep terminal/tmux use optional.
5. Record the branch, worktree path, upstream/base, and purpose.
6. For cleanup, verify the exact target, uncommitted changes, and recoverability before removal; ask before destructive cleanup.
7. Do not delete remote branches or user work without explicit authorization.
