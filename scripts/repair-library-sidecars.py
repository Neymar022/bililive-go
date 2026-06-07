#!/usr/bin/env python3
"""Repair UGREEN/Plex-style sidecar metadata for bililive-go TV libraries.

Default mode is dry-run. Use --apply to write tvshow.nfo and incomplete
episode .nfo files. The script never deletes or moves media files.
"""

from __future__ import annotations

import argparse
import html
import re
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path


EPISODE_RE = re.compile(
    r"^(?P<alias>.+?)\.S(?P<season>\d{2})E(?P<episode>\d{4})"
    r"(?:-S\d{2}E\d{4})?\.(?P<date>\d{4}-\d{2}-\d{2}) - (?P<title>.+)$"
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
        alias=match.group("alias").strip() or path.parent.parent.name,
        title=match.group("title").strip() or "未命名直播",
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


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default="/volume2/docker/bililive-go/video")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--only-show", default="")
    args = parser.parse_args()

    root = Path(args.root)
    episodes = collect_episodes(root)
    if args.only_show:
        episodes = [ep for ep in episodes if ep.alias == args.only_show or ep.show_dir.name == args.only_show]

    by_show: dict[Path, list[Episode]] = {}
    for ep in episodes:
        by_show.setdefault(ep.show_dir, []).append(ep)

    show_repairs = 0
    episode_repairs = 0
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

    mode = "apply" if args.apply else "dry-run"
    print(
        f"{datetime.now().isoformat(timespec='seconds')} {mode}: "
        f"shows={len(by_show)} episodes={len(episodes)} "
        f"show_repairs={show_repairs} episode_repairs={episode_repairs}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
