import copy
import datetime as dt
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from observer import configured_library_root, publish, validate_snapshot

ROOT = "/fixture/media"


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.now = dt.datetime.now(dt.timezone.utc)
        self.data = {'schema_version': 2, 'adapter_version': 'ugreen-fixed-select-v2', 'platform': {'state': 'platform_unconfirmed'},
                     'generated_at': self.now.isoformat(), 'library_root': ROOT,
                     'libraries': [{'id': 4, 'roots': [ROOT], 'read_local_info_first': True,
                                    'nfo_info_save_to_local': True}], 'files': []}

    def test_live_setting_is_preserved_not_claimed_safe(self):
        result = validate_snapshot(json.dumps(self.data).encode(), self.now, ROOT)
        self.assertTrue(result['libraries'][0]['nfo_info_save_to_local'])

    def test_unknown_or_stale_data_rejected(self):
        variants = [('generated_at', (self.now-dt.timedelta(minutes=2)).isoformat()),
                    ('generated_at', (self.now+dt.timedelta(minutes=2)).isoformat()),
                    ('libraries', []), ('library_root', '/elsewhere'), ('schema_version', 1)]
        for key, value in variants:
            with self.subTest(key=key, value=value):
                data = copy.deepcopy(self.data); data[key] = value
                with self.assertRaises(ValueError):
                    validate_snapshot(json.dumps(data).encode(), self.now, ROOT)

    def test_library_root_must_be_explicit_and_normalized(self):
        for root in ('', 'video', '/', '//video', '/video/../other', '/video/', '/video\nother'):
            with self.subTest(root=root), patch.dict(os.environ, {'BILILIVE_INDEX_LIBRARY_ROOT': root}):
                with self.assertRaisesRegex(ValueError, 'library_root_configuration_invalid'):
                    configured_library_root()
        with patch.dict(os.environ, {'BILILIVE_INDEX_LIBRARY_ROOT': ROOT}):
            self.assertEqual(configured_library_root(), ROOT)

    def test_files_cannot_escape_configured_library_prefix(self):
        self.data['files'] = [{'path': ROOT + '-other/sample.mp4'}]
        with self.assertRaisesRegex(ValueError, 'file_path_invalid'):
            validate_snapshot(json.dumps(self.data).encode(), self.now, ROOT)

    def test_atomic_publish_replaces_complete_json(self):
        with tempfile.TemporaryDirectory() as name:
            directory = Path(name)
            publish(directory, self.data)
            self.assertEqual(json.loads((directory/'snapshot.json').read_text()), self.data)
            self.data['files'] = [{'path': ROOT+'/sample.mp4'}]
            publish(directory, self.data)
            self.assertEqual(json.loads((directory/'snapshot.json').read_text()), self.data)
            self.assertEqual(sorted(p.name for p in directory.iterdir()), ['snapshot.json'])


class PlatformTests(unittest.TestCase):
    def test_process_unique_uid_start_and_only_arg0_read(self):
        import io
        import os
        import time
        from unittest.mock import patch
        import platform_probe as observer
        with tempfile.TemporaryDirectory() as name:
            proc = Path(name)
            boot = int(time.time()) - 1000
            (proc/'stat').write_text('btime '+str(boot)+'\n')
            (proc/'uptime').write_text(str(time.time()-boot)+' 0\n')
            def process(pid, arg0=observer.STARTUP):
                entry=proc/str(pid); entry.mkdir()
                (entry/'comm').write_text('video_serv\n')
                (entry/'stat').write_text(str(pid)+' (video_serv) '+' '.join(['S']+['0']*18+['100'])+'\n')
                (entry/'cmdline').write_bytes(arg0.encode()+b'\0PRIVATE_ARGUMENT_NOT_READ\0')
            process(42)
            streams=[]
            original=Path.open
            class Tracked(io.BytesIO):
                def close(self):
                    streams.append(self.tell()); super().close()
            def opened(path, *args, **kwargs):
                if path.name=='cmdline':
                    return Tracked(observer.STARTUP.encode()+b'\0PRIVATE_ARGUMENT_NOT_READ\0')
                return original(path,*args,**kwargs)
            with patch.object(observer,'PROC',proc), patch.object(Path,'open',opened):
                identity=observer._process_identity()
                self.assertEqual(identity[:3], (42, os.geteuid(), 100))
                self.assertEqual(streams,[len(observer.STARTUP)+1])
            with patch.object(observer,'PROC',proc):
                process(43)
                with self.assertRaisesRegex(ValueError,'process_not_unique'):
                    observer._process_identity()
                (proc/'43'/'comm').write_text('other\n')
                (proc/'42'/'cmdline').write_bytes(b'/tmp/arbitrary\0secret\0')
                with self.assertRaisesRegex(ValueError,'process_startup_path_changed'):
                    observer._process_identity()

    def test_root_group_writable_parent_and_symlink_permissions(self):
        import stat
        from types import SimpleNamespace
        from unittest.mock import patch
        import platform_probe as observer
        def entry(mode, uid=0, gid=0):
            return SimpleNamespace(st_mode=mode,st_uid=uid,st_gid=gid)
        with patch.object(observer.os,'geteuid',return_value=100), patch.object(observer.os,'getegid',return_value=100), patch.object(observer.os,'getgroups',return_value=[100]):
            observer._trusted_installation_stat(entry(stat.S_IFDIR|0o775))
            observer._trusted_installation_stat(entry(stat.S_IFLNK|0o777))
            for mode,uid,gid in [(stat.S_IFDIR|0o777,0,0),(stat.S_IFDIR|0o775,0,100),(stat.S_IFREG|0o755,100,100)]:
                with self.subTest(mode=mode,uid=uid,gid=gid), self.assertRaises(ValueError):
                    observer._trusted_installation_stat(entry(mode,uid,gid))

    def test_same_build_restart_is_new_instance_but_not_new_capability(self):
        from observer import same_platform_instance
        before={'state':'confirmed','pid':1,'uid':0,'start_ticks':10,'started_at':'old','startup_path':'fixed','build_path':'fixed-build','build_sha256':'a'*64}
        after=dict(before,pid=2,start_ticks=20,started_at='new')
        self.assertFalse(same_platform_instance(before,after))
        self.assertTrue(same_platform_instance(after,dict(after,observed_at='later')))


class ProbeBoundaryTests(unittest.TestCase):
    def test_helper_has_no_external_execution_network_or_argument_dispatch(self):
        import ast
        import platform_probe
        source = Path(platform_probe.__file__).read_text()
        tree = ast.parse(source)
        prohibited = {'subprocess', 'socket', 'requests', 'urllib', 'psycopg', 'psycopg2'}
        for item in ast.walk(tree):
            if isinstance(item, (ast.Import, ast.ImportFrom)):
                names = [n.name.split('.')[0] for n in item.names] if isinstance(item, ast.Import) else [item.module.split('.')[0]]
                self.assertFalse(prohibited.intersection(names))
            if isinstance(item, ast.Call):
                name = item.func.id if isinstance(item.func, ast.Name) else item.func.attr if isinstance(item.func, ast.Attribute) else ''
                self.assertNotIn(name, ('exec', 'eval', 'system', 'popen', 'spawn', 'execve', 'execv'))
        self.assertNotIn('os.environ', source)
        self.assertNotIn('argparse', source)

    def test_exe_permission_failure_publishes_unconfirmed_not_old_success(self):
        from unittest.mock import patch
        import platform_probe
        with patch.object(platform_probe, '_process_identity', return_value=(42, 0, 100, 100)), \
             patch.object(platform_probe, '_installation_identity', return_value={}), \
             patch.object(platform_probe.os, 'open', side_effect=PermissionError):
            self.assertEqual(platform_probe.capture_platform()['state'], 'platform_unconfirmed')


if __name__ == '__main__':
    unittest.main()
