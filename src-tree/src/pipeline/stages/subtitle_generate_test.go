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
