package subtitle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnsureLibraryHardlinkIgnoresOrphanSidecarsWithoutOverwritingThem(t *testing.T) {
	stubCoverExtraction(t)
	root := t.TempDir()
	season := filepath.Join(root, "Host", "Season 01")
	require.NoError(t, os.MkdirAll(season, 0o755))
	orphans := map[string]string{}
	for _, title := range []string{"OldA", "OldB"} {
		stem := filepath.Join(season, "Host.S01E0029.2026-07-08 - "+title)
		orphans[stem+".ass"] = "[Script Info]\n"
		orphans[stem+".nfo"] = "<episodedetails><episode>29</episode></episodedetails>"
	}
	for path, content := range orphans {
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	source := filepath.Join(t.TempDir(), "Host - 2026-09-05 12-00-00 - New.mp4")
	require.NoError(t, os.WriteFile(source, []byte("source"), 0o644))
	recordedAt := time.Date(2026, 9, 5, 12, 0, 0, 0, mediaLibraryLocation)
	target, err := EnsureLibraryHardlink(context.Background(), source, root, "Host", recordedAt, "test")
	require.NoError(t, err)
	nfo, err := os.ReadFile(sidecarStem(target) + ".nfo")
	require.NoError(t, err)
	require.Contains(t, string(nfo), "<episode>1</episode>")
	for path, expected := range orphans {
		actual, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, expected, string(actual))
	}
}

func TestEnsureLibraryHardlinkRejectsMultipleMediaForSameIdentity(t *testing.T) {
	stubCoverExtraction(t)
	library, sourceRoot := t.TempDir(), t.TempDir()
	start := time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation)
	first := filepath.Join(sourceRoot, "Host - 2026-09-05 10-00-00 - First.mp4")
	second := filepath.Join(sourceRoot, "Host - 2026-09-05 11-00-00 - Second.mp4")
	require.NoError(t, os.WriteFile(first, []byte("first"), 0o644))
	require.NoError(t, os.WriteFile(second, []byte("second"), 0o644))
	path, err := EnsureLibraryHardlink(context.Background(), first, library, "Host", start, "test")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sidecarStem(path)+".mkv", []byte("another media"), 0o644))
	_, err = EnsureLibraryHardlink(context.Background(), second, library, "Host", start.Add(time.Hour), "test")
	require.ErrorContains(t, err, "multiple media")
}

func TestEnsureLibraryHardlinkBlocksConflictingPublishedIdentity(t *testing.T) {
	stubCoverExtraction(t)
	library := t.TempDir()
	root := t.TempDir()
	first := filepath.Join(root, "Host - 2026-09-05 10-00-00 - First.mp4")
	second := filepath.Join(root, "Host - 2026-09-05 11-00-00 - Second.mp4")
	require.NoError(t, os.WriteFile(first, []byte("first"), 0o644))
	require.NoError(t, os.WriteFile(second, []byte("second"), 0o644))
	start := time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation)
	published, err := EnsureLibraryHardlink(context.Background(), first, library, "Host", start, "test")
	require.NoError(t, err)
	nfoPath := sidecarStem(published) + ".nfo"
	nfo, err := os.ReadFile(nfoPath)
	require.NoError(t, err)
	corrupt := strings.Replace(string(nfo), `default="false">1685894400000000`, `default="false">1685894400000008`, 1)
	require.NotEqual(t, string(nfo), corrupt)
	require.NoError(t, os.WriteFile(nfoPath, []byte(corrupt), 0o644))
	_, err = EnsureLibraryHardlink(context.Background(), second, library, "Host", start.Add(time.Hour), "test")
	require.ErrorContains(t, err, "identity")
	require.Equal(t, corrupt, string(mustReadFile(t, nfoPath)))
	require.FileExists(t, published)
}

func TestBuildLibraryEpisodeNFORejectsRecordingTimeIdentityMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Host.S01E1685894400000000.2026-09-05 - Room.mp4")
	_, err := BuildLibraryEpisodeNFO(path, time.Date(2026, 9, 5, 11, 0, 0, 0, mediaLibraryLocation), "test")
	require.ErrorContains(t, err, "identity")
}

func TestRecordingWorkPathRejectsSymlinkBeforeCreatingDirectories(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "video")
	require.NoError(t, os.Mkdir(library, 0o755))
	require.NoError(t, os.Symlink(library, filepath.Join(root, ".live_session_segments")))
	_, err := RecordingWorkPath(library, "session", 1, 0, "input.mp4", "Host", time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation))
	require.Error(t, err)
	entries, err := os.ReadDir(library)
	require.NoError(t, err)
	require.Empty(t, entries, "检查失败前不得在媒体库留下工作目录")
}

func TestLibraryPublicationSerializesSymlinkAliases(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Mkdir(real, 0o755))
	require.NoError(t, os.Symlink(real, alias))
	// 季目录尚不存在时，两种库根也必须得到同一把发布锁。
	unlock := LockLibraryPublication(filepath.Join(real, "Host", "Season 01", "first.mp4"))
	acquired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		release := LockLibraryPublication(filepath.Join(alias, "Host", "Season 01", "second.mp4"))
		defer release()
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Error("别名路径绕过了同一合集的发布锁")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("释放后未继续发布")
	}
}
