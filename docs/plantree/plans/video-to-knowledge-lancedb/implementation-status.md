# Implementation Status

更新时间：2026-08-19

## State Ownership

本文件是后续恢复工作的主状态。旧 thread 和 provider goal 只作为历史执行上下文，不再作为唯一事实源。

- 旧 thread：codex://threads/019e024c-7321-7503-99ec-2973c7c96fc0
- 旧 goal：`文档优先 + 字幕证据 + 非阻塞同步 + 可重建索引`
- 当前计划根：`docs/plantree/plans/video-to-knowledge-lancedb/`
- 恢复入口：先读 `docs/plantree/README.md`、`baseline/`、本文件、`roadmap.md`、`open-questions.md`。

## Current Phase

P0/P1 之间：先确保生产链路安全、非阻塞、幂等，再扩大知识质量和检索体验验证。

旧 goal 已拆成四个可验证要求：

1. 文档优先：BiliNote 先生成可读精华 Markdown，再从 Markdown 章节抽取知识。
2. 字幕证据：知识条目必须保留原字幕片段、时间戳、视频路径和字幕路径。
3. 非阻塞同步：Bililive-go/BiliNote 同步不得因长 LLM 生成或 LanceDB 不可达阻塞烧录成功状态。
4. 可重建索引：SQLite/source tables 是事实源，LanceDB 是可删除、可重建、可降级的 side index。

## Completed From Old Thread

- 明确长期状态应从 provider goal 迁移到 plan-tree；goal 以后只作为执行引擎。
- 已建立 `docs/plantree/` 入口、baseline、计划根、roadmap、open questions 和 ideas inbox。
- BiliNote 侧已有知识库方向实现进展：
  - `/api/knowledge/ingest` 机器 token 入口。
  - `knowledge_sources` / `knowledge_items` 事实源模型。
  - Markdown-first extractor，可在 `l2_content` 中保留 `原字幕证据`。
  - 回填脚本 `knowledge_backfill.py` 可从 Bililive-go 媒体库构建 ingest payload。
  - 回填脚本已补 `--remote-ingest`、sidecar 字幕去重、`--non-blocking` 参数。
  - BiliNote 非阻塞 document-first ingest 已通过 PR #40 合并到 `Neymar022/bilinote`，merge commit `8b4b401c58b451169eadb782ef8a56f22893896c`。
- 本仓库 PRD 已记录生产链路事实：Mac MLX 转写、Mac 硬编烧录、MOVESPEED/BiliNoteRuntime、NAS 媒体库路径、LanceDB side index。

## Current Work

### 2026-07-29 UGREEN inline live-session 错误归类修复

- 生产红灯在 `2026-07-29 08:34 +08` 连续两次稳定复现：媒体库根内 inline MP4 `187`、UGREEN `file_info` inline 索引 `76`、建筑师电影 type 索引 `13`。
- 根因已证实：Bililive-go 把内部 live-session 分段放在 `/volume2/docker/bililive-go/video` 监控根内部；UGREEN 会递归扫描点目录。分段均无同名 NFO，因此普通主播形成重复 TV 分类，`建筑师 linkai` 的 13 个分段被匹配成 2006/2009 两部豆瓣电影。
- 历史修复已完成且未删除媒体：187 个 MP4（78,576,915,095 bytes）迁到同级 `/volume2/docker/bililive-go/.live_session_segments`；195 个有效 sidecar 原子更新 402 次精确引用；2 个无引用 MP4 仍按原身份保留。147 次预存 stale 引用保持不变，避免猜测性改写。
- UGREEN 相关表和文件引用已备份到 `/volume2/docker/bililive-go/backups/live-segment-relocation-20260729-085035/`。文件迁移 journal 为 `completed`；UGREEN watcher 自然清理错误索引，无需执行手写 DB 事务。
- 文件后验：旧路径 `0`、新路径 `187`、总字节不变、size mismatch `0`、旧有效引用 `0`、新有效引用 `402`、库内 inline 目录 `0`。原始三断言在 `08:54:45` 和 `08:56:01 +08` 连续为 GREEN。
- TDD 源码修复已完成：旧代码回归测试因隐藏路径仍在媒体库根内而 RED；最小修复将隐藏根改为 `libraryRoot` 同级，并验证 sidecar/manifest 重载仍解析到保留的分段媒体。双轴 review 发现并修复相对库根和文件系统根边界后复审无发现；定向测试、`make dev`、`make build-web dev`、`make lint`、`make test`、agent 同步检查和 diff/plan 链接检查均通过。
- 源码经 PR #37 合并到 master，merge commit 为 `99e19f96dd5726883e53618b23ce164c0a1e3392`；master Docker Hub 发布 workflow 成功，`latest` 与 `sha-99e19f9` 指向相同 digest。
- UGREEN 原生 Docker 项目运行态交付已完成：部署前、拉取后重建前和部署后门禁均为录制 `0`、pipeline running/pending `0/0`、update `idle`；只重建 `bililive`，`subtitle-worker` 未重启。活动 compose 保持不变，部署回滚备份位于 `/volume2/docker/bililive-go/backups/deploy-99e19f9-20260729-093247/`。
- 最终运行态 `/api/info.git_hash=99e19f96dd5726883e53618b23ce164c0a1e3392`，app image revision 与之相同，API 返回 HTTP 200、容器 restart count 为 0。部署后两轮后验均确认三断言 `0/0/0`、外置 MP4 `187`、总字节 `78,576,915,095`、旧/新 JSON 引用 `0/402`、解析错误 `0`，UGREEN 建筑师错误电影条目 `0`。本主题当前无未完成运行态步骤。

### 2026-07-29 UGREEN 合集封面与稳定身份续修

- 用户生产 UI 复测发现：`旭东聊装修` 合集无封面；`天津蛋哥 6点说车` 仍有两个合集，其中错误 category `4384` 只含可见成品 `S01E0033`。只读红灯连续两次为：旭东有效合集封面 `0`、缺 episode JPG `1`、天津 active category `2`、singleton category `1`、非 NFO category `1`。
- 根因证据：旭东 category `4322` 的 `poster_path/backdrop_path` 指向已不存在的 `S01E0105.mp4`，合集根没有稳定 poster；天津正确 category `4326` 为 `use_nfo=3`，错误 category `4384` 为 `use_nfo=1`，其唯一 episode 无标题且 cover 指向 MP4。连续空白规范化后的三空格目录当前没有 MP4，不是本轮重复合集的直接来源。
- 历史修复已在每次写入前重新通过录制 `0`、pipeline running/pending `0/0`、update `idle` 门禁。相关三表 custom dump、关系 CSV、原始 NFO、文件 inventory、工具哈希和回滚硬链接位于 `/volume2/docker/bililive-go/backups/show-cover-identity-20260729-112930/`。
- 现有 MP4 全部保留。补齐旭东 E0073 和天津 E0015/E0016/E0017 的真实视频抽帧 JPG，为两个 show 从 episode JPG 原子复制独立 `poster.jpg`，并给 `tvshow.nfo` 增加 poster thumb。天津 E0033 移出/恢复后，watcher 将其归入 category `4326` 并自然移除 category `4384`；旭东 E0073 同样重扫后 cover 正确，随后只用一行事务把 category `4322` 的 stale poster/backdrop 改为现存 `poster.jpg`。
- 修复后精确审计连续两轮 GREEN：库内 inline MP4、inline `file_info`、建筑师电影关系均为 `0`；JSON 解析错误和有效旧引用为 `0`，保留 stale 引用 `147`，库外新引用 `402`；旭东/天津 active category 均为 `1`，有效 show poster、NFO poster thumb、缺 episode JPG 分别为 `1/1/0`；天津 singleton、非 NFO、错误 category 文件均为 `0`。两 show 修复前 MP4 无缺失、无大小变化；外置隐藏 MP4 仍为 `187`、`78,576,915,095` bytes。
- 源码 TDD 和发布已完成：新发布和历史修复都从真实 episode JPG 创建独立 show poster，不覆盖已有非空用户封面；`tvshow.nfo` 固化 poster thumb；Go/Python 对 tab、newline、NBSP、全角空格和 Cf 使用同一 show identity；ffmpeg 临时封面保留 `.jpg` 扩展。aggregate 使用 manifest 中持久化的稳定可见路径，原 N 个分段全部保留在库外；增量旧 aggregate 版本连同 metadata/sidecar 归档到库外。aggregate metadata 仅记录实际选定的 hidden 路径，首次、冲突唯一目标、重复 stale refs 自愈和增量归档均有回归测试。分段迁移按 target hardlink → metadata → source unlink 执行并保留 journal；aggregate metadata/归档成功后才提交事务，rename 后 fsync 失败则保留一致 hidden 状态和恢复锚点。双轴 review 的 Standards/Spec 均为 `0 findings`；Python 10 项、定向测试、两包 `-race`、`make dev`、`make build-web dev`、`make lint`、`make test`、`make check-agents` 和 diff 检查均通过。PR #39 已合并 master，merge commit 为 `3060d134f39a003bb17aa2ae93d50d371557ead3`。
- 生产交付于 `2026-07-30 07:53 +08` 完成。原 nohup 脚本在 02:00 字幕批次清空后正确取得 `0/0/0/idle`，但因旭东旧 `tvshow.nfo` 缺 poster thumb 在 initial 静止窗口稳定 RED 而 fail-closed 退出，没有部署。随后在重新核对门禁并备份旧 NFO 后，仅用已审查补偿工具补齐 `<thumb aspect="poster">poster.jpg</thumb>`；修复前后哈希、两轮 GREEN 和回滚文件位于 `/volume2/docker/bililive-go/backups/show-cover-nfo-repair-20260730-074530/`。
- 安全续接使用精确 tag `sha-3060d13`，只重建 `bililive`；`subtitle-worker` 的 container ID、image ID 和 startedAt 均未变化。回滚点位于 `/volume2/docker/bililive-go/backups/deploy-3060d13-resume-20260730-074825/`。最终 `/api/info.git_hash=3060d134f39a003bb17aa2ae93d50d371557ead3`，app/worker 均为 `running`、restart count 均为 `0`；两者没有配置 Docker HEALTHCHECK，因此另以 app root HTTP `200` 和 worker 容器内 `/openapi.json` HTTP `200` 验证服务可用。门禁连续为录制 `0`、pipeline running/pending `0/0`、update `idle`；活动 compose 所有权仍为 UGREEN 原生项目，compose/config/.env checksum 与部署前备份一致。
- 脚本两轮后验和独立新鲜复测均为 GREEN：旭东/天津 active category 均为 `1`，旭东有效 show poster、DB cover、NFO poster thumb 均为 `1`，天津 singleton/non-NFO/wrong-category 均为 `0`；两 show 所有可见 MP4 的 episode NFO/JPG 缺失均为 `0`。inline FS/DB/建筑师电影三断言为 `0/0/0`。媒体库与库外隐藏根合计 `509` 个 MP4、`722,616,320,467` bytes，相对部署前 missing/added/changed 均为 `0`；JSON `1088` 个、解析错误 `0`，外置引用 `406` 且缺失 `0`，有效旧 inline 引用 `0`，`147` 条历史 stale 文本按既定策略保留。既有 `349` 条缺失 critical 历史引用未新增，仍属于后续独立数据卫生范围，不是本次部署回归。

2026-06-13 09:35 +08 用户完成 UGREEN 原生 Docker 拉取重建后，二次验收确认 `/api/info` 仍为 `app_version=806da08` / `git_hash=806da08c5cc299d593910a83221c6c6e640532d1`，`/api/update/status` 为 `idle`。`/api/pipeline/tasks?limit=80` 结构化检查显示，新版本之后的最近任务（如 #672 旭东聊装修、#673 汤山老王）已经完成到字幕产物和同场知识同步提交；历史失败主要集中在 2026-06-08 至 2026-06-11 的同场聚合任务，错误为旧版本生成 `*.mp4.tmp` 输出路径后 FFmpeg 报 `Unable to choose an output format ... .mp4.tmp`。当前代码已改为 `*.tmp.mp4`，并且 concat 命令显式加 `-f mp4`，说明防复发代码已在新镜像中。不要批量调用 `/api/pipeline/tasks/{id}/retry` 修历史：`RetryTask` 会重置到第 0 阶段并使用 `InitialFiles`，而许多历史任务的 FLV 已在 convert 阶段删除，直接 retry 容易制造新失败或重复产物。历史补偿应走 NAS 文件系统脚本：先修复/聚合媒体库 sidecar 和重复 show，再回填 BiliNote cover；没有安全 NAS 写入通道前只能做只读验收。

2026-06-13 09:10 +08 用户在 UGREEN 原生 Docker 项目中拉取并重建 `bililive` / `subtitle-worker` 后，运行态只读验收已确认切到本次封面/sidecar 修复版本：`GET http://192.168.1.80:18090/api/info` 返回 `app_version=806da08`、`git_hash=806da08c5cc299d593910a83221c6c6e640532d1`、`platform=linux/amd64`；`/api/update/status` 返回 `state=idle`、`graceful_update_pending=false`、`active_recordings_count=0`；BiliNote `GET http://192.168.1.80:3015/api/sys_health` 返回 success。重新验证源码与补偿脚本：`python3 -m unittest tests/test_repair_library_sidecars.py tests/test_backfill_bilinote_covers.py` 通过，`python3 -m py_compile scripts/repair-library-sidecars.py scripts/backfill-bilinote-covers.py` 通过，非沙盒 `env GOCACHE=/tmp/bililive-go-gocache go test -count=1 ./src/subtitle ./src/pipeline/stages` 通过。当前历史遗留补偿尚未执行：本地没有 `/volume2/docker/bililive-go/video` / `/volume2/docker/bilinote/data` 挂载；免密 SSH 不可用；Dockge `bililive-go` 页面是漂移的旧 compose，容器 Bash 入口直接报 `/volume2/docker/bililive-go/.env not found`，不能作为生产执行通道。下一步需要通过 UGREEN 原生 Docker 容器终端、NAS shell/SSH 显式授权通道，或用户提供的一次性安全执行通道，在 NAS 上先 dry-run `scripts/repair-library-sidecars.py --root /volume2/docker/bililive-go/video --fail-on-duplicate-shows`，再按结果执行 `--apply --merge-duplicate-shows`；随后对 BiliNote DB 运行 `scripts/backfill-bilinote-covers.py` 回填 `cover_url`，最后用本地 Chrome/Computer Use 验收 BiliNote 最近文档封面和 UGREEN 媒体库重复 show。

2026-06-13 cover/sidecar 与重复剧集复发修复已在隔离 worktree `worktrees/same-live-media-library-aggregation` / branch `codex/same-live-media-library-aggregation` 完成本地实现，尚未提交、PR、构建镜像、部署 NAS，也未修改生产媒体库。根因确认：前次修复只覆盖 `EnsureLibraryHardlink` 新建硬链接路径；最近生产任务多数通过 `ResolveLibraryVideoPath` 直接命中已存在媒体库路径，因此跳过 `.jpg`、episode `.nfo`、`tvshow.nfo` finalizer。BiliNote/NAS 封面缺失不是 pending 队列导致，而是 completed 产物缺最终 sidecar；同场聚合也会先找分段 `.jpg`，当分段缺封面且 ffmpeg 抽帧失败被静默吞掉时，`audio_meta.cover_url` 会继续为空。重复剧集复发来自历史物理目录仍未完全归一，例如不可见字符、控制/格式字符或连续 Unicode 空白导致同一主播落入多个 show 目录，UGREEN 会按物理目录识别成多个剧集库。

本地实现策略：新增统一 `subtitle.EnsureLibrarySidecars` finalizer，无论 `ResolveLibraryVideoPath` 命中还是 `EnsureLibraryHardlink` 兜底创建，都在字幕发布阶段补齐 `tvshow.nfo`、episode `.nfo` 和 `.jpg`；封面抽取失败现在返回明确错误，不再允许“completed 但暂无封面”的静默成功。同场聚合封面逻辑改为先复用分段封面，再从聚合视频抽帧，再从源/分段视频抽帧；全部失败会阻止后续知识同步。`scripts/repair-library-sidecars.py` 扩展为历史补偿工具：默认 dry-run，`--apply` 才写入缺失 sidecar 和封面；规范化后同名 show 目录合并时不删除媒体、不覆盖冲突文件，冲突整集迁到媒体库根同级 `.<root>-quarantine-library-sidecars/<timestamp>/`，避免 UGREEN 再次扫描。新增 `scripts/backfill-bilinote-covers.py`，只对 `note_records.audio_meta.cover_url` 为空且源视频旁已有 `.jpg` 的记录回填本地静态封面 URL，不重跑总结模型。

本地验证：`python3 -m unittest tests/test_repair_library_sidecars.py tests/test_backfill_bilinote_covers.py` 通过；`python3 -m py_compile scripts/repair-library-sidecars.py scripts/backfill-bilinote-covers.py` 通过；`GOCACHE=/tmp/bililive-go-gocache go test -count=1 ./src/subtitle ./src/pipeline/stages` 通过。新增 Go 覆盖 `ResolveLibraryVideoPath` 成功路径也会补 sidecar、封面无法创建会失败、同场聚合缺封面会阻止发布；新增 Python fixture 覆盖 dry-run 报告、apply 后补齐 `.jpg/.nfo/tvshow.nfo`、重复 show 合并冲突 quarantine，以及 BiliNote cover backfill 不覆盖已有封面。剩余验收：提交/PR/CI/合并后构建 `latest`，NAS 拉取新镜像，再验证 `/api/info` git hash、completed MP4 缺 `.jpg/.nfo` 数为 0、规范化重复 show 组为 0，并使用用户本地 Chrome + Computer Use 验收 BiliNote 最近文档封面和 UGREEN 媒体库剧集库。

2026-06-08 same-live 媒体库聚合补齐已在隔离 worktree `worktrees/same-live-media-library-aggregation` / branch `codex/same-live-media-library-aggregation` 完成代码实现和本地测试，尚未提交、PR、构建镜像或部署 NAS。根因确认：此前同场直播只在 BiliNote 文档层按 `live_session_id` 聚合，UGREEN 媒体库仍扫描 `video/<主播>/Season 01/*.mp4`，所以由录制切片产生的主段+尾段会继续作为多个可见 S01E 半成品出现。修复策略已改为媒体库最终产物优先：同一 `live_session_id` 在静默窗口稳定后，先用 ffmpeg concat 生成一个可见整场聚合 MP4，命名为 `主播.S01E起始-S01E结束.日期 - 标题.mp4`；生成整场 `.subtitle.json`、`.srt`、`.ass`、`.nfo`、`.jpg` 和 `tvshow.nfo`；原子分段视频移动到 `Season 01/.live_session_segments/<session-hash>/` 作为内部素材保留，分段 sidecar 记录 `live_session_media_aggregate_path` / `live_session_segment_hidden_path`，后续同源重跑会命中已完成 sidecar，不再重新创建可见子分段。整场字幕和 BiliNote 时间轴偏移优先使用每个分段的真实视频时长，字幕尾部只作为 ffmpeg duration 探测失败时的兜底，避免静音尾段导致目录/原片跳转提前。BiliNote 同步 payload 在聚合成功后改指向整场 aggregate `source_video_path`，避免文档跳转继续依赖隐藏子分段。额外修复：Go hardlink 编号和 `scripts/repair-library-sidecars.py` 都能识别 `S01E0002-S01E0003` range 并保留整个编号区间，避免后续重编号复用被聚合集覆盖的 S01E。验证：新增 RED/GREEN 测试覆盖 range 编号保留、同场两个分段最终只留下一个可见 MP4、被整场吸收的分段重跑不会重新发布、真实视频时长大于字幕尾部时仍按视频时长推进 offset；`GOCACHE=/tmp/bililive-go-build-cache go test ./src/... -count=1` 通过；`python3 -m py_compile scripts/repair-library-sidecars.py` 通过。剩余：提交/PR/CI/合并；master 发布 `latest` 后，必须用 NAS 原生 Docker 项目拉取 latest，并用用户本地 Chrome + Computer Use 验收 UGREEN 媒体库同场分段是否只显示一个整场成品。

2026-06-03 same-live 后续复查发现，Bililive-go 侧聚合不是唯一缺口；BiliNote 消费端原先没有理解 `source_videos`/多媒体段契约，会把 `source_id=live-session:*` 当成单一媒体路径渲染，导致原片链接落到 `/api/knowledge/media/live-session%3A...`，截图也无法映射回具体分段。同时，BiliNote 可见笔记记录只按 `task_id` upsert，同一个 `live-session:*` 若多次 ingest 且 task_id 不同，会生成多张历史卡片；长文分块总结还暴露了目录问题：只要部分 H2 有合法 `Content-[mm:ss]`，旧逻辑就整体跳过补齐，后半段章节可能显示 `原片（原片）` 或缺失时间跳转，目录编号也随 LLM 分块从 1 重置。已在独立 BiliNote 工作树 `worktrees/bilinote-same-live-media-segments-clean` / branch `codex/same-live-media-segments` 完成对应修复：模型接收 `source_videos`/`media_segments`；note 后处理按全局时间选择子媒体并用本地秒数生成链接/截图；`live-session:*` 可见记录复用已有 task_id 并用最新 payload 覆盖；目录生成在检测到编号标题时改为全局连续编号，逐标题补齐缺失 Content 标记并清理无效 `原片（原片）` 标签。BiliNote 验证：先 RED 再 GREEN；最终 `PYTHONPATH=backend python3 -m pytest backend/tests/test_prompt_and_note_regressions.py backend/tests/test_knowledge_memory.py backend/tests/test_note_router_regressions.py -q` 返回 `85 passed, 32 warnings`；`git diff --check` 无输出。Bililive-go 同一直播分支同步补充 `media_segments` payload 别名，与 `source_videos` 内容一致，以防消费端契约漂移；验证：`GOCACHE=/tmp/bililive-go-build-cache go test ./src/pipeline/stages -run 'TestBuildKnowledgeSessionIngestPayloadKeepsPerSegmentSourceEvidence|TestSubtitleGenerateDoesNotSkipKnowledgeSyncForSameLiveSessionContinuation|TestSyncKnowledgeLiveSessionPostsAggregatedPayloadOnceAfterQuietWindow' -count=1` 返回通过，`GOCACHE=/tmp/bililive-go-build-cache go test ./src/... -count=1` 返回通过，`git diff --check` 无输出。剩余：两个仓库分别提交/PR/合并；BiliNote 与 Bililive-go 新 master 发布 latest 后，NAS 拉取并用用户本地 Chrome + Computer Use 验收新的同一直播样本。

2026-06-02 same-live session aggregation 已在隔离 worktree `worktrees/same-live-session-aggregation` / branch `codex/same-live-session-aggregation` 完成代码实现和本地测试，尚未提交、PR、构建镜像或部署 NAS。核心变更：`RecordInfo` 持久化 `live_session_id`；字幕完成后按 `live-session:<id>` 写入 `library_root/.knowledge_sessions/*.json` manifest；同场直播分段在 `knowledge_sync.live_session_quiet_window_seconds` 静默窗口内返回 `RetryLater`，窗口稳定后只向 BiliNote POST 一次聚合 payload；manifest 记录 `posted_content_hash` 防止未变化 session 重复 POST；RetryLater 恢复时若 `.subtitle.json` 已完成则跳过字幕 worker，只继续知识聚合；`min_library_video_duration_seconds` 和 `knowledge_sync.min_video_duration_seconds` 仅过滤无 live session 的独立短片段，不再按分段自身时长排除同场直播内容。最终验证：`GOCACHE=/tmp/bililive-go-build-cache go test ./src/... -count=1` 通过；`PATH=/Users/jansonhan/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin:$PATH GOCACHE=/tmp/bililive-go-build-cache make build-web test` 通过；`PATH=/Users/jansonhan/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin:$PATH GOCACHE=/tmp/bililive-go-build-cache make bililive` 通过；`GOCACHE=/tmp/bililive-go-build-cache make check-agents` 通过；`git diff --check` 通过。注意：本机默认 Node `23.11.0` 与依赖 engine 不兼容，最终 CI 等价验证使用 Codex bundled Node `v24.14.0`。剩余验收：提交/PR；CI 通过后合并；master 发布 `latest` 后由 NAS 拉取验证。若后续做临时镜像 NAS 验证，必须恢复 compose/image tag 到 `latest`；Chrome 验收必须使用用户本地 Chrome + Computer Use，不使用沙箱 Chromium。

已完成 BiliNote NAS backend 运行态 env 门禁修复。2026-05-28 16:51 +08 通过本机 Chrome/Dockge `.env` 编辑态恢复 backend 可读取的 machine ingest env 和 remote vector env，未在命令输出或 plan-tree 中写入真实 token；重新部署后 `GET /api/knowledge/runtime/machine` 返回 PR #40 build `sha=8b4b401c58b451169eadb782ef8a56f22893896c`，ingest token/user/user_exists 为 true，vector index active 为 remote `http://192.168.1.17:8495`。有效 `non_blocking=true` dry-run、非法 `non_blocking={"invalid": true}` schema 探针和 LanceDB `/healthz` 均已通过。执行层面曾停止在真实样本 payload 构建：2026-05-28 16:58 +08 已通过 Bililive-go HTTP API 找到已完成样本路径，但当时只能拿到任务元数据和最终 `.mp4/.srt/.ass` 路径。2026-05-28 17:21 +08 使用用户提供的 NAS SSH 凭据走只读路径取得 task `473` 最终 `.srt`，构建 1623 segment 的真实 payload；dry-run 返回 `drafts_count=15`、`accepted_count=15`。真实 `generate_note=true` + `non_blocking=true` ingest 立即返回 queued，证明请求非阻塞；后台生成了可见 note markdown，但知识入库失败于 `knowledge_items.id` 唯一约束冲突，未生成 `knowledge_sources/items`，LanceDB 只验证到 `/healthz` 可用。当时不再卡在字幕读取，而是卡在 BiliNote 文档优先知识入库的同批次 item id/dedupe 问题。

2026-05-28 17:28 +08 已通过 NAS SSH 对 BiliNote 生产容器和失败 note record 做只读根因复现。生产镜像的 `KnowledgeExtractor.extract(request, markdown=...)` 会把 Markdown 标题拆成文档章节；无时间戳章节回退到整条字幕的起点，并用起点附近 180 秒证据窗口计算相同 `end`。`deterministic_knowledge_id` 只包含 `source_type`、`source_id`、`start`、`end`、`l0_abstract`，而 `_build_abstract` 会把编号标题 `1. ...` / `2. ...` 截成 `装修: 1` / `装修: 2`。task `473` 的失败 markdown 只读复现结果为 `drafts=11`、`accepted=11`、`unique_accepted_ids=9`，两个重复组分别是 `1. 全屋定制报价审核与避坑` 与 `1. 套餐包含明细` 共享 `e29fee720f753e5cbfe9582c5bfcf16e`，`2. 半包施工报价评估（114㎡ 新房）` 与 `2. 潜在风险与避坑建议` 共享 `c0e3b044157400d02b51d84cbfeded38`。BiliNote `KnowledgeStore.upsert_drafts` 对同批次 accepted drafts 没有入库前去重，循环内按 `draft.id` `db.get` / `db.add` 后统一 commit，因此重复 id 会在 `knowledge_items.id` 唯一约束处失败。根因当时已从“未知入库失败”收窄为 BiliNote 文档优先 id 生成与 store 防御性去重缺口；该阶段计划仍不 implementation-ready 于 Bililive-go 侧自动同步实现。

2026-05-28 17:33 +08 已把 BiliNote 修复策略固化为 [Decision 0001](decisions/0001-bilinote-document-first-id-and-retry.md)。只读核对 PR #40 工作树 `/Volumes/ISCSI-Disk/Folder/Bililive-go/.worktrees/bilinote-unified-knowledge-memory` 后确认：当前测试覆盖 document-first ingest 和 non-blocking queue，但没有覆盖无时间戳重复编号章节导致的同批次重复 id。最小复现脚本在该工作树上得到 `drafts=4`、`unique_ids=2`、`duplicate_groups=2`，证明新增回归用例可稳定触发旧问题。现有 `backfill_notes` 会调用 extractor 的 transcript-first 路径，`source rebuild` 又需要已存在的 `KnowledgeSource`；由于 task `473` 没有 source/items 成功落库，失败记录处理策略确定为：BiliNote patch 部署后，用同一真实 payload、同一 `task_id=bililive-go-473`、`generate_note=true`、`non_blocking=true` 重跑一次，不先清理 DB，不使用现有 backfill/rebuild 路径。

2026-05-28 17:40 +08 已在 BiliNote PR #40 工作树 `/Volumes/ISCSI-Disk/Folder/Bililive-go/.worktrees/bilinote-unified-knowledge-memory` 按 Decision 0001 完成最小本地补丁，未提交、未推送、未部署。修改文件为 `backend/app/services/knowledge_extractor.py`、`backend/app/services/knowledge_store.py`、`backend/tests/test_knowledge_memory.py`。补丁行为：document-first draft 写入 `document_section_index`、`document_section_heading`、`document_section_identity` 并将 section identity 纳入 id seed；abstract 构建时去除编号前缀，避免 `装修: 1` / `装修: 2` 坍缩；store 在 admission 后按 `draft.id` 做同批次 dedupe，重复 id 作为 `duplicate_batch_id` 非致命 rejection。新增三条回归测试先在旧代码上 RED：extractor `2 != 4`、route `2 != 4`、store `items_count 2 != 1`；补丁后 targeted tests `3 passed`，完整 BiliNote backend tests `305 passed, 31 warnings`。

2026-05-28 17:44 +08 发布前 review 已完成，仍未提交、未推送、未发布、未部署。review 中只补了一处健壮性修正：`deterministic_knowledge_id` 读取 `support_info` 时兼容 `None`，避免外部构造 `KnowledgeDraft` 时因 `support_info=None` 报错；不改变 document-first 正常行为。重新验证 targeted regression tests 返回 `3 passed, 42 deselected, 3 warnings`，完整 BiliNote backend tests 返回 `305 passed, 31 warnings in 3.51s`，`git diff --check` 无输出。BiliNote 工作树当前只改 3 个文件：`knowledge_extractor.py`、`knowledge_store.py`、`test_knowledge_memory.py`。

2026-05-28 17:47 +08 已核对 BiliNote 发布路径和授权边界，仍未提交、未推送、未发布、未部署。BiliNote OpenSpec 规则确认该改动属于 bug fix（恢复 document-first 知识入库预期行为），不需要新增 proposal；`openspec list --specs` 当前返回 `No specs found`。`.github/workflows/main.yml` 的 Docker workflow 规则为：push 到 `master` 会构建 backend/frontend 并推 `latest`、`git-<sha>`、`branch-master`；`workflow_dispatch` 可指定 `image_suffix`，默认不推 `latest`，只推 `git-<sha>` 和 `branch-<suffix>`，除非显式 `push_latest=true`。当前补丁仍停在 `codex/nonblocking-knowledge-ingest` 工作树，本轮重新验证 targeted regression tests `3 passed, 42 deselected, 3 warnings in 3.07s`，full backend tests `305 passed, 31 warnings in 3.61s`，`git diff --check` 无输出。下一步若获明确授权，建议先提交并推分支，再用 workflow_dispatch 构建带独立 suffix 的 backend image；NAS 验证通过后再决定是否合并/推 latest，避免直接污染 `latest`。

2026-05-28 17:58 +08 已改用 NAS SSH 做只读运行态复查，不再依赖 Dockge 网页终端输入。`sudo docker ps` 显示 `bilinote-backend`、`bilinote-frontend`、`bilinote-nginx`、`bililive`、`subtitle-worker` 均在运行；`bilinote-backend` 当时镜像仍为 `docker.1ms.run/neymar022/bilinote-backend:latest`，image id 为 `sha256:c1f9b00f0c2e3ea95e16292e7e32481823a7f9eaf0f1767a7a0c6f686bb72166`。容器 env/build flags 显示 build `sha=8b4b401c58b451169eadb782ef8a56f22893896c`、`ref=master`、`tag=branch-master`、`time=2026-05-28T05:41:16Z`，ingest token/user 均已配置，vector backend 为 remote `http://192.168.1.17:8495`。通过容器内 token 调 `http://127.0.0.1:8000/api/knowledge/runtime/machine` 返回同一 build SHA、ingest `user_exists=true`、remote LanceDB available=true；运行中 backend 代码未出现 `document_section_identity`、`_dedupe_batch_accepted`、`duplicate_batch_id` 标记，证明本地 BiliNote patch 当时尚未部署。只读 DB 查询显示 `note_records.task_id=bililive-go-473` 仍为 `FAILED`，`markdown_len=4370`，`knowledge_sources/items` 匹配计数仍为 `0`。该阶段 blocker 因此仍是“已验证补丁尚未进入可部署产物并部署到 NAS”，不是 NAS SSH 访问问题。

2026-05-28 18:02 +08 按 Next Target 第 2 项重新执行发布前验证门禁，仍未提交、未推送、未触发 workflow、未部署 NAS、未重跑 task `473`。BiliNote 工作树仍在 `codex/nonblocking-knowledge-ingest`，只修改 `backend/app/services/knowledge_extractor.py`、`backend/app/services/knowledge_store.py`、`backend/tests/test_knowledge_memory.py`。Targeted regression command 返回 `3 passed, 42 deselected, 3 warnings in 3.07s`；full backend command 返回 `305 passed, 31 warnings in 3.61s`；`git diff --check` 无输出。发布前门禁仍 clean，下一步仍需要用户明确授权后才能提交/推送并构建验证镜像。

2026-05-28 19:11 +08 用户已明确授权继续执行直到链路跑通。提交/推送前再次保留发布前门禁：targeted regression command 返回 `3 passed, 42 deselected, 3 warnings in 3.64s`；full backend command 返回 `305 passed, 31 warnings in 3.28s`；`git diff --check` 无输出。下一步按授权提交并推送 BiliNote 分支，然后触发 `workflow_dispatch` 构建独立 suffix 验证镜像，仍暂不推 `latest`。

2026-05-28 19:12 +08 已按授权提交并推送 BiliNote patch。提交为 `b5544ce120d2910724f06a69e976b8cc152918f1`（`Fix document-first knowledge item IDs`），远端分支为 `origin/codex/nonblocking-knowledge-ingest`。已触发 GitHub Actions `Build & Push Docker images` workflow_dispatch，run id `26571213166`，ref `codex/nonblocking-knowledge-ingest`，head SHA `b5544ce120d2910724f06a69e976b8cc152918f1`，`image_suffix=vtok-473-b5544ce`，`push_latest=false`。预期验证镜像 tag 为 `neymar022/bilinote-backend:branch-vtok-473-b5544ce` 和 `neymar022/bilinote-backend:git-b5544ce120d2910724f06a69e976b8cc152918f1`；等待 workflow 完成后再部署 NAS。

2026-05-28 19:54 +08 GitHub Actions 验证镜像构建已完成。Run `26571213166` 结论为 `success`；backend job `78278544429` 于 `2026-05-28T11:34:14Z` 完成，frontend job `78278544405` 于 `2026-05-28T11:35:22Z` 完成。Docker manifest 已验证：`neymar022/bilinote-backend:branch-vtok-473-b5544ce` 与 `neymar022/bilinote-backend:git-b5544ce120d2910724f06a69e976b8cc152918f1` 均存在且 digest 为 `sha256:0420aa61ad5d7705d219c4ff7bfac225923bb441692cb5bec8a6f4f3072961fc`，包含 `linux/amd64` 和 `linux/arm64` manifest。下一步部署 NAS backend 到该验证 tag，并验证运行态 build SHA。

2026-05-28 20:02 +08 NAS backend 已部署到验证 tag。直连 Docker Hub 在 NAS 上拉取超时，改用现有 compose 使用的镜像代理 `docker.1ms.run` 拉取 `docker.1ms.run/neymar022/bilinote-backend:branch-vtok-473-b5544ce`，拉取成功且 digest 为 `sha256:0420aa61ad5d7705d219c4ff7bfac225923bb441692cb5bec8a6f4f3072961fc`。已备份 `/volume2/docker/dockge/stacks/bilinote/compose.yaml` 为 `compose.yaml.bak-vtok-473-b5544ce-20260528200155`，只把 backend image 改为验证 tag，frontend 仍为 `docker.1ms.run/neymar022/bilinote-frontend:latest`，随后 `docker compose up -d backend` 重建并启动 `bilinote-backend`。运行态验证通过：`GET /api/sys_health` 返回 `200`；container image 为 `docker.1ms.run/neymar022/bilinote-backend:branch-vtok-473-b5544ce`；env/build flags 为 `BILINOTE_BUILD_SHA=b5544ce120d2910724f06a69e976b8cc152918f1`、`BILINOTE_BUILD_REF=codex/nonblocking-knowledge-ingest`、`BILINOTE_BUILD_TAG=branch-vtok-473-b5544ce`；补丁标记 `document_section_identity`、`_dedupe_batch_accepted`、`duplicate_batch_id` 均存在；`/api/knowledge/runtime/machine` 返回 build SHA `b5544ce120d2910724f06a69e976b8cc152918f1`、ingest token/user/user_exists 均 true、remote LanceDB available=true。随后按计划只重跑一次 task `473` 同一真实 payload。

2026-05-28 20:25 +08 已按计划重跑 task `473` 同一真实 payload、同一 `task_id=bililive-go-473`、`generate_note=true`、`non_blocking=true`。POST 在约 `0.084s` 返回 queued，后台状态从 `PENDING`/`SUMMARIZING` 到 `SUCCESS`；最新 `note_records.id=24`，`markdown_len=1551`。SQLite 事实源已落库：`knowledge_sources.id=bbb20216b66679f4d7f6e17364cc2f68`，`status=active`，`item_count=3`，`content_hash=39f98a9e14f8c5069b93a8151957b8acacebac651e4fea22ab661f66c799d83d`。3 个 `knowledge_items` 均来自该 source，均保留 `source_video_path`、`subtitle_path`、`start=0.0`、`end=181.64`、`l2_content` 中的 `原字幕证据`，并带有唯一 `document_section_identity`。首次写入 LanceDB 时出现 `index_status=failed` / `index_error=timed out`；直接查 Mac LanceDB `/search` 返回 HTTP 500，stderr 根因为 `Too many open files (os error 24)`。已对 `com.bilinote.mac-lancedb` 启动脚本加入 `ulimit -n 65536 || ulimit -n 4096 || true` 并用 `launchctl kickstart -k` 重启，`/healthz` 恢复；随后把 NAS BiliNote `.env` 的 `BILINOTE_VECTOR_SERVICE_TIMEOUT` 调整为 `30` 并重建 backend，手动 reindex task `473` 的 3 个 item，最终 3 个 item 均为 `index_status=indexed`、`index_error=null`。BiliNote 检索层 `KnowledgeRetriever.search_response(..., query='全屋定制 报价 装修', mode='keyword')` 返回 `total=3`、`high=3`、`sources=1`；远程 LanceDB `/search` 对 `全屋定制 报价 装修` 和 `泰州 全屋定制 抽屉` 均能命中 task `473` item `b107c2de2a3b657a956003f9ab70dfcc`。BiliNote/task `473` blocker 已解除；下一步回到 Bililive-go 侧自动触发、同步状态、错误消息和重试语义。

2026-05-28 22:25 +08 Bililive-go 侧自动触发已用真实生产任务跑通到检索层。实现提交为 `97c3f27fde58260a7626e624271018edeb73ad88`（`Add BiliNote knowledge sync to subtitle pipeline`），远端分支 `origin/codex/bililive-knowledge-sync-status-p17`；GitHub Actions `Publish Docker Images` run `26576824942` 成功，NAS `bililive` 已部署 `docker.1ms.run/neymar022/bililive-go-app:sha-97c3f27`，运行 image config digest 为 `sha256:3ba28fb0499fa2eb86a495eae0427ff7bcd2c4938307ef6d97a1240df7e9888c`。NAS `/volume2/docker/bililive-go/config.yml` 已启用 `subtitle.knowledge_sync.enabled=true`，endpoint 为 `http://192.168.1.80:3015/api/knowledge/ingest`，`provider_id=qwen`，`model_name=qwen3.6-plus`，`generate_note=true`，`non_blocking=true`，`timeout_seconds=30`；`/api/subtitles/settings` 运行态返回同一配置。自动样本为 pipeline task `476`（`Geek徐Sir.S01E0013.2026-05-28 - 今晚开干：零基础学AI编程，开发电商管理系统！.mp4`）。该任务先受串行字幕/烧录通道影响，Mac `/burn` 于 22:18 +08 返回 `200` 后，Bililive stage 日志出现 `POST BiliNote /api/knowledge/ingest (non-blocking)`，task `476` 状态变为 `completed`，不被知识生成阻塞。对应 `.subtitle.json` 为 `status=completed`、`renderer_status=completed`、`segments=2833`、`knowledge_sync_status=queued`、`knowledge_sync_task_id=bililive-go-476`、`knowledge_sync_source_id=Geek徐Sir/Season 01/Geek徐Sir.S01E0013.2026-05-28 - 今晚开干：零基础学AI编程，开发电商管理系统！.mp4`、`knowledge_sync_attempts=1`、`knowledge_sync_error=null`。BiliNote 后台随后生成 note `id=25`，`status=SUCCESS`，`markdown_len=3907`；SQLite 事实源落库 `knowledge_sources.id=42b301ffe99c0f424e5fbaac1a2b2fa6`，`task_id=bililive-go-476`，`status=active`，`item_count=19`，`content_hash=ca3331e0c2f33a2730edc7b661a02d18231388ccffbb9e062fdee9fafa99f5f8`。19 个 `knowledge_items` 均通过 `source_ref_id=42b301ffe99c0f424e5fbaac1a2b2fa6` 关联，全部 `index_status=indexed`、`index_error=null`，并保留 `source_id` 相对媒体库路径、`source_video_path`、`subtitle_path`、时间戳和 `原字幕证据`。远程 Mac LanceDB `/healthz` 返回 `ok=true`；`/search` 查询 `Vue Vite Element UI Flask MySQL 电商管理系统` 命中 task `476` item `f606f358722b26e87776a99543e33f50`，查询 `DeepSeek Trae AI编程 成本 token` 命中 task `476` item `74490e2b098e0720bb74691ea816389f`。该证据闭合了 Bililive-go 新视频烧录完成后自动非阻塞触发 BiliNote ingest、可见 note、SQLite source/items、LanceDB index/search 的端到端成功路径。task `474`、`475`、`477` 仍在串行字幕/烧录队列中运行或排队，属于后续观察项，不再是 task `476` 成功链路的 blocker。

2026-05-28 22:51 +08 连续自动样本观察已补齐。pipeline task `474`、`475`、`477`、`478` 均为 `completed`；对应 stage 日志均出现 `POST BiliNote /api/knowledge/ingest (non-blocking)` 和 `知识同步已提交`。四个 `.subtitle.json` 均为 `status=completed`、`renderer_status=completed`、`knowledge_sync_status=queued`、`knowledge_sync_attempts=1`、`knowledge_sync_error=null`，task id 分别写为 `bililive-go-474/475/477/478`。BiliNote 后台 note 均为 `SUCCESS`：task `474` note `id=27` `markdown_len=437`，task `475` note `id=26` `markdown_len=1265`，task `477` note `id=28` `markdown_len=164`，task `478` note `id=29` `markdown_len=406`。SQLite source 均已落库：task `474` source `e90ed25b97b83a94dd6bd2adc778beb3` `item_count=2`，task `475` source `f418fb95e1735460868264b9ef605e74` `item_count=4`，task `477` source `72294e5bb5d659d2c33013ecd865d681` `item_count=1`，task `478` source `2e68128e66f845b608af521662b7b491` `item_count=0`。其中 `474`、`475`、`477` 的 7 个 item 全部 `index_status=indexed` 且 `index_error=null`；task `478` 是只有 1 段字幕的退化样本，证明 note/source 入口成功和非阻塞语义，但不适合作为检索验收样本。远程 LanceDB `/search` 验证：`设计师 加班 画图 建筑师` 命中 task `477` item `e55cdb7957e655a399b0228182edc362`；`墙面基层 涂料 51 cm 衣帽间` 命中 task `475` items `415bb4ae17df2126678615b45787687c`、`35c9f406c1d67877472d5d39e99864ae`；`装修达人 免费连麦 连麦问答 核心内容` 命中 task `474` items `86d4f66bcd5a7df9fe0b1cad6780a826`、`46f0bbf6577b0c4a43f1c25bf1a94b63`。运行态排队核对：`task_queue.max_concurrent=3`，task `474/475` 于 21:33 +08 开始，task `477` 于 22:18 +08 开始，task `478` 于 22:39 +08 开始；当前 API 配置没有暴露“只在凌晨 2 点烧录”的生效字段，本批次也已在 2 点前实际启动，当前观察到的延迟更符合 pipeline/远端 burn 串行排队，而不是一个仍在阻止任务运行的 02:00 gate。

2026-05-28 22:51 +08 发布治理状态已收窄。BiliNote workflow run `26571213166` 和 Bililive-go workflow run `26576824942` 均为 `success`；但 BiliNote 验证提交 `b5544ce120d2910724f06a69e976b8cc152918f1` 尚未进入 `origin/master`（当前 `origin/master=8b4b401c58b451169eadb782ef8a56f22893896c`），Bililive-go 验证提交 `97c3f27fde58260a7626e624271018edeb73ad88` 也尚未进入 `origin/master`（当前 `origin/master=e2931e6e5b1acff0a788c463686a95cdc0eb35f5`）。`Neymar022/bililive-go` 当前没有该 head branch 的 PR；`Neymar022/bilinote` 只查到已 merged 的 PR #40，验证提交 `b5544ce` 是该分支后续新增提交，不应误认为已经随 PR #40 进入 master。决策：NAS 继续 pin BiliNote `branch-vtok-473-b5544ce` 与 Bililive-go `sha-97c3f27` 作为验证 tag；进入稳定发布前需要显式开 PR/合并/打稳定 tag 或明确继续 pin。链路成功路径已经 implementation-ready；小批量/全量 backfill 仍不 implementation-ready，需先完成发布 tag 决策、失败 smoke/rebuild/限流恢复策略。

## Active TODO

- 单样本端到端自动链路已通过：
  - BiliNote/task `473` 手动真实 payload 验证已通过：note/source/items/index/search 均成功。
  - Bililive-go/task `476` 自动触发验证已通过：烧录完成后自动 POST BiliNote，`.subtitle.json` 写入 `knowledge_sync_status=queued`，BiliNote note `SUCCESS`，1 个 source、19 个 item 成功落库，19 个 item 全部 `indexed`，远程 LanceDB `/search` 可命中。
  - 连续自动样本 task `474`、`475`、`477`、`478` 已验证：全部 pipeline completed，全部 sidecar 写入 `knowledge_sync_status=queued`，全部进入 BiliNote note/source；其中 `474/475/477` 共产生 7 个 indexed item 且 LanceDB search 可命中，`478` 因只有 1 段字幕产生 0 item，不作为检索样本。
  - Bililive-go 成功状态未被 BiliNote 后台生成阻塞；知识同步请求本身为非阻塞 queued 语义。
- 当前仅剩收尾/发布治理目标：
  - 决定 BiliNote 验证 tag 与 Bililive-go 验证 tag 是否开 PR、合并、发布稳定 tag，或继续 pin 在 NAS。
  - 如需生产级失败验收，再做受控坏 endpoint/停 BiliNote 的非阻塞失败 smoke；当前代码单测已覆盖 BiliNote 502 不影响 subtitle stage。
  - backfill 暂不进入执行，直到发布 tag、失败 smoke、限流/恢复和 LanceDB rebuild 路径明确。

## Last Known Verification

- 本仓库已有 PRD 记录 3 个烧录成功样本，速度约 2.8x-3.0x。
- BiliNote 后端相关测试在旧 thread 中通过：`PYTHONPATH=backend python3 -m pytest backend/tests -q`，结果 `302 passed, 29 warnings`。
- 2026-05-28 14:00 +08 运行态门禁检查：
  - `GET http://192.168.1.80:3015/api/sys_health` 返回 `200`。
  - 未带 token 访问 `GET /api/knowledge/runtime/machine` 返回 `401 Missing bearer token`，说明 machine runtime 路由存在。
  - 带 `BILINOTE_INGEST_TOKEN` 访问 `GET /api/knowledge/runtime/machine` 返回 build `sha=5bb2758b5ea35c81ae5e455578e97447c12ee0b4`, `ref=master`, `tag=branch-master`, `time=2026-05-27T14:55:09Z`；ingest token/user/user_exists 均为 true；vector index active 为 remote `http://192.168.1.17:8495`，remote backend 为 `mac_lancedb`。
  - `POST /api/knowledge/ingest?dry_run=true` 带 machine token 可接受最小 bililive-go payload 并返回 `dry_run=true`, `drafts_count=1`, `accepted_count=1`；但 dry-run 不能证明 `non_blocking` 生效，因为 PR #39 代码会忽略未知字段。
  - `GET http://192.168.1.17:8495/healthz` 返回 `200`，root 为 `/Volumes/BiliNoteRuntime/mac-lancedb/data`，table 为 `knowledge_items`；`GET /stats` 返回 `404`。
  - `git fetch origin master` 后，`origin/master` 为 `8b4b401c58b451169eadb782ef8a56f22893896c`（PR #40，提交时间 2026-05-28 13:40 +08）。运行态 build SHA `5bb2758b5ea35c81ae5e455578e97447c12ee0b4` 是 PR #39 merge，且是 `8b4b401` 的祖先。
  - `git diff 5bb2758..8b4b401 -- backend/app/models/knowledge_model.py backend/app/routers/knowledge.py` 显示 PR #40 才新增 `non_blocking` 字段、`BackgroundTasks` 队列返回和后台文档优先 ingest 路径。
- 2026-05-28 14:02 +08 复查运行态门禁：
  - `GET /api/knowledge/runtime/machine` 仍返回 build `sha=5bb2758b5ea35c81ae5e455578e97447c12ee0b4`，vector index 仍 active remote，ingest token/user/user_exists 仍为 true。
  - `POST /api/knowledge/ingest?dry_run=true` 带 `non_blocking={"invalid": true}` 仍返回 `200` 和 `dry_run=true`，而不是请求模型校验失败；这说明当前生产请求模型会忽略未知 `non_blocking` 字段，不能证明 PR #40 的 `KnowledgeIngestRequest.non_blocking: bool` 已部署。
  - `POST /api/knowledge/ingest?dry_run=true` 带 `non_blocking=true` 也返回 `200`，但结合非法类型探针，只能证明 dry-run ingest 可用，不能证明后台非阻塞分支存在。
  - LanceDB `GET /healthz` 仍返回 `200`，`GET /stats` 仍返回 `404`。
- 2026-05-28 14:07 +08 镜像发布与 NAS 刷新复查：
  - Docker Hub `neymar022/bilinote-backend:branch-master`、`:latest`、`:git-8b4b401c58b451169eadb782ef8a56f22893896c` 均已存在，且共同指向 digest `sha256:9d365865165d0c4194ec9365cb278e95f1a2448e8461614502ae3d57541d895d`。
  - `branch-master` tag `last_updated=2026-05-28T06:02:14Z`，`git-8b4b401c58b451169eadb782ef8a56f22893896c` tag `last_updated=2026-05-28T06:02:11Z`。
  - 复查 `GET /api/knowledge/runtime/machine` 仍返回 build `sha=5bb2758b5ea35c81ae5e455578e97447c12ee0b4`, `time=2026-05-27T14:55:09Z`，晚于 Docker Hub 新 tag 发布时间后仍未变化。
  - 复查非法 `non_blocking={"invalid": true}` dry-run 仍返回 `200` 和 `dry_run=true`，进一步证明 NAS backend 尚未运行 PR #40 请求模型。
  - 本机没有可用的 NAS Docker context；`/volume2/docker/dockge/stacks/bilinote` 未挂载到当前 Mac；直接 SSH 到 `192.168.1.80` 此前返回认证失败。因此当前线程只能证明需要刷新 NAS Dockge/backend 容器，不能安全执行该部署动作。
- 2026-05-28 16:10 +08 再次复查版本门禁和访问路径：
  - `GET /api/knowledge/runtime/machine` 仍返回 build `sha=5bb2758b5ea35c81ae5e455578e97447c12ee0b4`, `time=2026-05-27T14:55:09Z`。
  - 非法 `non_blocking={"invalid": true}` dry-run 仍返回 `200` 和 `dry_run=true`。
  - `GET /api/sys_health` 返回 `200`，LanceDB `GET /healthz` 返回 `200`。
  - Dockge 在 `http://192.168.1.80:5001` 可访问，但 Chrome 打开后是登录页；当前没有可复用的已登录会话。
  - 非交互 SSH 探针 `Neymar@192.168.1.80`、`neymar@192.168.1.80`、`root@192.168.1.80` 均返回 `Permission denied (publickey,password)`。
  - 结论：当前线程仍不能安全刷新 NAS backend 容器；需要用户在 Dockge 登录后刷新/recreate，或提供可用 SSH/Docker context。
- 2026-05-28 16:13 +08 复查版本门禁：
  - `GET /api/knowledge/runtime/machine` 仍返回 build `sha=5bb2758b5ea35c81ae5e455578e97447c12ee0b4`, `time=2026-05-27T14:55:09Z`。
  - 非法 `non_blocking={"invalid": true}` dry-run 仍返回 `200` 和 `dry_run=true`。
  - Docker Hub `neymar022/bilinote-backend:git-8b4b401c58b451169eadb782ef8a56f22893896c` 仍存在，`last_updated=2026-05-28T06:02:11Z`，digest 为 `sha256:9d365865165d0c4194ec9365cb278e95f1a2448e8461614502ae3d57541d895d`。
  - Dockge 页面仍是登录页；非交互 SSH `Neymar@192.168.1.80` 仍返回 `Permission denied (publickey,password)`；Docker context 仍只有本机 `default`/`desktop-linux`。
- 2026-05-28 16:15 +08 复查版本门禁：
  - `GET /api/knowledge/runtime/machine` 仍返回 build `sha=5bb2758b5ea35c81ae5e455578e97447c12ee0b4`, `time=2026-05-27T14:55:09Z`。
  - 非法 `non_blocking={"invalid": true}` dry-run 仍返回 `200` 和 `dry_run=true`。
  - Docker Hub `neymar022/bilinote-backend:git-8b4b401c58b451169eadb782ef8a56f22893896c` 仍存在，`last_updated=2026-05-28T06:02:11Z`，digest 为 `sha256:9d365865165d0c4194ec9365cb278e95f1a2448e8461614502ae3d57541d895d`。
  - Dockge HTTP 首页可访问但仍需要登录；非交互 SSH `Neymar@192.168.1.80` 仍返回 `Permission denied (publickey,password)`；Docker context 仍只有本机 `default`/`desktop-linux`。
- 2026-05-28 16:23 +08 通过本机 Chrome/Computer Use 使用已登录 Dockge 会话进入 `http://192.168.1.80:5001/compose/bilinote`：
  - Dockge 项目页显示 backend 镜像为 `docker.1ms.run/neymar022/bilinote-backend:latest`，frontend 镜像为 `docker.1ms.run/neymar022/bilinote-frontend:latest`，nginx 为 `nginx:1.25-alpine`。
  - 点击项目级 `更新` 后，Dockge 日志显示 `backend Pulled`、`frontend Pulled`、`nginx Pulled`，随后 `container bilinote-nginx Started`、`container bilinote-frontend Started`、`container bilinote-backend Started`。
  - 更新后 `GET /api/sys_health` 返回 `200`。
  - 更新后带 machine token 访问 `GET /api/knowledge/runtime/machine` 返回 `401 {"detail":"BILINOTE_INGEST_TOKEN is not configured"}`，因此无法从 machine endpoint 读取 build SHA、vector index 或 ingest user 状态。
  - 更新后非法 `non_blocking={"invalid": true}` dry-run 返回 `422 Unprocessable Entity`，包含 `loc=["body","non_blocking"]` 和 `type="bool_type"`，证明请求模型已包含 PR #40 的 `non_blocking: bool` 字段。
  - 更新后有效 `non_blocking=true` dry-run 无论带不带 machine token 都返回 `401 BILINOTE_INGEST_TOKEN is not configured`，因此不能执行真实样本 ingest。
- 2026-05-28 16:29 +08 再次复查 env/schema 门禁：
  - `GET /api/sys_health` 仍返回 `200`。
  - 带本机保存的 machine token 访问 `GET /api/knowledge/runtime/machine` 仍返回 `401 {"detail":"BILINOTE_INGEST_TOKEN is not configured"}`。
  - 非法 `non_blocking={"invalid": true}` dry-run 仍返回 `422 Unprocessable Entity`，且只剩 `loc=["body","non_blocking"]` / `type="bool_type"`，继续证明 PR #40 请求模型已部署。
  - LanceDB `GET http://192.168.1.17:8495/healthz` 仍返回 `200`，root 为 `/Volumes/BiliNoteRuntime/mac-lancedb/data`，table 为 `knowledge_items`。
  - 尝试通过 Dockge backend Bash 执行不打印 secret 的 env/build probe，但页面终端输入未产生可审计输出；不要把该路径作为当前证明来源。
- 2026-05-28 16:51 +08 BiliNote NAS backend env 修复与门禁复查：
  - 先用小写 ASCII 终端 smoke command 验证 Dockge Web terminal 输入通道，`printf 'ok\n'` 输出 `ok`。
  - BiliNote backend 容器内非敏感 env probe 显示：`token_set=no`、`user_id_set=no`、`user_id=unset`、`build_sha=8b4b401c58b451169eadb782ef8a56f22893896c`、`build_tag=branch-master`、`lancedb_runtime=unset`。
  - Dockge `bilinote` stack `.env` key-only 检查显示原先缺少 `BILINOTE_INGEST_TOKEN`、`BILINOTE_INGEST_USER_ID`、`BILINOTE_LANCEDB_RUNTIME`、`BILINOTE_VECTOR_BACKEND`、`BILINOTE_VECTOR_SERVICE_URL`、`BILINOTE_VECTOR_SERVICE_TIMEOUT`；通过页面编辑器同步本机 BiliNote `.env` 中对应值并点击 `部署`，未打印真实 token。
  - 部署后 `GET /api/knowledge/runtime/machine` 返回 build `sha=8b4b401c58b451169eadb782ef8a56f22893896c`，`ref=master`，`tag=branch-master`，ingest `token_configured/user_id_configured/user_exists` 均为 true；vector index 为 adaptive/remote active，remote URL `http://192.168.1.17:8495`，remote backend `mac_lancedb`，remote root `/Volumes/BiliNoteRuntime/mac-lancedb/data`。
  - 有效 `POST /api/knowledge/ingest?dry_run=true` 带 `non_blocking=true` 返回 `200`，`dry_run=true`、`drafts_count=1`、`accepted_count=1`。
  - 非法 `non_blocking={"invalid": true}` dry-run 返回 `422`，`loc=["body","non_blocking"]`，`type="bool_type"`。
  - LanceDB `GET http://192.168.1.17:8495/healthz` 返回 `200`，`ok=true`，backend `mac_lancedb`，table `knowledge_items`。
- 2026-05-28 16:51 +08 真实样本访问复查：
  - 当前 Mac 的 `/Volumes/ISCSI-Disk/Folder/Bililive-go` 未挂载 NAS 媒体库 `video/`，`/Volumes/MOVESPEED` 也未找到 PRD 中的 `旭东聊装修.S01E0003/0004/0005` 样本。
  - BiliNote backend 容器内 `test -d /volume2/docker/bililive-go/video` 返回 `media_mount=no`，不能从 BiliNote backend 直接读取样本字幕。
  - Dockge `/console` 显示 `Console is not enabled`，不能通过 Dockge 主终端访问 host Docker/stack 文件。
  - Dockge `bililive-go` stack 页面显示 compose `env_file: /volume2/docker/bililive-go/.env`，终端日志报该 env file 不存在，`bililive` 与 `subtitle-worker` 服务状态为 `N/A`；`subtitle-worker` terminal 只显示 env file missing，未得到可用 shell。不要在本计划范围内顺手修复 bililive-go stack。
- 2026-05-28 16:58 +08 真实样本路径发现：
  - NAS SMB/NFS 端口对当前 Mac 不可用；本机仍未挂载 NAS 媒体库。
  - `http://192.168.1.80:18090/` 的 Bililive-go Web/API 可访问；`GET /api/info` 返回 `200`。
  - `GET /api/pipeline/tasks?status=completed&limit=10` 返回多个已完成样本；候选 task `473` 为 `旭东聊装修`，最终文件包括 `/volume2/docker/bililive-go/video/旭东聊装修/Season 01/旭东聊装修.S01E0047.2026-05-27 - 装修达人。免费连麦解决装修问题。装修知识科普官.mp4`、同名 `.srt` 和 `.ass`，stage `subtitle_generate` 状态为 `completed`。
  - `GET /api/file/旭东聊装修` 只暴露当前 `out_put_path=/volume2/docker/bililive-go/srt_video` 下的文件；未发现可用于 payload 的 `.srt`、`.ass` 或 `.subtitle.json`。
  - 通过 `/files/../video/...` 和 `/api/file/../video/...` 读取最终媒体库 `.srt` 均不可用或被重定向后返回 `404`；因此当前只能证明候选样本路径，不能读取真实字幕内容。
  - 避免在后续日志或文档中转储完整 `/api/config`，该接口可能包含 cookies 等敏感字段；需要配置事实时只提取非敏感字段。
- 2026-05-28 17:03 +08 真实字幕内容访问路径复查：
  - 仓库代码 `src/servers/server.go` 与 `src/servers/handler.go` 显示 `/files/`、`/api/file/{path}` 均以 `configs.GetCurrentConfig().OutPutPath` 为根，并通过 `getSafePath` 阻止越界；这解释了为什么无法通过 `../video` 读取最终媒体库字幕。
  - `GET /api/pipeline/tasks/473` 可返回完整 task 元数据、最终 `.mp4/.srt/.ass` 路径、`subtitle_generate` completed 状态和 worker command，但响应不包含字幕正文。
  - 横向扫描 `GET /api/file/` 下所有可访问主播目录，未发现 `.srt`、`.ass` 或 `.subtitle.json`。
  - `192.168.1.80:8091` 连接拒绝，subtitle-worker 没有对当前 Mac 暴露只读 API；`GET /api/openlist/status` 返回 `openlist_running=false`，没有可用 OpenList 存储导出。
  - `18090/files/旭东聊装修/` 可列出 `srt_video` 当前目录中的 nfo 和未归集/处理中视频，但没有 task `473` 的最终 `.srt` 内容。
- 2026-05-28 17:09 +08 其它只读访问路径复查：
  - `GET /api/config/effective` 只提取非敏感字段，确认运行态 `out_put_path=/volume2/docker/bililive-go/srt_video`、`app_data_path=/srv/bililive/.appdata`；`/api/file/.appdata`、`/api/file/reports`、`/api/file/tools`、`/api/file/video` 均返回获取目录失败。
  - 当前 Mac 只挂载 `/Volumes/ISCSI-Disk`、`/Volumes/MOVESPEED`、`/Volumes/BiliNoteRuntime` 和本地时间磁盘，没有 `192.168.1.80` 的 SMB/NFS/WebDAV 媒体库挂载。
  - UGREEN `http://192.168.1.80:9999/desktop/?os=ugospro` 可达，但当前 Chrome 会话跳转到 `#/login/account`，没有可复用登录态；未尝试提交登录。
  - WebDAV `5005/5006` 可达，但 `OPTIONS`、`PROPFIND` 和候选 `.srt` `HEAD` 均返回 `401 Unauthorized`；未尝试凭证。
  - SSH `jansonhan`、`Neymar`、`neymar`、`root`、`admin` 使用 BatchMode 均返回 `Permission denied (publickey,password)`。
  - openresty 常见静态路径 `/video`、`/media`、`/downloads`、`/files` 和绝对路径变体访问候选 `.srt` 均为 `404`。
- 2026-05-28 17:21 +08 SSH 样本读取与真实 ingest 验证：
  - 使用用户提供的 NAS SSH 凭据建立只读访问路径；未把密码写入 plan-tree。
  - `sudo docker ps` 显示实际运行容器包括 `bililive`、`subtitle-worker`、`bilinote-backend`、`bilinote-nginx`；这说明 Dockge `bililive-go` 页面 `env_file` 报错是 Dockge/compose 管理视图漂移，不等同于实际 `18090` 服务未运行。
  - 通过远端 `find /volume2/docker/bililive-go/video -name "*S01E0047*.srt"` 定位并流式读取 task `473` 最终 `.srt`；本机临时文件大小 `97328` bytes，解析出 `1623` 个 segments，时间范围 `00:00:00.000` 到 `3655.22s`。
  - 运行态 BiliNote backend 代码确认 `KnowledgeIngestRequest` 支持 `generate_note` 与 `non_blocking`；`generate_note=true` 需要同时提供 `model_name` 和 `provider_id` 才会进入文档优先生成。
  - 只读 DB 检查显示用户级 provider setting 中 `qwen` enabled，候选 model 为 `qwen3.6-plus`；未读取或记录 API key。
  - 构建真实 payload：`source_id` 为最终 `.mp4` 相对媒体库路径，`task_id=bililive-go-473`，`host=旭东聊装修`，`source_video_path` 和 `subtitle_path` 均指向 `/volume2/docker/bililive-go/video/...`。
  - `POST /api/knowledge/ingest?dry_run=true` 返回 `200`，`dry_run=true`、`drafts_count=15`、`accepted_count=15`、`rejected_count=0`。
  - 真实 `POST /api/knowledge/ingest` 返回 `200`，耗时约 `0.05s`，`source.queued=true`、`note.queued=true`、`note.mode=background`，证明请求非阻塞。
  - 后台任务先进入 `SUMMARIZING`，随后 `note_records.task_id=bililive-go-473` 变为 `FAILED`；`markdown_len=4370`，说明可见 note markdown 已生成。
  - 失败原因是 `(sqlite3.IntegrityError) UNIQUE constraint failed: knowledge_items.id`，发生在写入 `knowledge_items` 时；错误参数显示同一次文档优先抽取产生了重复 item id。
  - `knowledge_sources` 中没有 task `473` 或 `S01E0047` 对应 source，相关 `knowledge_items` 计数为 `0`；因此 LanceDB 没有该样本可验证的新增索引。
  - LanceDB `GET http://192.168.1.17:8495/healthz` 仍返回 `200`；但该样本的 search/index 验证未通过，因为源和 item 未落库。
- 2026-05-28 17:28 +08 BiliNote document-first 重复 id 根因复现：
  - 生产容器 `/app/app/services/knowledge_extractor.py` 确认 document-first 分支会从 Markdown 标题生成知识 draft；无时间戳章节回退到 `fallback_start=0.0`，并选取起点后 180 秒字幕作为证据窗口。
  - 生产容器 `deterministic_knowledge_id` 只使用 `source_type`、`source_id`、`start`、`end` 和 `l0_abstract`，没有章节序号、原始 heading、section hash 或 source range。
  - 对 `note_records.task_id=bililive-go-473` 的 `form_data + markdown` 只读调用 extractor：`segments=1623`、`markdown_len=4370`、`drafts=11`、`accepted=11`、`unique_accepted_ids=9`。
  - 重复组 1：`1. 全屋定制报价审核与避坑` 与 `1. 套餐包含明细` 均为 `start=0.0`、`end=181.64`、`abstract=装修: 1`，共享 id `e29fee720f753e5cbfe9582c5bfcf16e`。
  - 重复组 2：`2. 半包施工报价评估（114㎡ 新房）` 与 `2. 潜在风险与避坑建议` 均为 `start=0.0`、`end=181.64`、`abstract=装修: 2`，共享 id `c0e3b044157400d02b51d84cbfeded38`。
  - 生产容器 `KnowledgeStore.upsert_drafts` 在 admission 后直接循环 accepted drafts，按 `draft.id` 查找或新增 `KnowledgeItem`，没有同批次 dedupe/merge；最终 commit 时触发 `knowledge_items.id` 唯一约束。
- 2026-05-28 17:33 +08 BiliNote 修复策略收敛：
  - 已新增 [Decision 0001](decisions/0001-bilinote-document-first-id-and-retry.md)，接受三段式修复：document-first id 加章节级身份、abstract 避免编号坍缩、store 同批次重复 id 非致命 dedupe。
  - PR #40 工作树最小脚本复现：四个无时间戳 Markdown 编号章节生成 `drafts=4`、`unique_ids=2`、`duplicate_groups=2`，两个 `1.` 章节共享一个 id，两个 `2.` 章节共享另一个 id。
  - 当前 BiliNote 测试只覆盖 document-first 正常抽取和 non-blocking queue；缺少 extractor/store/route 层的重复编号章节回归测试。
  - `backfill_notes` 当前会从 note record 重建 request 后调用 transcript-first extractor；`source rebuild` 要求已有 `KnowledgeSource`；二者都不适合直接修复 task `473` 的 failed note record。
  - task `473` 后续处理策略确定为补丁部署后同 `task_id` 重跑同一真实 payload；重跑前仍不得再次触发真实 ingest。
- 2026-05-28 17:40 +08 BiliNote 本地补丁和测试：
  - RED：`PYTHONPATH=backend python3 -m pytest backend/tests/test_knowledge_memory.py -q -k 'document_first_markdown_sections_get_unique_ids_when_headings_repeat_numbers or upsert_drafts_deduplicates_duplicate_ids_before_commit or ingest_generate_note_handles_duplicate_numbered_markdown_sections'` 在旧代码上失败，失败点分别证明 extractor 只有 2 个唯一 id、route 返回重复 `item_ids`、store 未把同批次重复 id 降为 1 个 item。
  - GREEN：同一 targeted command 在补丁后返回 `3 passed, 42 deselected, 3 warnings`。
  - FULL：`PYTHONPATH=backend python3 -m pytest backend/tests -q` 返回 `305 passed, 31 warnings in 4.49s`。
  - 本地补丁未触发 NAS task `473` ingest，未提交、未推送、未发布 Docker image。
- 2026-05-28 17:44 +08 BiliNote 发布前 review 后复验：
  - review 修正：`deterministic_knowledge_id` 改为通过 `support_info = draft.support_info or {}` 读取 `document_section_identity`，兼容外部构造 `KnowledgeDraft(support_info=None)`。
  - Targeted regression：`PYTHONPATH=backend python3 -m pytest backend/tests/test_knowledge_memory.py -q -k 'document_first_markdown_sections_get_unique_ids_when_headings_repeat_numbers or upsert_drafts_deduplicates_duplicate_ids_before_commit or ingest_generate_note_handles_duplicate_numbered_markdown_sections'` 返回 `3 passed, 42 deselected, 3 warnings in 2.09s`。
  - Full backend：`PYTHONPATH=backend python3 -m pytest backend/tests -q` 返回 `305 passed, 31 warnings in 3.51s`。
  - `git diff --check` 无输出；BiliNote 工作树只包含 3 个改动文件，未触发真实 ingest，未提交、未推送、未构建或发布镜像。
- 2026-05-28 17:47 +08 BiliNote 发布路径核对和复验：
  - OpenSpec：`openspec list` 显示存在多个历史/活跃 change，`openspec list --specs` 返回 `No specs found`；按 `openspec/AGENTS.md`，本次是 bug fix，跳过新 proposal。
  - Docker workflow：`.github/workflows/main.yml` push `master` 会推 `latest`；manual `workflow_dispatch` 默认 `push_latest=false`，适合先构建带 suffix 的验证镜像。
  - Targeted regression：`PYTHONPATH=backend python3 -m pytest backend/tests/test_knowledge_memory.py -q -k 'document_first_markdown_sections_get_unique_ids_when_headings_repeat_numbers or upsert_drafts_deduplicates_duplicate_ids_before_commit or ingest_generate_note_handles_duplicate_numbered_markdown_sections'` 返回 `3 passed, 42 deselected, 3 warnings in 3.07s`。
  - Full backend：`PYTHONPATH=backend python3 -m pytest backend/tests -q` 返回 `305 passed, 31 warnings in 3.61s`。
  - `git diff --check` 无输出；仍未触发真实 ingest，未提交、未推送、未构建或发布镜像。
- 2026-05-28 17:58 +08 NAS SSH 只读运行态复查：
  - `bilinote-backend` 运行镜像仍为 `docker.1ms.run/neymar022/bilinote-backend:latest`，image id 为 `sha256:c1f9b00f0c2e3ea95e16292e7e32481823a7f9eaf0f1767a7a0c6f686bb72166`。
  - backend container env/build flags：`BILINOTE_BUILD_SHA=8b4b401c58b451169eadb782ef8a56f22893896c`、`BILINOTE_BUILD_REF=master`、`BILINOTE_BUILD_TAG=branch-master`、`BILINOTE_BUILD_TIME=2026-05-28T05:41:16Z`；ingest token/user 均为 set；remote vector URL 为 `http://192.168.1.17:8495`。
  - 容器内 token 调 `http://127.0.0.1:8000/api/knowledge/runtime/machine` 返回同一 build SHA、ingest `token_configured/user_id_configured/user_exists=true`、remote LanceDB `available=true`，remote root `/Volumes/BiliNoteRuntime/mac-lancedb/data`，table `knowledge_items`。
  - 运行中 backend 代码未包含本地补丁标记 `document_section_identity`、`_dedupe_batch_accepted`、`duplicate_batch_id`。
  - 只读 DB 查询显示 `note_records.task_id=bililive-go-473` 仍为 `FAILED`，`markdown_len=4370`；`knowledge_sources` 与 `knowledge_items` 对 `S01E0047` / `bililive-go-473` 的匹配计数均为 `0`。
- 2026-05-28 18:02 +08 发布前门禁复验：
  - BiliNote 工作树仍在 `codex/nonblocking-knowledge-ingest`，未提交、未推送、未构建、未部署、未重跑 task `473`。
  - Targeted regression：`PYTHONPATH=backend python3 -m pytest backend/tests/test_knowledge_memory.py -q -k 'document_first_markdown_sections_get_unique_ids_when_headings_repeat_numbers or upsert_drafts_deduplicates_duplicate_ids_before_commit or ingest_generate_note_handles_duplicate_numbered_markdown_sections'` 返回 `3 passed, 42 deselected, 3 warnings in 3.07s`。
  - Full backend：`PYTHONPATH=backend python3 -m pytest backend/tests -q` 返回 `305 passed, 31 warnings in 3.61s`。
  - `git diff --check` 无输出。
- 2026-05-28 19:11 +08 授权后提交/推送前门禁复验：
  - Targeted regression：`PYTHONPATH=backend python3 -m pytest backend/tests/test_knowledge_memory.py -q -k 'document_first_markdown_sections_get_unique_ids_when_headings_repeat_numbers or upsert_drafts_deduplicates_duplicate_ids_before_commit or ingest_generate_note_handles_duplicate_numbered_markdown_sections'` 返回 `3 passed, 42 deselected, 3 warnings in 3.64s`。
  - Full backend：`PYTHONPATH=backend python3 -m pytest backend/tests -q` 返回 `305 passed, 31 warnings in 3.28s`。
  - `git diff --check` 无输出。
- 2026-05-28 19:12 +08 BiliNote patch 提交/推送与验证镜像构建触发：
  - Commit：`b5544ce120d2910724f06a69e976b8cc152918f1`，message `Fix document-first knowledge item IDs`。
  - Remote branch：`origin/codex/nonblocking-knowledge-ingest`。
  - GitHub Actions：workflow `Build & Push Docker images`，run id `26571213166`，event `workflow_dispatch`，status `in_progress`，head SHA `b5544ce120d2910724f06a69e976b8cc152918f1`。
  - Dispatch inputs：`image_suffix=vtok-473-b5544ce`，`push_latest=false`。
- 2026-05-28 19:54 +08 验证镜像构建完成：
  - GitHub Actions run `26571213166` conclusion `success`。
  - Backend job `78278544429` success, completed at `2026-05-28T11:34:14Z`。
  - Frontend job `78278544405` success, completed at `2026-05-28T11:35:22Z`。
  - `neymar022/bilinote-backend:branch-vtok-473-b5544ce` digest `sha256:0420aa61ad5d7705d219c4ff7bfac225923bb441692cb5bec8a6f4f3072961fc`，platforms include `linux/amd64` and `linux/arm64`。
  - `neymar022/bilinote-backend:git-b5544ce120d2910724f06a69e976b8cc152918f1` points to the same digest.
- 2026-05-28 20:02 +08 NAS backend 验证镜像部署与运行态门禁：
  - NAS 直连 Docker Hub 拉取 `neymar022/bilinote-backend:branch-vtok-473-b5544ce` 超时；使用现有镜像代理 `docker.1ms.run/neymar022/bilinote-backend:branch-vtok-473-b5544ce` 拉取成功，digest `sha256:0420aa61ad5d7705d219c4ff7bfac225923bb441692cb5bec8a6f4f3072961fc`。
  - 已备份 compose 为 `/volume2/docker/dockge/stacks/bilinote/compose.yaml.bak-vtok-473-b5544ce-20260528200155`，只改 backend image 到验证 tag，frontend/nginx 未变。
  - `docker compose up -d backend` 完成，`bilinote-backend` recreate/start 成功。
  - `GET http://192.168.1.80:3015/api/sys_health` 返回 `200`。
  - `docker inspect` 显示 backend container image 为 `docker.1ms.run/neymar022/bilinote-backend:branch-vtok-473-b5544ce`。
  - container env/build flags：`BILINOTE_BUILD_SHA=b5544ce120d2910724f06a69e976b8cc152918f1`、`BILINOTE_BUILD_REF=codex/nonblocking-knowledge-ingest`、`BILINOTE_BUILD_TAG=branch-vtok-473-b5544ce`、`BILINOTE_BUILD_TIME=2026-05-28T11:13:06Z`；ingest token/user 均 set；remote vector URL `http://192.168.1.17:8495`。
  - 运行中代码包含补丁标记 `document_section_identity`、`_dedupe_batch_accepted`、`duplicate_batch_id`。
  - `/api/knowledge/runtime/machine` 返回 build SHA `b5544ce120d2910724f06a69e976b8cc152918f1`，ingest `token_configured/user_id_configured/user_exists=true`，remote LanceDB `available=true`，remote root `/Volumes/BiliNoteRuntime/mac-lancedb/data`，table `knowledge_items`。
- 2026-05-28 20:02 +08 NAS backend 验证镜像部署与运行态门禁：
  - NAS 直连 Docker Hub 拉取 `neymar022/bilinote-backend:branch-vtok-473-b5544ce` 超时；使用现有镜像代理 `docker.1ms.run/neymar022/bilinote-backend:branch-vtok-473-b5544ce` 拉取成功，digest `sha256:0420aa61ad5d7705d219c4ff7bfac225923bb441692cb5bec8a6f4f3072961fc`。
  - 已备份 compose 为 `/volume2/docker/dockge/stacks/bilinote/compose.yaml.bak-vtok-473-b5544ce-20260528200155`，只改 backend image 到验证 tag，frontend/nginx 未变。
  - `GET http://192.168.1.80:3015/api/sys_health` 返回 `200`；`/api/knowledge/runtime/machine` 返回 build SHA `b5544ce120d2910724f06a69e976b8cc152918f1`、ingest token/user/user_exists 均 true、remote LanceDB available=true。
- 2026-05-28 20:25 +08 task `473` 重跑和知识链路验证：
  - 最新 `note_records.task_id=bililive-go-473` 为 `SUCCESS`，`note_records.id=24`，`markdown_len=1551`，`updated_at=2026-05-28 12:14:26`。
  - `knowledge_sources.id=bbb20216b66679f4d7f6e17364cc2f68`，`status=active`，`item_count=3`。
  - `knowledge_items` 为 `7163881f446cb54b86a8809bf694ecc7`、`b107c2de2a3b657a956003f9ab70dfcc`、`d3142f010ac7acb2b4a0d5dc15b50266`；三者均 `index_status=indexed`、`index_error=null`。
  - 三个 item 均有 `source_video_path`、`subtitle_path`、`start=0.0`、`end=181.64`、`原字幕证据` 和 `document_section_identity`。
  - Mac LanceDB 初次 search 失败根因为 `Too many open files (os error 24)`；已通过 `com.bilinote.mac-lancedb` 启动脚本 `ulimit` 和 launchd 重启修复。
  - NAS backend `.env` 已把 `BILINOTE_VECTOR_SERVICE_TIMEOUT` 调整为 `30` 并重建 backend；3 个 task `473` item 已 reindex 成功。
  - 远程 LanceDB `/search` 查询 `全屋定制 报价 装修` 命中 `b107c2de2a3b657a956003f9ab70dfcc`；查询 `装修达人 免费连麦` 命中 task `473` 的三个 item。
- Bililive-go 本仓库当前仍只修改 `docs/plantree`/PRD 相关规划文档；尚未进入 Bililive-go 自动同步实现。

## Blockers

- 成功路径 blocker 已解除：真实字幕读取、BiliNote document-first 入库、字幕证据字段、路径字段、Bililive-go 自动触发、sidecar 状态、LanceDB index/search 均已验证。
- 剩余不是实现阻塞，而是发布/运维风险：
  - BiliNote backend 当前运行验证 tag `branch-vtok-473-b5544ce`，对应提交 `b5544ce` 尚未进入 BiliNote `origin/master`；Bililive-go app 当前运行验证 tag `sha-97c3f27`，对应提交 `97c3f27` 尚未进入 Bililive-go `origin/master`。
  - 生产级失败路径尚未用受控坏 endpoint 实测；当前只通过 Go 单元测试证明 BiliNote 502 不会让 subtitle stage 失败。
  - LanceDB 可从 SQLite facts 全量 rebuild 仍未验证；当前已证明 task `473` 手动 reindex/search 和 task `476` 自动 index/search。
  - task `478` 为 1 段字幕退化样本，BiliNote note/source 成功但 item_count 为 0；这不是同步 blocker，但不能作为检索质量验收样本。

## Next Target

当前 Next Target 不再是实现自动同步；task `476` 和连续样本 `474/475/477` 已证明自动同步成功路径，task `478` 已证明退化短字幕样本不会阻塞 pipeline。下一步唯一目标是收尾发布治理：

1. 对 BiliNote `branch-vtok-473-b5544ce` 和 Bililive-go `sha-97c3f27` 做发布治理决策：开 PR/合并/发布稳定 tag，或明确继续 NAS pin 验证 tag。
2. 如需要更强生产验收，补一次受控失败 smoke：临时 bad endpoint 或 BiliNote 不可达，确认 `.subtitle.json` 记录 `knowledge_sync_status=failed`，但 pipeline 仍 completed。
3. 后续再进入小批量 backfill/全量回填策略；不要在未决定发布 tag、失败 smoke、限流/恢复和 LanceDB rebuild 路径之前扩大到 100+ 视频。

## Verification Commands

- 后端 Go 变更：`make dev`
- 前端变更：`make build-web dev`
- 单元测试：`make test`
- lint：`make lint`
- E2E：`make test-e2e`
- BiliNote backend 测试参考：`PYTHONPATH=backend python3 -m pytest backend/tests -q`
- BiliNote 运行态需额外验证：Docker image SHA、`/api/knowledge/runtime/machine`、`/api/knowledge/ingest` dry-run/real-run、LanceDB `/healthz` 和 search/rebuild 证据。

## Handoff Notes

- 不要把知识抽取和长期治理搬进 Bililive-go。
- 不要让 BiliNote/LanceDB 失败改变视频烧录成功状态。
- 不要把 LanceDB 当成事实源；索引必须可重建。
- 不要在未验证镜像 SHA 和运行态之前宣称生产链路已跑通。
- 不要继续依赖旧 provider goal 的内存状态；任何新结论都要回写 plan-tree。

## 2026-06-13 原片截图缺失 RCA

- 用户报告：BiliNote 已勾选“原片截图”，但最新和最近由自动烧录生成的文档均没有原片截图。
- 运行态证据：
  - `GET http://192.168.1.80:18090/api/info` 返回 `app_version=806da08`、`git_hash=806da08c5cc299d593910a83221c6c6e640532d1`。
  - `GET /api/subtitles/settings` 的 `subtitle.knowledge_sync` 只包含 `enabled/endpoint/provider_id/model_name/generate_note/non_blocking/timeout/min_video_duration/live_session_quiet_window`，没有 `format`、`link`、`screenshot`、`video_understanding`。
  - 最近完成任务 `#672/#673` 的字幕阶段命令均为 `POST BiliNote /api/knowledge/ingest (same-live aggregation, non-blocking)`，即走 Bililive-go 自动同步机器接口，不走 BiliNote 左侧表单的手动生成接口。
- 代码证据：
  - Bililive-go `knowledgeIngestPayload` 支持发送 `format/link/screenshot`，但只从 `SubtitleKnowledgeSyncConfig` 复制；当前运行配置为空时不会发送。
  - Bililive-go 默认 `SubtitleKnowledgeSyncConfig` 不设置 `Format/Link/Screenshot`，现有测试也断言默认 `Format` 为空、`Screenshot` 为 `nil`。
  - BiliNote 运行目录 `/Volumes/BiliNoteRuntime/BiliNote/backend/app/models/knowledge_model.py` 中 `KnowledgeIngestRequest` 没有 `format/link/screenshot/style/extras` 字段；手动 `/notes/from_transcript` 使用的 `TranscriptNoteRequest` 才有这些字段。
  - BiliNote 截图后处理只在 `formats` 包含 `"screenshot"` 时进入 `_post_process_markdown` 的截图插入/兜底逻辑。
- 根因：
  - “原片截图”勾选状态只作用于 BiliNote 手动生成路径；Bililive-go 自动烧录同步路径没有继承 UI 勾选项。
  - 更深层设计偏差是 Bililive-go 注释假设 `format/link/screenshot` 留空时 BiliNote 机器 ingest 会使用默认格式，但 BiliNote 当前机器 ingest schema 实际没有这些字段，也没有读取用户 UI 默认格式。因此自动同步生成的文档会稳定缺少原片截图。
- 非根因：
  - 不是媒体库 `.jpg/.nfo/tvshow.nfo` sidecar 封面问题。
  - 不是 ffmpeg 截图器失效；截图器只在 BiliNote 接收到 `screenshot=true` 或 `format` 包含 `screenshot` 后才会被调用。
- 修复方向：
  - BiliNote：扩展 `KnowledgeIngestRequest`，支持并持久化 `format/link/screenshot/style/extras/video_understanding/video_interval/grid_size`，生成 note 时复用与 `/notes/from_transcript` 一致的截图后处理。
  - Bililive-go：自动 `knowledge_sync` 默认值或运行配置必须显式传 `format: [toc, link, screenshot, summary]`、`link: true`、`screenshot: true`，并用回归测试覆盖 same-live aggregation payload。
  - 验收：真实自动任务完成后，BiliNote markdown 中应出现 `/static/screenshots/...` 或 `![视频截图 ...]`；若用户只在 UI 勾选而未配置自动同步，必须在设置页明确提示两者不是同一配置域。

## 2026-06-13 原片截图缺失修复 checkpoint

- 执行方式：按用户要求调用两个子代理并由主会话审核。
  - BiliNote 子任务：扩展 `/api/knowledge/ingest` 机器接口，支持 `format/link/screenshot/style/extras/video_understanding/video_interval/grid_size` 并调度 `NoteGenerator.generate_from_transcript`。
  - Bililive-go 子任务：`SubtitleKnowledgeSyncConfig` 增加默认 note 格式 `[toc, link, screenshot, summary]`，默认 `link/screenshot=true`，所有单段、same-live session、aggregate payload 均通过 `ResolveNoteOptions()` 发送。
- 主会话审核追加修正：
  - BiliNote 机器 ingest 显式 `link=false` / `screenshot=false` 现在会从 format 中移除对应项，避免显式关闭被默认格式覆盖。
  - BiliNote `generate_from_transcript` 在 `screenshot` 或 `video_understanding` 开启但请求缺少采样参数时，默认使用 `video_interval=4`、`grid_size=[3,3]`，避免自动链路只传截图开关却没有可用帧，导致 LLM 不产出 Screenshot 标记且 fallback 无帧可插。
  - Bililive-go `config.yml` 样例和 `config_comments.go` 注释模板同步说明 `link/screenshot` 留空时使用 Bililive-go 默认值，避免后续 YAML round-trip 恢复旧误导注释。
- 本地验证：
  - `PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/Volumes/ISCSI-Disk/Folder/BiliNote/backend python3 -m pytest /Volumes/ISCSI-Disk/Folder/BiliNote/backend/tests/test_knowledge_ingest_note_options.py /Volumes/ISCSI-Disk/Folder/BiliNote/backend/tests/test_notes_from_transcript.py -q -p no:cacheprovider` -> `6 passed`。
  - `PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/Volumes/ISCSI-Disk/Folder/BiliNote/backend python3 -m pytest /Volumes/ISCSI-Disk/Folder/BiliNote/backend/tests/test_note_router_regressions.py /Volumes/ISCSI-Disk/Folder/BiliNote/backend/tests/test_prompt_and_note_regressions.py -q -p no:cacheprovider` -> `28 passed`。
  - `PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/Volumes/ISCSI-Disk/Folder/BiliNote/backend python3 -m pytest /Volumes/ISCSI-Disk/Folder/BiliNote/backend/tests/test_knowledge_memory.py -q -p no:cacheprovider` -> `19 passed`。
  - `GOCACHE=/tmp/bililive-go-gocache go test -count=1 ./src/configs ./src/pipeline/stages` -> passed after rerun outside sandbox because `httptest` needs loopback bind.
  - `GOCACHE=/tmp/bililive-go-gocache make dev` -> built `bin/bililive-darwin-arm64` successfully.
  - `git diff --check` clean for both Bililive-go worktree and BiliNote scoped changes.
- Remaining rollout gate:
  - 当前只是本地代码和 unit/build 验证；还未提交 PR、未发布镜像、未在 NAS `latest` 上真实自动任务验收。
  - 上线后必须用本地 Chrome/Computer Use 验证 BiliNote 最近自动文档出现 `![视频截图 ...]`，并用 NAS `/api/info` 确认 Bililive-go 运行 hash。

## 2026-06-13 原片截图缺失分支拆分

- 复查旧分支状态：
  - Bililive-go `codex/same-live-media-library-aggregation` 对应 PR #34 已 merged，当前未提交截图默认值补丁不能继续追加到已合并分支。
  - BiliNote `codex/fix-long-transcript-chunking` 对应 PR #35 已 merged，且原工作树混有前端/transcriber 等无关脏改动，不能作为本次截图修复提交面。
- 新分支策略：
  - Bililive-go 从最新 `origin/master=806da08c5cc299d593910a83221c6c6e640532d1` 切出 `codex/restore-note-screenshots`，承载自动同步 `format/link/screenshot` 默认值与 payload 测试。
  - BiliNote 从最新 `origin/master=4237108c745996c3a016a1a3dcd4ca19cb838e23` 新建干净 worktree `worktrees/bilinote-machine-ingest-screenshots` / branch `codex/machine-ingest-screenshots`，只承载 `generate_from_transcript` 缺省视频采样兜底与回归测试。
- 新增验证：
  - BiliNote 干净 worktree：`PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=backend python3 -m pytest backend/tests/test_prompt_and_note_regressions.py backend/tests/test_knowledge_memory.py backend/tests/test_note_router_regressions.py -q -p no:cacheprovider` -> `89 passed`。
  - Bililive-go 新分支：`GOCACHE=/tmp/bililive-go-gocache go test -count=1 ./src/configs ./src/pipeline/stages` -> sandbox 因 `httptest` loopback bind 被拒；非沙盒重跑通过。
  - 两个工作树 `git diff --check` 均 clean。

## 2026-06-13 原片截图缺失发布 checkpoint

- Bililive-go 修复已进入 `master`：
  - PR：Neymar022/bililive-go#35。
  - Merge commit：`96c7db59c28d0ff6cf5bdb4db8da9c17dd81a85a`。
  - GitHub Actions：master `Publish Docker Images On Master` run `27464561456` 成功，已发布 `neymar022/bililive-go-app:latest` 与 `neymar022/bililive-go-subtitle-worker:latest`。
- BiliNote 修复已进入 `master`：
  - PR：Neymar022/bilinote#48。
  - Merge commit：`8f4a353c7d94183a8141f336d2112085c0935be3`。
  - GitHub Actions：master `Build & Push Docker images` run `27464561410` 成功，frontend 与 backend Docker Hub `latest` 均已推送。
- NAS 运行态尚未更新到上述镜像：
  - `GET http://192.168.1.80:18090/api/info` 返回 `git_hash=806da08c5cc299d593910a83221c6c6e640532d1`，仍是本次修复前的 Bililive-go 版本。
  - `GET http://192.168.1.80:3015/api/sys_health` 返回 `200`，但 `/api/knowledge/runtime/machine` 需要 bearer token；未在未授权情况下读取 BiliNote runtime SHA。
- 下一步验收门禁：
  1. 先在 NAS Docker 项目中拉取并重建 Bililive-go 与 BiliNote latest。
  2. 通过 `/api/info` 确认 Bililive-go 运行 hash 为 `96c7db59c28d0ff6cf5bdb4db8da9c17dd81a85a` 或对应短 hash。
  3. 通过 BiliNote runtime/build 证据确认 backend 包含 `8f4a353c7d94183a8141f336d2112085c0935be3`。
  4. 使用用户本地 Chrome/Computer Use 验证最近自动生成文档包含 `![视频截图 ...]` / `/static/screenshots/...`，不要使用沙盒浏览器替代。

## 2026-06-13 NAS latest 验收 checkpoint

- 运行态版本已确认：
  - Bililive-go `GET /api/info` 返回 `app_version=96c7db5`、`git_hash=96c7db59c28d0ff6cf5bdb4db8da9c17dd81a85a`，已是 PR #35/latest 截图默认值修复版本。
  - BiliNote backend 容器环境返回 `BILINOTE_BUILD_SHA=8f4a353c7d94183a8141f336d2112085c0935be3`，已是 PR #48/latest 机器 ingest 截图修复版本。
  - `GET http://192.168.1.80:3015/api/sys_health` 返回 success。
- 部署偏差与修复：
  - BiliNote backend 虽已是 latest，但原 compose 没有把 `/volume2/docker/bililive-go/video` 挂入容器，机器 ingest 收到 `source_video_path` 后无法读取原视频，因此无法生成封面与原片截图。
  - 已更新 NAS `/volume2/docker/dockge/stacks/bilinote/compose.yaml`，为 backend 增加 `/volume2/docker/bililive-go/video:/volume2/docker/bililive-go/video:ro`，并保持 backend/frontend 使用 Docker Hub `latest`。
  - 已重建 BiliNote backend/nginx；`docker inspect bilinote-backend` 确认媒体路径已只读挂载，容器内可 `find /volume2/docker/bililive-go/video`。
- 历史媒体库补偿：
  - 已执行 `/volume2/docker/bililive-go/reports/codex-repair-library-sidecars.py --apply`。
  - 复核 dry-run：`show_repairs=0 episode_repairs=0 cover_repairs=0 cover_failures=0 duplicate_show_identities=0 moved_episodes=0 moved_files=0 quarantined_files=0`。
- 历史 BiliNote 文档封面补偿：
  - 已在 BiliNote backend 容器内执行 `/tmp/codex-backfill-bilinote-covers.py --apply`，回填 18 条 `note_records.audio_meta.cover_url`，不重跑总结模型、不修改媒体库文件。
  - 复核示例：`bililive-go-673`、`bililive-go-678` 均已有 `/static/cover/...jpg`。
  - 仍有 6 条更早 `bililive-go-*` 记录无 cover_url，原因待单独确认；当前 dry-run 没有可用 `.jpg` 候选，可能是历史源路径缺失或不指向媒体库成品。
- 真实链路验收进度：
  - 已尝试重跑历史记录 `汤山老王.S01E0015.2026-06-12...`，生成任务 `682`。
  - `682` 失败原因为该历史记录的 MP4 已被存储清理：`video file does not exist`，不是封面/截图链路失败。
  - 下一步需要改选当前实际存在的 completed MP4，通过 `/api/subtitles/records/{relative_path}/rerun` 重新验收，并检查新 BiliNote 记录的 `cover_url`、markdown screenshot markers、`/app/static/screenshots` 文件。
- 当前阻塞：
  - NAS SSH 继续检查被工具审批额度阻断，系统提示 21:56 后重试；不能绕过审批限制。
  - 额度恢复后继续执行：选取实际存在 MP4 -> 重跑 -> 查询 BiliNote DB -> 使用本地 Chrome/Computer Use 验收 UI。

## 2026-06-13 NAS latest 真实链路二次根因

- 运行态版本复核：
  - Bililive-go NAS `/api/info` 已是 `app_version=96c7db5` / `git_hash=96c7db59c28d0ff6cf5bdb4db8da9c17dd81a85a`。
  - BiliNote backend 容器环境已是 `BILINOTE_BUILD_SHA=8f4a353c7d94183a8141f336d2112085c0935be3`。
- 真实链路复现：
  - 改选实际存在的 completed 媒体库 MP4 `小司说钢构.S01E0004.2026-06-11 - 钢结构该怎么选择002.mp4` 触发 `/api/subtitles/records/{path}/rerun`，生成任务 `683` 并完成。
  - 任务 `683` 的 BiliNote payload 已包含 `format=["toc","link","screenshot","summary"]`、`screenshot=true`、`video_understanding=true`、`video_interval=4`、`grid_size=[3,3]`，说明 PR #35/#48 的截图参数链路已生效。
  - 但 BiliNote 新记录 `bililive-go-683` 仍无 `cover_url` 且 markdown 无 `/static/screenshots/`，同时 NAS 文件侧该媒体库 MP4 在字幕完成后消失，只剩 `.ass/.srt/.jpg/.nfo/.subtitle.json`。
- 根因：
  - `subtitle_generate.deleteSourceAfterCompletion` 在知识同步前调用 `subtitle.DeleteSourceFile(libraryPath, sourceRoot)`。
  - 当 rerun 输入已经是媒体库成品 MP4 时，`Metadata.SourcePath=file.Path` 被写成同一个媒体库 MP4；`ResolveSourcePath` 又优先返回 sidecar 中仍存在的 `SourcePath`，于是 `DeleteSourceFile` 删除了最终媒体库成品。
  - 这解释了“截图参数已生效但 BiliNote 无截图”：BiliNote 后台收到的是正确 `source_video_path`，但异步生成截图时视频文件已经被 Bililive-go 删除。
  - 同一中心 helper 还被 retention cleanup 和手动 delete-source API 复用，因此必须在 `DeleteSourceFile` 中央加防线，不能只在 subtitle stage 局部修。
- 本地修复：
  - `DeleteSourceFile` 新增 `ErrSourceNotDeletable` 与中心校验：只允许删除 `sourceRoot` 内、且不是 `videoPath` 自身的路径；媒体库成品或源目录外路径一律拒绝删除。
  - `CleanupExpiredSources` 遇到 `ErrSourceNotDeletable` 跳过污染记录并继续，不中断后台清理。
  - `subtitle_generate` 遇到 `ErrSourceNotDeletable` 记录“保留媒体库成品”并继续 pipeline，不再写成普通删除失败。
- 新增回归：
  - `TestDeleteSourceFileRefusesLibraryVideoPath`
  - `TestCleanupExpiredSourcesSkipsLibraryVideoSourcePath`
  - `TestSubtitleGenerateDoesNotDeleteLibraryVideoOnRerun`
- 本地验证：
  - `GOCACHE=/tmp/bililive-go-gocache go test -count=1 ./src/subtitle ./src/pipeline/stages` -> passed。
  - `GOCACHE=/tmp/bililive-go-gocache go test -count=1 ./src/...` -> passed（仅 `github.com/shoenig/go-m1cpu` 依赖在 macOS 上输出 clang VLA warning）。
- 剩余 rollout：
  - 需要提交/PR/CI/合并并发布新 `latest` 后，NAS 再拉取验证。
  - 当前 NAS latest 仍是会删除媒体库 MP4 的版本，不能继续用它跑自动截图验收，否则会制造新的历史缺失记录。
  - 已被任务 `683` 删除的 `小司说钢构.S01E0004...mp4` 需要后续按是否有源文件/备份单独恢复；没有 MP4 时无法补回截图，只能用已有 `.jpg` 回填封面。

## 2026-08-18 live-session 旧路径兼容续修

- Goal 保持 active：修复历史四位集号路径在 live-session manifest/sidecar 中残留导致的聚合失败，安全恢复 task `1183/1184`，验证 task `1203` 自然执行，再定向统一已确认的 5 个历史 NFO/UGREEN 展示日期；完成源码、生产、发布部署和清理闭环后才结束。
- 授权与边界：写前必须重新满足轻量门禁并备份；不删除 MP4、不覆盖冲突目标。`p6-monitor-http` 及其唯一引用的 local worker 镜像在 owner/用途未确认前不得删除；`chronological-renumber-20260815-082317` 约 735GB 恢复备份继续保留。
- 新鲜 RED：task `1183/1184` 均在同场聚合阶段引用已不存在的 `伊布讲AI.S01E0024.2026-08-14 - Seedance2.5 实战答疑现场.mp4`，最终报 `live session segment video missing`。源码 seam 已用 `publishLiveSessionMediaAggregate` 回归复现：metadata 与 manifest 同时残留旧四位路径、同目录只存在唯一 recordedAt 长 identity 成品时，当前 master 直接失败。
- 当前源码 fixed point：隔离 worktree 基于 `origin/master=e5bafd69837f3be324afbe62c717b932fa568376`。最小修复只对旧四位 identity 启用同目录、同主播/季/日期/标题/扩展名，且落在 `record_meta.start_time` 对应 `base..base+7` 的唯一长 identity 映射；显式隐藏路径与仍存在的 output/library path 优先，多候选、无候选、缺可信时间或唯一但 recordedAt 不匹配均继续 fail closed。上述 RED 已转 GREEN。
- 第一批源码已由 PR #42 合并为 `master=31435609cb8ef32d000664847aea15b23c7374f7`。CI 的 build、test、lint、AI 指令检查和 Playwright E2E 均通过；`claude-review` 仅因仓库未安装 Claude Code GitHub App 返回 401，属于外部配置失败，不是代码失败。
- 生产 fixed point 的进一步只读复现发现：task `734` manifest 中 `1174..1182` 仍保留旧四位 library/metadata path；既有 aggregate metadata 已保存它们位于媒体库外的真实 hidden output path，且对应 MP4 全部存在，但各旧 segment sidecar 缺少 `live_session_segment_hidden_path`。仅靠同目录 recordedAt 长 identity 映射仍会正确拒绝不属于该 start_time 的唯一可见候选，因此不能恢复这批真实输入。第二个 RED 已在同一 `publishLiveSessionMediaAggregate` seam 固化：aggregate metadata content hash 因新增 `1183/1184` 失效时，也必须先从既有 aggregate 的 metadata/library path 到 hidden output 映射恢复旧分段；恢复目标必须存在并解析在媒体库外的专用 hidden root 内，其他路径忽略。RED 已转 GREEN，仍不删除或覆盖媒体。
- 第二批修复已通过正式双轴 review，Standards/Spec 均为 `0 findings`：identity 改变时先从 `PreviousAggregatePath` 加载旧 aggregate metadata；恢复映射必须同时匹配 metadata/library 双字段并保持 key/output 一对一；当前 sidecar 与 aggregate output 都必须是 regular file、位于当前 session 专用 hidden family，跨 session、缺失、重复、冲突及文件/父目录 symlink 逃逸均 fail closed。事务中精确等于 `library_path` 的旧可见路径仍可按既有流程重新隐藏。定向 RED/GREEN、`pipeline/stages` 全包、`make dev`、`make lint`、`make test` 和 `git diff --check` 已通过；本机 `make build-web dev` 仅因系统 Node `23.11.0` 不满足依赖声明而停止，未产生持久 diff，前端未修改，完整门禁交给 GitHub CI 的受支持 Node 环境复验。
- 当前生产门禁为录制 `1`、pipeline running/pending `0/4`、update `idle`，故尚未执行 NAS/DB/媒体写入或部署。task `1203..1206` 均为 `2026-08-19 02:00 +08` 计划任务；继续完成第二批源码审查与发布，不以 pending 阻断只读/源码主链。写入与重试仍须等录制/媒体写入清空并完成备份。

## 2026-08-14 直播合集时间排序 checkpoint

- 状态：源码防复发与历史只读计划已完成，本地验证为绿；生产历史重编号与部署尚未执行。
- 根因：旧媒体发布按第一个空闲 `S01E` 分配，任务完成顺序会覆盖真实录制顺序；UGREEN 主要按 season/episode 展示，因此晚到任务造成 UI 逆序。
- 普通发布以精确 `recordedAt` 生成稳定 `int64` episode identity，优先 `record_meta.start_time/RecordInfo.StartTime`，可靠回退为固定 `UTC+8` 的规范文件名时间；不使用 mtime 或路径字典序。
- 同场聚合按最早真实录制时间排序并继承 `start_time/dateadded`；晚到更早分段会迁移 aggregate identity，旧 aggregate 及 sidecar 归档到媒体库外。manifest 的 `previous_aggregate_path` 保存中断恢复点，失败或进程重试都不会遗失旧聚合身份。
- 历史工具的 `--plan-chronological-renumber` 仍是只读模式，现在输出完整文件双射、NFO 字段变化、媒体库/manifest/库外隐藏 sidecar 的 JSON Pointer old/new 引用，并实际模拟替换后验证 old refs=0、new refs 守恒及 MP4 count/bytes 不变；缺 NFO 时拒绝生成成功计划。
- 2026-08-15 当前生产最终 dry-run 已以 root 只读运行并通过：`episodes/unique_sources/unique_targets=399/399/399`、`files=2346`、`json=1319`、`references/json_edits=2100/2100`、`post_old_refs=0`、`media_before=media_after=400/787776233209`、`conflicts=0`、`monotonic=true`；压缩报告 SHA-256 为 `c55b83f80ffec0e06cdd5195577a37c37478f62d8827080b2aeeab9a4e59a8bb`。首次非 root 试跑把 root-only sidecar 的 `PermissionError` 收敛成“缺可信时间”，不作为数据 RED；root 权威运行证明 Geek徐Sir E0062 的 sidecar 时间可读且计划完整。
- 同期轻量门禁为 active recordings `0`、pipeline running/pending `0/2`、update `idle`、短时媒体写入 `0`。两项 pending 是 `not_before=2026-08-16 02:00 +08:00` 的字幕任务，输入都在 `srt_video`，与当前 399 集重编号集合及 2100 条引用交集为 `0`；但旧生产版本在其完成后仍可能发布新四位集号，所以最终 apply fixed point 必须等待其自然完成后重新生成，不能复用当前快照直接写入。
- 本地验证：`go test ./src/subtitle ./src/pipeline/stages -count=1`、Python 14 tests、Linux `386` compile-only、`make dev`、`make test`、`make lint`、`git diff --check` 均通过。
- 下一门：实时满足录制 0、pipeline 0/0、update idle 后，先备份媒体/sidecar/manifest 和 UGREEN 三表，再审核最终双射计划；历史原子重编号及生产镜像部署需要在该门后执行，并验证 UGREEN 对长 episode number 的实际解析。任何目标冲突 fail closed，禁止删除 MP4。
- 持续授权：本次精确录制时间排序范围内，可在每次生产写入前重新通过轻量门禁后，连续完成历史重编号、必要 DB/引用修复、源码提交与中文 PR/CI/合并、镜像发布及最小生产部署；不得删除 MP4、覆盖冲突目标或扩大到无关清理，任何非双射、引用断裂或媒体不守恒均 fail closed 并回滚。
- Goal 完成门禁：阶段性 dry-run、fixed point、计划任务等待、PR 或部署都不是完成；只有生产历史修复与后验、源码合并发布、必要 NAS 部署和最终验收全部完成，并将 closure 写回本状态后才可结束。

## 2026-08-19 live-session 旧路径与展示日期 closure

- 源码与发布已闭环：第二批最小修复由中文 PR #43 合并为 `master=62ebec4ed62c3cf0519228d27441f9f1aec4a623`；CI 的 build、test、lint、AI 指令检查与 Playwright E2E 均通过，Docker workflow `32128093746` 成功发布。生产仅更新 `bililive`，`GET /api/info` 返回 HTTP `200` 和同一 git hash；活动 UGREEN Compose 仍使用 `neymar022/bililive-go-app:latest` / `neymar022/bililive-go-subtitle-worker:latest`，checksum 为 `3aa98320570ee6a7d3ca2c98cbcb00833d7afed635942bf3bc5ddcf255a015a5`。
- 历史恢复已闭环：task `1183/1184` 分别于 `2026-08-18 19:37:24` / `19:39:03 +08` 完成。session `734` manifest 为 11 个 sources，aggregate metadata 同样有 11 个唯一 hidden output，11/11 均存在且位于媒体库外；manifest content hash 与 metadata hash 一致，`previous_aggregate_path` 为空。旧 manifest 的四位 `library_path/metadata_path` 继续作为兼容 key 保留，由受限 hidden mapping 恢复，不改写或猜测历史路径。
- 自然增量验证已闭环：task `1203` 于 `2026-08-19 02:44:48 +08` 完成；生成 episode `1673386692282296` 精确等于 `record_meta.start_time=2026-08-18T07:42:16.535287952+08:00` 的 recordedAt identity。MP4/NFO/JPG/SRT/ASS/subtitle JSON 齐全，UGREEN file/category/episode 关系一致，`陶-琛霸` 10 集按 episode/dateadded 无相邻倒退。随后 `1208..1212` 也自然完成，写入固定点重新达到 active `0`、pipeline `0/0`、update `idle`、ffmpeg `0`。
- 5 个已确认展示日期已按 recordedAt 定向校正：episode ID `15162/15167/15332/15337/15443` 的 NFO `<title>` 日期前缀与 `ug_television_episode.name` 分别改为 `2026-07-29/2026-08-03/2026-08-06/2026-08-12/2026-07-04`。NFO 使用 XML parser、同目录临时文件、fsync 与 `os.replace`；UGREEN watcher 观察 90 秒未更新名称后，使用精确旧值条件且强制影响 5 行的单事务更新。文件名、房间标题后缀、season/episode identity、MP4 均未改变。
- 日期修复回滚根为 `/volume2/docker/bililive-go/backups/recorded-at-date-correction-20260819-044422/`，包含 5 个原 NFO、`ug_television_episode` custom dump/list、前后 5 行、门禁、媒体/manifest/runtime/清理后验和完整 SHA-256 清单。回滚优先从 `files/video/...` 原子恢复 5 个 NFO，并按 `rows-before.psv` 的 ID 与当前值条件反向更新 5 行；不要无条件整表覆盖。
- 最终后验为绿：NFO/DB 新值 `5/5`、旧名称 `0`、inline filesystem MP4 `0`、UGREEN inline `file_info` `0`、建筑师 inline movie relation `0`、1374 个 JSON 解析错误 `0`。日期 apply 前后媒体严格守恒：可见根 `423 / 825377880762 bytes`，外置 hidden 根 `273 / 150985881365 bytes`；session `734/752` 的当前或兼容恢复引用均可解析到现存文件。
- 最终运行态：app HTTP `200`、SHA `62ebec4...`、restart `0`；worker OpenAPI HTTP `200`、运行 revision `e5bafd...`，夜间批次期间累计 restart count 为 `1`，当前 running。没有因日期修复重启容器或 UGREEN video 服务。
- 清理按保守边界完成：仅删除无进程占用、未被 Compose/容器引用的 `/tmp/bililive-app-62ebec4.tar`。`p6-monitor-http` 虽无 Compose/owner labels 且 bind source 已不存在，但停止容器仍唯一引用 `local/bililive-go-subtitle-worker:p6-mac-mlx-20260507`，因此容器和镜像保留；其它历史 local/sha/rollback 镜像 provenance 不足，也未删除。`chronological-renumber-20260815-082317` 约 735GB 恢复备份继续保留。
- 当前 Goal 完成：源码 TDD/focused/正式双轴 review、PR/CI/合并与必要 app 部署、1183/1184 安全恢复、1203 自然增量验证、5 行日期校正、生产后验、保守清理及 plan-tree closure 均已完成；没有删除任何 MP4。

## 2026-08-19 recordedAt 展示标题修复（生产已部署）

- 根因已固定：episode NFO `<title>` 与 UGREEN `ug_television_episode.name` 均为干净的 `YYYY-MM-DD - 房间标题`；长 recordedAt identity 只保留在底层文件名、NFO episode 与 DB episode number。UGREEN 对超长 episode 回退 basename 的 recent/card/serial 三处消费 seam 才会把 identity 暴露给用户。
- bililive-go 源码发布已完成：PR #45 的统一 Go/Web display-title 契约由 PR #46 补齐聚合 NFO `sorttitle`、批量烧录确认和 UGREEN serial 空副标签，并严格只清理 10 位以上 recordedAt identity。PR #46 合并为 `master=44e87a42590f54c4892d79a8386f5cd535b15390`；build/test/lint/E2E 通过，`claude-review` 仅因仓库未安装 GitHub App 返回 401；Docker workflow `32216325861` 成功发布 `sha-44e87a4` 和 `latest`。
- BiliNote 源码发布已完成：后端 API 增 `display_title` 而保留 raw title/path/source identity，前端覆盖历史、详情、知识来源/筛选/重建、设置预览和 task polling；新 Markdown 导出清理 heading/标题字段/链接可见标签，但保留完整路径、inline/reference target 和代码内容，不迁移历史 SQLite/raw title/旧导出。前后端共用 display contract fixture；相关 backend `89` 项、frontend `158` 项、typecheck/lint/build、graphify 和最终双轴 review 均通过。PR #59 合并为 `master=cac9e18ff248885de32263e353038f1d7a3ee5e2`；镜像 workflow `32216325041` 于 `2026-08-19 12:59 +08` 成功完成 backend/frontend 多架构发布。
- 用户于 `2026-08-19 15:12 +08` 明确授权立即部署并保留两项计划任务。写前新鲜门禁为 active `0`、running `0`、update `idle`、最近媒体写入 `0`、ffmpeg `0`；pending `1213/1214` 的 ID、stage、`not_before` 与 `srt_video` 输入在部署前后完全一致，没有强跑、取消或改写。旧等待 driver 仅在 `sleep 300` 子进程中等待，终止该本任务进程及其 sleep 后确认 lock 释放、完成 marker/部署备份均不存在，再启动唯一授权 driver。
- 生产部署于 `15:16:45 +08` 完成，回滚根为 `/volume2/docker/bililive-go/backups/recorded-at-display-v2-20260819-151449/`。备份包含活动 Bililive/BiliNote Compose、UGREEN 原始 JS/GZ、容器与镜像 inspect、媒体清单、pending 前后快照、patcher 和 before/after 校验。UGREEN 两个资产均原子补丁并复核为 `already-patched`；补丁后 SHA-256 分别为 `211d8a5a...31416` 和 `430ec41e...b620`。只重建 `bililive`、BiliNote backend/frontend；subtitle-worker 与 BiliNote nginx 的 container ID、image 和 StartedAt 全部保持不变。
- 最终运行态为 Bililive `/api/info.git_hash=44e87a42590f54c4892d79a8386f5cd535b15390`、BiliNote backend/frontend OCI revision `cac9e18ff248885de32263e353038f1d7a3ee5e2`；Bililive root、BiliNote health/root 和 worker `:8091/openapi.json` 均 HTTP `200`，所有目标容器 running，app/backend/frontend/nginx restart count 为 `0`。活动 Compose 仍引用 Docker Hub `latest` 且内容与备份一致；运行 app digest 为 `sha256:272f6609...2fa4`。
- 展示与身份契约后验为绿：Bililive `/api/subtitles/records` 共 `422` 条，`display_title` 长 identity 为 `0`；UGREEN DB 的 `394` 条长 episode number 全部保留，同时 `394` 条 episode name 均为日期标题、含长 identity 的 name 为 `0`。汤山老王样本证明 DB name 为 `2026-05-12 - ...`，底层 file name 仍保留 `S01E1605432872139912`。可见 episode NFO clean title `777`、长 identity title `0`；inline filesystem/UGREEN file_info/建筑师电影关系仍为 `0/0/0`。
- 媒体与恢复后验为绿：部署前后 MP4 均为 `696` 个、`976,363,762,127` bytes，清单完全一致；没有删除或重命名 MP4。`chronological-renumber-20260815-082317` 约 `735G` 恢复备份继续保留。生产 bundle/API/DB/NFO seam 已证明日期可见且长 identity 不作为 display label；当前仅剩用户端已打开页面可能持有旧缓存，需要重新进入影视中心后做一次视觉确认，不构成生产数据或部署回滚条件。

## 2026-08-19 UGREEN 选集交互与封面复核（执行中）

- 用户真实页面把两个问题固定到不同 seam：序号视图因上一版把超长 episode 标签改成空串而显示成不可判断的灰块；卡片视图的 `HorizontalList` 子组件已发出 `select`，但剧集页父组件未绑定 `handleChangeEpisode`，且通用居中滚动会让首卡停在左侧裁切位置。目标是序号显示 `MM-DD` 且 tooltip 保留完整日期标题、卡片整卡可点击、首卡完整可见，同时继续保留底层 MP4/NFO/DB/JSON/manifest 的长 recordedAt identity。
- 封面不是媒体缺失：`建筑师 linkai` 的 UGREEN `getTV` 53 集均有同 stem `cover_path`，另有合集 `poster.jpg`；54/54 Bililive 资产和使用当前 UGREEN credential 的 `getImaStream` 均返回可解码 JPEG。页面缓存中的 15 个灰封面使用过期图片授权并返回 `path ... is illegal`，用当前 credential 重放同一路径 15/15 成功。因此本轮不重写 NFO/JPG、不重新抽帧，也不运行历史 sidecar repair。
- 源码 fixed point 为 `origin/master=5cf730d2b6257c9128d4b411adf34cf9bce6f93a`，隔离分支提交 `2223702650e752d5e0b2dbacb5399e5f24b28378`。patcher 只接受 vendor、已部署 title-v1 或 final 三种完整 bundle 状态，未知混合态 fail closed；对 JS/GZ 先全量预检、备份后原子写入，第二资产失败会回滚全部已尝试资产。
- 当前验证：Python 回归 `8/8`、真实生产 bundle dry-run、补丁后 `node --check`、`make dev`、使用 Node `24.19.0` 的 `make build-web dev`、`make lint`、`make test` 和 `git diff --check` 均通过。正式 Spec 初审发现同一 seam 的 vendor/final 已知片段并存时原识别仍可能误接受；新增 RED 后已要求每个 seam 的所有已知 variant 总计严格为 1，回归转绿。`make test` 首轮唯一 subtitle 重试计数时序失败经同一测试 `10/10` 定向通过后，整套复跑通过，未改动 subtitle 源码。
- 正式 Standards/Spec 双轴复审均为 `0 findings`。剩余 rollout：中文 PR/CI/合并；生产写前重新确认录制/流水线/update/媒体写入门禁，备份 UGREEN JS/GZ 后只应用前端 vendor patch。最终必须在真实影视中心证明序号日期可见、整卡点击生效、首卡不裁切且 53 集加 poster 封面可见；不得删除或重命名 MP4，也不得清理 `chronological-renumber-20260815-082317` 恢复备份。
