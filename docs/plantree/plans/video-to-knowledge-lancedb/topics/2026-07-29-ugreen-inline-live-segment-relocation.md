# UGREEN 内嵌直播分段迁移

## 范围

修复 UGREEN 影视中心把 Bililive-go 内部 live-session 分段识别成重复直播合集或豆瓣电影的问题，同时保留全部媒体、内部引用和可回滚证据。

不在本主题中删除 MP4、手工重做媒体内容、扩大 BiliNote/LanceDB 范围，或绕过录制和 pipeline 门禁。

## 红灯反馈回路

生产只读红灯同时断言：

1. `/volume2/docker/bililive-go/video` 内 `Season 01/.live_session_segments` MP4 必须为 `0`。
2. UGREEN `video.file_info.file_path` 不得索引这些 inline 路径。
3. `建筑师 linkai` inline 路径不得通过 `file_info.category_id` 落入 `ug_video_info.type=1`。

修复前在 `2026-07-29 08:34:43` 和 `08:34:57 +08` 连续得到：

```text
inline_fs_mp4=187 indexed_inline_mp4=76 architect_movie_mp4=13
RED
```

修复后在 `08:54:45` 和 `08:56:01 +08` 连续得到：

```text
inline_fs_mp4=0 indexed_inline_mp4=0 architect_movie_mp4=0
GREEN
```

## 根因

- UGREEN `media_lib_set_id=4` 的媒体源为 `/volume2/docker/bililive-go/video`，并会递归扫描根内点目录。
- Bililive-go 当前把聚合后的内部分段移入各主播 `Season 01/.live_session_segments/<session>/`，仍处于监控根内。
- 187 个分段均无同名 NFO 或其它同 stem sidecar。UGREEN 因此对文件名做普通刮削：63 个已索引分段形成重复 TV 分类；`建筑师 linkai` 的 13 个分段分别进入 2009/2006 两个 `type=1` 豆瓣电影分类。
- 直接关联证据是 `file_info.file_path/category_id -> ug_video_info.category_id`。受影响的 `ug_video_info.file_path` 均为空，不能用它否定归类关系。

## 精确清点

- MP4：187 个，49 个 session dirs，78,576,915,095 bytes（73.180455 GiB）。
- 主播分布：Geek徐Sir 7；天津蛋哥 6点说车 28；小司说钢构 2；小燕子出口退税 1；建筑师 linkai 13；旭东聊装修 125；汤山老王 11。
- 187 个均 `nlink=1`，无同名 NFO、无同名可见 MP4；修复前同级正确隐藏根内 MP4 为 0。
- 媒体库内 259 个有效 JSON 含 inline 路径字符串。其中 195 个有效 sidecar 对 185 个现存 MP4 有 402 次精确引用；2 个 MP4 没有 JSON 引用。
- 另有 147 次预存失效引用。8 个 manifest 的 17 组 inline library/metadata 路径全部指向已不存在的 6 月旧文件，不属于本次有效映射。
- UGREEN `file_info` 共索引 76 个 distinct inline MP4，形成 8 个 category：电影 13 条，TV 63 条。

## 修复

写入前重新确认：

```text
active_recordings_count=0
pipeline running_count=0
pipeline pending_count=0
update state=idle
```

备份根：

```text
/volume2/docker/bililive-go/backups/live-segment-relocation-20260729-085035/
```

备份包含：

- `file_info`、`ug_video_info`、`ug_television_episode` 的 PostgreSQL custom dump 和 `pg_restore -l` 清单。
- 76 条受影响 `file_info` 关系 CSV。
- 195 个被改 JSON 原件。
- 187 条 source/target/size/mtime inventory、completed journal、迁移脚本及 SHA-256。
- 写入前 `/api/info`、update 和 pipeline gate。

执行策略：

- 目标为媒体库根同级 `/volume2/docker/bililive-go/.live_session_segments/<主播>/Season 01/<session>/<file>`。
- 全量 preflight 后要求目标冲突为 0；任何冲突拒绝覆盖。
- MP4 使用同文件系统 rename，全部媒体保留。
- 只对等于现存源 MP4 的结构化 JSON 字符串做精确映射，临时文件 fsync 后原子 replace。
- 任一步失败时从 JSON 备份恢复并逆序 rename MP4；journal 记录最终状态。
- 不猜测或改写预存 stale 引用。

## 验证

- 187 个目标全部存在，旧源全部不存在，总字节与迁移前相同，size mismatch 为 0。
- 195 个当前 JSON 均可解析；旧有效引用为 0，新有效引用为 402。
- 媒体库根内 inline 目录为 0，外置隐藏根 MP4 为 187。
- UGREEN watcher 自动移除 76 个 inline `file_info` 和无剩余媒体的错误 category/episode，无需手写 DB 事务。
- 原 8 个 category 中，天津蛋哥 category `4384` 正确保留，因为它仍关联可见聚合成品 `S01E0033`；其余 7 个无媒体错误 category 自动移除。
- 修复全程没有停止或重启 Bililive-go、subtitle worker 或 UGREEN video 服务。

## 源码防复发

回归 seam 为 `publishLiveSessionMediaAggregate`：

- 隐藏 MP4 必须位于 `libraryRoot` 外、其同级 `.live_session_segments` 内。
- `Season 01/.live_session_segments` 不得生成。
- 相对 `libraryRoot` 必须先绝对化；若配置为文件系统根、无法提供库外父目录，则必须在媒体处理前拒绝。
- segment sidecar 的 `output_path` 和 `live_session_segment_hidden_path` 必须指向保留媒体。
- manifest 重新加载后必须仍可通过 sidecar 解析隐藏分段和可见 aggregate。

旧代码测试 RED；最小实现只调整库根规范化、`hiddenLiveSessionSegmentPath` 的隐藏根和越界判断，随后绝对库根、相对库根和文件系统根回归测试均 GREEN。

本地验证通过：定向 Go 测试、`make dev`、`make build-web dev`、`make lint`、`make test`、`make sync-agents`、`make check-agents`、diff 检查和 plan 链接检查。双轴 code review 修复相对库根/文件系统根边界和文档标题后，Standards 与 Spec 复审均无可操作发现。

## 回滚

- 文件层：按 inventory 逆序把 target rename 回 source，并从 `file-reference-backup/json/` 原子恢复 195 个 JSON。
- DB 层：本次未执行手写 DB 事务。若回滚文件后 UGREEN 未自然重建，可参考 custom dump 和 affected CSV 做受控恢复；不要在仍有正确可见媒体关联时整表或整 category 覆盖。
- 在 master 镜像部署和最终生产后验完成前，不删除备份根。
