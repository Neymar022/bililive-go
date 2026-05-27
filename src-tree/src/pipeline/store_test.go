package pipeline

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteStoreMigratesOldSchemaWithNotBefore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pipeline.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`
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
	require.NoError(t, db.Close())

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	future := time.Now().Add(time.Hour)
	task := NewPipelineTask(RecordInfo{HostName: "migrated"}, &PipelineConfig{Stages: []StageConfig{{Name: "passthrough"}}}, []FileInfo{NewVideoFileInfo("/tmp/video.mp4")})
	task.NotBefore = &future
	require.NoError(t, store.CreateTask(context.Background(), task))

	stored, err := store.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.NotBefore)
	assert.WithinDuration(t, future, *stored.NotBefore, time.Second)
}

func TestSQLiteStorePendingTasksHonorsNotBefore(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	dueTask := NewPipelineTask(RecordInfo{HostName: "due"}, &PipelineConfig{Stages: []StageConfig{{Name: "passthrough"}}}, []FileInfo{NewVideoFileInfo("/tmp/due.mp4")})
	pendingTask := NewPipelineTask(RecordInfo{HostName: "future"}, &PipelineConfig{Stages: []StageConfig{{Name: "passthrough"}}}, []FileInfo{NewVideoFileInfo("/tmp/future.mp4")})
	future := now.Add(time.Hour)
	pendingTask.NotBefore = &future

	require.NoError(t, store.CreateTask(ctx, dueTask))
	require.NoError(t, store.CreateTask(ctx, pendingTask))

	tasks, err := store.GetPendingTasks(ctx, 10)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, dueTask.ID, tasks[0].ID)

	past := now.Add(-time.Minute)
	pendingTask.NotBefore = &past
	require.NoError(t, store.UpdateTask(ctx, pendingTask))

	tasks, err = store.GetPendingTasks(ctx, 10)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, dueTask.ID, tasks[0].ID)
	assert.Equal(t, pendingTask.ID, tasks[1].ID)
}
