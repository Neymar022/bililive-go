package subtitle

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// referenceTime is a stable timestamp used across the hardlink tests.
var referenceTime = time.Date(2026, 3, 20, 10, 0, 0, 0, time.Local)

// TestEnsureLibraryHardlink_CreatesNewLink verifies that a hardlink is created
// with the correct Plex-style path when the Season 01 directory is empty.
func TestEnsureLibraryHardlink_CreatesNewLink(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(sourcePath, libraryRoot, "主播", referenceTime)
	require.NoError(t, err)

	// Target path must follow the Plex naming convention.
	expectedPath := filepath.Join(libraryRoot, "主播", "Season 01", "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	assert.Equal(t, expectedPath, targetPath)

	// File must exist on disk.
	_, err = os.Stat(targetPath)
	require.NoError(t, err, "hardlinked file should exist")

	// Source and target must share the same inode (true hardlink).
	srcInfo, err := os.Stat(sourcePath)
	require.NoError(t, err)
	dstInfo, err := os.Stat(targetPath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(srcInfo, dstInfo), "source and target must be the same inode")
}

// TestEnsureLibraryHardlink_IdempotentSameInode verifies that calling
// EnsureLibraryHardlink a second time when the target already exists with the
// same inode returns the path without error and without creating a duplicate.
func TestEnsureLibraryHardlink_IdempotentSameInode(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	// First call — creates the link.
	targetPath1, err := EnsureLibraryHardlink(sourcePath, libraryRoot, "主播", referenceTime)
	require.NoError(t, err)

	// Second call — must return same path without error.
	targetPath2, err := EnsureLibraryHardlink(sourcePath, libraryRoot, "主播", referenceTime)
	require.NoError(t, err)
	assert.Equal(t, targetPath1, targetPath2, "second call must return same path")

	// Only one mp4 should exist under Season 01.
	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	entries, err := os.ReadDir(seasonDir)
	require.NoError(t, err)
	mp4Count := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".mp4" {
			mp4Count++
		}
	}
	assert.Equal(t, 1, mp4Count, "idempotent call must not create duplicate file")
}

// TestEnsureLibraryHardlink_ExistingUnrelatedFileInSlot verifies that if an
// unrelated file already occupies the computed episode slot (different inode),
// EnsureLibraryHardlink creates the next available slot (E0002) and does NOT
// overwrite the existing file at E0001.
//
// In practice this path is rarely reached: if cron had created a true hardlink
// at E0001, ResolveLibraryVideoPath would have found it (same inode) and we'd
// never call EnsureLibraryHardlink.  This test guards against edge cases where
// an unrelated file happens to occupy the slot.
func TestEnsureLibraryHardlink_ExistingUnrelatedFileInSlot(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	// Pre-create an unrelated file at E0001 (different content/inode).
	unrelatedPath := filepath.Join(seasonDir, "主播.S01E0001.2026-03-19 - 其他.mp4")
	require.NoError(t, os.WriteFile(unrelatedPath, []byte("unrelated"), 0o644))

	returnedPath, err := EnsureLibraryHardlink(sourcePath, libraryRoot, "主播", referenceTime)
	require.NoError(t, err)

	// EnsureLibraryHardlink counts 1 existing mp4 → assigns E0002.
	expectedTarget := filepath.Join(seasonDir, "主播.S01E0002.2026-03-20 - 测试.mp4")
	assert.Equal(t, expectedTarget, returnedPath)

	// E0001 must be untouched.
	content, err := os.ReadFile(unrelatedPath)
	require.NoError(t, err)
	assert.Equal(t, "unrelated", string(content), "EnsureLibraryHardlink must not overwrite pre-existing file")

	// New hardlink must exist and share inode with source.
	srcInfo, err := os.Stat(sourcePath)
	require.NoError(t, err)
	dstInfo, err := os.Stat(expectedTarget)
	require.NoError(t, err)
	assert.True(t, os.SameFile(srcInfo, dstInfo))
}

// TestEnsureLibraryHardlink_EpisodeNumbering verifies that when N mp4 files
// already exist in Season 01, a new call assigns episode N+1.
func TestEnsureLibraryHardlink_EpisodeNumbering(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	// Pre-create 2 existing episodes so the next one should be E0003.
	require.NoError(t, os.WriteFile(filepath.Join(seasonDir, "主播.S01E0001.2026-03-18 - 第一集.mp4"), []byte("ep1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(seasonDir, "主播.S01E0002.2026-03-19 - 第二集.mp4"), []byte("ep2"), 0o644))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 第三集.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(sourcePath, libraryRoot, "主播", referenceTime)
	require.NoError(t, err)

	expectedPath := filepath.Join(seasonDir, "主播.S01E0003.2026-03-20 - 第三集.mp4")
	assert.Equal(t, expectedPath, targetPath)
}

// TestEnsureLibraryHardlink_DoesNotReuseEpisodeWithSidecars verifies that
// completed subtitle sidecars reserve an episode slot even if the rendered mp4
// was removed by cleanup. Reusing that slot would attach old subtitles to a new
// source recording.
func TestEnsureLibraryHardlink_DoesNotReuseEpisodeWithSidecars(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	stem := filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 同名直播")
	require.NoError(t, os.WriteFile(stem+".srt", []byte("old subtitles"), 0o644))
	require.NoError(t, os.WriteFile(stem+".ass", []byte("old ass"), 0o644))
	require.NoError(t, os.WriteFile(stem+".subtitle.json", []byte(`{"status":"completed"}`), 0o644))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 11-00-00 - 同名直播.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("new source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(sourcePath, libraryRoot, "主播", referenceTime)
	require.NoError(t, err)

	expectedPath := filepath.Join(seasonDir, "主播.S01E0002.2026-03-20 - 同名直播.mp4")
	assert.Equal(t, expectedPath, targetPath)
	_, err = os.Stat(stem + ".mp4")
	assert.True(t, os.IsNotExist(err), "old sidecar slot must not receive the new video")
}

// TestBuildEpisodeFilename_Basic checks the filename builder directly.
func TestBuildEpisodeFilename_Basic(t *testing.T) {
	ts := time.Date(2026, 3, 20, 10, 0, 0, 0, time.Local)
	name := buildEpisodeFilename("主播", 1, ts, "测试标题", ".mp4")
	assert.Equal(t, "主播.S01E0001.2026-03-20 - 测试标题.mp4", name)
}

// TestParseSourceFilename_Normalized verifies that normalized filenames are
// parsed correctly.
func TestParseSourceFilename_Normalized(t *testing.T) {
	ts := time.Date(2026, 3, 20, 10, 0, 0, 0, time.Local)
	meta := parseSourceFilename("/some/dir/主播 - 2026-03-20 10-00-00 - 测试标题.mp4", "fallback", ts)
	assert.Equal(t, "主播", meta.aliasName)
	assert.Equal(t, "测试标题", meta.title)
	assert.Equal(t, "2026-03-20", meta.recordedAt.Format("2006-01-02"))
}

// TestParseSourceFilename_Fallback verifies that unrecognised filenames fall
// back to the RecordInfo host/time values.
func TestParseSourceFilename_Fallback(t *testing.T) {
	ts := time.Date(2026, 3, 20, 10, 0, 0, 0, time.Local)
	meta := parseSourceFilename("/some/dir/unrecognised-filename.mp4", "fallback-host", ts)
	assert.Equal(t, "fallback-host", meta.aliasName)
	assert.Equal(t, "未命名直播", meta.title)
}
