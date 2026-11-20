"""可选 v1 完成证明：只在可信共享根内持锁计算、无覆盖提交和回放。"""
from __future__ import annotations

from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import json
import os
from pathlib import Path
import re
import stat
import tempfile
from typing import Any, Callable, Literal
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field


class CompletionError(Exception):
    """只携带固定错误码，不携带路径、请求、字幕或底层异常正文。"""
    def __init__(self, code: str, status: int = 409):
        super().__init__(code)
        self.code = code
        self.status = status


class CompletionOwner(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    task_id: int = Field(gt=0)
    session_id: str = Field(min_length=1)
    producer_id: str = Field(min_length=1)
    input_index: int = Field(ge=0)


class CompletionRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    schema_version: Literal[1] = Field(alias='schema')
    request_id: str
    owner: CompletionOwner
    source_path: str
    formal_output_path: str
    attempt_directory: str
    output_video_path: str
    output_srt_path: str
    output_ass_path: str
    record_meta: dict[str, Any]
    request_snapshot: dict[str, Any]


# 只保存实际传给 core 的业务配置；secret、URL 和整环境不进入证明。
EXECUTION_FIELDS = (
    'source_path', 'output_video_path', 'output_srt_path', 'provider', 'language',
    'burn_style', 'provider_chain', 'ffmpeg_bin', 'dashscope_model', 'local_model',
    'local_compute_type', 'allow_cloud_asr', 'mac_transcriber_model',
    'mac_transcriber_timeout_seconds', 'video_encoder', 'vaapi_device', 'vaapi_qp',
    'burn_chain', 'mac_burn_codec', 'mac_burn_bitrate', 'mac_burn_timeout_seconds',
)
_SECRET_KEY = re.compile(r'(^|_)(token|tokens|key|keys|secret|secrets|password|authorization|cookie)($|_)', re.I)


class _CoreFailure(Exception):
    def __init__(self, original: Exception):
        self.original = original


def _same(left: Any, right: Any) -> bool:
    return json.dumps(left, sort_keys=True, allow_nan=False) == json.dumps(right, sort_keys=True, allow_nan=False)


def execution_snapshot(options: dict[str, Any]) -> dict[str, Any]:
    return {name: options[name] for name in EXECUTION_FIELDS}


def _no_secret_fields(value: Any) -> None:
    if isinstance(value, dict):
        for key, child in value.items():
            if not isinstance(key, str) or _SECRET_KEY.search(key):
                raise CompletionError('completion_secret_field', 400)
            _no_secret_fields(child)
    elif isinstance(value, list):
        for child in value:
            _no_secret_fields(child)


def _configuration() -> tuple[Path, str]:
    raw = os.getenv('SUBTITLE_COMPLETION_SHARED_ROOT', '')
    root_id = os.getenv('SUBTITLE_COMPLETION_SHARED_ROOT_ID', '')
    if not raw or not root_id or not re.fullmatch(r'[A-Za-z0-9._-]{1,128}', root_id):
        raise CompletionError('completion_not_enabled', 503)
    root = Path(raw).resolve(strict=True)
    if not root.is_dir() or root == Path(root.anchor):
        raise CompletionError('completion_invalid_shared_root', 503)
    return root, root_id


def capability() -> dict[str, Any]:
    try:
        _configuration()
        enabled = True
    except (CompletionError, OSError, ValueError):
        enabled = False
    return dict(supported=True, enabled=enabled, schema=1,
                identity_mode='shared-root-relative-inode')


def _path(raw: str, root: Path) -> Path:
    path = Path(raw)
    if not path.is_absolute() or str(path) != raw or '..' in path.parts:
        raise CompletionError('completion_invalid_path', 400)
    try:
        relative = path.relative_to(root)
    except ValueError:
        raise CompletionError('completion_path_outside_root', 400) from None
    current = root
    for part in relative.parts:
        current = current / part
        try:
            info = current.lstat()
        except FileNotFoundError:
            continue
        if stat.S_ISLNK(info.st_mode):
            raise CompletionError('completion_symlink_path', 400)
    return path


def _identity(path: Path, root: Path, root_id: str, *, sync: bool = False) -> dict[str, Any]:
    _path(str(path), root)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        before = os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_size <= 0:
            raise CompletionError('completion_invalid_file')
        if sync:
            os.fsync(fd)
        after = os.fstat(fd)
        current = path.lstat()
        fields = lambda s: (s.st_dev, s.st_ino, s.st_size, s.st_mtime_ns, stat.S_IFMT(s.st_mode))
        if fields(before) != fields(after) or fields(after) != fields(current):
            raise CompletionError('completion_file_changed')
        return dict(type='regular', shared_root_id=root_id,
                    relative_path=str(path.relative_to(root)), inode=after.st_ino,
                    size=after.st_size, mtime_ns=after.st_mtime_ns)
    finally:
        os.close(fd)


def _read(directory: int, name: str) -> Any:
    fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise CompletionError('completion_invalid_proof_file')
        with os.fdopen(fd, 'r', encoding='utf-8', closefd=False) as stream:
            # 不接受重复字段，避免两端 JSON 解析语义不一致。
            def unique(pairs):
                result = {}
                for key, value in pairs:
                    if key in result:
                        raise CompletionError('completion_invalid_json')
                    result[key] = value
                return result
            return json.load(stream, object_pairs_hook=unique,
                             parse_constant=lambda _: (_ for _ in ()).throw(CompletionError('completion_invalid_json')))
    finally:
        os.close(fd)


def _exists(directory: int, name: str) -> bool:
    try:
        os.stat(name, dir_fd=directory, follow_symlinks=False)
        return True
    except FileNotFoundError:
        return False


def _install(directory: int, attempt: Path, name: str, value: dict[str, Any]) -> bool:
    # 临时 inode fsync 后 link 为固定名：原子 no-replace，绝不用 replace 覆盖证明。
    data = json.dumps(value, ensure_ascii=False, allow_nan=False, separators=(',', ':')).encode('utf-8')
    fd, temporary = tempfile.mkstemp(prefix='.completion-', dir=attempt)
    temp_name = Path(temporary).name
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        try:
            os.link(temp_name, name, src_dir_fd=directory, dst_dir_fd=directory, follow_symlinks=False)
        except FileExistsError:
            return False
        os.fsync(directory)
        return True
    finally:
        os.unlink(temp_name, dir_fd=directory)
        os.fsync(directory)


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _validate_request(request: dict[str, Any], posted: dict[str, Any], root: Path) -> tuple[Path, dict[str, Path]]:
    try:
        if str(UUID(request['request_id'])) != request['request_id']:
            raise ValueError()
    except ValueError:
        raise CompletionError('completion_invalid_request_id', 400) from None
    _no_secret_fields(request)
    for key in ('source_path', 'output_video_path', 'output_srt_path', 'record_meta'):
        if not _same(request[key], posted[key]):
            raise CompletionError('completion_request_mismatch', 400)
    if not _same(request['request_snapshot'], posted['request_snapshot']):
        raise CompletionError('completion_snapshot_mismatch', 400)
    owner, meta = request['owner'], request['record_meta']
    if (meta.get('pipeline_task_id') != str(owner['task_id']) or
        meta.get('live_session_id') != owner['session_id'] or
        meta.get('recording_producer_id') != owner['producer_id'] or
        meta.get('recording_attempt_path') != request['output_video_path'] or
        ('input_index' in meta and not _same(meta['input_index'], owner['input_index']))):
        raise CompletionError('completion_owner_mismatch', 400)
    attempt = _path(request['attempt_directory'], root)
    formal = _path(request['formal_output_path'], root)
    source = _path(request['source_path'], root)
    outputs = {key: _path(request[f'output_{key}_path'], root) for key in ('video', 'srt', 'ass')}
    video = outputs['video']
    if (not re.fullmatch(r'\.attempt-[A-Za-z0-9_-]+', attempt.name) or
        attempt.parent != formal.parent or video.parent != attempt or
        video.name != formal.name or formal.suffix != '.mp4' or
        outputs['srt'] != video.with_suffix('.srt') or outputs['ass'] != video.with_suffix('.ass') or
        source in [formal, *outputs.values()]):
        raise CompletionError('completion_attempt_mismatch', 400)
    info = attempt.lstat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o700:
        raise CompletionError('completion_attempt_not_private', 400)
    return attempt, outputs


@contextmanager
def _locked(attempt: Path):
    directory = os.open(attempt, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    lock = None
    try:
        lock = os.open('.worker.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=directory)
        info = os.fstat(lock)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise CompletionError('completion_invalid_lock')
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise CompletionError('completion_attempt_busy') from None
        yield directory
    finally:
        if lock is not None:
            os.close(lock)
        os.close(directory)


def _response(response: Any, ass: Path) -> None:
    if (not isinstance(response, dict) or response.get('ass_path') != str(ass) or
        not isinstance(response.get('segments'), list) or
        any(not isinstance(response.get(key), str) for key in
            ('render_preset', 'actual_provider', 'actual_model', 'actual_burn_provider'))):
        raise CompletionError('completion_invalid_response')
    for segment in response['segments']:
        if (not isinstance(segment, dict) or not isinstance(segment.get('text'), str) or
            type(segment.get('index')) is not int or segment['index'] < 1 or
            any(not isinstance(segment.get(key), str) or not re.fullmatch(r'\d{2,}:[0-5]\d:[0-5]\d,\d{3}', segment[key]) for key in ('start', 'end'))):
            raise CompletionError('completion_invalid_response')
    _no_secret_fields(response)


def execute_completion(protocol: CompletionRequest, posted: dict[str, Any], prepare: Callable[[], dict[str, Any]],
                       run: Callable[..., dict[str, Any]]) -> dict[str, Any]:
    """在同一 OS 锁下完成核对、core 和提交；任何不确定状态都不再计算。"""
    request = protocol.model_dump(by_alias=True)
    try:
        root, root_id = _configuration()
        attempt, outputs = _validate_request(request, posted, root)
        with _locked(attempt) as directory:
            if not _same(_read(directory, 'worker-request.json'), request):
                raise CompletionError('completion_persisted_request_mismatch')
            if _exists(directory, 'worker-result.json'):
                saved = _read(directory, 'worker-result.json')
                if (not isinstance(saved, dict) or type(saved.get('schema')) is not int or
                    saved.get('schema') != 1 or not _same(saved.get('request'), request)):
                    raise CompletionError('completion_receipt_mismatch')
                started = _read(directory, 'worker-started.json')
                saved_execution = saved.get('executed_request')
                source_identity = saved.get('source_identity')
                if (not isinstance(saved_execution, dict) or set(saved_execution) != set(EXECUTION_FIELDS) or
                    not isinstance(source_identity, dict) or
                    set(source_identity) != {'type', 'shared_root_id', 'relative_path', 'inode', 'size', 'mtime_ns'} or
                    source_identity.get('type') != 'regular' or source_identity.get('shared_root_id') != root_id or
                    source_identity.get('relative_path') != str(Path(request['source_path']).relative_to(root)) or
                    any(type(source_identity.get(key)) is not int for key in ('inode', 'size', 'mtime_ns')) or
                    source_identity['size'] <= 0):
                    raise CompletionError('completion_receipt_mismatch')
                for key in ('source_path', 'output_video_path', 'output_srt_path', 'provider', 'language', 'burn_style'):
                    if not _same(saved_execution[key], posted[key]):
                        raise CompletionError('completion_executed_request_mismatch')
                if (not isinstance(started, dict) or type(started.get('schema')) is not int or
                    started.get('schema') != 1 or not _same(started.get('request'), request) or
                    not _same(started.get('executed_request'), saved.get('executed_request')) or
                    not _same(started.get('source_identity'), saved.get('source_identity')) or
                    not isinstance(saved.get('completed_at'), str) or
                    not isinstance(saved.get('receipt_created_at'), str)):
                    raise CompletionError('completion_receipt_mismatch')
                _no_secret_fields(saved)
                _response(saved.get('response'), outputs['ass'])
                if not _same(saved.get('output_identities'), {key: _identity(path, root, root_id) for key, path in outputs.items()}):
                    raise CompletionError('completion_output_changed')
                # 即使之前安装成功后 fsync 报错，再次返回前仍须重新耐久。
                for path in outputs.values():
                    _identity(path, root, root_id, sync=True)
                fd = os.open('worker-result.json', os.O_RDONLY | os.O_NOFOLLOW, dir_fd=directory)
                try:
                    os.fsync(fd)
                finally:
                    os.close(fd)
                os.fsync(directory)
                return saved['response']
            if _exists(directory, 'worker-started.json'):
                raise CompletionError('completion_unknown')
            if any(os.path.lexists(path) for path in outputs.values()):
                raise CompletionError('completion_output_occupied')
            source = _identity(Path(request['source_path']), root, root_id)
            try:
                options = prepare()
            except (OSError, ValueError, TypeError, KeyError) as exc:
                raise _CoreFailure(exc) from None
            executed = execution_snapshot(options)
            _no_secret_fields(executed)
            started = dict(schema=1, request=request, executed_request=executed,
                           source_identity=source, started_at=_now())
            if not _install(directory, attempt, 'worker-started.json', started):
                raise CompletionError('completion_unknown')
            try:
                response = run(**options)
            except (OSError, ValueError, TypeError, KeyError) as exc:
                # 保留原 core 异常类别，不能被证明 IO 的 unknown 处理吞掉。
                raise _CoreFailure(exc) from None
            _response(response, outputs['ass'])
            identities = {key: _identity(path, root, root_id, sync=True) for key, path in outputs.items()}
            if _identity(Path(request['source_path']), root, root_id) != source:
                raise CompletionError('completion_source_changed')
            # 再核路径，阻止 core 执行期间的目录或输出替换被当成原路径成果。
            _validate_request(request, posted, root)
            current_dir = attempt.lstat()
            held_dir = os.fstat(directory)
            if (current_dir.st_dev, current_dir.st_ino) != (held_dir.st_dev, held_dir.st_ino):
                raise CompletionError('completion_attempt_changed')
            if identities != {key: _identity(path, root, root_id) for key, path in outputs.items()}:
                raise CompletionError('completion_output_changed')
            receipt = dict(schema=1, request=request, executed_request=executed, response=response,
                           source_identity=source, output_identities=identities,
                           completed_at=_now(), receipt_created_at=_now())
            if not _install(directory, attempt, 'worker-result.json', receipt):
                if not _same(_read(directory, 'worker-result.json'), receipt):
                    raise CompletionError('completion_receipt_conflict')
            return response
    except _CoreFailure as exc:
        raise exc.original from None
    except CompletionError:
        raise
    except (OSError, ValueError, TypeError, KeyError):
        raise CompletionError('completion_io_or_proof_unknown') from None
