"""仅本地隔离 Linux 容器运行的真实 ELF/UID 反例；不代表 NAS 或 systemd 实证。"""
import copy
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import unittest
from unittest.mock import patch

import observer
import platform_probe as probe


@unittest.skipUnless(sys.platform == 'linux' and os.environ.get('BILILIVE_PLATFORM_PROCESS_FIXTURE') == '1',
                     '需要自有 Linux tmpfs 容器，不触及真实安装路径')
class PlatformProcessTests(unittest.TestCase):
    def setUp(self):
        self.app = Path(probe.BUILD).parent
        self.app.mkdir(parents=True, exist_ok=True)
        shutil.copyfile('/bin/sleep', probe.BUILD)
        os.chmod(probe.BUILD, 0o755)
        Path(probe.STARTUP).symlink_to(probe.BUILD)
        self.processes = []
        self.start()

    def tearDown(self):
        for process in self.processes:
            if process.poll() is None:
                process.terminate()
            process.wait()
        Path(probe.STARTUP).unlink(missing_ok=True)
        shutil.rmtree('/volume1/@appstore')
        if probe.PROOF.parent.is_symlink():
            probe.PROOF.parent.unlink()
        elif probe.PROOF.parent.exists():
            shutil.rmtree(probe.PROOF.parent)

    def start(self):
        process = subprocess.Popen([probe.STARTUP, '120'], executable=probe.STARTUP)
        self.processes.append(process)
        # Popen 的 exec 错误握手保证已 exec；不以任意 sleep 推测 readiness。
        return process

    def stop(self, process):
        process.terminate()
        process.wait()

    def helper(self):
        result = subprocess.run([sys.executable, '-B', str(Path(probe.__file__))], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(probe.PROOF.read_text())

    def consume(self):
        code = 'import observer,json; print(json.dumps(observer.observe_platform()))'
        result = subprocess.run([sys.executable, '-B', '-c', code], user=65534, group=65534, extra_groups=[],
                                env={'PYTHONPATH': str(Path(observer.__file__).parent)}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def baseline(self):
        proof = self.helper()
        self.assertEqual(proof['state'], 'confirmed')
        actual = hashlib.sha256(Path('/proc/%d/exe' % self.processes[-1].pid).read_bytes()).hexdigest()
        self.assertEqual(proof['build_sha256'], actual)
        self.assertEqual(self.consume()['state'], 'confirmed')
        return proof

    def staged(self):
        staged = self.app.with_name('staged')
        staged.mkdir()
        (staged/'video_serv').write_bytes(Path(probe.BUILD).read_bytes() + b'\nnew-build-B\n')
        (staged/'video_serv').chmod(0o755)
        return staged

    def test_directory_upgrade_cannot_confirm_disk_build(self):
        self.baseline()
        staged = self.staged()
        self.app.rename(self.app.with_name('old'))
        staged.rename(self.app)
        actual = hashlib.sha256(Path('/proc/%d/exe' % self.processes[-1].pid).read_bytes()).hexdigest()
        self.assertNotEqual(actual, hashlib.sha256(Path(probe.BUILD).read_bytes()).hexdigest())
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')
        self.assertEqual(self.helper()['state'], 'platform_unconfirmed')
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')

    def test_upgrade_rollback_cannot_reuse_directory_inode_anchor(self):
        baseline = self.baseline()
        old_ino = self.app.stat().st_ino
        staged = self.staged()
        self.stop(self.processes[-1])
        self.app.rename(self.app.with_name('old'))
        staged.rename(self.app)
        current = self.start()
        self.app.rename(self.app.with_name('failed-new'))
        self.app.with_name('old').rename(self.app)
        self.assertEqual(old_ino, self.app.stat().st_ino)
        self.assertEqual(baseline['build_sha256'], hashlib.sha256(Path(probe.BUILD).read_bytes()).hexdigest())
        self.assertNotEqual(baseline['build_sha256'], hashlib.sha256(Path('/proc/%d/exe' % current.pid).read_bytes()).hexdigest())
        self.assertEqual(self.helper()['state'], 'platform_unconfirmed')
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')

    def test_same_build_restart_recovers_next_round(self):
        before = self.baseline()
        self.stop(self.processes[-1])
        self.start()
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')
        after = self.helper()
        self.assertEqual(after['state'], 'confirmed')
        self.assertEqual(before['build_sha256'], after['build_sha256'])
        self.assertNotEqual(before['pid'], after['pid'])
        self.assertEqual(self.consume()['state'], 'confirmed')

    def test_multiple_processes_invalidate_previous_proof(self):
        self.baseline()
        self.start()
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')
        self.assertEqual(self.helper()['state'], 'platform_unconfirmed')
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')

    def test_shared_parent_change_does_not_require_manual_reanchor(self):
        before = self.baseline()
        marker = self.app.parent/'unrelated-normal-install'
        marker.mkdir()
        self.assertEqual(self.consume()['state'], 'confirmed')
        self.assertEqual(self.helper()['build_sha256'], before['build_sha256'])

    def test_missing_stale_untrusted_and_invalid_proof_fail_closed(self):
        original = self.baseline()
        changes = {
            'stale': lambda d: d.update(observed_at=(dt.datetime.now(dt.timezone.utc)-dt.timedelta(minutes=2)).isoformat()),
            'future': lambda d: d.update(observed_at=(dt.datetime.now(dt.timezone.utc)+dt.timedelta(minutes=2)).isoformat()),
            'pid': lambda d: d.update(pid=d['pid']+100000),
            'start': lambda d: d.update(start_ticks=d['start_ticks']+1),
            'path': lambda d: d.update(build_path='/tmp/arbitrary'),
            'sha': lambda d: d.update(build_sha256='unknown'),
            'boolean_pid': lambda d: d.update(pid=True),
            'schema': lambda d: d.update(schema_version=2),
            'unknown_key': lambda d: d.update(extra='unknown'),
            'missing_stat': lambda d: d.pop('exe_stat'),
            'inode': lambda d: d['exe_stat'].update(ino=d['exe_stat']['ino']+1),
        }
        for name, mutate in changes.items():
            with self.subTest(case=name):
                value = copy.deepcopy(original)
                mutate(value)
                probe.publish_proof(value)
                self.assertEqual(self.consume()['state'], 'platform_unconfirmed')
        probe.publish_proof(original)
        probe.PROOF.chmod(0o666)
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')
        probe.PROOF.chmod(0o644)
        os.chown(probe.PROOF, 65534, 65534)
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')
        os.chown(probe.PROOF, 0, 0)
        probe.PROOF.unlink()
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')
        probe.PROOF.symlink_to('/etc/passwd')
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')
        with self.assertRaises(ValueError):
            probe.publish_proof(original)
        probe.PROOF.unlink()
        probe.PROOF.parent.chmod(0o777)
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')
        probe.PROOF.parent.chmod(0o755)
        probe.PROOF.parent.rmdir()
        probe.PROOF.parent.symlink_to('/fixture')
        with self.assertRaises(ValueError):
            probe.publish_proof(original)
        self.assertEqual(self.consume()['state'], 'platform_unconfirmed')

    def test_atomic_proof_is_root_owned_and_service_cannot_write(self):
        previous = os.umask(0o077)
        try:
            self.baseline()
        finally:
            os.umask(previous)
        for _ in range(2):
            self.helper()
            file = probe.PROOF.stat()
            self.assertEqual((file.st_uid, file.st_mode & 0o777), (0, 0o644))
            directory = probe.PROOF.parent.stat()
            self.assertEqual((directory.st_uid, directory.st_mode & 0o777), (0, 0o755))
            self.assertEqual([p.name for p in probe.PROOF.parent.iterdir()], ['platform.json'])
        code = 'from pathlib import Path; Path("/run/bililive-platform-observer/platform.json").write_text("forged")'
        result = subprocess.run([sys.executable, '-B', '-c', code], user=65534, group=65534, extra_groups=[], capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        code = 'from pathlib import Path; Path("/run/bililive-platform-observer/new.json").write_text("forged")'
        result = subprocess.run([sys.executable, '-B', '-c', code], user=65534, group=65534, extra_groups=[], capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        result = subprocess.run([sys.executable, '-B', str(Path(probe.__file__)), '/arbitrary/pid'], capture_output=True)
        self.assertNotEqual(result.returncode, 0)

    def test_query_interval_restart_or_build_replace_invalidates_output(self):
        for mode in ('restart', 'directory'):
            with self.subTest(case=mode):
                self.baseline()
                def query(*args, **kwargs):
                    if mode == 'restart':
                        self.stop(self.processes[-1]); self.start()
                        probe.publish_proof(probe.capture_platform())  # 即使有另一轮新证明，查询前后仍须同实例。
                    else:
                        staged = self.staged()
                        self.app.rename(self.app.with_name('old')); staged.rename(self.app)
                    from types import SimpleNamespace
                    data = {'schema_version': 2, 'adapter_version': 'ugreen-fixed-select-v2',
                            'generated_at': dt.datetime.now(dt.timezone.utc).isoformat(), 'library_root': '/fixture/library',
                            'libraries': [{'id': 4, 'roots': ['/fixture/library'], 'read_local_info_first': True,
                                           'nfo_info_save_to_local': False}], 'files': []}
                    return SimpleNamespace(stdout=json.dumps(data))
                published = []
                with patch.dict(os.environ, {'BILILIVE_INDEX_LIBRARY_ROOT': '/fixture/library'}), \
                     patch.object(observer, 'QUERY', Path('/fixture/query.sql')), \
                     patch.object(observer, 'DESTINATION', Path('/fixture')), \
                     patch.object(observer.subprocess, 'run', side_effect=query), \
                     patch.object(observer, 'publish', side_effect=lambda path, data: published.append(data)):
                    Path('/fixture/query.sql').write_text('固定只读fixture，不执行PG')
                    observer.main()
                self.assertEqual(len(published), 1)
                self.assertEqual(published[0]['platform']['state'], 'platform_unconfirmed')
