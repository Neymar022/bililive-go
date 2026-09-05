import importlib.util
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "adopt-recording-session.py"


class HistoricalRecordingSessionTest(unittest.TestCase):
    def load_driver(self):
        spec = importlib.util.spec_from_file_location("adopt_recording_session", SCRIPT)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def fixture(self, directory):
        root = Path(directory).resolve()
        library = root / "video"
        source = root / "source"
        library.mkdir()
        source.mkdir()
        original = source / "主播 - 2026-09-04 18-01-35 - 测试.flv"
        split = [{"path": str(original.with_name(original.stem + f"00{i}.flv")), "type": "video", "source_path": str(original)} for i in (1, 2, 3)]
        files = [{"path": str(Path(item["path"]).with_suffix(".mp4")), "type": "video", "source_path": item["path"]} for item in split]
        for item in files:
            Path(item["path"]).write_bytes(b"retained-video")
        initial = [{"path": str(original), "type": "video"}]
        failure = "subtitle_generate: failed to ensure library hardlink: EnsureLibraryHardlink: published episode ordinals require repair: duplicate ordinal 29"
        results = [
            {"stage_name": "fix_flv", "stage_index": 0, "status": "completed", "input_files": initial, "output_files": split},
            {"stage_name": "convert_mp4", "stage_index": 1, "status": "completed", "input_files": split, "output_files": files},
            {"stage_name": "subtitle_generate", "stage_index": 2, "status": "failed", "input_files": files, "error_message": failure},
        ]
        database = root / "pipeline.db"
        conn = sqlite3.connect(database)
        conn.row_factory = sqlite3.Row
        conn.executescript('''
            CREATE TABLE pipeline_tasks (id INTEGER PRIMARY KEY, status TEXT, record_info_json TEXT, pipeline_config_json TEXT,
                initial_files_json TEXT, current_files_json TEXT, current_stage INTEGER, total_stages INTEGER,
                stage_results_json TEXT, can_retry INTEGER, not_before TEXT, error_message TEXT);
            CREATE TABLE pipeline_recording_sessions (id TEXT PRIMARY KEY, live_id TEXT, state_json TEXT);
            ATTACH DATABASE ':memory:' AS legacy_lives;
            CREATE TABLE legacy_lives.live_sessions (id INTEGER PRIMARY KEY, live_id TEXT, start_time INTEGER, end_time INTEGER, end_reason TEXT, host_name TEXT, room_name TEXT);
            CREATE TABLE legacy_lives.live_rooms (live_id TEXT PRIMARY KEY, url TEXT);
        ''')
        record = {"live_id": "room", "live_session_id": "961", "start_time": "2026-09-04T18:58:16+08:00", "host_name": "主播", "room_name": "测试"}
        conn.execute("INSERT INTO pipeline_tasks VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", (1518, "failed", json.dumps(record), json.dumps({"stages": [{"name": name} for name in ("fix_flv", "convert_mp4", "subtitle_generate")]}), json.dumps(initial), json.dumps(files), 2, 3, json.dumps(results), 1, "2026-09-06T02:00:00+08:00", failure))
        conn.execute("INSERT INTO legacy_lives.live_sessions VALUES(961,'room',1788516095,1788519619,'normal','主播','测试')")
        conn.execute("INSERT INTO legacy_lives.live_rooms VALUES('room','https://live.douyin.com/565107510570')")
        conn.commit()
        log = root / "recording.log"
        log.write_text(
            'time="2026-09-04 18:01:35" level=info msg="Record Start https://live.douyin.com/565107510570" host=live.douyin.com room=/565107510570\n'
            'time="2026-09-04 18:58:16" level=info msg="pipeline task enqueued: 1 files, 3 stages" host=live.douyin.com room=/565107510570\n'
            'time="2026-09-04 19:00:19" level=info msg="Record End" host=live.douyin.com room=/565107510570\n'
            'time="2026-09-04 19:00:19" level=info msg="推送录制摘要：1 个文件" host=live.douyin.com room=/565107510570\n', encoding="utf-8")
        return conn, log, library, source, files

    def test_record_end_without_final_summary_cannot_seal_historical_inputs(self):
        driver = self.load_driver()
        session = {"start_time": 1788516095, "end_time": 1788519619, "end_reason": "normal"}
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "recording.log"
            text = (
                'time="2026-09-04 18:01:35" level=info msg="Record Start https://live.douyin.com/565107510570" host=live.douyin.com room=/565107510570\n'
                'time="2026-09-04 18:58:16" level=info msg="pipeline task enqueued: 1 files, 3 stages" host=live.douyin.com room=/565107510570\n'
                'time="2026-09-04 19:00:19" level=info msg="Record End" host=live.douyin.com room=/565107510570\n'
            )
            log.write_text(text, encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "final recording summary"):
                driver.verify_closure(log, session, "https://live.douyin.com/565107510570", 1)
            log.write_text(text + 'time="2026-09-04 19:00:19" level=info msg="推送录制摘要：1 个文件" host=live.douyin.com room=/565107510570\n', encoding="utf-8")
            proof = driver.verify_closure(log, session, "https://live.douyin.com/565107510570", 1)
            self.assertEqual([1, 2, 3, 4], proof["lines"])
            self.assertEqual(1, proof["registered_inputs"])

    def test_plan_keeps_converted_inputs_and_uses_proven_recording_start(self):
        driver = self.load_driver()
        with tempfile.TemporaryDirectory() as directory:
            conn, log, library, source, files = self.fixture(directory)
            self.addCleanup(conn.close)
            before = dict(conn.execute("SELECT * FROM pipeline_tasks").fetchone())
            plan = driver.build_plan(conn, "961", log, library, source)
            self.assertEqual(before, dict(conn.execute("SELECT * FROM pipeline_tasks").fetchone()))
            self.assertEqual(files, json.loads(plan["before_task"]["current_files_json"]))
            record = json.loads(plan["after_task"]["record_info_json"])
            self.assertEqual("2026-09-04T18:01:35+08:00", record["start_time"])
            self.assertTrue(record["recording_producer_id"].startswith("historical-"))
            self.assertEqual("failed", plan["after_task"]["status"])
            self.assertEqual(before["not_before"], plan["after_task"]["not_before"])
            self.assertFalse(plan["session"]["ready"])
            self.assertEqual({"1518": {"ready": False, "sources": []}}, plan["session"]["tasks"])

    def test_unowned_same_day_library_media_blocks_adoption(self):
        driver = self.load_driver()
        with tempfile.TemporaryDirectory() as directory:
            conn, log, library, source, _ = self.fixture(directory)
            self.addCleanup(conn.close)
            season = library / "主播" / "Season 01"
            season.mkdir(parents=True)
            (season / "主播.S01E123.2026-09-04 - 测试.mp4").write_bytes(b"existing-result")
            with self.assertRaisesRegex(ValueError, "existing publication"):
                driver.build_plan(conn, "961", log, library, source)

    def test_apply_and_rollback_change_only_task_origin_with_current_value_guard(self):
        driver = self.load_driver()
        with tempfile.TemporaryDirectory() as directory:
            conn, log, library, source, files = self.fixture(directory)
            self.addCleanup(conn.close)
            conn.execute("INSERT INTO pipeline_recording_sessions VALUES('newer','room','{}')")
            conn.commit()
            plan = driver.build_plan(conn, "961", log, library, source)
            backup = Path(directory) / "backup"
            driver.apply_plan(conn, plan, backup)
            self.assertEqual(plan["after_task"], dict(conn.execute("SELECT * FROM pipeline_tasks").fetchone()))
            self.assertEqual("newer", conn.execute("SELECT id FROM pipeline_recording_sessions ORDER BY rowid DESC LIMIT 1").fetchone()[0])
            self.assertEqual([b"retained-video"] * 3, [Path(item["path"]).read_bytes() for item in files])
            with self.assertRaises(ValueError):
                driver.apply_plan(conn, plan, Path(directory) / "second-backup")
            driver.rollback(conn, backup)
            self.assertEqual(plan["before_task"], dict(conn.execute("SELECT * FROM pipeline_tasks").fetchone()))
            self.assertIsNone(conn.execute("SELECT 1 FROM pipeline_recording_sessions WHERE id='961'").fetchone())

    def test_stale_plan_is_rejected_before_any_database_changes(self):
        driver = self.load_driver()
        with tempfile.TemporaryDirectory() as directory:
            conn, log, library, source, _ = self.fixture(directory)
            self.addCleanup(conn.close)
            plan = driver.build_plan(conn, "961", log, library, source)
            conn.execute("UPDATE pipeline_tasks SET not_before = 'changed'")
            conn.commit()
            with self.assertRaisesRegex(ValueError, "fixed point changed"):
                driver.apply_plan(conn, plan, Path(directory) / "backup")
            self.assertEqual(0, conn.execute("SELECT count(*) FROM pipeline_recording_sessions").fetchone()[0])
            self.assertEqual("changed", conn.execute("SELECT not_before FROM pipeline_tasks").fetchone()[0])

    def test_rollback_refuses_resumed_task_and_preserves_new_state(self):
        driver = self.load_driver()
        with tempfile.TemporaryDirectory() as directory:
            conn, log, library, source, _ = self.fixture(directory)
            self.addCleanup(conn.close)
            plan = driver.build_plan(conn, "961", log, library, source)
            backup = Path(directory) / "backup"
            driver.apply_plan(conn, plan, backup)
            conn.execute("UPDATE pipeline_tasks SET status = 'pending'")
            conn.commit()
            with self.assertRaisesRegex(ValueError, "checkpoint changed"):
                driver.rollback(conn, backup)
            self.assertEqual("pending", conn.execute("SELECT status FROM pipeline_tasks").fetchone()[0])
            self.assertEqual(1, conn.execute("SELECT count(*) FROM pipeline_recording_sessions").fetchone()[0])

    def test_failed_parser_tail_cannot_be_hidden_by_an_earlier_successful_summary(self):
        driver = self.load_driver()
        with tempfile.TemporaryDirectory() as directory:
            conn, log, library, source, _ = self.fixture(directory)
            self.addCleanup(conn.close)
            lines = log.read_text().splitlines()
            lines.insert(2, 'time="2026-09-04 18:59:00" level=error msg="failed to parse live stream" host=live.douyin.com room=/565107510570')
            log.write_text("\n".join(lines) + "\n")
            (source / "主播 - 2026-09-04 18-58-30 - 末段.flv").write_bytes(b"unregistered-tail")
            with self.assertRaisesRegex(ValueError, "ambiguous recording closure"):
                driver.build_plan(conn, "961", log, library, source)
            lines.pop(2)
            log.write_text("\n".join(lines) + "\n")
            with self.assertRaisesRegex(ValueError, "unregistered recording input"):
                driver.build_plan(conn, "961", log, library, source)

    def test_changed_host_alias_cannot_hide_an_existing_completed_result(self):
        driver = self.load_driver()
        with tempfile.TemporaryDirectory() as directory:
            conn, log, library, source, files = self.fixture(directory)
            self.addCleanup(conn.close)
            record = json.loads(conn.execute("SELECT record_info_json FROM pipeline_tasks").fetchone()[0])
            record["host_name"] = "新主播名"
            conn.execute("UPDATE pipeline_tasks SET record_info_json = ?", (json.dumps(record),))
            conn.execute("UPDATE legacy_lives.live_sessions SET host_name = '新主播名'")
            conn.commit()
            season = library / "主播" / "Season 01"
            season.mkdir(parents=True)
            (season / "already.subtitle.json").write_text(json.dumps({"status": "completed", "source_path": files[0]["path"], "record_meta": {"live_session_id": "961"}}))
            with self.assertRaisesRegex(ValueError, "source alias"):
                driver.build_plan(conn, "961", log, library, source)


if __name__ == "__main__":
    unittest.main()
