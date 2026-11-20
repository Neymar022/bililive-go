package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

func TestFormalPublicationAutomaticallyRepairsHistoricalMissingIdentity(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("自动写入仅Linux；非Linux拒绝单独验证")
	}
	stubLiveSessionMediaForEpisodeList(t, []float64{3}, []string{"S01E0019"})
	stubLiveSessionCoverExtraction(t, nil)
	root := t.TempDir()
	library := filepath.Join(root, "video")
	const show = "Fixture Host"
	const identity = "1035417600000000"
	season := filepath.Join(library, show, "Season 01")
	require.NoError(t, os.MkdirAll(season, 0755))
	old := filepath.Join(season, show+".S01E"+identity+".2024-02-07 - Synthetic episode.mp4")
	original := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<episodedetails>
  <title>Synthetic episode</title><season>1</season><episode>1</episode>
  <uniqueid type="fixture">unrelated-id</uniqueid>
</episodedetails>`)
	require.NoError(t, os.WriteFile(old, []byte("historical-media"), 0644))
	stem := strings.TrimSuffix(old, ".mp4")
	require.NoError(t, os.WriteFile(stem+".nfo", original, 0700))
	originalMetadata := subtitle.Metadata{
		Status: subtitle.StatusCompleted, OutputPath: old,
		Segments:   []subtitle.Segment{{Start: "00:00:00,000", End: "00:00:01,000", Text: "合成字幕"}},
		RecordMeta: map[string]any{"start_time": "2024-02-07T00:00:00Z"},
	}
	require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", originalMetadata))
	incoming, meta, _ := writeCompletedKnowledgeSessionSidecarAt(t, filepath.Join(root, ".live_session_segments", "processing"), show, 19, time.Date(2024, 2, 9, 18, 19, 0, 0, time.UTC), "字幕")
	manifest := knowledgeSessionManifest{LiveSessionID: "new-session", PublicationVersion: 1, Sources: []knowledgeSessionManifestSource{{LibraryPath: incoming, MetadataPath: meta}}}
	installTestLibraryRecoveryCapability(t, library)
	store, err := pipeline.NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	manager := pipeline.NewManager(context.Background(), store, nil, nil)
	defer manager.Close(context.Background())
	manager.SetLibraryIndexObserver(recoveryFixtureObserver{})
	hooks := &pipeline.PipelineContext{LibraryPublication: manager.EnsureLibraryPublication}
	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", library, &manifest, hooks)
	require.NoError(t, err, "正式发布必须在合并前自动恢复完整旧季")
	require.NotNil(t, aggregate)
	require.NoError(t, subtitle.ValidatePublishedLibraryEpisode(aggregate.LibraryPath))
	after := mustReadStageFile(t, stem+".nfo")
	insertion := []byte(`<uniqueid type="bililive-recorded-at" default="false">` + identity + `</uniqueid>`)
	require.Equal(t, string(original), strings.Replace(string(after), string(insertion), "", 1))
	require.Equal(t, "historical-media", string(mustReadStageFile(t, old)))
}

func installTestLibraryRecoveryCapability(t *testing.T, library string) {
	t.Helper()
	state, err := subtitle.CaptureLibraryFileState(library, false)
	require.NoError(t, err)
	capability := map[string]any{"schema_version": 2, "canonical_library_root": library, "library_dev": state.Device, "library_inode": state.Inode, "service_uid": os.Geteuid(), "service_gid": os.Getegid(), "mode": "exclusive_application_nfo_writer", "platform_build_path": subtitle.LibraryPlatformBuildPath, "platform_build_sha256": strings.Repeat("b", 64), "installed_at": time.Now().Format(time.RFC3339Nano), "probe_evidence_sha256": strings.Repeat("a", 64), "library_attributes": state}
	root := subtitle.LibraryRecoveryRoot(library)
	require.NoError(t, os.MkdirAll(root, 0700))
	b, err := json.Marshal(capability)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "capability.json"), b, 0600))
}

type recoveryFixtureObserver struct{}

func (recoveryFixtureObserver) VerifyPublication(ctx context.Context, expected pipeline.LibraryIndexExpected) (pipeline.LibraryIndexObservation, error) {
	if expected.Path == "" {
		return pipeline.LibraryIndexObservation{State: "verified", GeneratedAt: time.Now(), AdapterVersion: "isolated-fixture", Platform: subtitle.LibraryRecoveryPlatform{State: "confirmed", PID: 123, UID: 0, StartTicks: 10, StartedAt: time.Now().Add(-time.Hour), ObservedAt: time.Now(), StartupPath: subtitle.LibraryPlatformStartupPath, BuildPath: subtitle.LibraryPlatformBuildPath, BuildSHA256: strings.Repeat("b", 64)}}, nil
	}
	return pipeline.LibraryIndexObservation{State: "index_unconfirmed", Reason: "fixture has no PG"}, nil
}

func TestAutomaticFailedCheckpointPublishesWithoutWorkerAndRetainsSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("自动NFO写入仅Linux")
	}
	ctx := context.Background()
	root := t.TempDir()
	if shared := os.Getenv("RECOVERY_PUBLISH_CHILD_ROOT"); shared != "" {
		root = shared
	}
	library := filepath.Join(root, "video")
	require.NoError(t, os.MkdirAll(library, 0755))
	store, err := pipeline.NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	require.NoError(t, store.OpenRecordingSession(ctx, "42", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	start := time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation)
	source, err := subtitle.RecordingCapturePath(library, "42", origin.ProducerID, "Host - 2026-09-05 10-00-00 - Room.mp4")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(source, []byte("original-source"), 0644))
	task := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "room", LiveSessionID: "42", RecordingProducerID: origin.ProducerID, HostName: "Host", StartTime: start}, &pipeline.PipelineConfig{Stages: []pipeline.StageConfig{{Name: pipeline.StageNameSubtitleGenerate}}}, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
	task.Status = pipeline.PipelineStatusFailed
	require.NoError(t, store.CreateTask(ctx, task))
	task.ErrorMessage = "published NFO identity mismatch"
	require.NoError(t, store.UpdateTask(ctx, task))
	work, err := subtitle.RecordingWorkPath(library, "42", task.ID, 0, source, "Host", start)
	require.NoError(t, err)
	stem := strings.TrimSuffix(work, ".mp4")
	for _, p := range []string{work, stem + ".srt", stem + ".ass"} {
		require.NoError(t, os.WriteFile(p, []byte("completed-checkpoint"), 0644))
	}
	require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", subtitle.Metadata{Status: subtitle.StatusCompleted, SourcePath: source, OutputPath: work, SRTPath: stem + ".srt", ASSPath: stem + ".ass", Segments: []subtitle.Segment{{Start: "00:00:00,000", End: "00:00:03,000", Text: "字幕"}}, RecordMeta: map[string]any{"live_session_id": "42", "recording_producer_id": origin.ProducerID, "pipeline_task_id": fmt.Sprint(task.ID), "start_time": start.Format(time.RFC3339Nano)}}))
	require.NoError(t, store.CompleteRecordingTaskMedia(ctx, "42", task.ID, []pipeline.SessionMediaSource{{TaskID: task.ID, ProducerID: origin.ProducerID, InputPath: source, LibraryPath: work, MetadataPath: stem + ".subtitle.json"}}))
	require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	season := filepath.Join(library, "Host", "Season 01")
	require.NoError(t, os.MkdirAll(season, 0755))
	oldRecordedAt := time.Date(2024, 2, 8, 11, 12, 13, 246813579, mediaLibraryLocation)
	oldIdentity := chronologicalLiveSessionEpisodeIdentity(oldRecordedAt)
	old := filepath.Join(season, fmt.Sprintf("Host.S01E%d.%s - old.mp4", oldIdentity, oldRecordedAt.Format("2006-01-02")))
	oldStem := strings.TrimSuffix(old, ".mp4")
	require.NoError(t, os.WriteFile(old, []byte("old-media"), 0644))
	require.NoError(t, os.WriteFile(oldStem+".nfo", []byte(`<episodedetails><season>1</season><episode>1</episode></episodedetails>`), 0644))
	require.NoError(t, subtitle.SaveMetadata(oldStem+".subtitle.json", subtitle.Metadata{Status: subtitle.StatusCompleted, OutputPath: old, RecordMeta: map[string]any{"start_time": oldRecordedAt.Format(time.RFC3339Nano)}}))
	installTestLibraryRecoveryCapability(t, library)
	var workerCalls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls.Add(1)
		http.Error(w, "must not rerun worker", 500)
	}))
	defer worker.Close()
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = library
	cfg.Subtitle.Schedule.Enabled = false
	cfg.Subtitle.DeleteSourceOnCompletion = true
	cfg.Subtitle.KnowledgeSync.Enabled = false
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	defer configs.SetCurrentConfig(previous)
	stubLiveSessionMediaForEpisodeList(t, []float64{3}, []string{"S01E1685894400000000"})
	stubLiveSessionCoverExtraction(t, nil)
	manager := pipeline.NewManager(ctx, store, &pipeline.ManagerConfig{MaxConcurrent: 1, PollInterval: 10 * time.Millisecond}, nil)
	defer manager.Close(ctx)
	observer := &controlledRecoveryObserver{}
	manager.SetLibraryIndexObserver(observer)
	RegisterBuiltinStagesToManager(manager)
	if os.Getenv("RECOVERY_PUBLISH_CHILD_ROOT") != "" {
		manager.SetLibraryRecoveryHooks(pipeline.LibraryRecoveryHooks{Boundary: func(point string) error {
			if point == "published" {
				process, e := os.FindProcess(os.Getpid())
				if e != nil {
					return e
				}
				return process.Kill()
			}
			return nil
		}})
	}
	require.NoError(t, manager.Start(ctx))
	var completed *pipeline.PipelineTask
	require.Eventually(t, func() bool {
		completed, err = manager.GetTask(task.ID)
		return err == nil && completed.Status == pipeline.PipelineStatusCompleted
	}, 5*time.Second, 10*time.Millisecond)
	require.Zero(t, workerCalls.Load())
	require.NotNil(t, completed.LibraryRecovery)
	require.Equal(t, "index_unconfirmed", completed.LibraryRecovery.State)
	require.Equal(t, "original-source", string(mustReadStageFile(t, source)))
	product := completed.CurrentFiles[0].Path
	metadata, err := subtitle.LoadMetadata(strings.TrimSuffix(product, ".mp4") + ".subtitle.json")
	require.NoError(t, err)
	require.True(t, metadata.KeepSource)
	require.NoError(t, subtitle.ValidatePublishedLibraryEpisode(product))
	deleted, err := subtitle.CleanupExpiredSources(library, "", 1, time.Now().Add(365*24*time.Hour))
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.FileExists(t, source)
	observer.mode.Store(1)
	require.Eventually(t, func() bool {
		task, e := manager.GetTask(task.ID)
		return e == nil && task.LibraryRecovery != nil && task.LibraryRecovery.State == "external_index_conflict"
	}, 3*time.Second, 10*time.Millisecond)
	require.ErrorContains(t, subtitle.AssertLibraryPublicationReady(product), "season_recovery_in_progress")
}

func TestAutomaticPublicationCommitSIGKILLReusesMediaOnStartup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("真实SIGKILL闭环仅Linux")
	}
	root := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run", "^TestAutomaticFailedCheckpointPublishesWithoutWorkerAndRetainsSource$")
	child.Env = append(os.Environ(), "RECOVERY_PUBLISH_CHILD_ROOT="+root)
	output, err := child.CombinedOutput()
	require.Error(t, err, string(output))
	require.Contains(t, err.Error(), "killed", string(output))
	library := filepath.Join(root, "video")
	// 按目录中的文件名找真实成品，避免 glob 把方括号当作字符类。
	var products []string
	entries, err := os.ReadDir(filepath.Join(library, "Host", "Season 01"))
	require.NoError(t, err)
	for _, e := range entries {
		if strings.Contains(e.Name(), "[同场聚合]") && strings.HasSuffix(e.Name(), ".mp4") {
			products = append(products, filepath.Join(library, "Host", "Season 01", e.Name()))
		}
	}
	require.Len(t, products, 1)
	before, err := subtitle.CaptureLibraryFileState(products[0], false)
	require.NoError(t, err)
	store, err := pipeline.NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = library
	cfg.Subtitle.Schedule.Enabled = false
	cfg.Subtitle.DeleteSourceOnCompletion = true
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	defer configs.SetCurrentConfig(previous)
	originalConcat := liveSessionMediaConcat
	originalProbe := liveSessionMediaProbeDuration
	defer func() { liveSessionMediaConcat = originalConcat; liveSessionMediaProbeDuration = originalProbe }()
	var concatCalls, probeCalls atomic.Int32
	liveSessionMediaConcat = func(context.Context, string, []string, string) error {
		concatCalls.Add(1)
		return fmt.Errorf("must reuse committed media")
	}
	liveSessionMediaProbeDuration = func(context.Context, string, string) (float64, error) {
		probeCalls.Add(1)
		return 0, fmt.Errorf("must reuse committed media")
	}
	manager := pipeline.NewManager(context.Background(), store, &pipeline.ManagerConfig{MaxConcurrent: 1, PollInterval: 10 * time.Millisecond}, nil)
	defer manager.Close(context.Background())
	manager.SetLibraryIndexObserver(recoveryFixtureObserver{})
	RegisterBuiltinStagesToManager(manager)
	require.NoError(t, manager.Start(context.Background()))
	require.Eventually(t, func() bool {
		task, e := manager.GetTask(1)
		return e == nil && task.Status == pipeline.PipelineStatusCompleted
	}, 5*time.Second, 10*time.Millisecond)
	after, err := subtitle.CaptureLibraryFileState(products[0], false)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Zero(t, concatCalls.Load())
	require.Zero(t, probeCalls.Load())
}

type controlledRecoveryObserver struct{ mode atomic.Int32 }

func (o *controlledRecoveryObserver) VerifyPublication(ctx context.Context, expected pipeline.LibraryIndexExpected) (pipeline.LibraryIndexObservation, error) {
	if expected.Path == "" {
		return pipeline.LibraryIndexObservation{State: "verified", GeneratedAt: time.Now(), AdapterVersion: "isolated-fixture", Platform: subtitle.LibraryRecoveryPlatform{State: "confirmed", PID: 123, UID: 0, StartTicks: 10, StartedAt: time.Now().Add(-time.Hour), ObservedAt: time.Now(), StartupPath: subtitle.LibraryPlatformStartupPath, BuildPath: subtitle.LibraryPlatformBuildPath, BuildSHA256: strings.Repeat("b", 64)}}, nil
	}
	if o.mode.Load() == 1 {
		return pipeline.LibraryIndexObservation{State: "external_index_conflict", Reason: "protected mapping changed", GeneratedAt: time.Now(), AdapterVersion: "isolated-fixture", Platform: subtitle.LibraryRecoveryPlatform{State: "confirmed", PID: 123, UID: 0, StartTicks: 10, StartedAt: time.Now().Add(-time.Hour), ObservedAt: time.Now(), StartupPath: subtitle.LibraryPlatformStartupPath, BuildPath: subtitle.LibraryPlatformBuildPath, BuildSHA256: strings.Repeat("b", 64)}}, nil
	}
	return pipeline.LibraryIndexObservation{State: "index_unconfirmed", Reason: "fixture has no PG"}, nil
}
