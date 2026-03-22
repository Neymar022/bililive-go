package servers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/instance"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterSubtitleHandlersIncludesStyleLabRoutes(t *testing.T) {
	router := mux.NewRouter()
	RegisterSubtitleHandlers(router)

	testCases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "get settings", method: http.MethodGet, path: "/subtitles/style-lab/settings"},
		{name: "put settings", method: http.MethodPut, path: "/subtitles/style-lab/settings"},
		{name: "post preview", method: http.MethodPost, path: "/subtitles/style-lab/preview"},
		{name: "post sample", method: http.MethodPost, path: "/subtitles/style-lab/sample"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			match := &mux.RouteMatch{}

			assert.True(t, router.Match(req, match))
			assert.NotNil(t, match.Handler)
		})
	}
}

func TestListSubtitleRecordsHandler(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(t.TempDir(), "video")
	require.NoError(t, os.MkdirAll(filepath.Join(libraryRoot, "主播", "Season 01"), 0o755))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	videoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 标题.mp4")
	require.NoError(t, os.Link(sourcePath, videoPath))

	recordedAt := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	require.NoError(t, subtitle.SaveMetadata(
		filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.subtitle.json"),
		subtitle.Metadata{
			Status:       subtitle.StatusCompleted,
			Provider:     "dashscope",
			Language:     "zh",
			SourcePath:   sourcePath,
			OutputPath:   videoPath,
			SRTPath:      filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.srt"),
			SourceExists: true,
			RecordMeta: map[string]any{
				"platform":   "douyin",
				"host_name":  "主播",
				"room_name":  "标题",
				"start_time": recordedAt.Format(time.RFC3339),
			},
		},
	))

	cfg := configs.NewConfig()
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.SourceRoot = sourceRoot
	cfg.Subtitle.LibraryRoot = libraryRoot
	configs.SetCurrentConfig(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/subtitles/records", nil)
	recorder := httptest.NewRecorder()

	listSubtitleRecords(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var resp commonResp
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.ErrNo)

	payload, err := json.Marshal(resp.Data)
	require.NoError(t, err)

	var records []subtitle.Record
	require.NoError(t, json.Unmarshal(payload, &records))
	require.Len(t, records, 1)
	assert.Equal(t, "主播", records[0].HostName)
	assert.Equal(t, "标题", records[0].RoomName)
	assert.Equal(t, subtitle.StatusCompleted, records[0].Status)
}

func TestPutSubtitleSettingsHandler(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(t.TempDir(), "video")
	require.NoError(t, os.MkdirAll(libraryRoot, 0o755))

	configFile := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(configFile, []byte("rpc:\n  enable: true\nlive_rooms: []\n"), 0o644))

	cfg, err := configs.NewConfigWithFile(configFile)
	require.NoError(t, err)
	cfg.OutPutPath = sourceRoot
	cfg.File = configFile
	cfg.Subtitle.SourceRoot = sourceRoot
	cfg.Subtitle.LibraryRoot = libraryRoot
	configs.SetCurrentConfig(cfg)

	body := struct {
		Subtitle configs.SubtitleConfig `json:"subtitle"`
	}{
		Subtitle: configs.SubtitleConfig{
			Enabled:         true,
			AutoGenerate:    true,
			DefaultProvider: "dashscope",
			SourceRoot:      sourceRoot,
			LibraryRoot:     libraryRoot,
			PublicURLBase:   "https://bililive.example.com",
			RetentionDays:   14,
			Language:        "zh",
			Local: configs.SubtitleLocalConfig{
				Model:       "small",
				ComputeType: "int8",
			},
			Cloud: configs.SubtitleCloudConfig{
				Vendor: "aliyun",
				Model:  "qwen3-asr-flash-filetrans",
			},
			BurnStyle: configs.SubtitleBurnStyle{
				Preset:   "bottom_center",
				FontName: "Noto Sans CJK SC",
				FontSize: 26,
				MarginV:  28,
				Outline:  2,
				Shadow:   0,
			},
		},
	}
	bodyBytes, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/subtitles/settings", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	putSubtitleSettings(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var resp commonResp
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.ErrNo)

	updated := configs.GetCurrentConfig()
	require.NotNil(t, updated)
	assert.True(t, updated.Subtitle.Enabled)
	assert.Equal(t, 14, updated.Subtitle.RetentionDays)
	assert.Equal(t, "https://bililive.example.com", updated.Subtitle.PublicURLBase)

	content, err := os.ReadFile(configFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "subtitle:")
	assert.Contains(t, string(content), "retention_days: 14")
}

func TestGetSubtitleStyleLabSettingsHandler(t *testing.T) {
	cfg := configs.NewConfig()
	cfg.Subtitle.BurnStyle.Preset = "vizard_classic_cn"
	cfg.Subtitle.BurnStyle.FontSize = 50
	configs.SetCurrentConfig(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/subtitles/style-lab/settings", nil)
	recorder := httptest.NewRecorder()

	getSubtitleStyleLabSettings(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var resp commonResp
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.ErrNo)

	payload, err := json.Marshal(resp.Data)
	require.NoError(t, err)

	var data struct {
		BurnStyle configs.SubtitleBurnStyle `json:"burn_style"`
	}
	require.NoError(t, json.Unmarshal(payload, &data))
	assert.Equal(t, "vizard_classic_cn", data.BurnStyle.Preset)
	assert.Equal(t, 50, data.BurnStyle.FontSize)
}

func TestPutSubtitleStyleLabSettingsHandler(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(configFile, []byte("rpc:\n  enable: true\nlive_rooms: []\n"), 0o644))

	cfg, err := configs.NewConfigWithFile(configFile)
	require.NoError(t, err)
	cfg.File = configFile
	cfg.Subtitle.BurnStyle.Preset = "vizard_classic_cn"
	cfg.Subtitle.BurnStyle.FontSize = 35
	configs.SetCurrentConfig(cfg)

	bodyBytes, err := json.Marshal(map[string]any{
		"burn_style": map[string]any{
			"preset":    "vizard_classic_cn",
			"font_name": "Noto Sans CJK SC",
			"font_size": 50,
			"margin_v":  24,
			"outline":   2,
			"shadow":    0,
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/subtitles/style-lab/settings", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	putSubtitleStyleLabSettings(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var resp commonResp
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.ErrNo)

	updated := configs.GetCurrentConfig()
	require.NotNil(t, updated)
	assert.Equal(t, 50, updated.Subtitle.BurnStyle.FontSize)

	content, err := os.ReadFile(configFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "font_size: 50")
}

func TestPreviewSubtitleStyleLabHandler(t *testing.T) {
	var workerRequest subtitle.StyleLabPreviewRequest
	worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1/style-lab/preview", req.URL.Path)
		require.NoError(t, json.NewDecoder(req.Body).Decode(&workerRequest))
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(subtitle.StyleLabPreviewResponse{
			PreviewImagePath: "/tmp/preview.png",
			RenderPreset:     "vizard_classic_cn",
		}))
	}))
	t.Cleanup(worker.Close)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	cfg := configs.NewConfig()
	cfg.Subtitle.BurnStyle.Preset = "bottom_center"
	configs.SetCurrentConfig(cfg)

	bodyBytes, err := json.Marshal(map[string]any{
		"source_path":  "/tmp/source.mp4",
		"preview_text": "测试预览",
		"burn_style": map[string]any{
			"preset":    "bottom_center",
			"font_name": "Noto Sans CJK SC",
			"font_size": 50,
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/subtitles/style-lab/preview", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	previewSubtitleStyleLab(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var resp commonResp
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.ErrNo)
	payload, err := json.Marshal(resp.Data)
	require.NoError(t, err)

	var data subtitle.StyleLabPreviewResponse
	require.NoError(t, json.Unmarshal(payload, &data))
	assert.Equal(t, "/tmp/preview.png", data.PreviewImagePath)
	assert.Equal(t, "vizard_classic_cn", workerRequest.BurnStyle.Preset)
	assert.Equal(t, "测试预览", workerRequest.PreviewText)
}

func TestSampleSubtitleStyleLabHandler(t *testing.T) {
	var workerRequest subtitle.StyleLabSampleRequest
	worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1/style-lab/sample", req.URL.Path)
		require.NoError(t, json.NewDecoder(req.Body).Decode(&workerRequest))
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(subtitle.StyleLabSampleResponse{
			SampleVideoPath: "/tmp/sample.burned.mp4",
			SampleSRTPath:   "/tmp/sample.srt",
			RenderPreset:    "vizard_classic_cn",
		}))
	}))
	t.Cleanup(worker.Close)
	t.Setenv("SUBTITLE_WORKER_URL", worker.URL)

	cfg := configs.NewConfig()
	cfg.Subtitle.BurnStyle.Preset = "bottom_center"
	configs.SetCurrentConfig(cfg)

	bodyBytes, err := json.Marshal(map[string]any{
		"source_path":          "/tmp/source.mp4",
		"sample_text":          "测试样片",
		"duration_seconds":     30,
		"start_time_seconds":   5,
		"output_dir":           "/tmp/.style-lab-samples",
		"burn_style": map[string]any{
			"preset":    "bottom_center",
			"font_name": "Noto Sans CJK SC",
			"font_size": 50,
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/subtitles/style-lab/sample", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	sampleSubtitleStyleLab(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var resp commonResp
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.ErrNo)
	payload, err := json.Marshal(resp.Data)
	require.NoError(t, err)

	var data subtitle.StyleLabSampleResponse
	require.NoError(t, json.Unmarshal(payload, &data))
	assert.Equal(t, "/tmp/sample.burned.mp4", data.SampleVideoPath)
	assert.Equal(t, "/tmp/sample.srt", data.SampleSRTPath)
	assert.Equal(t, "vizard_classic_cn", workerRequest.BurnStyle.Preset)
	assert.Equal(t, "测试样片", workerRequest.SampleText)
}

func TestRerunSubtitleRecordPreservesKeepSourceInSidecar(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(t.TempDir(), "video")
	require.NoError(t, os.MkdirAll(filepath.Join(libraryRoot, "主播", "Season 01"), 0o755))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	videoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 标题.mp4")
	require.NoError(t, os.Link(sourcePath, videoPath))
	sidecarPath := filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.subtitle.json")
	require.NoError(t, subtitle.SaveMetadata(sidecarPath, subtitle.Metadata{
		Status:         subtitle.StatusCompleted,
		Provider:       "dashscope",
		SourcePath:     sourcePath,
		OutputPath:     videoPath,
		SRTPath:        filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.srt"),
		KeepSource:     true,
		SourceExists:   true,
		RenderPreset:   "vizard_classic_cn",
		RendererStatus: subtitle.StatusCompleted,
		RecordMeta: map[string]any{
			"platform":   "抖音",
			"host_name":  "主播",
			"room_name":  "标题",
			"start_time": time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
		},
	}))

	cfg := configs.NewConfig()
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.SourceRoot = sourceRoot
	cfg.Subtitle.LibraryRoot = libraryRoot
	configs.SetCurrentConfig(cfg)

	store := pipeline.NewMemoryStore()
	manager := pipeline.NewManager(context.Background(), store, pipeline.DefaultManagerConfig(), nil)
	inst := &instance.Instance{PipelineManager: manager}

	req := httptest.NewRequest(http.MethodPost, "/api/subtitles/records/主播/Season%2001/主播.S01E0001.2026-03-20%20-%20标题.mp4/rerun", bytes.NewReader([]byte(`{}`)))
	req = mux.SetURLVars(req, map[string]string{
		"path": "主播/Season 01/主播.S01E0001.2026-03-20 - 标题.mp4",
	})
	req = req.WithContext(context.WithValue(req.Context(), instance.Key, inst))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	rerunSubtitleRecord(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)

	metadata, err := subtitle.LoadMetadata(sidecarPath)
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusQueued, metadata.Status)
	assert.True(t, metadata.KeepSource)
	assert.Equal(t, subtitle.StatusQueued, metadata.RendererStatus)
}
