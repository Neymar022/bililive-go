#!/usr/bin/env python3
import argparse
import json
import re
import sys
from dataclasses import asdict, dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Sequence, Tuple
from urllib.error import URLError
from urllib.request import urlopen
from xml.sax.saxutils import escape


LEGACY_PATTERN = re.compile(
    r"^\[(?P<recorded_at>\d{4}-\d{2}-\d{2} \d{2}-\d{2}-\d{2})\]\[(?P<host_name>.*?)\]\[(?P<title>.*?)\]$"
)
NORMALIZED_PATTERN = re.compile(
    r"^(?P<alias_name>.+?) - (?P<recorded_at>\d{4}-\d{2}-\d{2} \d{2}-\d{2}-\d{2}) - (?P<title>.+)$"
)
VIDEO_EXTENSIONS = {
    ".mp4",
    ".m4v",
    ".mkv",
    ".mov",
    ".avi",
    ".flv",
    ".ts",
    ".webm",
}
DEFAULT_ROOTS = [
    "/volume2/docker/bililive-go/source",
]
DEFAULT_REPORT_DIR = "/volume2/docker/bililive-go/reports"
INVALID_FILENAME_CHARS = re.compile(r'[<>:"/\\|?*\x00-\x1F]')


@dataclass
class VideoMetadata:
    recorded_at: str
    host_name: str
    title: str
    extension: str
    source_format: str


@dataclass
class ProcessResult:
    source_path: Path
    target_path: Path
    status: str
    reason: str = ""
    platform_name: str = ""
    alias_name: str = ""
    title: str = ""
    recorded_at: str = ""

    def to_dict(self) -> Dict[str, str]:
        return {
            "source_path": str(self.source_path),
            "target_path": str(self.target_path),
            "status": self.status,
            "reason": self.reason,
            "platform_name": self.platform_name,
            "alias_name": self.alias_name,
            "title": self.title,
            "recorded_at": self.recorded_at,
        }


class AliasResolver:
    def __init__(self, aliases: Dict[Tuple[str, str], str]) -> None:
        self.aliases: Dict[Tuple[str, str], str] = {}
        self.alias_platforms: Dict[str, str] = {}
        for (platform_name, host_name), alias_name in aliases.items():
            platform_name = sanitize_component(platform_name)
            host_name = sanitize_component(host_name)
            alias_name = sanitize_component(alias_name)
            if not platform_name or not host_name or not alias_name:
                continue
            self.aliases[(platform_name, host_name)] = alias_name
            self.alias_platforms.setdefault(host_name, platform_name)
            self.alias_platforms.setdefault(alias_name, platform_name)

    def resolve(self, platform_name: str, host_name: str, current_dir_name: str) -> str:
        host_name = sanitize_component(host_name)
        current_dir_name = sanitize_component(current_dir_name)
        alias_name = self.aliases.get((platform_name, host_name))
        if alias_name:
            return sanitize_component(alias_name)
        if current_dir_name:
            return current_dir_name
        if host_name:
            return host_name
        return "未分类主播"

    def resolve_platform(self, platform_name: str, host_name: str, current_dir_name: str) -> str:
        platform_name = sanitize_component(platform_name)
        if platform_name and platform_name not in {"source"}:
            return platform_name

        current_dir_name = sanitize_component(current_dir_name)
        host_name = sanitize_component(host_name)
        for candidate in (current_dir_name, host_name):
            if candidate and candidate in self.alias_platforms:
                return self.alias_platforms[candidate]
        return "未知平台"


@dataclass
class Organizer:
    alias_resolver: AliasResolver
    report_path: Path
    results: List[ProcessResult] = field(default_factory=list)

    def process_video_file(
        self,
        video_path: Path,
        platform_name: str,
        platform_root: Path,
        dry_run: bool,
    ) -> ProcessResult:
        metadata = parse_video_filename(video_path.name)
        current_dir_name = video_path.parent.name if video_path.parent != platform_root else ""
        resolved_platform_name = self.alias_resolver.resolve_platform(platform_name, metadata.host_name, current_dir_name)
        alias_name = self.alias_resolver.resolve(resolved_platform_name, metadata.host_name, current_dir_name)
        title = sanitize_component(metadata.title) or "未命名直播"
        target_filename = build_target_filename(alias_name, metadata.recorded_at, title, metadata.extension)
        target_dir = platform_root / alias_name
        target_path = target_dir / target_filename
        source_nfo = video_path.with_suffix(".nfo")
        target_nfo = target_path.with_suffix(".nfo")
        nfo_text = build_nfo_xml(
            platform_name=resolved_platform_name,
            alias_name=alias_name,
            title=title,
            recorded_at=metadata.recorded_at,
        )

        status = "unchanged"
        reason = ""
        rename_needed = video_path != target_path
        nfo_needs_write = not target_nfo.exists() or target_nfo.read_text(encoding="utf-8") != nfo_text

        if rename_needed and target_path.exists():
            result = ProcessResult(
                source_path=video_path,
                target_path=target_path,
                status="conflict",
                reason="target video already exists",
                platform_name=resolved_platform_name,
                alias_name=alias_name,
                title=title,
                recorded_at=metadata.recorded_at,
            )
            self.results.append(result)
            return result

        if dry_run:
            status = "updated" if rename_needed or nfo_needs_write else "unchanged"
        else:
            target_dir.mkdir(parents=True, exist_ok=True)
            if rename_needed:
                video_path.rename(target_path)
                if source_nfo.exists() and source_nfo != target_nfo:
                    source_nfo.rename(target_nfo)
                status = "updated"
            if nfo_needs_write:
                target_nfo.write_text(nfo_text, encoding="utf-8")
                status = "updated"
            if not rename_needed and not nfo_needs_write:
                status = "unchanged"

        result = ProcessResult(
            source_path=video_path,
            target_path=target_path,
            status=status,
            reason=reason,
            platform_name=resolved_platform_name,
            alias_name=alias_name,
            title=title,
            recorded_at=metadata.recorded_at,
        )
        self.results.append(result)
        return result

    def save_report(self, dry_run: bool) -> Path:
        self.report_path.parent.mkdir(parents=True, exist_ok=True)
        summary = {}
        for result in self.results:
            summary[result.status] = summary.get(result.status, 0) + 1
        payload = {
            "generated_at": datetime.now().isoformat(timespec="seconds"),
            "dry_run": dry_run,
            "summary": summary,
            "results": [result.to_dict() for result in self.results],
        }
        self.report_path.write_text(
            json.dumps(payload, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        return self.report_path


def sanitize_component(text: str) -> str:
    sanitized = INVALID_FILENAME_CHARS.sub(" ", text or "")
    sanitized = sanitized.replace("\n", " ").replace("\r", " ")
    sanitized = re.sub(r"\s+", " ", sanitized).strip(" .")
    return sanitized


def truncate_utf8(text: str, max_bytes: int) -> str:
    if len(text.encode("utf-8")) <= max_bytes:
        return text
    encoded = text.encode("utf-8")[:max_bytes]
    while encoded:
        try:
            return encoded.decode("utf-8").rstrip(" .")
        except UnicodeDecodeError:
            encoded = encoded[:-1]
    return ""


def parse_video_filename(filename: str) -> VideoMetadata:
    extension = Path(filename).suffix
    stem = Path(filename).stem
    legacy_match = LEGACY_PATTERN.match(stem)
    if legacy_match:
        return VideoMetadata(
            recorded_at=legacy_match.group("recorded_at"),
            host_name=sanitize_component(legacy_match.group("host_name")),
            title=sanitize_component(legacy_match.group("title")),
            extension=extension,
            source_format="legacy",
        )
    normalized_match = NORMALIZED_PATTERN.match(stem)
    if normalized_match:
        alias_name = sanitize_component(normalized_match.group("alias_name"))
        return VideoMetadata(
            recorded_at=normalized_match.group("recorded_at"),
            host_name=alias_name,
            title=sanitize_component(normalized_match.group("title")),
            extension=extension,
            source_format="normalized",
        )
    fallback_recorded_at = datetime.now().strftime("%Y-%m-%d %H-%M-%S")
    return VideoMetadata(
        recorded_at=fallback_recorded_at,
        host_name="",
        title=sanitize_component(stem),
        extension=extension,
        source_format="fallback",
    )


def build_target_filename(alias_name: str, recorded_at: str, title: str, extension: str) -> str:
    alias_name = sanitize_component(alias_name) or "未分类主播"
    title = sanitize_component(title) or "未命名直播"
    prefix = f"{alias_name} - {recorded_at} - "
    max_bytes = 255 - len(extension.encode("utf-8"))
    prefix_bytes = len(prefix.encode("utf-8"))
    if prefix_bytes >= max_bytes:
        alias_name = truncate_utf8(alias_name, max(32, max_bytes // 2))
        prefix = f"{alias_name} - {recorded_at} - "
        prefix_bytes = len(prefix.encode("utf-8"))
    title = truncate_utf8(title, max(1, max_bytes - prefix_bytes))
    return f"{prefix}{title}{extension}"


def build_nfo_xml(platform_name: str, alias_name: str, title: str, recorded_at: str) -> str:
    dt = datetime.strptime(recorded_at, "%Y-%m-%d %H-%M-%S")
    premiered = dt.strftime("%Y-%m-%d")
    dateadded = dt.strftime("%Y-%m-%d %H:%M:%S")
    sort_title = f"{alias_name} - {recorded_at}"
    plot = f"{platform_name} | 主播: {alias_name} | 标题: {title} | 录制时间: {recorded_at}"
    set_overview = f"{alias_name} 的直播录屏合集"

    return "\n".join(
        [
            "<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>",
            "<movie>",
            f"  <title>{escape(title)}</title>",
            f"  <originaltitle>{escape(title)}</originaltitle>",
            f"  <sorttitle>{escape(sort_title)}</sorttitle>",
            "  <set>",
            f"    <name>{escape(alias_name)}</name>",
            f"    <overview>{escape(set_overview)}</overview>",
            "  </set>",
            f"  <series>{escape(alias_name)}</series>",
            f"  <studio>{escape(platform_name)}</studio>",
            f"  <plot>{escape(plot)}</plot>",
            "  <genre>直播录屏</genre>",
            "  <tag>直播录屏</tag>",
            f"  <premiered>{premiered}</premiered>",
            f"  <dateadded>{dateadded}</dateadded>",
            "  <actor>",
            f"    <name>{escape(alias_name)}</name>",
            "  </actor>",
            "</movie>",
            "",
        ]
    )


def load_aliases_from_api(api_url: str, timeout_seconds: int) -> Dict[Tuple[str, str], str]:
    with urlopen(api_url, timeout=timeout_seconds) as response:
        payload = json.loads(response.read().decode("utf-8"))
    aliases: Dict[Tuple[str, str], str] = {}
    for item in payload:
        platform_name = sanitize_component(item.get("platform_cn_name", ""))
        host_name = sanitize_component(item.get("host_name", ""))
        nick_name = sanitize_component(item.get("nick_name", "")) or host_name
        if platform_name and host_name and nick_name:
            aliases[(platform_name, host_name)] = nick_name
            aliases[(platform_name, nick_name)] = nick_name
    return aliases


def is_video_candidate(path: Path) -> bool:
    if not path.is_file():
        return False
    if path.name.startswith("."):
        return False
    if any(part.startswith(".appdata") for part in path.parts):
        return False
    if path.suffix.lower() not in VIDEO_EXTENSIONS:
        return False
    return True


def iter_video_files(roots: Sequence[Path], explicit_paths: Sequence[Path]) -> Iterable[Tuple[Path, Path, str]]:
    if explicit_paths:
        for target_path in explicit_paths:
            if target_path.is_dir():
                for file_path in sorted(target_path.rglob("*")):
                    if is_video_candidate(file_path):
                        platform_root = find_platform_root(file_path, roots)
                        yield file_path, platform_root, platform_root.name
            elif is_video_candidate(target_path):
                platform_root = find_platform_root(target_path, roots)
                yield target_path, platform_root, platform_root.name
        return

    for root in roots:
        for file_path in sorted(root.rglob("*")):
            if is_video_candidate(file_path):
                yield file_path, root, root.name


def find_platform_root(file_path: Path, roots: Sequence[Path]) -> Path:
    for root in roots:
        try:
            file_path.relative_to(root)
            return root
        except ValueError:
            continue
    raise ValueError(f"{file_path} is not inside configured roots")


def default_report_path(report_dir: Path, dry_run: bool) -> Path:
    timestamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    suffix = "dry-run" if dry_run else "apply"
    return report_dir / f"bililive-organizer-{suffix}-{timestamp}.json"


def run(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(description="Normalize bililive-go recordings and write NFO sidecars.")
    parser.add_argument("--api-url", default="http://127.0.0.1:18090/api/lives")
    parser.add_argument("--timeout-seconds", type=int, default=5)
    parser.add_argument("--root", action="append", dest="roots", default=[])
    parser.add_argument("--path", action="append", dest="paths", default=[])
    parser.add_argument("--report-dir", default=DEFAULT_REPORT_DIR)
    parser.add_argument("--report-file", default="")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args(argv)

    roots = [Path(root) for root in (args.roots or DEFAULT_ROOTS)]
    paths = [Path(path) for path in args.paths]
    report_path = Path(args.report_file) if args.report_file else default_report_path(Path(args.report_dir), args.dry_run)

    try:
        aliases = load_aliases_from_api(args.api_url, args.timeout_seconds)
    except (URLError, json.JSONDecodeError, TimeoutError):
        aliases = {}

    organizer = Organizer(alias_resolver=AliasResolver(aliases), report_path=report_path)
    for video_path, platform_root, platform_name in iter_video_files(roots, paths):
        organizer.process_video_file(video_path, platform_name=platform_name, platform_root=platform_root, dry_run=args.dry_run)

    report = organizer.save_report(dry_run=args.dry_run)
    print(json.dumps({"report": str(report), "summary": summarize(organizer.results)}, ensure_ascii=False))
    return 0


def summarize(results: Sequence[ProcessResult]) -> Dict[str, int]:
    summary: Dict[str, int] = {}
    for result in results:
        summary[result.status] = summary.get(result.status, 0) + 1
    return summary


if __name__ == "__main__":
    sys.exit(run())
