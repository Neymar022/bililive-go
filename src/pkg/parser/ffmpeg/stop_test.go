package ffmpeg

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/live"
	livemock "github.com/bililive-go/bililive-go/src/live/mock"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMain(m *testing.M) {
	if os.Getenv("BILILIVE_TEST_FFMPEG_STOP_HELPER") == "1" {
		path := os.Args[len(os.Args)-1]
		if os.WriteFile(path+".started", nil, 0o644) != nil {
			os.Exit(2)
		}
		b := make([]byte, 1)
		if _, err := io.ReadFull(os.Stdin, b); err != nil || b[0] != 'q' {
			os.Exit(3)
		}
		if os.WriteFile(path, []byte("final recording segment"), 0o644) != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStopBeforeParseDoesNotStartFFmpeg(t *testing.T) {
	p, err := (&builder{}).Build(nil, livelogger.New(1024, nil))
	require.NoError(t, err)
	require.NoError(t, p.Stop())
	require.NoError(t, p.Stop())
	require.NotPanics(t, func() {
		require.NoError(t, p.ParseLiveStream(context.Background(), nil, nil, ""))
	})
	require.Zero(t, p.(*Parser).GetPID())
}

func TestStopDuringFFmpegPreparationPreventsProcessStart(t *testing.T) {
	cfg := configs.NewConfig()
	executable, err := os.Executable()
	require.NoError(t, err)
	cfg.FfmpegPath = executable
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous) })
	p, err := (&builder{}).Build(nil, livelogger.New(1024, nil))
	require.NoError(t, err)
	ctrl := gomock.NewController(t)
	l := livemock.NewMockLive(ctrl)
	entered, release := make(chan struct{}), make(chan struct{})
	l.EXPECT().GetRawUrl().DoAndReturn(func() string {
		close(entered)
		<-release
		return "https://example.com/room"
	})
	l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
	u, err := url.Parse("https://example.com/stream.bin")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- p.ParseLiveStream(context.Background(), &live.StreamUrlInfo{Url: u}, l, "") }()
	<-entered
	require.NoError(t, p.Stop())
	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("停止后启动了无法收尾的 FFmpeg")
	}
	require.Nil(t, p.(*Parser).cmd, "Stop 必须与创建及启动子进程串行化")
}

func TestFFmpegStopWaitsForFinalOutput(t *testing.T) {
	t.Setenv("BILILIVE_TEST_FFMPEG_STOP_HELPER", "1")
	cfg := configs.NewConfig()
	executable, err := os.Executable()
	require.NoError(t, err)
	cfg.FfmpegPath = executable
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous) })
	p, err := (&builder{}).Build(nil, livelogger.New(1024, nil))
	require.NoError(t, err)
	ctrl := gomock.NewController(t)
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
	u, err := url.Parse("https://example.com/stream.bin")
	require.NoError(t, err)
	output := filepath.Join(t.TempDir(), "recording.bin")
	done := make(chan error, 1)
	go func() { done <- p.ParseLiveStream(context.Background(), &live.StreamUrlInfo{Url: u}, l, output) }()
	t.Cleanup(func() {
		p.(*Parser).cmdLock.Lock()
		defer p.(*Parser).cmdLock.Unlock()
		if p.(*Parser).cmd != nil && p.(*Parser).cmd.Process != nil {
			_ = p.(*Parser).cmd.Process.Kill()
		}
	})
	require.Eventually(t, func() bool { _, err := os.Stat(output + ".started"); return err == nil }, 3*time.Second, time.Millisecond)
	require.NoError(t, p.Stop())
	require.NoError(t, p.Stop())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("FFmpeg 没有完成正常停止")
	}
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "final recording segment", string(content))
}
