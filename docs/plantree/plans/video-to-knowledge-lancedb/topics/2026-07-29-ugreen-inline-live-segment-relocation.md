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

## 发布与运行态后验

- 中文 PR #37 已合并 master，merge commit 为 `99e19f96dd5726883e53618b23ce164c0a1e3392`。PR 的 build、test、lint、agent 指示检查、build-web 和 Playwright E2E 均通过；`claude-review` 在开始审查前因仓库未安装 Claude Code GitHub App 返回 401，属于外部审查服务配置，已在 PR 留痕。
- master workflow `30414072309` 成功发布 app 和 worker 镜像。Docker Hub app `latest` 与 `sha-99e19f9` 均指向 digest `sha256:9140fd9baad22812bb5ba60a89efa44c2b73517b82fa152b1f4b54f7d204b208`。
- 生产活动 compose 由 UGREEN 原生项目 `bililive-go` 持有，路径为 `/volume2/docker/bililive-go/bililive-go-ugreen/docker-compose.yaml`。部署前备份活动/canonical compose、配置、运行环境摘要、旧容器和镜像回滚信息到 `/volume2/docker/bililive-go/backups/deploy-99e19f9-20260729-093247/`，并为旧 app image 保留本地 rollback tag。
- 部署前、拉取后重建前和部署后均重新核对：`active_recordings_count=0`、pipeline `running_count=0`、`pending_count=0`、update `idle`。只拉取并重建 `bililive`；`subtitle-worker` 容器 ID 保持不变，没有重启 UGREEN video 服务。
- 部署后 `/api/info` 返回 HTTP 200，`git_hash=99e19f96dd5726883e53618b23ce164c0a1e3392`；运行 image ID 为 `sha256:5ac71076622c943ccf64136d25c35775f71c5f3802084f56088c5cf729edeb25`，OCI revision 与 master 一致，容器 restart count 为 0，活动 compose checksum 未变化。
- 两轮部署后验均为 GREEN：库内 inline MP4 `0`、UGREEN `file_info` inline 索引 `0`、建筑师电影 type 索引 `0`；外置 MP4 `187`、总字节 `78,576,915,095`、missing/size mismatch `0`；195 个 JSON 解析错误 `0`、旧引用 `0`、新引用 `402`。错误电影 `ug_video_info_id 4381/4385` 及其文件关系均为 `0`。

## 合集封面与稳定身份续修

用户在上述部署后从生产 UI 发现两个剩余症状：`旭东聊装修` 无合集封面；`天津蛋哥 6点说车` 仍显示两个合集，其中一个只有一集。新的只读红灯同时覆盖文件、NFO 和 UGREEN DB：

```text
xudong_cover_image=0
xudong_missing_episode_jpg=1
tianjin_active_categories=2
tianjin_singleton_categories=1
tianjin_non_nfo_categories=1
RED
```

直接证据：

- 旭东 category `4322` 的 `poster_path/backdrop_path` 指向已不存在的 `S01E0105.mp4`，show 根没有 `poster.jpg`，E0073 没有 JPG。
- 天津正确 category `4326` 为 `use_nfo=3`；错误 category `4384` 为 `use_nfo=1`，仅关联可见成品 E0033，episode title 为空且 cover 指向 MP4。
- `天津蛋哥   6点说车` 三空格目录没有 MP4，不是当前两个 active category 的直接来源。

历史修复备份根为 `/volume2/docker/bililive-go/backups/show-cover-identity-20260729-112930/`，包含三表 custom dump、`pg_restore -l` 清单、目标关系 CSV、原始 NFO、文件 inventory、工具哈希、执行日志和 E0033/E0073/E0036 回滚硬链接。所有写入前均重新满足录制 `0`、pipeline running/pending `0/0`、update `idle`。

修复保留所有 MP4：从真实视频补齐缺失 episode JPG；从首个有效 episode JPG 原子复制独立的 `poster.jpg`，不覆盖已有非空 poster；`tvshow.nfo` 加入 `<thumb aspect="poster">poster.jpg</thumb>`。E0033 移出监控根后 watcher 删除旧关系，恢复时读取已在位的 NFO/JPG，将其归入 category `4326` 并自然移除 `4384`。旭东 E0073 采用相同重扫；旧 poster/backdrop 仍未自然更新，因此在完整 DB 备份后用单行事务只把 category `4322` 改指向现存 show poster。没有删除 category、episode 或 MP4。

修复后的同一命令连续两次为 GREEN：

```text
inline_mp4=0
inline_file_info=0
architect_inline_movies=0
json_parse_errors=0
effective_old_inline_json_refs=0
preserved_stale_inline_json_refs=147
external_hidden_json_refs=402
xudong_active_categories=1
xudong_cover_image=1
xudong_show_poster=1
xudong_missing_episode_jpg=0
xudong_tvshow_poster_thumb=1
xudong_poster_independent_inode=1
tianjin_active_categories=1
tianjin_singleton_categories=0
tianjin_non_nfo_categories=0
tianjin_wrong_category_files=0
tianjin_show_poster=1
tianjin_missing_episode_jpg=0
tianjin_tvshow_poster_thumb=1
GREEN
```

文件后验确认修复前 inventory 的 MP4 缺失 `0`、大小变化 `0`，两个 show 的可见 MP4 均有同 stem JPG/NFO；库外隐藏媒体仍为 `187` 个、`78,576,915,095` bytes。源码防复发采用同一稳定身份规则：常规发布与 aggregate 都先准备 episode NFO/JPG、show NFO 和独立 poster；重复/增量发布保留已有 poster；Go/Python 同时把 tab、newline、NBSP、全角空格等 Unicode whitespace 归一成单空格，并删除其余 Cc/Cf；ffmpeg 临时封面保留 `.jpg` 扩展。

同场 aggregate 不再复用或覆盖最后一个原分段，而是使用 manifest `aggregate_path` 持久化独立、稳定的可见路径；原 N 个分段全部保留到媒体库外。Session 增加迟到分段时继续复用同一路径，旧 aggregate 视频、metadata 和 sidecar 一起归档到库外 `.aggregate_versions`。aggregate metadata 的 `live_session_media_sources` 在实际 hidden 冲突处理完成后才写入，保证首次发布、唯一冲突目标、重复发布 stale refs 自愈和增量归档都只引用现存媒体。

分段事务顺序为持久化 target hardlink、原子更新 segment metadata、再 unlink source；aggregate metadata 和旧版本归档成功后才删除 journal。rollback 使用 inode/size/mtime/hash CAS，并按 source、metadata、target 的依赖顺序 fail-stop；metadata restore、restored-source fsync、hidden-target fsync、source `ENOENT` 和 aggregate metadata rename 后 fsync 失败都有故障注入覆盖。失败无法完整回滚时保留 hidden 媒体、仍指向它的 metadata 和事务目录，不删除最后副本。双轴 review 的可操作发现均在当前循环修复，源码仍待 PR、合并、镜像发布和运行态部署。

## 回滚

- 文件层：按 inventory 逆序把 target rename 回 source，并从 `file-reference-backup/json/` 原子恢复 195 个 JSON。
- DB 层：本次未执行手写 DB 事务。若回滚文件后 UGREEN 未自然重建，可参考 custom dump 和 affected CSV 做受控恢复；不要在仍有正确可见媒体关联时整表或整 category 覆盖。
- 合集续修：从 `show-cover-identity-20260729-112930` 恢复原始 `tvshow.nfo` 和目标三表；恢复 DB 前先核对当前关系，优先把单行 poster/backdrop 恢复为 `xudong-category-before-poster-fix.csv` 中的值，不整表覆盖。回滚硬链接可原子恢复 E0033/E0073/E0036，禁止删除当前可见 MP4。
- 运行态：使用部署备份中的活动 compose 和旧 app image rollback tag，只重建 `bililive`；回滚前仍须重新满足零录制、零 running/pending pipeline 和 update idle 门禁。
- 历史迁移与部署备份均保留，稳定观察期结束前不要删除。

## 2026-08-14 合集时间排序修复

### 根因与源码修复

- UGREEN 影视中心主要按 `season_number/episode_number` 排序；旧发布逻辑按“第一个空闲集号”分配，任务晚到或并发完成时会让集号与真实录制开始时间相反。
- 普通发布现在优先采用 `RecordInfo.StartTime`，仅在其为空时解析规范源文件名，并固定按 `UTC+8` 解释无时区时间；不使用文件 mtime 或路径字典序代表录制时间。
- episode identity 为 `(recordedAt Unix 微秒 - 2020-01-01 UTC Unix 微秒) * 8 + collision slot`。8 个碰撞槽处理同一精确时间的多个媒体；超出 JavaScript 安全整数范围时 fail closed。Go 使用 `int64` 并通过 Linux `386` 编译检查。
- live-session 输入按 `record_meta.start_time`、metadata `source_path/output_path` 中的规范时间、可解码的新 episode identity 依次取可信时间；存在 metadata 但无法得到可信时间时拒绝聚合。旧无 sidecar 测试 seam 仅兼容既有四位集号顺序。
- 聚合视频、字幕来源、NFO `dateadded` 和 aggregate metadata `start_time` 均采用最早真实录制时间。晚到更早分段时切换到更早 episode identity，旧聚合 MP4/NFO/JPG/SRT/ASS/metadata 整体归档到库外 `.aggregate_versions`；manifest 用 `previous_aggregate_path` 保留崩溃恢复身份，故障注入与重试测试证明中断后仍会归档旧聚合并清除恢复标记。

### 历史只读计划

- `scripts/repair-library-sidecars.py --plan-chronological-renumber` 只生成计划，不提供 apply：按精确 `recordedAt` 和确定性 collision slot 计算全库双射目标，拒绝越界、重复目标和已有冲突。
- dry-run 逐项输出 MP4/sidecar 改名、NFO `episode/sorttitle/aired/dateadded/plot` 字段变化，以及媒体库、`.knowledge_sessions` 和库外 `.live_session_segments` 中 manifest/sidecar JSON 的 JSON Pointer、old/new path；缺 NFO 时 fail closed。
- dry-run 在内存中实际替换并重新遍历 JSON，要求 old refs 为 0、new refs 不少于原有效引用数，并证明 MP4 数量和总字节守恒、每个合集的时间与集号单调。
- 早期 NAS 只读算法试跑得到 `episodes=389`、`unique_sources=389`、`unique_targets=389`、`changed=389`、`target_conflicts=0`，14 个 show 均单调；该证据早于最终“2020 epoch + 精确微秒 + 8 槽”算法，必须用当前脚本重新生成后才能作为历史 apply 依据。

### 当前门禁与回滚

- 2026-08-15 root 权威 dry-run 已通过：399 个 episode source/target 完整双射，2346 个媒体/sidecar 文件、1319 个 JSON、2100 条引用均可计划更新；模拟后旧引用为 0，400 个 MP4 和 `787776233209` bytes 守恒，冲突 0、所有 show 单调。报告压缩文件 SHA-256 为 `c55b83f80ffec0e06cdd5195577a37c37478f62d8827080b2aeeab9a4e59a8bb`。
- 当前门禁为 active recordings `0`、pipeline `running=0`、`pending=2`、update `idle`。两项计划字幕任务的输入位于 `srt_video`，与当前重编号 source/target/JSON 引用交集为 0；它们不会被 apply 改名，但旧生产版本完成任务后可能新增四位集号，因此不强跑、不取消，等待其自然完成后重新固定最终 apply 快照。
- 历史 apply 前必须重新满足录制 0、pipeline running/pending 0/0、update idle；随后备份目标 MP4/NFO/字幕/JSON/manifest 映射及 `file_info/ug_video_info/ug_television_episode`，并用临时名和原子 rename 处理换名环，任何目标冲突拒绝覆盖。
- 回滚必须按 journal 逆序恢复文件名与 JSON/NFO 原件，再核对 MP4 数量、字节和引用；不得删除任何 MP4。部署新 episode identity 后还需单独验证 UGREEN 对长 episode number 的真实兼容性。

## 2026-08-19 旧路径恢复与日期校正验收

- PR #43 已合并并部署 app `62ebec4ed62c3cf0519228d27441f9f1aec4a623`。task `1183/1184` 在不回退到缺失 FLV、不删除分段的前提下完成；session `734` 的 11 个 manifest sources 与 aggregate metadata mapping 一一对应，11 个唯一 output 全部位于库外 hidden root 且存在，content hash 一致，恢复标记为空。
- task `1203` 自然调度完成，episode `1673386692282296` 与精确 `record_meta.start_time` 的 recordedAt identity 一致；文件、NFO、manifest 和 UGREEN episode 关系均通过，所属合集没有时间倒退。写前等待 `1208..1212` 自然完成，最终门禁为 active `0`、pipeline `0/0`、update `idle`、ffmpeg `0`，没有强跑或取消任务。
- 5 个历史 NFO/UGREEN 名称只校正日期前缀，不改标题正文、文件名或 episode identity。备份根 `/volume2/docker/bililive-go/backups/recorded-at-date-correction-20260819-044422/` 保存原 NFO、数据库 custom dump、前后行与 SHA-256；NFO 原子替换后 watcher 未更新名称，才以精确旧值和 `5 rows` 断言执行最小事务。
- 后验：目标新值 `5`、旧值 `0`，JSON parse errors `0`，媒体 apply 前后为可见 `423 / 825377880762 bytes`、hidden `273 / 150985881365 bytes`；inline filesystem/DB/建筑师电影三断言仍为 `0/0/0`。app 与 worker API 均为 HTTP `200`，日期修复没有触发容器或 UGREEN video 服务重启。
- 只删除已确认无引用的临时 app tar；保留所有 Docker rollback/local 镜像。`p6-monitor-http` 的 bind source 虽缺失，但停止容器仍引用其 local worker image，owner 未证明废弃，因此不清理。735GB chronological-renumber 恢复备份继续保留。

回滚日期校正时，只从新备份根恢复 5 个 NFO，并按 `rows-before.psv` 对 5 个 episode ID 做带当前值条件的反向事务；不得恢复整表、改 episode identity 或删除 MP4。

## 2026-08-22 Android TV 公开集号兼容

- Mac 影视中心能展示完整选集而 Android TV 只显示一集，媒体和 UGREEN episode 行均未缺失。真实 A/B canary 证明：只要 NFO `<episode>` 为连续 `1..N`，Android TV 即能识别总集数；长 recordedAt 文件名仍兼容。A 组 `file_path/local_count` treatment 与未 treatment 的 B 组结果相同，不是根因。
- 稳定契约因此拆成两层：文件名和 `<uniqueid type="bililive-recorded-at">` 保留 recordedAt identity，供排序、引用和 join；UGREEN 消费的 NFO `<episode>` 只发布按 recordedAt 排列的连续 ordinal。晚到更早场次不得静默追加，必须 fail closed 后走受控重编号。
- 历史 apply 只原子替换 441 个 NFO 的 `<episode>`，不改 MP4、文件名、字幕、JSON 或 manifest。UGREEN watcher 未自然同步既有 DB 值后，先固定 `412` 条文件/episode、`14` 个 category、冲突 `0`，再在同一事务更新 `file_info.episode_num` 与 `ug_television_episode.episode`；触发器仅登记官方增量同步。最终 DB 长公开集号为 `0/0`，NFO、file_info 与 episode 三方一致。
- 回滚根 `/volume2/docker/bililive-go/backups/ugreen-episode-ordinal-20260822-233003/` 包含原 NFO 硬链接、媒体守恒清单、精确 DB CSV 与双向 SQL。生产后验为 `17` 个 show、`441` 个 NFO、`441 / 854478547076 bytes` 媒体守恒，441 个长 identity 文件名均保留。A/B canary 目录、临时关系、6 个媒体硬链接和 canary 备份已完整删除。
- 源码防复发由 `EnsureLibraryHardlink` 统一生成 identity 文件名、ordinal NFO 和 identity uniqueid；历史 planner/apply driver 只做 NFO fixed-point，并在缺 NFO、identity 不匹配、目标冲突或媒体变化时拒绝执行。
