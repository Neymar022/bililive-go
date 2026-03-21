from __future__ import annotations

from typing import Any


DEFAULT_MAX_LINE_CHARS = 11
DEFAULT_MAX_LINES = 2


def normalize_cue_text(text: str) -> str:
    return " ".join(str(text).replace("\n", " ").split()).strip()


def split_cue_lines(text: str, max_line_chars: int = DEFAULT_MAX_LINE_CHARS, max_lines: int = DEFAULT_MAX_LINES) -> list[str]:
    normalized = normalize_cue_text(text)
    if not normalized:
        return [""]

    lines = [
        normalized[index:index + max_line_chars]
        for index in range(0, len(normalized), max_line_chars)
    ]
    if len(lines) > max_lines:
        lines = lines[: max_lines - 1] + ["".join(lines[max_lines - 1:])]
    return lines


def layout_vizard_classic_cn(text: str, video_width: int) -> dict[str, Any]:
    lines = split_cue_lines(text)
    longest_line = max((len(line) for line in lines), default=0)
    horizontal_padding = 72
    char_width = 38
    card_width = min(video_width - 240, max(560, longest_line * char_width + horizontal_padding * 2))
    card_height = 148 if len(lines) == 1 else 196

    return {
        "preset": "vizard_classic_cn",
        "normalized_text": normalize_cue_text(text),
        "lines": lines,
        "card_width": card_width,
        "card_height": card_height,
        "bottom_offset": 88,
        "horizontal_padding": horizontal_padding,
        "vertical_padding": 34,
        "line_gap": 18,
        "max_line_chars": DEFAULT_MAX_LINE_CHARS,
    }
