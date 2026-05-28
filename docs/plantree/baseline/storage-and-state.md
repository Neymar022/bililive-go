# Storage And State

## 仓库内状态

- `config*.yml`：运行配置样例和 Docker 配置。
- `src/livestate/`：直播状态存储与迁移。
- `src/pipeline/store.go`：pipeline 状态存储相关代码入口。
- `Videos/`：仓库内示例或本地输出目录，不代表生产 NAS 媒体库。

## PRD 中记录的生产路径

- NAS 地址：`192.168.1.80`
- Bililive-go 根目录：`/volume2/docker/bililive-go`
- 原始录制/待处理目录：`/volume2/docker/bililive-go/srt_video`
- 成品媒体库目录：`/volume2/docker/bililive-go/video`
- 报告目录：`/volume2/docker/bililive-go/reports`
- 工具目录：`/volume2/docker/bililive-go/tools`

## 媒体库形态

```text
/volume2/docker/bililive-go/video/<主播>/Season 01/<主播>.S01E####.<日期> - <标题>.mp4
/volume2/docker/bililive-go/video/<主播>/Season 01/<主播>.S01E####.<日期> - <标题>.srt
/volume2/docker/bililive-go/video/<主播>/Season 01/<主播>.S01E####.<日期> - <标题>.ass
/volume2/docker/bililive-go/video/<主播>/Season 01/<主播>.S01E####.<日期> - <标题>.nfo
/volume2/docker/bililive-go/video/<主播>/Season 01/<主播>.S01E####.<日期> - <标题>.subtitle.json
```

## BiliNote/LanceDB 状态边界

- BiliNote SQLite/source tables 是知识事实源。
- LanceDB 只做 side index，必须可从 SQLite 重建。
- LanceDB runtime 不应直接放在容易断开的 iSCSI 路径。
- PRD 当前建议 Mac runtime 位于 `/Volumes/BiliNoteRuntime`。

## 状态风险

- 同源重复入队可能让失败任务覆盖成功 metadata。
- 成品文件存在不等于 `.subtitle.json` 状态可信。
- 知识 ingest 失败不得改变视频烧录成功状态。
