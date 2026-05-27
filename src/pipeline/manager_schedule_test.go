package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerDefersAutomaticSubtitleStageAndResumesWhenDue(t *testing.T) {
	now := time.Now()
	futureRunAt := now.Add(time.Hour).Format("15:04")
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.AutoGenerate = true
	cfg.Subtitle.Schedule.Enabled = true
	cfg.Subtitle.Schedule.RunAt = futureRunAt
	configs.SetCurrentConfig(cfg)

	ctx := context.Background()
	store := NewMemoryStore()
	manager := NewManager(ctx, store, &ManagerConfig{MaxConcurrent: 1, PollInterval: time.Hour}, nil)
	defer manager.Close(ctx)

	prefixRuns := 0
	subtitleRuns := 0
	manager.RegisterStage("prefix", func(config StageConfig) (Stage, error) {
		return testStage{name: "prefix", run: func(input []FileInfo) []FileInfo {
			prefixRuns++
			return []FileInfo{NewVideoFileInfo("/tmp/converted.mp4")}
		}}, nil
	})
	manager.RegisterStage(StageNameSubtitleGenerate, func(config StageConfig) (Stage, error) {
		return testStage{name: StageNameSubtitleGenerate, run: func(input []FileInfo) []FileInfo {
			subtitleRuns++
			return append(input, FileInfo{Path: "/tmp/converted.srt", Type: FileTypeOther})
		}}, nil
	})

	task := NewPipelineTask(
		RecordInfo{HostName: "主播"},
		&PipelineConfig{Stages: []StageConfig{
			{Name: "prefix"},
			{Name: StageNameSubtitleGenerate, Options: map[string]any{OptionSubtitleScheduled: true}},
		}},
		[]FileInfo{NewVideoFileInfo("/tmp/source.mp4")},
	)
	require.NoError(t, store.CreateTask(ctx, task))

	manager.startTask(task)

	require.Eventually(t, func() bool {
		stored, err := store.GetTask(ctx, task.ID)
		if err != nil {
			return false
		}
		return stored.Status == PipelineStatusPending && stored.NotBefore != nil
	}, time.Second, 10*time.Millisecond)

	deferred, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, prefixRuns)
	assert.Equal(t, 0, subtitleRuns)
	assert.Equal(t, 1, deferred.CurrentStage)
	require.Len(t, deferred.CurrentFiles, 1)
	assert.Equal(t, "/tmp/converted.mp4", deferred.CurrentFiles[0].Path)
	require.Len(t, deferred.StageResults, 1)
	assert.Equal(t, "prefix", deferred.StageResults[0].StageName)

	deferred.CreatedAt = now.Add(-24 * time.Hour)
	past := now.Add(-time.Minute)
	deferred.NotBefore = &past
	require.NoError(t, store.UpdateTask(ctx, deferred))

	manager.startTask(deferred)

	require.Eventually(t, func() bool {
		stored, err := store.GetTask(ctx, task.ID)
		if err != nil {
			return false
		}
		return stored.Status == PipelineStatusCompleted
	}, time.Second, 10*time.Millisecond)

	completed, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, prefixRuns, "resume must not rerun completed prefix stage")
	assert.Equal(t, 1, subtitleRuns)
	assert.Nil(t, completed.NotBefore)
	require.Len(t, completed.StageResults, 2)
	assert.Equal(t, StageNameSubtitleGenerate, completed.StageResults[1].StageName)
}

func TestManagerDoesNotDeferManualSubtitleTask(t *testing.T) {
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.Schedule.Enabled = true
	cfg.Subtitle.Schedule.RunAt = time.Now().Add(time.Hour).Format("15:04")
	configs.SetCurrentConfig(cfg)

	ctx := context.Background()
	store := NewMemoryStore()
	manager := NewManager(ctx, store, &ManagerConfig{MaxConcurrent: 1, PollInterval: time.Hour}, nil)
	defer manager.Close(ctx)

	subtitleRuns := 0
	manager.RegisterStage(StageNameSubtitleGenerate, func(config StageConfig) (Stage, error) {
		return testStage{name: StageNameSubtitleGenerate, run: func(input []FileInfo) []FileInfo {
			subtitleRuns++
			return input
		}}, nil
	})

	task := NewPipelineTask(
		RecordInfo{HostName: "主播"},
		&PipelineConfig{Stages: []StageConfig{{Name: StageNameSubtitleGenerate}}},
		[]FileInfo{NewVideoFileInfo("/tmp/source.mp4")},
	)
	require.NoError(t, store.CreateTask(ctx, task))

	manager.startTask(task)

	require.Eventually(t, func() bool {
		stored, err := store.GetTask(ctx, task.ID)
		if err != nil {
			return false
		}
		return stored.Status == PipelineStatusCompleted
	}, time.Second, 10*time.Millisecond)

	completed, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, subtitleRuns)
	assert.Nil(t, completed.NotBefore)
}
