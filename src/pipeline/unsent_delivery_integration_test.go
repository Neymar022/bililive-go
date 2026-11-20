package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pipeline/stages"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

func unsentIntegrationFixture(t *testing.T) (*pipeline.SQLiteStore, *pipeline.PipelineTask, string, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = filepath.Join(root, "video")
	cfg.Subtitle.Schedule.Enabled = true
	cfg.Subtitle.Schedule.RunAt = "02:00"
	cfg.Subtitle.KnowledgeSync.Enabled = false
	require.NoError(t, os.Mkdir(cfg.Subtitle.LibraryRoot, 0755))
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Setenv("SUBTITLE_WORKER_URL", "http://"+listener.Addr().String())
	require.NoError(t, listener.Close())
	database := filepath.Join(root, "pipeline.db")
	store, err := pipeline.NewSQLiteStore(database)
	require.NoError(t, err)
	require.NoError(t, store.OpenRecordingSession(ctx, "owned-session", "owned-room"))
	origin, err := store.BeginRecordingProducer(ctx, "owned-room")
	require.NoError(t, err)
	source := filepath.Join(root, "converted.mp4")
	require.NoError(t, os.WriteFile(source, []byte("保留转换后的输入"), 0644))
	task := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "owned-room", HostName: "Host", LiveSessionID: "owned-session", RecordingProducerID: origin.ProducerID, StartTime: time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)}, &pipeline.PipelineConfig{Stages: []pipeline.StageConfig{{Name: "completed_prefix"}, {Name: pipeline.StageNameSubtitleGenerate, Options: map[string]any{pipeline.OptionSubtitleScheduled: true}}}}, []pipeline.FileInfo{pipeline.NewVideoFileInfo(filepath.Join(root, "removed.flv"))})
	task.CurrentStage = 1
	task.CurrentFiles = []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)}
	task.StageResults = []pipeline.StageResult{{StageName: "completed_prefix", Status: pipeline.StageStatusCompleted, OutputFiles: task.CurrentFiles}}
	// 首次02:00已过；后续采用RetryLater而不是重新安排到次日。
	task.CreatedAt = time.Now().UTC().Add(-48 * time.Hour)
	return store, task, database, cfg.Subtitle.LibraryRoot
}

func unsentIntegrationManager(store *pipeline.SQLiteStore) *pipeline.Manager {
	m := pipeline.NewManager(context.Background(), store, &pipeline.ManagerConfig{MaxConcurrent: 1, PollInterval: time.Hour}, nil)
	m.RegisterStage(pipeline.StageNameSubtitleGenerate, stages.NewSubtitleGenerateStage)
	return m
}

func TestUnsentDeliveryManagerSQLitePendingRestartAndClaims(t *testing.T) {
	ctx := context.Background()
	store, task, database, library := unsentIntegrationFixture(t)
	m := unsentIntegrationManager(store)
	t.Cleanup(func() { m.Close(ctx) })
	require.NoError(t, m.EnqueueTask(task))
	var queued *pipeline.PipelineTask
	require.Eventually(t, func() bool {
		current, err := store.GetTask(ctx, task.ID)
		if err != nil {
			return false
		}
		if current.Status == pipeline.PipelineStatusPending && strings.Contains(current.ErrorMessage, "worker request not sent") {
			queued = current
			return true
		}
		return false
	}, 5*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { stats, err := m.GetStats(); return err == nil && stats.RunningCount == 0 }, time.Second, time.Millisecond)
	require.Equal(t, 1, queued.CurrentStage)
	require.Equal(t, task.CurrentFiles, queued.CurrentFiles)
	require.Len(t, queued.StageResults, 2)
	require.Equal(t, pipeline.StageStatusFailed, queued.StageResults[1].Status)
	require.Equal(t, "completed_prefix", queued.StageResults[0].StageName)
	require.Nil(t, queued.CompletedAt)
	require.NotNil(t, queued.NotBefore)
	require.True(t, queued.NotBefore.After(time.Now()))
	require.True(t, queued.NotBefore.Before(time.Now().Add(10*time.Minute)), "首次02:00已过，不自动改为次日02:00")
	work, err := subtitle.RecordingWorkPath(library, task.RecordInfo.LiveSessionID, task.ID, 0, task.CurrentFiles[0].Path, "Host", task.RecordInfo.StartTime)
	require.NoError(t, err)
	metaPath := strings.TrimSuffix(work, ".mp4") + ".subtitle.json"
	before, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	meta, err := subtitle.LoadMetadata(metaPath)
	require.NoError(t, err)
	require.Equal(t, subtitle.StatusQueued, meta.Status)
	session, err := store.RecordingSession(ctx, task.RecordInfo.LiveSessionID)
	require.NoError(t, err)
	require.Empty(t, session.Sources())
	m.Close(ctx)
	store, err = pipeline.NewSQLiteStore(database)
	require.NoError(t, err)
	defer store.Close()
	restarted := unsentIntegrationManager(store)
	require.NoError(t, restarted.Start(ctx))
	defer restarted.Close(ctx)
	pending, err := store.GetPendingTasks(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	current, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, pipeline.PipelineStatusPending, current.Status)
	require.Equal(t, queued.CurrentFiles, current.CurrentFiles)
	after, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	require.Equal(t, before, after, "重启不得重写queued或claim未到期任务")
	require.Error(t, store.ClaimTask(ctx, current, time.Now()))
	duplicate, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	due := current.NotBefore.Add(time.Second)
	competitor, err := pipeline.NewSQLiteStore(database)
	require.NoError(t, err)
	defer competitor.Close()
	start, claimed := make(chan struct{}), make(chan error, 2)
	go func() { <-start; claimed <- store.ClaimTask(ctx, current, due) }()
	go func() { <-start; claimed <- competitor.ClaimTask(ctx, duplicate, due) }()
	close(start)
	wins := 0
	for i := 0; i < 2; i++ {
		if <-claimed == nil {
			wins++
		}
	}
	require.Equal(t, 1, wins, "独立store连接竞争同一due task只能有一个claim")
	require.Error(t, store.ClaimTask(ctx, duplicate, due))
	b, err := os.ReadFile(task.CurrentFiles[0].Path)
	require.NoError(t, err)
	require.Equal(t, "保留转换后的输入", string(b))
}

// 在真实stage已写queued、Manager尚未保存DB的公开Stage边界暂停。
// 只控制返回时机，不替换writer、Executor或store。
type unsentAfterStageBarrier struct {
	actual  pipeline.Stage
	reached chan error
	release chan struct{}
}

func (s unsentAfterStageBarrier) Name() string { return s.actual.Name() }
func (s unsentAfterStageBarrier) Execute(ctx *pipeline.PipelineContext, input []pipeline.FileInfo) ([]pipeline.FileInfo, error) {
	output, err := s.actual.Execute(ctx, input)
	s.reached <- err
	<-s.release
	return output, err
}

func TestUnsentDeliveryManagerCancelAfterQueuedCannotRevive(t *testing.T) {
	ctx := context.Background()
	store, task, _, library := unsentIntegrationFixture(t)
	defer store.Close()
	m := unsentIntegrationManager(store)
	t.Cleanup(func() { m.Close(ctx) })
	reached, release := make(chan error, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	m.RegisterStage(pipeline.StageNameSubtitleGenerate, func(cfg pipeline.StageConfig) (pipeline.Stage, error) {
		stage, err := stages.NewSubtitleGenerateStage(cfg)
		if err != nil {
			return nil, err
		}
		return unsentAfterStageBarrier{actual: stage, reached: reached, release: release}, nil
	})
	require.NoError(t, m.EnqueueTask(task))
	select {
	case err := <-reached:
		var later *pipeline.RetryLaterError
		require.True(t, errors.As(err, &later), "真实stage应先完成queued: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("真实stage未到queued边界")
	}
	work, err := subtitle.RecordingWorkPath(library, task.RecordInfo.LiveSessionID, task.ID, 0, task.CurrentFiles[0].Path, "Host", task.RecordInfo.StartTime)
	require.NoError(t, err)
	metaPath := strings.TrimSuffix(work, ".mp4") + ".subtitle.json"
	before, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	require.NoError(t, m.CancelTask(task.ID))
	unblock()
	require.Eventually(t, func() bool { stats, err := m.GetStats(); return err == nil && stats.RunningCount == 0 }, time.Second, time.Millisecond)
	current, err := store.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, pipeline.PipelineStatusCancelled, current.Status)
	require.Equal(t, 1, current.CurrentStage)
	require.Equal(t, task.CurrentFiles, current.CurrentFiles)
	after, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Error(t, store.ClaimTask(ctx, current, time.Now().Add(time.Hour)))
}

func TestUnsentDeliveryScheduleAndHoldUseExistingPolicy(t *testing.T) {
	for _, offset := range []int{8, 9, -5} {
		t.Run(fmt.Sprintf("UTC%+d", offset), func(t *testing.T) {
			zone := time.FixedZone("service", offset*3600)
			created := time.Date(2026, 11, 3, 1, 55, 0, 0, zone)
			schedule := configs.SubtitleScheduleConfig{Enabled: true, RunAt: "02:00"}
			first, err := schedule.NextRunAfter(created)
			require.NoError(t, err)
			require.Equal(t, time.Date(2026, 11, 3, 2, 0, 0, 0, zone), first)
			require.Equal(t, created.Location(), first.Location())
			task := pipeline.NewPipelineTask(pipeline.RecordInfo{}, &pipeline.PipelineConfig{}, nil)
			hold := first.Add(3 * time.Hour)
			task.NotBefore = &hold
			task.MarkDeferred(first.Add(5*time.Minute), 0, nil)
			require.True(t, task.NotBefore.Equal(hold), "旧hold不能缩短")
			// 已过首次02:00后的RetryLater沿用延迟，不重新使用NextRunAfter(now)。
			retryAt := first.Add(10 * time.Minute)
			task.NotBefore = nil
			task.MarkDeferred(retryAt, 0, nil)
			require.True(t, task.NotBefore.Equal(retryAt))
			originalWindow, err := schedule.NextRunAfter(created)
			require.NoError(t, err)
			require.False(t, retryAt.Before(originalWindow))
		})
	}
}

func TestUnsentDeliveryManagerFirst0200WindowDoesNotCallStage(t *testing.T) {
	ctx := context.Background()
	store, task, _, library := unsentIntegrationFixture(t)
	m := unsentIntegrationManager(store)
	defer m.Close(ctx)
	var posts, constructed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	t.Setenv("SUBTITLE_WORKER_URL", server.URL)
	m.RegisterStage(pipeline.StageNameSubtitleGenerate, func(cfg pipeline.StageConfig) (pipeline.Stage, error) {
		constructed.Add(1)
		return stages.NewSubtitleGenerateStage(cfg)
	})
	// CreatedAt是本轮创建时刻，首次NextRunAfter(CreatedAt)必在未来；固定策略仍为02:00。
	task.CreatedAt = time.Now().UTC()
	require.NoError(t, m.EnqueueTask(task))
	var deferred *pipeline.PipelineTask
	require.Eventually(t, func() bool {
		current, err := store.GetTask(ctx, task.ID)
		if err == nil && current.Status == pipeline.PipelineStatusPending && current.NotBefore != nil {
			deferred = current
			return true
		}
		return false
	}, 5*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { stats, err := m.GetStats(); return err == nil && stats.RunningCount == 0 }, time.Second, time.Millisecond)
	require.Equal(t, 2, deferred.NotBefore.UTC().Hour())
	require.Zero(t, deferred.NotBefore.Minute())
	require.True(t, deferred.NotBefore.After(task.CreatedAt))
	require.Zero(t, posts.Load())
	require.Zero(t, constructed.Load())
	require.Equal(t, 1, deferred.CurrentStage)
	require.Equal(t, task.CurrentFiles, deferred.CurrentFiles)
	require.Len(t, deferred.StageResults, 1)
	require.Error(t, store.ClaimTask(ctx, deferred, time.Now()))
	var metadata []string
	require.NoError(t, filepath.WalkDir(filepath.Dir(library), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".subtitle.json") {
			metadata = append(metadata, path)
		}
		return nil
	}))
	require.Empty(t, metadata, "首次时段之前不得进入stage写checkpoint")
}
