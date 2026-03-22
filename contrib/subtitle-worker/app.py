import os
from typing import Any
from typing import Optional

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from worker_core import generate_style_lab_sample, render_style_lab_preview, transcribe_and_burn


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
    outline: int = 2
    shadow: int = 0


class ProcessRequest(BaseModel):
    source_path: str
    output_video_path: str
    output_srt_path: str
    provider: str = "dashscope"
    language: str = "zh"
    burn_style: BurnStyle = Field(default_factory=BurnStyle)
    record_meta: dict[str, Any] = Field(default_factory=dict)


class StyleLabPreviewRequest(BaseModel):
    source_path: str
    preview_text: str = "字幕样式实验室预览"
    frame_time_seconds: float = 1.0
    output_preview_path: Optional[str] = None
    burn_style: BurnStyle = Field(default_factory=BurnStyle)


class StyleLabSampleRequest(BaseModel):
    source_path: str
    sample_text: str = "字幕样式实验室测试样片"
    start_time_seconds: float = 0
    duration_seconds: float = 30
    output_dir: Optional[str] = None
    burn_style: BurnStyle = Field(default_factory=BurnStyle)


app = FastAPI(title="bililive-go subtitle worker")


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


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
            ffmpeg_bin=os.getenv("FFMPEG_BIN", "ffmpeg"),
            source_root=os.getenv("SUBTITLE_SOURCE_ROOT"),
            public_url_base=os.getenv("SUBTITLE_PUBLIC_URL_BASE"),
            dashscope_api_key=os.getenv("DASHSCOPE_API_KEY"),
            dashscope_base_url=os.getenv("DASHSCOPE_BASE_URL", "https://dashscope.aliyuncs.com"),
            dashscope_model=os.getenv("SUBTITLE_DASHSCOPE_MODEL", "qwen3-asr-flash-filetrans"),
            local_model=os.getenv("SUBTITLE_LOCAL_MODEL", "small"),
            local_compute_type=os.getenv("SUBTITLE_LOCAL_COMPUTE_TYPE", "int8"),
        )
    except Exception as exc:  # pragma: no cover - FastAPI handles serialization
        raise HTTPException(status_code=500, detail=str(exc)) from exc


@app.post("/api/v1/style-lab/preview")
def style_lab_preview(req: StyleLabPreviewRequest) -> dict[str, Any]:
    try:
        return render_style_lab_preview(
            source_path=req.source_path,
            preview_text=req.preview_text,
            burn_style=req.burn_style.model_dump(),
            output_preview_path=req.output_preview_path,
            frame_time_seconds=req.frame_time_seconds,
            ffmpeg_bin=os.getenv("FFMPEG_BIN", "ffmpeg"),
        )
    except Exception as exc:  # pragma: no cover - FastAPI handles serialization
        raise HTTPException(status_code=500, detail=str(exc)) from exc


@app.post("/api/v1/style-lab/sample")
def style_lab_sample(req: StyleLabSampleRequest) -> dict[str, Any]:
    try:
        return generate_style_lab_sample(
            source_path=req.source_path,
            sample_text=req.sample_text,
            burn_style=req.burn_style.model_dump(),
            output_dir=req.output_dir,
            start_time_seconds=req.start_time_seconds,
            duration_seconds=min(req.duration_seconds, 30),
            ffmpeg_bin=os.getenv("FFMPEG_BIN", "ffmpeg"),
        )
    except Exception as exc:  # pragma: no cover - FastAPI handles serialization
        raise HTTPException(status_code=500, detail=str(exc)) from exc
