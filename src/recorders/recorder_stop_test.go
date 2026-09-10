package recorders

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
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

type stopRaceParser struct {
	started  chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
}

type finishingStopParser struct {
	stopRaceParser
	finalizing chan struct{}
	finish     chan struct{}
}

type registrationOnlyStore struct{ *pipeline.SQLiteStore }

// 此处只验收末段登记，避免测试清理阶段与无关的任务执行调度并发。
func (*registrationOnlyStore) GetPendingTasks(context.Context, int) ([]*pipeline.PipelineTask, error) {
	return nil, nil
}

func (p *finishingStopParser) ParseLiveStream(_ context.Context, _ *live.StreamUrlInfo, _ live.Live, path string) error {
	close(p.started)
	<-p.stopped
	close(p.finalizing)
	<-p.finish
	return os.WriteFile(path, []byte("final recording segment"), 0o644)
}

func TestRecorderCloseWaitsForFinalSegmentRegistrationAndSummary(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "restart"}[restart], func(t *testing.T) {
			cfg := configs.NewConfig()
			root := t.TempDir()
			cfg.OutPutPath = root
			cfg.OutputTmpl = "recording.bin"
			cfg.OnRecordFinished.ConvertToMp4 = true
			var notificationStarted, releaseNotification chan struct{}
			var releaseOnce sync.Once
			if !restart {
				notificationStarted, releaseNotification = make(chan struct{}), make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					close(notificationStarted)
					<-releaseNotification
					_, _ = w.Write([]byte(`{"code":200}`))
				}))
				t.Cleanup(server.Close)
				t.Cleanup(func() { releaseOnce.Do(func() { close(releaseNotification) }) })
				cfg.Notify.SendRecordingSummary = true
				cfg.Notify.Bark.Enable = true
				cfg.Notify.Bark.ServerURL = server.URL
				cfg.Notify.Bark.DeviceKey = "test-device"
			}
			previousConfig := configs.GetCurrentConfig()
			configs.SetCurrentConfig(cfg)
			t.Cleanup(func() { configs.SetCurrentConfig(previousConfig) })
			inst := &instance.Instance{Cache: gcache.New(10).Build()}
			ctx := context.WithValue(context.Background(), instance.Key, inst)
			events.NewDispatcher(ctx)
			store, err := pipeline.NewSQLiteStore(filepath.Join(root, "pipeline.db"))
			require.NoError(t, err)
			pm := pipeline.NewManager(ctx, &registrationOnlyStore{store}, nil, nil)
			inst.PipelineManager = pm
			t.Cleanup(func() { pm.Close(ctx) })
			require.NoError(t, pm.OpenRecordingSession("session", "room"))
			ctrl := gomock.NewController(t)
			l := livemock.NewMockLive(ctrl)
			l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
			l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
			l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
			l.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
			u, err := url.Parse("https://example.com/stream.bin")
			require.NoError(t, err)
			l.EXPECT().GetStreamInfos().Return([]*live.StreamUrlInfo{{Url: u}}, nil)
			require.NoError(t, inst.Cache.Set(l, &live.Info{Live: l, HostName: "Host", RoomName: "Room", Status: true}))
			p := &finishingStopParser{
				stopRaceParser: stopRaceParser{started: make(chan struct{}), stopped: make(chan struct{})},
				finalizing:     make(chan struct{}), finish: make(chan struct{}),
			}
			previousParser := newParser
			newParser = func(*url.URL, configs.DownloaderType, map[string]string, *livelogger.LiveLogger) (parser.Parser, error) {
				return p, nil
			}
			t.Cleanup(func() { newParser = previousParser })
			capture, err := NewRecorder(ctx, l)
			require.NoError(t, err)
			require.NoError(t, capture.Start(ctx))
			<-p.started
			require.NoError(t, pm.EndRecordingSession("room", "normal"))
			done := make(chan struct{})
			go func() {
				if restart {
					capture.CloseForRestart()
				} else {
					capture.Close()
				}
				close(done)
			}()
			<-p.finalizing
			session, err := store.RecordingSession(ctx, "session")
			require.NoError(t, err)
			require.False(t, session.Sealed(), "尾段还在写入时不得封口")
			close(p.finish)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				if !restart {
					releaseOnce.Do(func() { close(releaseNotification) })
				}
				t.Fatal("正常停止没有完成尾段收尾")
			}
			session, err = store.RecordingSession(ctx, "session")
			require.NoError(t, err)
			require.True(t, session.Sealed())
			require.Len(t, session.Tasks, 1)
			tasks, err := store.ListTasks(ctx, pipeline.TaskFilter{})
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			require.Equal(t, capture.(*recorder).origin.ProducerID, tasks[0].RecordInfo.RecordingProducerID)
			require.FileExists(t, filepath.Join(root, "recording.bin"))
			require.Equal(t, restart, capture.(*recorder).suppressSummary)
			if !restart {
				select {
				case <-notificationStarted:
				case <-time.After(3 * time.Second):
					t.Fatal("停止后未发送录制摘要")
				}
				releaseOnce.Do(func() { close(releaseNotification) })
			}
			// 通知不阻塞 Close，但读取摘要前须等待通知释放它正在持有的锁。
			capture.(*recorder).recordedFilesMu.Lock()
			recordedFileCount := len(capture.(*recorder).recordedFiles)
			capture.(*recorder).recordedFilesMu.Unlock()
			require.Equal(t, 1, recordedFileCount, "摘要必须包含已登记的最后一段")
		})
	}
}

func (p *stopRaceParser) ParseLiveStream(context.Context, *live.StreamUrlInfo, live.Live, string) error {
	close(p.started)
	<-p.stopped
	return nil
}

func (p *stopRaceParser) Stop() error {
	p.stopOnce.Do(func() { close(p.stopped) })
	return nil
}

func TestRecorderCloseRejectsLateParser(t *testing.T) {
	for _, blockedAt := range []string{"stream_lookup", "parser_creation"} {
		t.Run(blockedAt, func(t *testing.T) {
			cfg := configs.NewConfig()
			cfg.OutPutPath = t.TempDir()
			cfg.OutputTmpl = "recording.bin"
			previousConfig := configs.GetCurrentConfig()
			configs.SetCurrentConfig(cfg)
			t.Cleanup(func() { configs.SetCurrentConfig(previousConfig) })
			inst := &instance.Instance{Cache: gcache.New(10).Build()}
			ctx := context.WithValue(context.Background(), instance.Key, inst)
			events.NewDispatcher(ctx)
			ctrl := gomock.NewController(t)
			l := livemock.NewMockLive(ctrl)
			l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
			l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
			l.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
			entered, release := make(chan struct{}), make(chan struct{})
			streamURL, err := url.Parse("https://example.com/stream.bin")
			require.NoError(t, err)
			l.EXPECT().GetStreamInfos().DoAndReturn(func() ([]*live.StreamUrlInfo, error) {
				if blockedAt == "stream_lookup" {
					close(entered)
					<-release
				}
				return []*live.StreamUrlInfo{{Url: streamURL}}, nil
			})
			require.NoError(t, inst.Cache.Set(l, &live.Info{Live: l, HostName: "Host", RoomName: "Room", Status: true}))
			p := &stopRaceParser{started: make(chan struct{}), stopped: make(chan struct{})}
			previousParser := newParser
			newParser = func(*url.URL, configs.DownloaderType, map[string]string, *livelogger.LiveLogger) (parser.Parser, error) {
				if blockedAt == "parser_creation" {
					close(entered)
					<-release
				}
				return p, nil
			}
			t.Cleanup(func() { newParser = previousParser })
			capture, err := NewRecorder(ctx, l)
			require.NoError(t, err)
			require.NoError(t, capture.Start(ctx))
			<-entered
			done := make(chan struct{})
			go func() { capture.Close(); close(done) }()
			<-capture.(*recorder).stop
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("停止后晚返回的 parser 导致 Close 永久等待")
				_ = p.Stop()
				<-done
			}
			select {
			case <-p.started:
				t.Error("停止后不得调用晚返回 parser 的 ParseLiveStream")
			default:
			}
			require.Nil(t, capture.(*recorder).getParser(), "已停止的 recorder 不应安装新 parser")
		})
	}
}
