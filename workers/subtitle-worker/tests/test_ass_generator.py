import pathlib
import re
import sys
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from ass_generator import build_ass_document, build_ass_style_profile


class ASSGeneratorTest(unittest.TestCase):
    @staticmethod
    def _box_dimensions(line: str) -> tuple[int, int]:
        path = line.split(r"\p1}", 1)[1]
        pairs = [(int(x), int(y)) for x, y in re.findall(r"(-?\d+) (-?\d+)", path)]
        xs = [x for x, _ in pairs]
        ys = [y for _, y in pairs]
        return max(xs, default=0), max(ys, default=0)

    def test_build_ass_style_profile_uses_distinct_orientation_parameters(self):
        landscape = build_ass_style_profile(1920, 1080)
        portrait = build_ass_style_profile(1080, 1920)

        self.assertEqual("landscape", landscape.orientation)
        self.assertEqual("portrait", portrait.orientation)
        self.assertNotEqual(landscape.margin_v, portrait.margin_v)
        self.assertNotEqual(landscape.max_chars, portrait.max_chars)
        self.assertNotEqual(landscape.min_box_width, portrait.min_box_width)
        self.assertNotEqual(landscape.max_box_width, portrait.max_box_width)
        self.assertNotEqual(landscape.safe_text_width, portrait.safe_text_width)

    def test_build_ass_document_generates_ass_sections_and_single_line_dialogues(self):
        ass_text, segments = build_ass_document(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 4200,
                    "text": "今天市场已经明显转暖，所以我们先看成交量，再看情绪变化。",
                }
            ],
            video_width=1080,
            video_height=1920,
            burn_style={"font_name": "Noto Sans CJK SC", "font_size": 50, "outline": 2, "shadow": 0},
        )

        self.assertIn("[Script Info]", ass_text)
        self.assertIn("[V4+ Styles]", ass_text)
        self.assertIn("[Events]", ass_text)
        self.assertIn("PlayResX: 1080", ass_text)
        self.assertIn("PlayResY: 1920", ass_text)
        self.assertIn("Style: Text", ass_text)
        self.assertIn("Style: Box", ass_text)
        self.assertTrue(all(r"\N" not in segment["text"] for segment in segments))
        self.assertGreater(len(segments), 1)
        self.assertEqual(len(segments) * 2, ass_text.count("Dialogue:"))

    def test_build_ass_document_uses_different_style_lines_for_landscape_and_portrait(self):
        landscape_ass, _ = build_ass_document(
            [{"index": 1, "start_ms": 0, "end_ms": 1800, "text": "横屏字幕样式"}],
            video_width=1920,
            video_height=1080,
            burn_style={"font_name": "Noto Sans CJK SC", "font_size": 50, "outline": 2, "shadow": 0},
        )
        portrait_ass, _ = build_ass_document(
            [{"index": 1, "start_ms": 0, "end_ms": 1800, "text": "竖屏字幕样式"}],
            video_width=1080,
            video_height=1920,
            burn_style={"font_name": "Noto Sans CJK SC", "font_size": 50, "outline": 2, "shadow": 0},
        )

        self.assertNotEqual(landscape_ass, portrait_ass)
        self.assertIn("MarginV,Encoding", landscape_ass)
        self.assertIn("MarginV,Encoding", portrait_ass)

    def test_build_ass_document_places_portrait_dialogues_in_visual_mid_lower_band(self):
        ass_text, _ = build_ass_document(
            [{"index": 1, "start_ms": 0, "end_ms": 1800, "text": "字幕参数"}],
            video_width=480,
            video_height=864,
            burn_style={
                "font_name": "Noto Sans CJK SC",
                "font_size": 50,
                "card_width": 920,
                "card_height": 196,
                "bottom_offset": 640,
                "margin_v": 24,
                "outline": 2,
                "shadow": 0,
            },
        )

        self.assertIn("Style: Text,Noto Sans CJK SC,27,", ass_text)
        self.assertIn(r"{\an5\pos(240,648)\q2}", ass_text)

    def test_build_ass_document_respects_explicit_portrait_margin_v_when_it_pushes_above_visual_anchor(self):
        ass_text, _ = build_ass_document(
            [{"index": 1, "start_ms": 0, "end_ms": 1800, "text": "字幕参数"}],
            video_width=480,
            video_height=864,
            burn_style={
                "font_name": "Noto Sans CJK SC",
                "font_size": 50,
                "card_width": 920,
                "card_height": 196,
                "bottom_offset": 640,
                "margin_v": 500,
                "outline": 2,
                "shadow": 0,
            },
        )

        self.assertIn(r"{\an5\pos(240,619)\q2}", ass_text)

    def test_build_ass_style_profile_uses_font_driven_single_line_box_heights(self):
        style = {
            "font_name": "Noto Sans CJK SC",
            "font_size": 50,
            "card_width": 920,
            "card_height": 196,
            "bottom_offset": 640,
            "margin_v": 24,
            "outline": 2,
            "shadow": 0,
        }

        portrait = build_ass_style_profile(480, 864, style)
        landscape = build_ass_style_profile(1920, 1080, style)

        self.assertEqual(46, portrait.box_height)
        self.assertEqual(108, portrait.min_box_width)
        self.assertEqual(112, landscape.box_height)
        self.assertEqual(300, landscape.min_box_width)
        self.assertEqual(648, portrait.text_pos_y)

        recent_portrait = build_ass_style_profile(720, 1280, style)
        self.assertEqual(41, recent_portrait.font_size)
        self.assertEqual(70, recent_portrait.box_height)
        self.assertEqual(613, recent_portrait.max_box_width)
        self.assertEqual(27, recent_portrait.side_padding)

    def test_build_ass_document_uses_compact_single_line_boxes_for_short_cues(self):
        style = {
            "font_name": "Noto Sans CJK SC",
            "font_size": 50,
            "card_width": 920,
            "card_height": 196,
            "bottom_offset": 640,
            "margin_v": 24,
            "outline": 2,
            "shadow": 0,
        }
        portrait_ass, _ = build_ass_document(
            [{"index": 1, "start_ms": 0, "end_ms": 1800, "text": "染话筒。"}],
            video_width=480,
            video_height=864,
            burn_style=style,
        )
        landscape_ass, _ = build_ass_document(
            [{"index": 1, "start_ms": 0, "end_ms": 1800, "text": "横屏字幕样式"}],
            video_width=1920,
            video_height=1080,
            burn_style=style,
        )

        portrait_box = next(line for line in portrait_ass.splitlines() if line.startswith("Dialogue: 0,"))
        landscape_box = next(line for line in landscape_ass.splitlines() if line.startswith("Dialogue: 0,"))

        self.assertEqual((131, 46), self._box_dimensions(portrait_box))
        self.assertEqual((382, 112), self._box_dimensions(landscape_box))

    def test_build_ass_document_uses_vector_box_background_with_rounded_corners(self):
        ass_text, _ = build_ass_document(
            [{"index": 1, "start_ms": 0, "end_ms": 1800, "text": "圆角底板测试"}],
            video_width=1080,
            video_height=1920,
            burn_style={"font_name": "Noto Sans CJK SC", "font_size": 50, "outline": 2, "shadow": 0},
        )

        self.assertIn(r"{\an7\pos(", ass_text)
        self.assertIn(r"\p1}", ass_text)
        self.assertIn(" b ", ass_text)
        self.assertIn(r"{\an5\pos(", ass_text)

    def test_build_ass_document_uses_adaptive_box_width_within_profile_bounds(self):
        ass_text, _ = build_ass_document(
            [
                {"index": 1, "start_ms": 0, "end_ms": 1600, "text": "短句"},
                {"index": 2, "start_ms": 1600, "end_ms": 4200, "text": "这是一条明显更长的字幕内容"},
            ],
            video_width=1080,
            video_height=1920,
            burn_style={"font_name": "Noto Sans CJK SC", "font_size": 50, "outline": 2, "shadow": 0},
        )

        box_lines = [line for line in ass_text.splitlines() if line.startswith("Dialogue: 0,")]
        self.assertEqual(2, len(box_lines))
        profile = build_ass_style_profile(1080, 1920, {"font_name": "Noto Sans CJK SC", "font_size": 50, "outline": 2, "shadow": 0})

        def box_width(line: str) -> int:
            match = re.search(r"m (\d+) 0 l (\d+) 0", line)
            self.assertIsNotNone(match)
            radius = int(match.group(1))
            right_minus_radius = int(match.group(2))
            return radius + right_minus_radius

        short_width = box_width(box_lines[0])
        long_width = box_width(box_lines[1])
        self.assertLess(short_width, long_width)
        self.assertGreaterEqual(short_width, profile.min_box_width)
        self.assertLessEqual(long_width, profile.max_box_width)


if __name__ == "__main__":
    unittest.main()
