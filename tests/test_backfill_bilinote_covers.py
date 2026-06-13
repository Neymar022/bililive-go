import json
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts" / "backfill-bilinote-covers.py"


class BackfillBiliNoteCoversTest(unittest.TestCase):
    def run_script(self, db: Path, static_root: Path, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--db",
                str(db),
                "--static-root",
                str(static_root),
                "--api-base-url",
                "http://192.168.1.80",
                "--backend-port",
                "8483",
                *args,
            ],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def create_db(self, db: Path, video: Path) -> None:
        connection = sqlite3.connect(db)
        try:
            connection.execute(
                "CREATE TABLE note_records (id INTEGER PRIMARY KEY, task_id TEXT, audio_meta TEXT, form_data TEXT)"
            )
            connection.execute(
                "INSERT INTO note_records (id, task_id, audio_meta, form_data) VALUES (?, ?, ?, ?)",
                (
                    1,
                    "task-1",
                    json.dumps({"title": "测试", "video_path": str(video)}, ensure_ascii=False),
                    json.dumps({}, ensure_ascii=False),
                ),
            )
            connection.execute(
                "INSERT INTO note_records (id, task_id, audio_meta, form_data) VALUES (?, ?, ?, ?)",
                (
                    2,
                    "task-2",
                    json.dumps({"title": "已有封面", "cover_url": "http://old/cover.jpg"}, ensure_ascii=False),
                    json.dumps({"source_video_path": str(video)}, ensure_ascii=False),
                ),
            )
            connection.commit()
        finally:
            connection.close()

    def test_dry_run_reports_without_mutating_and_apply_backfills_cover_url(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = Path(tmpdir)
            video = tmp / "主播.S01E0001.2026-06-12 - 测试.mp4"
            video.write_bytes(b"video")
            video.with_suffix(".jpg").write_bytes(b"cover")
            db = tmp / "bilinote.db"
            static_root = tmp / "static"
            self.create_db(db, video)

            dry_run = self.run_script(db, static_root)
            self.assertEqual(0, dry_run.returncode, dry_run.stderr)
            self.assertIn("candidate_records=1", dry_run.stdout)
            self.assertFalse((static_root / "cover").exists())

            apply = self.run_script(db, static_root, "--apply")
            self.assertEqual(0, apply.returncode, apply.stderr)
            self.assertTrue(any((static_root / "cover").glob("*.jpg")))

            connection = sqlite3.connect(db)
            try:
                rows = connection.execute("SELECT id, audio_meta FROM note_records ORDER BY id").fetchall()
            finally:
                connection.close()

            first_meta = json.loads(rows[0][1])
            second_meta = json.loads(rows[1][1])
            self.assertTrue(first_meta["cover_url"].startswith("http://192.168.1.80:8483/static/cover/"))
            self.assertEqual("http://old/cover.jpg", second_meta["cover_url"])


if __name__ == "__main__":
    unittest.main()
