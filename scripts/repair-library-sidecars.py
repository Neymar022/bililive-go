#!/usr/bin/env python3
"""Repair UGREEN/Plex-style sidecar metadata for bililive-go TV libraries.

Default mode is dry-run. Use --apply to write tvshow.nfo and incomplete
episode .nfo files. The script never deletes media files. Duplicate show
directory merges require both --merge-duplicate-shows and --apply.
"""

from __future__ import annotations

import argparse
import html
import os
import re
import subprocess
import unicodedata
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
    value = "".join(ch for ch in value if unicodedata.category(ch) not in {"Cc", "Cf"})
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


def show_nfo_complete(path: Path, alias: str) -> bool:
    if not path.exists():
        return False
    text = path.read_text(encoding="utf-8", errors="ignore")
    return all(
        [
            contains_tag(text, "title", alias),
            contains_tag(text, "showtitle", alias),
            contains_tag(text, "year"),
            contains_tag(text, "studio"),
        ]
    )


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


def move_or_quarantine(src: Path, dst: Path, root: Path, apply: bool) -> tuple[bool, bool]:
    if dst.exists():
        quarantine_root = root / ".quarantine-library-sidecars" / datetime.now().strftime("%Y%m%d-%H%M%S")
        try:
            rel = src.relative_to(root)
        except ValueError:
            rel = Path(src.name)
        quarantine = unique_quarantine_path(quarantine_root, rel)
        print(f"[quarantine] {src} -> {quarantine} (conflict: {dst})")
        if apply:
            quarantine.parent.mkdir(parents=True, exist_ok=True)
            src.replace(quarantine)
        return False, True

    print(f"[move] {src} -> {dst}")
    if apply:
        dst.parent.mkdir(parents=True, exist_ok=True)
        src.replace(dst)
    return True, False


def move_duplicate_show_dirs(root: Path, episodes: list[Episode], apply: bool) -> tuple[int, int, int]:
    by_show: dict[Path, list[Episode]] = {}
    for ep in episodes:
        by_show.setdefault(ep.show_dir, []).append(ep)

    moved_episodes = 0
    moved_files = 0
    quarantined_files = 0
    for identity, dirs in sorted(duplicate_show_identities(episodes).items()):
        canonical_show_dir = root / identity
        canonical_season_dir = canonical_show_dir / "Season 01"

        for show_dir in dirs:
            if show_dir == canonical_show_dir:
                continue
            for ep in sorted(by_show.get(show_dir, []), key=lambda item: (item.season, item.episode, item.path.name)):
                new_stem = f"{identity}.S01E{ep.episode:04d}.{ep.date} - {ep.title}"
                moved_episodes += 1
                for suffix in SIDECAR_SUFFIXES:
                    src = ep.path.with_suffix(suffix)
                    if not src.exists():
                        continue
                    dst = canonical_season_dir / f"{new_stem}{suffix}"
                    moved, quarantined = move_or_quarantine(src, dst, root, apply)
                    if moved:
                        moved_files += 1
                    if quarantined:
                        quarantined_files += 1

    return moved_episodes, moved_files, quarantined_files


def cover_complete(path: Path) -> bool:
    try:
        return path.exists() and path.stat().st_size > 0
    except OSError:
        return False


def extract_cover(video_path: Path, cover_path: Path, ffmpeg_bin: str, apply: bool) -> bool:
    print(f"[cover] {cover_path}")
    if not apply:
        return True

    cover_path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = cover_path.with_name(f".{cover_path.name}.tmp")
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

    root = Path(args.root)
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
    if args.merge_duplicate_shows:
        moved_episodes, moved_files, quarantined_files = move_duplicate_show_dirs(root, episodes, args.apply)
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
    for show_dir, show_episodes in sorted(by_show.items(), key=lambda item: str(item[0])):
        first = show_episodes[0]
        show_nfo = show_dir / "tvshow.nfo"
        sample_nfo = first.path.with_suffix(".nfo")
        studio = existing_studio(sample_nfo)
        if not show_nfo_complete(show_nfo, first.alias):
            show_repairs += 1
            print(f"[show] {show_nfo}")
            if args.apply:
                show_nfo.write_text(build_show_nfo(first.alias, first.date, studio), encoding="utf-8")

        for ep in show_episodes:
            nfo = ep.path.with_suffix(".nfo")
            studio = existing_studio(nfo)
            if not episode_nfo_complete(nfo, ep):
                episode_repairs += 1
                print(f"[episode] {nfo}")
                if args.apply:
                    nfo.write_text(build_episode_nfo(ep, studio), encoding="utf-8")

            cover = ep.path.with_suffix(".jpg")
            if not cover_complete(cover):
                cover_repairs += 1
                if not extract_cover(ep.path, cover, args.ffmpeg, args.apply):
                    cover_failures += 1

    mode = "apply" if args.apply else "dry-run"
    print(
        f"{datetime.now().isoformat(timespec='seconds')} {mode}: "
        f"shows={len(by_show)} episodes={len(episodes)} "
        f"show_repairs={show_repairs} episode_repairs={episode_repairs} "
        f"cover_repairs={cover_repairs} cover_failures={cover_failures} "
        f"duplicate_show_identities={len(duplicates)} "
        f"moved_episodes={moved_episodes} moved_files={moved_files} "
        f"quarantined_files={quarantined_files}"
    )
    if cover_failures:
        return 3
    if duplicates and args.fail_on_duplicate_shows:
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
