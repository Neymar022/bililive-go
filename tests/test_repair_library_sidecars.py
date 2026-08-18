import importlib.util
import json
import os
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from types import ModuleType


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts" / "repair-library-sidecars.py"


class RepairLibrarySidecarsTest(unittest.TestCase):
    def run_script(
        self,
        root: Path,
        *args: str,
        env: dict[str, str] | None = None,
        cwd: Path | None = None,
    ) -> subprocess.CompletedProcess[str]:
        merged_env = os.environ.copy()
        if env:
            merged_env.update(env)
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--root", str(root), *args],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=merged_env,
            cwd=cwd,
            check=False,
        )

    def load_script_module(self) -> ModuleType:
        spec = importlib.util.spec_from_file_location("repair_library_sidecars_test_module", SCRIPT)
        assert spec is not None and spec.loader is not None
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        return module

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
                case "$out" in
                  *.jpg) ;;
                  *) printf 'output must retain jpg extension: %s\\n' "$out" >&2; exit 9 ;;
                esac
                printf cover > "$out"
                """
            ),
            encoding="utf-8",
        )
        path.chmod(path.stat().st_mode | stat.S_IXUSR)
        return path

    def test_normalize_component_matches_runtime_unicode_whitespace_identity(self) -> None:
        module = self.load_script_module()
        for separator in ("\t", "\n", "\u00a0", "\u3000", "\t\u00a0\u3000\n"):
            self.assertEqual(
                "天津蛋哥 6点说车",
                module.normalize_component(f"天津蛋哥{separator}6点说车"),
            )
        self.assertEqual("主播", module.normalize_component("主\u200b播"))

    def test_chronological_plan_is_bijective_monotonic_and_uses_exact_recorded_at(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            season = root / "主播" / "Season 01"
            season.mkdir(parents=True)
            later = season / "主播.S01E0001.2026-06-12 - 较晚场次.mp4"
            earlier = season / "主播.S01E0099.2026-06-12 - 较早场次.mp4"
            later.write_bytes(b"later")
            earlier.write_bytes(b"earlier")
            later.with_suffix(".subtitle.json").write_text(
                '{"record_meta":{"start_time":"2026-06-12T19:00:00.123789+08:00"}}',
                encoding="utf-8",
            )
            earlier.with_suffix(".subtitle.json").write_text(
                '{"record_meta":{"start_time":"2026-06-12T19:00:00.123456+08:00"}}',
                encoding="utf-8",
            )
            earlier.with_suffix(".nfo").write_text(
                "<episodedetails>"
                "<sorttitle>旧顺序</sorttitle><episode>99</episode>"
                "<aired>2026-06-12</aired><dateadded>2026-06-12 00:00:00</dateadded>"
                "<plot>旧录制时间</plot></episodedetails>",
                encoding="utf-8",
            )
            later.with_suffix(".nfo").write_text(
                "<episodedetails>"
                "<sorttitle>旧顺序</sorttitle><episode>1</episode>"
                "<aired>2026-06-12</aired><dateadded>2026-06-12 00:00:00</dateadded>"
                "<plot>旧录制时间</plot></episodedetails>",
                encoding="utf-8",
            )
            later.touch()

            module = self.load_script_module()
            plan = module.plan_chronological_episode_renumber(root.resolve(), module.collect_episodes(root.resolve()))

            self.assertEqual(2, len(plan))
            self.assertEqual(2, len({item.source for item in plan}))
            self.assertEqual(2, len({item.target for item in plan}))
            ordered = sorted(plan, key=lambda item: item.recorded_at)
            self.assertLess(ordered[0].episode, ordered[1].episode)

            file_plan = module.plan_chronological_file_moves(root.resolve(), plan)
            self.assertEqual(6, len(file_plan))
            self.assertEqual(6, len({item.source for item in file_plan}))
            self.assertEqual(6, len({item.target for item in file_plan}))

            manifest_root = root.parent / ".knowledge_sessions"
            manifest_root.mkdir()
            hidden_root = root.parent / ".live_session_segments"
            hidden_root.mkdir()
            manifest = manifest_root / "session.json"
            earlier_source = next(item.source for item in file_plan if item.source.name == earlier.name)
            manifest.write_text(
                json.dumps(
                    {
                        "sources": [{"library_path": str(earlier_source)}],
                        "unrelated": str(next(item.target for item in file_plan if item.source == earlier_source)),
                    }
                ),
                encoding="utf-8",
            )
            hidden_metadata = hidden_root / "aggregate.subtitle.json"
            hidden_metadata.write_text(
                json.dumps({"record_meta": {"live_session_media_superseded_by": str(earlier_source)}}),
                encoding="utf-8",
            )
            parsed_json, references_by_source = module.chronological_reference_audit(
                [root.resolve(), manifest_root.resolve(), hidden_root.resolve()], file_plan
            )
            self.assertEqual(4, parsed_json)
            self.assertEqual(2, sum(references_by_source.values()))
            self.assertEqual(2, references_by_source[str(earlier_source)])

            nfo_edits = module.plan_chronological_nfo_edits(plan)
            self.assertEqual(2, len(nfo_edits))
            earlier_edit = next(item for item in nfo_edits if item.path == earlier.with_suffix(".nfo").resolve())
            self.assertEqual(str(ordered[0].episode), earlier_edit.fields["episode"][1])
            self.assertEqual("2026-06-12 19:00:00", earlier_edit.fields["dateadded"][1])
            self.assertIn("19-00-00.123456", earlier_edit.fields["sorttitle"][1])

            json_edits = module.plan_chronological_json_edits(
                [root.resolve(), manifest_root.resolve(), hidden_root.resolve()], file_plan
            )
            self.assertEqual(2, len(json_edits))
            self.assertEqual(
                {"/sources/0/library_path", "/record_meta/live_session_media_superseded_by"},
                {item.pointer for item in json_edits},
            )
            self.assertEqual({str(earlier_source)}, {item.old for item in json_edits})
            post_refs = module.simulate_chronological_json_references(
                [root.resolve(), manifest_root.resolve(), hidden_root.resolve()], file_plan
            )
            self.assertEqual(0, post_refs.old_references)
            self.assertEqual(3, post_refs.new_references)

            conservation = module.chronological_media_conservation(root.resolve(), file_plan)
            self.assertEqual(2, conservation.before_count)
            self.assertEqual(conservation.before_count, conservation.after_count)
            self.assertEqual(conservation.before_bytes, conservation.after_bytes)

    def test_chronological_plan_refuses_target_conflicts(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            season = root / "主播" / "Season 01"
            season.mkdir(parents=True)
            video = season / "主播.S01E0001.2026-06-12 - 场次.mp4"
            video.write_bytes(b"video")
            video.with_suffix(".subtitle.json").write_text(
                '{"record_meta":{"start_time":"2026-06-12T19:00:00.123456+08:00"}}',
                encoding="utf-8",
            )
            module = self.load_script_module()
            episode = next(item for item in module.collect_episodes(root.resolve()) if item.episode == 1)
            identity = module.chronological_episode_identity(module.episode_recorded_at(episode))
            conflict = season / f"主播.S01E{identity:04d}.2026-06-12 - 场次.mp4"
            conflict.write_bytes(b"user-file")
            with self.assertRaisesRegex(ValueError, "target conflict"):
                module.plan_chronological_episode_renumber(
                    root.resolve(),
                    [episode],
                )

    def test_chronological_plan_includes_srt_video_task_references(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            season = root / "伊布讲AI" / "Season 01"
            season.mkdir(parents=True)
            video = season / "伊布讲AI.S01E0024.2026-08-14 - Seedance2.5 实战答疑现场.mp4"
            video.write_bytes(b"video")
            video.with_suffix(".subtitle.json").write_text(
                '{"record_meta":{"start_time":"2026-08-14T23:30:00.123456+08:00"}}',
                encoding="utf-8",
            )
            video.with_suffix(".nfo").write_text(
                "<episodedetails><episode>24</episode><aired>2026-08-14</aired></episodedetails>",
                encoding="utf-8",
            )
            srt_video = root.parent / "srt_video" / "伊布讲AI"
            srt_video.mkdir(parents=True)
            task_sidecar = srt_video / "task-1183.subtitle.json"
            task_sidecar.write_text(
                json.dumps({"output_path": str(video.resolve())}),
                encoding="utf-8",
            )

            result = self.run_script(root, "--plan-chronological-renumber")

            self.assertEqual(0, result.returncode, result.stderr)
            self.assertIn(f"path={task_sidecar.resolve()} pointer=/output_path", result.stdout)
            self.assertIn("references=1", result.stdout)

    def test_chronological_plan_refuses_missing_episode_nfo(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            season = root / "主播" / "Season 01"
            season.mkdir(parents=True)
            video = season / "主播.S01E0001.2026-06-12 - 场次.mp4"
            video.write_bytes(b"video")
            video.with_suffix(".subtitle.json").write_text(
                '{"record_meta":{"start_time":"2026-06-12T19:00:00.123456+08:00"}}',
                encoding="utf-8",
            )
            module = self.load_script_module()
            plan = module.plan_chronological_episode_renumber(root.resolve(), module.collect_episodes(root.resolve()))
            with self.assertRaisesRegex(ValueError, "missing episode NFO"):
                module.plan_chronological_nfo_edits(plan)

    def test_chronological_plan_allows_exact_time_ties_without_reversing_time(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            season = root / "主播" / "Season 01"
            season.mkdir(parents=True)
            for episode, title in ((9, "同场上半段"), (2, "同场下半段")):
                video = season / f"主播.S01E{episode:04d}.2026-06-12 - {title}.mp4"
                video.write_bytes(title.encode())
                video.with_suffix(".subtitle.json").write_text(
                    '{"record_meta":{"start_time":"2026-06-12T19:00:00.123456+08:00"}}',
                    encoding="utf-8",
                )

            module = self.load_script_module()
            plan = module.plan_chronological_episode_renumber(root.resolve(), module.collect_episodes(root.resolve()))

            self.assertEqual(2, len(plan))
            self.assertEqual(2, len({item.target for item in plan}))
            ordered = sorted(plan, key=lambda item: item.source)
            self.assertNotEqual(ordered[0].episode, ordered[1].episode)
            self.assertEqual(1, abs(ordered[0].episode - ordered[1].episode))

    def test_chronological_identity_uses_explicit_plus_eight_fallback_and_safe_limit(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            season = root / "主播" / "Season 01"
            season.mkdir(parents=True)
            video = season / "主播.S01E0001.2026-06-12 - 场次.mp4"
            video.write_bytes(b"video")
            video.with_suffix(".subtitle.json").write_text(
                json.dumps({"source_path": "/source/主播 - 2026-06-12 19-00-00 - 场次.mp4"}),
                encoding="utf-8",
            )

            module = self.load_script_module()
            episode = module.collect_episodes(root.resolve())[0]
            recorded_at = module.episode_recorded_at(episode)
            self.assertEqual("2026-06-12T19:00:00+08:00", recorded_at.isoformat())
            with self.assertRaisesRegex(ValueError, "safe episode identity range"):
                module.chronological_episode_identity(
                    module.datetime(2056, 1, 1, tzinfo=module.timezone.utc)
                )

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
            self.assertEqual(b"cover", (root / "主播" / "poster.jpg").read_bytes())
            self.assertIn(
                '<thumb aspect="poster">poster.jpg</thumb>',
                (root / "主播" / "tvshow.nfo").read_text(encoding="utf-8"),
            )

            show_nfo = root / "主播" / "tvshow.nfo"
            custom_nfo = show_nfo.read_text(encoding="utf-8").replace(
                '  <thumb aspect="poster">poster.jpg</thumb>\n',
                "  <custom>保留字段</custom>\n",
            )
            custom_nfo = custom_nfo.replace("<year>2026</year>", "<year>2025</year>")
            custom_nfo = custom_nfo.replace("<studio>bililive-go</studio>", "<studio>用户平台</studio>")
            show_nfo.write_text(custom_nfo, encoding="utf-8")
            (root / "主播" / "poster.jpg").write_bytes(b"")
            repaired_empty = self.run_script(root, "--apply", env={"FFMPEG_BIN": str(self.fake_ffmpeg(tmp))})
            self.assertEqual(0, repaired_empty.returncode, repaired_empty.stderr)
            self.assertEqual(b"cover", (root / "主播" / "poster.jpg").read_bytes())
            repaired_nfo = show_nfo.read_text(encoding="utf-8")
            self.assertIn("<custom>保留字段</custom>", repaired_nfo)
            self.assertIn('<thumb aspect="poster">poster.jpg</thumb>', repaired_nfo)
            self.assertIn("<year>2025</year>", repaired_nfo)
            self.assertIn("<studio>用户平台</studio>", repaired_nfo)

            show_nfo.write_text(
                "<tvshow>\n"
                "<title>主播</title>\n"
                "<showtitle>主播</showtitle>\n"
                "<plot>未闭合\n"
                "<custom>损坏文件</custom>\n"
                "</tvshow>\n",
                encoding="utf-8",
            )
            repaired_malformed = self.run_script(root, "--apply", env={"FFMPEG_BIN": str(self.fake_ffmpeg(tmp))})
            self.assertEqual(0, repaired_malformed.returncode, repaired_malformed.stderr)
            rebuilt_nfo = show_nfo.read_text(encoding="utf-8")
            self.assertIn("<tvshow>", rebuilt_nfo)
            self.assertIn("</tvshow>", rebuilt_nfo)
            self.assertNotIn("<custom>损坏文件</custom>", rebuilt_nfo)

            (root / "主播" / "poster.jpg").write_bytes(b"curated-poster")
            repeated = self.run_script(root, "--apply", env={"FFMPEG_BIN": str(self.fake_ffmpeg(tmp))})
            self.assertEqual(0, repeated.returncode, repeated.stderr)
            self.assertEqual(b"curated-poster", (root / "主播" / "poster.jpg").read_bytes())
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
            canonical_video.with_suffix(".jpg").write_bytes(b"canonical-cover")
            duplicate_video.with_suffix(".jpg").write_bytes(b"duplicate-cover")
            duplicate_video.with_suffix(".nfo").write_text("<episodedetails><title>错误绑定</title></episodedetails>", encoding="utf-8")
            (duplicate.parent / "poster.jpg").write_bytes(b"curated-duplicate-poster")
            (duplicate.parent / "tvshow.nfo").write_text(
                "<tvshow>\n"
                "  <title>天津蛋哥 6点说车</title>\n"
                "  <showtitle>天津蛋哥 6点说车</showtitle>\n"
                "  <year>2026</year>\n"
                "  <studio>抖音</studio>\n"
                "  <custom>保留合集字段</custom>\n"
                "</tvshow>\n",
                encoding="utf-8",
            )

            result = self.run_script(root, "--apply", "--merge-duplicate-shows")

            self.assertEqual(0, result.returncode, result.stderr)
            self.assertIn("quarantined_files=3", result.stdout)
            self.assertTrue(canonical_video.exists())
            self.assertFalse(any(root.rglob(".quarantine-library-sidecars")))
            quarantine_root = root.parent / ".video-quarantine-library-sidecars"
            self.assertTrue(any(quarantine_root.rglob("*.mp4")))
            self.assertTrue(any(quarantine_root.rglob("*.jpg")))
            self.assertTrue(any(quarantine_root.rglob("*.nfo")))
            self.assertNotIn("错误绑定", canonical_video.with_suffix(".nfo").read_text(encoding="utf-8"))
            self.assertEqual(b"curated-duplicate-poster", (canonical.parent / "poster.jpg").read_bytes())
            canonical_nfo = (canonical.parent / "tvshow.nfo").read_text(encoding="utf-8")
            self.assertIn("<custom>保留合集字段</custom>", canonical_nfo)
            self.assertIn('<thumb aspect="poster">poster.jpg</thumb>', canonical_nfo)

    def test_duplicate_show_merge_refuses_conflicting_posters(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            canonical = root / "天津蛋哥 6点说车" / "Season 01"
            first = root / "天津蛋哥  6点说车" / "Season 01"
            second = root / "天津蛋哥   6点说车" / "Season 01"
            canonical.mkdir(parents=True)
            first.mkdir(parents=True)
            second.mkdir(parents=True)
            canonical_video = canonical / "天津蛋哥 6点说车.S01E0003.2026-06-14 - 说车.mp4"
            first_video = first / "天津蛋哥 6点说车.S01E0001.2026-06-12 - 说车.mp4"
            second_video = second / "天津蛋哥 6点说车.S01E0002.2026-06-13 - 说车.mp4"
            canonical_video.write_bytes(b"canonical")
            first_video.write_bytes(b"first")
            second_video.write_bytes(b"second")
            canonical_video.with_suffix(".jpg").write_bytes(b"canonical-cover")
            first_video.with_suffix(".jpg").write_bytes(b"first-cover")
            second_video.with_suffix(".jpg").write_bytes(b"second-cover")
            (canonical.parent / "poster.jpg").write_bytes(b"canonical-poster")
            (first.parent / "poster.jpg").write_bytes(b"poster-one")
            (second.parent / "poster.jpg").write_bytes(b"poster-two")

            result = self.run_script(root, "--apply", "--merge-duplicate-shows")

            self.assertEqual(3, result.returncode)
            self.assertIn("show_file_conflicts=1", result.stdout)
            self.assertTrue(canonical_video.exists())
            self.assertTrue(first_video.exists())
            self.assertTrue(second_video.exists())
            self.assertEqual(b"canonical-poster", (canonical.parent / "poster.jpg").read_bytes())

    def test_duplicate_episode_group_rolls_back_when_second_move_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            source_dir = root / "主播  " / "Season 01"
            target_dir = root / "主播" / "Season 01"
            source_dir.mkdir(parents=True)
            target_dir.mkdir(parents=True)
            sources = [source_dir / "episode.mp4", source_dir / "episode.nfo", source_dir / "episode.jpg"]
            targets = [target_dir / path.name for path in sources]
            for index, source in enumerate(sources):
                source.write_bytes(f"source-{index}".encode())

            module = self.load_script_module()
            original_move_path = module.move_path
            calls = 0

            def fail_second_move(source: Path, target: Path) -> None:
                nonlocal calls
                calls += 1
                if calls == 2:
                    raise OSError("injected second move failure")
                original_move_path(source, target)

            module.move_path = fail_second_move
            with self.assertRaisesRegex(OSError, "injected second move failure"):
                module.execute_episode_moves(list(zip(sources, targets)), root.resolve(), True)

            for index, source in enumerate(sources):
                self.assertEqual(f"source-{index}".encode(), source.read_bytes())
            for target in targets:
                self.assertFalse(target.exists())
            transaction_root = root.parent / ".video-sidecar-transactions"
            self.assertFalse(any(transaction_root.glob("episode-*")))

    def test_move_path_restores_source_when_post_unlink_fsync_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir)
            source_dir = root / "source"
            target_dir = root / "target"
            source_dir.mkdir()
            target_dir.mkdir()
            source = source_dir / "episode.mp4"
            target = target_dir / "episode.mp4"
            source.write_bytes(b"source")

            module = self.load_script_module()
            original_fsync_directory = module.fsync_directory

            def fail_source_sync(path: Path) -> None:
                if path == source_dir and not source.exists():
                    raise OSError("injected source directory fsync failure")
                original_fsync_directory(path)

            module.fsync_directory = fail_source_sync
            with self.assertRaisesRegex(OSError, "injected source directory fsync failure"):
                module.move_path(source, target)

            self.assertEqual(b"source", source.read_bytes())
            self.assertFalse(target.exists())

    def test_move_path_persists_target_before_unlinking_source(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir)
            source_dir = root / "source"
            target_dir = root / "target"
            source_dir.mkdir()
            target_dir.mkdir()
            source = source_dir / "episode.mp4"
            target = target_dir / "episode.mp4"
            source.write_bytes(b"source")

            module = self.load_script_module()
            original_fsync_directory = module.fsync_directory
            observations: list[tuple[Path, bool, bool]] = []

            def observe_sync(path: Path) -> None:
                observations.append((path, source.exists(), target.exists()))
                original_fsync_directory(path)

            module.fsync_directory = observe_sync
            module.move_path(source, target)

            self.assertGreaterEqual(len(observations), 2)
            self.assertEqual((target_dir, True, True), observations[0])
            self.assertEqual((source_dir, False, True), observations[1])

    def test_move_path_removes_new_target_when_target_fsync_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir)
            source_dir = root / "source"
            target_dir = root / "target"
            source_dir.mkdir()
            target_dir.mkdir()
            source = source_dir / "episode.mp4"
            target = target_dir / "episode.mp4"
            source.write_bytes(b"source")

            module = self.load_script_module()
            original_fsync_directory = module.fsync_directory
            failed = False

            def fail_first_target_sync(path: Path) -> None:
                nonlocal failed
                if path == target_dir and not failed:
                    failed = True
                    raise OSError("injected target directory fsync failure")
                original_fsync_directory(path)

            module.fsync_directory = fail_first_target_sync
            with self.assertRaisesRegex(OSError, "injected target directory fsync failure"):
                module.move_path(source, target)

            self.assertEqual(b"source", source.read_bytes())
            self.assertFalse(target.exists())

    def test_move_path_reports_cleanup_failure_when_source_unlink_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir)
            source = root / "source.mp4"
            target = root / "target.mp4"
            source.write_bytes(b"source")

            module = self.load_script_module()
            original_unlink = Path.unlink

            def fail_source_and_target_unlink(path: Path, *args: object, **kwargs: object) -> None:
                if path == source:
                    raise OSError("injected source unlink failure")
                if path == target:
                    raise OSError("injected target cleanup failure")
                original_unlink(path, *args, **kwargs)

            Path.unlink = fail_source_and_target_unlink
            try:
                with self.assertRaisesRegex(RuntimeError, "target cleanup failed"):
                    module.move_path(source, target)
            finally:
                Path.unlink = original_unlink

            self.assertEqual(b"source", source.read_bytes())
            self.assertEqual(b"source", target.read_bytes())

    def test_relative_root_quarantine_stays_outside_media_library(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir) / "video"
            canonical = root / "主播" / "Season 01"
            duplicate = root / "主播  " / "Season 01"
            canonical.mkdir(parents=True)
            duplicate.mkdir(parents=True)
            canonical_video = canonical / "主播.S01E0001.2026-06-12 - 直播.mp4"
            duplicate_video = duplicate / "主播.S01E0001.2026-06-12 - 直播.mp4"
            canonical_video.write_bytes(b"canonical")
            duplicate_video.write_bytes(b"duplicate")
            canonical_video.with_suffix(".jpg").write_bytes(b"canonical-cover")
            duplicate_video.with_suffix(".jpg").write_bytes(b"duplicate-cover")

            result = self.run_script(Path("."), "--apply", "--merge-duplicate-shows", cwd=root)

            self.assertEqual(0, result.returncode, result.stderr)
            self.assertFalse(duplicate_video.exists())
            self.assertFalse(any(path for path in root.rglob("*.mp4") if path != canonical_video))
            quarantine_root = root.parent / ".video-quarantine-library-sidecars"
            self.assertTrue(any(quarantine_root.rglob("*.mp4")))


if __name__ == "__main__":
    unittest.main()
