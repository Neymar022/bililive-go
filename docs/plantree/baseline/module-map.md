# Module Map

## 顶层结构

| 路径 | 角色 |
|---|---|
| `src/cmd/bililive/` | Bililive-go 主程序入口。 |
| `src/cmd/build/` 与 `build.go` | 构建、测试、生成和同步 AI 指示文件的命令入口。 |
| `src/configs/` | 配置模型、持久化和配置相关测试。 |
| `src/live/` | 各直播平台适配器，包括 bilibili、douyin、douyu、huya 等。 |
| `src/recorders/` | 录制管理和 recorder 行为。 |
| `src/pipeline/` | 视频处理 pipeline 的配置、执行、状态存储和类型。 |
| `src/pipeline/stages/` | pipeline 具体处理阶段。 |
| `src/servers/` | HTTP API、SSE、pipeline、更新和监控相关 handler。 |
| `src/webapp/` | 前端 Web 管理界面源代码和构建入口。 |
| `src/notify/` | Telegram、ntfy、email、bark 等通知能力。 |
| `src/livestate/` | live state 数据存储和迁移。 |
| `src/metrics/` | Prometheus/Grafana 指标。 |
| `scripts/` | 辅助脚本，例如 E2E 报告服务。 |
| `tests/e2e/` | Playwright E2E 测试。 |
| `docs/` | 用户文档、PRD 和本 plan-tree。 |

## 语言和工具

- Go module：`github.com/bililive-go/bililive-go`，`go 1.25`。
- 前端和 E2E 使用 Node.js/Playwright。
- 构建入口统一走 `make` 和 `go run build.go ...`。

## 与知识库计划相关的模块

- `src/pipeline/`：生产链路和后处理任务状态的主要落点。
- `src/servers/pipeline_handler.go`：pipeline API 入口。
- `src/configs/`：知识同步相关配置如果落在 Bililive-go 侧，应从这里纳入配置模型和持久化。
- `docs/PRD-video-to-knowledge-lancedb.md`：当前视频到知识库产品目标和验收来源。

## 未完成盘点

- `src/pipeline/stages/` 中具体字幕、烧录、知识同步阶段的当前实现细节尚未在本 baseline 中展开。
- NAS 生产部署目录中的 worker 脚本与本仓库代码之间的映射需要单独确认。
