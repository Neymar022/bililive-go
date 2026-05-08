# bililive-go deployment tools (NAS host scripts)

These scripts run on the NAS host (NOT in container), invoked by cron, to
maintain the Plex/Jellyfin TV library structure under `video/<host>/Season 01/`.

## Files

- `bililive_sync_library.sh` — top-level wrapper, called by cron every 10 min
- `bililive_media_organizer.py` — normalizes filenames + writes NFO sidecars
- `bililive_tv_library_builder.py` — builds Plex S01E### hardlink structure

## Cron entry (host crontab)

```cron
*/10 * * * * /bin/sh /volume2/docker/bililive-go/tools/bililive_sync_library.sh >> /volume2/docker/bililive-go/organizer-cron.log 2>&1
```

## Why these aren't shipped in the container

bililive-go's recording stage writes to `srt_video/` (configured via
`subtitle.source_root` in `config.yml`). The pipeline's `subtitle_generate`
stage expects the matching display video to ALREADY exist in `video/<host>/Season 01/`,
which is the cron wrapper's job.

If the wrapper scans the wrong directory (e.g., legacy `source/` after the
P6/P7 introduction of `srt_video/`), every recording's pipeline silently fails
at `subtitle_generate` with "未在字幕库中找到源文件对应的展示视频".

## Regression test

`tests/test_sync_library.sh` validates the wrapper picks up new mp4 from
`srt_video/` and creates the Plex hardlink. Run on NAS:

```sh
sudo bash /volume2/docker/bililive-go/tools/tests/test_sync_library.sh
```

Expected output: `PASS: hardlink created at ... Plex format ✓`
