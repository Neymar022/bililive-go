#!/usr/bin/env python3
"""Repair UGREEN/Plex-style sidecar metadata for bililive-go TV libraries.

Default mode is dry-run. Use --apply to write tvshow.nfo and incomplete
episode .nfo files. The script never deletes media files. Duplicate show
directory merges require both --merge-duplicate-shows and --apply.
"""

from __future__ import annotations

import argparse
import hashlib
import html
import json
import os
import re
import shutil
import subprocess
import tempfile
import unicodedata
import xml.etree.ElementTree as ET
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path


EPISODE_RE = re.compile(
    r"^(?P<alias>.+?)\.S(?P<season>\d{2})E(?P<episode>\d+)"
    r"(?:-S\d{2}E\d+)?\.(?P<date>\d{4}-\d{2}-\d{2}) - (?P<title>.+)$"
)
SOURCE_RECORDED_AT_RE = re.compile(r" - (?P<recorded_at>\d{4}-\d{2}-\d{2} \d{2}-\d{2}-\d{2}) - ")
SIDECAR_SUFFIXES = (
    ".mp4",
    ".nfo",
    ".jpg",
    ".srt",
    ".ass",
    ".subtitle.json",
    ".transcript.json",
)
CHRONOLOGICAL_EPISODE_IDENTITY_BASE = 8
CHRONOLOGICAL_EPISODE_EPOCH = datetime(2020, 1, 1, tzinfo=timezone.utc)
MAX_SAFE_EPISODE_IDENTITY = 9_007_199_254_740_991
MEDIA_LIBRARY_TIMEZONE = timezone(timedelta(hours=8))


@dataclass(frozen=True)
class Episode:
    path: Path
    show_dir: Path
    season: int
    episode: int
    date: str
    alias: str
    title: str


@dataclass(frozen=True)
class ChronologicalEpisodeMove:
    source: Path
    target: Path
    recorded_at: datetime
    episode: int


@dataclass(frozen=True)
class ChronologicalFileMove:
    source: Path
    target: Path


@dataclass(frozen=True)
class ChronologicalNFOEdit:
    path: Path
    fields: dict[str, tuple[str, str]]


@dataclass(frozen=True)
class ChronologicalJSONEdit:
    path: Path
    pointer: str
    old: str
    new: str


@dataclass(frozen=True)
class ChronologicalReferenceSimulation:
    old_references: int
    new_references: int


@dataclass(frozen=True)
class ChronologicalMediaConservation:
    before_count: int
    before_bytes: int
    after_count: int
    after_bytes: int


def normalize_component(value: str) -> str:
    """Match bililive-go's media-library component cleanup for identity checks."""
    value = "".join(
        " " if ch.isspace() else "" if unicodedata.category(ch) in {"Cc", "Cf"} else ch
        for ch in value
    )
    value = re.sub(r'[<>:"/\\|?*\x00-\x1F]', " ", value)
    value = re.sub(r"\s+", " ", value)
    return value.strip(" .")


def parse_episode(path: Path, library_root: Path) -> Episode | None:
    if path.suffix.lower() != ".mp4":
        return None
    try:
        rel = path.relative_to(library_root)
    except ValueError:
        return None
    if any(part.startswith(".") or part == "@eaDir" for part in rel.parts):
        return None
    if len(rel.parts) < 3 or not rel.parts[-2].lower().startswith("season "):
        return None

    match = EPISODE_RE.match(path.stem)
    if not match:
        return None

    return Episode(
        path=path,
        show_dir=path.parent.parent,
        season=int(match.group("season")),
        episode=int(match.group("episode")),
        date=match.group("date"),
        alias=normalize_component(match.group("alias")) or normalize_component(path.parent.parent.name) or "未分类主播",
        title=normalize_component(match.group("title")) or "未命名直播",
    )


def episode_recorded_at(ep: Episode) -> datetime:
    metadata_path = ep.path.with_suffix(".subtitle.json")
    try:
        metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    except (OSError, ValueError, TypeError):
        metadata = {}
    value = (metadata.get("record_meta") or {}).get("start_time")
    if isinstance(value, str):
        try:
            parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
            if parsed.year >= 2000:
                return parsed if parsed.tzinfo is not None else parsed.replace(tzinfo=MEDIA_LIBRARY_TIMEZONE)
        except ValueError:
            pass
    for key in ("source_path", "output_path"):
        candidate = metadata.get(key)
        match = SOURCE_RECORDED_AT_RE.search(candidate) if isinstance(candidate, str) else None
        if match:
            return datetime.strptime(match.group("recorded_at"), "%Y-%m-%d %H-%M-%S").replace(
                tzinfo=MEDIA_LIBRARY_TIMEZONE
            )
    raise ValueError(f"no reliable recorded_at: {ep.path}")


def chronological_episode_identity(recorded_at: datetime, collision: int = 0) -> int:
    utc = recorded_at.astimezone(timezone.utc)
    delta = utc - CHRONOLOGICAL_EPISODE_EPOCH
    microseconds = (delta.days * 86400 + delta.seconds) * 1_000_000 + delta.microseconds
    if microseconds <= 0:
        raise ValueError(f"recorded_at predates chronological identity epoch: {recorded_at.isoformat()}")
    if not 0 <= collision < CHRONOLOGICAL_EPISODE_IDENTITY_BASE:
        raise ValueError(f"recorded_at collision space exhausted: {recorded_at.isoformat()}")
    identity = microseconds * CHRONOLOGICAL_EPISODE_IDENTITY_BASE + collision
    if identity > MAX_SAFE_EPISODE_IDENTITY:
        raise ValueError(f"recorded_at exceeds safe episode identity range: {recorded_at.isoformat()}")
    return identity


def plan_chronological_episode_renumber(
    library_root: Path,
    episodes: list[Episode],
) -> list[ChronologicalEpisodeMove]:
    root = library_root.resolve()
    planned: list[ChronologicalEpisodeMove] = []
    sources = {ep.path.resolve() for ep in episodes}
    targets: set[Path] = set()
    by_recorded_at: dict[tuple[Path, datetime], list[Episode]] = {}
    for ep in episodes:
        recorded_at = episode_recorded_at(ep)
        by_recorded_at.setdefault((ep.show_dir.resolve(), recorded_at), []).append(ep)

    numbered: list[tuple[Episode, datetime, int]] = []
    for (_, recorded_at), grouped in sorted(by_recorded_at.items(), key=lambda item: str(item[0])):
        grouped.sort(key=lambda ep: (ep.episode, str(ep.path)))
        for collision, ep in enumerate(grouped):
            numbered.append((ep, recorded_at, chronological_episode_identity(recorded_at, collision)))

    for ep, recorded_at, episode_number in numbered:
        stem = f"{ep.alias}.S{ep.season:02d}E{episode_number:04d}.{recorded_at.strftime('%Y-%m-%d')} - {ep.title}"
        target = ep.path.with_name(stem + ep.path.suffix).resolve()
        try:
            target.relative_to(root)
        except ValueError as exc:
            raise ValueError(f"target outside library root: {target}") from exc
        if target in targets:
            raise ValueError(f"duplicate target: {target}")
        if target.exists() and target not in sources:
            raise ValueError(f"target conflict: {target}")
        targets.add(target)
        planned.append(ChronologicalEpisodeMove(ep.path.resolve(), target, recorded_at, episode_number))

    for show_dir, show_moves in _group_chronological_moves(planned).items():
        ordered = sorted(show_moves, key=lambda item: (item.recorded_at, str(item.source)))
        if any(
            left.recorded_at < right.recorded_at and left.episode >= right.episode
            for left, right in zip(ordered, ordered[1:])
        ):
            raise ValueError(f"non-monotonic episode identity: {show_dir}")
    return planned


def _group_chronological_moves(
    moves: list[ChronologicalEpisodeMove],
) -> dict[Path, list[ChronologicalEpisodeMove]]:
    grouped: dict[Path, list[ChronologicalEpisodeMove]] = {}
    for move in moves:
        grouped.setdefault(move.source.parent.parent, []).append(move)
    return grouped


def plan_chronological_file_moves(
    library_root: Path,
    episode_moves: list[ChronologicalEpisodeMove],
) -> list[ChronologicalFileMove]:
    root = library_root.resolve()
    planned: list[ChronologicalFileMove] = []
    sources: set[Path] = set()
    targets: set[Path] = set()
    for episode_move in episode_moves:
        for suffix in SIDECAR_SUFFIXES:
            source = episode_move.source.with_suffix(suffix).resolve()
            if not source.exists():
                continue
            target = episode_move.target.with_suffix(suffix).resolve()
            target.relative_to(root)
            if source in sources:
                raise ValueError(f"duplicate source: {source}")
            sources.add(source)
            planned.append(ChronologicalFileMove(source, target))

    for move in planned:
        if move.target in targets:
            raise ValueError(f"duplicate target: {move.target}")
        if move.target.exists() and move.target not in sources:
            raise ValueError(f"target conflict: {move.target}")
        targets.add(move.target)
    return planned


def chronological_reference_audit(
    search_roots: list[Path],
    file_moves: list[ChronologicalFileMove],
) -> tuple[int, dict[str, int]]:
    mapping = {str(move.source): str(move.target) for move in file_moves if move.source != move.target}
    parsed_json = 0
    matched_references = {source: 0 for source in mapping}

    def count_paths(value: object) -> None:
        if isinstance(value, str):
            if value in matched_references:
                matched_references[value] += 1
            return
        if isinstance(value, list):
            for item in value:
                count_paths(item)
            return
        if isinstance(value, dict):
            for item in value.values():
                count_paths(item)

    for search_root in search_roots:
        if not search_root.exists():
            continue
        for path in search_root.rglob("*.json"):
            try:
                content = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, ValueError, TypeError) as exc:
                raise ValueError(f"invalid JSON reference file: {path}: {exc}") from exc
            parsed_json += 1
            count_paths(content)
    return parsed_json, matched_references


def _xml_field_values(path: Path, tags: tuple[str, ...]) -> dict[str, str]:
    try:
        root = ET.fromstring(path.read_text(encoding="utf-8"))
    except (OSError, ET.ParseError) as exc:
        raise ValueError(f"invalid episode NFO: {path}: {exc}") from exc
    return {tag: (root.findtext(tag) or "").strip() for tag in tags}


def plan_chronological_nfo_edits(
    episode_moves: list[ChronologicalEpisodeMove],
) -> list[ChronologicalNFOEdit]:
    edits: list[ChronologicalNFOEdit] = []
    tags = ("episode", "sorttitle", "aired", "dateadded", "plot")
    for move in episode_moves:
        source = move.source.with_suffix(".nfo")
        if not source.exists():
            raise ValueError(f"missing episode NFO: {source}")
        episode = parse_episode(move.source, move.source.parent.parent.parent)
        if episode is None:
            raise ValueError(f"cannot parse planned episode: {move.source}")
        old = _xml_field_values(source, tags)
        precise_time = move.recorded_at.strftime("%Y-%m-%d %H-%M-%S.%f")
        studio = existing_studio(source)
        expected = {
            "episode": str(move.episode),
            "sorttitle": f"{episode.alias} - {precise_time}",
            "aired": move.recorded_at.strftime("%Y-%m-%d"),
            "dateadded": move.recorded_at.strftime("%Y-%m-%d %H:%M:%S"),
            "plot": f"{studio} | 主播: {episode.alias} | 标题: {episode.title} | 录制时间: {precise_time}",
        }
        changed = {tag: (old[tag], expected[tag]) for tag in tags if old[tag] != expected[tag]}
        if changed:
            edits.append(ChronologicalNFOEdit(source.resolve(), changed))
    return edits


def _json_pointer_token(value: object) -> str:
    return str(value).replace("~", "~0").replace("/", "~1")


def plan_chronological_json_edits(
    search_roots: list[Path],
    file_moves: list[ChronologicalFileMove],
) -> list[ChronologicalJSONEdit]:
    mapping = {str(move.source): str(move.target) for move in file_moves if move.source != move.target}
    edits: list[ChronologicalJSONEdit] = []

    def visit(path: Path, value: object, pointer: str) -> None:
        if isinstance(value, str):
            target = mapping.get(value)
            if target is not None:
                edits.append(ChronologicalJSONEdit(path, pointer or "/", value, target))
            return
        if isinstance(value, list):
            for index, item in enumerate(value):
                visit(path, item, f"{pointer}/{index}")
            return
        if isinstance(value, dict):
            for key, item in value.items():
                visit(path, item, f"{pointer}/{_json_pointer_token(key)}")

    for search_root in search_roots:
        if not search_root.exists():
            continue
        for path in search_root.rglob("*.json"):
            try:
                content = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, ValueError, TypeError) as exc:
                raise ValueError(f"invalid JSON reference file: {path}: {exc}") from exc
            visit(path.resolve(), content, "")
    return edits


def simulate_chronological_json_references(
    search_roots: list[Path],
    file_moves: list[ChronologicalFileMove],
) -> ChronologicalReferenceSimulation:
    mapping = {str(move.source): str(move.target) for move in file_moves if move.source != move.target}
    old_references = 0
    new_references = 0

    def count(value: object) -> None:
        nonlocal old_references, new_references
        if isinstance(value, str):
            old_references += int(value in mapping)
            new_references += int(value in mapping.values())
            return
        if isinstance(value, list):
            for item in value:
                count(item)
            return
        if isinstance(value, dict):
            for item in value.values():
                count(item)

    def simulate(value: object) -> object:
        if isinstance(value, str):
            return mapping.get(value, value)
        if isinstance(value, list):
            return [simulate(item) for item in value]
        if isinstance(value, dict):
            return {key: simulate(item) for key, item in value.items()}
        return value

    for search_root in search_roots:
        if not search_root.exists():
            continue
        for path in search_root.rglob("*.json"):
            try:
                content = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, ValueError, TypeError) as exc:
                raise ValueError(f"invalid JSON reference file: {path}: {exc}") from exc
            count(simulate(content))
    return ChronologicalReferenceSimulation(
        old_references=old_references,
        new_references=new_references,
    )


def chronological_media_conservation(
    library_root: Path,
    file_moves: list[ChronologicalFileMove],
) -> ChronologicalMediaConservation:
    root = library_root.resolve()
    media_moves = [move for move in file_moves if move.source.suffix.lower() == ".mp4"]
    before_paths = [path.resolve() for path in root.rglob("*.mp4")]
    sources = {move.source for move in media_moves}
    after_paths = [path for path in before_paths if path not in sources]
    after_paths.extend(move.target for move in media_moves)
    before_bytes = sum(path.stat().st_size for path in before_paths)
    after_bytes = sum(move.source.stat().st_size for move in media_moves)
    after_bytes += sum(path.stat().st_size for path in before_paths if path not in sources)
    return ChronologicalMediaConservation(len(before_paths), before_bytes, len(after_paths), after_bytes)


def existing_studio(nfo_path: Path) -> str:
    if not nfo_path.exists():
        return "bililive-go"
    try:
        text = nfo_path.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        return "bililive-go"
    match = re.search(r"<studio>(.*?)</studio>", text, re.S)
    if not match:
        return "bililive-go"
    value = re.sub(r"\s+", " ", html.unescape(match.group(1))).strip()
    return value or "bililive-go"


def build_show_nfo(alias: str, date: str, studio: str) -> str:
    year = date[:4]
    plot = f"{alias} 的直播录屏剧集库。来源平台: {studio}。"
    added = f"{date} 00:00:00"
    return "\n".join(
        [
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>',
            "<tvshow>",
            f"  <title>{html.escape(alias)}</title>",
            f"  <showtitle>{html.escape(alias)}</showtitle>",
            f"  <sorttitle>{html.escape(alias)}</sorttitle>",
            f"  <year>{year}</year>",
            f"  <plot>{html.escape(plot)}</plot>",
            f"  <studio>{html.escape(studio)}</studio>",
            "  <genre>直播录屏</genre>",
            "  <tag>直播录屏</tag>",
            '  <thumb aspect="poster">poster.jpg</thumb>',
            f"  <premiered>{date}</premiered>",
            f"  <dateadded>{added}</dateadded>",
            "</tvshow>",
            "",
        ]
    )


def build_episode_nfo(ep: Episode, studio: str) -> str:
    display_title = f"{ep.date} - {ep.title}"
    sort_title = f"{ep.alias} - {ep.date} 00-00-00"
    plot = f"{studio} | 主播: {ep.alias} | 标题: {ep.title} | 录制时间: {ep.date} 00-00-00"
    return "\n".join(
        [
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>',
            "<episodedetails>",
            f"  <title>{html.escape(display_title)}</title>",
            f"  <showtitle>{html.escape(ep.alias)}</showtitle>",
            f"  <sorttitle>{html.escape(sort_title)}</sorttitle>",
            f"  <season>{ep.season}</season>",
            f"  <episode>{ep.episode}</episode>",
            f"  <plot>{html.escape(plot)}</plot>",
            f"  <studio>{html.escape(studio)}</studio>",
            "  <genre>直播录屏</genre>",
            "  <tag>直播录屏</tag>",
            f"  <aired>{ep.date}</aired>",
            f"  <dateadded>{ep.date} 00:00:00</dateadded>",
            "</episodedetails>",
            "",
        ]
    )


def contains_tag(text: str, tag: str, expected: str | None = None) -> bool:
    pattern = rf"<{tag}>(.*?)</{tag}>"
    match = re.search(pattern, text, re.S)
    if not match:
        return False
    if expected is None:
        return True
    return html.unescape(match.group(1)).strip() == expected


def show_nfo_identity_complete(text: str, alias: str) -> bool:
    try:
        root = ET.fromstring(text)
    except ET.ParseError:
        return False
    return (
        root.tag == "tvshow"
        and (root.findtext("title") or "").strip() == alias
        and (root.findtext("showtitle") or "").strip() == alias
    )


def show_nfo_complete(path: Path, alias: str) -> bool:
    if not path.exists():
        return False
    text = path.read_text(encoding="utf-8", errors="ignore")
    return all(
        [
            show_nfo_identity_complete(text, alias),
            contains_tag(text, "year"),
            contains_tag(text, "studio"),
            '<thumb aspect="poster">poster.jpg</thumb>' in text,
        ]
    )


def ensure_show_nfo_fields(text: str, date: str, studio: str) -> str:
    if "</tvshow>" not in text:
        return text
    additions: list[str] = []
    if not contains_tag(text, "year"):
        additions.append(f"  <year>{html.escape(date[:4])}</year>")
    if not contains_tag(text, "studio"):
        additions.append(f"  <studio>{html.escape(studio)}</studio>")
    if '<thumb aspect="poster">poster.jpg</thumb>' not in text:
        additions.append('  <thumb aspect="poster">poster.jpg</thumb>')
    if not additions:
        return text
    return text.replace("</tvshow>", "\n".join(additions) + "\n</tvshow>", 1)


def episode_nfo_complete(path: Path, ep: Episode) -> bool:
    if not path.exists():
        return False
    text = path.read_text(encoding="utf-8", errors="ignore")
    return all(
        [
            contains_tag(text, "title", f"{ep.date} - {ep.title}"),
            contains_tag(text, "showtitle", ep.alias),
            contains_tag(text, "season", str(ep.season)),
            contains_tag(text, "episode", str(ep.episode)),
            contains_tag(text, "studio"),
        ]
    )


def collect_episodes(root: Path) -> list[Episode]:
    episodes: list[Episode] = []
    for mp4 in root.rglob("*.mp4"):
        ep = parse_episode(mp4, root)
        if ep is not None:
            episodes.append(ep)
    episodes.sort(key=lambda item: (str(item.show_dir), item.season, item.episode, item.path.name))
    return episodes


def duplicate_show_identities(episodes: list[Episode]) -> dict[str, list[Path]]:
    by_identity: dict[str, set[Path]] = {}
    for ep in episodes:
        by_identity.setdefault(ep.alias, set()).add(ep.show_dir)
    return {
        identity: sorted(dirs)
        for identity, dirs in by_identity.items()
        if len(dirs) > 1
    }


def unique_quarantine_path(quarantine_root: Path, relative_path: Path) -> Path:
    target = quarantine_root / relative_path
    if not target.exists():
        return target
    for index in range(1, 1000):
        candidate = target.with_name(f"{target.stem}.conflict{index}{target.suffix}")
        if not candidate.exists():
            return candidate
    raise RuntimeError(f"too many quarantine conflicts for {target}")


def quarantine_root_for_library(root: Path) -> Path:
    root = root.resolve()
    if root.parent == root:
        raise ValueError(f"media library root cannot be the filesystem root: {root}")
    quarantine_root = (
        root.parent / f".{root.name}-quarantine-library-sidecars" / datetime.now().strftime("%Y%m%d-%H%M%S")
    ).resolve()
    try:
        quarantine_root.relative_to(root)
    except ValueError:
        return quarantine_root
    raise ValueError(f"quarantine root must be outside media library root: {quarantine_root}")


def move_path(source: Path, target: Path) -> None:
    os.link(source, target)
    try:
        fsync_directory(target.parent)
    except OSError as move_error:
        try:
            target.unlink()
            fsync_directory(target.parent)
        except OSError as recovery_error:
            raise RuntimeError(
                f"target durability failed: {move_error}; target cleanup failed: {recovery_error}"
            ) from move_error
        raise
    try:
        source.unlink()
    except OSError as move_error:
        try:
            target.unlink(missing_ok=True)
            fsync_directory(target.parent)
        except OSError as recovery_error:
            raise RuntimeError(
                f"source unlink failed: {move_error}; target cleanup failed: {recovery_error}"
            ) from move_error
        raise
    try:
        fsync_directory(source.parent)
        if target.parent != source.parent:
            fsync_directory(target.parent)
    except OSError as move_error:
        try:
            os.link(target, source)
            fsync_directory(source.parent)
            target.unlink()
            fsync_directory(target.parent)
        except OSError as recovery_error:
            raise RuntimeError(
                f"move durability failed: {move_error}; source recovery failed: {recovery_error}"
            ) from move_error
        raise


def fsync_directory(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def execute_episode_moves(moves: list[tuple[Path, Path]], root: Path, apply: bool) -> None:
    for source, target in moves:
        print(f"[move] {source} -> {target}")
    if not apply:
        return

    for _, target in moves:
        target.parent.mkdir(parents=True, exist_ok=True)
    transaction_parent = root.parent / f".{root.name}-sidecar-transactions"
    transaction_parent.mkdir(parents=True, exist_ok=True)
    transaction_root = Path(tempfile.mkdtemp(prefix="episode-", dir=transaction_parent))
    journal_path = transaction_root / "moves.json"
    with journal_path.open("w", encoding="utf-8") as journal:
        json.dump(
            [{"source": str(source), "target": str(target)} for source, target in moves],
            journal,
            ensure_ascii=False,
            indent=2,
        )
        journal.write("\n")
        journal.flush()
        os.fsync(journal.fileno())
    fsync_directory(transaction_root)
    fsync_directory(transaction_parent)

    moved: list[tuple[Path, Path]] = []
    try:
        for source, target in moves:
            move_path(source, target)
            moved.append((source, target))
    except OSError as exc:
        rollback_errors: list[str] = []
        for source, target in reversed(moved):
            try:
                move_path(target, source)
            except OSError as rollback_exc:
                rollback_errors.append(f"{target} -> {source}: {rollback_exc}")
        if rollback_errors:
            raise RuntimeError(
                f"episode move failed: {exc}; rollback failed: {' | '.join(rollback_errors)}; "
                f"preserved transaction: {transaction_root}"
            ) from exc
        shutil.rmtree(transaction_root)
        raise
    shutil.rmtree(transaction_root)


def move_episode_group(
    pairs: list[tuple[Path, Path]],
    root: Path,
    apply: bool,
) -> tuple[bool, int, int]:
    conflicts = [dst for _, dst in pairs if dst.exists()]
    if conflicts:
        quarantine_root = quarantine_root_for_library(root)
        print(
            f"[episode-conflict] sources={len(pairs)} "
            f"targets={' | '.join(str(path) for path in conflicts)}"
        )
        moves: list[tuple[Path, Path]] = []
        for src, _ in sorted(pairs, key=lambda item: item[0].suffix.lower() != ".mp4"):
            relative = src.relative_to(root)
            quarantine = unique_quarantine_path(quarantine_root, relative)
            moves.append((src, quarantine))
        execute_episode_moves(moves, root, apply)
        return False, 0, len(pairs)

    moves = sorted(pairs, key=lambda item: item[0].suffix.lower() == ".mp4")
    execute_episode_moves(moves, root, apply)
    return True, len(pairs), 0


def copy_show_file_without_overwrite(source: Path, target: Path, apply: bool) -> bool:
    print(f"[show-file] {source} -> {target}")
    if not apply:
        return True

    target.parent.mkdir(parents=True, exist_ok=True)
    temp_path: Path | None = None
    try:
        with source.open("rb") as source_file, tempfile.NamedTemporaryFile(
            dir=target.parent,
            prefix=f".{target.stem}-",
            suffix=target.suffix,
            delete=False,
        ) as temp:
            temp_path = Path(temp.name)
            shutil.copyfileobj(source_file, temp)
            temp.flush()
            os.fsync(temp.fileno())
        temp_path.chmod(0o644)
        try:
            os.link(temp_path, target)
            return True
        except FileExistsError:
            if cover_complete(target):
                return True
            target.unlink(missing_ok=True)
            try:
                os.link(temp_path, target)
                return True
            except FileExistsError:
                return cover_complete(target)
    except OSError as exc:
        if cover_complete(target):
            return True
        print(f"[show-file-error] {target}: {exc}")
        return False
    finally:
        if temp_path is not None:
            temp_path.unlink(missing_ok=True)


def preserve_duplicate_show_metadata(identity: str, dirs: list[Path], root: Path, apply: bool) -> bool:
    copies: list[tuple[Path, Path]] = []
    for filename in ("poster.jpg", "tvshow.nfo"):
        canonical = root / identity / filename
        candidates = list(
            dict.fromkeys(
                path
                for path in [canonical, *(show_dir / filename for show_dir in dirs)]
                if cover_complete(path)
            )
        )
        if not candidates:
            continue
        by_digest: dict[str, list[Path]] = {}
        for candidate in candidates:
            digest = hashlib.sha256(candidate.read_bytes()).hexdigest()
            by_digest.setdefault(digest, []).append(candidate)
        if len(by_digest) > 1:
            joined = " | ".join(str(path) for path in candidates)
            print(f"[show-file-conflict] identity={identity} file={filename} candidates={joined}")
            return False
        if not cover_complete(canonical):
            copies.append((candidates[0], canonical))

    return all(copy_show_file_without_overwrite(source, target, apply) for source, target in copies)


def move_duplicate_show_dirs(root: Path, episodes: list[Episode], apply: bool) -> tuple[int, int, int, int]:
    by_show: dict[Path, list[Episode]] = {}
    for ep in episodes:
        by_show.setdefault(ep.show_dir, []).append(ep)

    moved_episodes = 0
    moved_files = 0
    quarantined_files = 0
    show_file_conflicts = 0
    for identity, dirs in sorted(duplicate_show_identities(episodes).items()):
        canonical_show_dir = root / identity
        canonical_season_dir = canonical_show_dir / "Season 01"
        if not preserve_duplicate_show_metadata(identity, dirs, root, apply):
            show_file_conflicts += 1
            continue

        for show_dir in dirs:
            if show_dir == canonical_show_dir:
                continue
            for ep in sorted(by_show.get(show_dir, []), key=lambda item: (item.season, item.episode, item.path.name)):
                new_stem = f"{identity}.S01E{ep.episode:04d}.{ep.date} - {ep.title}"
                pairs: list[tuple[Path, Path]] = []
                for suffix in SIDECAR_SUFFIXES:
                    src = ep.path.with_suffix(suffix)
                    if not src.exists():
                        continue
                    dst = canonical_season_dir / f"{new_stem}{suffix}"
                    pairs.append((src, dst))
                moved, group_moved_files, group_quarantined_files = move_episode_group(pairs, root, apply)
                if moved:
                    moved_episodes += 1
                moved_files += group_moved_files
                quarantined_files += group_quarantined_files

    return moved_episodes, moved_files, quarantined_files, show_file_conflicts


def cover_complete(path: Path) -> bool:
    try:
        return path.exists() and path.stat().st_size > 0
    except OSError:
        return False


def write_text_atomically(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_path: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            dir=path.parent,
            prefix=f".{path.name}.",
            suffix=".tmp",
            delete=False,
        ) as temp:
            temp_path = Path(temp.name)
            temp.write(content)
            temp.flush()
            os.fsync(temp.fileno())
        temp_path.chmod(0o644)
        temp_path.replace(path)
    finally:
        if temp_path is not None:
            temp_path.unlink(missing_ok=True)


def extract_cover(video_path: Path, cover_path: Path, ffmpeg_bin: str, apply: bool) -> bool:
    print(f"[cover] {cover_path}")
    if not apply:
        return True

    cover_path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = cover_path.with_name(f".{cover_path.stem}.tmp{cover_path.suffix}")
    if tmp_path.exists():
        tmp_path.unlink()
    cmd = [
        ffmpeg_bin,
        "-y",
        "-hide_banner",
        "-loglevel",
        "error",
        "-ss",
        "1",
        "-i",
        str(video_path),
        "-frames:v",
        "1",
        "-q:v",
        "2",
        str(tmp_path),
    ]
    result = subprocess.run(cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    if result.returncode != 0:
        if tmp_path.exists():
            tmp_path.unlink()
        print(f"[cover-error] {video_path}: {result.stderr.strip() or result.stdout.strip() or result.returncode}")
        return False
    if not cover_complete(tmp_path):
        if tmp_path.exists():
            tmp_path.unlink()
        print(f"[cover-error] {video_path}: ffmpeg produced empty cover")
        return False
    tmp_path.replace(cover_path)
    return True


def ensure_show_poster(show_dir: Path, episodes: list[Episode], apply: bool) -> bool:
    poster_path = show_dir / "poster.jpg"
    if cover_complete(poster_path):
        return True

    source_cover = next(
        (ep.path.with_suffix(".jpg") for ep in sorted(episodes, key=lambda item: (item.season, item.episode, item.path.name)) if cover_complete(ep.path.with_suffix(".jpg"))),
        None,
    )
    if source_cover is None and not apply and episodes:
        source_cover = sorted(episodes, key=lambda item: (item.season, item.episode, item.path.name))[0].path.with_suffix(".jpg")
    if source_cover is None:
        print(f"[show-poster-error] no episode cover is available: {poster_path}")
        return False

    return copy_show_file_without_overwrite(source_cover, poster_path, apply)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default="/volume2/docker/bililive-go/video")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--only-show", default="")
    parser.add_argument(
        "--plan-chronological-renumber",
        action="store_true",
        help="print a read-only bijective rename plan using exact recorded_at; never writes files",
    )
    parser.add_argument(
        "--fail-on-duplicate-shows",
        action="store_true",
        help="return a non-zero status when one normalized show identity maps to multiple directories",
    )
    parser.add_argument(
        "--merge-duplicate-shows",
        action="store_true",
        help="move duplicate show-directory episodes into the normalized canonical show directory; requires --apply",
    )
    parser.add_argument(
        "--ffmpeg",
        default=os.environ.get("FFMPEG_BIN", "ffmpeg"),
        help="ffmpeg binary used when --apply needs to extract missing episode covers",
    )
    args = parser.parse_args()
    if args.merge_duplicate_shows and not args.apply:
        parser.error("--merge-duplicate-shows requires --apply")

    root = Path(args.root).resolve()
    if root.parent == root:
        parser.error("--root cannot be the filesystem root")
    episodes = collect_episodes(root)
    if args.only_show:
        only_show = normalize_component(args.only_show)
        episodes = [
            ep
            for ep in episodes
            if ep.alias == only_show or normalize_component(ep.show_dir.name) == only_show
        ]
    if args.plan_chronological_renumber:
        if args.apply or args.merge_duplicate_shows:
            parser.error("--plan-chronological-renumber is read-only and cannot be combined with apply modes")
        plan = plan_chronological_episode_renumber(root, episodes)
        file_plan = plan_chronological_file_moves(root, plan)
        search_roots = [root]
        knowledge_sessions = root.parent / ".knowledge_sessions"
        if knowledge_sessions.exists():
            search_roots.append(knowledge_sessions)
        hidden_segments = root.parent / ".live_session_segments"
        if hidden_segments.exists():
            search_roots.append(hidden_segments)
        srt_video = root.parent / "srt_video"
        if srt_video.exists():
            search_roots.append(srt_video)
        parsed_json, references_by_source = chronological_reference_audit(search_roots, file_plan)
        matched_references = sum(references_by_source.values())
        nfo_edits = plan_chronological_nfo_edits(plan)
        json_edits = plan_chronological_json_edits(search_roots, file_plan)
        simulated_refs = simulate_chronological_json_references(search_roots, file_plan)
        conservation = chronological_media_conservation(root, file_plan)
        if simulated_refs.old_references != 0:
            raise ValueError("chronological plan leaves old JSON references after simulation")
        expected_new_references = sum(references_by_source.values())
        if simulated_refs.new_references < expected_new_references:
            raise ValueError("chronological plan does not preserve every JSON reference")
        if conservation.before_count != conservation.after_count or conservation.before_bytes != conservation.after_bytes:
            raise ValueError("chronological plan does not preserve media count and bytes")
        changed = [item for item in plan if item.source != item.target]
        for item in changed:
            print(
                "[chronological-renumber] "
                f"recorded_at={item.recorded_at.isoformat()} episode={item.episode} "
                f"source={item.source} target={item.target}"
            )
        for item in nfo_edits:
            print(f"[chronological-nfo] path={item.path} fields={json.dumps(item.fields, ensure_ascii=False, sort_keys=True)}")
        for item in json_edits:
            print(
                "[chronological-json] "
                f"path={item.path} pointer={item.pointer} old={item.old} new={item.new}"
            )
        print(
            f"chronological-renumber dry-run: episodes={len(plan)} changed={len(changed)} "
            f"unique_sources={len({item.source for item in plan})} "
            f"unique_targets={len({item.target for item in plan})} "
            f"files={len(file_plan)} json={parsed_json} references={matched_references} "
            f"nfo_edits={len(nfo_edits)} json_edits={len(json_edits)} "
            f"post_old_refs={simulated_refs.old_references} post_new_refs={simulated_refs.new_references} "
            f"media_before={conservation.before_count}/{conservation.before_bytes} "
            f"media_after={conservation.after_count}/{conservation.after_bytes} "
            "conflicts=0 monotonic=true"
        )
        return 0

    moved_episodes = 0
    moved_files = 0
    quarantined_files = 0
    show_file_conflicts = 0
    if args.merge_duplicate_shows:
        moved_episodes, moved_files, quarantined_files, show_file_conflicts = move_duplicate_show_dirs(root, episodes, args.apply)
        episodes = collect_episodes(root)
        if args.only_show:
            episodes = [
                ep
                for ep in episodes
                if ep.alias == only_show or normalize_component(ep.show_dir.name) == only_show
            ]

    by_show: dict[Path, list[Episode]] = {}
    for ep in episodes:
        by_show.setdefault(ep.show_dir, []).append(ep)

    duplicates = duplicate_show_identities(episodes)
    for identity, dirs in sorted(duplicates.items()):
        joined_dirs = " | ".join(str(path) for path in dirs)
        print(f"[duplicate-show] identity={identity} dirs={joined_dirs}")

    show_repairs = 0
    episode_repairs = 0
    cover_repairs = 0
    cover_failures = 0
    show_poster_repairs = 0
    show_poster_failures = 0
    for show_dir, show_episodes in sorted(by_show.items(), key=lambda item: str(item[0])):
        first = show_episodes[0]
        show_nfo = show_dir / "tvshow.nfo"
        sample_nfo = first.path.with_suffix(".nfo")
        studio = existing_studio(sample_nfo)
        if not show_nfo_complete(show_nfo, first.alias):
            show_repairs += 1
            print(f"[show] {show_nfo}")
            if args.apply:
                current = show_nfo.read_text(encoding="utf-8", errors="ignore") if show_nfo.exists() else ""
                content = (
                    ensure_show_nfo_fields(current, first.date, studio)
                    if show_nfo_identity_complete(current, first.alias)
                    else build_show_nfo(first.alias, first.date, studio)
                )
                write_text_atomically(show_nfo, content)

        for ep in show_episodes:
            nfo = ep.path.with_suffix(".nfo")
            studio = existing_studio(nfo)
            if not episode_nfo_complete(nfo, ep):
                episode_repairs += 1
                print(f"[episode] {nfo}")
                if args.apply:
                    write_text_atomically(nfo, build_episode_nfo(ep, studio))

            cover = ep.path.with_suffix(".jpg")
            if not cover_complete(cover):
                cover_repairs += 1
                if not extract_cover(ep.path, cover, args.ffmpeg, args.apply):
                    cover_failures += 1

        if not cover_complete(show_dir / "poster.jpg"):
            show_poster_repairs += 1
            if not ensure_show_poster(show_dir, show_episodes, args.apply):
                show_poster_failures += 1

    mode = "apply" if args.apply else "dry-run"
    print(
        f"{datetime.now().isoformat(timespec='seconds')} {mode}: "
        f"shows={len(by_show)} episodes={len(episodes)} "
        f"show_repairs={show_repairs} episode_repairs={episode_repairs} "
        f"cover_repairs={cover_repairs} cover_failures={cover_failures} "
        f"show_poster_repairs={show_poster_repairs} show_poster_failures={show_poster_failures} "
        f"show_file_conflicts={show_file_conflicts} "
        f"duplicate_show_identities={len(duplicates)} "
        f"moved_episodes={moved_episodes} moved_files={moved_files} "
        f"quarantined_files={quarantined_files}"
    )
    if cover_failures or show_poster_failures or show_file_conflicts:
        return 3
    if duplicates and args.fail_on_duplicate_shows:
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
