package recorders

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/instance"
	"github.com/bililive-go/bililive-go/src/live"
	livemock "github.com/bililive-go/bililive-go/src/live/mock"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pkg/events"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/bililive-go/bililive-go/src/pkg/parser"
	"github.com/bililive-go/bililive-go/src/types"
	"github.com/bluele/gcache"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type panicAfterWriteParser struct{}

func (panicAfterWriteParser) ParseLiveStream(_ context.Context, _ *live.StreamUrlInfo, _ live.Live, path string) error {
	if err := os.WriteFile(path, []byte("unverified recording tail"), 0o644); err != nil {
		return err
	}
	panic("parser failed after creating output")
}
func (panicAfterWriteParser) Stop() error { return nil }

func TestUnverifiedParserPanicTailCannotBecomeProvenEmpty(t *testing.T) {
	root := t.TempDir()
	cfg := configs.NewConfig()
	cfg.OutPutPath = filepath.Join(root, "srt_video")
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = filepath.Join(root, "video")
	cfg.OutputTmpl = "raw.flv"
	require.NoError(t, os.Mkdir(cfg.Subtitle.LibraryRoot, 0o755))
	configs.SetCurrentConfig(cfg)
	inst := &instance.Instance{Cache: gcache.New(10).Build()}
	ctx := context.WithValue(context.Background(), instance.Key, inst)
	events.NewDispatcher(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	pm := pipeline.NewManager(ctx, store, nil, nil)
	inst.PipelineManager = pm
	defer pm.Close(ctx)
	_, err = pm.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	require.NoError(t, pm.OpenRecordingSession("session", "room"))
	ctrl := gomock.NewController(t)
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
	l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	l.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
	l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	streamURL, err := url.Parse("https://example.com/stream.bin")
	require.NoError(t, err)
	l.EXPECT().GetStreamInfos().Return([]*live.StreamUrlInfo{{Url: streamURL}}, nil).AnyTimes()
	require.NoError(t, inst.Cache.Set(l, &live.Info{Live: l, HostName: "Host", RoomName: "Room", Status: true}))
	previous := newParser
	newParser = func(_ *url.URL, _ configs.DownloaderType, _ map[string]string, _ *livelogger.LiveLogger) (parser.Parser, error) {
		return panicAfterWriteParser{}, nil
	}
	defer func() { newParser = previous }()
	r, err := NewRecorder(ctx, l)
	require.NoError(t, err)
	require.NoError(t, r.Start(ctx))
	defer r.Close()
	select {
	case <-r.(*recorder).done:
	case <-time.After(time.Second):
		t.Fatal("parser panic did not close its producer")
	}
	require.NoError(t, pm.EndRecordingSession("room", "normal"))
	run, err := pm.RecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, pipeline.RecordingRunPaused, run.State, "parser left unverified bytes but default empty evidence rearmed once")
}

type interruptedRecordingParser struct {
	output  chan string
	content []byte
}

func (p interruptedRecordingParser) ParseLiveStream(_ context.Context, _ *live.StreamUrlInfo, _ live.Live, file string) error {
	content := p.content
	if content == nil {
		content = []byte("unfinished recording")
	}
	if err := os.WriteFile(file, content, 0o644); err != nil {
		return err
	}
	p.output <- file
	return errors.New("stream interrupted after data was written")
}

func (p interruptedRecordingParser) Stop() error { return nil }

func TestRecorderFailedRegistrationCanCloseWithoutWaitingForUnstartedThread(t *testing.T) {
	ctrl := gomock.NewController(t)
	ctx := context.WithValue(context.Background(), instance.Key, &instance.Instance{})
	events.NewDispatcher(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	manager := pipeline.NewManager(ctx, store, nil, nil)
	instance.GetInstance(ctx).PipelineManager = manager
	t.Cleanup(func() { manager.Close(ctx) })
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetLiveId().Return(types.LiveID("unregistered")).AnyTimes()
	r, err := NewRecorder(ctx, l)
	require.NoError(t, err)
	require.Error(t, r.Start(ctx))
	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Error("Start 登记失败不应留下永久等待的 Close")
		close(r.(*recorder).done)
		<-done
	}
}

func TestRecorderDoesNotSealSessionWhenFailedParserLeftUnregisteredVideo(t *testing.T) {
	ctrl := gomock.NewController(t)
	root := t.TempDir()
	cfg := configs.NewConfig()
	cfg.OutPutPath = filepath.Join(root, "srt_video")
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = filepath.Join(root, "video")
	require.NoError(t, os.Mkdir(cfg.Subtitle.LibraryRoot, 0o755))
	cfg.OutputTmpl = "raw.flv"
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous) })
	inst := &instance.Instance{Cache: gcache.New(10).Build()}
	ctx := context.WithValue(context.Background(), instance.Key, inst)
	events.NewDispatcher(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	manager := pipeline.NewManager(ctx, store, nil, nil)
	inst.PipelineManager = manager
	t.Cleanup(func() { manager.Close(ctx) })
	require.NoError(t, manager.OpenRecordingSession("99", "room"))
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
	l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	l.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
	l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	streamURL, err := url.Parse("https://example.com/stream.bin")
	require.NoError(t, err)
	l.EXPECT().GetStreamInfos().Return([]*live.StreamUrlInfo{{Url: streamURL}}, nil).AnyTimes()
	require.NoError(t, inst.Cache.Set(l, &live.Info{Live: l, HostName: "Host", RoomName: "Room", Status: true}))
	output := make(chan string, 1)
	originalParser := newParser
	newParser = func(_ *url.URL, _ configs.DownloaderType, _ map[string]string, _ *livelogger.LiveLogger) (parser.Parser, error) {
		return interruptedRecordingParser{output: output}, nil
	}
	t.Cleanup(func() { newParser = originalParser })
	r, err := NewRecorder(ctx, l)
	require.NoError(t, err)
	require.NoError(t, r.Start(ctx))
	t.Cleanup(r.Close)
	var path string
	select {
	case path = <-output:
	case <-time.After(time.Second):
		t.Fatal("recorder did not produce its final segment")
	}
	require.NoError(t, manager.EndRecordingSession("room", "normal"))
	r.Close()
	session, err := store.RecordingSession(ctx, "99")
	require.NoError(t, err)
	require.False(t, session.Sealed(), "未登记的非空尾段必须阻止整场发布")
	require.NotEmpty(t, session.Blocked)
	require.FileExists(t, path)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	rel, err := filepath.Rel(filepath.Join(resolvedRoot, ".live_session_segments"), path)
	require.NoError(t, err)
	require.False(t, strings.HasPrefix(rel, ".."), "新录制不能进入旧 organizer 的源目录")
}

func TestRecorderConsumesOnceFromPlayableCaptureDespiteParserError(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("真实录制产物判定需要 ffmpeg")
	}
	ctrl := gomock.NewController(t)
	root := t.TempDir()
	sample := filepath.Join(root, "sample.mp4")
	buildCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(buildCtx, ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=size=16x16:rate=5", "-t", "0.2", "-c:v", "mpeg4", sample).CombinedOutput()
	require.NoError(t, err, "%s", output)
	content, err := os.ReadFile(sample)
	require.NoError(t, err)
	cfg := configs.NewConfig()
	cfg.OutPutPath = filepath.Join(root, "srt_video")
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = filepath.Join(root, "video")
	cfg.FfmpegPath = ffmpeg
	cfg.OutputTmpl = "raw.flv"
	require.NoError(t, os.Mkdir(cfg.Subtitle.LibraryRoot, 0o755))
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous) })
	inst := &instance.Instance{Cache: gcache.New(10).Build()}
	ctx := context.WithValue(context.Background(), instance.Key, inst)
	events.NewDispatcher(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	manager := pipeline.NewManager(ctx, store, nil, nil)
	inst.PipelineManager = manager
	t.Cleanup(func() { manager.Close(ctx) })
	_, err = store.EnsureRecordingRun(ctx, "room", configs.RecordingModeOnce)
	require.NoError(t, err)
	require.NoError(t, manager.OpenRecordingSession("once-session", "room"))
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
	l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	l.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
	l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	streamURL, err := url.Parse("https://example.com/stream.bin")
	require.NoError(t, err)
	l.EXPECT().GetStreamInfos().Return([]*live.StreamUrlInfo{{Url: streamURL}}, nil).AnyTimes()
	require.NoError(t, inst.Cache.Set(l, &live.Info{Live: l, HostName: "Host", RoomName: "Room", Status: true}))
	written := make(chan string, 1)
	originalParser := newParser
	newParser = func(_ *url.URL, _ configs.DownloaderType, _ map[string]string, _ *livelogger.LiveLogger) (parser.Parser, error) {
		return interruptedRecordingParser{output: written, content: content}, nil
	}
	t.Cleanup(func() { newParser = originalParser })
	r, err := NewRecorder(ctx, l)
	require.NoError(t, err)
	require.NoError(t, r.Start(ctx))
	t.Cleanup(r.Close)
	var path string
	select {
	case path = <-written:
	case <-time.After(3 * time.Second):
		t.Fatal("recorder did not write its capture")
	}
	require.NoError(t, manager.EndRecordingSession("room", "normal"))
	r.Close()
	run, err := store.GetRecordingRun(ctx, "room")
	require.NoError(t, err)
	require.Equal(t, pipeline.RecordingRunCompleted, run.State)
	session, err := store.RecordingSession(ctx, "once-session")
	require.NoError(t, err)
	require.False(t, session.Sealed(), "录制额度成功不能消除发布输入不完整的问题")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, after)
}
