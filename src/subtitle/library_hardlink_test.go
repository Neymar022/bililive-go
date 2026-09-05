package subtitle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// referenceTime is a stable timestamp used across the hardlink tests.
var referenceTime = time.Date(2026, 3, 20, 10, 0, 0, 0, time.Local)

func stubCoverExtraction(t *testing.T) {
	t.Helper()
	original := extractCoverTo
	extractCoverTo = func(_ context.Context, _, coverPath string) (string, error) {
		if err := os.WriteFile(coverPath, []byte("cover"), 0o644); err != nil {
			return "", err
		}
		return coverPath, nil
	}
	t.Cleanup(func() {
		extractCoverTo = original
	})
}

// TestEnsureLibraryHardlink_CreatesNewLink verifies that a hardlink is created
// with the correct Plex-style path when the Season 01 directory is empty.
func TestEnsureLibraryHardlink_CreatesNewLink(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.NoError(t, err)

	// Target path must follow the Plex naming convention.
	expectedPath := filepath.Join(libraryRoot, "主播", "Season 01", fmt.Sprintf("主播.S01E%d.2026-03-20 - 测试标题.mp4", episodeIdentityForRecordedAt(referenceTime)))
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

	nfoText, err := os.ReadFile(strings.TrimSuffix(expectedPath, ".mp4") + ".nfo")
	require.NoError(t, err)
	assert.Contains(t, string(nfoText), "<title>2026-03-20 - 测试标题</title>")
	assert.Contains(t, string(nfoText), "<showtitle>主播</showtitle>")
	assert.Contains(t, string(nfoText), "<episode>1</episode>")
	assert.Contains(t, string(nfoText), fmt.Sprintf("<uniqueid type=\"bililive-recorded-at\" default=\"false\">%d</uniqueid>", episodeIdentityForRecordedAt(referenceTime)))
	assert.Contains(t, string(nfoText), "<studio>哔哩哔哩</studio>")
	require.FileExists(t, strings.TrimSuffix(expectedPath, ".mp4")+".jpg")

	showNFOText, err := os.ReadFile(filepath.Join(libraryRoot, "主播", "tvshow.nfo"))
	require.NoError(t, err)
	assert.Contains(t, string(showNFOText), "<tvshow>")
	assert.Contains(t, string(showNFOText), "<title>主播</title>")
	assert.Contains(t, string(showNFOText), "<showtitle>主播</showtitle>")
	assert.Contains(t, string(showNFOText), "<year>2026</year>")
	assert.Contains(t, string(showNFOText), "<studio>哔哩哔哩</studio>")
}

func TestEnsureLibraryHardlinkPublishesUGREENCompatibleOrdinalAndRetainsRecordedAtIdentity(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	recordedAt := time.Date(2026, 3, 20, 10, 0, 0, 123456000, time.FixedZone("UTC+8", 8*60*60))
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", recordedAt, "哔哩哔哩")
	require.NoError(t, err)

	identity := episodeIdentityForRecordedAt(recordedAt)
	assert.Contains(t, filepath.Base(targetPath), fmt.Sprintf(".S01E%d.", identity), "底层文件名必须保留精确 recordedAt identity")
	assert.Greater(t, identity, int64(1_000_000_000_000), "精确 recordedAt identity 仍作为内部稳定身份保留")
	nfoText := string(mustReadFile(t, strings.TrimSuffix(targetPath, filepath.Ext(targetPath))+".nfo"))
	assert.Contains(t, nfoText, "<episode>1</episode>")
	assert.NotContains(t, nfoText, fmt.Sprintf("<episode>%d</episode>", identity))
	assert.Contains(t, nfoText, fmt.Sprintf("<uniqueid type=\"bililive-recorded-at\" default=\"false\">%d</uniqueid>", identity))
}

func TestEnsureLibraryHardlinkRejectsLateEarlierRecordingRatherThanMisordering(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	location := time.FixedZone("UTC+8", 8*60*60)
	laterAt := time.Date(2026, 3, 20, 10, 0, 0, 0, location)
	earlierAt := laterAt.Add(-time.Hour)
	laterSource := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 较晚场次.mp4")
	earlierSource := filepath.Join(sourceRoot, "主播 - 2026-03-20 09-00-00 - 较早场次.mp4")
	require.NoError(t, os.WriteFile(laterSource, []byte("later"), 0o644))
	require.NoError(t, os.WriteFile(earlierSource, []byte("earlier"), 0o644))

	laterPath, err := EnsureLibraryHardlink(context.Background(), laterSource, libraryRoot, "主播", laterAt, "抖音")
	require.NoError(t, err)
	assert.Contains(t, filepath.Base(laterPath), fmt.Sprintf(".S01E%d.", episodeIdentityForRecordedAt(laterAt)))
	assert.Contains(t, string(mustReadFile(t, strings.TrimSuffix(laterPath, ".mp4")+".nfo")), "<episode>1</episode>")

	earlierPath, err := EnsureLibraryHardlink(context.Background(), earlierSource, libraryRoot, "主播", earlierAt, "抖音")
	require.Error(t, err)
	assert.Empty(t, earlierPath)
	assert.Contains(t, err.Error(), "chronological renumber required")
	visible, globErr := filepath.Glob(filepath.Join(libraryRoot, "主播", "Season 01", "*.mp4"))
	require.NoError(t, globErr)
	assert.Equal(t, []string{laterPath}, visible, "晚到更早场次必须 fail closed，不能把时间顺序重新写乱")
}

func TestEnsureLibraryHardlinkRejectsSameSecondEarlierRecordingWithoutExactSidecar(t *testing.T) {
	stubCoverExtraction(t)
	libraryRoot := t.TempDir()
	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	location := time.FixedZone("UTC+8", 8*60*60)
	laterAt := time.Date(2026, 3, 20, 10, 0, 0, 900_000_000, location)
	earlierAt := time.Date(2026, 3, 20, 10, 0, 0, 100_000_000, location)
	existingStem := filepath.Join(
		seasonDir,
		fmt.Sprintf("主播.S01E%d.2026-03-20 - 已发布场次", episodeIdentityForRecordedAt(laterAt)),
	)
	require.NoError(t, os.WriteFile(existingStem+".mp4", []byte("later"), 0o644))
	require.NoError(t, os.WriteFile(
		existingStem+".nfo",
		[]byte(buildEpisodeNFO("主播", 1, episodeIdentityForRecordedAt(laterAt), laterAt, "已发布场次", "抖音")),
		0o644,
	))
	sourcePath := filepath.Join(t.TempDir(), "主播 - 2026-03-20 10-00-00 - 晚到的更早场次.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("earlier"), 0o644))

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", earlierAt, "抖音")

	require.Error(t, err)
	assert.Empty(t, targetPath)
	assert.Contains(t, err.Error(), "chronological renumber required")
}

func TestEnsureLibraryHardlinkRejectsInvalidPublishedOrdinalSequence(t *testing.T) {
	stubCoverExtraction(t)
	referenceLocation := time.FixedZone("UTC+8", 8*60*60)
	cases := []struct {
		name     string
		ordinals []int64
	}{
		{name: "重复", ordinals: []int64{1, 1}},
		{name: "缺号", ordinals: []int64{1, 3}},
		{name: "时间逆序", ordinals: []int64{2, 1}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			libraryRoot := t.TempDir()
			seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
			require.NoError(t, os.MkdirAll(seasonDir, 0o755))
			for index, ordinal := range testCase.ordinals {
				recordedAt := time.Date(2026, 3, 18+index, 10, 0, 0, 0, referenceLocation)
				identity := episodeIdentityForRecordedAt(recordedAt)
				stem := filepath.Join(seasonDir, fmt.Sprintf("主播.S01E%d.%s - 已发布%d", identity, recordedAt.Format("2006-01-02"), index+1))
				require.NoError(t, os.WriteFile(stem+".mp4", []byte(fmt.Sprintf("episode-%d", index)), 0o644))
				nfo := buildEpisodeNFO("主播", ordinal, identity, recordedAt, fmt.Sprintf("已发布%d", index+1), "抖音")
				require.NoError(t, os.WriteFile(stem+".nfo", []byte(nfo), 0o644))
			}
			newAt := time.Date(2026, 3, 21, 10, 0, 0, 0, referenceLocation)
			sourcePath := filepath.Join(t.TempDir(), "主播 - 2026-03-21 10-00-00 - 新场次.mp4")
			require.NoError(t, os.WriteFile(sourcePath, []byte("new"), 0o644))

			targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", newAt, "抖音")

			require.Error(t, err)
			assert.Empty(t, targetPath)
			assert.Contains(t, err.Error(), "published episode ordinals require repair")
		})
	}
}

func TestEnsureLibraryHardlinkRemovesNewEpisodeSidecarsWhenLinkFails(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 失败场次.mp4")
	require.NoError(t, os.Mkdir(sourcePath, 0o755), "目录不能被硬链接，用于稳定触发非 EEXIST 失败")
	showDir := filepath.Join(libraryRoot, "主播")
	showNFOPath := filepath.Join(showDir, "tvshow.nfo")
	showPosterPath := filepath.Join(showDir, "poster.jpg")
	require.NoError(t, os.MkdirAll(showDir, 0o755))
	require.NoError(t, os.WriteFile(showNFOPath, []byte("用户原始 NFO"), 0o640))
	require.NoError(t, os.WriteFile(showPosterPath, nil, 0o600))

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.Error(t, err)
	assert.Empty(t, targetPath)

	failedStem := filepath.Join(libraryRoot, "主播", "Season 01", fmt.Sprintf("主播.S01E%d.2026-03-20 - 失败场次", episodeIdentityForRecordedAt(referenceTime)))
	assert.NoFileExists(t, failedStem+".mp4")
	assert.NoFileExists(t, failedStem+".nfo")
	assert.NoFileExists(t, failedStem+".jpg")
	assert.Equal(t, []byte("用户原始 NFO"), mustReadFile(t, showNFOPath))
	assert.Empty(t, mustReadFile(t, showPosterPath))

	retrySource := filepath.Join(sourceRoot, "主播 - 2026-03-20 11-00-00 - 重试场次.mp4")
	require.NoError(t, os.WriteFile(retrySource, []byte("source"), 0o644))
	retryPath, retryErr := EnsureLibraryHardlink(context.Background(), retrySource, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.NoError(t, retryErr)
	assert.Contains(t, filepath.Base(retryPath), fmt.Sprintf(".S01E%d.", episodeIdentityForRecordedAt(referenceTime)), "失败发布不得占用录制时间身份")
}

func TestEnsureLibraryHardlinkPreservesConcurrentShowMetadataWhenLinkFails(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 并发更新.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	showDir := filepath.Join(libraryRoot, "主播")
	showNFOPath := filepath.Join(showDir, "tvshow.nfo")
	showPosterPath := filepath.Join(showDir, "poster.jpg")
	oldLink := libraryHardlinkLink
	libraryHardlinkLink = func(_, _ string) error {
		require.NoError(t, os.WriteFile(showNFOPath, []byte("用户并发 NFO"), 0o644))
		require.NoError(t, os.WriteFile(showPosterPath, []byte("用户并发封面"), 0o644))
		return errors.New("injected link failure")
	}
	t.Cleanup(func() {
		libraryHardlinkLink = oldLink
	})

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.Error(t, err)
	assert.Empty(t, targetPath)
	assert.Contains(t, err.Error(), "injected link failure")
	assert.Equal(t, []byte("用户并发 NFO"), mustReadFile(t, showNFOPath))
	assert.Equal(t, []byte("用户并发封面"), mustReadFile(t, showPosterPath))

	failedStem := filepath.Join(libraryRoot, "主播", "Season 01", fmt.Sprintf("主播.S01E%d.2026-03-20 - 并发更新", episodeIdentityForRecordedAt(referenceTime)))
	assert.NoFileExists(t, failedStem+".mp4")
	assert.NoFileExists(t, failedStem+".nfo")
	assert.NoFileExists(t, failedStem+".jpg")
	stagingDirs, globErr := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), ".library_publish_staging", "episode-*"))
	require.NoError(t, globErr)
	require.Len(t, stagingDirs, 1, "外部更新导致 show rollback 拒绝时必须保留 staging")
	assert.Contains(t, err.Error(), "publication rollback failed")
	require.FileExists(t, filepath.Join(stagingDirs[0], ".show-backup", "manifest.json"))
}

func TestEnsureLibraryHardlinkDoesNotPublishStagedSidecarsIntoExternallyOccupiedSlot(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 外部冲突.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	var occupiedTarget string
	oldLink := libraryHardlinkLink
	libraryHardlinkLink = func(_, targetPath string) error {
		occupiedTarget = targetPath
		require.NoError(t, os.WriteFile(targetPath, []byte("external-video"), 0o644))
		return os.ErrExist
	}
	t.Cleanup(func() {
		libraryHardlinkLink = oldLink
	})

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.ErrorContains(t, err, "target occupied during publication")
	assert.Empty(t, targetPath)
	require.NotEmpty(t, occupiedTarget)
	assert.Equal(t, []byte("external-video"), mustReadFile(t, occupiedTarget))
	stem := strings.TrimSuffix(occupiedTarget, filepath.Ext(occupiedTarget))
	assert.NoFileExists(t, stem+".nfo")
	assert.NoFileExists(t, stem+".jpg")
	assert.NoFileExists(t, filepath.Join(libraryRoot, "主播", "tvshow.nfo"))
	assert.NoFileExists(t, filepath.Join(libraryRoot, "主播", "poster.jpg"))
}

func TestEnsureLibraryHardlinkMovesSidecarsToConcurrentSameSourceSlot(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 同源并发.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	var occupiedTarget string
	var alternateTarget string
	oldLink := libraryHardlinkLink
	libraryHardlinkLink = func(stagedPath, targetPath string) error {
		occupiedTarget = targetPath
		alternateTarget = strings.Replace(targetPath, fmt.Sprintf(".S01E%d.", episodeIdentityForRecordedAt(referenceTime)), fmt.Sprintf(".S01E%d.", episodeIdentityForRecordedAt(referenceTime)+1), 1)
		require.NoError(t, os.WriteFile(targetPath, []byte("external-video"), 0o644))
		require.NoError(t, os.Link(stagedPath, alternateTarget))
		return os.ErrExist
	}
	t.Cleanup(func() {
		libraryHardlinkLink = oldLink
	})

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.NoError(t, err)
	assert.Equal(t, alternateTarget, targetPath)
	assert.Equal(t, []byte("external-video"), mustReadFile(t, occupiedTarget))
	occupiedStem := strings.TrimSuffix(occupiedTarget, filepath.Ext(occupiedTarget))
	assert.NoFileExists(t, occupiedStem+".nfo")
	assert.NoFileExists(t, occupiedStem+".jpg")
	alternateStem := strings.TrimSuffix(alternateTarget, filepath.Ext(alternateTarget))
	require.FileExists(t, alternateStem+".nfo")
	require.FileExists(t, alternateStem+".jpg")
}

func TestEnsureLibraryHardlinkPreservesInPlaceEpisodeNFOUpdateWhenVideoPublicationFails(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - NFO并发更新.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	var updatedNFO string
	oldLink := libraryHardlinkLink
	libraryHardlinkLink = func(_, targetPath string) error {
		updatedNFO = strings.TrimSuffix(targetPath, filepath.Ext(targetPath)) + ".nfo"
		require.NoError(t, os.WriteFile(updatedNFO, []byte("用户原地更新 NFO"), 0o644))
		return errors.New("injected video publish failure")
	}
	t.Cleanup(func() {
		libraryHardlinkLink = oldLink
	})

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.Error(t, err)
	assert.Empty(t, targetPath)
	assert.Contains(t, err.Error(), "publication rollback failed")
	assert.Contains(t, err.Error(), "changed before rollback")
	assert.Equal(t, []byte("用户原地更新 NFO"), mustReadFile(t, updatedNFO))
	assert.NoFileExists(t, strings.TrimSuffix(updatedNFO, ".nfo")+".mp4")
	require.FileExists(t, strings.TrimSuffix(updatedNFO, ".nfo")+".jpg")
	require.FileExists(t, filepath.Join(libraryRoot, "主播", "tvshow.nfo"))
	require.FileExists(t, filepath.Join(libraryRoot, "主播", "poster.jpg"))
	stagingDirs, globErr := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), ".library_publish_staging", "episode-*"))
	require.NoError(t, globErr)
	require.Len(t, stagingDirs, 1)
}

func TestEnsureLibraryHardlinkRollsBackVideoAndNFOWhenCoverPublicationConflicts(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 封面冲突.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	var conflictingCover string
	oldLink := librarySidecarLink
	librarySidecarLink = func(stagedPath, targetPath string) error {
		if strings.HasSuffix(targetPath, ".jpg") {
			conflictingCover = targetPath
			require.NoError(t, os.WriteFile(targetPath, []byte("用户封面"), 0o644))
			return os.ErrExist
		}
		return os.Link(stagedPath, targetPath)
	}
	t.Cleanup(func() {
		librarySidecarLink = oldLink
	})

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.ErrorContains(t, err, "publish episode cover")
	assert.Empty(t, targetPath)
	require.NotEmpty(t, conflictingCover)
	assert.Equal(t, []byte("用户封面"), mustReadFile(t, conflictingCover))
	stem := strings.TrimSuffix(conflictingCover, ".jpg")
	assert.NoFileExists(t, stem+".mp4")
	assert.NoFileExists(t, stem+".nfo")
	assert.NoFileExists(t, filepath.Join(libraryRoot, "主播", "tvshow.nfo"))
	assert.NoFileExists(t, filepath.Join(libraryRoot, "主播", "poster.jpg"))
}

func TestEnsureLibraryHardlinkRollsBackVideoWhenNFOPublicationConflicts(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - NFO冲突.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	var conflictingNFO string
	oldLink := librarySidecarLink
	librarySidecarLink = func(_, targetPath string) error {
		conflictingNFO = targetPath
		require.NoError(t, os.WriteFile(targetPath, []byte("用户 NFO"), 0o644))
		return os.ErrExist
	}
	t.Cleanup(func() {
		librarySidecarLink = oldLink
	})

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.ErrorContains(t, err, "publish episode NFO")
	assert.Empty(t, targetPath)
	require.NotEmpty(t, conflictingNFO)
	assert.Equal(t, []byte("用户 NFO"), mustReadFile(t, conflictingNFO))
	stem := strings.TrimSuffix(conflictingNFO, ".nfo")
	assert.NoFileExists(t, stem+".mp4")
	assert.NoFileExists(t, stem+".jpg")
	assert.NoFileExists(t, filepath.Join(libraryRoot, "主播", "tvshow.nfo"))
	assert.NoFileExists(t, filepath.Join(libraryRoot, "主播", "poster.jpg"))
}

func TestEnsureLibraryShowNFORebuildsMalformedMatchingIdentity(t *testing.T) {
	showDir := t.TempDir()
	nfoPath := filepath.Join(showDir, "tvshow.nfo")
	require.NoError(t, os.WriteFile(nfoPath, []byte("<tvshow>\n<title>主播</title>\n<showtitle>主播</showtitle>\n<plot>未闭合\n<custom>损坏文件</custom>\n</tvshow>\n"), 0o644))

	err := ensureLibraryShowNFO(showDir, sourceFileMeta{
		aliasName:  "主播",
		recordedAt: referenceTime,
		title:      "测试标题",
	}, "哔哩哔哩")
	require.NoError(t, err)

	content := string(mustReadFile(t, nfoPath))
	assert.Contains(t, content, "<tvshow>")
	assert.Contains(t, content, "</tvshow>")
	assert.Contains(t, content, "<title>主播</title>")
	assert.NotContains(t, content, "<custom>损坏文件</custom>")
}

func TestEnsureLibraryHardlinkCreatesStableShowPosterFromEpisodeCover(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.NoError(t, err)

	episodeCover := strings.TrimSuffix(targetPath, filepath.Ext(targetPath)) + ".jpg"
	showPoster := filepath.Join(libraryRoot, "主播", "poster.jpg")
	require.Equal(t, []byte("cover"), mustReadFile(t, episodeCover))
	require.Equal(t, []byte("cover"), mustReadFile(t, showPoster))

	showNFOPath := filepath.Join(libraryRoot, "主播", "tvshow.nfo")
	showNFOText := string(mustReadFile(t, showNFOPath))
	assert.Contains(t, showNFOText, `<thumb aspect="poster">poster.jpg</thumb>`)

	customShowNFO := strings.Replace(showNFOText, `  <thumb aspect="poster">poster.jpg</thumb>`+"\n", "  <custom>保留字段</custom>\n", 1)
	require.NoError(t, os.WriteFile(showNFOPath, []byte(customShowNFO), 0o644))
	require.NoError(t, os.WriteFile(showPoster, []byte("curated-poster"), 0o644))
	_, err = EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.NoError(t, err)
	assert.Equal(t, []byte("curated-poster"), mustReadFile(t, showPoster), "重复发布不得覆盖已有合集封面")
	assert.Equal(t, []byte("cover"), mustReadFile(t, episodeCover), "合集封面必须独立于单集封面")
	updatedShowNFO := string(mustReadFile(t, showNFOPath))
	assert.Contains(t, updatedShowNFO, "<custom>保留字段</custom>")
	assert.Contains(t, updatedShowNFO, `<thumb aspect="poster">poster.jpg</thumb>`)

	require.NoError(t, ensureLibraryShowNFO(filepath.Dir(showNFOPath), sourceFileMeta{
		aliasName:  "主播",
		recordedAt: referenceTime.AddDate(1, 0, 0),
		title:      "跨年直播",
	}, "抖音"))
	crossYearShowNFO := string(mustReadFile(t, showNFOPath))
	assert.Contains(t, crossYearShowNFO, "<custom>保留字段</custom>")
	assert.Contains(t, crossYearShowNFO, "<year>2026</year>")
	assert.Contains(t, crossYearShowNFO, "<studio>哔哩哔哩</studio>")
}

func TestEnsureLibraryHardlinkRepairsEmptyShowPoster(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	showDir := filepath.Join(libraryRoot, "主播")
	require.NoError(t, os.MkdirAll(showDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(showDir, "poster.jpg"), nil, 0o644))
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	_, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.NoError(t, err)
	assert.Equal(t, []byte("cover"), mustReadFile(t, filepath.Join(showDir, "poster.jpg")))
}

func TestEnsureLibraryHardlink_NormalizesInvisibleShowNameCharacters(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主\u200b播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "哔哩哔哩")
	require.NoError(t, err)

	expectedPath := filepath.Join(libraryRoot, "主播", "Season 01", fmt.Sprintf("主播.S01E%d.2026-03-20 - 测试标题.mp4", episodeIdentityForRecordedAt(referenceTime)))
	assert.Equal(t, expectedPath, targetPath)
	require.NoDirExists(t, filepath.Join(libraryRoot, "主\u200b播"))

	showNFOText, err := os.ReadFile(filepath.Join(libraryRoot, "主播", "tvshow.nfo"))
	require.NoError(t, err)
	assert.Contains(t, string(showNFOText), "<title>主播</title>")
}

func TestNewLibraryPublishStagingPathRejectsSymlinkBackIntoLibrary(t *testing.T) {
	parent := t.TempDir()
	libraryRoot := filepath.Join(parent, "video")
	inlineStaging := filepath.Join(libraryRoot, "inline-staging")
	require.NoError(t, os.MkdirAll(inlineStaging, 0o755))
	require.NoError(t, os.Symlink(inlineStaging, filepath.Join(parent, ".library_publish_staging")))
	targetPath := filepath.Join(libraryRoot, "主播", "Season 01", "episode.mp4")

	stagedPath, cleanup, err := newLibraryPublishStagingPath(libraryRoot, targetPath)
	require.Error(t, err)
	assert.Empty(t, stagedPath)
	assert.Nil(t, cleanup)
	assert.Contains(t, err.Error(), "inside library root")
}

func TestEnsureLibraryHardlinkKeepsIncrementalEpisodesInOneNormalizedShow(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	firstAlias := "天津蛋哥\t\n\u00a0\u3000 6点说车"
	firstSource := filepath.Join(sourceRoot, firstAlias+" - 2026-03-20 10-00-00 - 第一场.mp4")
	secondSource := filepath.Join(sourceRoot, "天津蛋哥 6点说车 - 2026-03-21 10-00-00 - 第二场.mp4")
	require.NoError(t, os.WriteFile(firstSource, []byte("first"), 0o644))
	require.NoError(t, os.WriteFile(secondSource, []byte("second"), 0o644))

	firstPath, err := EnsureLibraryHardlink(context.Background(), firstSource, libraryRoot, firstAlias, referenceTime, "抖音")
	require.NoError(t, err)
	secondPath, err := EnsureLibraryHardlink(context.Background(), secondSource, libraryRoot, "天津蛋哥 6点说车", referenceTime.Add(24*time.Hour), "抖音")
	require.NoError(t, err)

	canonicalShowDir := filepath.Join(libraryRoot, "天津蛋哥 6点说车")
	assert.Equal(t, canonicalShowDir, filepath.Dir(filepath.Dir(firstPath)))
	assert.Equal(t, canonicalShowDir, filepath.Dir(filepath.Dir(secondPath)))
	require.NoDirExists(t, filepath.Join(libraryRoot, firstAlias))
	require.FileExists(t, filepath.Join(canonicalShowDir, "poster.jpg"))
	showNFOText := string(mustReadFile(t, filepath.Join(canonicalShowDir, "tvshow.nfo")))
	assert.Contains(t, showNFOText, "<showtitle>天津蛋哥 6点说车</showtitle>")
	assert.Contains(t, showNFOText, `<thumb aspect="poster">poster.jpg</thumb>`)
}

func TestEnsureLibraryHardlinkNumbersLateRecordingsByExactStartTime(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	location := time.FixedZone("UTC+8", 8*60*60)
	earlierAt := time.Date(2026, 3, 20, 9, 30, 0, 123456000, location)
	laterAt := time.Date(2026, 3, 20, 10, 0, 0, 654321000, location)
	earlierSource := filepath.Join(sourceRoot, "主播 - 2026-03-20 09-30-00 - 较早场次.mp4")
	laterSource := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 较晚场次.mp4")
	require.NoError(t, os.WriteFile(earlierSource, []byte("earlier"), 0o644))
	require.NoError(t, os.WriteFile(laterSource, []byte("later"), 0o644))
	require.NoError(t, os.Chtimes(earlierSource, laterAt.Add(24*time.Hour), laterAt.Add(24*time.Hour)))
	require.NoError(t, os.Chtimes(laterSource, earlierAt.Add(-24*time.Hour), earlierAt.Add(-24*time.Hour)))

	// 正常发布顺序与 mtime 相反，证明身份与公开 ordinal 都不依赖 mtime。
	earlierPath, err := EnsureLibraryHardlink(context.Background(), earlierSource, libraryRoot, "主播", earlierAt, "抖音")
	require.NoError(t, err)
	laterPath, err := EnsureLibraryHardlink(context.Background(), laterSource, libraryRoot, "主播", laterAt, "抖音")
	require.NoError(t, err)

	earlierEpisode := episodeIdentityFromLibraryPath(earlierPath)
	laterEpisode := episodeIdentityFromLibraryPath(laterPath)
	assert.Positive(t, earlierEpisode)
	assert.Less(t, earlierEpisode, laterEpisode, "集号必须按精确录制开始时间递增，而不是按任务完成顺序或 mtime")
	assert.Equal(t, (laterAt.UnixMicro()-earlierAt.UnixMicro())*chronologicalEpisodeIdentityBase, int64(laterEpisode-earlierEpisode))
	earlierNFO := string(mustReadFile(t, strings.TrimSuffix(earlierPath, filepath.Ext(earlierPath))+".nfo"))
	laterNFO := string(mustReadFile(t, strings.TrimSuffix(laterPath, filepath.Ext(laterPath))+".nfo"))
	assert.Contains(t, earlierNFO, "<episode>1</episode>")
	assert.Contains(t, laterNFO, "<episode>2</episode>")
	assert.Contains(t, earlierNFO, "<dateadded>2026-03-20 09:30:00</dateadded>")
}

func TestEnsureLibraryHardlinkPreservesMicrosecondOrder(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	location := time.FixedZone("UTC+8", 8*60*60)
	earlierAt := time.Date(2026, 3, 20, 9, 30, 0, 123456000, location)
	laterAt := time.Date(2026, 3, 20, 9, 30, 0, 123789000, location)
	earlierSource := filepath.Join(sourceRoot, "主播 - 2026-03-20 09-30-00 - 较早微秒场次.mp4")
	laterSource := filepath.Join(sourceRoot, "主播 - 2026-03-20 09-30-00 - 较晚微秒场次.mp4")
	for _, source := range []string{earlierSource, laterSource} {
		require.NoError(t, os.WriteFile(source, []byte(source), 0o644))
	}

	// 微秒级 recordedAt 身份保持顺序，公开 ordinal 仍为连续小整数。
	earlierPath, err := EnsureLibraryHardlink(context.Background(), earlierSource, libraryRoot, "主播", earlierAt, "抖音")
	require.NoError(t, err)
	laterPath, err := EnsureLibraryHardlink(context.Background(), laterSource, libraryRoot, "主播", laterAt, "抖音")
	require.NoError(t, err)

	earlierEpisode := episodeIdentityFromLibraryPath(earlierPath)
	laterEpisode := episodeIdentityFromLibraryPath(laterPath)
	assert.Less(t, earlierEpisode, laterEpisode, "同一毫秒内仍必须按精确微秒排序")
	assert.Equal(t, int64(333*chronologicalEpisodeIdentityBase), int64(laterEpisode-earlierEpisode))
	assert.Contains(t, string(mustReadFile(t, strings.TrimSuffix(earlierPath, ".mp4")+".nfo")), "<episode>1</episode>")
	assert.Contains(t, string(mustReadFile(t, strings.TrimSuffix(laterPath, ".mp4")+".nfo")), "<episode>2</episode>")
}

func TestEnsureLibraryHardlinkUsesBoundedCollisionSlotsForExactTimeIdentity(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	recordedAt := time.Date(2026, 3, 20, 9, 30, 0, 123456000, time.FixedZone("UTC+8", 8*60*60))
	firstSource := filepath.Join(sourceRoot, "主播 - 2026-03-20 09-30-00 - 同名场次.mp4")
	secondSource := filepath.Join(t.TempDir(), "主播 - 2026-03-20 09-30-00 - 同名场次.mp4")
	require.NoError(t, os.WriteFile(firstSource, []byte("first"), 0o644))
	require.NoError(t, os.WriteFile(secondSource, []byte("second"), 0o644))

	firstPath, err := EnsureLibraryHardlink(context.Background(), firstSource, libraryRoot, "主播", recordedAt, "抖音")
	require.NoError(t, err)
	secondPath, err := EnsureLibraryHardlink(context.Background(), secondSource, libraryRoot, "主播", recordedAt, "抖音")
	require.NoError(t, err)
	assert.Equal(t, []byte("first"), mustReadFile(t, firstPath))
	assert.Equal(t, []byte("second"), mustReadFile(t, secondPath))
	assert.Equal(t, episodeIdentityFromLibraryPath(firstPath)+1, episodeIdentityFromLibraryPath(secondPath))
	visible, globErr := filepath.Glob(filepath.Join(filepath.Dir(firstPath), "*.mp4"))
	require.NoError(t, globErr)
	assert.ElementsMatch(t, []string{firstPath, secondPath}, visible)
}

func TestEnsureLibraryHardlinkSerializesConcurrentEpisodePublication(t *testing.T) {
	oldExtract := extractCoverTo
	extractCoverTo = func(_ context.Context, sourcePath, targetPath string) (string, error) {
		return targetPath, os.WriteFile(targetPath, []byte(filepath.Base(sourcePath)), 0o644)
	}
	t.Cleanup(func() {
		extractCoverTo = oldExtract
	})

	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	const recordings = 8
	type result struct {
		source string
		target string
		err    error
	}
	results := make(chan result, recordings)
	for index := 0; index < recordings; index++ {
		sourcePath := filepath.Join(sourceRoot, fmt.Sprintf("主播 - 2026-03-20 10-00-00 - 并发场次 %02d.mp4", index))
		require.NoError(t, os.WriteFile(sourcePath, []byte(fmt.Sprintf("source-%02d", index)), 0o644))
		go func(source string) {
			target, err := EnsureLibraryHardlink(context.Background(), source, libraryRoot, "主播", referenceTime, "哔哩哔哩")
			results <- result{source: source, target: target, err: err}
		}(sourcePath)
	}

	targets := make(map[string]struct{}, recordings)
	ordinals := make(map[int64]struct{}, recordings)
	for index := 0; index < recordings; index++ {
		item := <-results
		require.NoError(t, item.err)
		if _, exists := targets[item.target]; exists {
			t.Fatalf("并发发布复用了同一集数槽: %s", item.target)
		}
		targets[item.target] = struct{}{}
		ordinal, ok := episodeOrdinalFromNFO(strings.TrimSuffix(item.target, filepath.Ext(item.target)) + ".nfo")
		require.True(t, ok)
		ordinals[ordinal] = struct{}{}
		coverPath := strings.TrimSuffix(item.target, filepath.Ext(item.target)) + ".jpg"
		assert.Equal(t, []byte(filepath.Base(item.source)), mustReadFile(t, coverPath))
	}
	assert.Len(t, targets, recordings)
	assert.Len(t, ordinals, recordings)
}

// TestEnsureLibraryHardlink_IdempotentSameInode verifies that calling
// EnsureLibraryHardlink a second time when the target already exists with the
// same inode returns the path without error and without creating a duplicate.
func TestEnsureLibraryHardlink_IdempotentSameInode(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	// First call — creates the link.
	targetPath1, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "bililive-go")
	require.NoError(t, err)

	// Second call — must return same path without error.
	targetPath2, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "bililive-go")
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
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	// Pre-create an unrelated file at E0001 (different content/inode).
	unrelatedPath := filepath.Join(seasonDir, "主播.S01E0001.2026-03-19 - 其他.mp4")
	require.NoError(t, os.WriteFile(unrelatedPath, []byte("unrelated"), 0o644))
	writePublishedFixtureNFO(t, unrelatedPath, 1, referenceTime.Add(-24*time.Hour))

	returnedPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "bililive-go")
	require.NoError(t, err)

	// EnsureLibraryHardlink counts 1 existing mp4 → assigns E0002.
	expectedTarget := filepath.Join(seasonDir, fmt.Sprintf("主播.S01E%d.2026-03-20 - 测试.mp4", episodeIdentityForRecordedAt(referenceTime)))
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

// TestEnsureLibraryHardlink_EpisodeNumbering 验证旧顺序集号不会改变新录制的时间身份。
func TestEnsureLibraryHardlink_EpisodeNumbering(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	// Pre-create 2 existing episodes so the next one should be E0003.
	require.NoError(t, os.WriteFile(filepath.Join(seasonDir, "主播.S01E0001.2026-03-18 - 第一集.mp4"), []byte("ep1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(seasonDir, "主播.S01E0002.2026-03-19 - 第二集.mp4"), []byte("ep2"), 0o644))
	writePublishedFixtureNFO(t, filepath.Join(seasonDir, "主播.S01E0001.2026-03-18 - 第一集.mp4"), 1, referenceTime.Add(-48*time.Hour))
	writePublishedFixtureNFO(t, filepath.Join(seasonDir, "主播.S01E0002.2026-03-19 - 第二集.mp4"), 2, referenceTime.Add(-24*time.Hour))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 第三集.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "bililive-go")
	require.NoError(t, err)

	expectedPath := filepath.Join(seasonDir, fmt.Sprintf("主播.S01E%d.2026-03-20 - 第三集.mp4", episodeIdentityForRecordedAt(referenceTime)))
	assert.Equal(t, expectedPath, targetPath)
}

func TestEnsureLibraryHardlinkCountsLegacyRangeAsOnePublishedMedia(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(seasonDir, "主播.S01E0001.2026-03-18 - 第一集.mp4"), []byte("ep1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(seasonDir, "主播.S01E0002-S01E0003.2026-03-20 - 同场聚合.mp4"), []byte("ep2-3"), 0o644))
	writePublishedFixtureNFO(t, filepath.Join(seasonDir, "主播.S01E0001.2026-03-18 - 第一集.mp4"), 1, referenceTime.Add(-48*time.Hour))
	writePublishedFixtureNFO(t, filepath.Join(seasonDir, "主播.S01E0002-S01E0003.2026-03-20 - 同场聚合.mp4"), 2, referenceTime)

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-22 10-00-00 - 第四集.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime.Add(48*time.Hour), "bililive-go")
	require.NoError(t, err)

	expectedPath := filepath.Join(seasonDir, fmt.Sprintf("主播.S01E%d.2026-03-22 - 第四集.mp4", episodeIdentityForRecordedAt(referenceTime.Add(48*time.Hour))))
	assert.Equal(t, expectedPath, targetPath)
	require.Contains(t, string(mustReadFile(t, sidecarStem(targetPath)+".nfo")), "<episode>3</episode>")
}

func writePublishedFixtureNFO(t *testing.T, path string, ordinal int64, recordedAt time.Time) {
	t.Helper()
	nfo := buildEpisodeNFO("主播", ordinal, episodeIdentityForRecordedAt(recordedAt), recordedAt, "历史", "test")
	require.NoError(t, os.WriteFile(sidecarStem(path)+".nfo", []byte(nfo), 0o644))
}

// TestEnsureLibraryHardlink_DoesNotReuseEpisodeWithSidecars verifies that
// completed subtitle sidecars reserve an episode slot even if the rendered mp4
// was removed by cleanup. Reusing that slot would attach old subtitles to a new
// source recording.
func TestEnsureLibraryHardlink_DoesNotReuseEpisodeWithSidecars(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	stem := filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 同名直播")
	require.NoError(t, os.WriteFile(stem+".srt", []byte("old subtitles"), 0o644))
	require.NoError(t, os.WriteFile(stem+".ass", []byte("old ass"), 0o644))
	require.NoError(t, os.WriteFile(stem+".subtitle.json", []byte(`{"status":"completed"}`), 0o644))

	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-21 11-00-00 - 同名直播.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("new source"), 0o644))

	targetPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime.Add(24*time.Hour), "bililive-go")
	require.NoError(t, err)

	expectedPath := filepath.Join(seasonDir, fmt.Sprintf("主播.S01E%d.2026-03-21 - 同名直播.mp4", episodeIdentityForRecordedAt(referenceTime.Add(24*time.Hour))))
	assert.Equal(t, expectedPath, targetPath)
	_, err = os.Stat(stem + ".mp4")
	assert.True(t, os.IsNotExist(err), "old sidecar slot must not receive the new video")
}

func TestEnsureLibraryHardlink_UsesCompletedMetadataSourcePath(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("raw source"), 0o644))

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	libraryPath := filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	srtPath := filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 测试标题.srt")
	assPath := filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 测试标题.ass")
	require.NoError(t, os.WriteFile(libraryPath, []byte("burned output"), 0o644))
	require.NoError(t, os.WriteFile(srtPath, []byte("1\n"), 0o644))
	require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))
	completedAt := time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC)
	require.NoError(t, SaveMetadata(sidecarPathForVideo(libraryPath), Metadata{
		Status:         StatusCompleted,
		SourcePath:     sourcePath,
		OutputPath:     libraryPath,
		SRTPath:        srtPath,
		ASSPath:        assPath,
		SourceExists:   true,
		RendererStatus: StatusCompleted,
		Segments: []Segment{
			{Index: 1, Start: "00:00:00,000", End: "00:00:01,000", Text: "测试"},
		},
		CompletedAt: &completedAt,
	}))

	returnedPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "bililive-go")
	require.NoError(t, err)

	assert.Equal(t, libraryPath, returnedPath, "completed sidecar source_path should win even after burned output replaced the hardlink")
	entries, err := os.ReadDir(seasonDir)
	require.NoError(t, err)
	mp4Count := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".mp4" {
			mp4Count++
		}
	}
	assert.Equal(t, 1, mp4Count, "must not publish the same source as a second raw episode")
}

func TestEnsureLibraryHardlink_RepairsMissingSidecarsForExistingHardlink(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	targetPath := filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	require.NoError(t, os.Link(sourcePath, targetPath))

	returnedPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "bililive-go")
	require.NoError(t, err)

	assert.Equal(t, targetPath, returnedPath)
	require.FileExists(t, filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 测试标题.nfo"))
	require.FileExists(t, filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 测试标题.jpg"))
}

func TestEnsureLibraryHardlink_RepairsIncompleteEpisodeNFOTitle(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 正确标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	targetPath := filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 正确标题.mp4")
	require.NoError(t, os.Link(sourcePath, targetPath))
	nfoPath := filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 正确标题.nfo")
	require.NoError(t, os.WriteFile(nfoPath, []byte(strings.Join([]string{
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`,
		"<episodedetails>",
		"  <showtitle>主播</showtitle>",
		"  <episode>1</episode>",
		"</episodedetails>",
		"",
	}, "\n")), 0o644))

	returnedPath, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "bililive-go")
	require.NoError(t, err)

	assert.Equal(t, targetPath, returnedPath)
	nfoText, err := os.ReadFile(nfoPath)
	require.NoError(t, err)
	assert.Contains(t, string(nfoText), "<title>2026-03-20 - 正确标题</title>")
	assert.Contains(t, string(nfoText), "<season>1</season>")
	assert.Contains(t, string(nfoText), "<studio>bililive-go</studio>")
}

func TestEnsureLibraryHardlink_RepairsShowNFO(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	showDir := filepath.Join(libraryRoot, "主播")
	seasonDir := filepath.Join(showDir, "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(showDir, "tvshow.nfo"), []byte(strings.Join([]string{
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`,
		"<tvshow>",
		"  <title>主播</title>",
		"</tvshow>",
		"",
	}, "\n")), 0o644))

	_, err := EnsureLibraryHardlink(context.Background(), sourcePath, libraryRoot, "主播", referenceTime, "bililive-go")
	require.NoError(t, err)

	showNFOText, err := os.ReadFile(filepath.Join(showDir, "tvshow.nfo"))
	require.NoError(t, err)
	assert.Contains(t, string(showNFOText), "<title>主播</title>")
	assert.Contains(t, string(showNFOText), "<showtitle>主播</showtitle>")
	assert.Contains(t, string(showNFOText), "<year>2026</year>")
	assert.Contains(t, string(showNFOText), "<studio>bililive-go</studio>")
}

func TestEnsureLibrarySidecarsRepairsResolvedLibraryPath(t *testing.T) {
	stubCoverExtraction(t)
	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "天津蛋哥   6点说车 - 2026-06-12 20-00-00 - 晚间说车.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))

	seasonDir := filepath.Join(libraryRoot, "天津蛋哥 6点说车", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	libraryPath := filepath.Join(seasonDir, "天津蛋哥 6点说车.S01E0003.2026-06-12 - 晚间说车.mp4")
	require.NoError(t, os.Link(sourcePath, libraryPath))

	err := EnsureLibrarySidecars(context.Background(), sourcePath, libraryPath, "天津蛋哥 6点说车", referenceTime, "srt_video")
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(libraryRoot, "天津蛋哥 6点说车", "tvshow.nfo"))
	require.FileExists(t, strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath))+".nfo")
	require.FileExists(t, strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath))+".jpg")
	require.NoDirExists(t, filepath.Join(libraryRoot, "天津蛋哥   6点说车"))
}

func TestEnsureLibrarySidecarsFailsWhenCoverCannotBeCreated(t *testing.T) {
	original := extractCoverTo
	extractCoverTo = func(_ context.Context, _, _ string) (string, error) {
		return "", assert.AnError
	}
	t.Cleanup(func() {
		extractCoverTo = original
	})

	sourceRoot := t.TempDir()
	libraryRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "主播 - 2026-03-20 10-00-00 - 测试标题.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))
	seasonDir := filepath.Join(libraryRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	libraryPath := filepath.Join(seasonDir, "主播.S01E0001.2026-03-20 - 测试标题.mp4")
	require.NoError(t, os.Link(sourcePath, libraryPath))

	err := EnsureLibrarySidecars(context.Background(), sourcePath, libraryPath, "主播", referenceTime, "bililive-go")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "extract episode cover")
	assert.NoFileExists(t, strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath))+".jpg")
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
	meta := parseSourceFilename("/some/dir/主播 - 2026-03-20 10-00-00 - 测试标题.mp4", "fallback", time.Time{})
	assert.Equal(t, "主播", meta.aliasName)
	assert.Equal(t, "测试标题", meta.title)
	assert.Equal(t, "2026-03-20T10:00:00+08:00", meta.recordedAt.Format(time.RFC3339))
}

func TestEpisodeNumberForRecordedAtRejectsUnsafeIdentity(t *testing.T) {
	assert.Negative(t, episodeIdentityForRecordedAt(time.Date(2056, 1, 1, 0, 0, 0, 0, time.UTC)))
	assert.Negative(t, episodeIdentityForRecordedAt(time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)))
}

func TestEnsureLibraryHardlinkRejectsMissingReliableRecordedAt(t *testing.T) {
	stubCoverExtraction(t)
	sourcePath := filepath.Join(t.TempDir(), "无法解析时间.mp4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("video"), 0o644))

	target, err := EnsureLibraryHardlink(context.Background(), sourcePath, t.TempDir(), "主播", time.Time{}, "抖音")
	require.Error(t, err)
	assert.Empty(t, target)
	assert.Contains(t, err.Error(), "no reliable recording start time")
}

// TestParseSourceFilename_Fallback verifies that unrecognised filenames fall
// back to the RecordInfo host/time values.
func TestParseSourceFilename_Fallback(t *testing.T) {
	ts := time.Date(2026, 3, 20, 10, 0, 0, 0, time.Local)
	meta := parseSourceFilename("/some/dir/unrecognised-filename.mp4", "fallback-host", ts)
	assert.Equal(t, "fallback-host", meta.aliasName)
	assert.Equal(t, "未命名直播", meta.title)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return content
}
