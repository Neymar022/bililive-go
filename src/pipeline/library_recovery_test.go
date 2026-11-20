package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

func TestCancelledTaskCannotBeClaimedOrFinishedByStaleWorker(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	task := NewPipelineTask(RecordInfo{}, &PipelineConfig{}, nil)
	require.NoError(t, store.CreateTask(ctx, task))
	manager := NewManager(ctx, store, nil, nil)
	require.NoError(t, manager.CancelTask(task.ID))
	require.Error(t, store.ClaimTask(ctx, task, time.Now()))
	current, err := manager.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusCancelled, current.Status)
	// 显式用户重试生成新pending，避免测试启动异步调度。
	task.Status = PipelineStatusPending
	require.NoError(t, store.UpdateTask(ctx, task))
	task, err = store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.NoError(t, store.ClaimTask(ctx, task, time.Now()))
	claim := *task.StartedAt
	require.NoError(t, manager.CancelTask(task.ID))
	task.MarkCompleted()
	require.Error(t, store.saveClaimedTask(ctx, task, claim))
	current, err = manager.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusCancelled, current.Status)
}
func TestFailedCancellationIsDurableAndNeverAutomaticallyResumed(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	task := NewPipelineTask(RecordInfo{LiveSessionID: "42"}, &PipelineConfig{Stages: []StageConfig{{Name: StageNameSubtitleGenerate}}}, nil)
	task.Status = PipelineStatusFailed
	task.ErrorMessage = "published NFO identity mismatch"
	require.NoError(t, store.CreateTask(ctx, task))
	m := NewManager(ctx, store, nil, nil)
	require.NoError(t, m.CancelTask(task.ID))
	m.compensateLibraryFailures()
	got, err := m.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusCancelled, got.Status)
}
func TestRecoveryDoesNotShortenFutureHold(t *testing.T) {
	original := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	task := NewPipelineTask(RecordInfo{}, &PipelineConfig{}, nil)
	task.NotBefore = &original
	task.MarkDeferred(original.Add(-time.Hour), 0, nil)
	require.Equal(t, original, *task.NotBefore)
	task.MarkFailed(fmt.Errorf("known NFO error"))
	require.Equal(t, original, *task.NotBefore)
}
func TestRecoveryPlatformRejectsAutomaticNFOChanges(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("非Linux合同由macOS运行验证")
	}
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	library := filepath.Join(root, "video")
	season := filepath.Join(library, "Host", "Season 01")
	require.NoError(t, os.MkdirAll(season, 0755))
	old := filepath.Join(season, "Host.S01E1036800000000000.2024-02-07 - old.mp4")
	require.NoError(t, os.WriteFile(old, []byte("old"), 0644))
	nfo := strings.TrimSuffix(old, ".mp4") + ".nfo"
	before := []byte(`<episodedetails><episode>1</episode></episodedetails>`)
	require.NoError(t, os.WriteFile(nfo, before, 0644))
	store, err := NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	m := NewManager(ctx, store, nil, nil)
	err = m.EnsureLibraryPublication(ctx, LibraryPublicationRequest{LibraryRoot: library, Path: filepath.Join(season, "Host.S01E1685894400000000.2026-09-05 - next.mp4"), RecordedAt: time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)})
	require.ErrorContains(t, err, "capability_unavailable_non_linux")
	after, err := os.ReadFile(nfo)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// 调度证明使用正式session闭包与已完成metadata，不靠错误字符串绕过检查点。
func TestAutomaticRecoveryQueuesAtNextWindowAndResumesCheckpoint(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = filepath.Join(root, "video")
	require.NoError(t, os.MkdirAll(cfg.Subtitle.LibraryRoot, 0755))
	cfg.Subtitle.Schedule.Enabled = true
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	defer configs.SetCurrentConfig(previous)
	require.NoError(t, store.OpenRecordingSession(ctx, "42", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	input := filepath.Join(root, "input.mp4")
	require.NoError(t, os.WriteFile(input, []byte("source"), 0644))
	task := NewPipelineTask(RecordInfo{LiveID: "room", HostName: "Host", LiveSessionID: "42", RecordingProducerID: origin.ProducerID, StartTime: time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)}, &PipelineConfig{Stages: []StageConfig{{Name: StageNameSubtitleGenerate}}}, []FileInfo{NewVideoFileInfo(input)})
	task.Status = PipelineStatusFailed
	task.ErrorMessage = "published NFO identity mismatch"
	require.NoError(t, store.CreateTask(ctx, task))
	require.NoError(t, store.UpdateTask(ctx, task))
	require.NoError(t, store.CompleteRecordingTaskMedia(ctx, "42", task.ID, []SessionMediaSource{{TaskID: task.ID, ProducerID: origin.ProducerID, InputPath: input, LibraryPath: filepath.Join(root, "completed.mp4"), MetadataPath: filepath.Join(root, "completed.subtitle.json")}}))
	for _, path := range []string{filepath.Join(root, "completed.mp4"), filepath.Join(root, "completed.srt"), filepath.Join(root, "completed.ass")} {
		require.NoError(t, os.WriteFile(path, []byte("completed"), 0644))
	}
	require.NoError(t, subtitle.SaveMetadata(filepath.Join(root, "completed.subtitle.json"), subtitle.Metadata{Status: subtitle.StatusCompleted, SourcePath: input, OutputPath: filepath.Join(root, "completed.mp4"), SRTPath: filepath.Join(root, "completed.srt"), ASSPath: filepath.Join(root, "completed.ass"), Segments: []subtitle.Segment{{Text: "字幕"}}, RecordMeta: map[string]any{"live_session_id": "42", "recording_producer_id": origin.ProducerID, "pipeline_task_id": fmt.Sprint(task.ID)}}))
	require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	m := NewManager(ctx, store, nil, nil)
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Now: func() time.Time { return now }})
	disabled := *cfg
	disabled.Subtitle.Enabled = false
	configs.SetCurrentConfig(&disabled)
	m.compensateLibraryFailures()
	got, err := m.GetTask(task.ID)
	require.NoError(t, err)
	require.Nil(t, got.LibraryRecovery)
	require.Equal(t, PipelineStatusFailed, got.Status)

	configs.SetCurrentConfig(cfg)
	m.compensateLibraryFailures()
	got, err = m.GetTask(task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LibraryRecovery)
	require.Equal(t, "waiting", got.LibraryRecovery.State)
	require.True(t, got.LibraryRecovery.NextAttempt.After(now))
	require.Equal(t, PipelineStatusFailed, got.Status)
	due := got.LibraryRecovery.NextAttempt
	require.NoError(t, store.Close())
	store, err = NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	m = NewManager(ctx, store, nil, nil)
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Now: func() time.Time { return now }})
	now = due.Add(-time.Second)
	m.compensateLibraryFailures()
	got, err = m.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusFailed, got.Status)
	now = due
	m.compensateLibraryFailures()
	got, err = m.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusPending, got.Status)
	require.Equal(t, "resumed", got.LibraryRecovery.State)
	require.Equal(t, task.CurrentFiles, got.CurrentFiles)
	require.NotNil(t, got.NotBefore)
	require.False(t, got.NotBefore.Before(due))
	data, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"library_recovery"`)
}

func TestAtomicRecoveryResumePreservesConcurrentlyExtendedHold(t *testing.T) {
	ctx := context.Background()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	defer s.Close()
	task := NewPipelineTask(RecordInfo{}, &PipelineConfig{}, nil)
	task.Status = PipelineStatusFailed
	require.NoError(t, s.CreateTask(ctx, task))
	expected, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	due := time.Now().UTC().Add(time.Hour)
	later := due.Add(3 * time.Hour)
	r := &LibraryRecoveryRecord{OperationID: fmt.Sprintf("task-%d", task.ID), Kind: "task", TaskID: task.ID, State: "resume_requested", NextAttempt: due}
	require.NoError(t, s.saveLibraryRecovery(ctx, r))
	task.NotBefore = &later
	require.NoError(t, s.UpdateTask(ctx, task))
	require.NoError(t, s.ResumeTask(ctx, expected))
	got, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, got.NotBefore.Equal(later))
	require.Equal(t, PipelineStatusPending, got.Status)
}

func TestRecoveryMigrationMakesSQLiteOnlineBackupBeforeSchemaChange(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "pipeline.db")
	s, err := NewSQLiteStore(path)
	require.NoError(t, err)
	task := NewPipelineTask(RecordInfo{}, &PipelineConfig{}, []FileInfo{NewVideoFileInfo("preserved.mp4")})
	require.NoError(t, s.CreateTask(ctx, task))
	require.NoError(t, s.Close())
	legacy, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = legacy.Exec(`DROP TABLE pipeline_library_recoveries`)
	require.NoError(t, err)
	defer legacy.Close()
	wal, err := os.Stat(path + "-wal")
	require.NoError(t, err)
	require.Positive(t, wal.Size())
	s, err = NewSQLiteStore(path)
	require.NoError(t, err)
	defer s.Close()
	backups, err := filepath.Glob(path + ".before-library-recovery.*.sqlite")
	require.NoError(t, err)
	require.Len(t, backups, 1)
	backup, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	require.NoError(t, err)
	defer backup.Close()
	var files string
	require.NoError(t, backup.QueryRow(`SELECT current_files_json FROM pipeline_tasks WHERE id=?`, task.ID).Scan(&files))
	require.Contains(t, files, "preserved.mp4")
	var schema int
	require.NoError(t, backup.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='pipeline_library_recoveries'`).Scan(&schema))
	require.Zero(t, schema)
	got, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, task.CurrentFiles, got.CurrentFiles)
	var synchronous int
	require.NoError(t, s.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous))
	require.Equal(t, 2, synchronous)
}

func TestTaskClaimUsesAbsoluteDeadlineAcrossTimezones(t *testing.T) {
	for _, tc := range []struct {
		name          string
		deadline, now time.Time
		allowed       bool
	}{
		{"past_cross_date", time.Date(2026, 11, 2, 2, 0, 0, 0, time.FixedZone("UTC+8", 8*3600)), time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC), true},
		{"future_cross_date", time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 3, 0, 0, 0, time.FixedZone("JST", 9*3600)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
			require.NoError(t, err)
			defer s.Close()
			task := NewPipelineTask(RecordInfo{}, &PipelineConfig{}, nil)
			task.NotBefore = &tc.deadline
			require.NoError(t, s.CreateTask(context.Background(), task))
			err = s.ClaimTask(context.Background(), task, tc.now)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
