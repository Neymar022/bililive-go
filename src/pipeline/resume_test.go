package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResumeTaskRetainsCheckpointAndScheduleAfterOriginalWasRemoved(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	converted := filepath.Join(t.TempDir(), "converted.mp4")
	require.NoError(t, os.WriteFile(converted, []byte("converted"), 0o644))
	task := NewPipelineTask(RecordInfo{}, &PipelineConfig{Stages: []StageConfig{{Name: "convert_mp4"}, {Name: StageNameSubtitleGenerate}}}, []FileInfo{NewVideoFileInfo("/removed/source.flv")})
	task.CurrentStage = 1
	task.CurrentFiles = []FileInfo{NewVideoFileInfo(converted)}
	task.Status = PipelineStatusFailed
	future := time.Now().UTC().Add(time.Hour)
	task.NotBefore = &future
	require.NoError(t, store.CreateTask(ctx, task))
	manager := NewManager(ctx, store, nil, nil)
	t.Cleanup(manager.cancel)
	require.NoError(t, manager.ResumeTask(task.ID))
	actual, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusPending, actual.Status)
	require.Equal(t, task.CurrentStage, actual.CurrentStage)
	require.Equal(t, task.CurrentFiles, actual.CurrentFiles)
	require.True(t, actual.NotBefore.Equal(future))
	require.Error(t, manager.ResumeTask(task.ID))
}

func TestResumeTaskRefusesConcurrentCheckpointEdit(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	task := NewPipelineTask(RecordInfo{}, &PipelineConfig{Stages: []StageConfig{{Name: StageNameSubtitleGenerate}}}, []FileInfo{NewVideoFileInfo("original.mp4")})
	task.Status = PipelineStatusFailed
	require.NoError(t, store.CreateTask(ctx, task))
	expected, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	task.CurrentFiles = []FileInfo{NewVideoFileInfo("corrected.mp4")}
	require.NoError(t, store.UpdateTask(ctx, task))
	require.ErrorContains(t, store.ResumeTask(ctx, expected), "checkpoint changed")
	actual, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusFailed, actual.Status)
	require.Equal(t, task.CurrentFiles, actual.CurrentFiles)
}

func TestResumeTaskRejectsUnadoptedHistoricalSessionWithoutChangingTask(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	input := filepath.Join(t.TempDir(), "retained.mp4")
	require.NoError(t, os.WriteFile(input, []byte("recording"), 0o644))
	task := NewPipelineTask(RecordInfo{LiveID: "room", LiveSessionID: "legacy"}, &PipelineConfig{Stages: []StageConfig{{Name: StageNameSubtitleGenerate}}}, []FileInfo{NewVideoFileInfo(input)})
	task.Status = PipelineStatusFailed
	task.ErrorMessage = "original publication failure"
	require.NoError(t, store.CreateTask(ctx, task))
	before, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	manager := NewManager(ctx, store, nil, nil)
	t.Cleanup(manager.cancel)
	require.ErrorContains(t, manager.ResumeTask(task.ID), "verified input closure")
	after, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestAdoptedHistoricalSessionResumesWithoutLosingItsSealedInputSet(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "pipeline.db")
	store, err := NewSQLiteStore(database)
	require.NoError(t, err)
	input := filepath.Join(t.TempDir(), "retained.mp4")
	require.NoError(t, os.WriteFile(input, []byte("recording"), 0o644))
	task := NewPipelineTask(RecordInfo{LiveID: "room", LiveSessionID: "961"}, &PipelineConfig{Stages: []StageConfig{{Name: StageNameSubtitleGenerate}}}, []FileInfo{NewVideoFileInfo(input)})
	task.Status = PipelineStatusFailed
	require.NoError(t, store.CreateTask(ctx, task))
	// 精确复现维护工具提交的 JSON 契约；已有文件和失败检查点不变。
	producer := fmt.Sprintf("historical-961-%d", task.ID)
	state := fmt.Sprintf(`{"id":"961","live_id":"room","end_reason":"normal","producers":{%q:true},"tasks":{%q:{"ready":false,"sources":[]}},"ready":false}`, producer, fmt.Sprint(task.ID))
	_, err = store.db.ExecContext(ctx, `INSERT INTO pipeline_recording_sessions(rowid,id,live_id,state_json) VALUES(-1,'961','room',?)`, state)
	require.NoError(t, err)
	task.RecordInfo.RecordingProducerID = producer
	record, err := json.Marshal(task.RecordInfo)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `UPDATE pipeline_tasks SET record_info_json = ? WHERE id = ?`, string(record), task.ID)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	store, err = NewSQLiteStore(database)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.RecoverRecordingSessions(ctx))
	manager := NewManager(ctx, store, nil, nil)
	t.Cleanup(manager.cancel)
	require.NoError(t, manager.ResumeTask(task.ID))
	session, err := store.RecordingSession(ctx, "961")
	require.NoError(t, err)
	require.True(t, session.Sealed())
	require.False(t, session.Ready())
	require.NoError(t, store.CompleteRecordingTaskMedia(ctx, "961", task.ID, []SessionMediaSource{{InputPath: input, LibraryPath: "/work/result.mp4", MetadataPath: "/work/result.subtitle.json"}}))
	session, err = store.RecordingSession(ctx, "961")
	require.NoError(t, err)
	require.True(t, session.Ready())
	require.Len(t, session.Sources(), 1)
	require.Equal(t, input, session.Sources()[0].InputPath)
	require.Equal(t, producer, session.Sources()[0].ProducerID)
}
