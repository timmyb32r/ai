---
name: release
description: "Analyze repository-specific release rules and guide a safe release. Use when the user asks to prepare, cut, publish, or verify a versioned release."
---

# Release

1. Read repository instructions, CI workflows, version sources, changelog conventions, tags, and recent releases.
2. Derive a release checklist; cache it under `.agents/release-rules.md` only when the user wants durable project guidance.
3. Determine version impact and compatibility/migration notes.
4. Run the full declared release gate before mutation.
5. Show planned version, tag, artifacts, registry targets, and irreversible actions.
6. Require explicit authorization before publishing, pushing tags, or changing external release state.
7. Verify the published artifact and report URLs, checksums or versions, and rollback options.
