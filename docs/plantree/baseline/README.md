# Baseline

本 baseline 记录 Bililive-go 仓库中相对稳定的事实，供计划和实现接手时快速定向。它只写已从仓库文件或当前 PRD 中确认的内容；不确定项写入计划的 open questions。

## 项目定位

- Bililive-go 是多平台直播录制工具，核心能力是监听直播、录制、修复/转码、提供 Web 管理界面和通知能力。
- 当前扩展方向是把录制完成的视频、字幕和元数据同步到 BiliNote，由 BiliNote 负责知识抽取、检索和生命周期管理。
- 本仓库侧不应承担长期知识库事实源或向量索引职责。

## 当前主要入口

- 项目说明：[../../../README.md](../../../README.md)
- AI 开发规则：[../../../AGENTS.md](../../../AGENTS.md)
- 视频到知识库 PRD：[../../PRD-video-to-knowledge-lancedb.md](../../PRD-video-to-knowledge-lancedb.md)
- 构建和验证命令：[module-map.md](module-map.md), [test-and-release-gates.md](test-and-release-gates.md)

## 当前计划入口

- [video-to-knowledge-lancedb](../plans/video-to-knowledge-lancedb/README.md)

## 维护说明

- 本目录描述全项目事实；具体方案状态请放到 `docs/plantree/plans/<plan>/`。
- 如果代码结构、运行路径、验证命令或生产拓扑变化，应先更新对应 baseline，再更新具体计划。
