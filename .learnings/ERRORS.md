# Errors

## [ERR-20260911-001] task-code-explorer

**Logged**: 2026-09-11
**Priority**: low
**Status**: resolved
**Area**: docs

### Summary
调用内置 `code-explorer` 时显式传入 `subagent_path: builtin` 被拒绝。

### Error

```text
subagent_path not accessible: builtin
```

### Context

- 操作：启动只读子代理复核多份架构文档。
- 原因：`subagent_path` 是可选参数，内置代理不应把 `builtin` 作为可访问文件路径传入。

### Suggested Fix

调用内置 `code-explorer` 时只传 `subagent_name: code-explorer`，省略 `subagent_path`。

### Metadata

- Reproducible: yes
- Related Files: docs/00-project-brief.md, docs/01-product-architecture.md, docs/02-technical-architecture.md, docs/03-development-plan.md

### Resolution

- **Resolved**: 2026-09-11
- **Notes**: 后续调用省略 `subagent_path`。

---

## [ERR-20260911-002] replace-in-file-exact-match

**Logged**: 2026-09-11
**Priority**: low
**Status**: resolved
**Area**: docs

### Summary

架构文档批量替换因手工抄写旧文本时漏掉一个斜杠而未命中。

### Error

```text
The string to replace was not found in the file.
```

### Context

- 目标：更新 `docs/02-technical-architecture.md` 的架构目标与 ADR。
- 原因：旧文本把“单身/夫妻/带娃”误写为“单身/夫妻带娃”。

### Suggested Fix

替换较大文本前重新读取目标区段，并直接复制精确原文构造 `old_str`。

### Metadata

- Reproducible: yes
- Related Files: docs/02-technical-architecture.md

### Resolution

- **Resolved**: 2026-09-11
- **Notes**: 已重新读取目标区段并使用精确原文重试。

---

## [ERR-20260911-003] parallel-same-file-edit-lock

**Logged**: 2026-09-11
**Priority**: low
**Status**: resolved
**Area**: docs

### Summary

并行提交多个针对同一 Markdown 文件的替换，导致其中一个操作等待写锁超时。

### Error

```text
Acquire write lock on docs/03-development-plan.md timeout after 30000ms
```

### Context

- 操作：并行同步多个 WAL/容量术语。
- 原因：这些编辑逻辑独立但目标文件相同，会竞争文件写锁，不应并行执行。

### Suggested Fix

只并行编辑不同文件；同一文件上的多个替换必须顺序执行或合并为一次替换。失败后重新读取目标区段再重试。

### Metadata

- Reproducible: yes
- Related Files: docs/03-development-plan.md

### Resolution

- **Resolved**: 2026-09-11
- **Notes**: 已重新读取目标区段并顺序重试。

---
