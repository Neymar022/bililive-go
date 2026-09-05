package recorders

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVerifyRecordedVideoAcceptsDecodedSubsecondClip(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("真实解码测试需要 ffmpeg")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "short.mp4")
	output, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=size=16x16:rate=5", "-t", "0.2", "-c:v", "mpeg4", path).CombinedOutput()
	require.NoError(t, err, "%s", output)
	before, err := os.Stat(path)
	require.NoError(t, err)
	playable, err := VerifyRecordedVideo(ctx, ffmpeg, path)
	require.NoError(t, err)
	require.True(t, playable, "可播放不足一秒的视频也应完成单次，不借用知识时长门槛")
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	require.Equal(t, before.Size(), after.Size())
	require.Equal(t, before.ModTime(), after.ModTime())
}

func TestVerifyRecordedVideoDistinguishesNoVideoFromUnknownResult(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("真实解码测试需要 ffmpeg")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	audio := filepath.Join(root, "audio.m4a")
	output, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "sine=frequency=1000", "-t", "0.2", "-c:a", "aac", audio).CombinedOutput()
	require.NoError(t, err, "%s", output)
	playable, err := VerifyRecordedVideo(ctx, ffmpeg, audio)
	require.NoError(t, err)
	require.False(t, playable)
	empty := filepath.Join(root, "empty.mp4")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	playable, err = VerifyRecordedVideo(ctx, ffmpeg, empty)
	require.NoError(t, err)
	require.False(t, playable)
	_, err = VerifyRecordedVideo(ctx, ffmpeg, filepath.Join(root, "missing.mp4"))
	require.Error(t, err, "缺失文件不能冒充已经证实的空录制")
	_, err = VerifyRecordedVideo(ctx, filepath.Join(root, "missing-ffmpeg"), audio)
	require.Error(t, err, "工具不可用不能冒充没有可用视频")
	cancel()
	_, err = VerifyRecordedVideo(ctx, ffmpeg, audio)
	require.ErrorIs(t, err, context.Canceled)
}
