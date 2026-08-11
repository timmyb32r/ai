---
name: wiki
description: "Maintain a persistent cross-linked Markdown knowledge base for a project. Use when the user asks to wiki knowledge, ingest durable facts, query project knowledge, or lint knowledge pages."
---

# Project Wiki

Store pages under `.agents/wiki/` with stable filenames and relative links.

Operations:
- ingest: extract durable verified facts and merge them into the narrowest existing pages;
- query: answer from wiki plus current sources, marking stale or conflicting entries;
- lint: detect broken links, duplicates, orphan pages, unsupported claims, and obsolete paths;
- list/read/delete: inspect first and require clear target scope before deletion.

Prefer concise facts with source paths, dates, and version context. Never persist secrets, personal data, transient logs, or speculation as fact.
