# 字幕增强

本分支为 bililive-go 增加了一条录屏字幕处理链：

- 录制完成并转成 MP4 后，自动入队字幕任务
- 生成同名 `SRT`
- 生成烧录字幕后的正式展示视频
- 保留 `.subtitle.json` 记录字幕状态、分段、渲染 preset、renderer 状态/错误和源文件生命周期
- 提供 WebUI 的 `录屏字幕` 页面用于查看状态、手动重跑、下载 SRT、删除源文件

## 目录约定

推荐将原始录屏和正式媒体库拆开：

- `source/`：bililive-go 原始录屏输出目录
- `video/`：媒体库正式扫描目录

对应 Docker 路径约定：

- `/srv/bililive-source`
- `/srv/bililive`

字幕增强配置里的关键路径：

```yaml
out_put_path: /srv/bililive-source
 subtitle:
  enabled: true
  source_root: /srv/bililive-source
  library_root: /srv/bililive
  burn_style:
    preset: vizard_classic_cn
```

## Provider

### DashScope / Qwen ASR

默认云端模式使用阿里百炼 `qwen3-asr-flash-filetrans`。

需要：

- `DASHSCOPE_API_KEY`
- `DASHSCOPE_BASE_URL`
- `subtitle.public_url_base`

说明：

- DashScope 文件转写模式要求一个可访问的音频文件 URL
- worker 会先用 `ffmpeg` 从源视频抽取音频
- 然后用 `subtitle.public_url_base + /files/...` 生成可访问地址交给 DashScope
- `DASHSCOPE_BASE_URL` 既可以填写 `https://dashscope.aliyuncs.com`，也可以填写你在 OpenAI 兼容模式里常用的 `https://dashscope.aliyuncs.com/compatible-mode/v1`，worker 会自动归一化

如果 `public_url_base` 不是外部可访问地址，DashScope 模式会失败。

### local-whisper

本地模式使用 `faster-whisper`，默认：

- model: `small`
- compute_type: `int8`
- CPU 单任务

这种模式不依赖公网 URL，但首次运行会下载模型，建议给 `subtitle-worker` 容器单独挂缓存卷。

如果 NAS 直连 Hugging Face 不稳定，建议给 `subtitle-worker` 配置以下任一方式：

- `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY`
- `HF_ENDPOINT=https://hf-mirror.com`

例如在中国网络环境里常见的做法是：

```env
HTTP_PROXY=http://192.168.1.80:20171
HTTPS_PROXY=http://192.168.1.80:20171
HF_ENDPOINT=https://hf-mirror.com
```

这样本地 `faster-whisper` 首次下载模型时会稳定得多。

## 输出文件

对于 `video/<主播>/Season 01/<episode>.mp4`，字幕链会维护：

- `<episode>.mp4`
- `<episode>.srt`
- `<episode>.subtitle.json`

其中：

- `.mp4`：字幕成功后会被替换成烧录版。当前默认使用 `vizard_classic_cn` cue-card overlay 渲染，而不是依赖 `ffmpeg subtitles + force_style`
- `.srt`：仍然保留供下载或二次处理，即使烧录路径已经改成 preset renderer
- `.subtitle.json`：记录状态、分段、provider、render_preset、renderer_status、renderer_error、是否保留源文件等

## 源文件生命周期

当字幕任务满足以下条件时，源文件会在保留期后自动删除：

- 最新字幕任务成功
- 烧录版存在
- SRT 存在
- 未标记 `keep_source`

默认保留期：`7` 天。

也可以在 WebUI 的 `录屏字幕` 页面：

- 手动立即删除源文件
- 手动切换“保留源文件”

## WebUI

新增页面：

- `/recordings`

能力包括：

- 查看录屏字幕状态
- 查看当前 provider 与 render preset
- 手动用 DashScope 或 local-whisper 重跑
- 下载 SRT
- 删除源文件
- 在详情抽屉里预览视频、逐段字幕，以及单独展示渲染错误

## Docker Compose

项目根目录下的 `docker-compose.yml` 已经调整为双服务：

- `bililive-go`
- `subtitle-worker`

默认挂载：

- `./Videos/source -> /srv/bililive-source`
- `./Videos/library -> /srv/bililive`

`subtitle-worker` 镜像内会额外安装 Playwright Chromium 渲染栈和 `fonts-noto-cjk`，用于生成 `vizard_classic_cn` 字幕卡片。

如果你在 NAS 中使用 Dockge，请把这两条路径替换成自己的实际目录。
