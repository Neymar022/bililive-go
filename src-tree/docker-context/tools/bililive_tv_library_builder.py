#!/usr/bin/env python3
import argparse
import errno
import json
import os
import shutil
from collections import defaultdict
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Sequence, Set, Tuple
from xml.sax.saxutils import escape

from bililive_media_organizer import (
    AliasResolver,
    DEFAULT_ROOTS,
    default_report_path,
    is_video_candidate,
    load_aliases_from_api,
    parse_video_filename,
    sanitize_component,
    truncate_utf8,
)

DEFAULT_OUTPUT_ROOT = "/volume2/docker/bililive-go/video"
DEFAULT_REPORT_DIR = "/volume2/docker/bililive-go/reports"
PREFERRED_EXTENSIONS = [".mp4", ".mkv", ".mov", ".m4v", ".webm", ".avi", ".ts", ".flv"]
PREFERRED_EXTENSION_RANK = {extension: index for index, extension in enumerate(PREFERRED_EXTENSIONS)}
SHOW_MARKER_FILENAME = ".bililive-show"
EPISODE_SIDECAR_SUFFIXES = (".nfo", ".srt", ".ass", ".subtitle.json")
PROTECTED_SUBTITLE_STATUSES = {"queued", "running", "completed"}
RAW_VIDEO_EXTENSIONS = {".flv"}


@dataclass(frozen=True)
class SourceEpisode:
    alias_name: str
    title: str
    platform_name: str
    recorded_at: str
    recorded_dt: datetime
    source_path: Path


def iter_source_episodes(
    source_roots: Sequence[Path],
    alias_resolver: Optional[AliasResolver] = None,
    alias_filters: Optional[Set[str]] = None,
) -> Iterable[SourceEpisode]:
    alias_resolver = alias_resolver or AliasResolver({})
    alias_filters = {sanitize_component(alias) for alias in (alias_filters or set()) if sanitize_component(alias)}
    selected: Dict[Tuple[str, str, str, str], SourceEpisode] = {}
    for root in source_roots:
        for path in sorted(root.rglob("*")):
            if not is_video_candidate(path):
                continue
            meta = parse_video_filename(path.name)
            alias_name = sanitize_component(path.parent.name or meta.host_name) or "未分类主播"
            if alias_filters and alias_name not in alias_filters:
                continue
            title = sanitize_component(meta.title) or "未命名直播"
            platform_name = alias_resolver.resolve_platform(root.name, meta.host_name, alias_name)
            recorded_dt = datetime.strptime(meta.recorded_at, "%Y-%m-%d %H-%M-%S")
            episode = SourceEpisode(
                alias_name=alias_name,
                title=title,
                platform_name=platform_name,
                recorded_at=meta.recorded_at,
                recorded_dt=recorded_dt,
                source_path=path,
            )
            dedupe_key = (episode.alias_name, episode.platform_name, episode.recorded_at, episode.title)
            existing = selected.get(dedupe_key)
            if existing is None or is_preferred_source(episode.source_path, existing.source_path):
                selected[dedupe_key] = episode
    for episode in sorted(
        selected.values(),
        key=lambda item: (item.alias_name, item.recorded_dt, item.platform_name, item.title, str(item.source_path)),
    ):
        yield episode


def is_preferred_source(candidate: Path, current: Path) -> bool:
    candidate_rank = PREFERRED_EXTENSION_RANK.get(candidate.suffix.lower(), len(PREFERRED_EXTENSION_RANK))
    current_rank = PREFERRED_EXTENSION_RANK.get(current.suffix.lower(), len(PREFERRED_EXTENSION_RANK))
    if candidate_rank != current_rank:
        return candidate_rank < current_rank
    return str(candidate) < str(current)


def build_episode_display_title(recorded_at: str, title: str) -> str:
    recorded_dt = datetime.strptime(recorded_at, "%Y-%m-%d %H-%M-%S")
    title = sanitize_component(title) or "未命名直播"
    return f"{recorded_dt.strftime('%Y-%m-%d')} - {title}"


def build_episode_filename(alias_name: str, episode_number: int, recorded_at: str, title: str, extension: str) -> str:
    alias_name = sanitize_component(alias_name) or "未分类主播"
    title = build_episode_display_title(recorded_at=recorded_at, title=title)
    prefix = f"{alias_name}.S01E{episode_number:04d}."
    max_bytes = 255 - len(extension.encode("utf-8"))
    prefix_bytes = len(prefix.encode("utf-8"))
    if prefix_bytes >= max_bytes:
        alias_name = truncate_utf8(alias_name, max(24, max_bytes // 3))
        prefix = f"{alias_name}.S01E{episode_number:04d}."
        prefix_bytes = len(prefix.encode("utf-8"))
    title = truncate_utf8(title, max(1, max_bytes - prefix_bytes))
    return f"{prefix}{title}{extension}"


def build_tvshow_nfo(alias_name: str, premiered: str, platforms: Sequence[str]) -> str:
    platforms_text = "、".join(sorted({sanitize_component(name) for name in platforms if sanitize_component(name)}))
    plot = f"{alias_name} 的直播录屏剧集库。来源平台: {platforms_text or '未知平台'}。"
    return "\n".join(
        [
            "<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>",
            "<tvshow>",
            f"  <title>{escape(alias_name)}</title>",
            f"  <showtitle>{escape(alias_name)}</showtitle>",
            f"  <sorttitle>{escape(alias_name)}</sorttitle>",
            f"  <plot>{escape(plot)}</plot>",
            "  <genre>直播录屏</genre>",
            "  <tag>直播录屏</tag>",
            "  <studio>bililive-go</studio>",
            f"  <premiered>{premiered}</premiered>",
            "</tvshow>",
            "",
        ]
    )


def build_episode_nfo(
    alias_name: str,
    title: str,
    platform_name: str,
    recorded_at: str,
    episode_number: int,
) -> str:
    recorded_dt = datetime.strptime(recorded_at, "%Y-%m-%d %H-%M-%S")
    aired = recorded_dt.strftime("%Y-%m-%d")
    dateadded = recorded_dt.strftime("%Y-%m-%d %H:%M:%S")
    sort_title = f"{alias_name} - {recorded_at}"
    plot = f"{platform_name} | 主播: {alias_name} | 标题: {title} | 录制时间: {recorded_at}"
    display_title = build_episode_display_title(recorded_at=recorded_at, title=title)
    return "\n".join(
        [
            "<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>",
            "<episodedetails>",
            f"  <title>{escape(display_title)}</title>",
            f"  <showtitle>{escape(alias_name)}</showtitle>",
            f"  <sorttitle>{escape(sort_title)}</sorttitle>",
            "  <season>1</season>",
            f"  <episode>{episode_number}</episode>",
            f"  <plot>{escape(plot)}</plot>",
            f"  <studio>{escape(platform_name)}</studio>",
            "  <genre>直播录屏</genre>",
            "  <tag>直播录屏</tag>",
            f"  <aired>{aired}</aired>",
            f"  <dateadded>{dateadded}</dateadded>",
            "</episodedetails>",
            "",
        ]
    )


def ensure_text_file(path: Path, text: str, dry_run: bool) -> bool:
    if path.exists() and path.read_text(encoding="utf-8") == text:
        return False
    if not dry_run:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
    return True


def ensure_subtitle_metadata(source_path: Path, target_path: Path, dry_run: bool) -> bool:
    metadata_path = target_path.with_suffix(".subtitle.json")
    metadata = {}
    if metadata_path.exists():
        try:
            metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            metadata = {}

    if str(metadata.get("status", "")).lower() == "completed":
        return False

    metadata = dict(metadata)
    metadata["status"] = str(metadata.get("status") or "idle")
    metadata["source_path"] = str(source_path)
    metadata["output_path"] = str(target_path)
    metadata["source_exists"] = source_path.exists()
    metadata.setdefault("keep_source", False)
    return ensure_text_file(metadata_path, json.dumps(metadata, ensure_ascii=False, indent=2) + "\n", dry_run=dry_run)


def ensure_hardlink(source_path: Path, target_path: Path, dry_run: bool) -> bool:
    if should_preserve_rendered_video(source_path, target_path):
        return False
    if target_path.exists():
        try:
            if source_path.stat().st_ino == target_path.stat().st_ino:
                return False
        except FileNotFoundError:
            pass
        if not dry_run:
            target_path.unlink()
    if not dry_run:
        target_path.parent.mkdir(parents=True, exist_ok=True)
        try:
            os.link(source_path, target_path)
        except OSError as exc:
            if exc.errno not in (errno.EXDEV, errno.EPERM):
                raise
            shutil.copy2(source_path, target_path)
    return True


def should_preserve_rendered_video(source_path: Path, target_path: Path) -> bool:
    if not target_path.exists():
        return False

    metadata_path = target_path.with_suffix(".subtitle.json")
    if not metadata_path.exists():
        return False

    try:
        metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return False

    if not is_protected_subtitle_metadata(metadata):
        return False

    if not target_path.with_suffix(".ass").exists() and not target_path.with_suffix(".srt").exists():
        return False

    try:
        return source_path.stat().st_ino != target_path.stat().st_ino
    except FileNotFoundError:
        return True


def is_protected_subtitle_metadata(metadata: Dict[str, object]) -> bool:
    status = str(metadata.get("status", "")).lower()
    renderer_status = str(metadata.get("renderer_status", "")).lower()
    return status in PROTECTED_SUBTITLE_STATUSES or renderer_status in PROTECTED_SUBTITLE_STATUSES


def is_relative_to_path(path: Path, parent: Path) -> bool:
    try:
        path.resolve(strict=False).relative_to(parent.resolve(strict=False))
        return True
    except ValueError:
        return False


def subtitle_metadata_stem(metadata_path: Path) -> Path:
    name = metadata_path.name
    if name.endswith(".subtitle.json"):
        return metadata_path.with_name(name[: -len(".subtitle.json")])
    return metadata_path.with_suffix("")


def add_episode_family(expected_paths: Set[Path], episode_path: Path) -> None:
    expected_paths.add(episode_path)
    for suffix in EPISODE_SIDECAR_SUFFIXES:
        expected_paths.add(episode_path.with_suffix(suffix))


def episode_slot_has_sidecars(target_path: Path) -> bool:
    return any(target_path.with_suffix(suffix).exists() for suffix in EPISODE_SIDECAR_SUFFIXES)


def choose_episode_target_path(
    source_path: Path,
    season_dir: Path,
    alias_name: str,
    episode_number: int,
    recorded_at: str,
    title: str,
    extension: str,
    allocated_episode_numbers: Set[int],
) -> Tuple[int, Path]:
    while True:
        target_name = build_episode_filename(
            alias_name=alias_name,
            episode_number=episode_number,
            recorded_at=recorded_at,
            title=title,
            extension=extension,
        )
        target_path = season_dir / target_name
        if episode_number in allocated_episode_numbers:
            episode_number += 1
            continue
        if target_path.exists():
            try:
                if source_path.stat().st_ino == target_path.stat().st_ino:
                    return episode_number, target_path
            except FileNotFoundError:
                pass
            if should_preserve_rendered_video(source_path, target_path):
                episode_number += 1
                continue
        elif episode_slot_has_sidecars(target_path):
            episode_number += 1
            continue
        return episode_number, target_path


def collect_protected_subtitle_outputs(output_root: Path) -> Dict[str, Set[Path]]:
    protected: Dict[str, Set[Path]] = {}
    if not output_root.exists():
        return protected

    for metadata_path in output_root.rglob("*.subtitle.json"):
        if not metadata_path.name.endswith(".subtitle.json"):
            continue
        try:
            relative = metadata_path.relative_to(output_root)
        except ValueError:
            continue
        if len(relative.parts) < 3:
            continue

        show_dir = output_root / relative.parts[0]
        if not (show_dir / SHOW_MARKER_FILENAME).exists():
            continue
        try:
            metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        if not is_protected_subtitle_metadata(metadata):
            continue

        expected_paths = protected.setdefault(
            show_dir.name,
            {
                show_dir,
                show_dir / SHOW_MARKER_FILENAME,
                show_dir / "tvshow.nfo",
                metadata_path.parent,
            },
        )
        expected_paths.add(metadata_path)
        stem = subtitle_metadata_stem(metadata_path)
        for existing_path in metadata_path.parent.glob(stem.name + ".*"):
            expected_paths.add(existing_path)

        output_path = metadata.get("output_path")
        if isinstance(output_path, str) and output_path:
            candidate = Path(output_path)
            if is_relative_to_path(candidate, output_root):
                add_episode_family(expected_paths, candidate)
    return protected


def remove_path(path: Path, dry_run: bool) -> bool:
    if not path.exists():
        return False
    if not dry_run:
        if path.is_dir():
            shutil.rmtree(path)
        else:
            path.unlink()
    return True


def cleanup_managed_show_dirs(
    output_root: Path,
    expected_show_files: Dict[str, Set[Path]],
    dry_run: bool,
) -> Dict[str, int]:
    summary = {
        "removed_files": 0,
        "removed_dirs": 0,
    }
    if not output_root.exists():
        return summary

    expected_show_names = set(expected_show_files)
    for child in output_root.iterdir():
        if child.name.startswith(".") or not child.is_dir():
            continue
        marker_path = child / SHOW_MARKER_FILENAME
        if not marker_path.exists():
            continue
        if child.name not in expected_show_names:
            if remove_path(child, dry_run=dry_run):
                summary["removed_dirs"] += 1
            continue

        expected_paths = expected_show_files[child.name]
        for path in sorted(child.rglob("*"), key=lambda item: (item.is_dir(), str(item)), reverse=True):
            if path == child or path.name.startswith("."):
                continue
            if path in expected_paths:
                continue
            if remove_path(path, dry_run=dry_run):
                if path.is_dir():
                    summary["removed_dirs"] += 1
                else:
                    summary["removed_files"] += 1
    return summary


def build_tv_library(
    source_roots: Sequence[Path],
    output_root: Path,
    alias_filters: Optional[Sequence[str]] = None,
    alias_resolver: Optional[AliasResolver] = None,
    dry_run: bool = False,
) -> Dict[str, int]:
    grouped: Dict[str, List[SourceEpisode]] = defaultdict(list)
    for episode in iter_source_episodes(source_roots, alias_resolver=alias_resolver, alias_filters=set(alias_filters or [])):
        if episode.source_path.suffix.lower() in RAW_VIDEO_EXTENSIONS:
            continue
        grouped[episode.alias_name].append(episode)

    summary = {
        "shows": 0,
        "episodes": 0,
        "updated_links": 0,
        "updated_nfos": 0,
        "removed_files": 0,
        "removed_dirs": 0,
    }
    expected_show_files: Dict[str, Set[Path]] = collect_protected_subtitle_outputs(output_root)
    for alias_name, items in sorted(grouped.items()):
        items.sort(key=lambda item: (item.recorded_dt, item.platform_name, item.title, str(item.source_path)))
        summary["shows"] += 1
        summary["episodes"] += len(items)
        show_dir = output_root / alias_name
        season_dir = show_dir / "Season 01"
        show_marker = show_dir / SHOW_MARKER_FILENAME
        expected_show_files.setdefault(alias_name, set()).update(
            {
                show_dir,
                show_marker,
                show_dir / "tvshow.nfo",
                season_dir,
            }
        )
        premiered = items[0].recorded_dt.strftime("%Y-%m-%d")
        platforms = [item.platform_name for item in items]
        tvshow_nfo = build_tvshow_nfo(alias_name=alias_name, premiered=premiered, platforms=platforms)
        if ensure_text_file(show_marker, "", dry_run=dry_run):
            pass
        if ensure_text_file(show_dir / "tvshow.nfo", tvshow_nfo, dry_run=dry_run):
            summary["updated_nfos"] += 1

        allocated_episode_numbers: Set[int] = set()
        for index, item in enumerate(items, start=1):
            episode_number, target_path = choose_episode_target_path(
                source_path=item.source_path,
                season_dir=season_dir,
                alias_name=alias_name,
                episode_number=index,
                recorded_at=item.recorded_at,
                title=item.title,
                extension=item.source_path.suffix,
                allocated_episode_numbers=allocated_episode_numbers,
            )
            allocated_episode_numbers.add(episode_number)
            expected_show_files[alias_name].add(target_path)
            for suffix in EPISODE_SIDECAR_SUFFIXES:
                expected_show_files[alias_name].add(target_path.with_suffix(suffix))
            episode_nfo = build_episode_nfo(
                alias_name=alias_name,
                title=item.title,
                platform_name=item.platform_name,
                recorded_at=item.recorded_at,
                episode_number=index,
            )
            if ensure_hardlink(item.source_path, target_path, dry_run=dry_run):
                summary["updated_links"] += 1
            if ensure_text_file(target_path.with_suffix(".nfo"), episode_nfo, dry_run=dry_run):
                summary["updated_nfos"] += 1
            ensure_subtitle_metadata(item.source_path, target_path, dry_run=dry_run)

    cleanup_summary = cleanup_managed_show_dirs(output_root=output_root, expected_show_files=expected_show_files, dry_run=dry_run)
    summary["removed_files"] += cleanup_summary["removed_files"]
    summary["removed_dirs"] += cleanup_summary["removed_dirs"]
    return summary


def run(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(description="Build a TV-style hard-link mirror for bililive-go recordings.")
    parser.add_argument("--api-url", default="http://127.0.0.1:18090/api/lives")
    parser.add_argument("--timeout-seconds", type=int, default=5)
    parser.add_argument("--source-root", action="append", dest="source_roots", default=[])
    parser.add_argument("--output-root", default=DEFAULT_OUTPUT_ROOT)
    parser.add_argument("--alias", action="append", dest="aliases", default=[])
    parser.add_argument("--report-dir", default=DEFAULT_REPORT_DIR)
    parser.add_argument("--report-file", default="")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args(argv)

    source_roots = [Path(root) for root in (args.source_roots or DEFAULT_ROOTS)]
    output_root = Path(args.output_root)
    try:
        aliases = load_aliases_from_api(args.api_url, args.timeout_seconds)
    except Exception:
        aliases = {}
    summary = build_tv_library(
        source_roots=source_roots,
        output_root=output_root,
        alias_filters=args.aliases,
        alias_resolver=AliasResolver(aliases),
        dry_run=args.dry_run,
    )
    report_path = Path(args.report_file) if args.report_file else default_report_path(Path(args.report_dir), args.dry_run)
    payload = {
        "generated_at": datetime.now().isoformat(timespec="seconds"),
        "dry_run": args.dry_run,
        "output_root": str(output_root),
        "source_roots": [str(root) for root in source_roots],
        "aliases": args.aliases,
        "summary": summary,
    }
    if not args.dry_run:
        report_path.parent.mkdir(parents=True, exist_ok=True)
        report_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"report": str(report_path), "summary": summary}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(run())
