import pathlib
import sys
import tempfile
import unittest
from unittest import mock

from fastapi.testclient import TestClient


ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from app import app
from worker_core import generate_style_lab_sample


class StyleLabSampleTest(unittest.TestCase):
    def test_generate_style_lab_sample_cuts_clip_writes_srt_and_returns_hidden_output_paths(self):
        commands: list[list[str]] = []

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_root = pathlib.Path(temp_dir)
            source_dir = temp_root / "source"
            source_dir.mkdir(parents=True, exist_ok=True)
            source_path = source_dir / "source.mp4"
            source_path.write_bytes(b"fake-video")

            def fake_run(cmd, check, capture_output):
                commands.append(cmd)
                clip_path = pathlib.Path(cmd[-1])
                clip_path.parent.mkdir(parents=True, exist_ok=True)
                clip_path.write_bytes(b"clip-video")
                return mock.Mock(returncode=0)

            def fake_burn(input_path, subtitle_path, output_path, style, ffmpeg_bin="ffmpeg", *, segments=None):
                pathlib.Path(output_path).parent.mkdir(parents=True, exist_ok=True)
                pathlib.Path(output_path).write_bytes(b"burned-video")
                return "vizard_classic_cn"

            with mock.patch("worker_core.subprocess.run", side_effect=fake_run), \
                mock.patch("worker_core.burn_subtitles", side_effect=fake_burn):
                result = generate_style_lab_sample(
                    source_path=str(source_path),
                    sample_text="30 秒测试样片",
                    burn_style={
                        "preset": "vizard_classic_cn",
                        "font_name": "Noto Sans CJK SC",
                        "font_size": 50,
                    },
                )

            self.assertIn(".style-lab-samples", result["sample_video_path"])
            self.assertTrue(pathlib.Path(result["sample_video_path"]).exists())
            self.assertTrue(pathlib.Path(result["sample_srt_path"]).exists())
            self.assertEqual("vizard_classic_cn", result["render_preset"])
            self.assertIn("00:00:00,000 --> 00:00:30,000", pathlib.Path(result["sample_srt_path"]).read_text(encoding="utf-8"))
            self.assertIn("-t", commands[0])
            self.assertEqual("30.000", commands[0][commands[0].index("-t") + 1])

    def test_sample_endpoint_returns_sample_payload(self):
        client = TestClient(app)

        with tempfile.TemporaryDirectory() as temp_dir:
            sample_video_path = pathlib.Path(temp_dir) / "sample.burned.mp4"
            sample_srt_path = pathlib.Path(temp_dir) / "sample.srt"
            sample_video_path.write_bytes(b"video")
            sample_srt_path.write_text("1\n00:00:00,000 --> 00:00:30,000\n测试\n", encoding="utf-8")

            with mock.patch(
                "app.generate_style_lab_sample",
                return_value={
                    "sample_video_path": str(sample_video_path),
                    "sample_srt_path": str(sample_srt_path),
                    "render_preset": "vizard_classic_cn",
                },
            ) as sample_mock:
                response = client.post(
                    "/api/v1/style-lab/sample",
                    json={
                        "source_path": "/tmp/source.mp4",
                        "sample_text": "测试样片",
                        "burn_style": {
                            "preset": "vizard_classic_cn",
                            "font_name": "Noto Sans CJK SC",
                            "font_size": 50,
                        },
                    },
                )

        self.assertEqual(200, response.status_code)
        self.assertEqual(str(sample_video_path), response.json()["sample_video_path"])
        self.assertEqual(str(sample_srt_path), response.json()["sample_srt_path"])
        sample_mock.assert_called_once()


if __name__ == "__main__":
    unittest.main()
