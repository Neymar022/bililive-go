from __future__ import annotations

import re
import subprocess
from pathlib import Path
from typing import Any

from PIL import Image, ImageChops, ImageColor, ImageDraw, ImageFont

from .layout import layout_vizard_classic_cn


TEMPLATES_ROOT = Path(__file__).resolve().parent / "templates"
FALLBACK_FONT_PATHS = [
    "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
    "/usr/share/fonts/opentype/noto/NotoSansCJKSC-Regular.otf",
    "/System/Library/Fonts/Hiragino Sans GB.ttc",
    "/Library/Fonts/Arial Unicode.ttf",
]

PRESET_REGISTRY = {
    "bottom_center": "vizard_classic_cn",
    "vizard_classic_cn": "vizard_classic_cn",
}

CSS_VAR_RE = re.compile(r"--([a-z0-9-]+):\s*([^;]+);")


def resolve_render_preset(name: str | None) -> str:
    normalized = (name or "").strip() or "vizard_classic_cn"
    return PRESET_REGISTRY.get(normalized, "vizard_classic_cn")


def _load_style_vars(preset_name: str) -> dict[str, str]:
    preset_dir = TEMPLATES_ROOT / preset_name
    template_path = preset_dir / "template.html"
    style_path = preset_dir / "style.css"
    if not template_path.exists():
        raise FileNotFoundError(f"缺少模板文件: {template_path}")
    if not style_path.exists():
        raise FileNotFoundError(f"缺少样式文件: {style_path}")
    text = style_path.read_text(encoding="utf-8")
    return {match.group(1): match.group(2).strip() for match in CSS_VAR_RE.finditer(text)}


def _parse_css_color(value: str) -> tuple[int, int, int, int]:
    if value.startswith("rgba(") and value.endswith(")"):
        parts = [part.strip() for part in value[5:-1].split(",")]
        red, green, blue = (int(parts[0]), int(parts[1]), int(parts[2]))
        alpha = int(float(parts[3]) * 255)
        return red, green, blue, alpha
    red, green, blue = ImageColor.getrgb(value)
    return red, green, blue, 255


def _resolve_font_path(font_name: str) -> str:
    lowered = font_name.lower()
    candidates = []
    if "noto" in lowered:
        candidates.extend(FALLBACK_FONT_PATHS)
    candidates.extend(path for path in FALLBACK_FONT_PATHS if path not in candidates)
    for path in candidates:
        if Path(path).exists():
            return path
    raise FileNotFoundError(f"未找到可用字体: {font_name}")


def _load_font(font_name: str, font_size: int) -> ImageFont.FreeTypeFont | ImageFont.ImageFont:
    try:
        return ImageFont.truetype(_resolve_font_path(font_name), font_size)
    except Exception:
        return ImageFont.load_default()


def _measure_text(draw: ImageDraw.ImageDraw, font: ImageFont.FreeTypeFont | ImageFont.ImageFont, text: str) -> tuple[int, int]:
    bbox = draw.textbbox((0, 0), text, font=font, anchor="lt")
    return bbox[2] - bbox[0], bbox[3] - bbox[1]


def render_cue_png(
    text: str,
    output_path: str,
    *,
    video_width: int,
    video_height: int,
    preset_name: str = "vizard_classic_cn",
    font_name: str = "Noto Sans CJK SC",
    font_size: int = 24,
) -> dict[str, Any]:
    resolved_preset = resolve_render_preset(preset_name)
    style_vars = _load_style_vars(resolved_preset)
    layout = layout_vizard_classic_cn(text, video_width=video_width)

    canvas = Image.new("RGBA", (video_width, video_height), (0, 0, 0, 0))
    draw = ImageDraw.Draw(canvas)
    font = _load_font(font_name, font_size)

    card_width = int(layout["card_width"])
    card_height = int(layout["card_height"])
    bottom_offset = int(style_vars.get("bottom-offset", layout["bottom_offset"]))
    card_radius = int(style_vars.get("card-radius", "36"))
    card_fill = _parse_css_color(style_vars.get("card-bg", "rgba(18, 18, 20, 0.9)"))
    card_stroke = _parse_css_color(style_vars.get("card-border", "rgba(255, 255, 255, 0.08)"))
    text_color = _parse_css_color(style_vars.get("text-color", "#ffffff"))
    shadow_color = _parse_css_color(style_vars.get("shadow-color", "rgba(0, 0, 0, 0.18)"))

    left = (video_width - card_width) // 2
    top = video_height - bottom_offset - card_height
    right = left + card_width
    bottom = top + card_height

    shadow_offset = int(style_vars.get("shadow-offset", "10"))
    draw.rounded_rectangle(
        (left, top + shadow_offset, right, bottom + shadow_offset),
        radius=card_radius,
        fill=shadow_color,
    )
    draw.rounded_rectangle(
        (left, top, right, bottom),
        radius=card_radius,
        fill=card_fill,
        outline=card_stroke,
        width=int(style_vars.get("card-border-width", "2")),
    )

    line_gap = int(layout["line_gap"])
    text_sizes = [_measure_text(draw, font, line) for line in layout["lines"]]
    total_text_height = sum(height for _, height in text_sizes) + line_gap * max(0, len(text_sizes) - 1)
    current_y = top + (card_height - total_text_height) // 2
    center_x = video_width // 2
    for line, (_, height) in zip(layout["lines"], text_sizes):
        draw.text((center_x, current_y), line, font=font, fill=text_color, anchor="ma")
        current_y += height + line_gap

    path = Path(output_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    canvas.save(path, format="PNG")
    return {
        **layout,
        "preset": resolved_preset,
        "image_path": str(path),
    }


def compare_png_with_golden(actual_path: str, golden_path: str, tolerance: int = 0) -> bool:
    actual = Image.open(actual_path).convert("RGBA")
    golden = Image.open(golden_path).convert("RGBA")
    if actual.size != golden.size:
        return False
    diff = ImageChops.difference(actual, golden)
    if diff.getbbox() is None:
        return True
    if tolerance <= 0:
        return False
    extrema = diff.getextrema()
    max_channel_diff = max(channel[1] for channel in extrema)
    return max_channel_diff <= tolerance


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
