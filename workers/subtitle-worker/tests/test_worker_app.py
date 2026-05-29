import pathlib
import sys
import unittest
from unittest import mock

from fastapi import HTTPException


ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from app import ProcessRequest, app, process
from worker_core import WorkerRetryLater, WorkerSafeError


class WorkerAppRoutesTest(unittest.TestCase):
    def test_style_lab_routes_are_removed(self):
        routes = {route.path for route in app.routes}

        self.assertNotIn("/api/v1/style-lab/preview", routes)
        self.assertNotIn("/api/v1/style-lab/sample", routes)

    def test_process_reads_video_encoder_settings_from_environment(self):
        request = ProcessRequest(
            source_path="/tmp/source.mp4",
            output_video_path="/tmp/output.mp4",
            output_srt_path="/tmp/output.srt",
            provider="local-whisper",
        )

        with mock.patch.dict(
            "os.environ",
            {
                "SUBTITLE_VIDEO_ENCODER": "vaapi",
                "SUBTITLE_VAAPI_DEVICE": "/dev/dri/renderD128",
                "SUBTITLE_VAAPI_QP": "19",
            },
            clear=False,
        ), mock.patch("app.transcribe_and_burn", return_value={"segments": []}) as worker_mock:
            process(request)

        kwargs = worker_mock.call_args.kwargs
        self.assertEqual("vaapi", kwargs["video_encoder"])
        self.assertEqual("/dev/dri/renderD128", kwargs["vaapi_device"])
        self.assertEqual(19, kwargs["vaapi_qp"])

    def test_process_passes_cloud_asr_policy_from_environment(self):
        request = ProcessRequest(
            source_path="/tmp/source.mp4",
            output_video_path="/tmp/output.mp4",
            output_srt_path="/tmp/output.srt",
            provider="auto",
        )

        with mock.patch.dict(
            "os.environ",
            {
                "SUBTITLE_PROVIDER_CHAIN": "remote-mac-mlx,dashscope",
                "SUBTITLE_ALLOW_CLOUD_ASR": "false",
            },
            clear=False,
        ), mock.patch("app.transcribe_and_burn", return_value={"segments": []}) as worker_mock:
            process(request)

        kwargs = worker_mock.call_args.kwargs
        self.assertFalse(kwargs["allow_cloud_asr"])


    def _make_request(self) -> ProcessRequest:
        return ProcessRequest(
            source_path="/tmp/source.mp4",
            output_video_path="/tmp/output.mp4",
            output_srt_path="/tmp/output.srt",
            provider="local-whisper",
        )

    def test_safe_errors_expose_message_to_client(self):
        # WorkerSafeError 的 message 已被开发者审核为安全（如缺少 KEY、不支持 provider）。
        request = self._make_request()
        with mock.patch(
            "app.transcribe_and_burn",
            side_effect=WorkerSafeError("缺少 DASHSCOPE_API_KEY"),
        ):
            with self.assertRaises(HTTPException) as ctx:
                process(request)

        self.assertEqual(500, ctx.exception.status_code)
        self.assertEqual("缺少 DASHSCOPE_API_KEY", ctx.exception.detail)

    def test_retry_later_errors_return_503_with_code(self):
        request = self._make_request()
        with mock.patch(
            "app.transcribe_and_burn",
            side_effect=WorkerRetryLater(
                "mac_transcriber_unavailable",
                "mac_transcriber_unavailable: Mac 转写服务不可用，等待恢复后重试",
            ),
        ):
            with self.assertRaises(HTTPException) as ctx:
                process(request)

        self.assertEqual(503, ctx.exception.status_code)
        self.assertEqual(
            {
                "code": "mac_transcriber_unavailable",
                "message": "mac_transcriber_unavailable: Mac 转写服务不可用，等待恢复后重试",
            },
            ctx.exception.detail,
        )

    def test_value_errors_return_400_with_message(self):
        request = self._make_request()
        with mock.patch(
            "app.transcribe_and_burn",
            side_effect=ValueError("不支持的视频编码模式: foo"),
        ):
            with self.assertRaises(HTTPException) as ctx:
                process(request)

        self.assertEqual(400, ctx.exception.status_code)
        self.assertIn("不支持的视频编码模式", ctx.exception.detail)

    def test_unknown_errors_return_generic_detail_no_leak(self):
        # 关键安全契约：未知异常 message 可能含路径/key/stderr——不能直接回客户端。
        sensitive_message = (
            "ffmpeg burn failed: /srv/bililive/secret-path.mp4: "
            "Bearer eyJhbGciOiJIUzI1NiJ9.fake.token"
        )
        request = self._make_request()
        with mock.patch(
            "app.transcribe_and_burn",
            side_effect=RuntimeError(sensitive_message),
        ):
            with self.assertRaises(HTTPException) as ctx:
                process(request)

        self.assertEqual(500, ctx.exception.status_code)
        self.assertNotIn("Bearer", ctx.exception.detail)
        self.assertNotIn("/srv/bililive", ctx.exception.detail)
        self.assertNotIn("ffmpeg", ctx.exception.detail)
        self.assertEqual("internal worker error; check worker logs", ctx.exception.detail)


if __name__ == "__main__":
    unittest.main()
