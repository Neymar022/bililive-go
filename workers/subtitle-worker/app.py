import logging
import os
from typing import Any

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from worker_core import WorkerRetryLater, WorkerSafeError, transcribe_and_burn

_logger = logging.getLogger("subtitle_worker")


class BurnStyle(BaseModel):
    preset: str = "bottom_center"
    font_name: str = "Noto Sans CJK SC"
    font_size: int = 50
    card_width: int = 1018
    card_height: int = 196
    bottom_offset: int = 640
    background_opacity: float = 0.9
    border_opacity: float = 0.08
    single_line: bool = True
    overflow_mode: str = "ellipsis"
    margin_v: int = 24
    outline: int = 0
    shadow: int = 0


class ProcessRequest(BaseModel):
    source_path: str
    output_video_path: str
    output_srt_path: str
    provider: str = "dashscope"
    language: str = "zh"
    burn_style: BurnStyle = Field(default_factory=BurnStyle)
    record_meta: dict[str, Any] = Field(default_factory=dict)


app = FastAPI(title="bililive-go subtitle worker")


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


def _parse_provider_chain() -> list[str]:
    """SUBTITLE_PROVIDER_CHAIN 是逗号分隔的 provider 名清单，按顺序尝试。
    例：'remote-mac-mlx,dashscope,local-whisper' = 主用 Mac，挂了切云，再挂用 NAS CPU。
    空字符串返回空 list，调用方判断后报错。"""
    raw = os.getenv("SUBTITLE_PROVIDER_CHAIN", "").strip()
    if not raw:
        return []
    return [item.strip() for item in raw.split(",") if item.strip()]


def _parse_burn_chain() -> list[str]:
    """SUBTITLE_BURN_CHAIN 同 SUBTITLE_PROVIDER_CHAIN 的设计但用于 burn 阶段。
    例：'remote-mac,nas-software' = 主用 Mac VideoToolbox，挂了切 NAS 软编码。
    None/空 = 不走 chain，直接 NAS 软编码（向后兼容）。"""
    raw = os.getenv("SUBTITLE_BURN_CHAIN", "").strip()
    if not raw:
        return []
    return [item.strip() for item in raw.split(",") if item.strip()]


def _parse_bool_env(name: str, default: bool) -> bool:
    raw = os.getenv(name)
    if raw is None or raw.strip() == "":
        return default
    normalized = raw.strip().lower()
    if normalized in {"1", "true", "yes", "y", "on"}:
        return True
    if normalized in {"0", "false", "no", "n", "off"}:
        return False
    return default


@app.post("/api/v1/process")
def process(req: ProcessRequest) -> dict[str, Any]:
    try:
        return transcribe_and_burn(
            source_path=req.source_path,
            output_video_path=req.output_video_path,
            output_srt_path=req.output_srt_path,
            provider=req.provider,
            language=req.language,
            burn_style=req.burn_style.model_dump(),
            provider_chain=_parse_provider_chain(),
            ffmpeg_bin=os.getenv("FFMPEG_BIN", "ffmpeg"),
            source_root=os.getenv("SUBTITLE_SOURCE_ROOT"),
            public_url_base=os.getenv("SUBTITLE_PUBLIC_URL_BASE"),
            dashscope_api_key=os.getenv("DASHSCOPE_API_KEY"),
            dashscope_base_url=os.getenv("DASHSCOPE_BASE_URL", "https://dashscope.aliyuncs.com"),
            dashscope_model=os.getenv("SUBTITLE_DASHSCOPE_MODEL", "qwen3-asr-flash-filetrans"),
            local_model=os.getenv("SUBTITLE_LOCAL_MODEL", "small"),
            local_compute_type=os.getenv("SUBTITLE_LOCAL_COMPUTE_TYPE", "int8"),
            allow_cloud_asr=_parse_bool_env("SUBTITLE_ALLOW_CLOUD_ASR", True),
            mac_transcriber_url=os.getenv("SUBTITLE_MAC_TRANSCRIBER_URL"),
            mac_transcriber_token=os.getenv("SUBTITLE_MAC_TRANSCRIBER_TOKEN"),
            mac_transcriber_model=os.getenv("SUBTITLE_MAC_TRANSCRIBER_MODEL", "large-v3-turbo"),
            mac_transcriber_timeout_seconds=float(os.getenv("SUBTITLE_MAC_TRANSCRIBER_TIMEOUT", "3600")),
            video_encoder=os.getenv("SUBTITLE_VIDEO_ENCODER", "software"),
            vaapi_device=os.getenv("SUBTITLE_VAAPI_DEVICE", "/dev/dri/renderD128"),
            vaapi_qp=int(os.getenv("SUBTITLE_VAAPI_QP", "23")),
            # P7: burn 链式 fallback 配置
            burn_chain=_parse_burn_chain() or None,
            mac_burn_url=os.getenv("SUBTITLE_MAC_BURN_URL"),
            mac_burn_token=os.getenv("SUBTITLE_MAC_BURN_TOKEN"),
            mac_burn_codec=os.getenv("SUBTITLE_MAC_BURN_CODEC", "h264_videotoolbox"),
            mac_burn_bitrate=os.getenv("SUBTITLE_MAC_BURN_BITRATE", "5M"),
            # P15 修：默认从 1200s（旧 P9 era 值，会让 long video burn timeout 触发
            # NAS-software 兜底失败链）提到 18000s（5h），跟 worker_core.py 的
            # DEFAULT_MAC_BURN_TIMEOUT_SECONDS + Mac burn_handler.py 的
            # DEFAULT_FFMPEG_TIMEOUT_SECONDS 三层对齐。
            mac_burn_timeout_seconds=float(os.getenv("SUBTITLE_MAC_BURN_TIMEOUT", "18000")),
        )
    except WorkerRetryLater as exc:
        raise HTTPException(
            status_code=503,
            detail={"code": exc.code, "message": str(exc)},
        ) from exc
    except WorkerSafeError as exc:
        # message 已被 worker_core 标记为安全暴露：参数缺失/不支持的 provider 等。
        raise HTTPException(status_code=500, detail=str(exc)) from exc
    except ValueError as exc:
        # pydantic 校验或显式 ValueError——参数错，message 来源于用户输入回显，安全。
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    except Exception as exc:
        # 未知异常：message 可能含 ffmpeg stderr、文件路径、API key、OSS 临时凭据。
        # 完整 traceback 写到容器 stderr 供运维排查；客户端只见通用错误。
        _logger.exception("worker process failed")
        raise HTTPException(
            status_code=500,
            detail="internal worker error; check worker logs",
        ) from exc
