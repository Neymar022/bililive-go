import importlib.util
import json
import re
import sys
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
REPAIR_SCRIPT = REPO_ROOT / "scripts" / "repair-library-sidecars.py"
DRIVER_SCRIPT = REPO_ROOT / "scripts" / "apply-ugreen-episode-ordinals.py"


class ApplyUGREENEpisodeOrdinalsTest(unittest.TestCase):
    def load_module(self, path: Path, name: str):
        spec = importlib.util.spec_from_file_location(name, path)
        assert spec is not None and spec.loader is not None
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        return module

    def fixture(self, tmpdir: str):
        root = Path(tmpdir) / "video"
        season = root / "主播" / "Season 01"
        season.mkdir(parents=True)
        repair = self.load_module(REPAIR_SCRIPT, "repair_ordinals_fixture")
        for index, microsecond in enumerate((123789, 123456), start=1):
            recorded_at = repair.datetime(
                2026, 6, 12, 19, 0, 0, microsecond, tzinfo=repair.MEDIA_LIBRARY_TIMEZONE
            )
            identity = repair.chronological_episode_identity(recorded_at)
            video = season / f"主播.S01E{identity}.2026-06-12 - 场次{index}.mp4"
            video.write_bytes(f"video-{index}".encode())
            video.with_suffix(".subtitle.json").write_text(
                json.dumps({"record_meta": {"start_time": recorded_at.isoformat()}}),
                encoding="utf-8",
            )
            video.with_suffix(".nfo").write_text(
                "<episodedetails>"
                f"<episode>{identity}</episode>"
                f'<uniqueid type="bililive-recorded-at" default="false">{identity}</uniqueid>'
                "</episodedetails>",
                encoding="utf-8",
            )
        return root, season

    def test_apply_is_nfo_only_and_rollback_restores_exact_content(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root, season = self.fixture(tmpdir)
            backup = Path(tmpdir) / "backup"
            backup.mkdir()
            driver = self.load_module(DRIVER_SCRIPT, "apply_ordinals_success")
            repair = driver.load_repair_module(REPAIR_SCRIPT)
            changed, summary = driver.build_plan(repair, root.resolve())
            before_media = {path: path.stat().st_ino for path in season.glob("*.mp4")}
            before_nfo = {path: path.read_bytes() for path in season.glob("*.nfo")}

            driver.apply_plan(
                repair,
                root.resolve(),
                backup.resolve(),
                REPAIR_SCRIPT,
                DRIVER_SCRIPT,
                summary["fingerprint"],
            )

            self.assertEqual(2, len(changed))
            self.assertEqual([1, 2], sorted(int(repair._xml_field_values(path, ("episode",))["episode"]) for path in season.glob("*.nfo")))
            self.assertEqual(before_media, {path: path.stat().st_ino for path in season.glob("*.mp4")})
            self.assertEqual("applied", json.loads((backup / "manifest.json").read_text())["state"])

            driver.rollback(backup.resolve())

            self.assertEqual(before_nfo, {path: path.read_bytes() for path in season.glob("*.nfo")})
            self.assertEqual("rolled_back", json.loads((backup / "manifest.json").read_text())["state"])

    def test_scoped_repair_does_not_block_on_an_unrelated_invalid_show(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root, _ = self.fixture(tmpdir)
            other = root / "Unrelated" / "Season 01" / "Unrelated.S01E0010.2026-06-12 - Broken.mp4"
            other.parent.mkdir(parents=True)
            other.write_bytes(b"unrelated-video")
            driver = self.load_module(DRIVER_SCRIPT, "apply_ordinals_scope")
            repair = driver.load_repair_module(REPAIR_SCRIPT)
            _, summary = driver.build_plan(repair, root.resolve(), only_show="主播")
            self.assertEqual(2, summary["episodes"])
            backup = Path(tmpdir) / "backup"
            driver.apply_plan(repair, root.resolve(), backup, REPAIR_SCRIPT, DRIVER_SCRIPT, summary["fingerprint"], only_show="主播")
            self.assertEqual(b"unrelated-video", other.read_bytes())
            self.assertFalse(other.with_suffix(".nfo").exists())
            driver.rollback(backup)

    def test_apply_rejects_changed_fixed_point_before_backup(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root, season = self.fixture(tmpdir)
            backup = Path(tmpdir) / "backup"
            backup.mkdir()
            driver = self.load_module(DRIVER_SCRIPT, "apply_ordinals_fixed_point")
            repair = driver.load_repair_module(REPAIR_SCRIPT)
            _, summary = driver.build_plan(repair, root.resolve())
            changed_nfo = next(season.glob("*.nfo"))
            changed_nfo.write_text(
                re.sub(
                    r"<episode>.*?</episode>",
                    "<episode>7</episode>",
                    changed_nfo.read_text(encoding="utf-8"),
                    count=1,
                ),
                encoding="utf-8",
            )

            with self.assertRaisesRegex(RuntimeError, "fixed point changed"):
                driver.apply_plan(
                    repair,
                    root.resolve(),
                    backup.resolve(),
                    REPAIR_SCRIPT,
                    DRIVER_SCRIPT,
                    summary["fingerprint"],
                )
            self.assertFalse((backup / "manifest.json").exists())

    def test_rollback_validates_every_entry_before_restoring_any_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root, _ = self.fixture(tmpdir)
            backup = Path(tmpdir) / "backup"
            backup.mkdir()
            driver = self.load_module(DRIVER_SCRIPT, "apply_ordinals_atomic_rollback")
            repair = driver.load_repair_module(REPAIR_SCRIPT)
            _, summary = driver.build_plan(repair, root.resolve())
            driver.apply_plan(
                repair,
                root.resolve(),
                backup.resolve(),
                REPAIR_SCRIPT,
                DRIVER_SCRIPT,
                summary["fingerprint"],
            )
            manifest_path = backup / "manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            first = Path(manifest["entries"][0]["path"])
            second = Path(manifest["entries"][1]["path"])
            second_after_apply = second.read_bytes()
            first.write_text("external change", encoding="utf-8")

            with self.assertRaisesRegex(RuntimeError, "NFO changed after apply"):
                driver.rollback(backup.resolve())

            self.assertEqual(second_after_apply, second.read_bytes())
            self.assertEqual("applied", json.loads(manifest_path.read_text())["state"])

    def test_rollback_validates_media_before_restoring_any_nfo(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root, season = self.fixture(tmpdir)
            backup = Path(tmpdir) / "backup"
            backup.mkdir()
            driver = self.load_module(DRIVER_SCRIPT, "apply_ordinals_media_guard")
            repair = driver.load_repair_module(REPAIR_SCRIPT)
            _, summary = driver.build_plan(repair, root.resolve())
            driver.apply_plan(
                repair,
                root.resolve(),
                backup.resolve(),
                REPAIR_SCRIPT,
                DRIVER_SCRIPT,
                summary["fingerprint"],
            )
            manifest_path = backup / "manifest.json"
            applied_nfo = {path: path.read_bytes() for path in season.glob("*.nfo")}
            next(season.glob("*.mp4")).write_bytes(b"changed-media")

            with self.assertRaisesRegex(RuntimeError, "media changed"):
                driver.rollback(backup.resolve())

            self.assertEqual(applied_nfo, {path: path.read_bytes() for path in season.glob("*.nfo")})
            self.assertEqual("applied", json.loads(manifest_path.read_text())["state"])


    def test_apply_repairs_aggregate_missing_uniqueid_without_touching_media(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root, season = self.fixture(tmpdir)
            nfo = next(season.glob("*.nfo"))
            nfo.write_text(re.sub(r"<uniqueid.*?</uniqueid>", "", nfo.read_text()), encoding="utf-8")
            before = nfo.read_bytes()
            before_media = {path: path.stat().st_ino for path in season.glob("*.mp4")}
            driver = self.load_module(DRIVER_SCRIPT, "apply_aggregate_missing_identity")
            repair = driver.load_repair_module(REPAIR_SCRIPT)
            changed, summary = driver.build_plan(repair, root.resolve())
            self.assertEqual(2, len(changed))
            backup = Path(tmpdir) / "backup"
            driver.apply_plan(repair, root.resolve(), backup, REPAIR_SCRIPT, DRIVER_SCRIPT, summary["fingerprint"])
            self.assertIn('type="bililive-recorded-at"', nfo.read_text())
            self.assertEqual([], driver.build_plan(repair, root.resolve())[0])
            self.assertEqual(before_media, {path: path.stat().st_ino for path in season.glob("*.mp4")})
            driver.rollback(backup)
            self.assertEqual(before, nfo.read_bytes())

    def test_failed_apply_preserves_concurrent_edit_instead_of_rolling_over_it(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root, _ = self.fixture(tmpdir)
            backup = Path(tmpdir) / "backup"
            driver = self.load_module(DRIVER_SCRIPT, "apply_ordinals_concurrent_failure")
            repair = driver.load_repair_module(REPAIR_SCRIPT)
            changed, summary = driver.build_plan(repair, root.resolve())
            first, second = (item.nfo_path for item in changed)
            write = driver.atomic_write
            def interrupt(path, data, mode):
                if path == second:
                    first.write_text("concurrent edit", encoding="utf-8")
                    raise OSError("injected write failure")
                write(path, data, mode)
            driver.atomic_write = interrupt
            with self.assertRaises((OSError, RuntimeError)):
                driver.apply_plan(repair, root.resolve(), backup, REPAIR_SCRIPT, DRIVER_SCRIPT, summary["fingerprint"])
            self.assertEqual("concurrent edit", first.read_text())
            self.assertEqual("recovery_required", json.loads((backup / "manifest.json").read_text())["state"])


if __name__ == "__main__":
    unittest.main()
