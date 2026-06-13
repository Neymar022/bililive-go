#!/usr/bin/env python3
"""Backfill BiliNote note_records.audio_meta.cover_url from media sidecars.

Default mode is dry-run. Use --apply to copy media-library .jpg covers into the
BiliNote static cover folder and update note_records.audio_meta. The script does
not regenerate notes or modify media-library files.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import sqlite3
from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class Candidate:
    record_id: int
    task_id: str
    video_path: Path
    cover_path: Path
    cover_url: str


def load_json(value: str | None) -> dict[str, Any]:
    if not value:
        return {}
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def source_video_path(audio_meta: dict[str, Any], form_data: dict[str, Any]) -> str:
    for key in ("video_path", "source_video_path"):
        value = audio_meta.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
    value = form_data.get("source_video_path")
    if isinstance(value, str) and value.strip():
        return value.strip()
    return ""


def cover_is_usable(path: Path) -> bool:
    try:
        return path.exists() and path.stat().st_size > 0
    except OSError:
        return False


def static_cover_name(cover_path: Path) -> str:
    digest = hashlib.sha1(str(cover_path).encode("utf-8")).hexdigest()[:10]
    return f"{cover_path.stem}.{digest}{cover_path.suffix.lower() or '.jpg'}"


def static_cover_url(api_base_url: str, backend_port: str, file_name: str) -> str:
    base = api_base_url.rstrip("/")
    if backend_port and not base.rsplit("/", 1)[-1].count(":"):
        base = f"{base}:{backend_port}"
    return f"{base}/static/cover/{file_name}"


def collect_candidates(
    db_path: Path,
    static_root: Path,
    api_base_url: str,
    backend_port: str,
) -> list[Candidate]:
    connection = sqlite3.connect(db_path)
    try:
        rows = connection.execute(
            "SELECT id, task_id, audio_meta, form_data FROM note_records ORDER BY id"
        ).fetchall()
    finally:
        connection.close()

    candidates: list[Candidate] = []
    for record_id, task_id, audio_meta_raw, form_data_raw in rows:
        audio_meta = load_json(audio_meta_raw)
        if audio_meta.get("cover_url"):
            continue
        form_data = load_json(form_data_raw)
        video_path_text = source_video_path(audio_meta, form_data)
        if not video_path_text:
            continue
        video_path = Path(video_path_text)
        cover_path = video_path.with_suffix(".jpg")
        if not cover_is_usable(cover_path):
            continue
        file_name = static_cover_name(cover_path)
        candidates.append(
            Candidate(
                record_id=int(record_id),
                task_id=str(task_id),
                video_path=video_path,
                cover_path=cover_path,
                cover_url=static_cover_url(api_base_url, backend_port, file_name),
            )
        )
    return candidates


def apply_candidates(db_path: Path, static_root: Path, candidates: list[Candidate]) -> None:
    cover_dir = static_root / "cover"
    cover_dir.mkdir(parents=True, exist_ok=True)

    connection = sqlite3.connect(db_path)
    try:
        for candidate in candidates:
            target = cover_dir / Path(candidate.cover_url).name
            shutil.copy2(candidate.cover_path, target)
            audio_meta_raw = connection.execute(
                "SELECT audio_meta FROM note_records WHERE id = ?",
                (candidate.record_id,),
            ).fetchone()[0]
            audio_meta = load_json(audio_meta_raw)
            audio_meta["cover_url"] = candidate.cover_url
            connection.execute(
                "UPDATE note_records SET audio_meta = ? WHERE id = ?",
                (json.dumps(audio_meta, ensure_ascii=False), candidate.record_id),
            )
        connection.commit()
    finally:
        connection.close()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", required=True, help="Path to BiliNote SQLite database")
    parser.add_argument("--static-root", required=True, help="Path to BiliNote backend static directory")
    parser.add_argument("--api-base-url", default="http://localhost")
    parser.add_argument("--backend-port", default="8483")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()

    db_path = Path(args.db)
    static_root = Path(args.static_root)
    candidates = collect_candidates(db_path, static_root, args.api_base_url, args.backend_port)

    for candidate in candidates:
        print(
            f"[cover-url] record={candidate.record_id} task={candidate.task_id} "
            f"{candidate.cover_path} -> {candidate.cover_url}"
        )

    if args.apply:
        apply_candidates(db_path, static_root, candidates)

    mode = "apply" if args.apply else "dry-run"
    print(f"{mode}: candidate_records={len(candidates)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
