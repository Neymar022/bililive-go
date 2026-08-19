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
            't("HorizontalList",{ref:"cardView",attrs:{sourceData:e.currentGroupEpisodeList,'
            'imgWidth:182,imgHeight:170,className:"card-list"},scopedSlots:e._u([])})'
            't("div",{staticClass:"card-item"},[])'
            'scrollToActiveTab(){const e=this.$refs.scrollContainerRef,'
            't=this.$refs.cardItemRefs;if(!e||!t)return;const i=t[this.activeIndex];'
            'if(!i)return;const a=i.offsetLeft,s=e.clientWidth,'
            'o=a+i.offsetWidth/2-s/2,n=e.scrollWidth-s,'
            'l=Math.max(0,Math.min(o,n));e.scrollLeft=l}'
        )

    def deployed_v1_source(self):
        return (
            self.source()
            .replace(
                '(0,r.TI)(i)?`${(0,r.WP)(s)}`:a?',
                '(0,r.TI)(i)?a||`${(0,r.WP)(s)}`:a?',
            )
            .replace(
                't===g.OY?(0,r.WP)(a):i?',
                't===g.OY?i||(0,r.WP)(a):i?',
            )
            .replace(
                'e.isUnRecognizedEpisode(i.episode)?e.UNRECOGNIZED_EPISODE_TEXT:i.episode',
                'e.isUnRecognizedEpisode(i.episode)?"":i.episode',
            )
        )

    def test_patches_only_user_visible_fallbacks(self):
        source = self.source()

        patched, _ = MODULE.patch_javascript(source)

        self.assertIn('(0,r.TI)(i)?a||`${(0,r.WP)(s)}`', patched)
        self.assertIn('t===g.OY?i||(0,r.WP)(a)', patched)
        self.assertIn(
            'e.isUnRecognizedEpisode(i.episode)?'
            '(e.getEpisodeTitle(i).match(/\\d{4}-\\d{2}-\\d{2}/)||'
            '[e.UNRECOGNIZED_EPISODE_TEXT])[0].replace(/^\\d{4}-/,""):i.episode',
            patched,
        )
        self.assertIn(
            'className:"card-list"},on:{select:e.handleChangeEpisode},scopedSlots:',
            patched,
        )
        self.assertIn(
            'staticClass:"card-item",staticStyle:{cursor:"pointer"}',
            patched,
        )
        self.assertIn(
            '"card-list"===this.className',
            patched,
        )
        self.assertIn(
            'e.style.paddingRight=Math.max(0,l+s-c)+"px"',
            patched,
        )
        self.assertNotIn(
            'e.isUnRecognizedEpisode(i.episode)?"":i.episode',
            patched,
        )
        self.assertIn('S01E1673386692282296', patched)

    def test_upgrades_deployed_empty_serial_label_without_rewriting_identity(self):
        source = self.deployed_v1_source()

        patched, _ = MODULE.patch_javascript(source)

        self.assertNotIn('?"":i.episode', patched)
        self.assertIn('replace(/^\\d{4}-/,""):i.episode', patched)
        self.assertIn(
            'className:"card-list"},on:{select:e.handleChangeEpisode},scopedSlots:',
            patched,
        )
        self.assertIn('S01E1673386692282296', patched)

    def test_final_patch_is_idempotent(self):
        patched, _ = MODULE.patch_javascript(self.deployed_v1_source())

        second, _ = MODULE.patch_javascript(patched)

        self.assertEqual(patched, second)

    def test_fails_closed_for_unknown_mixed_patch_state(self):
        mixed = self.source().replace(
            '(0,r.TI)(i)?`${(0,r.WP)(s)}`:a?',
            '(0,r.TI)(i)?a||`${(0,r.WP)(s)}`:a?',
        )

        with self.assertRaisesRegex(RuntimeError, "known bundle state"):
            MODULE.patch_javascript(mixed)

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
