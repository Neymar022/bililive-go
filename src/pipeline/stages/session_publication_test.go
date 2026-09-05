package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

func TestSubtitleGeneratePublishesSingleCompletedSessionOnlyAfterSealingWithoutKnowledge(t *testing.T) {
	stubLiveSessionMediaForEpisodeList(t, []float64{3}, []string{"S01E1685894400000000"})
	stubLiveSessionCoverExtraction(t, nil)
	ctx := context.Background()
	root := t.TempDir()
	library := filepath.Join(root, "video")
	require.NoError(t, os.MkdirAll(library, 0o755))
	source := filepath.Join(root, "Host - 2026-09-05 10-00-00 - Room.mp4")
	require.NoError(t, os.WriteFile(source, []byte("recording"), 0o644))
	store, err := pipeline.NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.OpenRecordingSession(ctx, "42", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	task := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "room", LiveSessionID: "42", RecordingProducerID: origin.ProducerID, HostName: "Host", StartTime: time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation)}, &pipeline.PipelineConfig{}, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
	require.NoError(t, store.CreateTask(ctx, task))
	stageCtx := &pipeline.PipelineContext{Ctx: ctx, TaskID: task.ID, RecordInfo: task.RecordInfo, SessionMediaReady: func(sources []pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
		if err := store.CompleteRecordingTaskMedia(ctx, "42", task.ID, sources); err != nil {
			return pipeline.RecordingSession{}, err
		}
		return store.RecordingSession(ctx, "42")
	}}
	workerCalls := 0
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls++
		var request subtitle.ProcessRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		rel, err := filepath.Rel(library, request.OutputVideoPath)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(rel, ".."), "未完成的分段不能进入媒体库")
		require.NoError(t, os.WriteFile(request.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(request.OutputSRTPath, []byte("subtitle"), 0o644))
		assPath := strings.TrimSuffix(request.OutputSRTPath, ".srt") + ".ass"
		require.NoError(t, os.WriteFile(assPath, []byte("ass"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{ASSPath: assPath, Segments: []subtitle.Segment{{Start: "00:00:00,000", End: "00:00:03,000", Text: "字幕"}}}))
	}))
	t.Cleanup(worker.Close)
	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.Subtitle.Enabled = true
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)
	cfg.Subtitle.LibraryRoot = library
	cfg.Subtitle.KnowledgeSync.Enabled = false
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous) })
	stage := &SubtitleGenerateStage{}
	_, err = stage.Execute(stageCtx, task.CurrentFiles)
	requireRetryLater(t, err)
	entries, err := os.ReadDir(library)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	output, err := stage.Execute(stageCtx, task.CurrentFiles)
	require.NoError(t, err)
	require.Equal(t, 1, workerCalls)
	require.NotEmpty(t, output)
	require.FileExists(t, output[0].Path)
	nfo, err := os.ReadFile(strings.TrimSuffix(output[0].Path, ".mp4") + ".nfo")
	require.NoError(t, err)
	require.Contains(t, string(nfo), "<episode>1</episode>")
	require.FileExists(t, source)
	duplicate, err := stage.Execute(stageCtx, task.CurrentFiles)
	require.NoError(t, err)
	require.Equal(t, output, duplicate)
	require.Equal(t, 1, workerCalls)
	for _, ext := range []string{".nfo", ".mp4", ".srt"} {
		path := strings.TrimSuffix(output[0].Path, ".mp4") + ext
		original := mustReadStageFile(t, path)
		require.NoError(t, os.WriteFile(path, nil, 0o644))
		_, err := stage.Execute(stageCtx, task.CurrentFiles)
		require.Error(t, err, "已完成的公开产物损坏不得报告成功或重新烧录: %s", ext)
		require.Equal(t, 1, workerCalls)
		require.NoError(t, os.WriteFile(path, original, 0o644))
	}
}

func TestSubtitleGeneratePreservesConflictingWorkCheckpoints(t *testing.T) {
	for _, damage := range []string{"unowned_video", "invalid_metadata", "wrong_session", "wrong_source", "wrong_output", "symlink", "missing_completed_video", "unfinished_video"} {
		t.Run(damage, func(t *testing.T) {
			root := t.TempDir()
			library := filepath.Join(root, "video")
			require.NoError(t, os.Mkdir(library, 0o755))
			source := filepath.Join(root, "Host - 2026-09-05 10-00-00 - Room.mp4")
			require.NoError(t, os.WriteFile(source, []byte("original"), 0o644))
			start := time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation)
			path, err := subtitle.RecordingWorkPath(library, "42", 1, 0, source, "Host", start)
			require.NoError(t, err)
			stem := strings.TrimSuffix(path, ".mp4")
			meta := subtitle.Metadata{Status: subtitle.StatusCompleted, SourcePath: source, OutputPath: path, SRTPath: stem + ".srt", ASSPath: stem + ".ass", Segments: []subtitle.Segment{{Text: "字幕"}}, RecordMeta: map[string]any{"live_session_id": "42", "live_session_media_role": "segment", "recording_producer_id": "producer", "pipeline_task_id": "1"}}
			for _, file := range []string{path, meta.SRTPath, meta.ASSPath} {
				require.NoError(t, os.WriteFile(file, []byte("checkpoint"), 0o644))
			}
			switch damage {
			case "wrong_session":
				meta.RecordMeta["live_session_id"] = "41"
			case "wrong_source":
				meta.SourcePath = "another.mp4"
			case "wrong_output":
				meta.OutputPath = source
			case "symlink":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink(source, path))
			case "missing_completed_video":
				require.NoError(t, os.Remove(path))
			case "unfinished_video":
				meta.Status = subtitle.StatusFailed
			}
			if damage != "unowned_video" {
				require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", meta))
			}
			if damage == "invalid_metadata" {
				require.NoError(t, os.WriteFile(stem+".subtitle.json", []byte("{broken"), 0o644))
			}
			before := map[string]string{}
			for _, file := range []string{source, path, stem + ".subtitle.json", meta.SRTPath, meta.ASSPath} {
				if data, err := os.ReadFile(file); err == nil {
					before[file] = string(data)
				}
			}
			calls := 0
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = w.Write([]byte(`{"segments":[{"text":"unexpected"}]}`))
			}))
			defer worker.Close()
			t.Setenv("SUBTITLE_WORKER_URL", worker.URL)
			cfg := configs.NewConfig()
			cfg.Subtitle.Enabled, cfg.Subtitle.LibraryRoot = true, library
			previous := configs.GetCurrentConfig()
			configs.SetCurrentConfig(cfg)
			defer configs.SetCurrentConfig(previous)
			stage := &SubtitleGenerateStage{}
			_, err = stage.Execute(&pipeline.PipelineContext{Ctx: context.Background(), TaskID: 1, RecordInfo: pipeline.RecordInfo{HostName: "Host", LiveSessionID: "42", RecordingProducerID: "producer", StartTime: start}, SessionMediaReady: func([]pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
				return pipeline.RecordingSession{}, nil
			}}, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
			require.Error(t, err)
			require.Equal(t, 0, calls, "归属或完成状态不明时不得调用 worker")
			for file, data := range before {
				require.Equal(t, data, string(mustReadStageFile(t, file)), "不得覆盖检查点 %s", file)
			}
		})
	}
}

func TestSubtitleGenerateDoesNotOverwriteTargetsCreatedWhileWorkerRuns(t *testing.T) {
	for _, ext := range []string{".mp4", ".srt", ".ass", ".subtitle.json"} {
		t.Run(ext, func(t *testing.T) {
			root := t.TempDir()
			library := filepath.Join(root, "video")
			require.NoError(t, os.Mkdir(library, 0o755))
			source := filepath.Join(root, "Host - 2026-09-05 10-00-00 - Room.mp4")
			require.NoError(t, os.WriteFile(source, []byte("source"), 0o644))
			start := time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation)
			path, err := subtitle.RecordingWorkPath(library, "42", 1, 0, source, "Host", start)
			require.NoError(t, err)
			conflict := strings.TrimSuffix(path, ".mp4") + ext
			workerCalls := 0
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				workerCalls++
				var request subtitle.ProcessRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.NoError(t, os.WriteFile(conflict, []byte("concurrent-owner"), 0o644))
				ass := strings.TrimSuffix(request.OutputSRTPath, ".srt") + ".ass"
				for _, output := range []string{request.OutputVideoPath, request.OutputSRTPath, ass} {
					require.NoError(t, os.WriteFile(output, []byte("worker-output"), 0o644))
				}
				require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{ASSPath: ass, Segments: []subtitle.Segment{{Text: "字幕"}}}))
			}))
			defer worker.Close()
			t.Setenv("SUBTITLE_WORKER_URL", worker.URL)
			cfg := configs.NewConfig()
			cfg.Subtitle.Enabled, cfg.Subtitle.LibraryRoot = true, library
			previous := configs.GetCurrentConfig()
			configs.SetCurrentConfig(cfg)
			defer configs.SetCurrentConfig(previous)
			stage := &SubtitleGenerateStage{}
			readyCalls := 0
			stageCtx := &pipeline.PipelineContext{Ctx: context.Background(), TaskID: 1, RecordInfo: pipeline.RecordInfo{HostName: "Host", LiveSessionID: "42", RecordingProducerID: "producer", StartTime: start}, SessionMediaReady: func([]pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
				readyCalls++
				return pipeline.RecordingSession{}, nil
			}}
			for attempt := 0; attempt < 2; attempt++ {
				_, err = stage.Execute(stageCtx, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
				require.Error(t, err)
				require.Equal(t, 0, readyCalls, "冲突后的续跑不能将外来媒体登记为成功")
				require.Equal(t, 1, workerCalls, "冲突后的续跑不能重复烧录")
				require.Equal(t, "concurrent-owner", string(mustReadStageFile(t, conflict)))
			}
		})
	}
}

func TestSubtitleGenerateDoesNotRepeatUnconfirmedWorkerResult(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			root := t.TempDir()
			library := filepath.Join(root, "video")
			require.NoError(t, os.Mkdir(library, 0o755))
			source := filepath.Join(root, "Host - 2026-09-05 10-00-00 - Room.mp4")
			require.NoError(t, os.WriteFile(source, []byte("source"), 0o644))
			calls := 0
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				http.Error(w, "upstream result unknown", status)
			}))
			defer worker.Close()
			t.Setenv("SUBTITLE_WORKER_URL", worker.URL)
			cfg := configs.NewConfig()
			cfg.Subtitle.Enabled, cfg.Subtitle.LibraryRoot = true, library
			previous := configs.GetCurrentConfig()
			configs.SetCurrentConfig(cfg)
			defer configs.SetCurrentConfig(previous)
			ctx := &pipeline.PipelineContext{Ctx: context.Background(), TaskID: 1, RecordInfo: pipeline.RecordInfo{HostName: "Host", LiveSessionID: "42", RecordingProducerID: "producer", StartTime: time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation)}, SessionMediaReady: func([]pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
				t.Error("不确定的产物不能登记就绪")
				return pipeline.RecordingSession{}, nil
			}}
			stage := &SubtitleGenerateStage{}
			for i := 0; i < 2; i++ {
				_, err := stage.Execute(ctx, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
				require.Error(t, err)
				require.Equal(t, 1, calls, "HTTP 5xx 不能证明远端烧录已经终止")
			}
		})
	}
}

func TestSubtitleGenerateResumesOnlyConfirmedAttemptWithoutReburn(t *testing.T) {
	for _, state := range []string{"partial_commit", "response_saved", "unconfirmed_video", "foreign_response"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			library := filepath.Join(root, "video")
			require.NoError(t, os.Mkdir(library, 0o755))
			source := filepath.Join(root, "Host - 2026-09-05 10-00-00 - Room.mp4")
			require.NoError(t, os.WriteFile(source, []byte("source"), 0o644))
			start := time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation)
			path, err := subtitle.RecordingWorkPath(library, "42", 1, 0, source, "Host", start)
			require.NoError(t, err)
			stem := strings.TrimSuffix(path, ".mp4")
			attemptDir, err := os.MkdirTemp(filepath.Dir(path), ".attempt-*")
			require.NoError(t, err)
			attempt := filepath.Join(attemptDir, filepath.Base(path))
			meta := subtitle.Metadata{Status: subtitle.StatusCompleted, SourcePath: source, OutputPath: path, SRTPath: stem + ".srt", ASSPath: stem + ".ass", Segments: []subtitle.Segment{{Text: "字幕"}}, RecordMeta: map[string]any{"live_session_id": "42", "recording_producer_id": "producer", "pipeline_task_id": "1", "recording_attempt_path": attempt}}
			staged := meta
			staged.OutputPath, staged.SRTPath, staged.ASSPath = attempt, strings.TrimSuffix(attempt, ".mp4")+".srt", strings.TrimSuffix(attempt, ".mp4")+".ass"
			for _, file := range []string{staged.OutputPath, staged.SRTPath, staged.ASSPath} {
				require.NoError(t, os.WriteFile(file, []byte("confirmed-output"), 0o644))
			}
			if state == "partial_commit" {
				require.NoError(t, os.Link(staged.SRTPath, meta.SRTPath))
			} else {
				meta.Status, meta.Segments = subtitle.StatusRunning, nil
			}
			if state == "foreign_response" {
				staged.SourcePath = "foreign.mp4"
			}
			if state != "unconfirmed_video" {
				require.NoError(t, subtitle.SaveMetadata(strings.TrimSuffix(attempt, ".mp4")+".subtitle.json", staged))
			}
			require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", meta))
			calls, readyCalls := 0, 0
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				http.Error(w, "must not reburn", http.StatusInternalServerError)
			}))
			defer worker.Close()
			t.Setenv("SUBTITLE_WORKER_URL", worker.URL)
			cfg := configs.NewConfig()
			cfg.Subtitle.Enabled, cfg.Subtitle.LibraryRoot = true, library
			previous := configs.GetCurrentConfig()
			configs.SetCurrentConfig(cfg)
			defer configs.SetCurrentConfig(previous)
			ctx := &pipeline.PipelineContext{Ctx: context.Background(), TaskID: 1, RecordInfo: pipeline.RecordInfo{HostName: "Host", LiveSessionID: "42", RecordingProducerID: "producer", StartTime: start}, SessionMediaReady: func(sources []pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
				readyCalls++
				require.Len(t, sources, 1)
				return pipeline.RecordingSession{}, nil
			}}
			stage := &SubtitleGenerateStage{}
			for i := 0; i < 2; i++ {
				_, err = stage.Execute(ctx, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
				if state == "unconfirmed_video" || state == "foreign_response" {
					require.Error(t, err)
					require.Zero(t, readyCalls)
				} else {
					requireRetryLater(t, err)
					require.Equal(t, i+1, readyCalls)
					require.Equal(t, "confirmed-output", string(mustReadStageFile(t, path)))
				}
				require.Zero(t, calls)
			}
		})
	}
}

func TestSessionPublicationIncludesLateEnqueuedSegmentsAndSurvivesRestartAndKnowledgeFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	library := filepath.Join(root, "video")
	require.NoError(t, os.Mkdir(library, 0o755))
	dbPath := filepath.Join(root, "pipeline.db")
	store, err := pipeline.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.OpenRecordingSession(ctx, "43", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	newTask := func(hour int) *pipeline.PipelineTask {
		start := time.Date(2026, 9, 5, hour, 0, 0, 0, mediaLibraryLocation)
		source := filepath.Join(root, fmt.Sprintf("Host - 2026-09-05 %02d-00-00 - Room.mp4", hour))
		require.NoError(t, os.WriteFile(source, []byte("recording"), 0o644))
		task := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "room", LiveSessionID: "43", RecordingProducerID: origin.ProducerID, HostName: "Host", StartTime: start}, &pipeline.PipelineConfig{Stages: []pipeline.StageConfig{{Name: pipeline.StageNameSubtitleGenerate}}}, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
		require.NoError(t, store.CreateTask(ctx, task))
		return task
	}
	first, second := newTask(10), newTask(11)
	workerCalls, knowledgeCalls := 0, 0
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/knowledge/ingest" {
			knowledgeCalls++
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		workerCalls++
		var request subtitle.ProcessRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NoError(t, os.WriteFile(request.OutputVideoPath, []byte(filepath.Base(request.SourcePath)), 0o644))
		require.NoError(t, os.WriteFile(request.OutputSRTPath, []byte("subtitle"), 0o644))
		ass := strings.TrimSuffix(request.OutputSRTPath, ".srt") + ".ass"
		require.NoError(t, os.WriteFile(ass, []byte("ass"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{ASSPath: ass, Segments: []subtitle.Segment{{Start: "00:00:00,000", End: "00:02:00,000", Text: "字幕"}}}))
	}))
	t.Cleanup(worker.Close)
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = library
	cfg.Subtitle.KnowledgeSync.Enabled = true
	cfg.Subtitle.KnowledgeSync.Endpoint = worker.URL + "/api/knowledge/ingest"
	cfg.Subtitle.KnowledgeSync.Token = "test-token"
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous) })
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)
	stubLiveSessionMediaForEpisodeList(t, []float64{120, 120, 120}, []string{"S01E1685894400000000", "S01E1685923200000000", "S01E1685952000000000"})
	stubLiveSessionCoverExtraction(t, nil)
	execute := func(task *pipeline.PipelineTask) ([]pipeline.FileInfo, error) {
		stage := &SubtitleGenerateStage{}
		return stage.Execute(&pipeline.PipelineContext{Ctx: ctx, TaskID: task.ID, RecordInfo: task.RecordInfo, SessionMediaReady: func(sources []pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
			if err := store.CompleteRecordingTaskMedia(ctx, "43", task.ID, sources); err != nil {
				return pipeline.RecordingSession{}, err
			}
			return store.RecordingSession(ctx, "43")
		}}, task.CurrentFiles)
	}
	_, err = execute(second)
	requireRetryLater(t, err)
	require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
	last := newTask(12)
	_, err = execute(last)
	requireRetryLater(t, err)
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	first.Status = pipeline.PipelineStatusFailed
	require.NoError(t, store.UpdateTask(ctx, first))
	_, err = execute(second)
	require.ErrorContains(t, err, "resume its checkpoint first")
	require.NoError(t, store.Close())
	store, err = pipeline.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	require.NoError(t, store.RecoverRecordingSessions(ctx))
	manager := pipeline.NewManager(ctx, store, nil, nil)
	t.Cleanup(func() { manager.Close(ctx) })
	require.NoError(t, manager.ResumeTask(first.ID))
	session, err := store.RecordingSession(ctx, "43")
	require.NoError(t, err)
	var prior pipeline.SessionMediaSource
	for _, source := range session.Sources() {
		if source.TaskID == second.ID {
			prior = source
		}
	}
	require.NotEmpty(t, prior.MetadataPath)
	for _, field := range []string{"pipeline_task_id", "live_session_segment_hidden_path"} {
		original := mustReadStageFile(t, prior.MetadataPath)
		meta, err := subtitle.LoadMetadata(prior.MetadataPath)
		require.NoError(t, err)
		meta.RecordMeta[field] = first.CurrentFiles[0].Path
		require.NoError(t, subtitle.SaveMetadata(prior.MetadataPath, meta))
		_, err = execute(first)
		require.Error(t, err, "其他已 Ready 分段的身份或媒体引用也必须核验")
		require.NoError(t, os.WriteFile(prior.MetadataPath, original, 0o644))
	}
	output, err := execute(first)
	require.NoError(t, err)
	require.Equal(t, 3, workerCalls)
	require.Greater(t, knowledgeCalls, 0)
	require.Len(t, visibleMP4Files(t, filepath.Dir(output[0].Path)), 1)
	require.Contains(t, string(mustReadStageFile(t, strings.TrimSuffix(output[0].Path, ".mp4")+".nfo")), "<episode>1</episode>")
	for _, task := range []*pipeline.PipelineTask{first, second, last} {
		again, err := execute(task)
		require.NoError(t, err)
		require.Equal(t, output, again)
		require.FileExists(t, task.CurrentFiles[0].Path)
	}
	require.Equal(t, 3, workerCalls)
	before := mustReadStageFile(t, output[0].Path)
	next := filepath.Join(root, "Host - 2026-09-06 10-00-00 - Next.mp4")
	require.NoError(t, os.WriteFile(next, []byte("next-recording"), 0o644))
	nextPath, err := subtitle.EnsureLibraryHardlink(ctx, next, library, "Host", time.Date(2026, 9, 6, 10, 0, 0, 0, mediaLibraryLocation), "test")
	require.NoError(t, err, "聚合后下一次普通发布必须遵循同一小集号契约")
	require.Contains(t, string(mustReadStageFile(t, strings.TrimSuffix(nextPath, ".mp4")+".nfo")), "<episode>2</episode>")
	require.Equal(t, before, mustReadStageFile(t, output[0].Path))
}

func TestSealedSessionPublicationPreservesTargetsCreatedDuringPreparation(t *testing.T) {
	for _, extension := range []string{".mp4", ".nfo", ".subtitle.json"} {
		t.Run(extension, func(t *testing.T) {
			stubLiveSessionMedia(t, []float64{3, 4})
			stubLiveSessionCoverExtraction(t, nil)
			root := t.TempDir()
			library := filepath.Join(root, "video")
			require.NoError(t, os.MkdirAll(library, 0o755))
			work := filepath.Join(root, ".live_session_segments", "processing")
			manifest := knowledgeSessionManifest{LiveSessionID: "conflict", PublicationVersion: 1}
			for _, episode := range []int64{19, 20} {
				video, metadata, _ := writeCompletedKnowledgeSessionSidecar(t, work, "Host", episode, "字幕")
				require.NoError(t, os.Remove(strings.TrimSuffix(video, ".mp4")+".jpg"))
				manifest.Sources = append(manifest.Sources, knowledgeSessionManifestSource{LibraryPath: video, MetadataPath: metadata})
			}
			var conflictingPath string
			extractCover := liveSessionMediaExtractCover
			liveSessionMediaExtractCover = func(ctx context.Context, video, output string) (string, error) {
				conflictingPath = strings.TrimSuffix(manifest.AggregatePath, ".mp4") + extension
				require.NoError(t, os.WriteFile(conflictingPath, []byte("concurrent-owner"), 0o644))
				return extractCover(ctx, video, output)
			}
			_, err := publishLiveSessionMediaAggregate(context.Background(), "", library, &manifest)
			require.NotEmpty(t, conflictingPath)
			require.Error(t, err, "不能覆盖准备期间出现的媒体或孤立 sidecar")
			require.Equal(t, "concurrent-owner", string(mustReadStageFile(t, conflictingPath)))
			for _, source := range manifest.Sources {
				require.FileExists(t, source.LibraryPath)
			}
		})
	}
}
