#!/usr/bin/python3
"""固定运行映像取证；仅由同一 oneshot 的 root ExecStartPre 执行，无参数入口。"""
import collections
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys
import time

PROOF = Path('/run/bililive-platform-observer/platform.json')
MAX_BUILD_BYTES = 512 * 1024 * 1024
MAX_PROOF_BYTES = 16384
STAT_KEYS = ('dev', 'ino', 'mode', 'uid', 'gid', 'size', 'mtime_ns', 'ctime_ns')
PROC = Path('/proc')
STARTUP = '/var/targets/video_serv'
BUILD = '/volume1/@appstore/com.ugreen.videomgr/sbin/video_serv'


def _process_identity():
    found = []
    for entry in PROC.iterdir():
        if not entry.name.isdecimal():
            continue
        try:
            if (entry / 'comm').read_text().rstrip('\n') != 'video_serv':
                continue
            # 严格只读 arg0，永不读取或记录其它参数。
            with (entry / 'cmdline').open('rb', buffering=0) as stream:
                arg0 = bytearray()
                while len(arg0) <= 4096:
                    char = stream.read(1)
                    if char == b'\0':
                        break
                    if not char:
                        raise ValueError('process_arg0_unconfirmed')
                    arg0.extend(char)
                else:
                    raise ValueError('process_arg0_unconfirmed')
            if bytes(arg0) != STARTUP.encode():
                raise ValueError('process_startup_path_changed')
            raw = (entry / 'stat').read_text()
            fields = raw[raw.rfind(')')+2:].split()
            start_ticks = int(fields[19])
            if start_ticks <= 0:
                raise ValueError('process_start_unknown')
            found.append((int(entry.name), entry.stat().st_uid, start_ticks))
        except FileNotFoundError:
            # 退出中的实例；最终会重新核对完整进程集合。
            continue
    if len(found) != 1:
        raise ValueError('process_not_unique')
    boot = [line.split()[1] for line in (PROC / 'stat').read_text().splitlines()
            if line.startswith('btime ')]
    if len(boot) != 1:
        raise ValueError('boot_time_unknown')
    uptime = float((PROC / 'uptime').read_text().split()[0])
    boot_seconds = int(boot[0])
    if abs(time.time() - uptime - boot_seconds) > 5:
        raise ValueError('boot_clock_unconfirmed')
    pid, uid, ticks = found[0]
    # btime 与 start ticks 仅生成实例启动时间，不据此推断磁盘文件已被执行。
    started_ns = boot_seconds * 1000000000 + ticks * 1000000000 // os.sysconf('SC_CLK_TCK')
    if started_ns > time.time_ns() or started_ns <= 0:
        raise ValueError('process_start_unknown')
    return pid, uid, ticks, started_ns



def _stat_identity(info):
    return tuple(getattr(info, 'st_' + key) for key in STAT_KEYS)


def _trusted_installation_stat(info):
    if info.st_uid != 0:
        raise ValueError('installation_owner_untrusted')
    if stat.S_ISLNK(info.st_mode):
        return
    # root helper 不把自身 root 身份当作服务写权限；collector 仍按实际组检查。
    groups = set(os.getgroups()) | {os.getegid()}
    if info.st_mode & 0o002 or (info.st_mode & 0o020 and
            (info.st_gid != 0 or (os.geteuid() != 0 and info.st_gid in groups))):
        raise ValueError('installation_service_writable')


def _installation_identity():
    if os.path.realpath(STARTUP) != BUILD or os.path.realpath(BUILD) != BUILD:
        raise ValueError('installation_target_changed')
    pending = collections.deque(Path(STARTUP).parts[1:])
    resolved = Path('/')
    _trusted_installation_stat(resolved.lstat())
    entries = {}
    hops = 0
    while pending:
        part = pending.popleft()
        if part == '..':
            resolved = resolved.parent
            continue
        if part == '.':
            continue
        path = resolved / part
        info = path.lstat()
        _trusted_installation_stat(info)
        target = None
        if stat.S_ISLNK(info.st_mode):
            hops += 1
            if hops > 40:
                raise ValueError('installation_link_loop')
            target = os.readlink(path)
            parts = Path(target).parts
            if os.path.isabs(target):
                resolved = Path('/')
                parts = parts[1:]
            pending.extendleft(reversed(parts))
        else:
            if pending and not stat.S_ISDIR(info.st_mode):
                raise ValueError('installation_parent_invalid')
            resolved = path
        entries[str(path)] = (_stat_identity(info), target)
    if str(resolved) != BUILD:
        raise ValueError('installation_target_changed')
    info = resolved.lstat()
    if not stat.S_ISREG(info.st_mode) or not info.st_mode & 0o111 or not 0 < info.st_size <= MAX_BUILD_BYTES:
        raise ValueError('installation_build_invalid')
    return entries


def _now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def unconfirmed():
    return {'schema_version': 1, 'state': 'platform_unconfirmed',
            'observed_at': _now(), 'reason': 'running_build_unconfirmed'}


def capture_platform():
    try:
        process = _process_identity()
        before = _installation_identity()
        # PID 仅来自固定 comm/arg0 的唯一进程；实际 exe fd 才是哈希来源。
        exe = os.open(PROC / str(process[0]) / 'exe', os.O_RDONLY | os.O_CLOEXEC)
        try:
            running = _stat_identity(os.fstat(exe))
            if running != before[BUILD][0]:
                raise ValueError('running_build_mismatch')
            fd = os.open(BUILD, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
            try:
                if _stat_identity(os.fstat(fd)) != running:
                    raise ValueError('running_build_mismatch')
                digest = hashlib.sha256()
                total = 0
                while True:
                    chunk = os.read(exe, 1024 * 1024)
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > MAX_BUILD_BYTES:
                        raise ValueError('build_too_large')
                    digest.update(chunk)
                if total != running[5] or _stat_identity(os.fstat(exe)) != running or _stat_identity(os.fstat(fd)) != running:
                    raise ValueError('build_changed')
                if _installation_identity() != before or _process_identity() != process:
                    raise ValueError('platform_changed_during_capture')
                # 再开当前实例的 exe，拒绝同 PID exec 到另一个映像。
                current = os.open(PROC / str(process[0]) / 'exe', os.O_RDONLY | os.O_CLOEXEC)
                try:
                    if _stat_identity(os.fstat(current)) != running:
                        raise ValueError('running_build_changed')
                finally:
                    os.close(current)
            finally:
                os.close(fd)
        finally:
            os.close(exe)
        pid, uid, ticks, started_ns = process
        return {'schema_version': 1, 'state': 'confirmed', 'pid': pid, 'uid': uid,
                'start_ticks': ticks, 'started_at': dt.datetime.fromtimestamp(started_ns/1000000000, dt.timezone.utc).isoformat(),
                'observed_at': _now(), 'startup_path': STARTUP, 'build_path': BUILD,
                'build_sha256': digest.hexdigest(), 'build_stat': dict(zip(STAT_KEYS, running)),
                'exe_stat': dict(zip(STAT_KEYS, running))}
    except (OSError, ValueError, IndexError, OverflowError):
        return unconfirmed()


def _proof_directory(create=False):
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        root = os.fstat(fd)
        if root.st_uid != 0 or root.st_mode & 0o022:
            raise ValueError('unsafe_proof_parent')
        for index, part in enumerate(PROOF.parent.parts[1:]):
            created = False
            if create and index == len(PROOF.parent.parts) - 2:
                try:
                    os.mkdir(part, 0o700, dir_fd=fd)
                    created = True
                    os.fsync(fd)
                except FileExistsError:
                    pass
            info = os.stat(part, dir_fd=fd, follow_symlinks=False)
            if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
                raise ValueError('unsafe_proof_parent')
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            try:
                if _stat_identity(os.fstat(child)) != _stat_identity(info):
                    raise ValueError('proof_parent_changed')
                if created:
                    os.fchmod(child, 0o755)
                    os.fsync(child)
            except BaseException:
                os.close(child)
                raise
            os.close(fd)
            fd = child
        if stat.S_IMODE(os.fstat(fd).st_mode) != 0o755:
            raise ValueError('unsafe_proof_directory')
        return fd
    except BaseException:
        os.close(fd)
        raise


def _check_proof_file(info):
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o644 or info.st_nlink != 1:
        raise ValueError('unsafe_proof_file')


def publish_proof(data):
    if os.geteuid() != 0:
        raise ValueError('root_required')
    directory = _proof_directory(create=True)
    name = '.platform-' + os.urandom(12).hex()
    try:
        try:
            _check_proof_file(os.stat(PROOF.name, dir_fd=directory, follow_symlinks=False))
        except FileNotFoundError:
            pass
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=directory)
        try:
            os.fchmod(fd, 0o644)
            raw = (json.dumps(data, separators=(',', ':')) + '\n').encode()
            if len(raw) > MAX_PROOF_BYTES:
                raise ValueError('proof_too_large')
            with os.fdopen(fd, 'wb', closefd=False) as stream:
                stream.write(raw)
                stream.flush()
                os.fsync(fd)
            info = os.fstat(fd)
            _check_proof_file(info)
            if _stat_identity(info) != _stat_identity(os.stat(name, dir_fd=directory, follow_symlinks=False)):
                raise ValueError('proof_file_changed')
            os.replace(name, PROOF.name, src_dir_fd=directory, dst_dir_fd=directory)
            # rename 会改变文件 ctime；打开 fd 与新目录项必须仍为同一 inode。
            if _stat_identity(os.fstat(fd)) != _stat_identity(os.stat(PROOF.name, dir_fd=directory, follow_symlinks=False)):
                raise ValueError('proof_file_changed')
            os.fsync(directory)
        finally:
            os.close(fd)
    finally:
        try:
            os.unlink(name, dir_fd=directory)
        except FileNotFoundError:
            pass
        os.close(directory)


def _unique_object(pairs):
    data = {}
    for key, value in pairs:
        if key in data:
            raise ValueError('duplicate_proof_key')
        data[key] = value
    return data


def read_proof():
    directory = _proof_directory()
    try:
        info = os.stat(PROOF.name, dir_fd=directory, follow_symlinks=False)
        _check_proof_file(info)
        if info.st_size > MAX_PROOF_BYTES:
            raise ValueError('proof_too_large')
        fd = os.open(PROOF.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=directory)
        try:
            if _stat_identity(os.fstat(fd)) != _stat_identity(info):
                raise ValueError('proof_file_changed')
            with os.fdopen(fd, 'rb', closefd=False) as stream:
                raw = stream.read(MAX_PROOF_BYTES + 1)
            if len(raw) > MAX_PROOF_BYTES or _stat_identity(os.fstat(fd)) != _stat_identity(info) or _stat_identity(os.stat(PROOF.name, dir_fd=directory, follow_symlinks=False)) != _stat_identity(info):
                raise ValueError('proof_file_changed')
        finally:
            os.close(fd)
    finally:
        os.close(directory)
    return json.loads(raw, object_pairs_hook=_unique_object)


def validate_proof(data):
    fields = {'schema_version', 'state', 'pid', 'uid', 'start_ticks', 'started_at', 'observed_at',
              'startup_path', 'build_path', 'build_sha256', 'build_stat', 'exe_stat'}
    if not isinstance(data, dict) or set(data) != fields or type(data['schema_version']) is not int or data['schema_version'] != 1 or data['state'] != 'confirmed':
        raise ValueError('proof_schema_invalid')
    for key in ('pid', 'uid', 'start_ticks'):
        if type(data[key]) is not int or not (0 if key == 'uid' else 1) <= data[key] <= 2**63-1:
            raise ValueError('proof_process_invalid')
    for key in ('observed_at', 'started_at'):
        stamp = dt.datetime.fromisoformat(data[key].replace('Z', '+00:00'))
        if stamp.tzinfo is None or stamp.utcoffset() != dt.timedelta(0):
            raise ValueError('proof_time_invalid')
        age = (dt.datetime.now(dt.timezone.utc) - stamp).total_seconds()
        if age < -5 or (key == 'observed_at' and age > 60):
            raise ValueError('proof_time_invalid')
    if dt.datetime.fromisoformat(data['started_at']) > dt.datetime.fromisoformat(data['observed_at']):
        raise ValueError('proof_time_invalid')
    if data['startup_path'] != STARTUP or data['build_path'] != BUILD or not isinstance(data['build_sha256'], str) or not re.fullmatch('[0-9a-f]{64}', data['build_sha256']):
        raise ValueError('proof_build_invalid')
    for key in ('build_stat', 'exe_stat'):
        item = data[key]
        if not isinstance(item, dict) or set(item) != set(STAT_KEYS) or any(type(item[k]) is not int or item[k] < 0 or item[k] > 2**64-1 for k in STAT_KEYS):
            raise ValueError('proof_stat_invalid')
        if not stat.S_ISREG(item['mode']) or not item['mode'] & 0o111 or not 0 < item['size'] <= MAX_BUILD_BYTES or item['uid'] != 0 or item['ino'] == 0:
            raise ValueError('proof_stat_invalid')
    if data['build_stat'] != data['exe_stat']:
        raise ValueError('proof_stat_mismatch')
    return data


def consume_platform():
    try:
        data = validate_proof(read_proof())
        process = _process_identity()
        before = _installation_identity()
        stamp = dt.datetime.fromtimestamp(process[3]/1000000000, dt.timezone.utc).isoformat()
        if (data['pid'], data['uid'], data['start_ticks'], data['started_at']) != (*process[:3], stamp):
            raise ValueError('proof_instance_changed')
        expected = tuple(data['build_stat'][k] for k in STAT_KEYS)
        if before[BUILD][0] != expected:
            raise ValueError('proof_build_changed')
        fd = os.open(BUILD, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
        try:
            if _stat_identity(os.fstat(fd)) != expected or _installation_identity() != before or _process_identity() != process:
                raise ValueError('platform_changed_during_consume')
        finally:
            os.close(fd)
        return {key: value for key, value in data.items() if key not in ('schema_version', 'build_stat', 'exe_stat')}
    except (OSError, ValueError, TypeError, KeyError, IndexError, OverflowError):
        return {'state': 'platform_unconfirmed', 'reason': 'running_build_unconfirmed'}


def main():
    if len(sys.argv) != 1 or os.geteuid() != 0:
        raise ValueError('fixed_root_entry_only')
    publish_proof(capture_platform())


if __name__ == '__main__':
    main()
