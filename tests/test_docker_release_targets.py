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
        self.assertNotIn("chigusa/bililive-go", text)

    def test_release_workflow_pushes_to_neymar_namespace(self) -> None:
        text = (ROOT / ".github" / "workflows" / "release.yaml").read_text(encoding="utf-8")
        self.assertIn("DOCKER_APP_IMAGE: neymar022/bililive-go-app", text)
        self.assertIn("DOCKER_WORKER_IMAGE: neymar022/bililive-go-subtitle-worker", text)
        self.assertIn("env.DOCKER_APP_IMAGE", text)
        self.assertIn("env.DOCKER_WORKER_IMAGE", text)
        self.assertNotIn("type=image,name=chigusa/bililive-go", text)
        self.assertNotIn("chigusa/bililive-go:$GIT_TAG", text)

    def test_default_compose_pulls_docker_hub_images(self) -> None:
        text = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
        self.assertIn("image: ${BILILIVE_APP_IMAGE:-neymar022/bililive-go-app:latest}", text)
        self.assertIn(
            "image: ${SUBTITLE_WORKER_IMAGE:-neymar022/bililive-go-subtitle-worker:latest}",
            text,
        )
        self.assertNotIn("build:", text)

    def test_build_override_keeps_local_source_build_path(self) -> None:
        text = (ROOT / "docker-compose.build.yml").read_text(encoding="utf-8")
        self.assertIn("context: .", text)
        self.assertIn("dockerfile: Dockerfile.build-and-run", text)
        self.assertIn("context: ./contrib/subtitle-worker", text)
        self.assertIn("dockerfile: Dockerfile", text)

    def test_build_and_run_dockerfile_uses_official_yarn_registry_without_lock_rewrite(self) -> None:
        text = (ROOT / "Dockerfile.build-and-run").read_text(encoding="utf-8")
        self.assertIn("ARG YARN_REGISTRY=https://registry.yarnpkg.com", text)
        self.assertIn('RUN yarn config set registry "$YARN_REGISTRY"', text)
        self.assertNotIn("sed -i", text)
        self.assertNotIn("registry.npmmirror.com", text)

    def test_build_and_run_dockerfile_validates_linux_binary_only(self) -> None:
        text = (ROOT / "Dockerfile.build-and-run").read_text(encoding="utf-8")
        self.assertIn('find /build/bin -maxdepth 1 -type f -name "bililive-linux-*"', text)
        self.assertNotIn('/build/bin/bililive* --version', text)


if __name__ == "__main__":
    unittest.main()
