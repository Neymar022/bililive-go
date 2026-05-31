package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type retryLaterTestStage struct{}

func (retryLaterTestStage) Name() string {
	return "retry_later_test"
}

func (retryLaterTestStage) Execute(ctx *PipelineContext, input []FileInfo) ([]FileInfo, error) {
	return nil, NewRetryLaterError(errors.New("Mac 转写服务不可用，等待恢复后重试"), 10*time.Minute)
}

type fixedOutputTestStage struct {
	output []FileInfo
}

func (s fixedOutputTestStage) Name() string {
	return "fixed_output_test"
}

func (s fixedOutputTestStage) Execute(ctx *PipelineContext, input []FileInfo) ([]FileInfo, error) {
	return s.output, nil
}

func TestManagerRequeuesRetryLaterErrorWithNotBefore(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	manager := NewManager(ctx, store, &ManagerConfig{MaxConcurrent: 1, PollInterval: time.Hour}, nil)
	manager.RegisterStage("retry_later_test", func(config StageConfig) (Stage, error) {
		return retryLaterTestStage{}, nil
	})

	task := NewPipelineTask(
		RecordInfo{HostName: "主播"},
		&PipelineConfig{Stages: []StageConfig{{Name: "retry_later_test"}}},
		[]FileInfo{{Path: "/tmp/video.mp4", Type: FileTypeVideo}},
	)
	require.NoError(t, store.CreateTask(ctx, task))

	before := time.Now().UTC()
	manager.executeTask(ctx, task)

	stored, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.NotBefore)
	assert.Equal(t, PipelineStatusPending, stored.Status)
	assert.Contains(t, stored.ErrorMessage, "Mac 转写服务不可用")
	assert.True(t, stored.CanRetry)
	assert.Nil(t, stored.CompletedAt)
	assert.True(t, stored.NotBefore.After(before), "retry-later 任务必须写入未来的 not_before，避免调度热循环")
}

func TestManagerRetryLaterPreservesLastCompletedStageOutput(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	manager := NewManager(ctx, store, &ManagerConfig{MaxConcurrent: 1, PollInterval: time.Hour}, nil)
	converted := []FileInfo{{Path: "/tmp/converted.mp4", Type: FileTypeVideo, SourcePath: "/tmp/source.flv"}}
	manager.RegisterStage("fixed_output_test", func(config StageConfig) (Stage, error) {
		return fixedOutputTestStage{output: converted}, nil
	})
	manager.RegisterStage("retry_later_test", func(config StageConfig) (Stage, error) {
		return retryLaterTestStage{}, nil
	})

	task := NewPipelineTask(
		RecordInfo{HostName: "主播"},
		&PipelineConfig{Stages: []StageConfig{
			{Name: "fixed_output_test"},
			{Name: "retry_later_test"},
		}},
		[]FileInfo{{Path: "/tmp/source.flv", Type: FileTypeVideo}},
	)
	require.NoError(t, store.CreateTask(ctx, task))

	manager.executeTask(ctx, task)

	stored, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusPending, stored.Status)
	require.NotNil(t, stored.NotBefore)
	assert.Equal(t, converted, stored.CurrentFiles, "retry-later 后应从已完成阶段的输出继续，不能回到可能已被删除的原始 FLV")
}

func TestSQLiteStoreGetPendingTasksSkipsFutureNotBefore(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(t.TempDir() + "/pipeline.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	futureTask := NewPipelineTask(RecordInfo{HostName: "稍后"}, &PipelineConfig{}, []FileInfo{})
	future := time.Now().UTC().Add(time.Hour)
	futureTask.NotBefore = &future
	require.NoError(t, store.CreateTask(ctx, futureTask))

	readyTask := NewPipelineTask(RecordInfo{HostName: "现在"}, &PipelineConfig{}, []FileInfo{})
	require.NoError(t, store.CreateTask(ctx, readyTask))

	pending, err := store.GetPendingTasks(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, readyTask.ID, pending[0].ID)
	assert.Nil(t, pending[0].NotBefore)
}

func TestSQLiteStoreMigratesExistingPipelineTasksTableWithNotBefore(t *testing.T) {
	dbPath := t.TempDir() + "/pipeline.db"
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = rawDB.Exec(`
		CREATE TABLE pipeline_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			status TEXT NOT NULL DEFAULT 'pending',
			record_info_json TEXT,
			pipeline_config_json TEXT,
			initial_files_json TEXT,
			current_files_json TEXT,
			current_stage INTEGER DEFAULT 0,
			total_stages INTEGER DEFAULT 0,
			stage_results_json TEXT,
			progress INTEGER DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			started_at TIMESTAMP,
			completed_at TIMESTAMP,
			error_message TEXT,
			can_retry INTEGER DEFAULT 1
		)
	`)
	require.NoError(t, err)
	require.NoError(t, rawDB.Close())

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	task := NewPipelineTask(RecordInfo{HostName: "迁移后"}, &PipelineConfig{}, []FileInfo{})
	require.NoError(t, store.CreateTask(context.Background(), task))

	pending, err := store.GetPendingTasks(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, task.ID, pending[0].ID)
	assert.Nil(t, pending[0].NotBefore)
}
