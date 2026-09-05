package pipeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecordingSessionWaitDoesNotOccupyQueueAndPreservesSchedule(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.OpenRecordingSession(ctx, "123", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	task := NewPipelineTask(RecordInfo{LiveSessionID: "123", RecordingProducerID: origin.ProducerID}, &PipelineConfig{}, []FileInfo{NewVideoFileInfo("segment.mp4")})
	require.NoError(t, store.CreateTask(ctx, task))
	require.NoError(t, store.CompleteRecordingTaskMedia(ctx, "123", task.ID, []SessionMediaSource{{InputPath: "segment.mp4", LibraryPath: "/work/video.mp4", MetadataPath: "/work/video.subtitle.json"}}))
	pending, err := store.GetPendingTasks(ctx, 3)
	require.NoError(t, err)
	require.Empty(t, pending, "已处理分段应等封口事件，而不是反复占执行槽")
	require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
	pending, err = store.GetPendingTasks(ctx, 3)
	require.NoError(t, err)
	require.Empty(t, pending, "最终 producer 尚未退出")
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	future := time.Now().Add(time.Hour)
	task.NotBefore = &future
	require.NoError(t, store.UpdateTask(ctx, task))
	pending, err = store.GetPendingTasks(ctx, 3)
	require.NoError(t, err)
	require.Empty(t, pending, "封口不能越过用户计划时间")
	task.NotBefore = nil
	require.NoError(t, store.UpdateTask(ctx, task))
	pending, err = store.GetPendingTasks(ctx, 3)
	require.NoError(t, err)
	require.Len(t, pending, 1)
}

func TestRecordingSessionWaitsForLastProducerAndAllRegisteredTasks(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.OpenRecordingSession(ctx, "123", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	task := NewPipelineTask(RecordInfo{LiveSessionID: "123", RecordingProducerID: origin.ProducerID}, &PipelineConfig{}, []FileInfo{NewVideoFileInfo("segment.mp4")})
	require.NoError(t, store.CreateTask(ctx, task))
	require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
	snapshot, err := store.RecordingSession(ctx, "123")
	require.NoError(t, err)
	require.False(t, snapshot.Sealed())
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	snapshot, err = store.RecordingSession(ctx, "123")
	require.NoError(t, err)
	require.True(t, snapshot.Sealed())
	require.False(t, snapshot.Ready())
	sources := []SessionMediaSource{{InputPath: "segment.mp4", LibraryPath: "/work/result.mp4", MetadataPath: "/work/result.subtitle.json"}}
	require.NoError(t, store.CompleteRecordingTaskMedia(ctx, "123", task.ID, sources))
	snapshot, err = store.RecordingSession(ctx, "123")
	require.NoError(t, err)
	require.True(t, snapshot.Ready())
	require.Len(t, snapshot.Sources(), 1)
	late := NewPipelineTask(task.RecordInfo, &PipelineConfig{}, []FileInfo{NewVideoFileInfo("late.mp4")})
	require.Error(t, store.CreateTask(ctx, late))
}

func TestRecordingSessionRejectsIncompleteProcessedInputSet(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.OpenRecordingSession(ctx, "123", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	task := NewPipelineTask(RecordInfo{LiveSessionID: "123", RecordingProducerID: origin.ProducerID}, &PipelineConfig{}, []FileInfo{NewVideoFileInfo("first.mp4"), NewVideoFileInfo("last.mp4")})
	require.NoError(t, store.CreateTask(ctx, task))
	require.Error(t, store.CompleteRecordingTaskMedia(ctx, "123", task.ID, []SessionMediaSource{{LibraryPath: "/work/first.mp4", MetadataPath: "/work/first.subtitle.json"}}))
	session, err := store.RecordingSession(ctx, "123")
	require.NoError(t, err)
	require.Empty(t, session.Sources())
}

type sessionWakeStage struct{ called chan struct{} }

func (s sessionWakeStage) Name() string { return "session_wake" }
func (s sessionWakeStage) Execute(_ *PipelineContext, input []FileInfo) ([]FileInfo, error) {
	close(s.called)
	return input, nil
}
func (s sessionWakeStage) GetCommands() []string { return nil }
func (s sessionWakeStage) GetLogs() string       { return "" }

func TestRecordingSessionSealingWakesSchedulerWithoutPolling(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	manager := NewManager(ctx, store, &ManagerConfig{MaxConcurrent: 1, PollInterval: time.Hour}, nil)
	called := make(chan struct{})
	manager.RegisterStage("session_wake", func(StageConfig) (Stage, error) { return sessionWakeStage{called}, nil })
	require.NoError(t, manager.Start(ctx))
	defer manager.Close(ctx)
	require.NoError(t, manager.OpenRecordingSession("123", "room"))
	origin, err := manager.BeginRecordingProducer("room")
	require.NoError(t, err)
	task := NewPipelineTask(RecordInfo{LiveSessionID: "123", RecordingProducerID: origin.ProducerID}, &PipelineConfig{Stages: []StageConfig{{Name: "session_wake"}}}, []FileInfo{NewVideoFileInfo("segment.mp4")})
	// 计划时间保护初次调度，处理完成的等待任务不应再次抢占执行槽。
	future := time.Now().Add(time.Hour)
	task.NotBefore = &future
	require.NoError(t, store.CreateTask(ctx, task))
	require.NoError(t, store.CompleteRecordingTaskMedia(ctx, "123", task.ID, []SessionMediaSource{{InputPath: "segment.mp4", LibraryPath: "/work/video.mp4", MetadataPath: "/work/video.subtitle.json"}}))
	task.NotBefore = nil
	require.NoError(t, store.UpdateTask(ctx, task))
	require.NoError(t, manager.EndRecordingSession("room", "normal"))
	require.NoError(t, manager.FinishRecordingProducer(origin, ""))
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("场次封口未唤醒调度器")
	}
}

func TestRecordingSessionFailedOpenCannotBindPreviousSession(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	manager := NewManager(ctx, store, nil, nil)
	defer manager.Close(ctx)
	require.NoError(t, manager.OpenRecordingSession("old", "room"))
	require.Error(t, manager.OpenRecordingSession("old", "room"))
	_, err = manager.BeginRecordingProducer("room")
	require.Error(t, err, "登记失败后不得把新的录制线程绑定到旧场次")
}
