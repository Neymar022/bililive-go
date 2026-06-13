import os
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts" / "repair-library-sidecars.py"


class RepairLibrarySidecarsTest(unittest.TestCase):
    def run_script(self, root: Path, *args: str, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
        merged_env = os.environ.copy()
        if env:
            merged_env.update(env)
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--root", str(root), *args],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=merged_env,
            check=False,
        )

    def fake_ffmpeg(self, tmp: Path) -> Path:
        path = tmp / "fake-ffmpeg"
        path.write_text(
            textwrap.dedent(
                """\
                #!/bin/sh
                out=""
                for arg do
                    out="$arg"
                done
                printf cover > "$out"
                """
            ),
            encoding="utf-8",
        )
        path.chmod(path.stat().st_mode | stat.S_IXUSR)
        return path

    def test_repairs_missing_cover_and_nfo_with_configurable_ffmpeg(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = Path(tmpdir)
            root = tmp / "video"
            season = root / "主播" / "Season 01"
            season.mkdir(parents=True)
            video = season / "主播.S01E0001.2026-06-12 - 测试直播.mp4"
            video.write_bytes(b"video")

            dry_run = self.run_script(root)
            self.assertEqual(0, dry_run.returncode, dry_run.stderr)
            self.assertIn("cover_repairs=1", dry_run.stdout)
            self.assertFalse(video.with_suffix(".jpg").exists())

            apply = self.run_script(root, "--apply", env={"FFMPEG_BIN": str(self.fake_ffmpeg(tmp))})
            self.assertEqual(0, apply.returncode, apply.stderr)
            self.assertTrue((root / "主播" / "tvshow.nfo").exists())
            self.assertTrue(video.with_suffix(".nfo").exists())
            self.assertEqual(b"cover", video.with_suffix(".jpg").read_bytes())

    def test_duplicate_show_merge_quarantines_conflicting_sidecar(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            canonical = root / "天津蛋哥 6点说车" / "Season 01"
            duplicate = root / "天津蛋哥   6点说车" / "Season 01"
            canonical.mkdir(parents=True)
            duplicate.mkdir(parents=True)
            canonical_video = canonical / "天津蛋哥 6点说车.S01E0001.2026-06-12 - 说车.mp4"
            duplicate_video = duplicate / "天津蛋哥 6点说车.S01E0001.2026-06-12 - 说车.mp4"
            canonical_video.write_bytes(b"canonical")
            duplicate_video.write_bytes(b"duplicate")
            duplicate_video.with_suffix(".jpg").write_bytes(b"duplicate-cover")

            result = self.run_script(root, "--apply", "--merge-duplicate-shows")

            self.assertEqual(0, result.returncode, result.stderr)
            self.assertIn("quarantined_files=1", result.stdout)
            self.assertTrue(canonical_video.exists())
            self.assertTrue(any((root / ".quarantine-library-sidecars").rglob("*.mp4")))


if __name__ == "__main__":
    unittest.main()
