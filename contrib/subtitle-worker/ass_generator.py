from __future__ import annotations

import subprocess
from dataclasses import dataclass
from typing import Any

from segmenter import estimate_text_width, split_segments_for_timeline

DEFAULT_RENDER_PRESET = "vizard_classic_cn"


@dataclass(frozen=True)
class ASSStyleProfile:
    orientation: str
    font_name: str
    font_size: int
    outline: float
    shadow: float
    margin_v: int
    margin_l: int
    margin_r: int
    max_chars: int
    min_box_width: int
    max_box_width: int
    box_height: int
    corner_radius: int
    text_pos_y: int
    side_padding: int
    safe_text_width: int


def escape_ass_text(text: str) -> str:
    return text.replace("\\", r"\\").replace("{", "(").replace("}", ")").replace("\n", " ")


def ms_to_ass_time(milliseconds: int) -> str:
    total_centiseconds = max(0, round(int(milliseconds) / 10))
    hours, remainder = divmod(total_centiseconds, 360000)
    minutes, remainder = divmod(remainder, 6000)
    seconds, centiseconds = divmod(remainder, 100)
    return f"{hours}:{minutes:02d}:{seconds:02d}.{centiseconds:02d}"


def opacity_to_ass_alpha(opacity: float) -> str:
    clamped = min(max(float(opacity), 0.0), 1.0)
    alpha = round((1.0 - clamped) * 255)
    return f"{alpha:02X}"


def normalize_render_preset(name: str | None) -> str:
    normalized = (name or "").strip()
    if normalized in {"", "bottom_center"}:
        return DEFAULT_RENDER_PRESET
    return normalized


def probe_video_size(input_path: str, ffprobe_bin: str = "ffprobe") -> tuple[int, int]:
    try:
        completed = subprocess.run(
            [
                ffprobe_bin,
                "-v",
                "error",
                "-select_streams",
                "v:0",
                "-show_entries",
                "stream=width,height",
                "-of",
                "csv=p=0:s=x",
                input_path,
            ],
            check=True,
            capture_output=True,
            text=True,
        )
        width, height = completed.stdout.strip().split("x", 1)
        return int(width), int(height)
    except Exception:
        return 1920, 1080


def build_ass_style_profile(video_width: int, video_height: int, burn_style: dict[str, Any] | None = None) -> ASSStyleProfile:
    burn_style = burn_style or {}
    font_name = str(burn_style.get("font_name", "Noto Sans CJK SC"))
    base_font_size = int(burn_style.get("font_size", 50))
    base_outline = float(burn_style.get("outline", 2))
    base_shadow = float(burn_style.get("shadow", 0))

    if video_height > video_width:
        font_size = max(base_font_size + 10, 60)
        box_height = max(int(font_size * 1.8), 108)
        margin_v = max(int(video_height * 0.12), 220)
        min_box_width = max(int(video_width * 0.42), 360)
        max_box_width = min(video_width - 108, int(video_width * 0.9))
        side_padding = max(int(font_size * 0.95), 44)
        return ASSStyleProfile(
            orientation="portrait",
            font_name=font_name,
            font_size=font_size,
            outline=max(base_outline - 1, 1),
            shadow=base_shadow,
            margin_v=margin_v,
            margin_l=54,
            margin_r=54,
            max_chars=18,
            min_box_width=min_box_width,
            max_box_width=max_box_width,
            box_height=box_height,
            corner_radius=box_height // 2,
            text_pos_y=video_height - margin_v - (box_height // 2),
            side_padding=side_padding,
            safe_text_width=max(max_box_width - side_padding * 2, 1),
        )

    font_size = max(base_font_size - 2, 48)
    box_height = max(int(font_size * 1.6), 88)
    margin_v = max(int(video_height * 0.08), 96)
    min_box_width = max(int(video_width * 0.30), 420)
    max_box_width = min(video_width - 144, int(video_width * 0.78))
    side_padding = max(int(font_size * 0.8), 40)
    return ASSStyleProfile(
        orientation="landscape",
        font_name=font_name,
        font_size=font_size,
        outline=max(base_outline, 1),
        shadow=base_shadow,
        margin_v=margin_v,
        margin_l=72,
        margin_r=72,
        max_chars=28,
        min_box_width=min_box_width,
        max_box_width=max_box_width,
        box_height=box_height,
        corner_radius=box_height // 2,
        text_pos_y=video_height - margin_v - (box_height // 2),
        side_padding=side_padding,
        safe_text_width=max(max_box_width - side_padding * 2, 1),
    )


def build_round_rect_path(width: int, height: int, radius: int) -> str:
    radius = max(0, min(radius, width // 2, height // 2))
    if radius == 0:
        return f"m 0 0 l {width} 0 {width} {height} 0 {height}"

    kappa = 0.55228475
    offset = int(round(radius * kappa))
    right = width
    bottom = height
    return (
        f"m {radius} 0 "
        f"l {right - radius} 0 "
        f"b {right - radius + offset} 0 {right} {radius - offset} {right} {radius} "
        f"l {right} {bottom - radius} "
        f"b {right} {bottom - radius + offset} {right - radius + offset} {bottom} {right - radius} {bottom} "
        f"l {radius} {bottom} "
        f"b {radius - offset} {bottom} 0 {bottom - radius + offset} 0 {bottom - radius} "
        f"l 0 {radius} "
        f"b 0 {radius - offset} {radius - offset} 0 {radius} 0"
    )


def build_ass_document(
    segments: list[dict[str, Any]],
    *,
    video_width: int,
    video_height: int,
    burn_style: dict[str, Any] | None = None,
) -> tuple[str, list[dict[str, Any]]]:
    profile = build_ass_style_profile(video_width, video_height, burn_style)
    segmented = split_segments_for_timeline(
        segments,
        max_chars=profile.max_chars,
        font_size=profile.font_size,
        safe_text_width=profile.safe_text_width,
    )
    back_alpha = opacity_to_ass_alpha(float((burn_style or {}).get("background_opacity", 0.85)))

    text_style_line = (
        "Style: Text,"
        f"{profile.font_name},{profile.font_size},"
        "&H00FFFFFF,&H00FFFFFF,&H00000000,&H00000000,"
        "0,0,0,0,100,100,0,0,"
        f"1,{profile.outline},{profile.shadow},5,0,0,0,1"
    )
    box_style_line = (
        "Style: Box,"
        f"{profile.font_name},{profile.font_size},"
        f"&H{back_alpha}202020,&H{back_alpha}202020,&H{back_alpha}202020,&H{back_alpha}202020,"
        "0,0,0,0,100,100,0,0,"
        "1,0,0,7,0,0,0,1"
    )

    events: list[str] = []
    for segment in segmented:
        start = ms_to_ass_time(int(segment["start_ms"]))
        end = ms_to_ass_time(int(segment["end_ms"]))
        text = escape_ass_text(str(segment["text"]).strip())
        box_width = max(
            profile.min_box_width,
            min(profile.max_box_width, estimate_text_width(text, profile.font_size) + profile.side_padding * 2),
        )
        box_left = (video_width - box_width) // 2
        box_top = profile.text_pos_y - (profile.box_height // 2)
        round_rect_path = build_round_rect_path(box_width, profile.box_height, profile.corner_radius)
        events.append(
            "Dialogue: 0,"
            f"{start},{end},"
            "Box,,0,0,0,,"
            rf"{{\an7\pos({box_left},{box_top})\bord0\shad0\p1}}{round_rect_path}"
        )
        events.append(
            "Dialogue: 1,"
            f"{start},{end},"
            "Text,,0,0,0,,"
            rf"{{\an5\pos({video_width // 2},{profile.text_pos_y})\q2}}{text}"
        )

    ass_text = "\n".join(
        [
            "[Script Info]",
            "ScriptType: v4.00+",
            "WrapStyle: 2",
            "ScaledBorderAndShadow: yes",
            f"PlayResX: {video_width}",
            f"PlayResY: {video_height}",
            "",
            "[V4+ Styles]",
            "Format: Name,Fontname,Fontsize,PrimaryColour,SecondaryColour,OutlineColour,BackColour,"
            "Bold,Italic,Underline,StrikeOut,ScaleX,ScaleY,Spacing,Angle,BorderStyle,Outline,Shadow,"
            "Alignment,MarginL,MarginR,MarginV,Encoding",
            text_style_line,
            box_style_line,
            "",
            "[Events]",
            "Format: Layer,Start,End,Style,Name,MarginL,MarginR,MarginV,Effect,Text",
            *events,
            "",
        ]
    )
    return ass_text, segmented
