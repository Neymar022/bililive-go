#!/usr/bin/env python3
"""以旧录制线程的最终摘要核对历史场次，拒绝仅凭下播或静默目录封口。"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shlex
import sqlite3
import stat
import subprocess
import tempfile
import time
import unicodedata
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.parse import urlsplit
from urllib.request import urlopen


MEDIA_TIMEZONE = timezone(timedelta(hours=8))


def verify_closure(log: Path, session: dict, live_url: str, input_count: int) -> dict:
    if session["end_reason"] != "normal" or session["end_time"] <= session["start_time"]:
        raise ValueError("historical session has no confirmed normal end")
    url = urlsplit(live_url)
    events: list[tuple[str, int, int]] = []
    digest = hashlib.sha256()
    with log.open("rb") as source:
        for number, raw in enumerate(source, 1):
            digest.update(raw)
            text = raw.decode("utf-8")
            if f"room={url.path}" not in text:
                continue
            fields = dict(token.split("=", 1) for token in shlex.split(text) if "=" in token)
            if fields.get("host") != url.netloc or fields.get("room") != url.path:
                continue
            timestamp = int(datetime.strptime(fields["time"], "%Y-%m-%d %H:%M:%S").replace(tzinfo=MEDIA_TIMEZONE).timestamp())
            if not session["start_time"] <= timestamp <= session["end_time"] + 5:
                continue
            message = fields.get("msg", "")
            if fields.get("level") in ("error", "fatal", "panic", "warning") and not message.startswith(("failed to load room info", "failed to get stream url, will retry")):
                raise ValueError(f"ambiguous recording closure at log line {number}")
            if message == "Record Start " + live_url:
                events.append(("start", number, timestamp))
            elif message == "Record End":
                events.append(("end", number, timestamp))
            elif message == f"推送录制摘要：{input_count} 个文件":
                events.append(("summary", number, timestamp))
            elif message == f"pipeline task enqueued: {input_count} files, 3 stages":
                events.append(("enqueue", number, timestamp))
            elif any(marker in message for marker in ("Record Start", "pipeline task enqueued", "录制摘要", "继承上一个", "failed to enqueue", "failed to end recorder", "panic")):
                raise ValueError(f"ambiguous recording closure at log line {number}")
    if [event[0] for event in events] != ["start", "enqueue", "end", "summary"]:
        raise ValueError("complete producer lifecycle and final recording summary required")
    if events[0][2] != session["start_time"] or events[2][2] != session["end_time"] or events[3][2] < events[2][2]:
        raise ValueError("recording lifecycle does not match persisted session")
    return {"log": str(log), "sha256": digest.hexdigest(), "lines": [event[1] for event in events], "registered_inputs": input_count}


def encoded(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def file_snapshot(path: Path, root: Path) -> dict:
    if path.resolve() != path.absolute() or not path.is_relative_to(root):
        raise ValueError(f"input is outside the verified root or traverses a symlink: {path}")
    with path.open("rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size <= 0:
            raise ValueError(f"input is not a readable nonempty regular file: {path}")
    return {"path": str(path), "device": info.st_dev, "inode": info.st_ino, "size": info.st_size, "mtime_ns": info.st_mtime_ns}


def verify_unprocessed(library: Path, source_root: Path, session_id: str, record: dict, initial: list, files: list) -> list[dict]:
    snapshots = [file_snapshot(Path(item["path"]), source_root) for item in files]
    if len({(item["device"], item["inode"]) for item in snapshots}) != len(files):
        raise ValueError("duplicate checkpoint media identity")
    inputs = {item["path"] for item in files}
    original = Path(initial[0]["path"])
    if not original.is_relative_to(source_root) or original.resolve() != original.absolute():
        raise ValueError("initial recording is outside the verified source root")
    allowed = inputs | {str(original)} | {item["source_path"] for item in files}
    start = datetime.fromisoformat(record["start_time"])
    end = record["verified_end_time"]
    for path in original.parent.iterdir():
        if path.suffix.lower() not in (".mp4", ".flv", ".mkv", ".ts") or str(path) in allowed:
            continue
        match = re.search(r" - (\d{4}-\d{2}-\d{2} \d{2}-\d{2}-\d{2}) - ", path.name)
        if match is None:
            raise ValueError(f"unproven recording input: {path}")
        at = datetime.strptime(match.group(1), "%Y-%m-%d %H-%M-%S").replace(tzinfo=MEDIA_TIMEZONE)
        if start <= at and at.timestamp() <= end:
            raise ValueError(f"unregistered recording input: {path}")
    for item in files:
        if Path(item["path"]).with_suffix(".subtitle.json").exists():
            raise ValueError("existing worker checkpoint requires separate recovery")
    session_hash = hashlib.sha256(session_id.encode()).hexdigest()[:32]
    work = library.parent / ".live_session_segments" / "processing" / session_hash
    manifest = library / ".knowledge_sessions" / (hashlib.sha256(("live-session:" + session_id).encode()).hexdigest() + ".json")
    if work.exists() or manifest.exists():
        raise ValueError("existing session publication checkpoint requires separate recovery")
    alias = " ".join(record["host_name"].split())
    aliases = {Path(item["path"]).name.split(" - ", 1)[0] for item in initial + files}
    # 本工具只采用别名一致且无需额外净化的合集；更名/特殊字符需独立映射。
    if {" ".join(value.split()) for value in aliases} != {alias} or not alias or alias != alias.strip(" .") or any(character in '<>:"/\\|?*' or unicodedata.category(character) in ("Cc", "Cf") for character in alias):
        raise ValueError("source alias requires an explicit verified library mapping")
    show = library / alias
    if show.resolve() != show.absolute() or not show.is_relative_to(library):
        raise ValueError("show must be an exact non-symlink directory inside the library")
    # 只读取该合集的归属元数据，不能忽略不可读 sidecar 后判定从未处理。
    if show.exists():
        def fail(error):
            raise error
        for directory, directories, names in os.walk(show, onerror=fail):
            for name in directories + names:
                if (Path(directory) / name).is_symlink():
                    raise ValueError("show contains an unverified symlink")
            for name in names:
                path = Path(directory) / name
                date = re.search(r" - (\d{4}-\d{2}-\d{2}) ", original.name).group(1)
                if path.suffix.lower() in (".mp4", ".mkv") and f".{date} - " in name:
                    raise ValueError("existing publication requires separate recovery")
                if name.endswith(".subtitle.json"):
                    data = json.loads(path.read_text(encoding="utf-8"))
                    if data.get("source_path") in inputs or str(data.get("record_meta", {}).get("live_session_id", "")) == session_id:
                        raise ValueError("existing worker result requires separate recovery")
    return snapshots


def build_plan(conn: sqlite3.Connection, session_id: str, log: Path, library: Path, source_root: Path) -> dict:
    row = conn.execute("SELECT * FROM legacy_lives.live_sessions WHERE id = ?", (session_id,)).fetchone()
    if row is None:
        raise ValueError("historical live session is missing")
    legacy = dict(row)
    room = conn.execute("SELECT url FROM legacy_lives.live_rooms WHERE live_id = ?", (legacy["live_id"],)).fetchone()
    if room is None:
        raise ValueError("historical live room is missing")
    rows = conn.execute("SELECT * FROM pipeline_tasks WHERE json_extract(record_info_json, '$.live_session_id') = ?", (session_id,)).fetchall()
    if len(rows) != 1:
        raise ValueError("only a single-producer, single-task historical session is eligible")
    before = dict(rows[0])
    record = json.loads(before["record_info_json"])
    if record.get("recording_producer_id") or record.get("live_id") != legacy["live_id"]:
        raise ValueError("task recording identity conflicts with historical session")
    table = conn.execute("SELECT name FROM sqlite_master WHERE name = 'pipeline_recording_sessions'").fetchone()
    if table and conn.execute("SELECT 1 FROM pipeline_recording_sessions WHERE id = ?", (session_id,)).fetchone():
        raise ValueError("recording session is already registered")
    if before["status"] != "failed" or not before["can_retry"] or before["current_stage"] != 2 or before["total_stages"] != 3:
        raise ValueError("only retryable failures before subtitle worker are eligible")
    stages = json.loads(before["pipeline_config_json"])["stages"]
    if [stage.get("name") for stage in stages] != ["fix_flv", "convert_mp4", "subtitle_generate"] or any(stage.get("enabled") is False or stage.get("parallel") for stage in stages):
        raise ValueError("unsupported historical stage contract")
    initial = json.loads(before["initial_files_json"])
    files = json.loads(before["current_files_json"])
    results = json.loads(before["stage_results_json"])
    if len(initial) != 1 or len(results) != 3 or not files:
        raise ValueError("incomplete historical stage input set")
    previous = initial
    for index, result in enumerate(results):
        if result.get("stage_index") != index or result.get("stage_name") != stages[index]["name"] or result.get("input_files") != previous:
            raise ValueError("historical stage input chain is incomplete")
        if index < 2:
            output = result.get("output_files", [])
            if result.get("status") != "completed" or not output or any(item.get("source_path") not in {file["path"] for file in previous} for item in output):
                raise ValueError("historical conversion checkpoint is incomplete")
            if {item.get("source_path") for item in output} != {item["path"] for item in previous} or (index == 1 and len(output) != len(previous)):
                raise ValueError("historical conversion lost or duplicated a segment")
            previous = output
        elif result.get("status") != "failed" or result.get("output_files") or not result.get("error_message", "").startswith("subtitle_generate: failed to ensure library hardlink: EnsureLibraryHardlink:"):
            raise ValueError("worker may already have executed; separate checkpoint recovery required")
    if previous != files or any(item.get("type") != "video" or Path(item["path"]).suffix.lower() != ".mp4" for item in files):
        raise ValueError("current converted files do not match the completed checkpoint")
    started = datetime.fromtimestamp(legacy["start_time"], MEDIA_TIMEZONE)
    original = Path(initial[0]["path"])
    if initial[0].get("type") != "video" or original.suffix.lower() != ".flv" or f" - {started:%Y-%m-%d %H-%M-%S} - " not in original.name:
        raise ValueError("initial recording does not prove the session start")
    proof = verify_closure(log, legacy, room["url"], len(initial))
    media = verify_unprocessed(library, source_root, session_id, dict(record, start_time=started.isoformat(), verified_end_time=legacy["end_time"]), initial, files)
    producer = f"historical-{session_id}-{before['id']}"
    updated_record = dict(record, recording_producer_id=producer, start_time=started.isoformat())
    after = dict(before, record_info_json=encoded(updated_record))
    session = {"id": session_id, "live_id": record["live_id"], "end_reason": "normal", "producers": {producer: True}, "tasks": {str(before["id"]): {"ready": False, "sources": []}}, "ready": False}
    plan = {"version": 1, "before_task": before, "after_task": after, "legacy_session": legacy, "live_url": room["url"], "session": session, "proof": proof, "media": media, "library": str(library), "source_root": str(source_root)}
    plan["fingerprint"] = hashlib.sha256(encoded(plan).encode()).hexdigest()
    return plan


def write_manifest(backup: Path, manifest: dict, create: bool = False) -> None:
    path = backup / "manifest.json"
    content = (encoded(manifest) + "\n").encode()
    if create:
        backup.mkdir(mode=0o700, parents=True, exist_ok=False)
        with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "wb") as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
    else:
        with tempfile.NamedTemporaryFile(dir=backup, delete=False) as output:
            temporary = Path(output.name)
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        try:
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)
    descriptor = os.open(backup, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def check_media(plan: dict) -> None:
    if [file_snapshot(Path(item["path"]), Path(plan["source_root"])) for item in plan["media"]] != plan["media"]:
        raise ValueError("media changed since the fixed point")


def reject_running_tasks(conn: sqlite3.Connection) -> None:
    if conn.execute("SELECT 1 FROM pipeline_tasks WHERE status = 'running' LIMIT 1").fetchone():
        raise ValueError("pipeline is running; maintenance gate is closed")


def apply_plan(conn: sqlite3.Connection, plan: dict, backup: Path) -> None:
    conn.execute("BEGIN IMMEDIATE")
    try:
        reject_running_tasks(conn)
        fresh = build_plan(conn, plan["session"]["id"], Path(plan["proof"]["log"]), Path(plan["library"]), Path(plan["source_root"]))
        if fresh != plan:
            raise ValueError("fixed point changed; adoption refused")
        if conn.execute("SELECT name FROM sqlite_master WHERE name = 'pipeline_recording_sessions'").fetchone() is None:
            raise ValueError("deploy the recording-session schema before adoption")
        manifest = {"state": "prepared", "plan": fresh}
        write_manifest(backup, manifest, create=True)
        session = plan["session"]
        # 历史采用不能抢占按 rowid 查询的当前场次；新录制仍自然追加到末尾。
        rowid = conn.execute("SELECT MIN(COALESCE(MIN(rowid), 0), 0) - 1 FROM pipeline_recording_sessions").fetchone()[0]
        conn.execute("INSERT INTO pipeline_recording_sessions(rowid, id, live_id, state_json) VALUES(?, ?, ?, ?)", (rowid, session["id"], session["live_id"], encoded(session)))
        conn.execute("UPDATE pipeline_tasks SET record_info_json = ? WHERE id = ?", (plan["after_task"]["record_info_json"], plan["before_task"]["id"]))
        check_media(plan)
        conn.commit()
    except BaseException:
        conn.rollback()
        raise
    manifest["state"] = "applied"
    write_manifest(backup, manifest)


def rollback(conn: sqlite3.Connection, backup: Path) -> None:
    manifest = json.loads((backup / "manifest.json").read_text(encoding="utf-8"))
    plan = manifest["plan"]
    conn.execute("BEGIN IMMEDIATE")
    try:
        reject_running_tasks(conn)
        rows = conn.execute("SELECT * FROM pipeline_tasks WHERE json_extract(record_info_json, '$.live_session_id') = ?", (plan["session"]["id"],)).fetchall()
        state = conn.execute("SELECT live_id, state_json FROM pipeline_recording_sessions WHERE id = ?", (plan["session"]["id"],)).fetchone()
        if len(rows) != 1 or dict(rows[0]) != plan["after_task"] or state is None or state["live_id"] != plan["session"]["live_id"] or json.loads(state["state_json"]) != plan["session"]:
            raise ValueError("adopted checkpoint changed; rollback refused")
        check_media(plan)
        conn.execute("UPDATE pipeline_tasks SET record_info_json = ? WHERE id = ?", (plan["before_task"]["record_info_json"], plan["before_task"]["id"]))
        conn.execute("DELETE FROM pipeline_recording_sessions WHERE id = ?", (plan["session"]["id"],))
        conn.commit()
    except BaseException:
        conn.rollback()
        raise
    manifest["state"] = "rolled_back"
    write_manifest(backup, manifest)


def maintenance_gate(api: str, roots: list[Path]) -> None:
    if not api:
        raise ValueError("writes require a fresh --api maintenance gate")
    with urlopen(api.rstrip("/") + "/api/update/status", timeout=8) as response:
        update = json.load(response)
    if update.get("state") != "idle" or update.get("active_recordings_count") != 0 or update.get("graceful_update_pending") is not False:
        raise ValueError("recording or update gate is busy or unknown")
    with urlopen(api.rstrip("/") + "/api/pipeline/tasks/stats", timeout=8) as response:
        queue = json.load(response)
    if queue.get("running_count") != 0:
        raise ValueError("pipeline running count is busy or unknown")
    commands = subprocess.run(["ps", "-eo", "comm="], check=True, capture_output=True, text=True, timeout=8).stdout.splitlines()
    if any(Path(command.strip()).name == "ffmpeg" for command in commands):
        raise ValueError("ffmpeg is still running")
    threshold = time.time() - 300
    def fail(error):
        raise error
    for root in roots:
        if not root.exists():
            continue
        for directory, _, names in os.walk(root, onerror=fail):
            for name in names:
                path = Path(directory) / name
                if path.suffix.lower() in (".mp4", ".mkv", ".flv", ".ts", ".nfo", ".json", ".srt", ".ass") and path.stat().st_mtime >= threshold:
                    raise ValueError(f"recent media or metadata write: {path}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pipeline-db", required=True, type=Path)
    parser.add_argument("--lives-db", required=True, type=Path)
    parser.add_argument("--library", required=True, type=Path)
    parser.add_argument("--source-root", required=True, type=Path)
    parser.add_argument("--session", required=True)
    parser.add_argument("--log", required=True, type=Path)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--apply", action="store_true")
    mode.add_argument("--rollback", action="store_true")
    parser.add_argument("--expect", default="")
    parser.add_argument("--backup-dir", type=Path)
    parser.add_argument("--api", default="")
    args = parser.parse_args()
    writing = args.apply or args.rollback
    library, source_root = args.library.resolve(strict=True), args.source_root.resolve(strict=True)
    conn = sqlite3.connect(args.pipeline_db.resolve(strict=True).as_uri() + ("?mode=rw" if writing else "?mode=ro"), uri=True, timeout=5)
    conn.row_factory = sqlite3.Row
    try:
        conn.execute("ATTACH DATABASE ? AS legacy_lives", (args.lives_db.resolve(strict=True).as_uri() + "?mode=ro",))
        if writing:
            if args.backup_dir is None:
                parser.error("writes require --backup-dir")
            maintenance_gate(args.api, [library, source_root, library.parent / ".live_session_segments"])
        if args.rollback:
            manifest = json.loads((args.backup_dir / "manifest.json").read_text(encoding="utf-8"))
            if manifest["plan"]["session"]["id"] != args.session or manifest["plan"]["library"] != str(library) or manifest["plan"]["source_root"] != str(source_root):
                raise ValueError("rollback target does not match supplied scope")
            rollback(conn, args.backup_dir)
            print("historical session adoption rolled back; media unchanged")
            return
        plan = build_plan(conn, args.session, args.log.resolve(strict=True), library, source_root)
        if args.apply:
            if args.expect != plan["fingerprint"]:
                raise ValueError("apply requires the current dry-run --expect fingerprint")
            apply_plan(conn, plan, args.backup_dir)
        print(encoded({"state": "adopted-not-retried" if args.apply else "dry-run", "fingerprint": plan["fingerprint"], "session_id": args.session, "task_id": plan["before_task"]["id"], "media_count": len(plan["media"]), "media_bytes": sum(item["size"] for item in plan["media"]), "proof_lines": plan["proof"]["lines"], "recording_start": json.loads(plan["after_task"]["record_info_json"])["start_time"]}))
    finally:
        conn.close()


if __name__ == "__main__":
    main()
