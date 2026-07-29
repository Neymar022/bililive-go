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
from datetime import datetime
from pathlib import Path


EPISODE_RE = re.compile(
    r"^(?P<alias>.+?)\.S(?P<season>\d{2})E(?P<episode>\d{4})"
    r"(?:-S\d{2}E\d{4})?\.(?P<date>\d{4}-\d{2}-\d{2}) - (?P<title>.+)$"
)
SIDECAR_SUFFIXES = (
    ".mp4",
    ".nfo",
    ".jpg",
    ".srt",
    ".ass",
    ".subtitle.json",
    ".transcript.json",
)


@dataclass(frozen=True)
class Episode:
    path: Path
    show_dir: Path
    season: int
    episode: int
    date: str
    alias: str
    title: str


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
