package subtitle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListRecordsReadsSidecarMetadata(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	videoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 标题.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0o755))
	require.NoError(t, os.WriteFile(videoPath, []byte("burned"), 0o644))
	srtPath := filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.srt")
	assPath := filepath.Join(filepath.Dir(videoPath), "主播.S01E0001.2026-03-20 - 标题.ass")
	require.NoError(t, os.WriteFile(srtPath, []byte("1\n"), 0o644))
	require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))

	completedAt := time.Unix(1_763_200_000, 0).UTC()
	require.NoError(t, SaveMetadata(sidecarPathForVideo(videoPath), Metadata{
		Status:             StatusCompleted,
		Provider:           "dashscope",
		ActualProvider:     "remote-mac-mlx",
		ActualModel:        "large-v3-turbo",
		ActualBurnProvider: "remote-mac",
		SourcePath:         sourcePath,
		OutputPath:         videoPath,
		ASSPath:            assPath,
		SRTPath:            srtPath,
		SourceExists:       true,
		RenderPreset:       "vizard_classic_cn",
		RendererStatus:     StatusCompleted,
		CompletedAt:        &completedAt,
		RecordMeta: map[string]any{
			"platform":   "抖音",
			"host_name":  "主播",
			"room_name":  "标题",
			"start_time": completedAt.Format(time.RFC3339),
		},
	}))

	records, err := ListRecords(libraryRoot, sourceRoot, 7)

	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, filepath.ToSlash("主播/Season 01/主播.S01E0001.2026-03-20 - 标题.mp4"), records[0].RelativePath)
	assert.Equal(t, StatusCompleted, records[0].Status)
	assert.Equal(t, "dashscope", records[0].Provider)
	assert.Equal(t, "remote-mac-mlx", records[0].ActualProvider)
	assert.Equal(t, "large-v3-turbo", records[0].ActualModel)
	assert.Equal(t, "remote-mac", records[0].ActualBurnProvider)
	assert.Equal(t, "抖音", records[0].Platform)
	assert.Equal(t, "2025-11-15 - 标题", records[0].DisplayTitle)
	assert.Equal(t, assPath, records[0].ASSPath)
	assert.Equal(t, "vizard_classic_cn", records[0].RenderPreset)
	assert.Equal(t, StatusCompleted, records[0].RendererStatus)
	assert.NotNil(t, records[0].RetentionDeadline)
}

func TestSetKeepSourceUpdatesMetadata(t *testing.T) {
	libraryRoot := t.TempDir()
	videoPath := filepath.Join(libraryRoot, "主播.S01E0001.2026-03-20 - 标题.mp4")
	require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0o644))
	require.NoError(t, SaveMetadata(sidecarPathForVideo(videoPath), Metadata{
		Status:     StatusCompleted,
		SourcePath: "/tmp/source.mp4",
		OutputPath: videoPath,
		ASSPath:    strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".ass",
		SRTPath:    strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt",
	}))

	require.NoError(t, SetKeepSource(videoPath, true))

	meta, err := LoadMetadata(sidecarPathForVideo(videoPath))
	require.NoError(t, err)
	assert.True(t, meta.KeepSource)
}

func TestDeleteSourceFileMarksMetadata(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "source.mp4")
	videoPath := filepath.Join(libraryRoot, "video.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))
	require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0o644))
	require.NoError(t, SaveMetadata(sidecarPathForVideo(videoPath), Metadata{
		Status:       StatusCompleted,
		SourcePath:   sourcePath,
		OutputPath:   videoPath,
		ASSPath:      strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".ass",
		SRTPath:      strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt",
		SourceExists: true,
	}))

	require.NoError(t, DeleteSourceFile(videoPath, sourceRoot))

	_, err := os.Stat(sourcePath)
	assert.Error(t, err)

	meta, err := LoadMetadata(sidecarPathForVideo(videoPath))
	require.NoError(t, err)
	assert.False(t, meta.SourceExists)
	assert.NotNil(t, meta.SourceDeletedAt)
}

func TestDeleteSourceFileRefusesLibraryVideoPath(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	videoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0o755))
	require.NoError(t, os.WriteFile(videoPath, []byte("library output"), 0o644))
	require.NoError(t, SaveMetadata(sidecarPathForVideo(videoPath), Metadata{
		Status:       StatusCompleted,
		SourcePath:   videoPath,
		OutputPath:   videoPath,
		ASSPath:      strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".ass",
		SRTPath:      strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt",
		SourceExists: true,
	}))

	err := DeleteSourceFile(videoPath, sourceRoot)

	require.ErrorIs(t, err, ErrSourceNotDeletable)
	_, statErr := os.Stat(videoPath)
	assert.NoError(t, statErr, "DeleteSourceFile must never remove the media-library product")
	meta, err := LoadMetadata(sidecarPathForVideo(videoPath))
	require.NoError(t, err)
	assert.True(t, meta.SourceExists)
	assert.Nil(t, meta.SourceDeletedAt)
}

func TestCleanupExpiredSourcesDeletesEligibleFiles(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	now := time.Unix(1_763_200_000, 0).UTC()

	makeRecord := func(name string, keep bool, ageDays int) string {
		sourcePath := filepath.Join(sourceRoot, name+".source.mp4")
		videoPath := filepath.Join(libraryRoot, name+".mp4")
		require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))
		require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0o644))
		completedAt := now.Add(-time.Duration(ageDays) * 24 * time.Hour)
		require.NoError(t, SaveMetadata(sidecarPathForVideo(videoPath), Metadata{
			Status:       StatusCompleted,
			KeepSource:   keep,
			SourcePath:   sourcePath,
			OutputPath:   videoPath,
			ASSPath:      strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".ass",
			SRTPath:      strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt",
			SourceExists: true,
			CompletedAt:  &completedAt,
		}))
		return sourcePath
	}

	oldSource := makeRecord("old", false, 9)
	keepSource := makeRecord("keep", true, 30)

	deleted, err := CleanupExpiredSources(libraryRoot, sourceRoot, 7, now)

	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	_, err = os.Stat(oldSource)
	assert.Error(t, err)
	_, err = os.Stat(keepSource)
	assert.NoError(t, err)
}

func TestCleanupExpiredSourcesSkipsVideosWithoutSidecar(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	now := time.Unix(1_763_200_000, 0).UTC()

	orphanVideoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 历史视频.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(orphanVideoPath), 0o755))
	require.NoError(t, os.WriteFile(orphanVideoPath, []byte("video"), 0o644))

	sourcePath := filepath.Join(sourceRoot, "new.source.mp4")
	videoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0002.2026-03-21 - 新视频.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))
	require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0o644))
	completedAt := now.Add(-9 * 24 * time.Hour)
	require.NoError(t, SaveMetadata(sidecarPathForVideo(videoPath), Metadata{
		Status:       StatusCompleted,
		SourcePath:   sourcePath,
		OutputPath:   videoPath,
		ASSPath:      strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".ass",
		SRTPath:      strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt",
		SourceExists: true,
		CompletedAt:  &completedAt,
	}))

	deleted, err := CleanupExpiredSources(libraryRoot, sourceRoot, 7, now)

	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	_, err = os.Stat(sourcePath)
	assert.Error(t, err)
	_, err = os.Stat(orphanVideoPath)
	assert.NoError(t, err)
}

func TestCleanupExpiredSourcesSkipsLibraryVideoSourcePath(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	now := time.Unix(1_763_200_000, 0).UTC()

	videoPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0o755))
	require.NoError(t, os.WriteFile(videoPath, []byte("library product"), 0o644))
	srtPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt"
	assPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".ass"
	completedAt := now.Add(-9 * 24 * time.Hour)
	require.NoError(t, SaveMetadata(sidecarPathForVideo(videoPath), Metadata{
		Status:       StatusCompleted,
		SourcePath:   videoPath,
		OutputPath:   videoPath,
		ASSPath:      assPath,
		SRTPath:      srtPath,
		SourceExists: true,
		CompletedAt:  &completedAt,
	}))

	deleted, err := CleanupExpiredSources(libraryRoot, sourceRoot, 7, now)

	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
	_, statErr := os.Stat(videoPath)
	assert.NoError(t, statErr, "retention cleanup must not remove media-library products")
	meta, err := LoadMetadata(sidecarPathForVideo(videoPath))
	require.NoError(t, err)
	assert.True(t, meta.SourceExists)
	assert.Nil(t, meta.SourceDeletedAt)
}
