import os
import pathlib
import sys
import tempfile
import unittest
from unittest import mock


ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from worker_core import (
    _transcribe_with_chain,
    burn_subtitles,
    build_public_file_url,
    check_mac_transcriber_health,
    create_dashscope_session,
    dashscope_result_to_segments,
    ms_to_srt_time,
    normalize_dashscope_base_url,
    run_dashscope_transcription,
    run_remote_mac_mlx,
    segments_to_api_payload,
    transcribe_and_burn,
    upload_file_to_dashscope_oss,
    segments_to_srt,
    wait_for_output_file,
)


class WorkerCoreTest(unittest.TestCase):
    def _run_transcribe_and_burn(self) -> tuple[dict[str, object], list[list[str]], pathlib.Path, pathlib.Path]:
        commands: list[list[str]] = []
        temp_dir = tempfile.TemporaryDirectory()
        temp_root = pathlib.Path(temp_dir.name)
        source_path = temp_root / "source.mp4"
        source_path.write_bytes(b"fake-video")
        output_video_path = temp_root / "library" / "episode.mp4"
        output_srt_path = temp_root / "library" / "episode.srt"
        output_ass_path = temp_root / "library" / "episode.ass"

        def fake_run(cmd, **_kwargs):
            commands.append(cmd)
            pathlib.Path(cmd[-1]).write_bytes(b"rendered-video")
            return mock.Mock(returncode=0)

        with mock.patch("worker_core.extract_audio"), \
            mock.patch(
                "worker_core.run_local_whisper",
                return_value=[
                    {"index": 1, "start_ms": 0, "end_ms": 1800, "text": "第一句字幕"},
                    {"index": 2, "start_ms": 1800, "end_ms": 3200, "text": "第二句字幕"},
                ],
            ), \
            mock.patch("worker_core.subprocess.run", side_effect=fake_run):
            result = transcribe_and_burn(
                source_path=str(source_path),
                output_video_path=str(output_video_path),
                output_srt_path=str(output_srt_path),
                provider="local-whisper",
                language="zh",
                burn_style={
                    "preset": "vizard_classic_cn",
                    "font_name": "Noto Sans CJK SC",
                    "font_size": 24,
                    "margin_v": 24,
                    "outline": 2,
                    "shadow": 0,
                },
            )

        self.addCleanup(temp_dir.cleanup)
        return result, commands, output_srt_path, output_ass_path

    def test_ms_to_srt_time(self):
        self.assertEqual("00:00:00,000", ms_to_srt_time(0))
        self.assertEqual("00:01:01,042", ms_to_srt_time(61042))

    def test_segments_to_srt(self):
        srt = segments_to_srt([
            {"index": 1, "start_ms": 0, "end_ms": 1800, "text": "第一句"},
            {"index": 2, "start_ms": 1800, "end_ms": 3200, "text": "第二句"},
        ])

        self.assertIn("1\n00:00:00,000 --> 00:00:01,800\n第一句", srt)
        self.assertIn("2\n00:00:01,800 --> 00:00:03,200\n第二句", srt)

    def test_segments_to_api_payload(self):
        payload = segments_to_api_payload([
            {"index": 1, "start_ms": 0, "end_ms": 1800, "text": "第一句"},
            {"index": 2, "start_ms": 1800, "end_ms": 3200, "text": "第二句"},
        ])

        self.assertEqual(
            [
                {"index": 1, "start": "00:00:00,000", "end": "00:00:01,800", "text": "第一句"},
                {"index": 2, "start": "00:00:01,800", "end": "00:00:03,200", "text": "第二句"},
            ],
            payload,
        )

    def test_transcribe_and_burn_returns_segments_with_render_preset(self):
        result, _, output_srt_path, output_ass_path = self._run_transcribe_and_burn()

        self.assertEqual(
            [
                {"index": 1, "start": "00:00:00,000", "end": "00:00:01,800", "text": "第一句字幕"},
                {"index": 2, "start": "00:00:01,800", "end": "00:00:03,200", "text": "第二句字幕"},
            ],
            result["segments"],
        )
        self.assertEqual("vizard_classic_cn", result.get("render_preset"))
        self.assertEqual(str(output_ass_path), result.get("ass_path"))
        self.assertEqual("local-whisper", result.get("actual_provider"),
                         "actual_provider 必须暴露给 NAS 主进程，让 .subtitle.json 记录实际转写者")
        self.assertTrue(output_srt_path.exists())
        self.assertTrue(output_ass_path.exists())

    def test_transcribe_and_burn_uses_ass_libass_filter(self):
        _, commands, _, _ = self._run_transcribe_and_burn()

        ffmpeg_command = " ".join(commands[-1])
        self.assertIn("subtitles=", ffmpeg_command)
        self.assertIn("episode.ass", ffmpeg_command)
        self.assertNotIn("force_style", ffmpeg_command)
        self.assertNotIn("-filter_complex", ffmpeg_command)

    def test_transcribe_and_burn_no_longer_creates_png_overlay_assets(self):
        _, commands, output_srt_path, _ = self._run_transcribe_and_burn()

        cue_dir = output_srt_path.parent / ".subtitle-tmp"
        self.assertFalse(cue_dir.exists())

        ffmpeg_command = commands[-1]
        self.assertIn("-vf", ffmpeg_command)
        self.assertNotIn("-filter_complex", ffmpeg_command)

    def test_burn_subtitles_places_temp_output_in_dedicated_per_run_directory(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_root = pathlib.Path(temp_dir)
            input_path = temp_root / "input.mp4"
            ass_path = temp_root / "episode.ass"
            output_path = temp_root / "library" / "episode.mp4"
            input_path.write_bytes(b"video")
            ass_path.write_text("ass", encoding="utf-8")
            temp_outputs: list[pathlib.Path] = []
            temp_dirs: list[pathlib.Path] = []

            def fake_run(cmd, **_kwargs):
                temp_output = pathlib.Path(cmd[-1])
                temp_outputs.append(temp_output)
                temp_dirs.append(temp_output.parent)
                self.assertEqual(output_path.suffix, temp_output.suffix)
                self.assertTrue(temp_output.parent.name.startswith("subtitle-burn-"))
                self.assertNotEqual(output_path.parent, temp_output.parent)
                self.assertFalse(temp_output.exists())
                temp_output.write_bytes(b"rendered-video")
                return mock.Mock(returncode=0)

            with mock.patch("worker_core.subprocess.run", side_effect=fake_run):
                render_preset = burn_subtitles(
                    input_path=str(input_path),
                    ass_path=str(ass_path),
                    output_path=str(output_path),
                    style={"preset": "vizard_classic_cn"},
                )

            self.assertEqual("vizard_classic_cn", render_preset)
            self.assertEqual(b"rendered-video", output_path.read_bytes())
            self.assertEqual(1, len(temp_outputs))
            self.assertFalse(temp_outputs[0].exists())
            self.assertFalse(temp_dirs[0].exists())
            self.assertFalse((output_path.parent / ".subtitle-tmp").exists())

    def test_burn_subtitles_cleans_up_temp_directory_when_ffmpeg_does_not_create_output(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_root = pathlib.Path(temp_dir)
            input_path = temp_root / "input.mp4"
            ass_path = temp_root / "episode.ass"
            output_path = temp_root / "library" / "episode.mp4"
            input_path.write_bytes(b"video")
            ass_path.write_text("ass", encoding="utf-8")

            with mock.patch("worker_core.subprocess.run", return_value=mock.Mock(returncode=0, stderr="")) as run_mock, \
                mock.patch("worker_core.wait_for_output_file", return_value=False):
                with self.assertRaisesRegex(RuntimeError, "subtitle finalize failed: burned output was not visible after ffmpeg exited"):
                    burn_subtitles(
                        input_path=str(input_path),
                        ass_path=str(ass_path),
                        output_path=str(output_path),
                        style={"preset": "vizard_classic_cn"},
                    )

            temp_output = pathlib.Path(run_mock.call_args.args[0][-1])
            self.assertTrue(temp_output.parent.name.startswith("subtitle-burn-"))
            self.assertFalse(temp_output.parent.exists())
            self.assertFalse((output_path.parent / ".subtitle-tmp").exists())

    def test_burn_subtitles_reports_finalize_error_when_publish_replace_fails(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_root = pathlib.Path(temp_dir)
            input_path = temp_root / "input.mp4"
            ass_path = temp_root / "episode.ass"
            output_path = temp_root / "library" / "episode.mp4"
            input_path.write_bytes(b"video")
            ass_path.write_text("ass", encoding="utf-8")

            def fake_run(cmd, **_kwargs):
                pathlib.Path(cmd[-1]).write_bytes(b"rendered-video")
                return mock.Mock(returncode=0, stderr="encode ok")

            with mock.patch("worker_core.subprocess.run", side_effect=fake_run), \
                mock.patch("worker_core.os.replace", side_effect=OSError("Device or resource busy")), \
                mock.patch("worker_core.time.sleep"):
                with self.assertRaisesRegex(RuntimeError, "subtitle finalize failed: publish burned output"):
                    burn_subtitles(
                        input_path=str(input_path),
                        ass_path=str(ass_path),
                        output_path=str(output_path),
                        style={"preset": "vizard_classic_cn"},
                    )

            self.assertFalse(output_path.exists())

    def test_burn_subtitles_waits_briefly_for_output_to_appear_after_ffmpeg_exits(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_root = pathlib.Path(temp_dir)
            input_path = temp_root / "input.mp4"
            ass_path = temp_root / "episode.ass"
            output_path = temp_root / "library" / "episode.mp4"
            input_path.write_bytes(b"video")
            ass_path.write_text("ass", encoding="utf-8")
            temp_output_holder: dict[str, pathlib.Path] = {}
            delayed_checks = 0
            real_exists = pathlib.Path.exists
            real_path_exists = os.path.exists

            def fake_run(cmd, **_kwargs):
                temp_output_holder["path"] = pathlib.Path(cmd[-1])
                return mock.Mock(returncode=0, stderr="")

            def fake_exists(path):
                nonlocal delayed_checks
                target = temp_output_holder.get("path")
                if target is not None and path == str(target):
                    delayed_checks += 1
                    if delayed_checks == 3:
                        target.write_bytes(b"rendered-video")
                    return real_exists(target)
                return real_path_exists(path)

            with mock.patch("worker_core.subprocess.run", side_effect=fake_run), \
                mock.patch("worker_core.os.path.exists", side_effect=fake_exists), \
                mock.patch("worker_core.time.sleep"):
                render_preset = burn_subtitles(
                    input_path=str(input_path),
                    ass_path=str(ass_path),
                    output_path=str(output_path),
                    style={"preset": "vizard_classic_cn"},
                )

            self.assertEqual("vizard_classic_cn", render_preset)
            self.assertEqual(b"rendered-video", output_path.read_bytes())
            self.assertGreaterEqual(delayed_checks, 4)

    def test_burn_subtitles_builds_vaapi_command_when_enabled(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_root = pathlib.Path(temp_dir)
            input_path = temp_root / "input.mp4"
            ass_path = temp_root / "episode.ass"
            output_path = temp_root / "library" / "episode.mp4"
            input_path.write_bytes(b"video")
            ass_path.write_text("ass", encoding="utf-8")
            captured_cmd: list[str] = []

            def fake_run(cmd, **_kwargs):
                captured_cmd[:] = cmd
                pathlib.Path(cmd[-1]).write_bytes(b"rendered-video")
                return mock.Mock(returncode=0, stderr="")

            with mock.patch("worker_core.subprocess.run", side_effect=fake_run):
                burn_subtitles(
                    input_path=str(input_path),
                    ass_path=str(ass_path),
                    output_path=str(output_path),
                    style={"preset": "vizard_classic_cn"},
                    video_encoder="vaapi",
                    vaapi_device="/dev/dri/renderD128",
                    vaapi_qp=21,
                )

            self.assertIn("-vaapi_device", captured_cmd)
            self.assertIn("/dev/dri/renderD128", captured_cmd)
            self.assertIn("-c:v", captured_cmd)
            self.assertIn("h264_vaapi", captured_cmd)
            self.assertIn("-qp", captured_cmd)
            self.assertIn("21", captured_cmd)
            filter_arg = captured_cmd[captured_cmd.index("-vf") + 1]
            self.assertIn("subtitles=", filter_arg)
            self.assertIn("format=nv12,hwupload", filter_arg)

    def test_burn_subtitles_rejects_unknown_video_encoder(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_root = pathlib.Path(temp_dir)
            input_path = temp_root / "input.mp4"
            ass_path = temp_root / "episode.ass"
            output_path = temp_root / "library" / "episode.mp4"
            input_path.write_bytes(b"video")
            ass_path.write_text("ass", encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "不支持的视频编码模式"):
                burn_subtitles(
                    input_path=str(input_path),
                    ass_path=str(ass_path),
                    output_path=str(output_path),
                    style={"preset": "vizard_classic_cn"},
                    video_encoder="mystery",
                )

    def test_burn_subtitles_reports_finalize_error_when_output_visibility_never_stabilizes(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_root = pathlib.Path(temp_dir)
            input_path = temp_root / "input.mp4"
            ass_path = temp_root / "episode.ass"
            output_path = temp_root / "library" / "episode.mp4"
            input_path.write_bytes(b"video")
            ass_path.write_text("ass", encoding="utf-8")

            def fake_run(cmd, **_kwargs):
                return mock.Mock(returncode=0, stderr="encode ok")

            with mock.patch("worker_core.subprocess.run", side_effect=fake_run), \
                mock.patch("worker_core.wait_for_output_file", return_value=False):
                with self.assertRaisesRegex(RuntimeError, "subtitle finalize failed: burned output was not visible after ffmpeg exited"):
                    burn_subtitles(
                        input_path=str(input_path),
                        ass_path=str(ass_path),
                        output_path=str(output_path),
                        style={"preset": "vizard_classic_cn"},
                    )

            self.assertFalse(output_path.exists())

    def test_wait_for_output_file_requires_size_to_stabilize(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            output_path = pathlib.Path(temp_dir) / "episode.mp4"
            sizes = [None, 128, 256, 256]

            def fake_size(_path):
                current = sizes.pop(0)
                if current is None:
                    return None
                output_path.write_bytes(b"x" * current)
                return current

            with mock.patch("worker_core.get_non_empty_file_size", side_effect=fake_size), \
                mock.patch("worker_core.time.sleep"):
                visible = wait_for_output_file(
                    str(output_path),
                    timeout_seconds=5.0,
                    poll_interval_seconds=0.1,
                    stable_polls=2,
                )

            self.assertTrue(visible)

    def test_build_public_file_url(self):
        file_url = build_public_file_url(
            file_path="/srv/bililive-source/主播/audio/test.wav",
            source_root="/srv/bililive-source",
            public_url_base="https://demo.example.com",
        )

        self.assertEqual("https://demo.example.com/files/%E4%B8%BB%E6%92%AD/audio/test.wav", file_url)

    def test_normalize_dashscope_base_url(self):
        self.assertEqual("https://dashscope.aliyuncs.com", normalize_dashscope_base_url("https://dashscope.aliyuncs.com/compatible-mode/v1"))
        self.assertEqual("https://dashscope.aliyuncs.com", normalize_dashscope_base_url("https://dashscope.aliyuncs.com/api/v1"))

    def test_dashscope_result_to_segments(self):
        payload = {
            "transcripts": [
                {
                    "channel_id": 0,
                    "text": "第一句第二句",
                    "sentences": [
                        {
                            "sentence_id": 0,
                            "begin_time": 0,
                            "end_time": 1560,
                            "text": "第一句",
                        },
                        {
                            "sentence_id": 1,
                            "begin_time": 1560,
                            "end_time": 3120,
                            "text": "第二句",
                        },
                    ],
                }
            ]
        }

        segments = dashscope_result_to_segments(payload)

        self.assertEqual(2, len(segments))
        self.assertEqual("第一句", segments[0]["text"])
        self.assertEqual(0, segments[0]["start_ms"])
        self.assertEqual(3120, segments[1]["end_ms"])

    def test_dashscope_result_to_segments_preserves_word_timestamps_as_tokens(self):
        payload = {
            "transcripts": [
                {
                    "channel_id": 0,
                    "text": "有问题吗？告诉我一声。",
                    "sentences": [
                        {
                            "sentence_id": 0,
                            "begin_time": 2700,
                            "end_time": 8276,
                            "text": "有问题吗？告诉我一声。",
                            "words": [
                                {"begin_time": 2700, "end_time": 3180, "text": "有", "punctuation": ""},
                                {"begin_time": 3180, "end_time": 3600, "text": "问题", "punctuation": "吗？"},
                                {"begin_time": 5178, "end_time": 5660, "text": "告诉", "punctuation": ""},
                                {"begin_time": 6417, "end_time": 7000, "text": "我一声", "punctuation": "。"},
                            ],
                        }
                    ],
                }
            ]
        }

        segments = dashscope_result_to_segments(payload)

        self.assertEqual(1, len(segments))
        self.assertEqual("有问题吗？告诉我一声。", segments[0]["text"])
        self.assertEqual(
            [
                {"text": "有", "start_ms": 2700, "end_ms": 3180},
                {"text": "问题吗？", "start_ms": 3180, "end_ms": 3600},
                {"text": "告诉", "start_ms": 5178, "end_ms": 5660},
                {"text": "我一声。", "start_ms": 6417, "end_ms": 7000},
            ],
            segments[0]["tokens"],
        )

    @mock.patch("worker_core.time.sleep")
    @mock.patch("worker_core.create_dashscope_session")
    def test_run_dashscope_transcription_requests_word_timestamps(self, mock_create_session, _mock_sleep):
        mock_session = mock.Mock()
        mock_create_session.return_value = mock_session
        mock_session.post.return_value = mock.Mock(
            raise_for_status=mock.Mock(),
            json=mock.Mock(return_value={"output": {"task_id": "task-1"}}),
        )
        mock_session.get.side_effect = [
            mock.Mock(
                raise_for_status=mock.Mock(),
                json=mock.Mock(return_value={"output": {"task_status": "SUCCEEDED", "result": {"transcription_url": "https://result"}}}),
            ),
            mock.Mock(
                raise_for_status=mock.Mock(),
                json=mock.Mock(
                    return_value={
                        "transcripts": [
                            {
                                "sentences": [
                                    {
                                        "sentence_id": 0,
                                        "begin_time": 0,
                                        "end_time": 1560,
                                        "text": "第一句",
                                        "words": [
                                            {"begin_time": 0, "end_time": 780, "text": "第一", "punctuation": ""},
                                            {"begin_time": 780, "end_time": 1560, "text": "句", "punctuation": ""},
                                        ],
                                    }
                                ]
                            }
                        ]
                    }
                ),
            ),
        ]

        segments = run_dashscope_transcription(
            "oss://demo/audio.wav",
            "zh",
            "test-api-key",
            "qwen3-asr-flash-filetrans",
            "https://dashscope.aliyuncs.com/compatible-mode/v1",
            resolve_oss_resource=True,
        )

        _, kwargs = mock_session.post.call_args
        self.assertTrue(kwargs["json"]["parameters"]["enable_words"])
        self.assertEqual(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 1560,
                    "text": "第一句",
                    "tokens": [
                        {"text": "第一", "start_ms": 0, "end_ms": 780},
                        {"text": "句", "start_ms": 780, "end_ms": 1560},
                    ],
                }
            ],
            segments,
        )

    @mock.patch("worker_core.time.sleep")
    @mock.patch("worker_core.create_dashscope_session")
    def test_run_dashscope_transcription_treats_no_valid_fragment_as_empty_result(self, mock_create_session, _mock_sleep):
        mock_session = mock.Mock()
        mock_create_session.return_value = mock_session
        mock_session.post.return_value = mock.Mock(
            raise_for_status=mock.Mock(),
            json=mock.Mock(return_value={"output": {"task_id": "task-1"}}),
        )
        mock_session.get.return_value = mock.Mock(
            raise_for_status=mock.Mock(),
            json=mock.Mock(
                return_value={
                    "request_id": "req-1",
                    "output": {
                        "task_id": "task-1",
                        "task_status": "FAILED",
                        "code": "SUCCESS_WITH_NO_VALID_FRAGMENT",
                        "message": "SUCCESS_WITH_NO_VALID_FRAGMENT",
                    },
                }
            ),
        )

        segments = run_dashscope_transcription(
            "oss://demo/audio.wav",
            "zh",
            "test-api-key",
            "qwen3-asr-flash-filetrans",
            "https://dashscope.aliyuncs.com/compatible-mode/v1",
        )

        self.assertEqual([], segments)

    def test_create_dashscope_session_disables_env_proxy(self):
        session = create_dashscope_session()

        self.assertFalse(session.trust_env)

    @mock.patch("worker_core.create_dashscope_session")
    def test_upload_file_to_dashscope_oss(self, mock_create_session):
        mock_session = mock.Mock()
        mock_session.get.return_value = mock.Mock(
            raise_for_status=mock.Mock(),
            json=mock.Mock(
                return_value={
                    "data": {
                        "policy": "policy-token",
                        "signature": "signature-token",
                        "upload_dir": "dashscope-instant/job-1",
                        "upload_host": "https://dashscope-file-mgr.oss-cn-beijing.aliyuncs.com",
                        "oss_access_key_id": "access-key-id",
                        "x_oss_object_acl": "private",
                        "x_oss_forbid_overwrite": "true",
                    }
                }
            ),
        )
        mock_session.post.return_value = mock.Mock(raise_for_status=mock.Mock())
        mock_create_session.return_value = mock_session

        with tempfile.TemporaryDirectory() as temp_dir:
            audio_path = pathlib.Path(temp_dir) / "clip.wav"
            audio_path.write_bytes(b"fake-audio")

            oss_url = upload_file_to_dashscope_oss(
                str(audio_path),
                api_key="test-api-key",
                model="qwen3-asr-flash-filetrans",
                base_url="https://dashscope.aliyuncs.com/compatible-mode/v1",
            )

        self.assertEqual("oss://dashscope-instant/job-1/clip.wav", oss_url)
        mock_session.get.assert_called_once_with(
            "https://dashscope.aliyuncs.com/api/v1/uploads?action=getPolicy&model=qwen3-asr-flash-filetrans",
            headers={"Authorization": "Bearer test-api-key"},
            timeout=30,
        )
        _, kwargs = mock_session.post.call_args
        self.assertEqual("https://dashscope-file-mgr.oss-cn-beijing.aliyuncs.com", mock_session.post.call_args.args[0])
        self.assertEqual("dashscope-instant/job-1/clip.wav", kwargs["data"]["key"])
        self.assertEqual("policy-token", kwargs["data"]["policy"])
        self.assertEqual("signature-token", kwargs["data"]["Signature"])
        self.assertEqual("access-key-id", kwargs["data"]["OSSAccessKeyId"])
        self.assertEqual("private", kwargs["data"]["x-oss-object-acl"])
        self.assertEqual("true", kwargs["data"]["x-oss-forbid-overwrite"])


class RemoteMacMLXTest(unittest.TestCase):
    """P6.2: NAS worker 通过 HTTP 把音频转给 BiliNote mac_transcriber_service。"""

    def _make_audio_file(self) -> tuple[tempfile.TemporaryDirectory, str]:
        td = tempfile.TemporaryDirectory()
        path = os.path.join(td.name, "clip.wav")
        with open(path, "wb") as f:
            f.write(b"fake-wav-bytes")
        return td, path

    def test_run_remote_mac_mlx_uploads_audio_and_parses_segments(self):
        """正常路径：上传音频到 /transcribe，解析 BiliNote 返回的 segments
        转成 worker 内部的 ms-based 格式。"""
        td, audio_path = self._make_audio_file()
        try:
            mock_response = mock.MagicMock()
            mock_response.status_code = 200
            mock_response.json.return_value = {
                "language": "zh",
                "full_text": "你好世界 第二段",
                "segments": [
                    {"start": 0.0, "end": 1.5, "text": "你好世界"},
                    {"start": 1.5, "end": 3.2, "text": "第二段"},
                ],
            }

            mock_session = mock.MagicMock()
            # __enter__ 自返：with requests.Session() as session 拿到同一 mock，
            # 否则 session.post 调到 __enter__ 默认返回的新 MagicMock 上，
            # mock_session.post.call_args 永远是 None。
            mock_session.__enter__.return_value = mock_session
            mock_session.post.return_value = mock_response

            with mock.patch("worker_core.requests.Session", return_value=mock_session):
                segments = run_remote_mac_mlx(
                    audio_path=audio_path,
                    language="zh",
                    mac_url="http://192.168.1.17:8484",
                    mac_token="secret-token",
                )

            # 验证上传请求
            args, kwargs = mock_session.post.call_args
            self.assertEqual("http://192.168.1.17:8484/transcribe", args[0])
            self.assertEqual("Bearer secret-token", kwargs["headers"]["Authorization"])
            self.assertIn("audio", kwargs["files"])

            # 验证 ms 转换
            self.assertEqual(len(segments), 2)
            self.assertEqual(segments[0]["start_ms"], 0)
            self.assertEqual(segments[0]["end_ms"], 1500)
            self.assertEqual(segments[0]["text"], "你好世界")
            self.assertEqual(segments[1]["start_ms"], 1500)
            self.assertEqual(segments[1]["end_ms"], 3200)
        finally:
            td.cleanup()

    def test_run_remote_mac_mlx_skips_invalid_segments_defensively(self):
        """BiliNote 已修了 NaN 时间戳，但 worker 端再加一道兜底——
        缺字段或解析失败的段直接跳过，不让一条异常段污染整批。"""
        td, audio_path = self._make_audio_file()
        try:
            mock_response = mock.MagicMock()
            mock_response.status_code = 200
            mock_response.json.return_value = {
                "language": "zh",
                "full_text": "正常段",
                "segments": [
                    {"start": 0.0, "end": 1.0, "text": "正常段"},
                    {"start": "not-a-number", "end": 2.0, "text": "异常段"},
                    {"end": 3.0, "text": "缺 start"},
                    {"start": 4.0, "end": 5.0, "text": "   "},  # 空文本也跳过
                ],
            }
            mock_session = mock.MagicMock()
            # __enter__ 自返：with requests.Session() as session 拿到同一 mock，
            # 否则 session.post 调到 __enter__ 默认返回的新 MagicMock 上，
            # mock_session.post.call_args 永远是 None。
            mock_session.__enter__.return_value = mock_session
            mock_session.post.return_value = mock_response

            with mock.patch("worker_core.requests.Session", return_value=mock_session):
                segments = run_remote_mac_mlx(
                    audio_path=audio_path,
                    language="zh",
                    mac_url="http://mac:8484",
                    mac_token=None,
                )

            self.assertEqual(len(segments), 1)
            self.assertEqual(segments[0]["text"], "正常段")
        finally:
            td.cleanup()

    def test_run_remote_mac_mlx_omits_auth_when_token_is_none(self):
        """token 未配（开发模式或本地局域网信任）时不应带 Authorization 头。"""
        td, audio_path = self._make_audio_file()
        try:
            mock_response = mock.MagicMock()
            mock_response.status_code = 200
            mock_response.json.return_value = {"language": "zh", "full_text": "", "segments": []}
            mock_session = mock.MagicMock()
            # __enter__ 自返：with requests.Session() as session 拿到同一 mock，
            # 否则 session.post 调到 __enter__ 默认返回的新 MagicMock 上，
            # mock_session.post.call_args 永远是 None。
            mock_session.__enter__.return_value = mock_session
            mock_session.post.return_value = mock_response

            with mock.patch("worker_core.requests.Session", return_value=mock_session):
                run_remote_mac_mlx(audio_path, "zh", "http://mac:8484", None)

            _, kwargs = mock_session.post.call_args
            self.assertNotIn("Authorization", kwargs["headers"])
        finally:
            td.cleanup()

    def test_check_mac_transcriber_health_returns_true_on_200(self):
        mock_session = mock.MagicMock()
        mock_session.__enter__.return_value = mock_session
        mock_session.get.return_value = mock.MagicMock(status_code=200)
        with mock.patch("worker_core.requests.Session", return_value=mock_session):
            self.assertTrue(check_mac_transcriber_health("http://mac:8484", "tok"))

    def test_check_mac_transcriber_health_returns_false_on_timeout(self):
        import requests

        mock_session = mock.MagicMock()
        mock_session.__enter__.return_value = mock_session
        mock_session.get.side_effect = requests.Timeout("timed out")
        with mock.patch("worker_core.requests.Session", return_value=mock_session):
            self.assertFalse(check_mac_transcriber_health("http://mac:8484", "tok"))

    def test_check_mac_transcriber_health_returns_false_on_5xx(self):
        mock_session = mock.MagicMock()
        mock_session.__enter__.return_value = mock_session
        mock_session.get.return_value = mock.MagicMock(status_code=503)
        with mock.patch("worker_core.requests.Session", return_value=mock_session):
            self.assertFalse(check_mac_transcriber_health("http://mac:8484", "tok"))

    def test_check_mac_transcriber_health_warns_on_401(self):
        """token 错配时 BiliNote /healthz 返 401——应当显式打 WARNING 日志，
        让运维不至于"明明 Mac 在线却始终走云端"摸不着头脑。"""
        mock_session = mock.MagicMock()
        mock_session.__enter__.return_value = mock_session
        mock_session.get.return_value = mock.MagicMock(status_code=401)
        with mock.patch("worker_core.requests.Session", return_value=mock_session), \
             self.assertLogs("worker_core", level="WARNING") as log_cm:
            self.assertFalse(check_mac_transcriber_health("http://mac:8484", "wrong-tok"))
        self.assertTrue(
            any("401" in rec.getMessage() and "token" in rec.getMessage().lower() for rec in log_cm.records),
            f"401 时应打含 'token' 的 WARNING 日志，实际：{[r.getMessage() for r in log_cm.records]}",
        )


class ProviderChainFallbackTest(unittest.TestCase):
    """P6.4: provider chain 按顺序尝试，第一个成功的接管转写。"""

    _BASE_KWARGS = dict(
        source_root=None,
        public_url_base=None,
        dashscope_api_key="key",
        dashscope_base_url="https://dashscope.aliyuncs.com",
        dashscope_model="qwen3-asr-flash-filetrans",
        local_model="small",
        local_compute_type="int8",
        mac_transcriber_url="http://mac:8484",
        mac_transcriber_token="tok",
        mac_transcriber_timeout_seconds=3600,
    )

    def test_chain_uses_first_provider_when_healthy(self):
        with mock.patch("worker_core.check_mac_transcriber_health", return_value=True), \
             mock.patch("worker_core.run_remote_mac_mlx", return_value=[{"index": 1, "start_ms": 0, "end_ms": 1000, "text": "Mac 接管"}]) as mac_mock, \
             mock.patch("worker_core.run_dashscope_transcription") as dash_mock, \
             mock.patch("worker_core.run_local_whisper") as local_mock:
            chosen, segments = _transcribe_with_chain(
                ["remote-mac-mlx", "dashscope", "local-whisper"],
                audio_path="/tmp/a.wav",
                language="zh",
                **self._BASE_KWARGS,
            )

        self.assertEqual("remote-mac-mlx", chosen)
        self.assertEqual("Mac 接管", segments[0]["text"])
        mac_mock.assert_called_once()
        dash_mock.assert_not_called()
        local_mock.assert_not_called()

    def test_chain_skips_unhealthy_mac_falls_back_to_dashscope(self):
        """Mac 离线（healthz 返 False）时不尝试调用 transcribe，直接跳 dashscope。
        关键：避免 mac 离线时浪费 60s+ HTTP timeout，立即降级。"""
        upload_mock = mock.MagicMock(return_value="oss://x.wav")
        with mock.patch("worker_core.check_mac_transcriber_health", return_value=False), \
             mock.patch("worker_core.run_remote_mac_mlx") as mac_mock, \
             mock.patch("worker_core.upload_file_to_dashscope_oss", upload_mock), \
             mock.patch("worker_core.run_dashscope_transcription", return_value=[{"index": 1, "start_ms": 0, "end_ms": 800, "text": "云端兜底"}]):
            chosen, segments = _transcribe_with_chain(
                ["remote-mac-mlx", "dashscope"],
                audio_path="/tmp/a.wav",
                language="zh",
                **self._BASE_KWARGS,
            )

        self.assertEqual("dashscope", chosen)
        self.assertEqual("云端兜底", segments[0]["text"])
        mac_mock.assert_not_called()  # 健康检查失败，不应真正调用 transcribe

    def test_chain_falls_through_on_runtime_exception(self):
        """Mac 健康但 transcribe 期间抛异常（网络中断、模型崩）也要降级。"""
        with mock.patch("worker_core.check_mac_transcriber_health", return_value=True), \
             mock.patch("worker_core.run_remote_mac_mlx", side_effect=RuntimeError("mlx 崩了")), \
             mock.patch("worker_core.run_local_whisper", return_value=[{"index": 1, "start_ms": 0, "end_ms": 500, "text": "本地兜底"}]):
            chosen, segments = _transcribe_with_chain(
                ["remote-mac-mlx", "local-whisper"],
                audio_path="/tmp/a.wav",
                language="zh",
                **self._BASE_KWARGS,
            )

        self.assertEqual("local-whisper", chosen)
        self.assertEqual("本地兜底", segments[0]["text"])

    def test_chain_raises_last_error_when_all_fail(self):
        with mock.patch("worker_core.check_mac_transcriber_health", return_value=True), \
             mock.patch("worker_core.run_remote_mac_mlx", side_effect=RuntimeError("Mac 错")), \
             mock.patch("worker_core.run_local_whisper", side_effect=RuntimeError("本地错")):
            with self.assertRaises(RuntimeError) as ctx:
                _transcribe_with_chain(
                    ["remote-mac-mlx", "local-whisper"],
                    audio_path="/tmp/a.wav",
                    language="zh",
                    **self._BASE_KWARGS,
                )
        self.assertEqual("本地错", str(ctx.exception), "应该抛最后一次的错误")

    def test_chain_raises_clear_error_for_empty_chain(self):
        """空链是配置错（用户配了 provider=auto 但忘了配 SUBTITLE_PROVIDER_CHAIN）。
        必须抛清晰指引的 WorkerSafeError，不要混进通用'全部不可用'。"""
        from worker_core import WorkerSafeError
        with self.assertRaises(WorkerSafeError) as ctx:
            _transcribe_with_chain(
                [],
                audio_path="/tmp/a.wav",
                language="zh",
                **self._BASE_KWARGS,
            )
        self.assertIn("SUBTITLE_PROVIDER_CHAIN", str(ctx.exception))

    def test_chain_records_attempts_in_log(self):
        """Mac 跳过 + dashscope 失败 + local-whisper 成功 → 日志能看到完整尝试链。
        故障定位关键——运维不必从代码翻调用顺序。"""
        with mock.patch("worker_core.check_mac_transcriber_health", return_value=False), \
             mock.patch("worker_core.run_dashscope_transcription", side_effect=RuntimeError("OSS 限流")), \
             mock.patch("worker_core.upload_file_to_dashscope_oss", return_value="oss://x"), \
             mock.patch("worker_core.run_local_whisper", return_value=[{"index": 1, "start_ms": 0, "end_ms": 1000, "text": "本地"}]), \
             self.assertLogs("worker_core", level="INFO") as log_cm:
            chosen, _ = _transcribe_with_chain(
                ["remote-mac-mlx", "dashscope", "local-whisper"],
                audio_path="/tmp/a.wav",
                language="zh",
                **self._BASE_KWARGS,
            )
        self.assertEqual("local-whisper", chosen)
        log_text = "\n".join(rec.getMessage() for rec in log_cm.records)
        self.assertIn("skipped:health", log_text)
        self.assertIn("failed:RuntimeError", log_text)
        self.assertIn("local-whisper", log_text)


if __name__ == "__main__":
    unittest.main()
