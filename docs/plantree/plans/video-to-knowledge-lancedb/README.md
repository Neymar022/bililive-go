# Plan: Video To Knowledge LanceDB

## Scope

本计划跟踪“Bililive-go 视频生产链路 -> BiliNote 文档/知识生成 -> LanceDB 检索索引”的端到端闭环。当前权威需求来源是 [docs/PRD-video-to-knowledge-lancedb.md](../../../PRD-video-to-knowledge-lancedb.md)。

## Non-goals

- 不把 Bililive-go 变成长期知识库事实源。
- 不把 LanceDB 当成唯一事实源。
- 不在本计划中替代 BiliNote 仓库自己的实现计划。
- 不把生产 NAS 的临时排障日志塞入 roadmap；需要保留时放入 history 或运行报告。

## Baseline Links

- [模块地图](../../baseline/module-map.md)
- [运行流](../../baseline/runtime-flows.md)
- [存储与状态边界](../../baseline/storage-and-state.md)
- [测试与发布门禁](../../baseline/test-and-release-gates.md)
- [风险热点](../../baseline/risk-hotspots.md)

## File Map

- [roadmap.md](roadmap.md)： durable 阶段状态。
- [implementation-status.md](implementation-status.md)：当前执行交接状态。
- [open-questions.md](open-questions.md)：尚未解决的问题。
- [decisions/](decisions/)：已接受的跨仓库计划决策。
- [UGREEN inline live-session 分段迁移](topics/2026-07-29-ugreen-inline-live-segment-relocation.md)：影视中心错误归类的根因、历史修复、回滚和源码防复发证据。

## Decisions

- [0001: BiliNote Document-First ID And Retry Strategy](decisions/0001-bilinote-document-first-id-and-retry.md)

## Current Reading Path

1. 读本文件。
2. 读 [roadmap.md](roadmap.md) 判断阶段。
3. 读 [implementation-status.md](implementation-status.md) 接手当前工作。
4. 读 [decisions/](decisions/) 了解已定策略。
5. 读 [open-questions.md](open-questions.md) 避免把未定问题当作实现事实。
