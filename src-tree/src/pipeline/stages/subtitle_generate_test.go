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
}
