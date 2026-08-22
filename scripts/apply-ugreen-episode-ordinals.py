#!/usr/bin/env python3
"""为 UGREEN 兼容公开集号提供带日志的 NFO-only apply/rollback。"""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
import re
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from types import ModuleType


def load_repair_module(path: Path) -> ModuleType:
    spec = importlib.util.spec_from_file_location("repair_library_sidecars_ordinals", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load repair script: {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def fsync_directory(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def atomic_write(path: Path, data: bytes, mode: int) -> None:
    temporary: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix=f".{path.name}.", delete=False) as output:
            temporary = Path(output.name)
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        temporary.chmod(mode)
        os.replace(temporary, path)
        fsync_directory(path.parent)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def render_episode_nfo(path: Path, old_episode: int, ordinal: int) -> bytes:
    text = path.read_text(encoding="utf-8")
    pattern = re.compile(r"(<episode>)(.*?)(</episode>)", re.S)
    match = pattern.search(text)
    if match is None or match.group(2).strip() != str(old_episode):
        raise ValueError(f"NFO fixed point changed: {path}")
    return (
        text[: match.start()]
        + match.group(1)
        + str(ordinal)
        + match.group(3)
        + text[match.end() :]
    ).encode("utf-8")


def build_plan(module: ModuleType, root: Path) -> tuple[list[object], dict[str, object]]:
    episodes = module.collect_episodes(root)
    plan = module.plan_ugreen_episode_ordinals(episodes)
    changed = [item for item in plan if item.old_episode != item.ordinal]
    media = [
        {
            "path": str(item.path),
            "inode": item.path.stat().st_ino,
            "size": item.path.stat().st_size,
        }
        for item in plan
    ]
    fingerprint_value = {
        "root": str(root),
        "entries": [
            {
                "path": str(item.path),
                "nfo": str(item.nfo_path),
                "recorded_at": item.recorded_at.isoformat(),
                "identity": item.identity,
                "old_episode": item.old_episode,
                "ordinal": item.ordinal,
                "nfo_sha256": sha256_file(item.nfo_path),
            }
            for item in plan
        ],
        "media": media,
    }
    fingerprint = sha256_bytes(
        json.dumps(fingerprint_value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    )
    return changed, {
        "fingerprint": fingerprint,
        "episodes": len(plan),
        "shows": len({item.path.parent.parent for item in plan}),
        "changed": len(changed),
        "media_count": len(media),
        "media_bytes": sum(item["size"] for item in media),
        "media": media,
    }


def write_manifest(path: Path, value: dict[str, object]) -> None:
    value["updated_at"] = datetime.now(timezone.utc).isoformat()
    atomic_write(path, (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8"), 0o600)


def validate_media(snapshot: list[dict[str, object]]) -> None:
    for item in snapshot:
        path = Path(str(item["path"]))
        stat = path.stat()
        if stat.st_ino != item["inode"] or stat.st_size != item["size"]:
            raise RuntimeError(f"media changed: {path}")


def apply_plan(
    module: ModuleType,
    root: Path,
    backup_dir: Path,
    repair_script: Path,
    driver_script: Path,
    expected_fingerprint: str,
) -> None:
    changed, summary = build_plan(module, root)
    if summary["fingerprint"] != expected_fingerprint:
        raise RuntimeError(
            f"fixed point changed: expected {expected_fingerprint}, got {summary['fingerprint']}"
        )
    if not changed:
        raise RuntimeError("ordinal plan is already applied")

    manifest_path = backup_dir / "manifest.json"
    originals = backup_dir / "original-nfo"
    if manifest_path.exists() or originals.exists():
        raise FileExistsError(f"backup operation already initialized: {backup_dir}")
    originals.mkdir(parents=True)
    entries: list[dict[str, object]] = []
    for item in changed:
        relative = item.nfo_path.relative_to(root)
        backup = originals / relative
        backup.parent.mkdir(parents=True, exist_ok=True)
        os.link(item.nfo_path, backup)
        entries.append(
            {
                "path": str(item.nfo_path),
                "backup": str(backup),
                "old_episode": item.old_episode,
                "ordinal": item.ordinal,
                "before_sha256": sha256_file(item.nfo_path),
            }
        )
    fsync_directory(originals)
    manifest: dict[str, object] = {
        "version": 1,
        "state": "prepared",
        "root": str(root),
        "fingerprint": expected_fingerprint,
        "repair_script_sha256": sha256_file(repair_script),
        "driver_script_sha256": sha256_file(driver_script),
        "episodes": summary["episodes"],
        "shows": summary["shows"],
        "changed": summary["changed"],
        "media_count": summary["media_count"],
        "media_bytes": summary["media_bytes"],
        "media": summary["media"],
        "entries": entries,
    }
    write_manifest(manifest_path, manifest)

    applied: list[dict[str, object]] = []
    try:
        for item, entry in zip(changed, entries, strict=True):
            if sha256_file(item.nfo_path) != entry["before_sha256"]:
                raise RuntimeError(f"NFO changed after backup: {item.nfo_path}")
            rendered = render_episode_nfo(item.nfo_path, item.old_episode, item.ordinal)
            atomic_write(item.nfo_path, rendered, item.nfo_path.stat().st_mode & 0o777)
            entry["after_sha256"] = sha256_file(item.nfo_path)
            applied.append(entry)
        validate_media(summary["media"])
        remaining, after = build_plan(module, root)
        if remaining or after["changed"] != 0:
            raise RuntimeError(f"ordinal postcondition failed: remaining={after['changed']}")
        manifest["state"] = "applied"
        write_manifest(manifest_path, manifest)
    except Exception:
        for entry in reversed(applied):
            backup = Path(str(entry["backup"]))
            path = Path(str(entry["path"]))
            atomic_write(path, backup.read_bytes(), backup.stat().st_mode & 0o777)
        manifest["state"] = "rolled_back"
        write_manifest(manifest_path, manifest)
        raise


def rollback(backup_dir: Path) -> None:
    manifest_path = backup_dir / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("state") != "applied":
        raise RuntimeError(f"cannot rollback manifest state: {manifest.get('state')}")
    entries = list(reversed(manifest["entries"]))
    for entry in entries:
        path = Path(entry["path"])
        if sha256_file(path) != entry["after_sha256"]:
            raise RuntimeError(f"NFO changed after apply: {path}")
        backup = Path(entry["backup"])
        if sha256_file(backup) != entry["before_sha256"]:
            raise RuntimeError(f"NFO backup changed after apply: {backup}")
    validate_media(manifest["media"])
    for entry in entries:
        path = Path(entry["path"])
        backup = Path(entry["backup"])
        atomic_write(path, backup.read_bytes(), backup.stat().st_mode & 0o777)
    validate_media(manifest["media"])
    manifest["state"] = "rolled_back"
    write_manifest(manifest_path, manifest)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default="/volume2/docker/bililive-go/video")
    parser.add_argument("--repair-script", required=True)
    parser.add_argument("--backup-dir", required=True)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--rollback", action="store_true")
    parser.add_argument("--expected-fingerprint", default="")
    args = parser.parse_args()
    if args.apply == args.rollback and (args.apply or args.rollback):
        parser.error("choose only one of --apply or --rollback")

    root = Path(args.root).resolve()
    repair_script = Path(args.repair_script).resolve()
    backup_dir = Path(args.backup_dir).resolve()
    driver_script = Path(__file__).resolve()
    module = load_repair_module(repair_script)

    if args.rollback:
        rollback(backup_dir)
        print("ugreen-episode-ordinals rollback: state=rolled_back")
        return 0
    if args.apply:
        if not args.expected_fingerprint:
            parser.error("--apply requires --expected-fingerprint")
        apply_plan(module, root, backup_dir, repair_script, driver_script, args.expected_fingerprint)
        print(f"ugreen-episode-ordinals apply: state=applied backup={backup_dir}")
        return 0

    _, summary = build_plan(module, root)
    print(
        f"ugreen-episode-ordinals fixed-point: shows={summary['shows']} episodes={summary['episodes']} "
        f"changed={summary['changed']} media={summary['media_count']}/{summary['media_bytes']} "
        f"fingerprint={summary['fingerprint']}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
