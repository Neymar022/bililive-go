from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]


class DockerReleaseTargetsTest(unittest.TestCase):
    def test_default_branch_publish_workflow_targets_neymar_namespace(self) -> None:
        text = (
            ROOT / ".github" / "workflows" / "publish-images-on-master.yaml"
        ).read_text(encoding="utf-8")
        self.assertIn("branches:", text)
        self.assertIn("- master", text)
        self.assertIn("APP_IMAGE: neymar022/bililive-go-app", text)
        self.assertIn("WORKER_IMAGE: neymar022/bililive-go-subtitle-worker", text)
        self.assertIn("tags: |", text)
        self.assertIn("type=raw,value=latest", text)
        self.assertIn("platforms: linux/amd64,linux/arm64", text)
        self.assertIn("workflow_dispatch:", text)
        self.assertIn("github.event.inputs.ref", text)
        self.assertNotIn("chigusa/bililive-go", text)

    def test_default_branch_publish_workflow_uses_buildx_and_hub_login(self) -> None:
        text = (
            ROOT / ".github" / "workflows" / "publish-images-on-master.yaml"
        ).read_text(encoding="utf-8")
        self.assertIn("docker/setup-qemu-action@v3", text)
        self.assertIn("docker/setup-buildx-action@v3", text)
        self.assertIn("docker/login-action@v3", text)
        self.assertIn("DOCKER_USERNAME", text)
        self.assertIn("DOCKER_TOKEN", text)


if __name__ == "__main__":
    unittest.main()
