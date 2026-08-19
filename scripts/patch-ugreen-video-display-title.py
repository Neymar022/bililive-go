#!/usr/bin/env python3
"""以 fail-closed 方式修复 UGREEN 影视中心的长集号展示回退。"""

from __future__ import annotations

import argparse
import gzip
import hashlib
import os
from pathlib import Path
import shutil
import tempfile
from typing import NamedTuple


PATCHES = {
    "recent": (
        '(0,r.TI)(i)?`${(0,r.WP)(s)}`:a?',
        '(0,r.TI)(i)?a||`${(0,r.WP)(s)}`:a?',
    ),
    "card": (
        't===g.OY?(0,r.WP)(a):i?',
        't===g.OY?i||(0,r.WP)(a):i?',
    ),
    "serial": (
        'e.isUnRecognizedEpisode(i.episode)?e.UNRECOGNIZED_EPISODE_TEXT:i.episode',
        'e.isUnRecognizedEpisode(i.episode)?i.ep_name||e.UNRECOGNIZED_EPISODE_TEXT:i.episode',
    ),
}


def patch_javascript(source: str) -> tuple[str, dict[str, int]]:
    counts: dict[str, int] = {}
    states: set[str] = set()
    for name, (old, new) in PATCHES.items():
        old_count = source.count(old)
        new_count = source.count(new)
        if old_count == 1 and new_count == 0:
            states.add("unpatched")
        elif old_count == 0 and new_count == 1:
            states.add("patched")
        else:
            raise RuntimeError(
                f"{name}: expected exactly one known unpatched or patched expression, "
                f"found old={old_count} new={new_count}"
            )
        counts[name] = old_count

    if len(states) != 1:
        raise RuntimeError(f"vendor bundle has a mixed patch state: {sorted(states)}")
    if states == {"patched"}:
        return source, counts

    for old, new in PATCHES.values():
        source = source.replace(old, new, 1)
    return source, counts


def read_asset(path: Path) -> tuple[str, bool]:
    raw = path.read_bytes()
    compressed = path.suffix == ".gz"
    if compressed:
        raw = gzip.decompress(raw)
    return raw.decode("utf-8"), compressed


def encode_asset(source: str, compressed: bool) -> bytes:
    raw = source.encode("utf-8")
    return gzip.compress(raw, mtime=0) if compressed else raw


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


class AssetPlan(NamedTuple):
    path: Path
    patched: str
    compressed: bool
    before: bytes
    after: bytes
    state: str


def prepare_assets(assets: list[Path]) -> list[AssetPlan]:
    plans: list[AssetPlan] = []
    for asset in assets:
        source, compressed = read_asset(asset)
        patched, counts = patch_javascript(source)
        before = asset.read_bytes()
        after = encode_asset(patched, compressed)
        state = "already-patched" if all(value == 0 for value in counts.values()) else "ready"
        plans.append(AssetPlan(asset, patched, compressed, before, after, state))

    states = {plan.state for plan in plans}
    if len(states) != 1:
        raise RuntimeError(f"assets have mixed patch states: {sorted(states)}")
    return plans


def atomic_write(path: Path, data: bytes) -> None:
    stat = path.stat()
    fd, temp_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temp_path = Path(temp_name)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temp_path, stat.st_mode)
        os.chown(temp_path, stat.st_uid, stat.st_gid)
        os.replace(temp_path, path)
        directory_fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if temp_path.exists():
            temp_path.unlink()


def apply_assets(plans: list[AssetPlan], backup_dir: Path) -> None:
    backup_paths = [backup_dir / plan.path.name for plan in plans]
    if len(set(backup_paths)) != len(backup_paths):
        raise RuntimeError("backup asset names are not unique")
    for backup in backup_paths:
        if backup.exists():
            raise RuntimeError(f"backup already exists: {backup}")

    backup_dir.mkdir(parents=True, exist_ok=True)
    for plan, backup in zip(plans, backup_paths):
        shutil.copy2(plan.path, backup)

    attempted: list[AssetPlan] = []
    try:
        for plan in plans:
            attempted.append(plan)
            atomic_write(plan.path, plan.after)
            verified, _ = read_asset(plan.path)
            if verified != plan.patched:
                raise RuntimeError(f"post-write verification failed: {plan.path}")
    except Exception as apply_error:
        rollback_errors: list[str] = []
        for plan in reversed(attempted):
            try:
                atomic_write(plan.path, plan.before)
            except Exception as rollback_error:
                rollback_errors.append(f"{plan.path}: {rollback_error}")
        if rollback_errors:
            raise RuntimeError(
                f"apply failed ({apply_error}); rollback also failed: {rollback_errors}"
            ) from apply_error
        raise


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("assets", nargs="+", type=Path)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--backup-dir", type=Path)
    args = parser.parse_args()
    if args.apply and args.backup_dir is None:
        parser.error("--apply requires --backup-dir")

    plans = prepare_assets(args.assets)
    for plan in plans:
        print(f"{plan.path}: {plan.state} sha256={sha256(plan.before)} -> {sha256(plan.after)}")
    if args.apply and plans[0].state == "ready":
        apply_assets(plans, args.backup_dir)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
