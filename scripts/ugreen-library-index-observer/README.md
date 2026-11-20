# UGREEN 本机只读索引观察器

部署时在 root 管理的 `/etc/bililive-index-observer.conf` 指定唯一库根，例如 `BILILIVE_INDEX_LIBRARY_ROOT=/path/to/media`。库根必须是规范化的绝对路径，且与应用的媒体库根及固定的库 ID 4 一致。采集器通过 psql 字符串参数绑定库根，查询范围仍包含该根下的所有文件映射，以保留跨库冲突检查。

宿主同一 oneshot 先运行固定 root helper，再由 `postgres` 用户执行固定 SELECT；既有 timer 在每次成功结束后等待 30 秒再次运行。仅查询现有 Unix socket `/var/run/postgresql:5433` 的 `video` 数据库，不新增 timer、daemon 或 API。应用不获得 PG 口令、超级用户、SSH、Docker socket 或任意 SQL 执行入口。

## 权限与可信安装

`ExecStartPre=+/usr/bin/python3 -B /usr/local/lib/bililive-index-observer/platform_probe.py` 使用 [systemd 252 的 `+` 前缀](https://www.freedesktop.org/software/systemd/man/252/systemd.service.html)。该命令具有完整 root 权限，忽略 `User/Group/CapabilityBoundingSet` 及文件系统 namespace 限制；不能称为最小 capability 隔离。后续 `ExecStart` 仍是 postgres，保留空 capability、NoNewPrivileges、AF_UNIX、ProtectSystem 等原限制。NAS 的实际 systemd/权限行为须由安装者实测，不能以单元测试替代。

安装者须将 `/usr/local/lib/bililive-index-observer` 及完整父链保持 root 持有、服务不可写，安装 `platform_probe.py`、`observer.py`、`query.sql` 为 root:root 0644；unit/timer 同样 root 持有且不可由服务改写。Python 模块只从可信安装目录加载，不设置用户可控 `PYTHONPATH` 或 `EnvironmentFile`。本目录不提供 root 命令执行接口；helper 拒绝所有额外参数，不读取环境路径，不执行 subprocess、网络、凭据读取或 PG 操作。

`postgres` 数据库角色本身不是受限只读角色；限制来自固定只读事务、固定 SELECT、可信安装和服务沙箱，不能称为新增只读 PG 角色。root helper 不执行该查询，也不把 root 权限传给 collector。

## 运行映像取证

helper 仅寻找唯一 `comm=video_serv`、arg0 严格等于 `/var/targets/video_serv` 的实例。逐字节只读 cmdline 第一个 NUL 前的 arg0，不读取或记录其它参数。固定 BUILD 为 `/volume1/@appstore/com.ugreen.videomgr/sbin/video_serv`；完整启动软链/真实目录链须 root 持有、服务不可写，且 realpath 固定。root 组可写的父目录仅在 collector 不属于该组时允许；软链自身 0777 不当作可写目录。

helper 打开该实例实际 `/proc/PID/exe` fd 与固定 BUILD fd，必须同 dev/ino/完整 stat，哈希仅从实际 exe fd 读取，限 512 MiB。读取前后核完整 stat、解析链、PID/uid/start ticks/启动时间，并再次打开 exe 检查映像稳定。多实例、权限失败、文件缺失、超界或任何变化均生成新鲜 `platform_unconfirmed`；发布失败则 helper 非零退出，collector 不执行，不能沿用旧 confirmed 声称成功。

固定证据 `/run/bililive-platform-observer/platform.json` 使用 schema1；包含固定路径、PID/uid/start、UTC 时间、实际 exe SHA256 及 exe/build stat。helper 安全建立 root:root 0755 目录（兼容 unit 的 UMask=0077），拒绝软链、可写父链和危险已有文件，使用目录 fd、lstat/fstat 身份比较、原子替换和文件/目录 fsync 发布 root-owned 0644 文件。postgres 只能读，不能修改文件或目录内容。无秘密数据。安装者也可预建该 root 目录，不能使用把目录交给 postgres 的 RuntimeDirectory 设置。

collector 只消费完整 schema、root owner/0644、可信父链、60 秒内且不超前超过 5 秒的证明。PG 查询前后各自核当前唯一 PID/uid/start/启动时间与固定 BUILD 的 stat、解析路径及打开 fd 身份，必须与证明一致；两次结果还须同一实例和 SHA。期间重启、替换、证据缺失/过期/不可信均发布禁写状态。同 build 正常重启在下一轮 root 重新取证后自动恢复，目录整体升级或回滚而未重启不能把磁盘 SHA 误认为运行 SHA。删除原先根据 ctime 与启动时间猜测安装历史的逻辑；ctime 仅用于单次 stat 稳定比较，不以共享父目录变化长期禁写。

这些检查不声称抵御恶意 root、任意时钟篡改或不合作写者在最终检查之后的纳秒级篡改；无人值守写入仍依赖已验证的唯一 NFO writer 合同。

## 快照与应用能力

输出目录 `/var/lib/bililive-index-observer` 仍归 postgres 持有；`snapshot.json` 通过原子替换和文件/目录 fsync 发布。只读挂载整个目录到应用 `/run/bililive-index`，设置 `BILILIVE_LIBRARY_INDEX_SNAPSHOT=/run/bililive-index/snapshot.json`，不能只挂载单个旧 inode。快照仍为 `schema_version=2`、`adapter_version=ugreen-fixed-select-v2`；SELECT 范围和 READ ONLY 事务未变。确认时平台公开字段仍为 `pid/uid/start_ticks/started_at/observed_at/startup_path/build_path/build_sha256`，未修改 Go 平台结构或 PG SQL。

自动恢复须在同挂载、实际 Go uid/gid 验证属性/rename/UGREEN 消费及唯一 writer 能力。能力 JSON 仍为 schema2，绑定库身份、uid/gid、属性、安装时间、证据 SHA 及 `platform_build_path/platform_build_sha256`。PID 不进能力签名，同构建正常重启无需重签；实际构建 SHA 改变仍由 Go capability gate 拒绝并要求重新验证能力。新鲜 root 证明不会签发能力，也不证明 UGREEN 缓存设置生效。

Go 同时验证 90 秒快照新鲜度、关闭 NFO 写回、完整 Season 唯一映射、原保护主键、媒体大小及 NFO 集号。健康 Season 的旧 failed 补偿每次放行前重新取新鲜 baseline/platform，并与原 protected 映射比较，禁止覆盖成新 baseline；未进入恢复的普通发布保持原行为。缺少/过期/不匹配则等待或禁写，不自动修改 PG。发布和索引验证不替代真实客户端播放。

## 隔离验证

本机单元测试：`python3 -B -m unittest discover -s scripts/ugreen-library-index-observer -p 'test_*.py' -v`。真实进程方法在普通宿主显式 skip；只在自有 Linux 容器设置 `BILILIVE_PLATFORM_PROCESS_FIXTURE=1`，以 tmpfs 隔离 `/fixture`、`/volume1`、`/var/targets`、`/run`，`TMPDIR=/fixture` 后运行。它们实际启动独立 sleep ELF，核 root exe SHA 和 uid65534 collector；覆盖目录升级、回滚、同 build 重启、多实例、不可信/过期证据和查询间实例变化。查询间测试只替代固定 PG 边界，不访问 PG。这是本地 fixture 证据，未运行 NAS、vendor ELF 或 systemd，不能当作现场权限或部署验收。
