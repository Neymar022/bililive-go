import pathlib
import unittest


class SubtitleWorkerDockerfileTest(unittest.TestCase):
    def test_installs_noto_cjk_font_package(self) -> None:
        dockerfile = pathlib.Path(__file__).resolve().parents[1] / "Dockerfile"
        text = dockerfile.read_text(encoding="utf-8")

        self.assertIn("fonts-noto-cjk", text)

    def test_installs_playwright_chromium_stack(self) -> None:
        root = pathlib.Path(__file__).resolve().parents[1]
        dockerfile_text = (root / "Dockerfile").read_text(encoding="utf-8")
        requirements_text = (root / "requirements.txt").read_text(encoding="utf-8")

        self.assertIn("playwright==", requirements_text)
        self.assertIn("python -m playwright install --with-deps chromium", dockerfile_text)


if __name__ == "__main__":
    unittest.main()
