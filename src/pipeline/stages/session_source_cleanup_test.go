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

func TestPublishedSessionSourceCleanup(t *testing.T) {
	for _, count := range []int{1, 2} {
		for _, scenario := range []string{"immediate", "retention", "keep", "blocked"} {
			t.Run(fmt.Sprintf("segments=%d/%s", count, scenario), func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				library := filepath.Join(root, "video")
				require.NoError(t, os.Mkdir(library, 0o755))
				store, err := pipeline.NewSQLiteStore(filepath.Join(root, "pipeline.db"))
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close() })
				require.NoError(t, store.OpenRecordingSession(ctx, "42", "room"))
				origin, err := store.BeginRecordingProducer(ctx, "room")
				require.NoError(t, err)
				var files []pipeline.FileInfo
				for i := 0; i < count; i++ {
					path, err := subtitle.RecordingCapturePath(library, "42", origin.ProducerID, fmt.Sprintf("Host - 2026-09-05 %02d-00-00 - Room.mp4", 10+i))
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(path, []byte("recording"), 0o644))
					files = append(files, pipeline.NewVideoFileInfo(path))
				}
				task := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "room", LiveSessionID: "42", RecordingProducerID: origin.ProducerID, HostName: "Host", StartTime: time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation)}, &pipeline.PipelineConfig{Stages: []pipeline.StageConfig{{Name: pipeline.StageNameSubtitleGenerate}}}, files)
				require.NoError(t, store.CreateTask(ctx, task))
				stageCtx := &pipeline.PipelineContext{Ctx: ctx, TaskID: task.ID, RecordInfo: task.RecordInfo, SessionMediaReady: func(sources []pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
					if err := store.CompleteRecordingTaskMedia(ctx, "42", task.ID, sources); err != nil {
						return pipeline.RecordingSession{}, err
					}
					return store.RecordingSession(ctx, "42")
				}}
				calls := 0
				worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var request subtitle.ProcessRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					ass := strings.TrimSuffix(request.OutputSRTPath, ".srt") + ".ass"
					for _, path := range []string{request.OutputVideoPath, request.OutputSRTPath, ass} {
						require.NoError(t, os.WriteFile(path, []byte("burned"), 0o644))
					}
					require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{ASSPath: ass, Segments: []subtitle.Segment{{Start: "00:00:00,000", End: "00:00:03,000", Text: "字幕"}}}))
				}))
				t.Cleanup(worker.Close)
				t.Setenv("SUBTITLE_WORKER_URL", worker.URL)
				cfg := configs.NewConfig()
				cfg.FfmpegPath = fakeFFmpegForCover(t)
				cfg.Subtitle.Enabled, cfg.Subtitle.LibraryRoot = true, library
				cfg.Subtitle.SourceRoot = filepath.Join(root, "legacy-source")
				cfg.Subtitle.DeleteSourceOnCompletion = scenario != "retention"
				cfg.Subtitle.KnowledgeSync.Enabled = false
				previous := configs.GetCurrentConfig()
				configs.SetCurrentConfig(cfg)
				t.Cleanup(func() { configs.SetCurrentConfig(previous) })
				stubLiveSessionMediaForEpisodeList(t, []float64{3, 3}, []string{"S01E1685894400000000", "S01E1685894400000000"})
				stubLiveSessionCoverExtraction(t, nil)
				stage := &SubtitleGenerateStage{}
				_, err = stage.Execute(stageCtx, files)
				requireRetryLater(t, err)
				for _, file := range files {
					require.FileExists(t, file.Path, "封口前不能删除源文件")
				}
				if scenario == "keep" {
					session, err := store.RecordingSession(ctx, "42")
					require.NoError(t, err)
					sources := session.Sources()
					require.NoError(t, subtitle.SetKeepSource(sources[len(sources)-1].LibraryPath, true))
				}
				require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
				failure := ""
				if scenario == "blocked" {
					failure = "recording interrupted"
				}
				require.NoError(t, store.FinishRecordingProducer(ctx, origin, failure))
				output, err := stage.Execute(stageCtx, files)
				if scenario == "blocked" {
					require.ErrorContains(t, err, failure)
					for _, file := range files {
						require.FileExists(t, file.Path)
					}
					return
				}
				require.NoError(t, err)
				if scenario == "keep" {
					deleted, err := subtitle.CleanupExpiredSources(library, cfg.Subtitle.SourceRoot, 7, time.Now().Add(365*24*time.Hour))
					require.NoError(t, err)
					require.Zero(t, deleted)
					for _, file := range files {
						require.FileExists(t, file.Path)
					}
					return
				}
				if scenario == "retention" {
					for _, file := range files {
						require.FileExists(t, file.Path)
					}
					deleted, err := subtitle.CleanupExpiredSources(library, cfg.Subtitle.SourceRoot, 7, time.Now().Add(365*24*time.Hour))
					require.NoError(t, err)
					require.Equal(t, 1, deleted, "retention按公开记录计数")
				}
				for _, file := range files {
					require.NoFileExists(t, file.Path, "成功公开后应遵循源文件清理配置")
				}
				session, err := store.RecordingSession(ctx, "42")
				require.NoError(t, err)
				for _, source := range session.Sources() {
					require.FileExists(t, source.LibraryPath, "内部已烧分段必须保留")
				}
				// 成品被其他同场任务发布并清理原片后，真实续跑入口仍应可用。
				task.Status = pipeline.PipelineStatusFailed
				task.ErrorMessage = "prior publication failure"
				future := time.Now().UTC().Add(time.Hour)
				task.NotBefore = &future
				require.NoError(t, store.UpdateTask(ctx, task))
				manager := pipeline.NewManager(ctx, store, nil, nil)
				t.Cleanup(func() { manager.Close(ctx) })
				publishedMetadata := strings.TrimSuffix(output[0].Path, ".mp4") + ".subtitle.json"
				originalMetadata := mustReadStageFile(t, publishedMetadata)
				require.NoError(t, os.WriteFile(publishedMetadata, []byte("{}"), 0o644))
				require.ErrorContains(t, manager.ResumeTask(task.ID), "checkpoint input unavailable")
				require.NoError(t, os.WriteFile(publishedMetadata, originalMetadata, 0o644))
				require.NoError(t, manager.ResumeTask(task.ID))
				resumed, err := store.GetTask(ctx, task.ID)
				require.NoError(t, err)
				require.True(t, resumed.NotBefore.Equal(future))
				require.Equal(t, files, resumed.CurrentFiles)
				again, err := stage.Execute(stageCtx, files)
				require.NoError(t, err, "源已删除后重入应复用成品")
				require.Equal(t, output, again)
				require.Equal(t, count, calls, "不能重复烧录")
			})
		}
	}
}
