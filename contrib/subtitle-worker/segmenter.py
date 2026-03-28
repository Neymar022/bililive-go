from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Any


PREFERRED_BREAK_CHARS = set("，。！？；：、,.!?;:")
PHRASE_BREAK_MARKERS = ("如果", "因为", "所以", "但是", "然后", "接着", "并且", "而且", "不过", "就")
MIN_CHUNK_CHARS = 4
PUNCTUATION_OVERFLOW_ALLOWANCE = 2
CHINESE_DIGIT_MAP = {
    "零": 0,
    "〇": 0,
    "一": 1,
    "二": 2,
    "两": 2,
    "三": 3,
    "四": 4,
    "五": 5,
    "六": 6,
    "七": 7,
    "八": 8,
    "九": 9,
}
CHINESE_UNIT_MAP = {
    "十": 10,
    "百": 100,
    "千": 1_000,
    "万": 10_000,
    "亿": 100_000_000,
}
CHINESE_NUMBER_RE = re.compile(r"[零〇一二两三四五六七八九十百千万亿点]{2,}")
REPEATED_YEAR_RE = re.compile(r"(?<!\d)20(?:20)+(\d{2})年")


@dataclass(frozen=True)
class BoundaryCandidate:
    index: int
    kind: str


def normalize_text(text: str) -> str:
    collapsed = "".join(str(text).split())
    normalized = normalize_number_sequences(collapsed)
    return lightly_dedupe_repetitions(normalized)


def normalize_number_sequences(text: str) -> str:
    normalized = CHINESE_NUMBER_RE.sub(_replace_chinese_number_match, text)
    return REPEATED_YEAR_RE.sub(r"20\1年", normalized)


def lightly_dedupe_repetitions(text: str) -> str:
    if len(text) < 4:
        return text

    previous = None
    current = text
    while previous != current:
        previous = current
        current = _dedupe_pass(current)
    return current


def _dedupe_pass(text: str) -> str:
    output: list[str] = []
    index = 0
    text_len = len(text)

    while index < text_len:
        collapsed = False
        max_unit_len = min(4, (text_len - index) // 2)
        for unit_len in range(max_unit_len, 1, -1):
            unit = text[index : index + unit_len]
            repeat_end = index + unit_len
            repeat_count = 1
            while text.startswith(unit, repeat_end):
                repeat_count += 1
                repeat_end += unit_len
            if repeat_count > 1:
                output.append(unit)
                index = repeat_end
                collapsed = True
                break
        if not collapsed:
            output.append(text[index])
            index += 1

    return "".join(output)


def _replace_chinese_number_match(match: re.Match[str]) -> str:
    value = match.group(0)
    if all(char in CHINESE_DIGIT_MAP for char in value):
        return "".join(str(CHINESE_DIGIT_MAP[char]) for char in value)
    return convert_chinese_number(value)


def convert_chinese_number(value: str) -> str:
    if "点" in value:
        integer_part, decimal_part = value.split("点", 1)
        converted_integer = convert_chinese_integer(integer_part)
        converted_decimal = "".join(str(CHINESE_DIGIT_MAP[char]) for char in decimal_part if char in CHINESE_DIGIT_MAP)
        return f"{converted_integer}.{converted_decimal}".rstrip(".")
    return str(convert_chinese_integer(value))


def convert_chinese_integer(value: str) -> int:
    if not value:
        return 0
    if all(char in CHINESE_DIGIT_MAP for char in value):
        return int("".join(str(CHINESE_DIGIT_MAP[char]) for char in value))

    total = 0
    section = 0
    number = 0

    for char in value:
        if char in CHINESE_DIGIT_MAP:
            number = CHINESE_DIGIT_MAP[char]
            continue
        unit = CHINESE_UNIT_MAP.get(char)
        if unit is None:
            continue
        if unit < 10_000:
            if number == 0:
                number = 1
            section += number * unit
        else:
            section = (section + number) * unit
            total += section
            section = 0
        number = 0

    return total + section + number


def trim_boundary_text(text: str) -> str:
    trimmed = normalize_text(text)
    while trimmed and trimmed[0] in PREFERRED_BREAK_CHARS:
        trimmed = trimmed[1:]
    while trimmed and trimmed[-1] in PREFERRED_BREAK_CHARS:
        trimmed = trimmed[:-1]
    return trimmed


def char_length(text: str) -> int:
    return len(normalize_text(text))


def estimate_text_width(text: str, font_size: int) -> int:
    units = 0.0
    for char in text:
        if char.isspace():
            units += 0.35
        elif char.isascii():
            units += 0.58
        else:
            units += 0.88
    return int(round(units * font_size))


def fits_single_line_budget(
    text: str,
    max_chars: int,
    *,
    font_size: int | None = None,
    safe_text_width: int | None = None,
) -> bool:
    normalized = normalize_text(text)
    if len(normalized) > max_chars:
        return False
    if font_size is not None and safe_text_width is not None:
        return estimate_text_width(normalized, font_size) <= safe_text_width
    return True


def punctuation_boundaries(text: str, max_chars: int) -> list[BoundaryCandidate]:
    candidates: list[BoundaryCandidate] = []
    upper_bound = min(len(text), max_chars + PUNCTUATION_OVERFLOW_ALLOWANCE)
    for index in range(MIN_CHUNK_CHARS, upper_bound):
        if text[index - 1] in PREFERRED_BREAK_CHARS:
            candidates.append(BoundaryCandidate(index=index, kind="punctuation"))
    return candidates


def phrase_boundaries(text: str, max_chars: int) -> list[BoundaryCandidate]:
    candidates: list[BoundaryCandidate] = []
    for marker in PHRASE_BREAK_MARKERS:
        marker_index = text.find(marker, MIN_CHUNK_CHARS)
        if marker_index != -1 and marker_index <= max_chars:
            candidates.append(BoundaryCandidate(index=marker_index, kind="phrase"))
    return sorted(candidates, key=lambda candidate: candidate.index)


def choose_split_point(
    text: str,
    max_chars: int,
    *,
    font_size: int | None = None,
    safe_text_width: int | None = None,
) -> int:
    if fits_single_line_budget(text, max_chars, font_size=font_size, safe_text_width=safe_text_width):
        return len(text)

    punctuation = [
        candidate
        for candidate in punctuation_boundaries(text, max_chars)
        if fits_single_line_budget(
            text[: candidate.index],
            max_chars,
            font_size=font_size,
            safe_text_width=safe_text_width,
        )
    ]
    if punctuation:
        return punctuation[-1].index

    phrases = [
        candidate
        for candidate in phrase_boundaries(text, max_chars)
        if fits_single_line_budget(
            text[: candidate.index],
            max_chars,
            font_size=font_size,
            safe_text_width=safe_text_width,
        )
    ]
    if phrases:
        return phrases[0].index

    for index in range(min(max_chars, len(text)), 0, -1):
        if fits_single_line_budget(text[:index], max_chars, font_size=font_size, safe_text_width=safe_text_width):
            return index

    return min(max_chars, len(text))


def split_text_by_rules(
    text: str,
    max_chars: int,
    *,
    font_size: int | None = None,
    safe_text_width: int | None = None,
) -> list[str]:
    normalized = normalize_text(text)
    if not normalized:
        return []
    if fits_single_line_budget(normalized, max_chars, font_size=font_size, safe_text_width=safe_text_width):
        return [normalized]

    chunks: list[str] = []
    remaining = normalized
    while remaining:
        if fits_single_line_budget(remaining, max_chars, font_size=font_size, safe_text_width=safe_text_width):
            chunks.append(remaining)
            break
        split_at = choose_split_point(
            remaining,
            max_chars,
            font_size=font_size,
            safe_text_width=safe_text_width,
        )
        current = trim_boundary_text(remaining[:split_at])
        if not current:
            split_at = min(max_chars, len(remaining))
            current = trim_boundary_text(remaining[:split_at])
        chunks.append(current)
        remaining = trim_boundary_text(remaining[split_at:])

    return rebalance_short_tail(chunks, max_chars, font_size=font_size, safe_text_width=safe_text_width)


def rebalance_short_tail(
    chunks: list[str],
    max_chars: int,
    *,
    font_size: int | None = None,
    safe_text_width: int | None = None,
) -> list[str]:
    if len(chunks) < 2:
        return chunks

    result = chunks[:]
    while len(result) >= 2:
        tail = result[-1]
        if len(tail) >= MIN_CHUNK_CHARS:
            break
        previous_tail_length = len(tail)
        previous_pair = result[-2:]
        merged = result[-2] + tail
        split_at = choose_rebalanced_split(
            merged,
            max_chars,
            font_size=font_size,
            safe_text_width=safe_text_width,
        )
        next_pair = [merged[:split_at], merged[split_at:]]
        if next_pair == previous_pair:
            break
        result[-2:] = next_pair
        if len(result[-1]) >= MIN_CHUNK_CHARS:
            break
        if len(result[-1]) <= previous_tail_length:
            break
        if split_at <= 1 or split_at >= len(merged) - 1:
            break

    return result


def choose_rebalanced_split(
    text: str,
    max_chars: int,
    *,
    font_size: int | None = None,
    safe_text_width: int | None = None,
) -> int:
    minimum_split = max(1, len(text) - max_chars)
    maximum_split = min(max_chars, len(text) - 1)
    target = len(text) / 2

    candidates: list[BoundaryCandidate] = []
    for candidate in punctuation_boundaries(text, maximum_split):
        left = text[: candidate.index]
        right = text[candidate.index :]
        if (
            minimum_split <= candidate.index <= maximum_split
            and fits_single_line_budget(left, max_chars, font_size=font_size, safe_text_width=safe_text_width)
            and fits_single_line_budget(right, max_chars, font_size=font_size, safe_text_width=safe_text_width)
        ):
            candidates.append(candidate)
    for candidate in phrase_boundaries(text, maximum_split):
        left = text[: candidate.index]
        right = text[candidate.index :]
        if (
            minimum_split <= candidate.index <= maximum_split
            and fits_single_line_budget(left, max_chars, font_size=font_size, safe_text_width=safe_text_width)
            and fits_single_line_budget(right, max_chars, font_size=font_size, safe_text_width=safe_text_width)
        ):
            candidates.append(candidate)
    if not candidates:
        candidates = [
            BoundaryCandidate(index=index, kind="char")
            for index in range(minimum_split, maximum_split + 1)
            if fits_single_line_budget(text[:index], max_chars, font_size=font_size, safe_text_width=safe_text_width)
            and fits_single_line_budget(text[index:], max_chars, font_size=font_size, safe_text_width=safe_text_width)
        ]
    if not candidates:
        candidates = [BoundaryCandidate(index=index, kind="char") for index in range(minimum_split, maximum_split + 1)]

    def score(candidate: BoundaryCandidate) -> tuple[int, float]:
        priority = {"punctuation": 0, "phrase": 1, "char": 2}[candidate.kind]
        return (priority, abs(candidate.index - target))

    return min(candidates, key=score).index


def split_segment_by_tokens(
    segment: dict[str, Any],
    max_chars: int,
    pause_threshold_ms: int,
    *,
    font_size: int | None = None,
    safe_text_width: int | None = None,
) -> list[dict[str, Any]] | None:
    tokens = segment.get("tokens")
    if not tokens:
        return None

    normalized_tokens = [token for token in tokens if normalize_text(token.get("text", ""))]
    if not normalized_tokens:
        return None

    chunks: list[list[dict[str, Any]]] = []
    current: list[dict[str, Any]] = []
    current_len = 0
    current_width = 0
    last_pause_cut = 0

    for index, token in enumerate(normalized_tokens):
        token_text = normalize_text(token["text"])
        projected_len = current_len + len(token_text)
        projected_width = current_width + estimate_text_width(token_text, font_size or 1)
        exceeds_width = font_size is not None and safe_text_width is not None and projected_width > safe_text_width
        if current and (projected_len > max_chars or exceeds_width):
            cut_index = last_pause_cut or len(current)
            chunks.append(current[:cut_index])
            current = current[cut_index:]
            current_len = sum(len(normalize_text(item["text"])) for item in current)
            current_width = sum(estimate_text_width(normalize_text(item["text"]), font_size or 1) for item in current)
            last_pause_cut = 0

        current.append(token)
        current_len += len(token_text)
        current_width += estimate_text_width(token_text, font_size or 1)

        if index + 1 < len(normalized_tokens):
            gap = int(normalized_tokens[index + 1]["start_ms"]) - int(token["end_ms"])
            if gap >= pause_threshold_ms:
                last_pause_cut = len(current)

    if current:
        chunks.append(current)

    merged_chunks = [trim_boundary_text("".join(token["text"] for token in chunk)) for chunk in chunks if chunk]
    merged_chunks = rebalance_short_tail(
        merged_chunks,
        max_chars,
        font_size=font_size,
        safe_text_width=safe_text_width,
    )
    if len(merged_chunks) == len(chunks):
        if any(
            not fits_single_line_budget(text, max_chars, font_size=font_size, safe_text_width=safe_text_width)
            for text in merged_chunks
        ):
            return None
        return [
            {
                "text": merged_chunks[index],
                "start_ms": int(chunk[0]["start_ms"]),
                "end_ms": int(chunk[-1]["end_ms"]),
            }
            for index, chunk in enumerate(chunks)
            if chunk
        ]
    return None


def distribute_times(start_ms: int, end_ms: int, chunks: list[str]) -> list[tuple[int, int]]:
    if not chunks:
        return []
    total_chars = sum(max(len(chunk), 1) for chunk in chunks)
    current_start = start_ms
    ranges: list[tuple[int, int]] = []
    consumed = 0
    for index, chunk in enumerate(chunks, start=1):
        consumed += max(len(chunk), 1)
        if index == len(chunks):
            current_end = end_ms
        else:
            ratio = consumed / total_chars
            current_end = start_ms + round((end_ms - start_ms) * ratio)
        ranges.append((current_start, current_end))
        current_start = current_end
    return ranges


def split_segments_for_timeline(
    segments: list[dict[str, Any]],
    max_chars: int,
    *,
    pause_threshold_ms: int = 350,
    font_size: int | None = None,
    safe_text_width: int | None = None,
) -> list[dict[str, Any]]:
    output: list[dict[str, Any]] = []

    for segment in segments:
        tokenized_chunks = split_segment_by_tokens(
            segment,
            max_chars,
            pause_threshold_ms,
            font_size=font_size,
            safe_text_width=safe_text_width,
        )
        if tokenized_chunks is not None:
            for chunk in tokenized_chunks:
                output.append(chunk)
            continue

        chunk_texts = split_text_by_rules(
            str(segment.get("text", "")),
            max_chars,
            font_size=font_size,
            safe_text_width=safe_text_width,
        )
        time_ranges = distribute_times(int(segment["start_ms"]), int(segment["end_ms"]), chunk_texts)
        for chunk_text, (chunk_start, chunk_end) in zip(chunk_texts, time_ranges):
            output.append(
                {
                    "text": chunk_text,
                    "start_ms": chunk_start,
                    "end_ms": chunk_end,
                }
            )

    indexed = [
        {
            "index": index,
            "text": chunk["text"],
            "start_ms": int(chunk["start_ms"]),
            "end_ms": int(chunk["end_ms"]),
        }
        for index, chunk in enumerate(output, start=1)
    ]
    return clamp_overlapping_segments(indexed)


def clamp_overlapping_segments(segments: list[dict[str, Any]]) -> list[dict[str, Any]]:
    if not segments:
        return []

    normalized: list[dict[str, Any]] = []
    previous_end = 0
    for segment in segments:
        start_ms = max(int(segment["start_ms"]), previous_end)
        end_ms = max(int(segment["end_ms"]), start_ms)
        updated = dict(segment)
        updated["start_ms"] = start_ms
        updated["end_ms"] = end_ms
        normalized.append(updated)
        previous_end = end_ms
    return normalized
