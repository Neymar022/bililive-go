# 字幕增强

本分支为 bililive-go 增加了一条正式的 `ASS/libass` 字幕处理链：

- 录制完成并转成 MP4 后，自动入队字幕任务
- 先得到结构化 ASR 片段
- 经过严格分段器切成单排时间片
- 生成同名 `.srt`
- 生成同名 `.ass`
- 使用 `ffmpeg + libass` 烧录成正式展示视频
- 源视频在烧录成功前停留在 `source/`，不会提前暴露到 `video/`
- 保留 `.subtitle.json` 记录状态、分段、产物路径和源文件生命周期

## 目录约定

推荐将原始录屏和正式媒体库拆开：

- `source/`：bililive-go 原始录屏输出目录
- `video/`：媒体库正式扫描目录

对应 Docker 路径约定：

- `/srv/bililive-source`
- `/srv/bililive`

关键配置：

```yaml
out_put_path: /srv/bililive-source
subtitle:
  enabled: true
  source_root: /srv/bililive-source
  library_root: /srv/bililive
  burn_style:
    preset: vizard_classic_cn
```

说明：

- `preset` 仍保留作兼容字段
- 正式烧录路径已经切到 `ASS/libass`
- 不再使用 `SRT + force_style`
- 不再使用 `Pillow PNG overlay` 作为默认方案
- 当 `source_root` 与 `library_root` 分离时，自动管道会直接把烧录成品发布到 `library_root`
- 不再依赖先执行 `library_sync` 才能开始烧录

生产 NAS 推荐使用独立暂存目录，而不是直接把录制输出写进最终媒体库：

```yaml
out_put_path: /volume2/docker/bililive-go/srt_video
subtitle:
  enabled: true
  source_root: /volume2/docker/bililive-go/srt_video
  library_root: /volume2/docker/bililive-go/video
  burn_style:
    preset: vizard_classic_cn
```

这样录制中的视频、误触发生成的短片和未烧录完成的中间文件都会停留在 `srt_video/`，只有字幕烧录成功后的正式成品才会进入 `video/`。

## Provider

### DashScope / Qwen ASR

默认云端模式使用阿里百炼 `qwen3-asr-flash-filetrans`。

需要：

- `DASHSCOPE_API_KEY`
- `DASHSCOPE_BASE_URL`

worker 会：

1. 用 `ffmpeg` 从源视频抽取音频
2. 调用 DashScope 文件转写
3. 拿到结构化分段结果
4. 进入严格切分和 `ASS/libass` 烧录

`DASHSCOPE_BASE_URL` 既可以填写：

- `https://dashscope.aliyuncs.com`
- `https://dashscope.aliyuncs.com/compatible-mode/v1`

worker 会自动归一化到 DashScope 根地址。

### local-whisper

本地模式使用 `faster-whisper`，默认：

- model: `small`
- compute_type: `int8`
- CPU 单任务

这种模式不依赖公网 URL，但首次运行会下载模型，建议给 `subtitle-worker` 单独挂缓存卷。

### remote-mac-mlx（Apple Silicon 加速）

把转写工作转交给 Mac 上的 [BiliNote mac_transcriber_service](https://github.com/JefferyHan/BiliNote/tree/main/mac_transcriber_service)，
利用 mlx-whisper + Apple GPU 跑 `large-v3-turbo` 等大模型——**比 NAS CPU 跑 small 快 5-10 倍且字幕质量明显更好**。

需要：

- `SUBTITLE_MAC_TRANSCRIBER_URL`：Mac 上服务地址，如 `http://192.168.1.17:8484`
- `SUBTITLE_MAC_TRANSCRIBER_TOKEN`：可选，与 BiliNote 的 `MAC_TRANSCRIBER_TOKEN` 一致

worker 会通过 multipart 把抽好的音频上传到 `/transcribe`，把返回的 `segments` 转换为 ms 时间戳后进入烧录流程。

**串行约束**：mac_transcriber_service 在 Apple GPU 上做了 `threading.Semaphore(1)` 串行——
NAS bililive worker 端的 `processSemaphore`（容量 1）也是同样道理。两端形成双闸门：
HTTP 请求并发抵达，应用层 FIFO 排队，**避免 GPU 抢资源/模型权重重复加载**。

## Provider Chain（自动调度）

把 `provider` 设置成 `auto`、再配置 `SUBTITLE_PROVIDER_CHAIN`，worker 会按链顺序选择第一个**健康的 provider** 接管转写：

```yaml
# config.yml
subtitle:
  enabled: true
  default_provider: auto    # 触发 chain 模式（直接写 dashscope 等单值则走单一 provider）
  language: zh

# docker-compose.yaml
environment:
  SUBTITLE_PROVIDER_CHAIN: "remote-mac-mlx"
  SUBTITLE_ALLOW_CLOUD_ASR: "false"
  SUBTITLE_MAC_TRANSCRIBER_URL: http://192.168.1.17:8484
  SUBTITLE_MAC_TRANSCRIBER_TOKEN: <token>
  SUBTITLE_MAC_TRANSCRIBER_MODEL: large-v3-turbo
```

生产推荐是 **Mac-only 等待恢复**：只保留 `remote-mac-mlx`，并设置
`SUBTITLE_ALLOW_CLOUD_ASR=false`。这样 Mac 不通或转写失败时，worker 返回
`mac_transcriber_unavailable`，Go 侧会把同一个 pipeline task 重新置为 `pending`，
写入 `not_before` 延后调度，`.subtitle.json` 保持 `queued`，不会回退到 Qwen ASR 产生费用。

如确实需要云端兜底，才显式启用：

```yaml
environment:
  SUBTITLE_PROVIDER_CHAIN: "remote-mac-mlx,dashscope,local-whisper"
  SUBTITLE_ALLOW_CLOUD_ASR: "true"
  DASHSCOPE_API_KEY: <key>
```

调度触发条件：

| 情境 | 行为 |
|------|------|
| 主链 `remote-mac-mlx` 健康（`/healthz` 1.5s 内 200） | 用 Mac 转写 |
| Mac 不健康（关机/重启/超时）且 `SUBTITLE_ALLOW_CLOUD_ASR=false` | 返回 `mac_transcriber_unavailable`，同任务延后重试 |
| Mac 不健康且显式允许云 ASR | 跳过 Mac，继续尝试 `dashscope` |
| Mac 健康但 transcribe 中途异常，且未配置本地兜底 | 返回 `mac_transcriber_unavailable`，同任务延后重试 |
| `dashscope` 调用失败（OSS/任务失败/网络）且链里有 `local-whisper` | 切到 `local-whisper` |
| 所有 provider 都失败 | 抛最后一次的异常，标记 metadata 为 Failed |

**烧录阶段在转写成功后只跑一次**——chain 只覆盖转写阶段，不会重复 ffmpeg 浪费时间。

### 配置一致性 checklist

`provider="auto"` 必须配齐以下 env，缺一个就在第一次录播完成时才会报错（worker 没有启动期校验）：

| Env | 必须 | 说明 |
|-----|-----|------|
| `SUBTITLE_PROVIDER_CHAIN` | 是 | 生产推荐 `remote-mac-mlx`；云兜底才写 `remote-mac-mlx,dashscope,local-whisper` |
| `SUBTITLE_ALLOW_CLOUD_ASR` | 是 | 生产推荐 `false`，防止误配 `dashscope` 后产生 Qwen ASR 费用 |
| `SUBTITLE_MAC_TRANSCRIBER_URL` | 链含 mac 时必填 | 例 `http://192.168.1.17:8484` |
| `SUBTITLE_MAC_TRANSCRIBER_TOKEN` | 跟 BiliNote 端一致 | 写错会触发 `mac_transcriber_unavailable`，不会再静默走云端 |
| `SUBTITLE_MAC_TRANSCRIBER_MODEL` | 否 | 用于落 sidecar，生产当前为 `large-v3-turbo` |
| `DASHSCOPE_API_KEY` | 仅云兜底时必填 | `SUBTITLE_ALLOW_CLOUD_ASR=false` 时不会调用 |
| `SUBTITLE_MAC_HEALTH_TIMEOUT_SECONDS` | 否（默认 3.0） | 跨网段或 Mac 频繁睡眠唤醒时调高 |
| `SUBTITLE_RETRY_LATER_DELAY` | 否（默认 5m） | Mac 不可用后同一任务的下次调度延迟 |

成功后 `.subtitle.json` 会记录 `actual_provider`、`actual_model`、`actual_burn_provider`。
确认是否产生云 ASR 费用时，以这三个字段为准。

### 故障排查：Mac 不可用导致等待

链跳过 Mac 的两种语义：

- **真不在线**（连不上、超时、5xx）→ worker 日志 INFO `mac transcriber health probe failed`
- **token 错配**（401）→ worker 日志 **WARNING** `mac transcriber rejected health probe with 401`

如果任务持续 queued，先 grep worker stderr 是否有上述 WARNING：

```bash
docker logs subtitle-worker 2>&1 | grep "mac transcriber"
```

token 错配修复：让 NAS 的 `SUBTITLE_MAC_TRANSCRIBER_TOKEN` 与 Mac 端 launchd plist 里的
`MAC_TRANSCRIBER_TOKEN` env 完全一致即可（两端都未配 token 时也兼容——auth 跳过）。

## Burn Chain（Mac VideoToolbox 硬件烧录加速，P7+）

P6 解决了 ASR 转写的 NAS CPU 瓶颈。**P7 解决 ffmpeg 烧录字幕的瓶颈**：把烧录步骤
也 offload 到 Mac，让 VideoToolbox（h264_videotoolbox / hevc_videotoolbox）跑硬件
编码——5 分钟视频从 NAS 软编码 ~10 分钟降到 Mac VideoToolbox ~30 秒，**18× 加速**。

### 启用方式

要求：Mac 端 BiliNote `mac_transcriber_service` 升级到 P7+ 版本（提供 `/burn` 端点 +
`/healthz` 返 `burn_supported: true`）。

```yaml
# docker-compose.yaml
environment:
  # ... P6 转写链路 ...
  # P7 烧录链路（默认空 → 走老的 NAS 软编码，零侵入）
  SUBTITLE_BURN_CHAIN: "remote-mac,nas-software"
  SUBTITLE_MAC_BURN_URL: "http://192.168.1.17:8484"   # 跟 transcriber 同地址
  SUBTITLE_MAC_BURN_TOKEN: ""                          # 跟 transcriber 同 token
  SUBTITLE_MAC_BURN_CODEC: "h264_videotoolbox"         # 或 hevc_videotoolbox 出更小文件
  SUBTITLE_MAC_BURN_BITRATE: "5M"                      # 平均码率
  SUBTITLE_MAC_BURN_TIMEOUT: "1200"                    # 20 分钟（60 分钟视频烧录余量）
```

| 情境 | 行为 |
|------|------|
| `SUBTITLE_BURN_CHAIN` 未配（旧部署） | 走 `nas-software`，零侵入老逻辑 |
| 链 `[remote-mac, nas-software]` + Mac /healthz 返 `burn_supported: true` | 用 Mac VideoToolbox |
| Mac 不健康 / 旧版没 burn_supported 字段 | 跳过 Mac，自动降级 nas-software |
| Mac 健康但烧录中途失败（网络中断/ffmpeg crash） | 异常上抛 → 切 nas-software 重做 |
| 全失败 | 抛最后一次的真实异常 |

### Codec 选择

| codec | 速度 | 文件大小 | 兼容性 |
|-------|------|---------|--------|
| `h264_videotoolbox`（默认） | 10-15× 实时 | 与原片接近 | 所有播放器 ✓ |
| `hevc_videotoolbox` | 8-12× 实时 | **小 30-50%** | 旧设备/某些电视不支持 |

### 串行化

Mac 端 `_BURN_LOCK = threading.Semaphore(1)` 串行 ffmpeg 调用——VideoToolbox 走专用
ANE/Media Engine，跟 mlx-whisper（GPU shader）理论可并行，但 unified memory 带宽
共享，保险起见单一锁。NAS `processSemaphore` 容量 1 也确保同时只发一个请求。

### 故障排查

`actual_burn_provider` 字段写到返回 dict 中：
- `actual_burn_provider=remote-mac` → P7 链路工作 ✓
- `actual_burn_provider=nas-software` → 降级到了软编码，看 worker 日志 grep `burn chain`

```bash
docker logs subtitle-worker 2>&1 | grep "burn chain"
# 看到例如：burn chain chose provider=nas-software after attempts=[('remote-mac', 'skipped:not-supported'), ('nas-software', 'ok')]
```

### 设计 caveats（部署前必读）

**1. 原子发布保证**：
- `burn_remote_mac` 写到 `<output>.burning-<pid>-<ts>.tmp` 再 `os.replace`——失败时
  原 output 文件保留（chain fallback 到 nas-software 时旧产物不被损坏）。
- `burn_subtitles`（NAS 软编码）也是原子发布。
- **但 ASS 和 SRT 是先于 burn 写入**：burn 失败时 ASS/SRT 已落盘，调用方
  （SubtitleManager）需要按 `actual_burn_provider` 字段判断是否生成完整。

**2. FastAPI 线程池约束**：
- Mac 端 `_BURN_LOCK` 是 `threading.Lock()` 串行 ffmpeg 进程——burn 期间持锁占
  一个 anyio threadpool worker（默认 40）。
- 单个 BiliNote/NAS 配 `processSemaphore=1` 不会触发上限。
- **BiliNote 自身 NoteResults 不能 5+ 并发 burn**——会让 transcribe / healthz 排队。
  实际场景（顺序处理 note）不会撞这个限制。

**3. ffmpeg subprocess 超时**：
- Mac 端 `subprocess.run(timeout=1100)` 兜底——VideoToolbox 偶发 GPU stall 会
  被 SIGKILL，`_BURN_LOCK` 自然释放，避免后续永久卡死。
- NAS 端 `SUBTITLE_MAC_BURN_TIMEOUT=1200` 留 100s 给 mac 先处理 hung。

**4. ClientDisconnect 行为**：
- Mac 端用 `FileResponse + BackgroundTask` 清理临时文件——即使 NAS 端中途断开，
  Starlette 会捕获 `ClientDisconnect` 并仍执行 BackgroundTask，`/tmp` 不会泄漏。

## 输出文件

对于 `video/<主播>/Season 01/<episode>.mp4`，字幕链会维护：

- `<episode>.mp4`
- `<episode>.srt`
- `<episode>.ass`
- `<episode>.subtitle.json`

其中：

- `.mp4`：字幕成功后会被替换成烧录版
- `.srt`：保留给下载或二次处理
- `.ass`：正式烧录脚本，也是主线样式产物
- `.subtitle.json`：记录状态、分段、provider、render_preset、renderer_status、renderer_error、`.ass/.srt` 路径、是否保留源文件等

当 worker 在 `ffmpeg` 成功退出后、正式发布烧录成品前失败时，会返回 `subtitle finalize failed: ...`。这类错误表示编码阶段已经完成，问题发生在产物可见性检查或最终替换/发布阶段，不应和 `ffmpeg burn failed: ...` 混为一类。

## 严格分段规则

主线分段器遵循这些硬约束：

- 字幕固定单排显示
- 字号固定，不会因为长句自动缩小
- 长句允许拆成多个连续时间片，但优先保持语义完整
- 禁止出现 `1-2` 字尾片
- 尽量避免 `3` 字尾片
- 禁止把一句话切成“前半句 + 单字/单词尾巴”

切分优先级：

1. 标点边界
2. ASR 停顿
3. 短语边界
4. 最后才允许字符级回退

字符级回退后会做尾片重平衡，避免出现短尾片。

## 横竖屏参数

横屏和竖屏使用独立参数集，不共用一套宽度、字号、边距：

- 竖屏：更大的字号、更高的底边距、更短的单排最大字数
- 横屏：更宽的单排空间、更低的底边距、更长的单排最大字数

这部分由 `.ass` 生成器按视频尺寸自动选择。

## 源文件生命周期

当字幕任务满足以下条件时，源文件会在保留期后自动删除：

- 最新字幕任务成功
- 烧录版存在
- `.srt` 存在
- `.ass` 存在
- 未标记 `keep_source`

默认保留期：`7` 天。

也可以在 WebUI 的 `录屏字幕` 页面：

- 手动立即删除源文件
- 手动切换“保留源文件”

## WebUI

当前保留的字幕相关页面只有：

- `/recordings`

能力包括：

- 查看录屏字幕状态
- 查看当前 provider 与 render preset
- 手动用 DashScope 或 local-whisper 重跑
- 重跑时优先读取源文件；仅当源文件已不存在时才回退到媒体库成品
- 下载 `SRT`
- 删除源文件
- 在详情抽屉里查看逐段字幕和错误信息

## 已移除能力

以下实验性质能力已从主线移除：

- `字幕样式实验室`
- `/subtitle-style-lab`
- 单帧 PNG 实时预览链路
- 30 秒样片实验室接口
- `Pillow PNG overlay` 默认烧录路径

## Docker Compose

项目根目录下的默认 `docker-compose.yml` 为双服务，并默认拉 Docker Hub 镜像：

- `bililive-go`
- `subtitle-worker`

默认镜像：

- `neymar022/bililive-go-app:latest`
- `neymar022/bililive-go-subtitle-worker:latest`

默认挂载：

- `./Videos/source -> /srv/bililive-source`
- `./Videos/library -> /srv/bililive`

`subtitle-worker` 需要：

- `ffmpeg`
- `libass`
- `fonts-noto-cjk`

不再依赖 Playwright 或样式实验室预览运行时。

如果需要在本地按源码重新构建镜像，而不是直接拉 Docker Hub 镜像，请叠加：

- `docker-compose.build.yml`

示例：

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up --build
```

## Docker Hub 发布

默认发布目标已经切到：

- `neymar022/bililive-go-app`
- `neymar022/bililive-go-subtitle-worker`

GitHub Actions 发布依赖以下 secrets：

- `DOCKER_USERNAME`
- `DOCKER_TOKEN`

如果你在 NAS 中使用 Dockge，请把这两条路径替换成自己的实际目录。
