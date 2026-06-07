#!/usr/bin/env python3
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

TOOLS_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(TOOLS_DIR))

from bililive_tv_library_builder import build_tv_library, extract_episode_cover


def sidecar(stem: Path, suffix: str) -> Path:
    return Path(str(stem) + suffix)


class TvLibraryBuilderTest(unittest.TestCase):
    def setUp(self):
        self.cover_patcher = mock.patch(
            "bililive_tv_library_builder.extract_episode_cover",
            side_effect=self._write_cover,
        )
        self.cover_patcher.start()
        self.addCleanup(self.cover_patcher.stop)

    def _write_cover(self, source_path: Path, cover_path: Path, timeout_seconds: int = 60) -> bool:
        cover_path.parent.mkdir(parents=True, exist_ok=True)
        cover_path.write_bytes(b"cover")
        return True

    def test_completed_subtitle_output_survives_when_source_is_missing(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            source_root = root / "source"
            output_root = root / "video"
            source_root.mkdir()

            show_dir = output_root / "主播"
            season_dir = show_dir / "Season 01"
            season_dir.mkdir(parents=True)
            (show_dir / ".bililive-show").write_text("", encoding="utf-8")
            (show_dir / "tvshow.nfo").write_text("<tvshow />\n", encoding="utf-8")

            stem = season_dir / "主播.S01E0001.2026-03-20 - 同名直播"
            video_path = sidecar(stem, ".mp4")
            video_path.write_bytes(b"rendered")
            sidecar(stem, ".srt").write_text("old subtitles\n", encoding="utf-8")
            sidecar(stem, ".ass").write_text("old ass\n", encoding="utf-8")
            sidecar(stem, ".subtitle.json").write_text(
                json.dumps(
                    {
                        "status": "completed",
                        "renderer_status": "completed",
                        "source_path": str(source_root / "missing.mp4"),
                        "output_path": str(video_path),
                    },
                    ensure_ascii=False,
                )
                + "\n",
                encoding="utf-8",
            )

            summary = build_tv_library(source_roots=[source_root], output_root=output_root, dry_run=False)

            self.assertEqual(0, summary["removed_dirs"])
            self.assertEqual(0, summary["removed_files"])
            self.assertTrue(video_path.exists())
            self.assertTrue(sidecar(stem, ".srt").exists())
            self.assertTrue(sidecar(stem, ".ass").exists())
            self.assertTrue(sidecar(stem, ".subtitle.json").exists())
            self.assertTrue(sidecar(stem, ".jpg").exists())

    def test_new_source_does_not_reuse_completed_sidecar_slot(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            source_root = root / "source"
            output_root = root / "video"
            source_dir = source_root / "主播"
            source_dir.mkdir(parents=True)

            show_dir = output_root / "主播"
            season_dir = show_dir / "Season 01"
            season_dir.mkdir(parents=True)
            (show_dir / ".bililive-show").write_text("", encoding="utf-8")
            (show_dir / "tvshow.nfo").write_text("<tvshow />\n", encoding="utf-8")

            old_stem = season_dir / "主播.S01E0001.2026-03-20 - 同名直播"
            sidecar(old_stem, ".srt").write_text("old subtitles\n", encoding="utf-8")
            sidecar(old_stem, ".ass").write_text("old ass\n", encoding="utf-8")
            sidecar(old_stem, ".subtitle.json").write_text(
                json.dumps(
                    {
                        "status": "completed",
                        "renderer_status": "completed",
                        "source_path": str(source_root / "missing.mp4"),
                        "output_path": str(sidecar(old_stem, ".mp4")),
                    },
                    ensure_ascii=False,
                )
                + "\n",
                encoding="utf-8",
            )

            new_source = source_dir / "主播 - 2026-03-20 11-00-00 - 同名直播.mp4"
            new_source.write_bytes(b"new source")

            build_tv_library(source_roots=[source_root], output_root=output_root, dry_run=False)

            new_target = season_dir / "主播.S01E0002.2026-03-20 - 同名直播.mp4"
            self.assertFalse(sidecar(old_stem, ".mp4").exists())
            self.assertTrue(new_target.exists())
            self.assertEqual(new_source.stat().st_ino, new_target.stat().st_ino)
            episode_nfo = sidecar(new_target.with_suffix(""), ".nfo").read_text(encoding="utf-8")
            self.assertIn("<episode>2</episode>", episode_nfo)
            self.assertTrue(new_target.with_suffix(".jpg").exists())

    def test_completed_output_without_source_gets_missing_episode_nfo(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            source_root = root / "source"
            output_root = root / "video"
            source_root.mkdir()

            show_dir = output_root / "主播"
            season_dir = show_dir / "Season 01"
            season_dir.mkdir(parents=True)
            (show_dir / ".bililive-show").write_text("", encoding="utf-8")
            (show_dir / "tvshow.nfo").write_text("<tvshow />\n", encoding="utf-8")

            stem = season_dir / "主播.S01E0007.2026-03-20 - 历史直播"
            video_path = sidecar(stem, ".mp4")
            video_path.write_bytes(b"rendered")
            sidecar(stem, ".srt").write_text("old subtitles\n", encoding="utf-8")
            sidecar(stem, ".ass").write_text("old ass\n", encoding="utf-8")
            sidecar(stem, ".subtitle.json").write_text(
                json.dumps(
                    {
                        "status": "completed",
                        "renderer_status": "completed",
                        "source_path": str(source_root / "missing.mp4"),
                        "output_path": str(video_path),
                    },
                    ensure_ascii=False,
                )
                + "\n",
                encoding="utf-8",
            )

            summary = build_tv_library(source_roots=[source_root], output_root=output_root, dry_run=False)

            episode_nfo = sidecar(stem, ".nfo")
            self.assertEqual(1, summary["updated_nfos"])
            self.assertEqual(1, summary["updated_covers"])
            self.assertTrue(episode_nfo.exists())
            self.assertTrue(sidecar(stem, ".jpg").exists())
            episode_nfo_text = episode_nfo.read_text(encoding="utf-8")
            self.assertIn("<showtitle>主播</showtitle>", episode_nfo_text)
            self.assertIn("<episode>7</episode>", episode_nfo_text)

    def test_new_source_gets_episode_cover(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            source_root = root / "source"
            output_root = root / "video"
            source_dir = source_root / "主播"
            source_dir.mkdir(parents=True)

            new_source = source_dir / "主播 - 2026-03-20 10-00-00 - 新直播.mp4"
            new_source.write_bytes(b"new source")

            summary = build_tv_library(source_roots=[source_root], output_root=output_root, dry_run=False)

            target = output_root / "主播" / "Season 01" / "主播.S01E0001.2026-03-20 - 新直播.mp4"
            self.assertEqual(1, summary["updated_covers"])
            self.assertEqual(0, summary["cover_errors"])
            self.assertTrue(target.exists())
            self.assertTrue(target.with_suffix(".nfo").exists())
            self.assertEqual(b"cover", target.with_suffix(".jpg").read_bytes())

    def test_extract_episode_cover_falls_back_to_zero_second_for_short_video(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            source = root / "short.mp4"
            cover = root / "short.jpg"
            source.write_bytes(b"video")
            calls = []

            def fake_run(cmd, stdout, stderr, check, timeout):
                calls.append(cmd[cmd.index("-ss") + 1])
                if calls[-1] == "00:00:00":
                    Path(cmd[-1]).write_bytes(b"cover")
                return mock.Mock(returncode=0, stderr=b"")

            with mock.patch("bililive_tv_library_builder.subprocess.run", side_effect=fake_run):
                self.assertTrue(extract_episode_cover(source, cover))

            self.assertEqual(["00:00:01", "00:00:00"], calls)
            self.assertEqual(b"cover", cover.read_bytes())

    def test_new_source_does_not_reuse_existing_episode_number_with_different_title(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            source_root = root / "source"
            output_root = root / "video"
            source_dir = source_root / "主播"
            source_dir.mkdir(parents=True)

            show_dir = output_root / "主播"
            season_dir = show_dir / "Season 01"
            season_dir.mkdir(parents=True)
            (show_dir / ".bililive-show").write_text("", encoding="utf-8")
            (show_dir / "tvshow.nfo").write_text("<tvshow />\n", encoding="utf-8")

            old_stem = season_dir / "主播.S01E0001.2026-03-19 - 旧直播"
            old_video = sidecar(old_stem, ".mp4")
            old_video.write_bytes(b"rendered")
            sidecar(old_stem, ".subtitle.json").write_text(
                json.dumps(
                    {
                        "status": "completed",
                        "renderer_status": "completed",
                        "source_path": str(source_root / "missing.mp4"),
                        "output_path": str(old_video),
                    },
                    ensure_ascii=False,
                )
                + "\n",
                encoding="utf-8",
            )

            new_source = source_dir / "主播 - 2026-03-20 10-00-00 - 新直播.mp4"
            new_source.write_bytes(b"new source")

            build_tv_library(source_roots=[source_root], output_root=output_root, dry_run=False)

            duplicate_target = season_dir / "主播.S01E0001.2026-03-20 - 新直播.mp4"
            new_target = season_dir / "主播.S01E0002.2026-03-20 - 新直播.mp4"
            self.assertFalse(duplicate_target.exists())
            self.assertTrue(new_target.exists())

    def test_existing_protected_target_wins_over_idle_duplicate_with_same_inode(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            source_root = root / "source"
            output_root = root / "video"
            source_dir = source_root / "主播"
            source_dir.mkdir(parents=True)

            show_dir = output_root / "主播"
            season_dir = show_dir / "Season 01"
            season_dir.mkdir(parents=True)
            (show_dir / ".bililive-show").write_text("", encoding="utf-8")
            (show_dir / "tvshow.nfo").write_text("<tvshow />\n", encoding="utf-8")

            old_stem = season_dir / "主播.S01E0001.2026-03-19 - 旧直播"
            old_video = sidecar(old_stem, ".mp4")
            old_video.write_bytes(b"rendered")
            sidecar(old_stem, ".subtitle.json").write_text(
                json.dumps(
                    {
                        "status": "completed",
                        "renderer_status": "completed",
                        "source_path": str(source_root / "missing.mp4"),
                        "output_path": str(old_video),
                    },
                    ensure_ascii=False,
                )
                + "\n",
                encoding="utf-8",
            )

            source = source_dir / "主播 - 2026-03-20 10-00-00 - 新直播.mp4"
            source.write_bytes(b"new source")

            duplicate_stem = season_dir / "主播.S01E0001.2026-03-20 - 新直播"
            duplicate_video = sidecar(duplicate_stem, ".mp4")
            duplicate_video.hardlink_to(source)
            sidecar(duplicate_stem, ".subtitle.json").write_text(
                json.dumps(
                    {
                        "status": "idle",
                        "source_path": str(source),
                        "output_path": str(duplicate_video),
                    },
                    ensure_ascii=False,
                )
                + "\n",
                encoding="utf-8",
            )

            protected_stem = season_dir / "主播.S01E0002.2026-03-20 - 新直播"
            protected_video = sidecar(protected_stem, ".mp4")
            protected_video.hardlink_to(source)
            sidecar(protected_stem, ".subtitle.json").write_text(
                json.dumps(
                    {
                        "status": "running",
                        "renderer_status": "running",
                        "source_path": str(source),
                        "output_path": str(protected_video),
                    },
                    ensure_ascii=False,
                )
                + "\n",
                encoding="utf-8",
            )

            summary = build_tv_library(source_roots=[source_root], output_root=output_root, dry_run=False)

            self.assertEqual(2, summary["removed_files"])
            self.assertFalse(duplicate_video.exists())
            self.assertFalse(sidecar(duplicate_stem, ".subtitle.json").exists())
            self.assertTrue(protected_video.exists())
            self.assertTrue(sidecar(protected_stem, ".subtitle.json").exists())

    def test_raw_flv_source_is_not_published_to_subtitle_library(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            source_root = root / "source"
            output_root = root / "video"
            source_dir = source_root / "主播"
            source_dir.mkdir(parents=True)

            raw_flv = source_dir / "主播 - 2026-03-20 10-00-00 - 原始FLV.flv"
            raw_flv.write_bytes(b"raw flv")

            summary = build_tv_library(source_roots=[source_root], output_root=output_root, dry_run=False)

            self.assertEqual(0, summary["episodes"])
            self.assertFalse((output_root / "主播").exists())


if __name__ == "__main__":
    unittest.main()
