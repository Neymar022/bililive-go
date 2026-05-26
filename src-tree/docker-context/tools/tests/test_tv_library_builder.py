#!/usr/bin/env python3
import json
import sys
import tempfile
import unittest
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(TOOLS_DIR))

from bililive_tv_library_builder import build_tv_library


def sidecar(stem: Path, suffix: str) -> Path:
    return Path(str(stem) + suffix)


class TvLibraryBuilderTest(unittest.TestCase):
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
            self.assertTrue(episode_nfo.exists())
            episode_nfo_text = episode_nfo.read_text(encoding="utf-8")
            self.assertIn("<showtitle>主播</showtitle>", episode_nfo_text)
            self.assertIn("<episode>7</episode>", episode_nfo_text)

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
