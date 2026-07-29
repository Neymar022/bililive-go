# Roadmap

## Done

- 明确产品目标：视频生产、字幕烧录、知识抽取、语义检索、知识沉淀闭环。
- 明确职责边界：Bililive-go 负责视频生产和非阻塞同步，BiliNote 负责知识事实源和 UI，LanceDB 负责可重建索引。
- 记录生产环境关键路径、Mac MLX/MOVESPEED runtime、BiliNote/LanceDB 关系和历史风险。
- 确认可用验收维度：视频链路、知识链路、检索质量、LanceDB 稳定性、删除与重建。
- BiliNote/task `473` 单样本已跑通：验证 tag `branch-vtok-473-b5544ce` 部署到 NAS backend，同一真实 payload 非阻塞返回 queued，note `SUCCESS`，1 个 source 和 3 个 document-first item 成功落库，字幕证据、视频路径、字幕路径和时间戳可查。
- Mac LanceDB `Too many open files` 已通过 launchd 启动脚本 `ulimit` 修复；NAS vector timeout 已调到 `30`；task `473` 的 3 个 item 已 reindex 为 `indexed` 并可通过 BiliNote 检索层和远程 `/search` 命中。
- Bililive-go/task `476` 自动样本已跑通：NAS app 运行验证 tag `sha-97c3f27`，字幕/烧录完成后自动非阻塞 POST BiliNote，`.subtitle.json` 写入 `knowledge_sync_status=queued` 和 `task_id=bililive-go-476`，BiliNote note `SUCCESS`，1 个 source 和 19 个 document-first item 成功落库，19 个 item 全部 `indexed`，远程 LanceDB `/search` 可命中该样本 item。
- 连续自动样本已跑通：task `474`、`475`、`477`、`478` 均 pipeline completed 且 sidecar 写入 `knowledge_sync_status=queued`；BiliNote note/source 均成功，其中 `474/475/477` 共产生 7 个 indexed item 并被远程 LanceDB `/search` 命中，`478` 是 1 段字幕退化样本，source 成功但 item_count 为 0。
- 当前运行态排队证据显示本批次 task 已在 21:33-22:39 +08 启动，`task_queue.max_concurrent=3`，没有发现仍在阻止本批次运行的 02:00-only 烧录 gate；观察到的等待更符合 pipeline/远端 burn 串行排队。
- 同场直播分段聚合的本地实现已完成：Bililive-go 持久化 `live_session_id`，字幕完成后用 `.knowledge_sessions` manifest 聚合同一直播分段，静默窗口稳定后只 POST 一次 session-level BiliNote payload；RetryLater 恢复不重复调用字幕 worker；媒体库/知识同步最小时长仅过滤无 session 的独立短片段。最终验证已通过 `go test ./src/... -count=1`、`make build-web test`、`make bililive`、`make check-agents` 和 `git diff --check`。
- 同场直播媒体库最终产物聚合的本地实现已完成：静默窗口后把同一 `live_session_id` 的多个分段 concat 为一个可见整场 MP4，写入整场字幕/封面/NFO sidecar，原子分段视频移入隐藏 `.live_session_segments` 保留为内部素材；BiliNote payload 改指向整场 aggregate 媒体，并按真实分段视频时长推进整场字幕/知识时间轴。已通过 `go test ./src/... -count=1` 和 `python3 -m py_compile scripts/repair-library-sidecars.py`。
- UGREEN 影视中心 inline live-session 历史污染已安全修复：187 个分段 MP4 全部保留并迁到媒体库根同级隐藏目录，195 个有效 JSON 原子更新 402 次引用；UGREEN watcher 自然清理 76 个错误 `file_info` 索引和对应错误电影/重复剧集，无需手写数据库事务。三断言红灯连续两次为 0，详细证据和回滚见 [迁移主题](topics/2026-07-29-ugreen-inline-live-segment-relocation.md)。

## In Progress

- P0：生产安全闭环。
  - 同源重复入队和 completed metadata 覆盖保护。
  - BiliNote ingest 幂等和机器 token。
  - 知识生成失败不阻断烧录。
  - LanceDB 不可达时降级。
- 文档优先 + 字幕证据 + 非阻塞同步 + 可重建索引的跨仓库验证。
  - BiliNote 手动样本、Bililive-go 单样本和连续自动样本均已验证；剩余工作是发布治理、失败 smoke 和回填策略。
- 同场直播聚合发布治理。
  - 已用 TDD 把 live-session 分段隐藏根移到媒体库根外，本地项目门禁和双轴 review 均通过；当前进入 PR、CI、合并和 NAS 原生 Docker `latest` 部署验证。

## Next

- 决定 BiliNote `branch-vtok-473-b5544ce` 与 Bililive-go `sha-97c3f27` 的开 PR、合并、稳定 tag 或 NAS pin 策略；两个验证提交当前都尚未进入各自 `origin/master`。
- 完成 inline live-session 分段库外隐藏修复的 review、PR、CI 和合并；等待 master 镜像后走 NAS 原生 Docker `latest` 验证，以 `/api/info` Git SHA、三断言红灯、文件/引用完整性和 UGREEN DB 后验为准。
- 如需更强生产验收，执行受控失败 smoke：BiliNote 不可达或 bad endpoint 时只记录 `knowledge_sync_status=failed`，不影响 pipeline completed。
- 小批量 backfill 前先确认发布 tag、失败 smoke、限流、失败恢复和 LanceDB rebuild 路径。
- 建立 golden queries：`部署`、`Claude Code`、`字幕`、以及一个无关词。

## Deferred

- 企业级权限继承和 ACL。
- 大规模知识图谱。
- 强制 cross-encoder rerank 默认开启。
- UI 高级治理能力之外的复杂分析报表。

## Acceptance Snapshot

- Bililive-go 新视频烧录完成后，能非阻塞触发 BiliNote ingest。
- BiliNote 先生成可读精华文档，再从文档抽取知识，同时保留原字幕证据。
- SQLite/source tables 是事实源；LanceDB 删除或不可达时不导致事实丢失。
- 重复触发同一 source 不产生重复知识，不覆盖成功 metadata。
- 检索结果有来源、主播、时间戳和命中解释。
