"""只用公开 process 边界、小文件与假 core 验证持久回执。"""
import json
import os
from pathlib import Path
import tempfile
import subprocess
import sys
import stat
import unittest
from unittest import mock

from app import ProcessRequest, process, healthz
from fastapi import HTTPException
from worker_core import WorkerRetryLater, segments_to_api_payload
import worker_core


class CompletionReceiptTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.configure_fixture(Path(self.temp.name).resolve())

    def configure_fixture(self, root):
        self.root = root
        self.attempt = self.root / '.attempt-example'
        self.attempt.mkdir(mode=0o700)
        self.source = self.root / 'source.mp4'
        self.source.write_bytes(b'synthetic source')
        self.video = self.attempt / 'episode.mp4'
        self.srt = self.attempt / 'episode.srt'
        self.ass = self.attempt / 'episode.ass'
        self.receipt = self.attempt / 'worker-result.json'
        self.meta = dict(pipeline_task_id='42', live_session_id='session-example',
                         recording_producer_id='producer-example', recording_attempt_path=str(self.video))
        self.protocol = dict(schema=1, request_id='6b34eac0-d40e-4461-a673-ab426d074d02',
            owner=dict(task_id=42, session_id='session-example', producer_id='producer-example', input_index=0),
            source_path=str(self.source), formal_output_path=str(self.root / 'episode.mp4'),
            attempt_directory=str(self.attempt), output_video_path=str(self.video),
            output_srt_path=str(self.srt), output_ass_path=str(self.ass), record_meta=self.meta,
            request_snapshot=dict(provider='local-whisper', language='zh', burn_style={}))
        self.payload = dict(source_path=str(self.source), output_video_path=str(self.video),
            output_srt_path=str(self.srt), provider='local-whisper', record_meta=self.meta,
            completion_v1=self.protocol)
        self.request_file = self.attempt / 'worker-request.json'
        self.save_request()
        self.env = mock.patch.dict(os.environ, {
            'SUBTITLE_COMPLETION_SHARED_ROOT': str(self.root),
            'SUBTITLE_COMPLETION_SHARED_ROOT_ID': 'synthetic-shared-root',
            'SUBTITLE_MAC_TRANSCRIBER_TOKEN': 'fixture-private-token',
        })
        self.env.start()
        self.addCleanup(self.env.stop)
        self.response = dict(segments=segments_to_api_payload([dict(index=1, start_ms=0, end_ms=10, text='合成字幕')]),
            ass_path=str(self.ass), render_preset='bottom_center', actual_provider='local-whisper',
            actual_model='fixture', actual_burn_provider='nas-software')

    def save_request(self):
        self.request_file.write_text(json.dumps(self.protocol), encoding='utf-8')
        self.request_file.chmod(0o600)

    def core(self, **kwargs):
        self.assertTrue((self.attempt / 'worker-started.json').exists())
        self.assertFalse(self.receipt.exists())
        self.video.write_bytes(b'synthetic mp4')
        self.srt.write_text('合成字幕', encoding='utf-8')
        self.ass.write_text('synthetic ass', encoding='utf-8')
        return self.response

    def test_completed_response_is_durable_before_return_and_replayed_once(self):
        # 首个 RED：旧入口没有 started，core 边界的行为断言必须失败。
        synced = []
        real_fsync = os.fsync
        def fsync(fd):
            real_fsync(fd)
            info = os.fstat(fd)
            synced.append((info.st_ino, stat.S_ISDIR(info.st_mode), self.receipt.exists()))
        with mock.patch('app.transcribe_and_burn', side_effect=self.core) as core, mock.patch('os.fsync', side_effect=fsync):
            first = process(ProcessRequest(**self.payload))
            first_syncs = list(synced)
            receipt_bytes = self.receipt.read_bytes()
            receipt_stat = self.receipt.stat()
            saved = json.loads(receipt_bytes)
            self.assertEqual(first, saved['response'])
            self.assertEqual(first, process(ProcessRequest(**self.payload)))
        self.assertEqual(1, core.call_count)
        self.assertEqual('fixture-private-token', core.call_args.kwargs['mac_transcriber_token'])
        for path in (self.video, self.srt, self.ass, self.receipt):
            self.assertIn(path.stat().st_ino, [item[0] for item in first_syncs])
        self.assertIn((self.attempt.stat().st_ino, True, True), first_syncs)
        self.assertIn('completion_v1', healthz())
        self.assertTrue(healthz()['completion_v1']['enabled'])
        self.assertNotIn('ctime', saved['output_identities']['video'])
        self.assertNotIn('nlink', saved['output_identities']['video'])
        # promotion 可改变链接数；旧 receipt 不应因此失效。
        os.link(self.video, self.root / 'promoted.mp4')
        with mock.patch('app.transcribe_and_burn', side_effect=AssertionError('不能重算')), mock.patch.dict(os.environ, {'SUBTITLE_VAAPI_QP': 'unavailable-config'}):
            self.assertEqual(first, process(ProcessRequest(**self.payload)))
        self.assertEqual(self.protocol, saved['request'])
        self.assertEqual(self.response, saved['response'])
        self.assertEqual('zh', saved['executed_request']['language'])
        self.assertEqual(50, saved['executed_request']['burn_style']['font_size'])
        self.assertNotIn('fixture-private-token', self.receipt.read_text())
        self.assertEqual(0o600, self.receipt.stat().st_mode & 0o777)
        self.assertEqual(0o700, self.attempt.stat().st_mode & 0o777)
        self.assertEqual(receipt_bytes, self.receipt.read_bytes())
        self.assertEqual(receipt_stat.st_ino, self.receipt.stat().st_ino)
        self.protocol['request_id'] = '63c3b922-43c7-4ddc-b791-478f29c46d88'
        self.save_request()
        self.assert_refused('completion_receipt_mismatch')
        self.assertEqual(receipt_bytes, self.receipt.read_bytes())

    def assert_refused(self, code):
        with mock.patch('app.transcribe_and_burn', side_effect=AssertionError('不能执行 core')) as core:
            with self.assertRaises(HTTPException) as failure:
                process(ProcessRequest(**self.payload))
        self.assertEqual(code, failure.exception.detail['code'])
        self.assertEqual(0, core.call_count)

    def test_wrong_owner_or_saved_request_never_executes(self):
        self.protocol['owner']['producer_id'] = 'other-producer'
        self.save_request()
        self.assert_refused('completion_owner_mismatch')
        self.protocol['owner']['producer_id'] = 'producer-example'
        self.assert_refused('completion_persisted_request_mismatch')
        self.assertFalse((self.attempt / 'worker-started.json').exists())

    def test_started_without_receipt_is_unknown(self):
        (self.attempt / 'worker-started.json').write_bytes(b'crash proof')
        self.assert_refused('completion_unknown')
        self.assertEqual(b'crash proof', (self.attempt / 'worker-started.json').read_bytes())
        self.assertFalse(self.receipt.exists())

    def test_partial_output_remains_unknown(self):
        def partial(**kwargs):
            self.video.write_bytes(b'partial mp4')
            return self.response
        with mock.patch('app.transcribe_and_burn', side_effect=partial):
            with self.assertRaises(HTTPException):
                process(ProcessRequest(**self.payload))
        self.assertTrue(self.video.exists())
        self.assertFalse(self.receipt.exists())
        self.assert_refused('completion_unknown')

    def test_receipt_commit_failure_preserves_started_and_all_outputs(self):
        link = os.link
        def failing_link(src, dst, **kwargs):
            if dst == 'worker-result.json':
                raise OSError('synthetic disk failure')
            return link(src, dst, **kwargs)
        with mock.patch('app.transcribe_and_burn', side_effect=self.core), mock.patch('os.link', side_effect=failing_link):
            with self.assertRaises(HTTPException) as failure:
                process(ProcessRequest(**self.payload))
        self.assertEqual('completion_io_or_proof_unknown', failure.exception.detail['code'])
        self.assertTrue(all(path.exists() for path in (self.video, self.srt, self.ass)))
        self.assertFalse(self.receipt.exists())
        self.assert_refused('completion_unknown')

    def test_attempt_lock_excludes_another_process_during_core(self):
        def locked_core(**kwargs):
            # 独立进程尝试同一个 OS 锁，不能依赖 Python 线程互斥。
            result = subprocess.run([sys.executable, '-c',
                "import fcntl,sys; f=open(sys.argv[1], 'r+'); "
                "\ntry: fcntl.flock(f, fcntl.LOCK_EX|fcntl.LOCK_NB)"
                "\nexcept BlockingIOError: sys.exit(23)", str(self.attempt / '.worker.lock')],
                capture_output=True, timeout=10)
            self.assertEqual(23, result.returncode, result.stderr.decode())
            with self.assertRaises(HTTPException) as failure:
                process(ProcessRequest(**self.payload))
            self.assertEqual('completion_attempt_busy', failure.exception.detail['code'])
            return self.core(**kwargs)
        with mock.patch('app.transcribe_and_burn', side_effect=locked_core):
            process(ProcessRequest(**self.payload))
        self.assertTrue(self.receipt.exists())

    def test_secret_metadata_and_symlink_rejected_before_core(self):
        self.meta['api_token'] = 'private-fixture'
        self.save_request()
        self.assert_refused('completion_secret_field')
        del self.meta['api_token']
        self.save_request()
        self.video.symlink_to(self.source)
        self.assert_refused('completion_symlink_path')

    def test_source_change_never_creates_receipt(self):
        def changed(**kwargs):
            response = self.core(**kwargs)
            self.source.write_bytes(b'changed source')
            return response
        with mock.patch('app.transcribe_and_burn', side_effect=changed):
            with self.assertRaises(HTTPException) as failure:
                process(ProcessRequest(**self.payload))
        self.assertEqual('completion_source_changed', failure.exception.detail['code'])
        self.assertFalse(self.receipt.exists())
        self.assert_refused('completion_unknown')

    def test_legacy_request_preserves_core_and_original_errors(self):
        legacy = dict(self.payload)
        del legacy['completion_v1']
        with mock.patch('app.transcribe_and_burn', return_value=self.response) as core:
            self.assertEqual(self.response, process(ProcessRequest(**legacy)))
        self.assertEqual(1, core.call_count)
        self.assertFalse((self.attempt / 'worker-started.json').exists())
        with mock.patch.dict(os.environ, {'SUBTITLE_COMPLETION_SHARED_ROOT': ''}):
            self.assertFalse(healthz()['completion_v1']['enabled'])
            with mock.patch('app.transcribe_and_burn', return_value=self.response):
                self.assertEqual(self.response, process(ProcessRequest(**legacy)))
        for request in (legacy, self.payload):
            with self.subTest(v1='completion_v1' in request):
                with mock.patch('app.transcribe_and_burn', side_effect=WorkerRetryLater('mac_transcriber_unavailable', 'safe message')):
                    with self.assertRaises(HTTPException) as failure:
                        process(ProcessRequest(**request))
                self.assertEqual(503, failure.exception.status_code)
                self.assertEqual('mac_transcriber_unavailable', failure.exception.detail['code'])
                if 'completion_v1' in request:
                    self.assertTrue((self.attempt / 'worker-started.json').exists())
                    self.assert_refused('completion_unknown')

    def test_real_core_segment_contract(self):
        # 导出微小 fixture 供 Go 真实 reader 接纳；普通运行仍用自动清理的临时目录。
        wire = os.getenv('BILILIVE_COMPLETION_WIRE_FIXTURE')
        if wire:
            root = Path(wire)
            root.mkdir(mode=0o700)
            self.configure_fixture(root.resolve())
        # 合成内部片段必须经真实 API converter，而不是自造 ProcessResponse 字段。
        self.response['segments'] = segments_to_api_payload([
            dict(index=1, start_ms=0, end_ms=10, text='合成字幕')])
        failure = None
        with mock.patch('app.transcribe_and_burn', side_effect=self.core) as core:
            try:
                first = process(ProcessRequest(**self.payload))
            except HTTPException as exc:
                failure = exc
            self.assertIsNone(failure, '真实 core API 字段应被 receipt 接纳')
            self.assertEqual(first, process(ProcessRequest(**self.payload)))
        self.assertEqual(1, core.call_count)
        self.assertEqual('00:00:00,000', first['segments'][0]['start'])
        self.assertEqual('00:00:00,010', first['segments'][0]['end'])
        self.assertEqual(self.response, json.loads(self.receipt.read_text())['response'])

    def test_v1_real_chains_keep_private_errors_out_of_logs(self):
        sentinel = 'synthetic-private-provider-sentinel'
        for phase in ('provider', 'burn'):
            with self.subTest(phase=phase), tempfile.TemporaryDirectory() as root:
                self.configure_fixture(Path(root).resolve())
                def real_chain(**kwargs):
                    if phase == 'provider':
                        return worker_core._transcribe_with_chain(['dashscope'], 'synthetic.wav', 'zh')
                    return worker_core._burn_with_chain(['nas-software'], str(self.source), str(self.ass), str(self.video), {})
                target = 'worker_core._transcribe_with_provider' if phase == 'provider' else 'worker_core._burn_with_provider'
                with mock.patch('app.transcribe_and_burn', side_effect=real_chain), mock.patch(target, side_effect=RuntimeError(sentinel)):
                    with self.assertLogs('worker_core', level='WARNING') as logs, self.assertLogs('subtitle_worker', level='ERROR'):
                        with self.assertRaises(HTTPException) as failure:
                            process(ProcessRequest(**self.payload))
                    self.assertEqual(500, failure.exception.status_code)
                    self.assertTrue((self.attempt / 'worker-started.json').exists())
                    self.assertFalse(self.receipt.exists())
                    self.assertNotIn(sentinel, '\n'.join(logs.output))
                    legacy = dict(self.payload)
                    del legacy['completion_v1']
                    with self.assertLogs('worker_core', level='WARNING') as legacy_logs, self.assertLogs('subtitle_worker', level='ERROR'):
                        with self.assertRaises(HTTPException):
                            process(ProcessRequest(**legacy))
                    self.assertIn(sentinel, '\n'.join(legacy_logs.output))


if __name__ == '__main__':
    unittest.main()
