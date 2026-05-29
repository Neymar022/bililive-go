package stages

import (
	"encoding/json"
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
			ASSPath: strings.TrimSuffix(request.OutputSRTPath, filepath.Ext(request.OutputSRTPath)) + ".ass",
			Segments: []subtitle.Segment{
				{Index: 1, Start: "00:00:00,000", End: "00:00:02,000", Text: "测试字幕"},
			},
		}))
	}))
	defer worker.Close()

	cfg := configs.NewConfig()
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
	assert.Len(t, metadata.Segments, 1)

	// P11 默认 false：源文件应该还在（走传统 retention_days 后台清理路径）
	_, statErr := os.Stat(sourcePath)
	assert.NoError(t, statErr, "DeleteSourceOnCompletion 默认 false 时源文件不应被删")
	assert.True(t, metadata.SourceExists, "metadata.SourceExists 应为 true")
	assert.Nil(t, metadata.SourceDeletedAt, "未触发删除时 SourceDeletedAt 应为 nil")
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
	assert.Empty(t, capturedPayload.Format)
	assert.Nil(t, capturedPayload.Link)
	assert.Nil(t, capturedPayload.Screenshot)
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
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.KnowledgeSync.Enabled = true
	cfg.Subtitle.KnowledgeSync.Endpoint = knowledge.URL + "/api/knowledge/ingest"
	cfg.Subtitle.KnowledgeSync.Token = "test-token"
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
