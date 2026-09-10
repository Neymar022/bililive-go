package subtitle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func publishedRecordingCleanupFixture(t *testing.T) (string, Metadata) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	library := filepath.Join(root, "video")
	season := filepath.Join(library, "Host", "Season 01")
	require.NoError(t, os.MkdirAll(season, 0o755))
	video := filepath.Join(season, "Host.S01E1685894400000000.2026-09-05 - Room.mp4")
	stem := strings.TrimSuffix(video, ".mp4")
	now := time.Now().UTC()
	meta := Metadata{Status: StatusCompleted, OutputPath: video, SRTPath: stem + ".srt", ASSPath: stem + ".ass", CompletedAt: &now, SourceExists: true, RecordMeta: map[string]any{"live_session_id": "42", "live_session_media_role": "aggregate"}}
	for i, name := range []string{"first.mp4", "second.mp4"} {
		source, err := RecordingCapturePath(library, "42", "producer", name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(source, []byte("source"), 0o644))
		work, err := RecordingWorkPath(library, "42", 1, i, source, "Host", time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation))
		require.NoError(t, err)
		checkpoint := Metadata{Status: StatusCompleted, SourcePath: source, OutputPath: work, SRTPath: strings.TrimSuffix(work, ".mp4") + ".srt", ASSPath: strings.TrimSuffix(work, ".mp4") + ".ass", RecordMeta: map[string]any{"live_session_id": "42", "recording_producer_id": "producer"}}
		for _, path := range []string{checkpoint.OutputPath, checkpoint.SRTPath, checkpoint.ASSPath} {
			require.NoError(t, os.WriteFile(path, []byte("burned"), 0o644))
		}
		require.NoError(t, SaveMetadata(sidecarPathForVideo(work), checkpoint))
		item, err := CaptureRecordingSource(library, "42", checkpoint)
		require.NoError(t, err)
		meta.RecordingSources = append(meta.RecordingSources, item)
	}
	meta.SourcePath = meta.RecordingSources[0].Path
	nfo, err := BuildLibraryEpisodeNFO(video, time.Date(2026, 9, 5, 10, 0, 0, 0, mediaLibraryLocation), "test")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(stem+".nfo", []byte(nfo), 0o644))
	for _, path := range []string{video, meta.SRTPath, meta.ASSPath} {
		require.NoError(t, os.WriteFile(path, []byte("product"), 0o644))
	}
	require.NoError(t, SaveMetadata(sidecarPathForVideo(video), meta))
	return video, meta
}

func TestRecordingSourceCleanupProtectsUnverifiedFiles(t *testing.T) {
	for _, damage := range []string{"keep", "legacy", "failed", "unpublished", "bad_nfo", "bad_subtitle", "checkpoint_missing", "checkpoint_keep", "checkpoint_product_missing", "checkpoint_product_empty", "changed", "symlink", "parent_symlink", "wrong_producer", "work_file", "outside"} {
		t.Run(damage, func(t *testing.T) {
			video, meta := publishedRecordingCleanupFixture(t)
			first, second := meta.RecordingSources[0].Path, meta.RecordingSources[1].Path
			switch damage {
			case "keep":
				meta.KeepSource = true
			case "legacy":
				meta.RecordingSources = nil
			case "failed":
				meta.Status = StatusFailed
			case "unpublished":
				require.NoError(t, os.Remove(video))
			case "bad_nfo":
				require.NoError(t, os.WriteFile(strings.TrimSuffix(video, ".mp4")+".nfo", nil, 0o644))
			case "bad_subtitle":
				require.NoError(t, os.WriteFile(meta.SRTPath, nil, 0o644))
			case "checkpoint_missing":
				require.NoError(t, os.Remove(meta.RecordingSources[1].MetadataPath))
			case "checkpoint_keep", "checkpoint_product_missing", "checkpoint_product_empty":
				path := meta.RecordingSources[1].MetadataPath
				checkpoint, err := LoadMetadata(path)
				require.NoError(t, err)
				switch damage {
				case "checkpoint_keep":
					checkpoint.KeepSource = true
					require.NoError(t, SaveMetadata(path, checkpoint))
				case "checkpoint_product_missing":
					require.NoError(t, os.Remove(checkpoint.OutputPath))
				case "checkpoint_product_empty":
					require.NoError(t, os.WriteFile(checkpoint.SRTPath, nil, 0o644))
				}
			case "changed":
				require.NoError(t, os.WriteFile(second, []byte("different source"), 0o644))
			case "symlink":
				require.NoError(t, os.Remove(second))
				require.NoError(t, os.Symlink(first, second))
			case "parent_symlink":
				parent := filepath.Dir(second)
				require.NoError(t, os.Rename(parent, parent+"-moved"))
				require.NoError(t, os.Symlink(parent+"-moved", parent))
			case "wrong_producer":
				meta.RecordingSources[1].ProducerID = "someone-else"
			case "work_file":
				meta.RecordingSources[1].Path = video
			case "outside":
				meta.RecordingSources[1].Path = filepath.Join(t.TempDir(), "external.mp4")
				require.NoError(t, os.WriteFile(meta.RecordingSources[1].Path, []byte("source"), 0o644))
			}
			require.NoError(t, SaveMetadata(sidecarPathForVideo(video), meta))
			require.Error(t, DeletePublishedRecordingSources(video))
			require.FileExists(t, first, "后续候选校验失败前不能删除任何原片")
			require.FileExists(t, second)
		})
	}
}

func TestRecordingSourceCleanupIsIdempotentAndKeepsProducts(t *testing.T) {
	video, meta := publishedRecordingCleanupFixture(t)
	// 上次删除后元数据尚未提交，允许补齐同一清单。
	require.NoError(t, os.Remove(meta.RecordingSources[0].Path))
	for i := 0; i < 2; i++ {
		require.NoError(t, DeletePublishedRecordingSources(video))
	}
	for _, item := range meta.RecordingSources {
		require.NoFileExists(t, item.Path)
	}
	for _, product := range []string{video, meta.SRTPath, meta.ASSPath} {
		require.Equal(t, "product", string(mustReadFile(t, product)))
	}
	after, err := LoadMetadata(sidecarPathForVideo(video))
	require.NoError(t, err)
	require.False(t, after.SourceExists)
	require.NotNil(t, after.SourceDeletedAt)
}

func TestRecordingSourceCleanupDoesNotRepeatCompletedDeletion(t *testing.T) {
	video, meta := publishedRecordingCleanupFixture(t)
	require.NoError(t, DeletePublishedRecordingSources(video))
	completed := mustReadFile(t, sidecarPathForVideo(video))
	// 同名、同大小和时间的后来文件不属于已经完成的清理。
	source := meta.RecordingSources[0]
	require.NoError(t, os.WriteFile(source.Path, []byte("source"), 0o644))
	modified := time.Unix(0, source.ModifiedAt)
	require.NoError(t, os.Chtimes(source.Path, modified, modified))
	require.ErrorIs(t, DeletePublishedRecordingSources(video), ErrSourceNotDeletable)
	require.FileExists(t, source.Path)
	require.Equal(t, completed, mustReadFile(t, sidecarPathForVideo(video)))
}
