# Test And Release Gates

## AGENTS.md 要求

- 修改后端 Go 代码后运行 `make dev`。
- 修改前端代码后运行 `make build-web dev`。
- 前后端都改时运行 `make build-web dev`。
- 提交前应确保 `make build-web dev`、`make lint`、`make test` 全部通过。
- 不要使用 `go build ./...` 替代 Make 命令。
- 不要主动提交或推送，除非用户明确要求。

## Make 入口

| 命令 | 用途 |
|---|---|
| `make dev` | 构建开发版后端。 |
| `make build-web` | 构建前端。 |
| `make build-web dev` | 前端 + 后端验证。 |
| `make test` | Go 单元测试。 |
| `make lint` | golangci-lint，使用 dev build tags。 |
| `make test-e2e` | Playwright E2E。 |
| `make sync-agents` | 从 `AGENTS.md` 同步下游 AI 指示文件。 |
| `make check-agents` | 检查 AI 指示文件一致性。 |

## CI/Release 文件

- `.github/workflows/tests.yaml`
- `.github/workflows/e2e-test.yml`
- `.github/workflows/publish-images-on-master.yaml`
- `.github/workflows/release.yaml`

## 文档类改动验证

- 文档-only 改动通常不需要完整构建，但需要确认链接路径和 plan-tree 入口可发现。
- 如果文档改变了 AI 指示，应只改 `AGENTS.md` 并运行 `make sync-agents`。
- 本次 `docs/plantree` 初始化不修改 AI 指示文件，不运行同步。
