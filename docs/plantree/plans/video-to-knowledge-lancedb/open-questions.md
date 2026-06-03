# Open Questions

## Bililive-go 侧

1. completed metadata 的保护应该在 enqueue、worker 状态机、`.subtitle.json` 写入层，还是三者都做？
2. `knowledge_sync_status` 目前已写入 `.subtitle.json` 并出现在 pipeline stage log；是否还需要在 Web UI、独立 report 或 pipeline task summary 中冗余展示？
3. 生产级失败验收是否需要做受控 bad endpoint smoke，还是以 Go 单元测试覆盖 BiliNote 502 不阻断 subtitle stage 即可？
4. 真实样本读取已通过用户授权的 NAS SSH 完成；后续如果要把该验证流程自动化，应该固定为 SSH/SFTP/WebDAV 挂载、Dockge console，还是新增受限的媒体库导出 API？

## BiliNote 侧

1. NAS `bilinote` backend 当前固定在验证 tag `docker.1ms.run/neymar022/bilinote-backend:branch-vtok-473-b5544ce`；验证提交 `b5544ce` 尚未进入 BiliNote `origin/master`。下一步是为该提交开 PR/合并并发布稳定 tag，还是继续 pin NAS 验证 tag？
2. BiliNote 后台任务失败时，是否需要提供 UI 或 API 级的单 source retry，而不是依赖重发同一 payload？
3. 2026-06-03 已确认同一直播聚合还需要 BiliNote 消费端修复：`source_videos`/`media_segments` 映射、`live-session:*` 可见笔记去重、目录连续编号和缺失时间戳补齐。该 BiliNote 分支是否与 Bililive-go same-live PR 同步合并发布，还是先独立发布 BiliNote 后再合并 Bililive-go？

## LanceDB 侧

1. LanceDB 当前 `/search` 已证明可用，但 `/stats` 返回 `404`，`/rebuild` 尚未验证；是否需要补齐可观测的 stats/rebuild API 来证明可重建索引？
2. 中文 FTS/BM25 是否已经启用，还是仍只依赖 vector/search fallback？
3. Mac LanceDB 已因 `Too many open files` 修过 launchd `ulimit`；后续崩溃监控应以 launchd 状态、crash report、health 连续性，还是组合门禁为准？

## 运行和发布

1. NAS Dockge 更新镜像后已可用 `/api/knowledge/runtime/machine` 证明当前容器 Git SHA；是否需要把这个检查固化成发布脚本或 CI/CD 验证步骤？
2. 全量回填 100+ 视频时，限流、重试和失败恢复策略是什么？
3. Bililive-go app 当前固定在验证 tag `docker.1ms.run/neymar022/bililive-go-app:sha-97c3f27`；验证提交 `97c3f27` 尚未进入 Bililive-go `origin/master`。2026-06-03 分支已补 `media_segments` payload 别名以兼容 BiliNote 多媒体段消费。下一步是为该提交开 PR/合并并发布稳定 tag，还是继续 pin NAS 验证 tag？
4. Dockge `bililive-go` stack 当前报 `/volume2/docker/bililive-go/.env` 缺失且服务状态为 `N/A`，但 SSH `sudo docker ps` 显示 `bililive` 与 `subtitle-worker` 实际运行；这是 Dockge 配置漂移、UGREEN/Dockge 管理路径不一致，还是仍需要修复的生产部署问题？
5. Bililive-go `18090` 当前 `/api/file` 和 `/files/` 按代码只暴露 `out_put_path=/volume2/docker/bililive-go/srt_video`；是否应提供受限的只读媒体库文件访问或导出接口来读取 `/volume2/docker/bililive-go/video/.../*.srt`，还是保持媒体库只通过外部 NAS SSH/WebDAV/挂载访问？
6. 当前批次任务已在 21:33-22:39 +08 之间启动，运行态配置未暴露 02:00-only gate；如仍需要“凌晨 2 点统一烧录队列”，该设置应落在哪个可观测配置面，并如何在 pipeline task API 中暴露 `not_before`/调度原因？
