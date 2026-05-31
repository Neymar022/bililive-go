from __future__ import annotations

from typing import Any


DEFAULT_MAX_LINE_CHARS = 16
DEFAULT_MAX_LINES = 1
DEFAULT_CARD_WIDTH = 1018
DEFAULT_CARD_HEIGHT = 196


def normalize_cue_text(text: str) -> str:
    return " ".join(str(text).replace("\n", " ").split()).strip()


def split_cue_lines(text: str, max_line_chars: int = DEFAULT_MAX_LINE_CHARS, max_lines: int = DEFAULT_MAX_LINES) -> list[str]:
    normalized = normalize_cue_text(text)
    if not normalized:
        return [""]

    if len(normalized) <= max_line_chars:
        return [normalized]

    if max_lines <= 1:
        return [normalized[:max_line_chars] + "…"]

    lines = [normalized[:max_line_chars]]
    remaining = normalized[max_line_chars:]
    if remaining:
        lines.append(remaining[:max_line_chars] + ("…" if len(remaining) > max_line_chars else ""))
    return lines[:max_lines]


def layout_vizard_classic_cn(text: str, video_width: int, video_height: int = 1080) -> dict[str, Any]:
    lines = split_cue_lines(text)
    horizontal_padding = 72
    card_width = min(video_width - 240, DEFAULT_CARD_WIDTH)
    card_height = DEFAULT_CARD_HEIGHT
    bottom_offset = round(video_height * 2 / 6)

    return {
        "preset": "vizard_classic_cn",
        "normalized_text": normalize_cue_text(text),
        "lines": lines,
        "card_width": card_width,
        "card_height": card_height,
        "bottom_offset": bottom_offset,
        "horizontal_padding": horizontal_padding,
        "vertical_padding": 34,
        "line_gap": 18,
        "max_line_chars": DEFAULT_MAX_LINE_CHARS,
    }
