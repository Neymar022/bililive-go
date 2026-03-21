package subtitle

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveLibraryVideoPathByHardlink(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(t.TempDir(), "video")
	require.NoError(t, os.MkdirAll(filepath.Join(libraryRoot, "主播", "Season 01"), 0o755))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("video"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	require.NoError(t, os.Link(sourcePath, libraryPath))

	resolved, err := ResolveLibraryVideoPath(sourcePath, libraryRoot)

	require.NoError(t, err)
	assert.Equal(t, libraryPath, resolved)
}

func TestResolveLibraryVideoPathPrefersMetadataWhenLibraryVideoWasReplaced(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(t.TempDir(), "video")
	require.NoError(t, os.MkdirAll(filepath.Join(libraryRoot, "主播", "Season 01"), 0o755))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(libraryPath, []byte("burned"), 0o644))
	require.NoError(t, SaveMetadata(sidecarPathForVideo(libraryPath), Metadata{
		Status:       StatusCompleted,
		SourcePath:   sourcePath,
		OutputPath:   libraryPath,
		SRTPath:      filepath.Join(filepath.Dir(libraryPath), "主播.S01E0001.2026-03-20 - 测试标题.srt"),
		SourceExists: true,
	}))

	resolved, err := ResolveLibraryVideoPath(sourcePath, libraryRoot)

	require.NoError(t, err)
	assert.Equal(t, libraryPath, resolved)
}

func TestResolveSourcePathPrefersMetadata(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	libraryPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.WriteFile(libraryPath, []byte("video"), 0o644))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	metadata := Metadata{
		Status:     StatusCompleted,
		SourcePath: sourcePath,
	}
	require.NoError(t, SaveMetadata(sidecarPathForVideo(libraryPath), metadata))

	resolved, err := ResolveSourcePath(libraryPath, sourceRoot)

	require.NoError(t, err)
	assert.Equal(t, sourcePath, resolved)
}

func TestShouldDeleteSourceAfterRetention(t *testing.T) {
	now := time.Unix(1_763_200_000, 0).UTC()
	completedAt := now.Add(-8 * 24 * time.Hour)

	meta := Metadata{
		Status:       StatusCompleted,
		KeepSource:   false,
		SourcePath:   "/tmp/source.mp4",
		CompletedAt:  &completedAt,
		SRTPath:      "/tmp/video.srt",
		OutputPath:   "/tmp/video.mp4",
		SourceExists: true,
	}

	assert.True(t, meta.ShouldDeleteSource(now, 7))
}

func TestShouldNotDeleteSourceWhenMarkedKeep(t *testing.T) {
	now := time.Unix(1_763_200_000, 0).UTC()
	completedAt := now.Add(-30 * 24 * time.Hour)

	meta := Metadata{
		Status:       StatusCompleted,
		KeepSource:   true,
		SourcePath:   "/tmp/source.mp4",
		CompletedAt:  &completedAt,
		SRTPath:      "/tmp/video.srt",
		OutputPath:   "/tmp/video.mp4",
		SourceExists: true,
	}

	assert.False(t, meta.ShouldDeleteSource(now, 7))
}

func TestSaveMetadataPersistsRendererFields(t *testing.T) {
	metadataPath := filepath.Join(t.TempDir(), "episode.subtitle.json")
	completedAt := time.Unix(1_763_200_000, 0).UTC()

	require.NoError(t, SaveMetadata(metadataPath, Metadata{
		Status:         StatusCompleted,
		Provider:       "dashscope",
		SourcePath:     "/tmp/source.mp4",
		OutputPath:     "/tmp/video.mp4",
		SRTPath:        "/tmp/video.srt",
		SourceExists:   true,
		RenderPreset:   "vizard_classic_cn",
		RendererStatus: StatusCompleted,
		CompletedAt:    &completedAt,
	}))

	loaded, err := LoadMetadata(metadataPath)
	require.NoError(t, err)
	assert.Equal(t, "vizard_classic_cn", loaded.RenderPreset)
	assert.Equal(t, StatusCompleted, loaded.RendererStatus)
}
