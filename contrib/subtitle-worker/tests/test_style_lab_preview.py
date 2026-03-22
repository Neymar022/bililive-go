import pathlib
import sys
import tempfile
import unittest
from unittest import mock

from fastapi.testclient import TestClient
from PIL import Image


ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from app import app
from worker_core import render_style_lab_preview


class StyleLabPreviewTest(unittest.TestCase):
    def test_render_style_lab_preview_extracts_frame_and_composites_preview_png(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_root = pathlib.Path(temp_dir)
            source_path = temp_root / "source.mp4"
            source_path.write_bytes(b"fake-video")
            output_path = temp_root / "preview.png"

            def fake_run(cmd, check, capture_output):
                frame_path = pathlib.Path(cmd[-1])
                frame_path.parent.mkdir(parents=True, exist_ok=True)
                Image.new("RGBA", (1080, 1920), (140, 150, 230, 255)).save(frame_path, format="PNG")
                return mock.Mock(returncode=0)

            with mock.patch("worker_core.subprocess.run", side_effect=fake_run):
                result = render_style_lab_preview(
                    source_path=str(source_path),
                    preview_text="字幕样式实验室预览",
                    burn_style={
                        "preset": "vizard_classic_cn",
                        "font_name": "Noto Sans CJK SC",
                        "font_size": 50,
                        "card_width": 1018,
                        "card_height": 196,
                        "bottom_offset": 640,
                        "background_opacity": 0.9,
                        "border_opacity": 0.08,
                        "single_line": True,
                        "overflow_mode": "ellipsis",
                    },
                    output_preview_path=str(output_path),
                )

            self.assertEqual(str(output_path), result["preview_image_path"])
            self.assertEqual("vizard_classic_cn", result["render_preset"])
            self.assertTrue(output_path.exists())
            preview = Image.open(output_path).convert("RGBA")
            self.assertEqual((1080, 1920), preview.size)

    def test_preview_endpoint_returns_preview_payload(self):
        client = TestClient(app)

        with tempfile.TemporaryDirectory() as temp_dir:
            preview_path = pathlib.Path(temp_dir) / "preview.png"
            preview_path.write_bytes(b"png")

            with mock.patch(
                "app.render_style_lab_preview",
                return_value={
                    "preview_image_path": str(preview_path),
                    "render_preset": "vizard_classic_cn",
                },
            ) as preview_mock:
                response = client.post(
                    "/api/v1/style-lab/preview",
                    json={
                        "source_path": "/tmp/source.mp4",
                        "preview_text": "测试文案",
                        "burn_style": {
                            "preset": "vizard_classic_cn",
                            "font_name": "Noto Sans CJK SC",
                            "font_size": 50,
                            "card_width": 1018,
                            "card_height": 196,
                            "bottom_offset": 640,
                            "background_opacity": 0.9,
                            "border_opacity": 0.08,
                            "single_line": True,
                            "overflow_mode": "ellipsis",
                        },
                    },
                )

        self.assertEqual(200, response.status_code)
        self.assertEqual(str(preview_path), response.json()["preview_image_path"])
        preview_mock.assert_called_once()


if __name__ == "__main__":
    unittest.main()
