import pathlib
import sys
import tempfile
import unittest
from unittest import mock


ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from worker_core import (
    build_burn_temp_dir,
    build_force_style,
    build_public_file_url,
    create_dashscope_session,
    dashscope_result_to_segments,
    ms_to_srt_time,
    normalize_dashscope_base_url,
    segments_to_api_payload,
    transcribe_and_burn,
    upload_file_to_dashscope_oss,
    segments_to_srt,
)


class WorkerCoreTest(unittest.TestCase):
    def _run_transcribe_and_burn(self) -> tuple[dict[str, object], list[list[str]], pathlib.Path]:
        commands: list[list[str]] = []
        temp_dir = tempfile.TemporaryDirectory()
        temp_root = pathlib.Path(temp_dir.name)
        source_path = temp_root / "source.mp4"
        source_path.write_bytes(b"fake-video")
        output_video_path = temp_root / "library" / "episode.mp4"
        output_srt_path = temp_root / "library" / "episode.srt"

        def fake_run(cmd, check, capture_output):
            commands.append(cmd)
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
        return result, commands, output_srt_path

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

    def test_build_force_style(self):
        style = build_force_style({
            "font_name": "Noto Sans CJK SC",
            "font_size": 28,
            "margin_v": 36,
            "outline": 2,
            "shadow": 0,
        })

        self.assertEqual("FontName=Noto Sans CJK SC,FontSize=28,MarginV=36,Outline=2,Shadow=0", style)

    def test_transcribe_and_burn_returns_segments_with_render_preset(self):
        result, _, output_srt_path = self._run_transcribe_and_burn()

        self.assertEqual(
            [
                {"index": 1, "start": "00:00:00,000", "end": "00:00:01,800", "text": "第一句字幕"},
                {"index": 2, "start": "00:00:01,800", "end": "00:00:03,200", "text": "第二句字幕"},
            ],
            result["segments"],
        )
        self.assertEqual("vizard_classic_cn", result.get("render_preset"))
        self.assertTrue(output_srt_path.exists())

    def test_vizard_preset_avoids_legacy_subtitles_force_style_filter(self):
        _, commands, _ = self._run_transcribe_and_burn()

        ffmpeg_command = " ".join(commands[-1])
        self.assertNotIn("subtitles=", ffmpeg_command)
        self.assertNotIn("force_style", ffmpeg_command)

    def test_transcribe_and_burn_creates_hidden_cue_assets_and_overlay_filter(self):
        _, commands, output_srt_path = self._run_transcribe_and_burn()

        cue_dir = output_srt_path.parent / ".subtitle-tmp"
        self.assertTrue(cue_dir.exists())
        self.assertEqual(
            ["cue-0001.png", "cue-0002.png"],
            sorted(path.name for path in cue_dir.glob("cue-*.png")),
        )

        ffmpeg_command = commands[-1]
        self.assertIn("-filter_complex", ffmpeg_command)
        self.assertNotIn("-vf", ffmpeg_command)
        self.assertEqual(
            "[0:v][1:v]overlay=0:0:enable='between(t,0.000,1.800)'[v1];[v1][2:v]overlay=0:0:enable='between(t,1.800,3.200)'[outv]",
            ffmpeg_command[ffmpeg_command.index("-filter_complex") + 1],
        )

    def test_build_public_file_url(self):
        file_url = build_public_file_url(
            file_path="/srv/bililive-source/主播/audio/test.wav",
            source_root="/srv/bililive-source",
            public_url_base="https://demo.example.com",
        )

        self.assertEqual("https://demo.example.com/files/%E4%B8%BB%E6%92%AD/audio/test.wav", file_url)

    def test_build_burn_temp_dir_uses_hidden_subdir(self):
        temp_dir = build_burn_temp_dir("/srv/bililive/汤山老王/Season 01/汤山老王.S01E0018.2026-02-25 - 标题.mp4")

        self.assertEqual("/srv/bililive/汤山老王/Season 01/.subtitle-tmp", temp_dir)

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


if __name__ == "__main__":
    unittest.main()
