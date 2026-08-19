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
    "recent": {
        "vendor": '(0,r.TI)(i)?`${(0,r.WP)(s)}`:a?',
        "final": '(0,r.TI)(i)?a||`${(0,r.WP)(s)}`:a?',
    },
    "card-title": {
        "vendor": 't===g.OY?(0,r.WP)(a):i?',
        "final": 't===g.OY?i||(0,r.WP)(a):i?',
    },
    "serial": {
        "vendor": (
            'e.isUnRecognizedEpisode(i.episode)?'
            'e.UNRECOGNIZED_EPISODE_TEXT:i.episode'
        ),
        "deployed-title-v1": (
            'e.isUnRecognizedEpisode(i.episode)?"":i.episode'
        ),
        "final": (
            'e.isUnRecognizedEpisode(i.episode)?'
            '(e.getEpisodeTitle(i).match(/\\d{4}-\\d{2}-\\d{2}/)||'
            '[e.UNRECOGNIZED_EPISODE_TEXT])[0].replace(/^\\d{4}-/,""):'
            'i.episode'
        ),
    },
    "card-click": {
        "vendor": 'className:"card-list"},scopedSlots:',
        "final": (
            'className:"card-list"},on:{select:e.handleChangeEpisode},'
            'scopedSlots:'
        ),
    },
    "card-cursor": {
        "vendor": 't("div",{staticClass:"card-item"},[',
        "final": (
            't("div",{staticClass:"card-item",'
            'staticStyle:{cursor:"pointer"}},['
        ),
    },
    "card-scroll": {
        "vendor": (
            'scrollToActiveTab(){const e=this.$refs.scrollContainerRef,'
            't=this.$refs.cardItemRefs;if(!e||!t)return;const i=t[this.activeIndex];'
            'if(!i)return;const a=i.offsetLeft,s=e.clientWidth,'
            'o=a+i.offsetWidth/2-s/2,n=e.scrollWidth-s,'
            'l=Math.max(0,Math.min(o,n));e.scrollLeft=l}'
        ),
        "final": (
            'scrollToActiveTab(){const e=this.$refs.scrollContainerRef,'
            't=this.$refs.cardItemRefs;if(!e||!t)return;const i=t[this.activeIndex];'
            'if(!i)return;const a=i.offsetLeft,s=e.clientWidth;'
            'if("card-list"===this.className){const i=this.cardOffset,'
            'a=Number(getComputedStyle(t[0]).marginRight.replace("px","")||0),'
            'o=Math.max(1,Math.floor((s+a)/i)),'
            'n=Math.max(0,Math.min(this.activeIndex-Math.floor((o-1)/2),'
            't.length-o)),l=t[n].offsetLeft,r=t[t.length-1],'
            'c=r.offsetLeft+r.offsetWidth;return e.style.paddingRight='
            'Math.max(0,l+s-c)+"px",void(e.scrollLeft=l)}'
            'const o=a+i.offsetWidth/2-s/2,n=e.scrollWidth-s,'
            'l=Math.max(0,Math.min(o,n));e.scrollLeft=l}'
        ),
    },
}

KNOWN_BUNDLE_STATES = {
    "vendor": {
        "recent": "vendor",
        "card-title": "vendor",
        "serial": "vendor",
        "card-click": "vendor",
        "card-cursor": "vendor",
        "card-scroll": "vendor",
    },
    "deployed-title-v1": {
        "recent": "final",
        "card-title": "final",
        "serial": "deployed-title-v1",
        "card-click": "vendor",
        "card-cursor": "vendor",
        "card-scroll": "vendor",
    },
    "final": {
        "recent": "final",
        "card-title": "final",
        "serial": "final",
        "card-click": "final",
        "card-cursor": "final",
        "card-scroll": "final",
    },
}


def detect_bundle_state(source: str) -> str:
    matches: list[str] = []
    diagnostics = {
        name: {
            variant: source.count(expression)
            for variant, expression in variants.items()
        }
        for name, variants in PATCHES.items()
    }
    for state, variants in KNOWN_BUNDLE_STATES.items():
        if all(
            diagnostics[name][variant] == 1
            and sum(diagnostics[name].values()) == 1
            for name, variant in variants.items()
        ):
            matches.append(state)

    if len(matches) != 1:
        raise RuntimeError(
            "expected exactly one known bundle state, "
            f"matched={matches}, counts={diagnostics}"
        )
    return matches[0]


def patch_javascript(source: str) -> tuple[str, str]:
    state = detect_bundle_state(source)
    if state == "final":
        return source, state

    for name, variant in KNOWN_BUNDLE_STATES[state].items():
        current = PATCHES[name][variant]
        final = PATCHES[name]["final"]
        if current != final:
            source = source.replace(current, final, 1)
    return source, state


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
        patched, bundle_state = patch_javascript(source)
        before = asset.read_bytes()
        after = encode_asset(patched, compressed)
        state = "already-patched" if bundle_state == "final" else f"ready:{bundle_state}"
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
    if args.apply and plans[0].state.startswith("ready:"):
        apply_assets(plans, args.backup_dir)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
