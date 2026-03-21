import importlib
import pathlib
import sys
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


if __name__ == "__main__":
    unittest.main()
