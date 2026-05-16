from __future__ import annotations

import subprocess
from dataclasses import dataclass
from typing import Any

from segmenter import estimate_text_width, split_segments_for_timeline

DEFAULT_RENDER_PRESET = "vizard_classic_cn"
PORTRAIT_REFERENCE = (1080, 1920)
LANDSCAPE_REFERENCE = (1920, 1080)
PORTRAIT_MIN_BOX_WIDTH = 240
LANDSCAPE_MIN_BOX_WIDTH = 300
PORTRAIT_VISUAL_ANCHOR_RATIO = 0.75
# 渲染层独立 boost：base_font_size 走 yaml/默认 50，这两个倍率只控制"渲染时再放大多少"，
# 这样 burn_style.font_size 改不改都能等比生效。用户反馈横/竖向字号都偏小、与背景气泡
# 比例不协调，把基础 boost 调高（横向新增 1.18，竖向 1.08→1.22）。
LANDSCAPE_FONT_BOOST = 1.18
PORTRAIT_FONT_BOOST = 1.22
LANDSCAPE_BOX_HEIGHT_RATIO = 1.90
PORTRAIT_BOX_HEIGHT_RATIO = 1.70
LANDSCAPE_SIDE_PADDING_EM = 0.60
PORTRAIT_SIDE_PADDING_EM = 0.65


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


def scaled_style_value(raw_value: Any, scale: float, minimum: int = 0) -> int:
    value = int(raw_value or 0)
    if value <= 0:
        return 0
    return max(minimum, int(round(value * scale)))


def resolve_single_line_box_height(font_size: int, orientation: str) -> int:
    if orientation == "portrait":
        return max(int(round(font_size * PORTRAIT_BOX_HEIGHT_RATIO)), 44)

    return max(int(round(font_size * LANDSCAPE_BOX_HEIGHT_RATIO)), 52)


def resolve_min_box_width(reference_width: int, font_size: int, scale: float, minimum_chars: int) -> int:
    return scaled_style_value(reference_width, scale, minimum=font_size * minimum_chars)


def derive_max_chars(safe_text_width: int, font_size: int, orientation: str) -> int:
    average_char_width = max(estimate_text_width("字幕", font_size) // 2, 1)
    estimated = max(1, safe_text_width // average_char_width)
    if orientation == "portrait":
        return min(22, max(8, estimated))
    return min(32, max(10, estimated))


def build_ass_style_profile(video_width: int, video_height: int, burn_style: dict[str, Any] | None = None) -> ASSStyleProfile:
    burn_style = burn_style or {}
    font_name = str(burn_style.get("font_name", "Noto Sans CJK SC"))
    base_font_size = int(burn_style.get("font_size", 50))
    # 默认 0 = 纯白文字无黑描边。气泡背景 0.85 不透明度已经提供足够对比度。
    # 历史 yaml 显式写 outline:N 仍然生效。
    base_outline = float(burn_style.get("outline", 0))
    base_shadow = float(burn_style.get("shadow", 0))

    if video_height > video_width:
        ref_width, ref_height = PORTRAIT_REFERENCE
        scale = min(video_width / ref_width, video_height / ref_height)
        font_size = scaled_style_value(base_font_size, scale * PORTRAIT_FONT_BOOST, minimum=20)
        box_height = resolve_single_line_box_height(font_size, "portrait")
        # ASS/libass uses a bottom-safe margin model; legacy bottom_offset belongs to the older card renderer.
        margin_v = scaled_style_value(burn_style.get("margin_v") or 220, scale)
        margin_l = scaled_style_value(54, scale, minimum=16)
        margin_r = scaled_style_value(54, scale, minimum=16)
        configured_width = scaled_style_value(burn_style.get("card_width", 0), scale)
        available_width = max(video_width - margin_l - margin_r, 1)
        max_box_width = min(available_width, configured_width or int(video_width * 0.9))
        min_box_width = min(max_box_width, resolve_min_box_width(PORTRAIT_MIN_BOX_WIDTH, font_size, scale, minimum_chars=4))
        side_padding = max(scaled_style_value(32, scale, minimum=16), int(round(font_size * PORTRAIT_SIDE_PADDING_EM)))
        safe_text_width = max(max_box_width - side_padding * 2, 1)
        text_half_height = box_height // 2
        visual_anchor_y = max(text_half_height, int(round(video_height * PORTRAIT_VISUAL_ANCHOR_RATIO)))
        bottom_safe_y = max(text_half_height, video_height - margin_v - text_half_height)
        return ASSStyleProfile(
            orientation="portrait",
            font_name=font_name,
            font_size=font_size,
            outline=max(round(base_outline * scale), 0),
            shadow=max(round(base_shadow * scale), 0),
            margin_v=margin_v,
            margin_l=margin_l,
            margin_r=margin_r,
            max_chars=derive_max_chars(safe_text_width, font_size, "portrait"),
            min_box_width=min_box_width,
            max_box_width=max_box_width,
            box_height=box_height,
            corner_radius=box_height // 2,
            text_pos_y=min(bottom_safe_y, visual_anchor_y),
            side_padding=side_padding,
            safe_text_width=safe_text_width,
        )

    ref_width, ref_height = LANDSCAPE_REFERENCE
    scale = min(video_width / ref_width, video_height / ref_height)
    font_size = scaled_style_value(base_font_size, scale * LANDSCAPE_FONT_BOOST, minimum=22)
    box_height = resolve_single_line_box_height(font_size, "landscape")
    margin_v = scaled_style_value(burn_style.get("margin_v") or 96, scale)
    margin_l = scaled_style_value(72, scale, minimum=20)
    margin_r = scaled_style_value(72, scale, minimum=20)
    configured_width = scaled_style_value(burn_style.get("card_width", 0), scale)
    available_width = max(video_width - margin_l - margin_r, 1)
    max_box_width = min(available_width, configured_width or int(video_width * 0.78))
    min_box_width = min(max_box_width, resolve_min_box_width(LANDSCAPE_MIN_BOX_WIDTH, font_size, scale, minimum_chars=5))
    side_padding = max(scaled_style_value(32, scale, minimum=18), int(round(font_size * LANDSCAPE_SIDE_PADDING_EM)))
    safe_text_width = max(max_box_width - side_padding * 2, 1)
    return ASSStyleProfile(
        orientation="landscape",
        font_name=font_name,
        font_size=font_size,
        outline=max(round(base_outline * scale), 0),
        shadow=max(round(base_shadow * scale), 0),
        margin_v=margin_v,
        margin_l=margin_l,
        margin_r=margin_r,
        max_chars=derive_max_chars(safe_text_width, font_size, "landscape"),
        min_box_width=min_box_width,
        max_box_width=max_box_width,
        box_height=box_height,
        corner_radius=box_height // 2,
        text_pos_y=max(box_height // 2, video_height - margin_v - (box_height // 2)),
        side_padding=side_padding,
        safe_text_width=safe_text_width,
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
