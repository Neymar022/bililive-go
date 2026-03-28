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

from ass_generator import build_ass_document, normalize_render_preset, probe_video_size


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


def derive_ass_path(output_srt_path: str) -> str:
    return str(Path(output_srt_path).with_suffix(".ass"))


def build_public_file_url(file_path: str, source_root: str, public_url_base: str) -> str:
    relative_path = Path(file_path).resolve().relative_to(Path(source_root).resolve())
    quoted_path = urllib.parse.quote(str(relative_path).replace(os.sep, "/"))
    return f"{public_url_base.rstrip('/')}/files/{quoted_path}"


def build_burn_temp_dir(output_path: str) -> str:
    return str(Path(output_path).resolve().parent / ".subtitle-tmp")


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
    ass_path: str,
    output_path: str,
    style: dict[str, Any],
    ffmpeg_bin: str = "ffmpeg",
) -> str:
    output_dir = os.path.dirname(output_path) or "."
    os.makedirs(output_dir, exist_ok=True)
    temp_dir = build_burn_temp_dir(output_path)
    os.makedirs(temp_dir, exist_ok=True)
    with tempfile.NamedTemporaryFile(prefix="subtitle-burn-", suffix=Path(output_path).suffix, dir=temp_dir, delete=False) as tmp:
        temp_output = tmp.name

    resolved_preset = normalize_render_preset(style.get("preset"))
    escaped_subtitle = ass_path.replace("\\", "\\\\").replace(":", "\\:").replace("'", r"\'")
    filter_arg = f"subtitles='{escaped_subtitle}'"
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

        video_width, video_height = probe_video_size(source_path)
        ass_content, segments = build_ass_document(
            segments,
            video_width=video_width,
            video_height=video_height,
            burn_style=burn_style,
        )
        ass_path = derive_ass_path(output_srt_path)
        Path(ass_path).write_text(ass_content, encoding="utf-8")
        srt_content = segments_to_srt(segments)
        Path(output_srt_path).write_text(srt_content, encoding="utf-8")
        render_preset = burn_subtitles(
            source_path,
            ass_path,
            output_video_path,
            burn_style,
            ffmpeg_bin=ffmpeg_bin,
        )
        return {
            "segments": segments_to_api_payload(segments),
            "ass_path": ass_path,
            "render_preset": render_preset,
        }
    finally:
        if os.path.exists(audio_path):
            os.remove(audio_path)
