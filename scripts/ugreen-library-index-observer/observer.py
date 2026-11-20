#!/usr/bin/python3
"""NAS 本机固定只读查询；只发布完整成功快照，不接收应用提供的 SQL 或路径。"""
import datetime as dt
import fcntl
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile

DESTINATION = Path('/var/lib/bililive-index-observer')
QUERY = Path('/usr/local/lib/bililive-index-observer/query.sql')
MAX_BYTES = 16 * 1024 * 1024


# root helper 的固定证据由 postgres 消费；查询前后仍绑定当前实例和磁盘文件。
from platform_probe import consume_platform as observe_platform


def same_platform_instance(before, after):
    return (before.get('state') == after.get('state') == 'confirmed' and
            all(before.get(key) == after.get(key) for key in
                ('pid', 'uid', 'start_ticks', 'started_at', 'startup_path', 'build_path', 'build_sha256')))


def configured_library_root():
    root = os.environ.get('BILILIVE_INDEX_LIBRARY_ROOT', '')
    if (not root.startswith('/') or root.startswith('//') or root == '/' or
            os.path.normpath(root) != root or any(ord(c) < 32 or ord(c) == 127 for c in root)):
        raise ValueError('library_root_configuration_invalid')
    return root


def validate_snapshot(raw, now, library_root):
    if len(raw) > MAX_BYTES:
        raise ValueError('snapshot_too_large')
    data = json.loads(raw)
    if data.get('schema_version') != 2 or data.get('adapter_version') != 'ugreen-fixed-select-v2':
        raise ValueError('schema_mismatch')
    platform = data.get('platform')
    if not isinstance(platform, dict) or platform.get('state') not in ('confirmed', 'platform_unconfirmed'):
        raise ValueError('platform_evidence_missing')
    if data.get('library_root') != library_root:
        raise ValueError('library_root_mismatch')
    captured = dt.datetime.fromisoformat(data['generated_at'].replace('Z', '+00:00'))
    if captured.tzinfo is None or not -5 <= (now - captured).total_seconds() <= 60:
        raise ValueError('snapshot_time_invalid')
    libraries = data.get('libraries')
    if not isinstance(libraries, list) or len(libraries) != 1 or libraries[0].get('id') != 4:
        raise ValueError('library_missing_or_duplicate')
    if libraries[0].get('roots') != [library_root]:
        raise ValueError('library_roots_changed')
    for key in ('read_local_info_first', 'nfo_info_save_to_local'):
        if type(libraries[0].get(key)) is not bool:
            raise ValueError('library_setting_unknown')
    files = data.get('files')
    if not isinstance(files, list) or len(files) > 100000:
        raise ValueError('file_list_invalid')
    # 重复映射保留给消费者判冲突，不通过字典去重掩盖。
    for item in files:
        path = item.get('path')
        if not isinstance(path, str) or not path.startswith(library_root + '/') or '/..' in path or '\x00' in path:
            raise ValueError('file_path_invalid')
    return data


def publish(destination, data):
    current = destination.lstat()
    if not stat.S_ISDIR(current.st_mode) or current.st_uid != os.geteuid() or current.st_mode & 0o022:
        raise ValueError('unsafe_snapshot_directory')
    fd, name = tempfile.mkstemp(prefix='.snapshot-', dir=destination)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(json.dumps(data, ensure_ascii=False, separators=(',', ':')).encode() + b'\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, destination / 'snapshot.json')
        directory_fd = os.open(destination, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def main():
    library_root = configured_library_root()
    # 安装目录和查询必须由 root 持有且不能被服务用户改写。
    for path in (QUERY.parent, QUERY):
        entry = path.lstat()
        if stat.S_ISLNK(entry.st_mode) or entry.st_uid != 0 or entry.st_mode & 0o022:
            raise ValueError('unsafe_query_installation')
    query = QUERY.read_text()
    # 环境不继承 PGOPTIONS、PGSERVICE 或调用方 PATH；无密码文件及网络登录。
    environment = {'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8', 'PGPASSFILE': '/dev/null'}
    lock = os.open(DESTINATION / 'observer.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        platform_before = observe_platform()
        result = subprocess.run(
            ['/usr/bin/psql', '-X', '-qAt', '--no-password', '-h', '/var/run/postgresql',
             '-p', '5433', '-U', 'postgres', '-d', 'video', '-v', 'ON_ERROR_STOP=1', '-v', 'library_root=' + library_root],
            input=query, text=True, capture_output=True, timeout=25,
            env=environment, cwd='/', check=True,
        )
        data = json.loads(result.stdout)
        platform_after = observe_platform()
        data['platform'] = (platform_after if same_platform_instance(platform_before, platform_after)
                            else {'state': 'platform_unconfirmed', 'reason': 'running_build_changed_or_unknown'})
        data = validate_snapshot(json.dumps(data).encode(), dt.datetime.now(dt.timezone.utc), library_root)
        publish(DESTINATION, data)
        print(json.dumps({'state': 'snapshot_written', 'generated_at': data['generated_at'], 'files': len(data['files'])}))
    finally:
        os.close(lock)


if __name__ == '__main__':
    main()
