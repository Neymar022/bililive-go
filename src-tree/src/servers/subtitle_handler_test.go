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

func TestRegisterSubtitleHandlersDoesNotIncludeStyleLabRoutes(t *testing.T) {
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

			assert.False(t, router.Match(req, match))
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

	store := newCapturingPipelineStore()
	manager := pipeline.NewManager(context.Background(), store, pipeline.DefaultManagerConfig(), nil)
	received := make(chan []pipeline.FileInfo, 1)
	manager.RegisterStage(pipeline.StageNameSubtitleGenerate, newCaptureSubtitleGenerateStage(received))
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

	task := waitForCreatedTask(t, store.created)
	require.Len(t, task.InitialFiles, 1)
	assert.Equal(t, sourcePath, task.InitialFiles[0].Path)
	assert.Equal(t, sourcePath, task.InitialFiles[0].SourcePath)

	metadata, err := subtitle.LoadMetadata(sidecarPath)
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusQueued, metadata.Status)
	assert.True(t, metadata.KeepSource)
	assert.Equal(t, subtitle.StatusQueued, metadata.RendererStatus)
}

func TestRerunSubtitleRecordFallsBackWhenSidecarSourcePathIsStale(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(t.TempDir(), "video")
	require.NoError(t, os.MkdirAll(filepath.Join(libraryRoot, "主播", "Season 01"), 0o755))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	videoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 标题.mp4")
	require.NoError(t, os.Link(sourcePath, videoPath))
	sidecarPath := filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.subtitle.json")
	require.NoError(t, subtitle.SaveMetadata(sidecarPath, subtitle.Metadata{
		Status:         subtitle.StatusFailed,
		Provider:       "dashscope",
		SourcePath:     "/volume2/docker/bililive-go/source/主播/主播 - 2026-03-20 10-00-00 - 标题.mp4",
		OutputPath:     videoPath,
		SRTPath:        filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.srt"),
		SourceExists:   false,
		RenderPreset:   "vizard_classic_cn",
		RendererStatus: subtitle.StatusFailed,
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

	store := newCapturingPipelineStore()
	manager := pipeline.NewManager(context.Background(), store, pipeline.DefaultManagerConfig(), nil)
	received := make(chan []pipeline.FileInfo, 1)
	manager.RegisterStage(pipeline.StageNameSubtitleGenerate, newCaptureSubtitleGenerateStage(received))
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
	assert.Equal(t, sourcePath, metadata.SourcePath)
	assert.True(t, metadata.SourceExists)
	assert.Equal(t, subtitle.StatusQueued, metadata.Status)
}

func TestRerunSubtitleRecordPrefersSourcePathWhenAvailable(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(t.TempDir(), "video")
	require.NoError(t, os.MkdirAll(filepath.Join(libraryRoot, "主播", "Season 01"), 0o755))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	videoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 标题.mp4")
	require.NoError(t, os.WriteFile(videoPath, []byte("library-video"), 0o644))

	sidecarPath := filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.subtitle.json")
	require.NoError(t, subtitle.SaveMetadata(sidecarPath, subtitle.Metadata{
		Status:         subtitle.StatusFailed,
		Provider:       "dashscope",
		SourcePath:     sourcePath,
		OutputPath:     videoPath,
		SRTPath:        filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.srt"),
		SourceExists:   true,
		RenderPreset:   "vizard_classic_cn",
		RendererStatus: subtitle.StatusFailed,
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

	store := newCapturingPipelineStore()
	manager := pipeline.NewManager(context.Background(), store, pipeline.DefaultManagerConfig(), nil)
	received := make(chan []pipeline.FileInfo, 1)
	manager.RegisterStage(pipeline.StageNameSubtitleGenerate, newCaptureSubtitleGenerateStage(received))
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

	task := waitForCreatedTask(t, store.created)
	require.Len(t, task.InitialFiles, 1)
	assert.Equal(t, sourcePath, task.InitialFiles[0].Path)
	assert.Equal(t, sourcePath, task.InitialFiles[0].SourcePath)

	metadata, err := subtitle.LoadMetadata(sidecarPath)
	require.NoError(t, err)
	assert.Equal(t, sourcePath, metadata.SourcePath)
	assert.True(t, metadata.SourceExists)
	assert.Equal(t, subtitle.StatusQueued, metadata.Status)
}

func TestRerunSubtitleRecordFallsBackToLibraryVideoWhenSourceMissing(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(t.TempDir(), "video")
	require.NoError(t, os.MkdirAll(filepath.Join(libraryRoot, "主播", "Season 01"), 0o755))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 标题.mp4")
	videoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 标题.mp4")
	require.NoError(t, os.WriteFile(videoPath, []byte("library-video"), 0o644))

	sidecarPath := filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.subtitle.json")
	require.NoError(t, subtitle.SaveMetadata(sidecarPath, subtitle.Metadata{
		Status:         subtitle.StatusFailed,
		Provider:       "dashscope",
		SourcePath:     sourcePath,
		OutputPath:     videoPath,
		SRTPath:        filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.srt"),
		SourceExists:   false,
		RenderPreset:   "vizard_classic_cn",
		RendererStatus: subtitle.StatusFailed,
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

	store := newCapturingPipelineStore()
	manager := pipeline.NewManager(context.Background(), store, pipeline.DefaultManagerConfig(), nil)
	received := make(chan []pipeline.FileInfo, 1)
	manager.RegisterStage(pipeline.StageNameSubtitleGenerate, newCaptureSubtitleGenerateStage(received))
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

	task := waitForCreatedTask(t, store.created)
	require.Len(t, task.InitialFiles, 1)
	assert.Equal(t, videoPath, task.InitialFiles[0].Path)
	assert.Equal(t, sourcePath, task.InitialFiles[0].SourcePath)

	metadata, err := subtitle.LoadMetadata(sidecarPath)
	require.NoError(t, err)
	assert.Equal(t, sourcePath, metadata.SourcePath)
	assert.False(t, metadata.SourceExists)
	assert.Equal(t, subtitle.StatusQueued, metadata.Status)
}

type captureSubtitleGenerateStage struct {
	received chan []pipeline.FileInfo
}

type capturingPipelineStore struct {
	*pipeline.MemoryStore
	created chan *pipeline.PipelineTask
}

func newCapturingPipelineStore() *capturingPipelineStore {
	return &capturingPipelineStore{
		MemoryStore: pipeline.NewMemoryStore(),
		created:     make(chan *pipeline.PipelineTask, 1),
	}
}

func (s *capturingPipelineStore) CreateTask(ctx context.Context, task *pipeline.PipelineTask) error {
	if err := s.MemoryStore.CreateTask(ctx, task); err != nil {
		return err
	}
	taskCopy := *task
	s.created <- &taskCopy
	return nil
}

func newCaptureSubtitleGenerateStage(received chan []pipeline.FileInfo) pipeline.StageFactory {
	return func(config pipeline.StageConfig) (pipeline.Stage, error) {
		return &captureSubtitleGenerateStage{received: received}, nil
	}
}

func (s *captureSubtitleGenerateStage) Name() string {
	return pipeline.StageNameSubtitleGenerate
}

func (s *captureSubtitleGenerateStage) Execute(ctx *pipeline.PipelineContext, input []pipeline.FileInfo) ([]pipeline.FileInfo, error) {
	copied := append([]pipeline.FileInfo(nil), input...)
	s.received <- copied
	return input, nil
}

func waitForCreatedTask(t *testing.T, created <-chan *pipeline.PipelineTask) *pipeline.PipelineTask {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case task := <-created:
			return task
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for created pipeline task")
	return nil
}
