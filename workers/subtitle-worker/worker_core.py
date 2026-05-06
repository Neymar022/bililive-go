from __future__ import annotations

import errno
import json
import logging
import os
import shutil
import subprocess
import tempfile
import time
import urllib.parse
from functools import lru_cache
from pathlib import Path
from typing import Any, Optional

import requests

_logger = logging.getLogger(__name__)

from ass_generator import build_ass_document, normalize_render_preset, probe_video_size


DEFAULT_DASHSCOPE_BASE_URL = "https://dashscope.aliyuncs.com"
DEFAULT_VIDEO_ENCODER = "software"
DEFAULT_VAAPI_DEVICE = "/dev/dri/renderD128"
DEFAULT_VAAPI_QP = 23
FINALIZE_RETRY_ERRNOS = {errno.ENOENT, errno.EACCES, errno.EPERM, errno.EBUSY}
DEFAULT_OUTPUT_VISIBILITY_TIMEOUT_SECONDS = 60.0
DEFAULT_OUTPUT_VISIBILITY_POLL_INTERVAL_SECONDS = 0.25
DEFAULT_OUTPUT_VISIBILITY_STABLE_POLLS = 2


class WorkerSafeError(RuntimeError):
    """异常 message 已经过审核，可以安全地回显给 HTTP 客户端。

    用此类型代替 RuntimeError 来表示"这条错误信息不会泄露内部路径、API key、
    ffmpeg stderr 片段等敏感内容"。app.py 的异常处理器会把它作为 detail 暴露；
    其余未标记的异常一律回显为通用错误，避免信息泄露。

    判断标准：
    - message 是开发者写死的中文短语 → 安全（如 "缺少 DASHSCOPE_API_KEY"）。
    - message 含 f-string 拼接的 stderr/路径/URL → 不安全，保持 RuntimeError。
    """


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
            segment = {
                "index": int(sentence.get("sentence_id", len(segments) + 1)) + 1,
                "start_ms": int(sentence.get("begin_time", 0)),
                "end_ms": int(sentence.get("end_time", 0)),
                "text": str(sentence.get("text", "")).strip(),
            }
            words = sentence.get("words") or []
            if words:
                segment["tokens"] = [
                    {
                        "text": f"{str(word.get('text', '')).strip()}{str(word.get('punctuation', '')).strip()}",
                        "start_ms": int(word.get("begin_time", 0)),
                        "end_ms": int(word.get("end_time", 0)),
                    }
                    for word in words
                    if str(word.get("text", "")).strip()
                ]
            segments.append(segment)
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


def summarize_command_output(output: str | None, max_chars: int = 2048) -> str:
    if output is None:
        return ""
    if isinstance(output, bytes):
        text = output.decode("utf-8", errors="ignore").strip()
    elif isinstance(output, str):
        text = output.strip()
    else:
        return ""
    if len(text) <= max_chars:
        return text
    return text[-max_chars:]


def is_non_empty_file(path: str) -> bool:
    try:
        return os.path.exists(path) and os.path.getsize(path) > 0
    except OSError:
        return False


def get_non_empty_file_size(path: str) -> int | None:
    try:
        if not os.path.exists(path):
            return None
        size = os.path.getsize(path)
    except OSError:
        return None
    if size <= 0:
        return None
    return size


def wait_for_output_file(
    path: str,
    timeout_seconds: float = DEFAULT_OUTPUT_VISIBILITY_TIMEOUT_SECONDS,
    poll_interval_seconds: float = DEFAULT_OUTPUT_VISIBILITY_POLL_INTERVAL_SECONDS,
    stable_polls: int = DEFAULT_OUTPUT_VISIBILITY_STABLE_POLLS,
) -> bool:
    deadline = time.monotonic() + timeout_seconds
    last_size: int | None = None
    stable_count = 0
    while True:
        size = get_non_empty_file_size(path)
        if size is not None:
            if size == last_size:
                stable_count += 1
            else:
                last_size = size
                stable_count = 1
            if stable_count >= stable_polls:
                return True
        else:
            last_size = None
            stable_count = 0
        if time.monotonic() >= deadline:
            return False
        time.sleep(poll_interval_seconds)


def should_retry_finalize_error(exc: OSError) -> bool:
    if isinstance(exc, FileNotFoundError):
        return True
    return exc.errno in FINALIZE_RETRY_ERRNOS


def publish_burned_output(temp_output: str, output_path: str, retries: int = 5, backoff_seconds: float = 0.2) -> None:
    for attempt in range(retries):
        try:
            os.replace(temp_output, output_path)
            return
        except OSError as exc:
            is_last_attempt = attempt == retries - 1
            if is_last_attempt or not should_retry_finalize_error(exc):
                raise
            time.sleep(backoff_seconds)


def copy_burned_output(src_path: str, dst_path: str, chunk_size: int = 1024 * 1024) -> None:
    with open(src_path, "rb") as src, open(dst_path, "wb") as dst:
        shutil.copyfileobj(src, dst, length=chunk_size)
        dst.flush()
        os.fsync(dst.fileno())


def normalize_video_encoder(video_encoder: str | None) -> str:
    normalized = (video_encoder or DEFAULT_VIDEO_ENCODER).strip().lower()
    if not normalized:
        return DEFAULT_VIDEO_ENCODER
    if normalized in {"software", "vaapi"}:
        return normalized
    raise ValueError(f"不支持的视频编码模式: {video_encoder}")


def build_burn_subtitles_command(
    *,
    input_path: str,
    ass_path: str,
    temp_output: str,
    ffmpeg_bin: str = "ffmpeg",
    video_encoder: str = DEFAULT_VIDEO_ENCODER,
    vaapi_device: str = DEFAULT_VAAPI_DEVICE,
    vaapi_qp: int = DEFAULT_VAAPI_QP,
) -> list[str]:
    normalized_encoder = normalize_video_encoder(video_encoder)
    escaped_subtitle = ass_path.replace("\\", "\\\\").replace(":", "\\:").replace("'", r"\'")
    filter_arg = f"subtitles='{escaped_subtitle}'"
    cmd = [ffmpeg_bin, "-y"]
    if normalized_encoder == "vaapi":
        cmd.extend(
            [
                "-vaapi_device",
                vaapi_device,
                "-i",
                input_path,
                "-vf",
                f"{filter_arg},format=nv12,hwupload",
                "-c:v",
                "h264_vaapi",
                "-qp",
                str(int(vaapi_qp)),
                "-c:a",
                "copy",
                temp_output,
            ]
        )
        return cmd

    cmd.extend(
        [
            "-i",
            input_path,
            "-vf",
            filter_arg,
            "-c:a",
            "copy",
            temp_output,
        ]
    )
    return cmd


def burn_subtitles(
    input_path: str,
    ass_path: str,
    output_path: str,
    style: dict[str, Any],
    ffmpeg_bin: str = "ffmpeg",
    video_encoder: str = DEFAULT_VIDEO_ENCODER,
    vaapi_device: str = DEFAULT_VAAPI_DEVICE,
    vaapi_qp: int = DEFAULT_VAAPI_QP,
) -> str:
    output_dir = str(Path(output_path).parent)
    os.makedirs(output_dir, exist_ok=True)
    resolved_preset = normalize_render_preset(style.get("preset"))

    with tempfile.TemporaryDirectory(prefix="subtitle-burn-") as temp_dir:
        temp_output = str(Path(temp_dir) / Path(output_path).name)
        cmd = build_burn_subtitles_command(
            input_path=input_path,
            ass_path=ass_path,
            temp_output=temp_output,
            ffmpeg_bin=ffmpeg_bin,
            video_encoder=video_encoder,
            vaapi_device=vaapi_device,
            vaapi_qp=vaapi_qp,
        )
        try:
            completed = subprocess.run(cmd, check=True, capture_output=True, text=True)
            stderr_tail = summarize_command_output(completed.stderr)
            if not wait_for_output_file(temp_output):
                detail = f"subtitle finalize failed: burned output was not visible after ffmpeg exited: {temp_output}"
                if stderr_tail:
                    detail = f"{detail}; ffmpeg stderr tail: {stderr_tail}"
                raise RuntimeError(detail)
            try:
                with tempfile.TemporaryDirectory(prefix="subtitle-publish-", dir=output_dir) as publish_dir:
                    staged_output = str(Path(publish_dir) / Path(output_path).name)
                    copy_burned_output(temp_output, staged_output)
                    if not is_non_empty_file(staged_output):
                        detail = f"subtitle finalize failed: staged burned output copy was not visible: {staged_output}"
                        if stderr_tail:
                            detail = f"{detail}; ffmpeg stderr tail: {stderr_tail}"
                        raise RuntimeError(detail)
                    publish_burned_output(staged_output, output_path)
            except OSError as exc:
                detail = f"subtitle finalize failed: publish burned output to {output_path} failed: {exc}"
                if stderr_tail:
                    detail = f"{detail}; ffmpeg stderr tail: {stderr_tail}"
                raise RuntimeError(detail) from exc
        except subprocess.CalledProcessError as exc:
            stderr = summarize_command_output(exc.stderr)
            stdout = summarize_command_output(exc.stdout)
            detail = stderr or stdout or str(exc)
            raise RuntimeError(f"ffmpeg burn failed: {detail}") from exc
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
            "parameters": {
                "language_hints": [language],
                "enable_words": True,
            },
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
            output = task_payload.get("output", {})
            if output.get("code") == "SUCCESS_WITH_NO_VALID_FRAGMENT":
                return []
            raise RuntimeError(f"dashscope transcription failed: {json.dumps(task_payload, ensure_ascii=False)}")
        time.sleep(2)


@lru_cache(maxsize=1)
def _get_whisper_model(model: str, compute_type: str):
    """按 (model, compute_type) 缓存 WhisperModel 实例。

    每个 small 模型权重约 240MB，每次新建会反复读盘并重做 int8 量化（5-30 秒）。
    maxsize=1：单 worker 通常只用一种模型；用户切换 model 时旧的被淘汰，
    长期只占一份显存/内存。如需同时持有多个模型可调大此值。
    """
    from faster_whisper import WhisperModel

    return WhisperModel(model, device="cpu", compute_type=compute_type)


def run_local_whisper(audio_path: str, language: str, model: str, compute_type: str) -> list[dict[str, Any]]:
    whisper_model = _get_whisper_model(model, compute_type)
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


# Mac mlx-whisper 单次任务上限：2 小时直播音频在 large-v3-turbo + Apple GPU 大约
# 8-25 分钟；多任务 FIFO 排队后单条等候时长可能数倍——3600s 是宽裕的总超时。
DEFAULT_MAC_TRANSCRIBER_TIMEOUT_SECONDS = 3600

# 健康探测的默认超时。1.5s 在同 LAN 健康响应时绰绰有余，但在以下场景会假阴性：
#   - 跨网段（NAS 在 1.x，Mac 在 2.x）路由首包 800-1500ms
#   - Mac 刚从睡眠唤醒，mDNS/ARP 解析 1-3s
#   - BiliNote uvicorn 启动后第一次 /healthz 请求穿过尚未热启的 Python import
# 3.0s 兼顾"快速降级"与"避免误判而走云端"，可经 SUBTITLE_MAC_HEALTH_TIMEOUT_SECONDS
# 调整。
DEFAULT_MAC_HEALTH_TIMEOUT_SECONDS = 3.0


def run_remote_mac_mlx(
    audio_path: str,
    language: str,
    mac_url: str,
    mac_token: Optional[str],
    timeout_seconds: float = DEFAULT_MAC_TRANSCRIBER_TIMEOUT_SECONDS,
) -> list[dict[str, Any]]:
    """通过 HTTP 上传音频到 BiliNote 的 mac_transcriber_service，
    复用其 mlx-whisper + Apple Silicon GPU 算力。

    上游服务（BiliNote/mac_transcriber_service）已用 threading.Semaphore(1)
    串行 GPU 访问，多任务并发 POST 时 HTTP 层接受、应用层 FIFO 排队，
    所以这里**不需要再加锁**——单次请求阻塞等响应即可。

    BiliNote 返回 schema：
        { "language": "zh", "full_text": "...",
          "segments": [{"start": 0.0, "end": 2.5, "text": "..."}, ...] }
    转成 worker_core 内部使用的 ms-based 格式。
    """
    headers = {}
    if mac_token:
        headers["Authorization"] = f"Bearer {mac_token}"

    # 用 with 显式管理 Session 生命周期：long-running NAS worker 进程每天数次到
    # 数十次转写，未关闭的 Session 会让 urllib3 连接池累积 fd——几个月不重启
    # 容器后会撞 ulimit。trust_env=False 跟 dashscope 那边保持一致，避免读
    # HTTP_PROXY 把局域网请求转出公网。
    with requests.Session() as session:
        session.trust_env = False
        with open(audio_path, "rb") as f:
            files = {"audio": (os.path.basename(audio_path), f, "audio/wav")}
            response = session.post(
                f"{mac_url.rstrip('/')}/transcribe",
                headers=headers,
                files=files,
                timeout=timeout_seconds,
            )

    response.raise_for_status()
    payload = response.json()

    output: list[dict[str, Any]] = []
    for index, segment in enumerate(payload.get("segments", []), start=1):
        try:
            start_ms = int(float(segment["start"]) * 1000)
            end_ms = int(float(segment["end"]) * 1000)
        except (KeyError, TypeError, ValueError):
            # 异常段直接跳过——MLX 偶发 NaN/inf 时间戳，BiliNote 那边已修了，
            # 这里再加一道兜底防御性。
            continue
        text = str(segment.get("text", "")).strip()
        if not text:
            continue
        output.append(
            {
                "index": index,
                "start_ms": start_ms,
                "end_ms": end_ms,
                "text": text,
            }
        )
    return output


def check_mac_transcriber_health(
    mac_url: str,
    mac_token: Optional[str],
    timeout_seconds: float = DEFAULT_MAC_HEALTH_TIMEOUT_SECONDS,
) -> bool:
    """快速探测 Mac mlx-whisper 服务是否在线，给 ProviderChain 自动降级用。

    返回 False 的语义分两种：
      - 网络/超时/连接拒绝 → Mac 真不在线
      - 401 未授权 → token 配错（NAS 端的 SUBTITLE_MAC_TRANSCRIBER_TOKEN 与
        BiliNote 端 MAC_TRANSCRIBER_TOKEN 不一致）；这里**主动打 WARNING**
        让运维不至于"Mac 健康却始终走云端账单飙升"。
    """
    headers = {}
    if mac_token:
        headers["Authorization"] = f"Bearer {mac_token}"
    try:
        # with 包 Session 同上：避免长跑进程的连接池泄漏。
        with requests.Session() as session:
            session.trust_env = False
            response = session.get(
                f"{mac_url.rstrip('/')}/healthz",
                headers=headers,
                timeout=timeout_seconds,
            )
    except Exception as exc:  # noqa: BLE001 - 探测失败一律视为不健康
        _logger.info("mac transcriber health probe failed (%s): %s", mac_url, exc)
        return False

    if response.status_code == 401:
        _logger.warning(
            "mac transcriber rejected health probe with 401: token 错配？"
            "检查 NAS SUBTITLE_MAC_TRANSCRIBER_TOKEN 与 Mac MAC_TRANSCRIBER_TOKEN 是否一致 (%s)",
            mac_url,
        )
        return False
    return response.status_code == 200


def _transcribe_with_provider(
    provider: str,
    audio_path: str,
    language: str,
    *,
    source_root: Optional[str],
    public_url_base: Optional[str],
    dashscope_api_key: Optional[str],
    dashscope_base_url: str,
    dashscope_model: str,
    local_model: str,
    local_compute_type: str,
    mac_transcriber_url: Optional[str],
    mac_transcriber_token: Optional[str],
    mac_transcriber_timeout_seconds: float,
) -> list[dict[str, Any]]:
    """根据 provider 名跑对应的 ASR 引擎，返回 segments。
    抽出来是为了让 ProviderChain fallback 逻辑能复用：转写阶段失败可切下一档，
    烧录阶段一旦开始就不用再切（segments 有了，留给 ffmpeg 一次跑完）。
    """
    if provider == "dashscope":
        if not dashscope_api_key:
            raise WorkerSafeError("缺少 DASHSCOPE_API_KEY")
        resolve_oss_resource = False
        if source_root and public_url_base:
            audio_url = build_public_file_url(audio_path, source_root, public_url_base)
        else:
            audio_url = upload_file_to_dashscope_oss(audio_path, dashscope_api_key, dashscope_model, dashscope_base_url)
            resolve_oss_resource = True
        return run_dashscope_transcription(
            audio_url,
            language,
            dashscope_api_key,
            dashscope_model,
            dashscope_base_url,
            resolve_oss_resource=resolve_oss_resource,
        )
    if provider == "local-whisper":
        return run_local_whisper(audio_path, language, local_model, local_compute_type)
    if provider == "remote-mac-mlx":
        if not mac_transcriber_url:
            raise WorkerSafeError("缺少 SUBTITLE_MAC_TRANSCRIBER_URL")
        return run_remote_mac_mlx(
            audio_path,
            language,
            mac_transcriber_url,
            mac_transcriber_token,
            timeout_seconds=mac_transcriber_timeout_seconds,
        )
    raise WorkerSafeError(f"不支持的字幕 provider: {provider}")


def _transcribe_with_chain(
    provider_chain: list[str],
    audio_path: str,
    language: str,
    **kwargs: Any,
) -> tuple[str, list[dict[str, Any]]]:
    """按 provider_chain 顺序尝试，返回 (实际成功的 provider, segments)。

    fallback 触发条件：
    - remote-mac-mlx：先 GET /healthz，不通 → 跳过（不消耗 timeout）
    - 任何 provider 抛 WorkerSafeError（明确 message）→ 记下来，跳下一个
    - 任何 provider 抛其它异常（网络/dashscope task 失败/未知）→ 也跳下一个

    异常路径有两种语义区分：
      1. 链是空的（配置错） → WorkerSafeError 提示用户配 SUBTITLE_PROVIDER_CHAIN
      2. 链非空但全跳过/全失败 → 抛最后一次真实异常（携带原因）；
         若全被健康检查跳过 → WorkerSafeError 提示运维所有 provider 都不在线
    """
    if not provider_chain:
        raise WorkerSafeError(
            "provider chain 为空：请配置 SUBTITLE_PROVIDER_CHAIN，"
            "例 SUBTITLE_PROVIDER_CHAIN=remote-mac-mlx,dashscope,local-whisper"
        )

    last_error: Optional[Exception] = None
    attempt_log: list[tuple[str, str]] = []  # 结构化记录每档结局，便于运维查日志

    health_timeout = float(os.getenv("SUBTITLE_MAC_HEALTH_TIMEOUT_SECONDS", str(DEFAULT_MAC_HEALTH_TIMEOUT_SECONDS)))

    for provider in provider_chain:
        # remote-mac-mlx 有便宜的健康探测，不健康直接跳——避免用 60s+ timeout 才发现
        if provider == "remote-mac-mlx":
            mac_url = kwargs.get("mac_transcriber_url")
            if not mac_url or not check_mac_transcriber_health(
                mac_url,
                kwargs.get("mac_transcriber_token"),
                timeout_seconds=health_timeout,
            ):
                attempt_log.append((provider, "skipped:health"))
                continue

        try:
            segments = _transcribe_with_provider(provider, audio_path, language, **kwargs)
            attempt_log.append((provider, "ok"))
            _logger.info("subtitle chain chose provider=%s after attempts=%s", provider, attempt_log)
            return provider, segments
        except Exception as exc:  # noqa: BLE001 - 故意拦截一切，让链能往下走
            last_error = exc
            attempt_log.append((provider, f"failed:{type(exc).__name__}"))
            _logger.warning("subtitle chain provider=%s failed: %s", provider, exc)
            continue

    # 所有 provider 都跳/失败；若 last_error 为空说明全被健康检查跳过
    _logger.error("subtitle chain exhausted, attempts=%s", attempt_log)
    if last_error is not None:
        raise last_error
    raise WorkerSafeError(f"provider chain 全部不可用: {provider_chain}")


def transcribe_and_burn(
    source_path: str,
    output_video_path: str,
    output_srt_path: str,
    provider: str,
    language: str,
    burn_style: dict[str, Any],
    *,
    provider_chain: Optional[list[str]] = None,
    ffmpeg_bin: str = "ffmpeg",
    source_root: Optional[str] = None,
    public_url_base: Optional[str] = None,
    dashscope_api_key: Optional[str] = None,
    dashscope_base_url: str = DEFAULT_DASHSCOPE_BASE_URL,
    dashscope_model: str = "qwen3-asr-flash-filetrans",
    local_model: str = "small",
    local_compute_type: str = "int8",
    mac_transcriber_url: Optional[str] = None,
    mac_transcriber_token: Optional[str] = None,
    mac_transcriber_timeout_seconds: float = DEFAULT_MAC_TRANSCRIBER_TIMEOUT_SECONDS,
    video_encoder: str = DEFAULT_VIDEO_ENCODER,
    vaapi_device: str = DEFAULT_VAAPI_DEVICE,
    vaapi_qp: int = DEFAULT_VAAPI_QP,
) -> dict[str, Any]:
    output_dir = os.path.dirname(output_video_path) or "."
    os.makedirs(output_dir, exist_ok=True)
    os.makedirs(os.path.dirname(output_srt_path) or ".", exist_ok=True)

    with tempfile.NamedTemporaryFile(prefix="subtitle-audio-", suffix=".wav", dir=output_dir, delete=False) as temp_audio:
        audio_path = temp_audio.name

    try:
        extract_audio(source_path, audio_path, ffmpeg_bin=ffmpeg_bin)

        provider_kwargs = dict(
            source_root=source_root,
            public_url_base=public_url_base,
            dashscope_api_key=dashscope_api_key,
            dashscope_base_url=dashscope_base_url,
            dashscope_model=dashscope_model,
            local_model=local_model,
            local_compute_type=local_compute_type,
            mac_transcriber_url=mac_transcriber_url,
            mac_transcriber_token=mac_transcriber_token,
            mac_transcriber_timeout_seconds=mac_transcriber_timeout_seconds,
        )

        # provider="auto" 时按 chain 顺序尝试；否则用单一 provider（向后兼容旧调用方）。
        # actual_provider 暴露给 NAS 主进程：SubtitleManager 可写到 .subtitle.json
        # 让用户在 WebUI 看到"今天这一集实际是 mac 还是 dashscope 转写的"，故障定位
        # 不必翻 worker 容器日志。
        if provider == "auto":
            if not provider_chain:
                raise WorkerSafeError(
                    "provider=auto 时必须配置 SUBTITLE_PROVIDER_CHAIN，"
                    "例 SUBTITLE_PROVIDER_CHAIN=remote-mac-mlx,dashscope,local-whisper"
                )
            actual_provider, segments = _transcribe_with_chain(provider_chain, audio_path, language, **provider_kwargs)
        else:
            segments = _transcribe_with_provider(provider, audio_path, language, **provider_kwargs)
            actual_provider = provider

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
            video_encoder=video_encoder,
            vaapi_device=vaapi_device,
            vaapi_qp=vaapi_qp,
        )
        return {
            "segments": segments_to_api_payload(segments),
            "ass_path": ass_path,
            "render_preset": render_preset,
            "actual_provider": actual_provider,
        }
    finally:
        if os.path.exists(audio_path):
            os.remove(audio_path)
