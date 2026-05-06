import pathlib
import unittest


class SubtitleWorkerDockerfileTest(unittest.TestCase):
    def test_does_not_install_style_lab_python_dependencies(self) -> None:
        requirements = pathlib.Path(__file__).resolve().parents[1] / "requirements.txt"
        text = requirements.read_text(encoding="utf-8")

        self.assertNotIn("Pillow==", text)
        self.assertNotIn("playwright==", text)

    def test_installs_noto_cjk_font_package(self) -> None:
        dockerfile = pathlib.Path(__file__).resolve().parents[1] / "Dockerfile"
        text = dockerfile.read_text(encoding="utf-8")

        self.assertIn("fonts-noto-cjk", text)

    def test_does_not_install_playwright_chromium_stack(self) -> None:
        root = pathlib.Path(__file__).resolve().parents[1]
        dockerfile_text = (root / "Dockerfile").read_text(encoding="utf-8")
        requirements_text = (root / "requirements.txt").read_text(encoding="utf-8")

        self.assertNotIn("playwright==", requirements_text)
        self.assertNotIn("python -m playwright install --with-deps chromium", dockerfile_text)

    def test_dockerfile_only_copies_runtime_sources(self) -> None:
        dockerfile = pathlib.Path(__file__).resolve().parents[1] / "Dockerfile"
        text = dockerfile.read_text(encoding="utf-8")

        self.assertIn("COPY app.py worker_core.py ass_generator.py segmenter.py /app/", text)
        self.assertNotIn("COPY renderers /app/renderers", text)


if __name__ == "__main__":
    unittest.main()
