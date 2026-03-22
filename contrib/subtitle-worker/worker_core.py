from __future__ import annotations

import json
import os
import subprocess
import tempfile
import time
import urllib.parse
from pathlib import Path
from typing import Any, Optional

import requests
from PIL import Image

from renderers.vizard_renderer import probe_video_size, render_cue_png, resolve_render_preset


DEFAULT_DASHSCOPE_BASE_URL = "https://dashscope.aliyuncs.com"


def ms_to_srt_time(milliseconds: int) -> str:
    hours, remainder = divmod(int(milliseconds), 3_600_000)
    minutes, remainder = divmod(remainder, 60_000)
    seconds, remainder = divmod(remainder, 1_000)
    return f"{hours:02d}:{minutes:02d}:{seconds:02d},{remainder:03d}"


def segments_to_srt(segments: list[dict[str, Any]]) -> str:
    parts: list[str] = []
    for index, segment in enumerate(segments, start=1):
        segment_index = segment.get("index", index)
        start_ms = int(segment["start_ms"])
        end_ms = int(segment["end_ms"])
        text = str(segment["text"]).strip()
        parts.append(
            f"{segment_index}\n"
            f"{ms_to_srt_time(start_ms)} --> {ms_to_srt_time(end_ms)}\n"
            f"{text}\n"
        )
    return "\n".join(parts).strip() + "\n"


def segments_to_api_payload(segments: list[dict[str, Any]]) -> list[dict[str, Any]]:
    payload: list[dict[str, Any]] = []
    for index, segment in enumerate(segments, start=1):
        segment_index = segment.get("index", index)
        start_ms = int(segment["start_ms"])
        end_ms = int(segment["end_ms"])
        payload.append(
            {
                "index": int(segment_index),
                "start": ms_to_srt_time(start_ms),
                "end": ms_to_srt_time(end_ms),
                "text": str(segment["text"]).strip(),
            }
        )
    return payload


def build_force_style(style: dict[str, Any]) -> str:
    entries = {
        "FontName": style.get("font_name", "Noto Sans CJK SC"),
        "FontSize": style.get("font_size", 24),
        "MarginV": style.get("margin_v", 24),
        "Outline": style.get("outline", 2),
        "Shadow": style.get("shadow", 0),
    }
    return ",".join(f"{key}={value}" for key, value in entries.items())


def build_public_file_url(file_path: str, source_root: str, public_url_base: str) -> str:
    relative_path = Path(file_path).resolve().relative_to(Path(source_root).resolve())
    quoted_path = urllib.parse.quote(str(relative_path).replace(os.sep, "/"))
    return f"{public_url_base.rstrip('/')}/files/{quoted_path}"


def build_burn_temp_dir(output_path: str) -> str:
    return str(Path(output_path).resolve().parent / ".subtitle-tmp")


def extract_preview_frame(input_path: str, frame_path: str, ffmpeg_bin: str = "ffmpeg", at_seconds: float = 1.0) -> None:
    cmd = [
        ffmpeg_bin,
        "-y",
        "-ss",
        f"{max(at_seconds, 0):.3f}",
        "-i",
        input_path,
        "-frames:v",
        "1",
        frame_path,
    ]
    subprocess.run(cmd, check=True, capture_output=True)


def render_style_lab_preview(
    source_path: str,
    preview_text: str,
    burn_style: dict[str, Any],
    *,
    output_preview_path: str | None = None,
    frame_time_seconds: float = 1.0,
    ffmpeg_bin: str = "ffmpeg",
) -> dict[str, Any]:
    preview_root = Path(output_preview_path).parent if output_preview_path else None
    with tempfile.TemporaryDirectory(prefix="style-lab-preview-", dir=str(preview_root) if preview_root else None) as temp_dir:
        temp_root = Path(temp_dir)
        frame_path = temp_root / "frame.png"
        cue_path = temp_root / "cue.png"

        extract_preview_frame(source_path, str(frame_path), ffmpeg_bin=ffmpeg_bin, at_seconds=frame_time_seconds)

        with Image.open(frame_path).convert("RGBA") as frame_image:
            video_width, video_height = frame_image.size
            render_result = render_cue_png(
                preview_text,
                str(cue_path),
                video_width=video_width,
                video_height=video_height,
                preset_name=str(burn_style.get("preset", "vizard_classic_cn")),
                font_name=str(burn_style.get("font_name", "Noto Sans CJK SC")),
                font_size=int(burn_style.get("font_size", 24)),
            )
            with Image.open(cue_path).convert("RGBA") as cue_image:
                preview = Image.alpha_composite(frame_image, cue_image)
                if output_preview_path:
                    output_path = Path(output_preview_path)
                    output_path.parent.mkdir(parents=True, exist_ok=True)
                else:
                    output_path = temp_root / "preview.png"
                preview.save(output_path, format="PNG")

        return {
            "preview_image_path": str(output_path),
            "render_preset": render_result["preset"],
        }


def build_style_lab_sample_dir(source_path: str, output_dir: str | None = None) -> Path:
    if output_dir:
        return Path(output_dir).resolve()
    return Path(source_path).resolve().parent / ".style-lab-samples"


def generate_style_lab_sample(
    source_path: str,
    sample_text: str,
    burn_style: dict[str, Any],
    *,
    output_dir: str | None = None,
    start_time_seconds: float = 0,
    duration_seconds: float = 30,
    ffmpeg_bin: str = "ffmpeg",
) -> dict[str, Any]:
    sample_dir = build_style_lab_sample_dir(source_path, output_dir=output_dir)
    sample_dir.mkdir(parents=True, exist_ok=True)

    clip_path = sample_dir / "sample.clip.mp4"
    srt_path = sample_dir / "sample.srt"
    video_path = sample_dir / "sample.burned.mp4"
    duration_ms = max(int(duration_seconds * 1000), 1)
    segments = [
        {
            "index": 1,
            "start_ms": 0,
            "end_ms": duration_ms,
            "text": sample_text.strip() or "字幕样式实验室测试样片",
        }
    ]

    cmd = [
        ffmpeg_bin,
        "-y",
        "-ss",
        f"{max(start_time_seconds, 0):.3f}",
        "-i",
        source_path,
        "-t",
        f"{max(duration_seconds, 0):.3f}",
        "-c",
        "copy",
        str(clip_path),
    ]
    subprocess.run(cmd, check=True, capture_output=True)

    srt_path.write_text(segments_to_srt(segments), encoding="utf-8")
    render_preset = burn_subtitles(
        str(clip_path),
        str(srt_path),
        str(video_path),
        burn_style,
        ffmpeg_bin=ffmpeg_bin,
        segments=segments,
    )
    return {
        "sample_video_path": str(video_path),
        "sample_srt_path": str(srt_path),
        "render_preset": render_preset,
    }


def build_overlay_filter(segments: list[dict[str, Any]]) -> str:
    if not segments:
        return "[0:v]copy[outv]"

    filter_parts: list[str] = []
    current_label = "[0:v]"
    for index, segment in enumerate(segments, start=1):
        next_label = "[outv]" if index == len(segments) else f"[v{index}]"
        start_seconds = max(float(segment["start_ms"]) / 1000, 0)
        end_seconds = max(float(segment["end_ms"]) / 1000, start_seconds)
        filter_parts.append(
            f"{current_label}[{index}:v]overlay=0:0:enable='between(t,{start_seconds:.3f},{end_seconds:.3f})'{next_label}"
        )
        current_label = next_label
    return ";".join(filter_parts)


def normalize_dashscope_base_url(base_url: str) -> str:
    parsed = urllib.parse.urlparse(base_url)
    if not parsed.scheme or not parsed.netloc:
        raise ValueError(f"无效的 DashScope base_url: {base_url}")
    return f"{parsed.scheme}://{parsed.netloc}"


def create_dashscope_session() -> requests.Session:
    session = requests.Session()
    session.trust_env = False
    return session


def upload_file_to_dashscope_oss(file_path: str, api_key: str, model: str, base_url: str = DEFAULT_DASHSCOPE_BASE_URL) -> str:
    root_url = normalize_dashscope_base_url(base_url)
    session = create_dashscope_session()
    policy_response = session.get(
        f"{root_url}/api/v1/uploads?action=getPolicy&model={urllib.parse.quote(model)}",
        headers={"Authorization": f"Bearer {api_key}"},
        timeout=30,
    )
    policy_response.raise_for_status()
    payload = policy_response.json()["data"]

    file_name = Path(file_path).name
    object_key = f"{payload['upload_dir'].rstrip('/')}/{file_name}"

    form_data = {
        "key": object_key,
        "policy": payload["policy"],
        "Signature": payload["signature"],
        "OSSAccessKeyId": payload["oss_access_key_id"],
        "x-oss-object-acl": payload["x_oss_object_acl"],
        "x-oss-forbid-overwrite": payload["x_oss_forbid_overwrite"],
        "success_action_status": "200",
    }
    with open(file_path, "rb") as file_obj:
        upload_response = session.post(
            payload["upload_host"],
            data=form_data,
            files={"file": (file_name, file_obj)},
            timeout=60,
        )
    upload_response.raise_for_status()
    return f"oss://{object_key}"


def dashscope_result_to_segments(payload: dict[str, Any]) -> list[dict[str, Any]]:
    transcripts = payload.get("transcripts", [])
    segments: list[dict[str, Any]] = []
    for transcript in transcripts:
        for sentence in transcript.get("sentences", []):
            segments.append(
                {
                    "index": int(sentence.get("sentence_id", len(segments) + 1)) + 1,
                    "start_ms": int(sentence.get("begin_time", 0)),
                    "end_ms": int(sentence.get("end_time", 0)),
                    "text": str(sentence.get("text", "")).strip(),
                }
            )
    return segments


def extract_audio(input_path: str, audio_path: str, ffmpeg_bin: str = "ffmpeg") -> None:
    cmd = [
        ffmpeg_bin,
        "-y",
        "-i",
        input_path,
        "-vn",
        "-ac",
        "1",
        "-ar",
        "16000",
        audio_path,
    ]
    subprocess.run(cmd, check=True, capture_output=True)


def burn_subtitles(
    input_path: str,
    subtitle_path: str,
    output_path: str,
    style: dict[str, Any],
    ffmpeg_bin: str = "ffmpeg",
    *,
    segments: Optional[list[dict[str, Any]]] = None,
) -> str:
    output_dir = os.path.dirname(output_path) or "."
    os.makedirs(output_dir, exist_ok=True)
    temp_dir = build_burn_temp_dir(output_path)
    os.makedirs(temp_dir, exist_ok=True)
    with tempfile.NamedTemporaryFile(prefix="subtitle-burn-", suffix=Path(output_path).suffix, dir=temp_dir, delete=False) as tmp:
        temp_output = tmp.name

    resolved_preset = resolve_render_preset(style.get("preset"))

    cue_paths: list[str] = []
    if resolved_preset == "vizard_classic_cn" and segments:
        video_width, video_height = probe_video_size(input_path)
        for segment in segments:
            cue_path = Path(temp_dir) / f"cue-{int(segment.get('index', len(cue_paths) + 1)):04d}.png"
            render_cue_png(
                str(segment.get("text", "")),
                str(cue_path),
                video_width=video_width,
                video_height=video_height,
                preset_name=resolved_preset,
                font_name=str(style.get("font_name", "Noto Sans CJK SC")),
                font_size=int(style.get("font_size", 24)),
            )
            cue_paths.append(str(cue_path))

        cmd = [ffmpeg_bin, "-y", "-i", input_path]
        for cue_path in cue_paths:
            cmd.extend(["-i", cue_path])
        cmd.extend(
            [
                "-filter_complex",
                build_overlay_filter(segments),
                "-map",
                "[outv]",
                "-map",
                "0:a?",
                "-c:v",
                "libx264",
                "-pix_fmt",
                "yuv420p",
                "-c:a",
                "copy",
                temp_output,
            ]
        )
    else:
        escaped_subtitle = subtitle_path.replace("\\", "\\\\").replace(":", "\\:").replace("'", r"\'")
        force_style = build_force_style(style).replace("'", r"\'")
        filter_arg = f"subtitles='{escaped_subtitle}':force_style='{force_style}'"
        cmd = [
            ffmpeg_bin,
            "-y",
            "-i",
            input_path,
            "-vf",
            filter_arg,
            "-c:a",
            "copy",
            temp_output,
        ]
    try:
        subprocess.run(cmd, check=True, capture_output=True)
        os.replace(temp_output, output_path)
    except Exception:
        if os.path.exists(temp_output):
            os.remove(temp_output)
        raise
    finally:
        try:
            os.rmdir(temp_dir)
        except OSError:
            pass
    return resolved_preset


def run_dashscope_transcription(
    audio_url: str,
    language: str,
    api_key: str,
    model: str,
    base_url: str = DEFAULT_DASHSCOPE_BASE_URL,
    *,
    resolve_oss_resource: bool = False,
) -> list[dict[str, Any]]:
    root_url = normalize_dashscope_base_url(base_url)
    session = create_dashscope_session()
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
        "X-DashScope-Async": "enable",
    }
    if resolve_oss_resource:
        headers["X-DashScope-OssResourceResolve"] = "enable"
    submit_response = session.post(
        f"{root_url}/api/v1/services/audio/asr/transcription",
        headers=headers,
        json={
            "model": model,
            "input": {"file_url": audio_url},
            "parameters": {"language_hints": [language]},
        },
        timeout=30,
    )
    submit_response.raise_for_status()
    task_id = submit_response.json()["output"]["task_id"]

    status_url = f"{root_url}/api/v1/tasks/{task_id}"
    while True:
        task_response = session.get(
            status_url,
            headers={"Authorization": f"Bearer {api_key}"},
            timeout=30,
        )
        task_response.raise_for_status()
        task_payload = task_response.json()
        task_status = task_payload["output"]["task_status"]
        if task_status == "SUCCEEDED":
            output = task_payload["output"]
            if "result" in output:
                result_url = output["result"]["transcription_url"]
            else:
                result_url = output["results"][0]["transcription_url"]
            result_response = session.get(result_url, timeout=30)
            result_response.raise_for_status()
            return dashscope_result_to_segments(result_response.json())
        if task_status in {"FAILED", "CANCELED"}:
            raise RuntimeError(f"dashscope transcription failed: {json.dumps(task_payload, ensure_ascii=False)}")
        time.sleep(2)


def run_local_whisper(audio_path: str, language: str, model: str, compute_type: str) -> list[dict[str, Any]]:
    from faster_whisper import WhisperModel

    whisper_model = WhisperModel(model, device="cpu", compute_type=compute_type)
    segments, _ = whisper_model.transcribe(audio_path, language=language or None)

    output: list[dict[str, Any]] = []
    for index, segment in enumerate(segments, start=1):
        output.append(
            {
                "index": index,
                "start_ms": int(segment.start * 1000),
                "end_ms": int(segment.end * 1000),
                "text": segment.text.strip(),
            }
        )
    return output


def transcribe_and_burn(
    source_path: str,
    output_video_path: str,
    output_srt_path: str,
    provider: str,
    language: str,
    burn_style: dict[str, Any],
    *,
    ffmpeg_bin: str = "ffmpeg",
    source_root: Optional[str] = None,
    public_url_base: Optional[str] = None,
    dashscope_api_key: Optional[str] = None,
    dashscope_base_url: str = DEFAULT_DASHSCOPE_BASE_URL,
    dashscope_model: str = "qwen3-asr-flash-filetrans",
    local_model: str = "small",
    local_compute_type: str = "int8",
) -> dict[str, Any]:
    output_dir = os.path.dirname(output_video_path) or "."
    os.makedirs(output_dir, exist_ok=True)
    os.makedirs(os.path.dirname(output_srt_path) or ".", exist_ok=True)

    with tempfile.NamedTemporaryFile(prefix="subtitle-audio-", suffix=".wav", dir=output_dir, delete=False) as temp_audio:
        audio_path = temp_audio.name

    try:
        extract_audio(source_path, audio_path, ffmpeg_bin=ffmpeg_bin)
        if provider == "dashscope":
            if not dashscope_api_key:
                raise RuntimeError("缺少 DASHSCOPE_API_KEY")
            resolve_oss_resource = False
            if source_root and public_url_base:
                audio_url = build_public_file_url(audio_path, source_root, public_url_base)
            else:
                audio_url = upload_file_to_dashscope_oss(audio_path, dashscope_api_key, dashscope_model, dashscope_base_url)
                resolve_oss_resource = True
            segments = run_dashscope_transcription(
                audio_url,
                language,
                dashscope_api_key,
                dashscope_model,
                dashscope_base_url,
                resolve_oss_resource=resolve_oss_resource,
            )
        elif provider == "local-whisper":
            segments = run_local_whisper(audio_path, language, local_model, local_compute_type)
        else:
            raise RuntimeError(f"不支持的字幕 provider: {provider}")

        srt_content = segments_to_srt(segments)
        Path(output_srt_path).write_text(srt_content, encoding="utf-8")
        render_preset = burn_subtitles(
            source_path,
            output_srt_path,
            output_video_path,
            burn_style,
            ffmpeg_bin=ffmpeg_bin,
            segments=segments,
        )
        return {
            "segments": segments_to_api_payload(segments),
            "render_preset": render_preset,
        }
    finally:
        if os.path.exists(audio_path):
            os.remove(audio_path)
