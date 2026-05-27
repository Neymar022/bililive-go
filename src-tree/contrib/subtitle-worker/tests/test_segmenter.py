import pathlib
import sys
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from ass_generator import build_ass_style_profile
from segmenter import convert_chinese_integer, normalize_text, split_segments_for_timeline, estimate_text_width


class SegmenterTest(unittest.TestCase):
    def test_prefers_punctuation_boundaries_for_long_sentences(self):
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 5000,
                    "text": "今天市场已经明显转暖，所以我们先看成交量，再看情绪变化。",
                }
            ],
            max_chars=14,
        )

        self.assertEqual(
            ["今天市场已经明显转暖", "所以我们先看成交量", "再看情绪变化"],
            [segment["text"] for segment in segments],
        )

    def test_prefers_pause_boundaries_when_tokens_are_available(self):
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 4200,
                    "text": "今天先看市场接着再看仓位",
                    "tokens": [
                        {"text": "今天", "start_ms": 0, "end_ms": 300},
                        {"text": "先看", "start_ms": 300, "end_ms": 700},
                        {"text": "市场", "start_ms": 700, "end_ms": 1000},
                        {"text": "接着", "start_ms": 1700, "end_ms": 2100},
                        {"text": "再看", "start_ms": 2100, "end_ms": 2500},
                        {"text": "仓位", "start_ms": 2500, "end_ms": 2900},
                    ],
                }
            ],
            max_chars=8,
            pause_threshold_ms=400,
        )

        self.assertEqual(
            ["今天先看市场", "接着再看仓位"],
            [segment["text"] for segment in segments],
        )

    def test_falls_back_to_phrase_boundaries_before_character_cuts(self):
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 3600,
                    "text": "这个策略如果仓位过重就很难执行",
                }
            ],
            max_chars=10,
        )

        self.assertEqual(
            ["这个策略", "如果仓位过重", "就很难执行"],
            [segment["text"] for segment in segments],
        )

    def test_rebalances_single_character_tail_fragments(self):
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 2400,
                    "text": "这个时候不要卡住",
                }
            ],
            max_chars=5,
        )

        self.assertEqual(
            ["这个时候", "不要卡住"],
            [segment["text"] for segment in segments],
        )

    def test_avoids_two_character_tail_fragments(self):
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 2600,
                    "text": "现在一定要控制仓位",
                }
            ],
            max_chars=6,
        )

        self.assertEqual(
            ["现在一定", "要控制仓位"],
            [segment["text"] for segment in segments],
        )

    def test_keeps_output_single_line_chunks(self):
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 3000,
                    "text": "我们今天重点讲估值修复的节奏",
                }
            ],
            max_chars=8,
        )

        self.assertTrue(all("\n" not in segment["text"] for segment in segments))
        self.assertTrue(all(len(segment["text"]) <= 8 for segment in segments))

    def test_normalizes_spoken_chinese_digits_to_arabic(self):
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 4200,
                    "text": "因为为什么呢？到二零二零二二年到现在，我们录了有一百。",
                }
            ],
            max_chars=18,
        )

        self.assertEqual(
            ["因为为什么呢？到2022年到现在", "我们录了有100"],
            [segment["text"] for segment in segments],
        )

    def test_keeps_large_spoken_unit_numbers_as_chinese_text(self):
        self.assertEqual("一万", normalize_text("一万"))
        self.assertEqual("的话早就一万年了", normalize_text("的话早就一万年了"))
        self.assertEqual(10000, convert_chinese_integer("一万"))
        self.assertEqual(100000, convert_chinese_integer("十万"))

    def test_still_converts_years_and_plain_spoken_numbers_to_arabic(self):
        self.assertEqual("2026年", normalize_text("二零二六年"))
        self.assertEqual("2026年", normalize_text("二零二零二六年"))
        self.assertEqual("1000", normalize_text("一千"))

    def test_lightly_dedupes_repeated_stutters_before_width_checks(self):
        profile = build_ass_style_profile(720, 1280, {"font_size": 50})
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 3200,
                    "text": "这种这种这种想通过裂变去搞营销的这种",
                }
            ],
            max_chars=profile.max_chars,
            font_size=profile.font_size,
            safe_text_width=profile.safe_text_width,
        )

        self.assertNotIn("这种这种这种", "".join(segment["text"] for segment in segments))
        self.assertTrue(
            all(
                estimate_text_width(segment["text"], profile.font_size) <= profile.safe_text_width
                for segment in segments
            )
        )
        self.assertTrue(all(len(segment["text"]) >= 4 for segment in segments))

    def test_keeps_non_contiguous_repeated_words_when_cleaning(self):
        profile = build_ass_style_profile(720, 1280, {"font_size": 50})
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 3600,
                    "text": "这种方法和那种方法，本质上还是这种打法。",
                }
            ],
            max_chars=profile.max_chars,
            font_size=profile.font_size,
            safe_text_width=profile.safe_text_width,
        )

        self.assertIn("这种", "".join(segment["text"] for segment in segments))
        self.assertIn("那种", "".join(segment["text"] for segment in segments))

    def test_keeps_valid_adjacent_repeated_chinese_words(self):
        self.assertEqual("这是一条明显非常非常长的字幕", normalize_text("这是一条明显非常非常长的字幕"))
        self.assertEqual("市场市场化不是简单重复", normalize_text("市场市场化不是简单重复"))

    def test_width_budget_forces_extra_splits_before_ass_render(self):
        profile = build_ass_style_profile(720, 1280, {"font_size": 50})
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 0,
                    "end_ms": 5000,
                    "text": "想了解哪一块，把哪一块打在公屏上面啊",
                }
            ],
            max_chars=profile.max_chars,
            font_size=profile.font_size,
            safe_text_width=max(profile.safe_text_width // 2, 1),
        )

        self.assertGreater(len(segments), 1)
        self.assertTrue(
            all(
                estimate_text_width(segment["text"], profile.font_size) <= max(profile.safe_text_width // 2, 1)
                for segment in segments
            )
        )

    def test_splits_time_ranges_monotonically(self):
        segments = split_segments_for_timeline(
            [
                {
                    "index": 1,
                    "start_ms": 1000,
                    "end_ms": 5000,
                    "text": "因为今天波动很大，所以我们先等确认信号。",
                }
            ],
            max_chars=10,
        )

        self.assertEqual(1000, segments[0]["start_ms"])
        self.assertEqual(5000, segments[-1]["end_ms"])
        self.assertTrue(all(left["end_ms"] <= right["start_ms"] for left, right in zip(segments, segments[1:])))

    def test_clamps_overlapping_source_segments_to_monotonic_timeline(self):
        segments = split_segments_for_timeline(
            [
                {"index": 1, "start_ms": 29540, "end_ms": 30870, "text": "如果你是企业"},
                {"index": 2, "start_ms": 30280, "end_ms": 32490, "text": "你家想要装光伏跟储能"},
            ],
            max_chars=18,
        )

        self.assertEqual(["如果你是企业", "你家想要装光伏跟储能"], [segment["text"] for segment in segments])
        self.assertEqual(30280, segments[1]["start_ms"])
        self.assertEqual(30280, segments[0]["end_ms"])
        self.assertTrue(all(left["end_ms"] <= right["start_ms"] for left, right in zip(segments, segments[1:])))

    def test_keeps_fast_overlapping_segment_prefix_visible_at_raw_start(self):
        segments = split_segments_for_timeline(
            [
                {"index": 1, "start_ms": 1000, "end_ms": 2100, "text": "上一句还没结束"},
                {"index": 2, "start_ms": 1850, "end_ms": 2600, "text": "前缀很快出现"},
            ],
            max_chars=18,
        )

        self.assertEqual(["上一句还没结束", "前缀很快出现"], [segment["text"] for segment in segments])
        self.assertEqual(1850, segments[1]["start_ms"])
        self.assertEqual(1850, segments[0]["end_ms"])
        self.assertTrue(all(left["end_ms"] <= right["start_ms"] for left, right in zip(segments, segments[1:])))


if __name__ == "__main__":
    unittest.main()
