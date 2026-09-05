package servers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/instance"
	"github.com/bililive-go/bililive-go/src/listeners"
	"github.com/bililive-go/bililive-go/src/live"
	livemock "github.com/bililive-go/bililive-go/src/live/mock"
	_ "github.com/bililive-go/bililive-go/src/live/system"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pkg/events"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/bililive-go/bililive-go/src/recorders"
	"github.com/bililive-go/bililive-go/src/types"
	"github.com/bluele/gcache"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestURLAndRecordingControlsCannotChangeIdentityInOneSave(t *testing.T) {
	ctx := context.Background()
	store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	pm := pipeline.NewManager(ctx, store, nil, nil)
	_, err = pm.ConfigureRecordingRun("room", configs.RecordingModeContinuous)
	require.NoError(t, err)
	require.NoError(t, pm.OpenRecordingSession("session", "room"))
	inst := &instance.Instance{PipelineManager: pm}
	ctx = context.WithValue(ctx, instance.Key, inst)
	inst.Ctx = ctx
	ctrl := gomock.NewController(t)
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
	l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	inst.Lives.Set("room", l)
	cfg := configs.NewConfig()
	cfg.File = filepath.Join(t.TempDir(), "config.yml")
	cfg.LiveRooms = []configs.LiveRoom{{Url: "https://example.com/room", LiveId: "room", IsListening: true}}
	configs.SetCurrentConfig(cfg)
	req := mux.SetURLVars(httptest.NewRequest(http.MethodPatch, "/api/config/rooms/id/room", strings.NewReader(`{"url":"https://example.com/new-room","recording_mode":"once","is_listening":false}`)).WithContext(ctx), map[string]string{"id": "room"})
	resp := httptest.NewRecorder()
	updateRoomConfigById(resp, req)
	require.Equal(t, http.StatusBadRequest, resp.Code, resp.Body.String())
	actual, err := pm.RecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, "https://example.com/room", configs.GetCurrentConfig().LiveRooms[0].Url)
	require.Equal(t, configs.RecordingModeContinuous, actual.Mode, "failed stop changed durable run mode while restoring only config mode")
}

func TestSpecSettingsEntrypointsStartAndStopTheirRecordingRun(t *testing.T) {
	for _, byID := range []bool{true, false} {
		t.Run(map[bool]string{true: "id", false: "url"}[byID], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inst := &instance.Instance{}
			ctx = context.WithValue(ctx, instance.Key, inst)
			inst.Ctx = ctx
			ed := events.NewDispatcher(ctx)
			lm := listeners.NewManager(ctx)
			recorders.NewManager(ctx)
			store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
			require.NoError(t, err)
			defer store.Close()
			pm := pipeline.NewManager(ctx, store, nil, ed)
			inst.PipelineManager = pm
			_, err = pm.ConfigureRecordingRun("room", configs.RecordingModeOnce)
			require.NoError(t, err)
			_, err = pm.StopRecordingRun("room")
			require.NoError(t, err)
			cfg := configs.NewConfig()
			cfg.File = filepath.Join(t.TempDir(), "config.yml")
			cfg.LiveRooms = []configs.LiveRoom{{Url: "https://example.com/room", LiveId: "room", IsListening: false, RecordingMode: configs.RecordingModeOnce}}
			configs.SetCurrentConfig(cfg)
			ctrl := gomock.NewController(t)
			l := livemock.NewMockLive(ctrl)
			l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
			l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
			l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
			l.EXPECT().GetInfo().Return(&live.Info{Live: l, Status: false}, nil).AnyTimes()
			l.EXPECT().GetInfoWithInterval(gomock.Any()).DoAndReturn(func(ctx context.Context) (*live.Info, error) { <-ctx.Done(); return nil, ctx.Err() }).AnyTimes()
			inst.Lives.Set("room", l)
			for _, enabled := range []bool{true, false} {
				payload := `{"is_listening":false}`
				if enabled {
					payload = `{"is_listening":true}`
				}
				req := mux.SetURLVars(httptest.NewRequest(http.MethodPatch, "/api/config/rooms/id/room", strings.NewReader(payload)).WithContext(ctx), map[string]string{"id": "room", "url": "https://example.com/room"})
				resp := httptest.NewRecorder()
				if byID {
					updateRoomConfigById(resp, req)
				} else {
					updateRoomConfig(resp, req)
				}
				require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
				require.Equal(t, enabled, lm.HasListener(ctx, "room"))
				run, err := pm.RecordingRun("room")
				require.NoError(t, err)
				if enabled {
					require.Equal(t, pipeline.RecordingRunWaiting, run.State)
				} else {
					require.Equal(t, pipeline.RecordingRunStopped, run.State)
				}
			}
		})
	}
	_ = time.Second
}

func TestSettingsEntrypointsStartAndStopTheirRecordingRun(t *testing.T) {
	for _, byID := range []bool{true, false} {
		t.Run(map[bool]string{true: "id", false: "url"}[byID], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inst := &instance.Instance{}
			ctx = context.WithValue(ctx, instance.Key, inst)
			inst.Ctx = ctx
			ed := events.NewDispatcher(ctx)
			lm := listeners.NewManager(ctx)
			recorders.NewManager(ctx)
			store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
			require.NoError(t, err)
			defer store.Close()
			pm := pipeline.NewManager(ctx, store, nil, ed)
			inst.PipelineManager = pm
			_, err = pm.ConfigureRecordingRun("room", configs.RecordingModeOnce)
			require.NoError(t, err)
			_, err = pm.StopRecordingRun("room")
			require.NoError(t, err)
			cfg := configs.NewConfig()
			cfg.File = filepath.Join(t.TempDir(), "config.yml")
			cfg.LiveRooms = []configs.LiveRoom{{Url: "https://example.com/room", LiveId: "room", IsListening: false, RecordingMode: configs.RecordingModeOnce}}
			configs.SetCurrentConfig(cfg)
			ctrl := gomock.NewController(t)
			l := livemock.NewMockLive(ctrl)
			l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
			l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
			l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
			l.EXPECT().GetInfo().Return(&live.Info{Live: l, Status: false}, nil).AnyTimes()
			l.EXPECT().GetInfoWithInterval(gomock.Any()).DoAndReturn(func(ctx context.Context) (*live.Info, error) { <-ctx.Done(); return nil, ctx.Err() }).AnyTimes()
			inst.Lives.Set("room", l)
			for _, enabled := range []bool{true, false} {
				payload := `{"is_listening":false}`
				if enabled {
					payload = `{"is_listening":true}`
				}
				req := mux.SetURLVars(httptest.NewRequest(http.MethodPatch, "/api/config/rooms/id/room", strings.NewReader(payload)).WithContext(ctx), map[string]string{"id": "room", "url": "https://example.com/room"})
				resp := httptest.NewRecorder()
				if byID {
					updateRoomConfigById(resp, req)
				} else {
					updateRoomConfig(resp, req)
				}
				require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
				require.Equal(t, enabled, lm.HasListener(ctx, "room"))
				run, err := pm.RecordingRun("room")
				require.NoError(t, err)
				if enabled {
					require.Equal(t, pipeline.RecordingRunWaiting, run.State)
				} else {
					require.Equal(t, pipeline.RecordingRunStopped, run.State)
				}
			}
		})
	}
}

type recordingModeLiveBuilder struct{ live.Live }

func (b recordingModeLiveBuilder) Build(*url.URL) (live.Live, error) { return b.Live, nil }

func TestAddLivesRecordsRequestedModeAndKeepsLegacyDefault(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        configs.RecordingMode
	}{
		{"once", `,"recording_mode":"once"`, configs.RecordingModeOnce},
		{"continuous", `,"recording_mode":"continuous"`, configs.RecordingModeContinuous},
		{"legacy", "", configs.RecordingModeContinuous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			inst := &instance.Instance{Cache: gcache.New(4).LRU().Build()}
			ctx = context.WithValue(ctx, instance.Key, inst)
			inst.Ctx = ctx
			listeners.NewManager(ctx)
			recorders.NewManager(ctx)
			store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
			require.NoError(t, err)
			t.Cleanup(func() { store.Close() })
			inst.PipelineManager = pipeline.NewManager(ctx, store, nil, nil)
			cfg := configs.NewConfig()
			cfg.File = filepath.Join(t.TempDir(), "config.yml")
			configs.SetCurrentConfig(cfg)
			ctrl := gomock.NewController(t)
			l := livemock.NewMockLive(ctrl)
			l.EXPECT().GetRawUrl().Return("https://recording-mode.test/room").AnyTimes()
			l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
			l.EXPECT().GetInfo().Return(&live.Info{Live: l, HostName: "test", RoomName: "room"}, nil).AnyTimes()
			l.EXPECT().GetOptions().Return(&live.Options{}).AnyTimes()
			l.EXPECT().GetLastStartTime().Return(time.Time{}).AnyTimes()
			l.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
			l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
			l.EXPECT().UpdateLiveOptionsbyConfig(gomock.Any(), gomock.Any()).AnyTimes()
			live.Register("recording-mode.test", recordingModeLiveBuilder{l})
			req := httptest.NewRequest(http.MethodPost, "/api/lives", strings.NewReader(`[{"url":"https://recording-mode.test/room","listen":false`+tc.field+`}]`)).WithContext(ctx)
			resp := httptest.NewRecorder()
			addLives(resp, req)
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			var result []struct {
				Mode  configs.RecordingMode `json:"recording_mode"`
				State string                `json:"recording_state"`
			}
			require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &result))
			require.Len(t, result, 1)
			require.Equal(t, tc.want, result[0].Mode)
			require.Equal(t, "stopped", result[0].State)
			require.Equal(t, tc.want, configs.GetCurrentConfig().LiveRooms[0].EffectiveRecordingMode())
		})
	}
}

func TestAddLivesRejectsInvalidRecordingModeBeforeAddingRooms(t *testing.T) {
	for _, field := range []string{`"repeat"`, `null`, `true`, `""`} {
		inst := &instance.Instance{}
		ctx := context.WithValue(context.Background(), instance.Key, inst)
		inst.Ctx = ctx
		configs.SetCurrentConfig(configs.NewConfig())
		req := httptest.NewRequest(http.MethodPost, "/api/lives", strings.NewReader(`[{"url":"https://invalid.test/room","recording_mode":`+field+`}]`)).WithContext(ctx)
		resp := httptest.NewRecorder()
		addLives(resp, req)
		require.Equal(t, http.StatusBadRequest, resp.Code)
		require.Empty(t, configs.GetCurrentConfig().LiveRooms)
	}
}

func TestAddingDeletedRoomWithListenTrueStartsNewRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inst := &instance.Instance{Cache: gcache.New(4).LRU().Build()}
	ctx = context.WithValue(ctx, instance.Key, inst)
	inst.Ctx = ctx
	events.NewDispatcher(ctx)
	lm := listeners.NewManager(ctx)
	recorders.NewManager(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	pm := pipeline.NewManager(ctx, store, nil, nil)
	inst.PipelineManager = pm
	cfg := configs.NewConfig()
	cfg.File = filepath.Join(t.TempDir(), "config.yml")
	configs.SetCurrentConfig(cfg)
	ctrl := gomock.NewController(t)
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetRawUrl().Return("https://recording-mode.test/room").AnyTimes()
	l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	l.EXPECT().GetInfo().Return(&live.Info{Live: l, HostName: "test", RoomName: "room"}, nil).AnyTimes()
	l.EXPECT().GetOptions().Return(&live.Options{}).AnyTimes()
	l.EXPECT().GetLastStartTime().Return(time.Time{}).AnyTimes()
	l.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
	l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	l.EXPECT().UpdateLiveOptionsbyConfig(gomock.Any(), gomock.Any()).AnyTimes()
	live.Register("recording-mode.test", recordingModeLiveBuilder{l})
	info, err := addLiveImpl(ctx, "https://recording-mode.test/room", false, configs.RecordingModeOnce)
	require.NoError(t, err)
	require.NoError(t, removeLiveImpl(ctx, info.Live))
	oldRun, err := pm.RecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, pipeline.RecordingRunStopped, oldRun.State)
	info, err = addLiveImpl(ctx, "https://recording-mode.test/room", true, configs.RecordingModeOnce)
	require.NoError(t, err)
	run, err := pm.RecordingRun("room")
	require.NoError(t, err)
	require.True(t, info.Listening, "重新添加未开启录制: %s", run.State)
	require.Equal(t, pipeline.RecordingRunWaiting, run.State)
	require.NotEqual(t, oldRun.RunID, run.RunID)
	require.NoError(t, lm.RemoveListener(ctx, "room"))
}

func TestAddingRoomRejectsUnboundFallbackWithoutLeavingInactiveConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inst := &instance.Instance{Cache: gcache.New(4).LRU().Build()}
	ctx = context.WithValue(ctx, instance.Key, inst)
	inst.Ctx = ctx
	events.NewDispatcher(ctx)
	listeners.NewManager(ctx)
	recorders.NewManager(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	pm := pipeline.NewManager(ctx, store, nil, nil)
	inst.PipelineManager = pm
	cfg := configs.NewConfig()
	cfg.File = filepath.Join(t.TempDir(), "config.yml")
	configs.SetCurrentConfig(cfg)
	ctrl := gomock.NewController(t)
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetRawUrl().Return("https://recording-mode-fallback.test/room").AnyTimes()
	l.EXPECT().GetLiveId().Return(types.LiveID("real-room")).AnyTimes()
	l.EXPECT().GetInfo().Return(nil, errors.New("transient platform failure")).Times(3)
	l.EXPECT().GetInfo().Return(&live.Info{Live: l, HostName: "test", RoomName: "room"}, nil).AnyTimes()
	l.EXPECT().GetOptions().Return(&live.Options{}).AnyTimes()
	l.EXPECT().GetLastStartTime().Return(time.Time{}).AnyTimes()
	l.EXPECT().GetPlatformCNName().Return("test").AnyTimes()
	l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	l.EXPECT().UpdateLiveOptionsbyConfig(gomock.Any(), gomock.Any()).AnyTimes()
	live.Register("recording-mode-fallback.test", recordingModeLiveBuilder{l})
	_, err = addLiveImpl(ctx, "https://recording-mode-fallback.test/room", true, configs.RecordingModeOnce)
	require.ErrorContains(t, err, "房间身份尚未确认")
	require.Empty(t, configs.GetCurrentConfig().LiveRooms)
	require.Zero(t, inst.Lives.Len())
	_, err = pm.RecordingRun("real-room")
	require.Error(t, err)
	info, err := addLiveImpl(ctx, "https://recording-mode-fallback.test/room", true, configs.RecordingModeOnce)
	require.NoError(t, err, "平台恢复后用户重试可以正常新增")
	require.True(t, info.Listening)
	require.NoError(t, removeLiveImpl(ctx, info.Live))
}

func TestRoomModeUpdateAffectsCurrentSessionWithoutStoppingIt(t *testing.T) {
	for _, byID := range []bool{true, false} {
		t.Run(map[bool]string{true: "id", false: "url"}[byID], func(t *testing.T) {
			ctx := context.Background()
			store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
			require.NoError(t, err)
			t.Cleanup(func() { store.Close() })
			pm := pipeline.NewManager(ctx, store, nil, nil)
			_, err = pm.ConfigureRecordingRun("room", configs.RecordingModeContinuous)
			require.NoError(t, err)
			require.NoError(t, pm.OpenRecordingSession("session", "room"))
			origin, err := pm.BeginRecordingProducer("room")
			require.NoError(t, err)
			require.NoError(t, pm.RecordCaptureEvidence(origin, true, ""))
			inst := &instance.Instance{PipelineManager: pm}
			ctx = context.WithValue(ctx, instance.Key, inst)
			inst.Ctx = ctx
			ctrl := gomock.NewController(t)
			l := livemock.NewMockLive(ctrl)
			l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
			l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
			inst.Lives.Set("room", l)
			cfg := configs.NewConfig()
			cfg.File = filepath.Join(t.TempDir(), "config.yml")
			cfg.LiveRooms = []configs.LiveRoom{{Url: "https://example.com/room", LiveId: "room", IsListening: true}}
			configs.SetCurrentConfig(cfg)
			req := httptest.NewRequest(http.MethodPatch, "/api/config/rooms/id/room", strings.NewReader(`{"recording_mode":"once"}`)).WithContext(ctx)
			req = mux.SetURLVars(req, map[string]string{"id": "room", "url": "https://example.com/room"})
			resp := httptest.NewRecorder()
			if byID {
				updateRoomConfigById(resp, req)
			} else {
				updateRoomConfig(resp, req)
			}
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			actual, err := pm.RecordingRun("room")
			require.NoError(t, err)
			require.Equal(t, configs.RecordingModeOnce, actual.Mode)
			require.Equal(t, pipeline.RecordingRunRecording, actual.State)
			require.True(t, configs.GetCurrentConfig().LiveRooms[0].IsListening)
			require.NoError(t, pm.EndRecordingSession("room", "normal"))
			require.NoError(t, pm.FinishRecordingProducer(origin, ""))
			actual, err = pm.RecordingRun("room")
			require.NoError(t, err)
			require.Equal(t, pipeline.RecordingRunCompleted, actual.State)
			var result commonResp
			require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &result))
			require.Zero(t, result.ErrNo)
		})
	}
}

func TestExplicitRestartAndStopWaitForPreviousLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inst := &instance.Instance{Cache: gcache.New(4).LRU().Build()}
	ctx = context.WithValue(ctx, instance.Key, inst)
	inst.Ctx = ctx
	ed := events.NewDispatcher(ctx)
	lm := listeners.NewManager(ctx)
	recorders.NewManager(ctx)
	store, err := pipeline.NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	pm := pipeline.NewManager(ctx, store, nil, ed)
	inst.PipelineManager = pm
	cfg := configs.NewConfig()
	cfg.File = filepath.Join(t.TempDir(), "config.yml")
	cfg.LiveRooms = []configs.LiveRoom{{Url: "https://example.com/room", LiveId: "room", IsListening: true, RecordingMode: configs.RecordingModeOnce}}
	configs.SetCurrentConfig(cfg)
	initial, err := pm.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	_, err = pm.StopRecordingRun("room")
	require.NoError(t, err)
	ctrl := gomock.NewController(t)
	l := livemock.NewMockLive(ctrl)
	l.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	l.EXPECT().GetRawUrl().Return("https://example.com/room").AnyTimes()
	l.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	l.EXPECT().GetInfo().Return(&live.Info{Live: l, Status: false}, nil).AnyTimes()
	l.EXPECT().GetInfoWithInterval(gomock.Any()).DoAndReturn(func(ctx context.Context) (*live.Info, error) { <-ctx.Done(); return nil, ctx.Err() }).AnyTimes()
	inst.Lives.Set("room", l)
	require.NoError(t, lm.AddListener(ctx, l))
	require.False(t, lm.HasListener(ctx, "room"))
	require.NoError(t, startListening(ctx, l))
	require.True(t, lm.HasListener(ctx, "room"))
	first, err := pm.RecordingRun("room")
	require.NoError(t, err)
	require.NotEqual(t, initial.RunID, first.RunID)

	entered, release := make(chan struct{}), make(chan struct{})
	ed.AddEventListener(listeners.UserStop, events.NewEventListener(func(*events.Event) { close(entered); <-release }))
	stopped, restarted := make(chan error, 1), make(chan error, 1)
	go func() { stopped <- stopListening(ctx, "room") }()
	<-entered
	go func() { restarted <- startListening(ctx, l) }()
	select {
	case err := <-restarted:
		t.Fatalf("重新开启越过旧的停止事件: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-stopped)
	require.NoError(t, <-restarted)
	next, err := pm.RecordingRun("room")
	require.NoError(t, err)
	require.NotEqual(t, first.RunID, next.RunID)
	require.Equal(t, pipeline.RecordingRunWaiting, next.State)
	require.NoError(t, lm.RemoveListener(ctx, "room"))
}

func TestModeSaveFailureDoesNotLeaveConfigPromisingAnInactiveMode(t *testing.T) {
	ctx := context.WithValue(context.Background(), instance.Key, &instance.Instance{})
	cfg := configs.NewConfig()
	cfg.File = filepath.Join(t.TempDir(), "config.yml")
	cfg.LiveRooms = []configs.LiveRoom{{Url: "https://example.com/room", LiveId: "room", IsListening: true}}
	configs.SetCurrentConfig(cfg)
	req := mux.SetURLVars(httptest.NewRequest(http.MethodPatch, "/api/config/rooms/id/room", strings.NewReader(`{"recording_mode":"once"}`)).WithContext(ctx), map[string]string{"id": "room"})
	resp := httptest.NewRecorder()
	updateRoomConfigById(resp, req)
	require.Equal(t, http.StatusInternalServerError, resp.Code)
	require.Equal(t, configs.RecordingModeContinuous, configs.GetCurrentConfig().LiveRooms[0].EffectiveRecordingMode())
}

func TestRoomURLUpdateDoesNotRequireRecordingControlInitialization(t *testing.T) {
	ctx := context.WithValue(context.Background(), instance.Key, &instance.Instance{})
	cfg := configs.NewConfig()
	cfg.File = filepath.Join(t.TempDir(), "config.yml")
	cfg.LiveRooms = []configs.LiveRoom{{Url: "https://example.com/room", LiveId: "room", IsListening: true}}
	configs.SetCurrentConfig(cfg)
	req := mux.SetURLVars(httptest.NewRequest(http.MethodPatch, "/api/config/rooms/id/room", strings.NewReader(`{"url":"https://example.com/new-room","is_listening":true}`)).WithContext(ctx), map[string]string{"id": "room"})
	resp := httptest.NewRecorder()
	updateRoomConfigById(resp, req)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	room := configs.GetCurrentConfig().LiveRooms[0]
	require.Equal(t, "https://example.com/new-room", room.Url)
	require.Equal(t, types.LiveID("room"), room.LiveId)
	require.True(t, room.IsListening)
}
