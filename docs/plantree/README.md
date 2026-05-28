# Plan Tree

这是本仓库的计划文档入口。`docs/plantree/` 只维护规划、状态、决策和问题，不替代代码、PRD、运行日志或发布说明。

## 权威顺序

1. 用户当前明确指令。
2. `AGENTS.md` 中的开发和验证规则。
3. 已合并代码、生产配置、运行态证据。
4. 本目录中的 baseline、roadmap、implementation-status、open questions。
5. 历史 PRD、聊天记录和临时分析。

## 如何阅读

1. 先看 [baseline/README.md](baseline/README.md) 了解项目上下文。
2. 再看对应计划根目录的 `README.md`。
3. 执行或接手任务时看该计划的 `implementation-status.md`。
4. 遇到范围不清时看 `open-questions.md`，不要把未解问题当成已定任务。

## Baseline

- [baseline/README.md](baseline/README.md)
- [baseline/module-map.md](baseline/module-map.md)
- [baseline/runtime-flows.md](baseline/runtime-flows.md)
- [baseline/storage-and-state.md](baseline/storage-and-state.md)
- [baseline/test-and-release-gates.md](baseline/test-and-release-gates.md)
- [baseline/risk-hotspots.md](baseline/risk-hotspots.md)

## Active Plans

| Plan | Scope | Status | Source |
|---|---|---|---|
| [video-to-knowledge-lancedb](plans/video-to-knowledge-lancedb/README.md) | Bililive-go 录制/字幕/烧录产物同步到 BiliNote 与 LanceDB 知识库 | In Progress | [docs/PRD-video-to-knowledge-lancedb.md](../PRD-video-to-knowledge-lancedb.md) |

## Ideas

- [ideas/inbox.md](ideas/inbox.md)

## 维护规则

- 新计划必须注册到 `Active Plans`。
- 项目级事实写入 `baseline/`，具体计划只链接，不复制大段上下文。
- `roadmap.md` 只保留 Done/In Progress/Next/Deferred 等 durable 状态。
- `implementation-status.md` 只写当前交接状态、下一步和最近验证，不变成第二份 roadmap。
- 已解决问题应从 `open-questions.md` 移出或转成决策记录。
