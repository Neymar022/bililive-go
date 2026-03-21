import pathlib
import unittest


class SubtitleWorkerDockerfileTest(unittest.TestCase):
    def test_installs_pillow_dependency(self) -> None:
        requirements = pathlib.Path(__file__).resolve().parents[1] / "requirements.txt"
        text = requirements.read_text(encoding="utf-8")

        self.assertIn("Pillow==", text)

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

    def test_compose_uses_same_playwright_browser_path(self) -> None:
        repo_root = pathlib.Path(__file__).resolve().parents[3]
        dockerfile_text = (repo_root / "contrib" / "subtitle-worker" / "Dockerfile").read_text(encoding="utf-8")
        compose_text = (repo_root / "docker-compose.yml").read_text(encoding="utf-8")

        self.assertIn("PLAYWRIGHT_BROWSERS_PATH=/root/.cache/ms-playwright", dockerfile_text)
        self.assertIn("PLAYWRIGHT_BROWSERS_PATH=/root/.cache/ms-playwright", compose_text)


if __name__ == "__main__":
    unittest.main()
