import importlib.util
import gzip
import pathlib
import tempfile
import unittest
from unittest import mock


SCRIPT = pathlib.Path(__file__).with_name("patch-ugreen-video-display-title.py")
SPEC = importlib.util.spec_from_file_location("ugreen_title_patch", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class PatchUGREENVideoDisplayTitleTest(unittest.TestCase):
    def source(self):
        return (
            'const selectedPath="/video/主播.S01E1673386692282296.2026-08-18 - 标题.mp4";'
            'function ye(e,t){const{episode:i,episodeName:a,filePath:s,releaseDate:o}=t;'
            'return e===c.zu.tv&&i?(0,r.TI)(i)?`${(0,r.WP)(s)}`:a?'
            '`${d.Ru.t("home.episodeNum",[i])} - ${a}`:"normal"}'
            'getEpisodeTitle(e){const{episode:t,ep_name:i,selectedPath:a}=e;'
            'return t===g.OY?(0,r.WP)(a):i?this.$t("home.currentEpisode",[t])+"："+i:'
            'this.$t("home.currentEpisode",[t])}'
            'e.isUnRecognizedEpisode(i.episode)?e.UNRECOGNIZED_EPISODE_TEXT:i.episode'
        )

    def test_patches_only_user_visible_fallbacks(self):
        source = self.source()

        patched, counts = MODULE.patch_javascript(source)

        self.assertEqual({"recent": 1, "card": 1, "serial": 1}, counts)
        self.assertIn('(0,r.TI)(i)?a||`${(0,r.WP)(s)}`', patched)
        self.assertIn('t===g.OY?i||(0,r.WP)(a)', patched)
        self.assertIn(
            'e.isUnRecognizedEpisode(i.episode)?"":i.episode',
            patched,
        )
        self.assertIn('S01E1673386692282296', patched)

    def test_fails_closed_when_vendor_bundle_contract_changes(self):
        with self.assertRaisesRegex(RuntimeError, "expected exactly one"):
            MODULE.patch_javascript("getEpisodeTitle changed upstream")

    def test_preflights_every_asset_before_any_write(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            good = root / "app.js"
            bad = root / "app.js.gz"
            good.write_text(self.source(), encoding="utf-8")
            bad.write_bytes(gzip.compress(b"contract changed", mtime=0))
            before = good.read_bytes()

            with self.assertRaisesRegex(RuntimeError, "expected exactly one"):
                MODULE.prepare_assets([good, bad])

            self.assertEqual(before, good.read_bytes())

    def test_rolls_back_all_assets_when_second_write_fails(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            javascript = root / "app.js"
            compressed = root / "app.js.gz"
            javascript.write_text(self.source(), encoding="utf-8")
            compressed.write_bytes(gzip.compress(self.source().encode(), mtime=0))
            before = {path: path.read_bytes() for path in (javascript, compressed)}
            plans = MODULE.prepare_assets([javascript, compressed])
            original_atomic_write = MODULE.atomic_write
            failed = False

            def fail_second_asset_once(path, data):
                nonlocal failed
                if path == compressed and not failed:
                    failed = True
                    raise OSError("injected second asset failure")
                original_atomic_write(path, data)

            with mock.patch.object(MODULE, "atomic_write", side_effect=fail_second_asset_once):
                with self.assertRaisesRegex(OSError, "injected second asset failure"):
                    MODULE.apply_assets(plans, root / "backup")

            self.assertEqual(before[javascript], javascript.read_bytes())
            self.assertEqual(before[compressed], compressed.read_bytes())
            self.assertTrue((root / "backup" / javascript.name).exists())
            self.assertTrue((root / "backup" / compressed.name).exists())


if __name__ == "__main__":
    unittest.main()
