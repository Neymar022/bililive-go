import importlib
import pathlib
import sys
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))


def load_layout_renderer():
    try:
        layout_module = importlib.import_module("renderers.layout")
    except ModuleNotFoundError:
        return None
    return getattr(layout_module, "layout_vizard_classic_cn", None)


def load_renderer_module():
    try:
        return importlib.import_module("renderers.vizard_renderer")
    except ModuleNotFoundError:
        return None


class VizardRendererLayoutTest(unittest.TestCase):
    def test_wraps_long_chinese_cue_into_expected_two_lines(self):
        layout_renderer = load_layout_renderer()

        self.assertIsNotNone(layout_renderer, "缺少 vizard_classic_cn 预设布局函数")
        if layout_renderer is None:
            return

        layout = layout_renderer(
            "今天我们来测试新的字幕卡片渲染效果",
            video_width=1920,
        )

        self.assertEqual("vizard_classic_cn", layout["preset"])
        self.assertEqual(
            ["今天我们来测试新的字幕", "卡片渲染效果"],
            layout["lines"],
        )

    def test_render_single_line_png_matches_golden(self):
        renderer_module = load_renderer_module()
        self.assertIsNotNone(renderer_module, "缺少 vizard renderer 模块")
        if renderer_module is None:
            return

        golden_path = ROOT / "tests" / "golden" / "vizard_classic_cn-single-cue.png"
        with tempfile.TemporaryDirectory() as temp_dir:
            output_path = pathlib.Path(temp_dir) / "single.png"
            renderer_module.render_cue_png(
                "字幕卡片测试",
                str(output_path),
                video_width=1920,
                video_height=1080,
                preset_name="vizard_classic_cn",
            )
            self.assertTrue(
                renderer_module.compare_png_with_golden(str(output_path), str(golden_path), tolerance=0)
            )

    def test_render_double_line_png_matches_golden(self):
        renderer_module = load_renderer_module()
        self.assertIsNotNone(renderer_module, "缺少 vizard renderer 模块")
        if renderer_module is None:
            return

        golden_path = ROOT / "tests" / "golden" / "vizard_classic_cn-double-line.png"
        with tempfile.TemporaryDirectory() as temp_dir:
            output_path = pathlib.Path(temp_dir) / "double.png"
            renderer_module.render_cue_png(
                "今天我们来测试新的字幕卡片渲染效果",
                str(output_path),
                video_width=1920,
                video_height=1080,
                preset_name="vizard_classic_cn",
            )
            self.assertTrue(
                renderer_module.compare_png_with_golden(str(output_path), str(golden_path), tolerance=0)
            )


if __name__ == "__main__":
    unittest.main()
