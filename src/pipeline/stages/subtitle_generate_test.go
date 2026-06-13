package stages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubtitleGenerateStageExecute(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	var request subtitle.ProcessRequest
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NoError(t, os.WriteFile(request.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(request.OutputSRTPath, []byte("1\n00:00:00,000 --> 00:00:02,000\n测试字幕\n"), 0o644))
		require.NoError(t, os.WriteFile(strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath))+".ass", []byte("[Script Info]\n"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{
			ASSPath:            strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath)) + ".ass",
			ActualProvider:     "remote-mac-mlx",
			ActualModel:        "large-v3-turbo",
			ActualBurnProvider: "remote-mac",
			Segments: []subtitle.Segment{
				{Index: 1, Start: "00:00:00,000", End: "00:00:02,000", Text: "测试字幕"},
			},
		}))
	}))
	defer worker.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		Logger: livelogger.New(livelogger.DefaultBufferSize, nil),
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})

	require.NoError(t, err)
	require.Len(t, output, 3)
	assert.Equal(t, libraryPath, output[0].Path)
	assert.Equal(t, pipeline.FileTypeVideo, output[0].Type)

	srtPath := filepath.Join(filepath.Dir(libraryPath), "主播.S01E0001.2026-03-20 - 测试标题.srt")
	assPath := filepath.Join(filepath.Dir(libraryPath), "主播.S01E0001.2026-03-20 - 测试标题.ass")
	assert.Equal(t, srtPath, output[1].Path)
	assert.Equal(t, pipeline.FileTypeOther, output[1].Type)
	assert.Equal(t, assPath, output[2].Path)
	assert.Equal(t, pipeline.FileTypeOther, output[2].Type)

	assert.Equal(t, sourcePath, request.SourcePath)
	assert.Equal(t, libraryPath, request.OutputVideoPath)
	assert.Equal(t, srtPath, request.OutputSRTPath)

	metadata, err := subtitle.LoadMetadata(filepath.Join(filepath.Dir(libraryPath), "主播.S01E0001.2026-03-20 - 测试标题.subtitle.json"))
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusCompleted, metadata.Status)
	assert.Equal(t, sourcePath, metadata.SourcePath)
	assert.Equal(t, assPath, metadata.ASSPath)
	assert.Equal(t, srtPath, metadata.SRTPath)
	assert.Equal(t, "remote-mac-mlx", metadata.ActualProvider)
	assert.Equal(t, "large-v3-turbo", metadata.ActualModel)
	assert.Equal(t, "remote-mac", metadata.ActualBurnProvider)
	assert.Len(t, metadata.Segments, 1)

	// P11 默认 false：源文件应该还在（走传统 retention_days 后台清理路径）
	_, statErr := os.Stat(sourcePath)
	assert.NoError(t, statErr, "DeleteSourceOnCompletion 默认 false 时源文件不应被删")
	assert.True(t, metadata.SourceExists, "metadata.SourceExists 应为 true")
	assert.Nil(t, metadata.SourceDeletedAt, "未触发删除时 SourceDeletedAt 应为 nil")
}

func TestSubtitleGenerateQueuesSidecarWhenMacTranscriberUnavailable(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-05-29 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-05-29 - 测试标题.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
			"code":   "mac_transcriber_unavailable",
			"detail": "Mac 转写服务不可用，等待恢复后重试",
		}))
	}))
	defer worker.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)
	t.Setenv("SUBTITLE_RETRY_LATER_DELAY", "1ms")

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	_, err = stage.Execute(&pipeline.PipelineContext{
		Logger:     livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{HostName: "主播"},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})

	require.Error(t, err)
	var retryLater *pipeline.RetryLaterError
	require.ErrorAs(t, err, &retryLater)
	assert.Contains(t, retryLater.Error(), "Mac 转写服务不可用")

	metadata, err := subtitle.LoadMetadata(strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".subtitle.json")
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusQueued, metadata.Status)
	assert.Equal(t, subtitle.StatusQueued, metadata.RendererStatus)
	assert.Contains(t, metadata.LastError, "Mac 转写服务不可用")
	assert.Contains(t, metadata.RendererError, "Mac 转写服务不可用")
}

func TestSubtitleGenerateQueuesKnowledgeSyncAfterSubtitleSuccess(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "旭东聊装修 - 2026-05-27 20-00-00 - 装修达人。免费连麦解决装修问题。装修知识科普官.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "旭东聊装修", "Season 01", "旭东聊装修.S01E0047.2026-05-27 - 装修达人。免费连麦解决装修问题。装修知识科普官.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request subtitle.ProcessRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NoError(t, os.WriteFile(request.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(request.OutputSRTPath, []byte("1\n00:00:00,000 --> 00:00:02,500\n全屋定制报价要看抽屉数量\n"), 0o644))
		assPath := strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath)) + ".ass"
		require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{
			ASSPath: assPath,
			Segments: []subtitle.Segment{
				{Index: 1, Start: "00:00:00,000", End: "00:00:02,500", Text: "全屋定制报价要看抽屉数量"},
				{Index: 2, Start: "00:03:01,640", End: "00:03:04,000", Text: "泰州业主要先确认柜体板材"},
			},
		}))
	}))
	defer worker.Close()

	type knowledgeSegment struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	}
	type knowledgePayload struct {
		SourceID           string             `json:"source_id"`
		SourceType         string             `json:"source_type"`
		TaskID             string             `json:"task_id"`
		Host               string             `json:"host"`
		Title              string             `json:"title"`
		SourceVideoPath    string             `json:"source_video_path"`
		SubtitlePath       string             `json:"subtitle_path"`
		Language           string             `json:"language"`
		ContentHash        string             `json:"content_hash"`
		GenerateNote       bool               `json:"generate_note"`
		NonBlocking        bool               `json:"non_blocking"`
		ModelName          string             `json:"model_name"`
		ProviderID         string             `json:"provider_id"`
		Format             []string           `json:"format"`
		Link               *bool              `json:"link"`
		Screenshot         *bool              `json:"screenshot"`
		VideoUnderstanding *bool              `json:"video_understanding"`
		VideoInterval      int                `json:"video_interval"`
		GridSize           []int              `json:"grid_size"`
		Segments           []knowledgeSegment `json:"segments"`
	}

	var capturedAuth string
	var capturedPayload knowledgePayload
	knowledge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/knowledge/ingest", r.URL.Path)
		capturedAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedPayload))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]any{
				"note": map[string]any{
					"queued":  true,
					"task_id": capturedPayload.TaskID,
				},
			},
		}))
	}))
	defer knowledge.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.KnowledgeSync.Enabled = true
	cfg.Subtitle.KnowledgeSync.Endpoint = knowledge.URL + "/api/knowledge/ingest"
	cfg.Subtitle.KnowledgeSync.Token = "test-token"
	cfg.Subtitle.KnowledgeSync.ProviderID = "qwen"
	cfg.Subtitle.KnowledgeSync.ModelName = "qwen3.6-plus"
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	srtPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".srt"
	_, err = stage.Execute(&pipeline.PipelineContext{
		TaskID: 473,
		Logger: livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{
			Platform: "哔哩哔哩",
			HostName: "旭东聊装修",
			RoomName: "装修达人。免费连麦解决装修问题。装修知识科普官",
		},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})
	require.NoError(t, err)

	assert.Equal(t, "Bearer test-token", capturedAuth)
	assert.Equal(t, "旭东聊装修/Season 01/旭东聊装修.S01E0047.2026-05-27 - 装修达人。免费连麦解决装修问题。装修知识科普官.mp4", capturedPayload.SourceID)
	assert.Equal(t, "bililive-go", capturedPayload.SourceType)
	assert.Equal(t, "bililive-go-473", capturedPayload.TaskID)
	assert.Equal(t, "旭东聊装修", capturedPayload.Host)
	assert.Equal(t, "旭东聊装修.S01E0047.2026-05-27 - 装修达人。免费连麦解决装修问题。装修知识科普官", capturedPayload.Title)
	assert.Equal(t, libraryPath, capturedPayload.SourceVideoPath)
	assert.Equal(t, srtPath, capturedPayload.SubtitlePath)
	assert.Equal(t, "zh", capturedPayload.Language)
	assert.NotEmpty(t, capturedPayload.ContentHash)
	assert.True(t, capturedPayload.GenerateNote)
	assert.True(t, capturedPayload.NonBlocking)
	assert.Equal(t, "qwen3.6-plus", capturedPayload.ModelName)
	assert.Equal(t, "qwen", capturedPayload.ProviderID)
	assert.Equal(t, []string{"toc", "link", "screenshot", "summary"}, capturedPayload.Format)
	require.NotNil(t, capturedPayload.Link)
	assert.True(t, *capturedPayload.Link)
	require.NotNil(t, capturedPayload.Screenshot)
	assert.True(t, *capturedPayload.Screenshot)
	assert.Nil(t, capturedPayload.VideoUnderstanding)
	assert.Zero(t, capturedPayload.VideoInterval)
	assert.Empty(t, capturedPayload.GridSize)
	require.Len(t, capturedPayload.Segments, 2)
	assert.InDelta(t, 2.5, capturedPayload.Segments[0].End, 0.001)
	assert.InDelta(t, 181.64, capturedPayload.Segments[1].Start, 0.001)

	metadata, err := subtitle.LoadMetadata(strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".subtitle.json")
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusCompleted, metadata.Status)
	assert.Equal(t, subtitle.StatusQueued, metadata.KnowledgeSyncStatus)
	assert.Equal(t, "bililive-go-473", metadata.KnowledgeSyncTaskID)
	assert.Equal(t, capturedPayload.SourceID, metadata.KnowledgeSyncSourceID)
	assert.Equal(t, 1, metadata.KnowledgeSyncAttempts)
	assert.Empty(t, metadata.KnowledgeSyncError)
	assert.NotNil(t, metadata.KnowledgeSyncUpdatedAt)
}

func TestSubtitleGenerateSkipsKnowledgeSyncForTooShortTranscript(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "小燕子出口退税 - 2026-05-31 20-13-56 - 出口退税实操直播.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "小燕子出口退税", "Season 01", "小燕子出口退税.S01E0013.2026-05-31 - 出口退税实操直播.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request subtitle.ProcessRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NoError(t, os.WriteFile(request.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(request.OutputSRTPath, []byte("1\n00:00:00,000 --> 00:00:03,000\n短片段\n"), 0o644))
		assPath := strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath)) + ".ass"
		require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{
			ASSPath: assPath,
			Segments: []subtitle.Segment{
				{Index: 1, Start: "00:00:00,000", End: "00:00:03,000", Text: "短片段"},
			},
		}))
	}))
	defer worker.Close()

	knowledgeCalls := 0
	knowledge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeCalls++
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"ok": true}))
	}))
	defer knowledge.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.KnowledgeSync.Enabled = true
	cfg.Subtitle.KnowledgeSync.Endpoint = knowledge.URL + "/api/knowledge/ingest"
	cfg.Subtitle.KnowledgeSync.Token = "test-token"
	cfg.Subtitle.KnowledgeSync.MinVideoDurationSeconds = 3
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		TaskID: 582,
		Logger: livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{
			Platform: "哔哩哔哩",
			HostName: "小燕子出口退税",
			RoomName: "出口退税实操直播",
		},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})
	require.NoError(t, err)
	require.Len(t, output, 3)
	assert.Equal(t, 0, knowledgeCalls, "小于等于阈值的视频不应触发 BiliNote ingest")

	metadata, err := subtitle.LoadMetadata(strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".subtitle.json")
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusCompleted, metadata.Status)
	assert.Equal(t, subtitle.StatusSkipped, metadata.KnowledgeSyncStatus)
	assert.Equal(t, "bililive-go-582", metadata.KnowledgeSyncTaskID)
	assert.Contains(t, metadata.KnowledgeSyncError, "at or below minimum")
	assert.Equal(t, 0, metadata.KnowledgeSyncAttempts)
	assert.NotNil(t, metadata.KnowledgeSyncUpdatedAt)
}

func TestSubtitleGenerateDoesNotSkipKnowledgeSyncForSameLiveSessionContinuation(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "建筑师 linkai - 2026-06-01 19-10-00 - 设计师还在加班画图吗？进来看看！.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "建筑师 linkai", "Season 01", "建筑师 linkai.S01E0020.2026-06-01 - 设计师还在加班画图吗？进来看看！.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request subtitle.ProcessRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NoError(t, os.WriteFile(request.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(request.OutputSRTPath, []byte("1\n00:00:00,000 --> 00:00:03,000\n同场续段\n"), 0o644))
		assPath := strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath)) + ".ass"
		require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{
			ASSPath: assPath,
			Segments: []subtitle.Segment{
				{Index: 1, Start: "00:00:00,000", End: "00:00:03,000", Text: "同场续段"},
			},
		}))
	}))
	defer worker.Close()

	knowledgeCalls := 0
	var capturedPayload struct {
		SourceID      string `json:"source_id"`
		LiveSessionID string `json:"live_session_id"`
		ModelName     string `json:"model_name"`
		Style         string `json:"style"`
		SourceVideos  []struct {
			TaskID          string `json:"task_id"`
			SourceVideoPath string `json:"source_video_path"`
			SubtitlePath    string `json:"subtitle_path"`
		} `json:"source_videos"`
		MediaSegments []struct {
			TaskID          string `json:"task_id"`
			SourceVideoPath string `json:"source_video_path"`
			SubtitlePath    string `json:"subtitle_path"`
		} `json:"media_segments"`
	}
	knowledge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeCalls++
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedPayload))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"ok": true}))
	}))
	defer knowledge.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.KnowledgeSync.Enabled = true
	cfg.Subtitle.KnowledgeSync.Endpoint = knowledge.URL + "/api/knowledge/ingest"
	cfg.Subtitle.KnowledgeSync.Token = "test-token"
	cfg.Subtitle.KnowledgeSync.ModelName = "qwen3.7-plus"
	cfg.Subtitle.KnowledgeSync.Style = "教程"
	cfg.Subtitle.KnowledgeSync.MinVideoDurationSeconds = 600
	cfg.Subtitle.KnowledgeSync.LiveSessionQuietWindowSeconds = 0
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		TaskID: 620,
		Logger: livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{
			Platform:      "哔哩哔哩",
			HostName:      "建筑师 linkai",
			RoomName:      "设计师还在加班画图吗？进来看看！",
			LiveSessionID: "session-20260601-linkai",
		},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})
	require.NoError(t, err)
	require.Len(t, output, 3)
	assert.Equal(t, 1, knowledgeCalls, "同场直播续段不能因为自身时长低于阈值而跳过 BiliNote ingest")
	assert.Equal(t, "live-session:session-20260601-linkai", capturedPayload.SourceID)
	assert.Equal(t, "session-20260601-linkai", capturedPayload.LiveSessionID)
	assert.Equal(t, "qwen3.7-plus", capturedPayload.ModelName)
	assert.Equal(t, "教程", capturedPayload.Style)
	require.Len(t, capturedPayload.SourceVideos, 1)
	assert.Equal(t, "bililive-go-620", capturedPayload.SourceVideos[0].TaskID)
	assert.Equal(t, libraryPath, capturedPayload.SourceVideos[0].SourceVideoPath)
	require.Len(t, capturedPayload.MediaSegments, 1)
	assert.Equal(t, capturedPayload.SourceVideos, capturedPayload.MediaSegments)

	metadata, err := subtitle.LoadMetadata(strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".subtitle.json")
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusQueued, metadata.KnowledgeSyncStatus)
	assert.Equal(t, "bililive-go-620", metadata.KnowledgeSyncTaskID)
}

func TestSubtitleGenerateUsesCompletedSidecarWhenKnowledgeAggregationRetries(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	libraryPath, metadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "同场续段")

	workerCalls := 0
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls++
		http.Error(w, "worker should not be called for completed subtitle sidecar", http.StatusInternalServerError)
	}))
	defer worker.Close()

	knowledgeCalls := 0
	knowledge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeCalls++
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"ok": true}))
	}))
	defer knowledge.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.KnowledgeSync.Enabled = true
	cfg.Subtitle.KnowledgeSync.Endpoint = knowledge.URL + "/api/knowledge/ingest"
	cfg.Subtitle.KnowledgeSync.Token = "test-token"
	cfg.Subtitle.KnowledgeSync.LiveSessionQuietWindowSeconds = 0
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		TaskID: 620,
		Logger: livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{
			Platform:      "哔哩哔哩",
			HostName:      "建筑师 linkai",
			RoomName:      "设计师还在加班画图吗？进来看看！",
			LiveSessionID: "session-20260601-linkai",
		},
	}, []pipeline.FileInfo{
		{Path: libraryPath, Type: pipeline.FileTypeVideo},
	})

	require.NoError(t, err)
	require.Len(t, output, 3)
	assert.Equal(t, 0, workerCalls, "RetryLater 恢复时不应重复调用字幕 worker")
	assert.Equal(t, 1, knowledgeCalls)

	metadata, err := subtitle.LoadMetadata(metadataPath)
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusQueued, metadata.KnowledgeSyncStatus)
	assert.Equal(t, "live-session:session-20260601-linkai", metadata.KnowledgeSyncSourceID)
}

func TestSubtitleGenerateDeletesResidualSourceWhenCompletedSidecarIsReused(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("raw source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试.mp4")
	srtPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".srt"
	assPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".ass"
	metadataPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".subtitle.json"
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.WriteFile(libraryPath, []byte("burned output"), 0o644))
	require.NoError(t, os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\n测试\n"), 0o644))
	require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))
	require.NoError(t, subtitle.SaveMetadata(metadataPath, subtitle.Metadata{
		Status:         subtitle.StatusCompleted,
		SourcePath:     sourcePath,
		OutputPath:     libraryPath,
		SRTPath:        srtPath,
		ASSPath:        assPath,
		SourceExists:   true,
		RendererStatus: subtitle.StatusCompleted,
		Segments: []subtitle.Segment{
			{Index: 1, Start: "00:00:00,000", End: "00:00:01,000", Text: "测试"},
		},
	}))

	workerCalls := 0
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls++
		http.Error(w, "worker should not be called for completed subtitle sidecar", http.StatusInternalServerError)
	}))
	defer worker.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.SourceRoot = sourceRoot
	cfg.Subtitle.DeleteSourceOnCompletion = true
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		Logger: livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{
			Platform: "哔哩哔哩",
			HostName: "主播",
			RoomName: "测试",
		},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})

	require.NoError(t, err)
	require.Len(t, output, 3)
	assert.Equal(t, 0, workerCalls, "completed sidecar retry must not call subtitle worker")
	_, statErr := os.Stat(sourcePath)
	assert.True(t, os.IsNotExist(statErr), "completed sidecar retry should delete residual source when configured")
	metadata, err := subtitle.LoadMetadata(metadataPath)
	require.NoError(t, err)
	assert.False(t, metadata.SourceExists)
	assert.NotNil(t, metadata.SourceDeletedAt)
}

func TestSubtitleGenerateSkipsLibraryPublishForTooShortVideo(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "建筑师 linkai - 2026-06-01 23-31-11 - 设计师还在加班画图吗？进来看看！.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("short source"), 0o644))

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.MinLibraryVideoDurationSeconds = 60
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", "http://127.0.0.1:1")

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		FFmpegPath: fakeFFmpegForDuration(t, "00:01:00.00"),
		Logger:     livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{HostName: "建筑师 linkai"},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})

	require.NoError(t, err)
	assert.Empty(t, output, "小于等于阈值的视频不应继续进入字幕 worker 或媒体库输出")

	matches, globErr := filepath.Glob(filepath.Join(libraryRoot, "建筑师 linkai", "Season 01", "*.mp4"))
	require.NoError(t, globErr)
	assert.Empty(t, matches, "过短视频不应创建 UGREEN 可见的 S01E 媒体库条目")

	_, statErr := os.Stat(sourcePath)
	assert.NoError(t, statErr, "跳过媒体库发布时默认保留源文件，便于人工复核")
}

func TestSubtitleGenerateDoesNotSkipLibraryPublishForSameLiveSessionShard(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "建筑师 linkai - 2026-06-01 23-31-11 - 设计师还在加班画图吗？进来看看！.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("short same-live source"), 0o644))

	cfg := configs.NewConfig()
	cfg.Subtitle.MinLibraryVideoDurationSeconds = 600
	stage := &SubtitleGenerateStage{}

	skipped, _, _ := stage.shouldSkipLibraryPublish(&pipeline.PipelineContext{
		FFmpegPath: fakeFFmpegForDuration(t, "00:01:00.00"),
		RecordInfo: pipeline.RecordInfo{
			HostName:      "建筑师 linkai",
			LiveSessionID: "session-20260601-linkai",
		},
	}, cfg.Subtitle, sourcePath, libraryRoot)

	assert.False(t, skipped, "同场直播分段不应按自身时长过滤，否则无法参与最终聚合")
}

func TestSubtitleGenerateRemovesExistingLibraryLinkForTooShortVideo(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("short source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.MinLibraryVideoDurationSeconds = 60
	configs.SetCurrentConfig(cfg)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		FFmpegPath: fakeFFmpegForDuration(t, "00:01:00.00"),
		Logger:     livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{HostName: "主播"},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})

	require.NoError(t, err)
	assert.Empty(t, output)
	_, libraryErr := os.Stat(libraryPath)
	assert.True(t, os.IsNotExist(libraryErr), "已存在的短片段硬链接应从媒体库移除，避免占用 S01E 编号")
	_, sourceErr := os.Stat(sourcePath)
	assert.NoError(t, sourceErr, "移除媒体库硬链接不能删除源文件")
}

func TestBuildKnowledgePayloadPassesOptionalNoteOverrides(t *testing.T) {
	link := true
	screenshot := true
	videoUnderstanding := true
	cfg := configs.SubtitleKnowledgeSyncConfig{
		GenerateNote:       true,
		NonBlocking:        true,
		ProviderID:         "qwen",
		ModelName:          "qwen3.6-plus",
		Format:             []string{"toc", "link", "screenshot", "summary"},
		Link:               &link,
		Screenshot:         &screenshot,
		VideoUnderstanding: &videoUnderstanding,
		VideoInterval:      4,
		GridSize:           []int{3, 3},
	}
	metadata := &subtitle.Metadata{
		SRTPath:  "/video/host/e1.srt",
		Language: "zh",
		Segments: []subtitle.Segment{
			{Index: 1, Start: "00:00:00,000", End: "00:00:02,000", Text: "字幕内容"},
		},
	}

	payload, err := buildKnowledgeIngestPayload(
		&pipeline.PipelineContext{
			TaskID: 473,
			RecordInfo: pipeline.RecordInfo{
				HostName: "主播",
				RoomName: "主题",
			},
		},
		cfg,
		"/video",
		"/video/host/e1.mp4",
		metadata,
	)

	require.NoError(t, err)
	assert.Equal(t, []string{"toc", "link", "screenshot", "summary"}, payload.Format)
	require.NotNil(t, payload.Link)
	assert.True(t, *payload.Link)
	require.NotNil(t, payload.Screenshot)
	assert.True(t, *payload.Screenshot)
	require.NotNil(t, payload.VideoUnderstanding)
	assert.True(t, *payload.VideoUnderstanding)
	assert.Equal(t, 4, payload.VideoInterval)
	assert.Equal(t, []int{3, 3}, payload.GridSize)
}

func TestBuildKnowledgePayloadRespectsExplicitScreenshotDisabled(t *testing.T) {
	screenshot := false
	cfg := configs.SubtitleKnowledgeSyncConfig{
		GenerateNote: true,
		NonBlocking:  true,
		ProviderID:   "qwen",
		ModelName:    "qwen3.6-plus",
		Format:       []string{"toc", "link", "summary"},
		Screenshot:   &screenshot,
	}
	metadata := &subtitle.Metadata{
		SRTPath:  "/video/host/e1.srt",
		Language: "zh",
		Segments: []subtitle.Segment{
			{Index: 1, Start: "00:00:00,000", End: "00:00:02,000", Text: "字幕内容"},
		},
	}

	payload, err := buildKnowledgeIngestPayload(
		&pipeline.PipelineContext{TaskID: 473},
		cfg,
		"/video",
		"/video/host/e1.mp4",
		metadata,
	)

	require.NoError(t, err)
	assert.Equal(t, []string{"toc", "link", "summary"}, payload.Format)
	require.NotNil(t, payload.Screenshot)
	assert.False(t, *payload.Screenshot)
}

func TestSubtitleGenerateDoesNotFailWhenKnowledgeSyncFails(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request subtitle.ProcessRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NoError(t, os.WriteFile(request.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(request.OutputSRTPath, []byte("1\n00:00:00,000 --> 00:00:02,000\n字幕\n"), 0o644))
		assPath := strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath)) + ".ass"
		require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{
			ASSPath:  assPath,
			Segments: []subtitle.Segment{{Index: 1, Start: "00:00:00,000", End: "00:00:02,000", Text: "字幕"}},
		}))
	}))
	defer worker.Close()

	knowledge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer knowledge.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.KnowledgeSync.Enabled = true
	cfg.Subtitle.KnowledgeSync.Endpoint = knowledge.URL + "/api/knowledge/ingest"
	cfg.Subtitle.KnowledgeSync.Token = "test-token"
	cfg.Subtitle.KnowledgeSync.MinVideoDurationSeconds = 0
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		TaskID: 473,
		Logger: livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{
			HostName: "主播",
		},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})
	require.NoError(t, err, "BiliNote 同步失败不能让字幕 pipeline 失败")
	require.Len(t, output, 3)

	metadata, err := subtitle.LoadMetadata(strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".subtitle.json")
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusCompleted, metadata.Status)
	assert.Equal(t, subtitle.StatusFailed, metadata.KnowledgeSyncStatus)
	assert.Equal(t, "bililive-go-473", metadata.KnowledgeSyncTaskID)
	assert.Equal(t, 1, metadata.KnowledgeSyncAttempts)
	assert.Contains(t, metadata.KnowledgeSyncError, "502")
	assert.NotNil(t, metadata.KnowledgeSyncUpdatedAt)
}

// TestSubtitleGenerateDeletesSourceOnCompletion P11：DeleteSourceOnCompletion=true 时
// 字幕生成成功后立即删除源文件，metadata.SourceExists 为 false，SourceDeletedAt 有值。
func TestSubtitleGenerateDeletesSourceOnCompletion(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source-content"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request subtitle.ProcessRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NoError(t, os.WriteFile(request.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(request.OutputSRTPath, []byte("1\n00:00:00,000 --> 00:00:02,000\n测试\n"), 0o644))
		require.NoError(t, os.WriteFile(strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath))+".ass", []byte("[Script Info]\n"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{
			ASSPath:  strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath)) + ".ass",
			Segments: []subtitle.Segment{{Index: 1, Start: "00:00:00,000", End: "00:00:02,000", Text: "测试"}},
		}))
	}))
	defer worker.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.SourceRoot = sourceRoot
	cfg.Subtitle.DeleteSourceOnCompletion = true // ← P11 开关
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	_, err = stage.Execute(&pipeline.PipelineContext{
		Logger: livelogger.New(livelogger.DefaultBufferSize, nil),
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})
	require.NoError(t, err)

	// 关键断言：源文件已删除
	_, statErr := os.Stat(sourcePath)
	assert.True(t, os.IsNotExist(statErr), "DeleteSourceOnCompletion=true 时源文件应被删除")

	// metadata 应反映源已删除
	metadata, err := subtitle.LoadMetadata(filepath.Join(filepath.Dir(libraryPath), "主播.S01E0001.2026-03-20 - 测试.subtitle.json"))
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusCompleted, metadata.Status)
	assert.False(t, metadata.SourceExists, "源已删，metadata.SourceExists 应为 false")
	assert.NotNil(t, metadata.SourceDeletedAt, "源已删，metadata.SourceDeletedAt 应有时间戳")
}

// TestSubtitleGenerateSelfSufficientCreatesHardlink P17: 当 library 中不存在对应
// 硬链接时（cron 尚未运行），pipeline 应自动创建 Plex-style 硬链接并继续处理，
// 而不是报 "未在字幕库中找到源文件" 的错误。
func TestSubtitleGenerateSelfSufficientCreatesHardlink(t *testing.T) {
	sourceRoot := t.TempDir()
	// libraryRoot is EMPTY — cron hasn't run yet
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	// Library is completely empty — no hardlink pre-created.

	var capturedRequest subtitle.ProcessRequest
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedRequest))
		require.NoError(t, os.WriteFile(capturedRequest.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(capturedRequest.OutputSRTPath, []byte("1\n00:00:00,000 --> 00:00:02,000\n字幕\n"), 0o644))
		assPath := strings.TrimSuffix(capturedRequest.OutputSRTPath, filepath.Ext(capturedRequest.OutputSRTPath)) + ".ass"
		require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{
			ASSPath: assPath,
			Segments: []subtitle.Segment{
				{Index: 1, Start: "00:00:00,000", End: "00:00:02,000", Text: "字幕"},
			},
		}))
	}))
	defer worker.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		Logger:     livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{HostName: "主播"},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})

	require.NoError(t, err, "P17: pipeline must not fail when library entry is absent")
	require.Len(t, output, 3)

	// Verify the auto-created library path follows Plex naming convention.
	expectedLibraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	assert.Equal(t, expectedLibraryPath, output[0].Path)
	assert.Equal(t, pipeline.FileTypeVideo, output[0].Type)

	// The library entry must actually exist on disk.
	_, err = os.Stat(expectedLibraryPath)
	require.NoError(t, err, "library hardlink should exist on disk")

	// Metadata should show completed.
	metadataPath := strings.TrimSuffix(expectedLibraryPath, filepath.Ext(expectedLibraryPath)) + ".subtitle.json"
	metadata, err := subtitle.LoadMetadata(metadataPath)
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusCompleted, metadata.Status)
}

// TestSubtitleGenerateRejectsRawFlvInput ensures raw FLV files never enter
// subtitle/burn processing directly. They must pass fix_flv + convert_mp4 first;
// otherwise bad FLV streams can leave visible library videos without subtitles.
func TestSubtitleGenerateRejectsRawFlvInput(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 原始FLV.flv")
	require.NoError(t, os.WriteFile(sourcePath, []byte("raw flv"), 0o644))

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.DeleteSourceOnCompletion = true
	configs.SetCurrentConfig(cfg)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	_, err = stage.Execute(&pipeline.PipelineContext{
		Logger:     livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{HostName: "主播"},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires mp4 input")
	_, statErr := os.Stat(sourcePath)
	assert.NoError(t, statErr, "raw FLV must be preserved for repair/retry")

	matches, globErr := filepath.Glob(filepath.Join(libraryRoot, "主播", "Season 01", "*.flv"))
	require.NoError(t, globErr)
	assert.Empty(t, matches, "raw FLV must not be published to subtitle library")
}

// TestSubtitleGenerateSelfSufficientUsesExistingLink P17 幂等性测试：如果
// cron 已先创建了硬链接，pipeline 应直接使用它（不创建重复文件），正常完成。
func TestSubtitleGenerateSelfSufficientUsesExistingLink(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	// Pre-create the library hardlink — simulating that cron got there first.
	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req subtitle.ProcessRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.NoError(t, os.WriteFile(req.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(req.OutputSRTPath, []byte("1\n"), 0o644))
		assPath := strings.TrimSuffix(req.OutputSRTPath, filepath.Ext(req.OutputSRTPath)) + ".ass"
		require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{ASSPath: assPath}))
	}))
	defer worker.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	output, err := stage.Execute(&pipeline.PipelineContext{
		Logger:     livelogger.New(livelogger.DefaultBufferSize, nil),
		RecordInfo: pipeline.RecordInfo{HostName: "主播"},
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})

	require.NoError(t, err, "pipeline must succeed when cron already created the link")
	require.Len(t, output, 3)
	assert.Equal(t, libraryPath, output[0].Path, "must use the pre-existing library path")

	// Exactly one mp4 in Season 01 — no duplicate.
	seasonDir := filepath.Dir(libraryPath)
	entries, err := os.ReadDir(seasonDir)
	require.NoError(t, err)
	mp4Count := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".mp4" {
			mp4Count++
		}
	}
	assert.Equal(t, 1, mp4Count, "must not create duplicate library entry")
}

// TestSubtitleGenerateAutoDeleteSurvivesMissingSource 源已经不存在时（极端情况，
// 比如外部进程并发删了），DeleteSourceOnCompletion 不应让 pipeline 失败——只 log
// warning 然后继续。pipeline 主目标是产出字幕，删源是次要节流操作。
func TestSubtitleGenerateAutoDeleteSurvivesMissingSource(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.Link(sourcePath, libraryPath))

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request subtitle.ProcessRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NoError(t, os.WriteFile(request.OutputVideoPath, []byte("burned"), 0o644))
		require.NoError(t, os.WriteFile(request.OutputSRTPath, []byte("x"), 0o644))
		require.NoError(t, os.WriteFile(strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath))+".ass", []byte("x"), 0o644))
		// 模拟：worker 处理过程中，源文件被外部进程删了（比如运维误删）
		_ = os.Remove(sourcePath)
		require.NoError(t, json.NewEncoder(w).Encode(subtitle.ProcessResponse{
			ASSPath: strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath)) + ".ass",
		}))
	}))
	defer worker.Close()

	cfg := configs.NewConfig()
	cfg.FfmpegPath = fakeFFmpegForCover(t)
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.SourceRoot = sourceRoot
	cfg.Subtitle.DeleteSourceOnCompletion = true
	configs.SetCurrentConfig(cfg)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	stage, err := NewSubtitleGenerateStage(pipeline.StageConfig{Name: pipeline.StageNameSubtitleGenerate})
	require.NoError(t, err)

	// 关键断言：源不存在 → SourceExists=false → 跳过 DeleteSourceFile → pipeline 不报错
	_, err = stage.Execute(&pipeline.PipelineContext{
		Logger: livelogger.New(livelogger.DefaultBufferSize, nil),
	}, []pipeline.FileInfo{
		{Path: sourcePath, Type: pipeline.FileTypeVideo},
	})
	require.NoError(t, err, "源文件提前不在时不应让 pipeline 失败")

	// metadata 应该正常完成
	metadata, err := subtitle.LoadMetadata(filepath.Join(filepath.Dir(libraryPath), "主播.S01E0001.2026-03-20 - 测试.subtitle.json"))
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusCompleted, metadata.Status)
	assert.False(t, metadata.SourceExists, "外部已删，metadata.SourceExists 应反映现实")
}

func fakeFFmpegForDuration(t *testing.T, duration string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := fmt.Sprintf("#!/bin/sh\necho 'Duration: %s, start: 0.000000, bitrate: 0 kb/s' >&2\nexit 0\n", duration)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func fakeFFmpegForCover(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := `#!/bin/sh
out=""
for arg do
  out="$arg"
done
printf cover > "$out"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}
