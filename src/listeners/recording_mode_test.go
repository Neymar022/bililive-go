package listeners

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/instance"
	"github.com/bililive-go/bililive-go/src/live"
	livemock "github.com/bililive-go/bililive-go/src/live/mock"
	_ "github.com/bililive-go/bililive-go/src/live/system"
	"github.com/bililive-go/bililive-go/src/log"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pkg/events"
	evtmock "github.com/bililive-go/bililive-go/src/pkg/events/mock"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/bililive-go/bililive-go/src/types"
	"github.com/bluele/gcache"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRealInitializationReplacementStartsOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inst := &instance.Instance{Cache: gcache.New(10).Build()}
	ctx = context.WithValue(ctx, instance.Key, inst)
	inst.Ctx = ctx
	ed := events.NewDispatcher(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	pm := pipeline.NewManager(ctx, store, nil, ed)
	inst.PipelineManager = pm
	cfg := configs.NewConfig()
	room := configs.LiveRoom{Url: "https://replacement-spec.test/room", IsListening: true, RecordingMode: configs.RecordingModeOnce}
	cfg.LiveRooms = []configs.LiveRoom{room}
	cfg.RefreshLiveRoomIndexCache()
	configs.SetCurrentConfig(cfg)
	log.New(ctx)
	ctrl := gomock.NewController(t)
	original := livemock.NewMockLive(ctrl)
	original.EXPECT().GetLiveId().Return(types.LiveID("real")).AnyTimes()
	original.EXPECT().GetRawUrl().Return(room.Url).AnyTimes()
	original.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
	original.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	original.EXPECT().UpdateLiveOptionsbyConfig(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	original.EXPECT().SetLastStartTime(gomock.Any()).AnyTimes()
	original.EXPECT().GetInfo().Return(&live.Info{Live: original, Status: true, HostName: "host", RoomName: "room"}, nil).AnyTimes()
	live.Register("replacement-spec.test", recordingLiveBuilder{original})
	lm := NewManager(ctx)
	require.NoError(t, lm.Start(ctx))
	defer lm.Close(ctx)
	started := make(chan string, 4)
	ed.AddEventListener(LiveStart, events.NewEventListener(func(event *events.Event) { started <- string(event.Object.(live.Live).GetLiveId()) }))
	initializing, err := live.NewInitializing(ctx, &room, inst.Cache, func(before live.Live, after live.Live, info *live.Info) {
		ed.DispatchEvent(events.NewEvent(RoomInitializingFinished, live.InitializingFinishedParam{InitializingLive: before, Live: after, Info: info}))
	})
	require.NoError(t, err)
	inst.Lives.Set(initializing.GetLiveId(), initializing)
	require.NoError(t, lm.AddListener(ctx, initializing))
	select {
	case id := <-started:
		require.Equal(t, "real", id)
	case <-ctx.Done():
		t.Fatal("real identity never started")
	}
	require.NoError(t, events.WaitForOrderedEvents(ctx, ed, "real"))
	require.Len(t, started, 0)
	require.True(t, lm.HasListener(ctx, "real"))
}

type recordingLiveBuilder struct{ live.Live }

func TestFinalizingListenerWaitsForEvidenceWithoutLosingNextLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inst := &instance.Instance{}
	ctx = context.WithValue(ctx, instance.Key, inst)
	ed := events.NewDispatcher(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	pm := pipeline.NewManager(ctx, store, nil, ed)
	inst.PipelineManager = pm
	cfg := configs.NewConfig()
	cfg.LiveRooms = []configs.LiveRoom{{Url: "https://example.com/room", IsListening: true, RecordingMode: configs.RecordingModeOnce}}
	configs.SetCurrentConfig(cfg)
	log.New(ctx)
	_, err = pm.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	require.NoError(t, pm.OpenRecordingSession("first", "room"))
	origin, err := pm.BeginRecordingProducer("room")
	require.NoError(t, err)
	require.NoError(t, pm.EndRecordingSession("room", "normal"))
	ctrl := gomock.NewController(t)
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
	l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	l.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
	l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	l.EXPECT().SetLastStartTime(gomock.Any()).AnyTimes()
	info := &live.Info{Live: l, Status: true}
	l.EXPECT().GetInfo().Return(info, nil).AnyTimes()
	updates := make(chan *live.Info, 1)
	l.EXPECT().GetInfoWithInterval(gomock.Any()).DoAndReturn(func(ctx context.Context) (*live.Info, error) {
		select {
		case info := <-updates:
			return info, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}).AnyTimes()
	starts := make(chan bool, 2)
	ed.AddEventListener(LiveStart, events.NewEventListener(func(*events.Event) { starts <- true }))
	monitor := NewListener(ctx, l)
	require.NoError(t, monitor.Start())
	defer monitor.Close()
	require.NoError(t, events.WaitForOrderedEvents(ctx, ed, "room"))
	require.Empty(t, starts, "收尾尚未完成时不能开启新录制")
	require.NoError(t, pm.RecordCaptureEvidence(origin, false, ""))
	require.NoError(t, pm.FinishRecordingProducer(origin, ""))
	updates <- info
	select {
	case <-starts:
	case <-ctx.Done():
		t.Fatal("空场确认后漏掉下一次开播")
	}
}

func (b recordingLiveBuilder) Build(*url.URL) (live.Live, error) { return b.Live, nil }

func TestInitializingListenerCannotStartBeforeRealIdentityIsBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inst := &instance.Instance{Cache: gcache.New(10).Build()}
	ctx = context.WithValue(ctx, instance.Key, inst)
	inst.Ctx = ctx
	ed := events.NewDispatcher(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	_, err = store.EnsureRecordingRun(ctx, "real-completed", configs.RecordingModeOnce)
	require.NoError(t, err)
	require.NoError(t, store.OpenRecordingSession(ctx, "old-session", "real-completed"))
	origin, err := store.BeginRecordingProducer(ctx, "real-completed")
	require.NoError(t, err)
	require.NoError(t, store.RecordCaptureEvidence(ctx, origin, true, ""))
	require.NoError(t, store.EndRecordingSession(ctx, "real-completed", "normal"))
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	pm := pipeline.NewManager(ctx, store, nil, ed)
	inst.PipelineManager = pm
	cfg := configs.NewConfig()
	room := configs.LiveRoom{Url: "https://initializing-recording.test/room", IsListening: true, RecordingMode: configs.RecordingModeOnce}
	cfg.LiveRooms = []configs.LiveRoom{room}
	cfg.RefreshLiveRoomIndexCache()
	configs.SetCurrentConfig(cfg)
	log.New(ctx)
	ctrl := gomock.NewController(t)
	original := livemock.NewMockLive(ctrl)
	original.EXPECT().GetLiveId().Return(types.LiveID("real-completed")).AnyTimes()
	original.EXPECT().GetRawUrl().Return(room.Url).AnyTimes()
	original.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
	original.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	original.EXPECT().UpdateLiveOptionsbyConfig(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	original.EXPECT().GetInfo().Return(&live.Info{Live: original, Status: true, HostName: "host", RoomName: "room"}, nil).AnyTimes()
	live.Register("initializing-recording.test", recordingLiveBuilder{original})
	releaseInit := make(chan struct{})
	defer close(releaseInit)
	ed.AddEventListener(RoomInitializingFinished, events.NewEventListener(func(*events.Event) { <-releaseInit }))
	started := make(chan string, 1)
	ed.AddEventListener(LiveStart, events.NewEventListener(func(event *events.Event) { started <- string(event.Object.(live.Live).GetLiveId()) }))
	initializing, err := live.NewInitializing(ctx, &room, inst.Cache, func(before live.Live, after live.Live, info *live.Info) {
		ed.DispatchEvent(events.NewEvent(RoomInitializingFinished, live.InitializingFinishedParam{InitializingLive: before, Live: after, Info: info}))
	})
	require.NoError(t, err)
	require.NotEqual(t, "real-completed", string(initializing.GetLiveId()))
	listener := NewListener(ctx, initializing)
	require.NoError(t, listener.Start())
	defer listener.Close()
	require.NoError(t, events.WaitForOrderedEvents(ctx, ed, string(initializing.GetLiveId())))
	select {
	case id := <-started:
		t.Fatalf("占位身份绕过已完成额度并触发录制: %s", id)
	default:
	}
	_, err = pm.RecordingRun(string(initializing.GetLiveId()))
	require.Error(t, err, "占位身份不得创建独立录制额度")
}

func TestListenerDoesNotRestartCompletedOnceAfterProcessRestart(t *testing.T) {
	ctx := context.Background()
	store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	_, err = store.EnsureRecordingRun(ctx, "room", configs.RecordingModeOnce)
	require.NoError(t, err)
	require.NoError(t, store.OpenRecordingSession(ctx, "finished-session", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	require.NoError(t, store.RecordCaptureEvidence(ctx, origin, true, ""))
	require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))

	ctrl := gomock.NewController(t)
	ed := evtmock.NewMockDispatcher(ctrl)
	pm := pipeline.NewManager(ctx, store, nil, ed)
	ctx = context.WithValue(ctx, instance.Key, &instance.Instance{EventDispatcher: ed, PipelineManager: pm})
	cfg := configs.NewConfig()
	cfg.LiveRooms = []configs.LiveRoom{{Url: "https://example.com/room", IsListening: true, RecordingMode: configs.RecordingModeOnce}}
	configs.SetCurrentConfig(cfg)
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
	l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	// 已完成的单次不得请求平台或派发 ListenStart/LiveStart，也不改变用户配置。
	listener := NewListener(ctx, l)
	require.NoError(t, listener.Start())
	listener.Close()
	actual, err := pm.RecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, pipeline.RecordingRunCompleted, actual.State)
	require.True(t, configs.GetCurrentConfig().LiveRooms[0].IsListening)
}
