package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncKnowledgeLiveSessionDefersUntilQuietWindow(t *testing.T) {
	libraryRoot := t.TempDir()
	libraryPath, metadataPath, metadata := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")

	knowledgeCalls := 0
	knowledge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeCalls++
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"ok": true}))
	}))
	defer knowledge.Close()

	cfg := configs.SubtitleKnowledgeSyncConfig{
		Enabled:                       true,
		Endpoint:                      knowledge.URL + "/api/knowledge/ingest",
		Token:                         "test-token",
		GenerateNote:                  true,
		NonBlocking:                   true,
		LiveSessionQuietWindowSeconds: 300,
	}
	ctx := knowledgeSessionTestContext(619)
	stage := &SubtitleGenerateStage{}
	now := time.Date(2026, 6, 1, 18, 30, 0, 0, time.UTC)

	err := stage.syncKnowledgeAt(ctx, cfg, libraryRoot, libraryPath, metadataPath, &metadata, now)

	var retryLater *pipeline.RetryLaterError
	require.True(t, errors.As(err, &retryLater), "same-live session should wait for the quiet window")
	assert.Equal(t, 0, knowledgeCalls)
	assert.LessOrEqual(t, retryLater.Delay, 300*time.Second)
	assert.Greater(t, retryLater.Delay, 0*time.Second)

	loaded, err := subtitle.LoadMetadata(metadataPath)
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusQueued, loaded.KnowledgeSyncStatus)
	assert.Equal(t, "live-session:session-20260601-linkai", loaded.KnowledgeSyncSourceID)
	assert.Contains(t, loaded.KnowledgeSyncError, "waiting for same live session aggregation")

	manifest, err := loadKnowledgeSessionManifest(knowledgeSessionManifestPath(libraryRoot, "session-20260601-linkai"))
	require.NoError(t, err)
	assert.Equal(t, "session-20260601-linkai", manifest.LiveSessionID)
	require.Len(t, manifest.Sources, 1)
	assert.Equal(t, libraryPath, manifest.Sources[0].LibraryPath)
	assert.Equal(t, metadataPath, manifest.Sources[0].MetadataPath)
}

func TestSyncKnowledgeLiveSessionPostsAggregatedPayloadOnceAfterQuietWindow(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, firstMetadata := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, secondMetadata := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")

	var capturedPayload knowledgeIngestPayload
	knowledgeCalls := 0
	knowledge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeCalls++
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedPayload))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"ok": true}))
	}))
	defer knowledge.Close()

	cfg := configs.SubtitleKnowledgeSyncConfig{
		Enabled:                       true,
		Endpoint:                      knowledge.URL + "/api/knowledge/ingest",
		Token:                         "test-token",
		GenerateNote:                  true,
		NonBlocking:                   true,
		ProviderID:                    "qwen",
		ModelName:                     "qwen3.7-plus",
		Style:                         "教程",
		LiveSessionQuietWindowSeconds: 300,
	}
	stage := &SubtitleGenerateStage{}
	firstCtx := knowledgeSessionTestContext(619)
	secondCtx := knowledgeSessionTestContext(620)
	start := time.Date(2026, 6, 1, 18, 30, 0, 0, time.UTC)

	requireRetryLater(t, stage.syncKnowledgeAt(firstCtx, cfg, libraryRoot, firstLibraryPath, firstMetadataPath, &firstMetadata, start))
	requireRetryLater(t, stage.syncKnowledgeAt(secondCtx, cfg, libraryRoot, secondLibraryPath, secondMetadataPath, &secondMetadata, start.Add(time.Minute)))
	assert.Equal(t, 0, knowledgeCalls)

	secondMetadata, err := subtitle.LoadMetadata(secondMetadataPath)
	require.NoError(t, err)
	err = stage.syncKnowledgeAt(secondCtx, cfg, libraryRoot, secondLibraryPath, secondMetadataPath, &secondMetadata, start.Add(7*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 1, knowledgeCalls)
	assert.Equal(t, "live-session:session-20260601-linkai", capturedPayload.SourceID)
	assert.Equal(t, "session-20260601-linkai", capturedPayload.LiveSessionID)
	assert.Equal(t, "qwen3.7-plus", capturedPayload.ModelName)
	assert.Equal(t, "教程", capturedPayload.Style)
	assert.Equal(t, []string{"toc", "link", "screenshot", "summary"}, capturedPayload.Format)
	require.NotNil(t, capturedPayload.Link)
	assert.True(t, *capturedPayload.Link)
	require.NotNil(t, capturedPayload.Screenshot)
	assert.True(t, *capturedPayload.Screenshot)
	assert.Contains(t, capturedPayload.SourceVideoPath, "S01E0019")
	assert.Contains(t, capturedPayload.SourceVideoPath, "[同场聚合]")
	assert.Empty(t, capturedPayload.SourceVideos)
	assert.Empty(t, capturedPayload.MediaSegments)
	require.Len(t, capturedPayload.Segments, 2)
	assert.Equal(t, 0, capturedPayload.Segments[0].SourceIndex)
	assert.Equal(t, 0, capturedPayload.Segments[1].SourceIndex)
	assert.Equal(t, 120.0, capturedPayload.Segments[1].Start)

	firstPosted, err := subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, err)
	secondPosted, err := subtitle.LoadMetadata(secondMetadataPath)
	require.NoError(t, err)
	assert.Equal(t, subtitle.StatusQueued, firstPosted.KnowledgeSyncStatus)
	assert.Equal(t, subtitle.StatusQueued, secondPosted.KnowledgeSyncStatus)
	assert.Equal(t, "live-session:session-20260601-linkai", firstPosted.KnowledgeSyncSourceID)
	assert.Equal(t, "live-session:session-20260601-linkai", secondPosted.KnowledgeSyncSourceID)

	err = stage.syncKnowledgeAt(secondCtx, cfg, libraryRoot, secondLibraryPath, secondMetadataPath, &secondPosted, start.Add(8*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 1, knowledgeCalls, "same unchanged session manifest should not post duplicate BiliNote documents")
}

func TestPublishLiveSessionMediaAggregateCreatesOneVisibleEpisodeAndHidesSegments(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")
	firstVideoBefore := mustReadStageFile(t, firstLibraryPath)
	secondVideoBefore := mustReadStageFile(t, secondLibraryPath)
	firstStem := strings.TrimSuffix(firstLibraryPath, filepath.Ext(firstLibraryPath))
	require.NoError(t, os.WriteFile(firstStem+".jpg", []byte("cover"), 0o644))

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:session-20260601-linkai",
		LiveSessionID: "session-20260601-linkai",
		Sources: []knowledgeSessionManifestSource{
			{
				TaskID:       "bililive-go-619",
				SourceID:     knowledgeSourceID(libraryRoot, firstLibraryPath),
				LibraryPath:  firstLibraryPath,
				MetadataPath: firstMetadataPath,
			},
			{
				TaskID:       "bililive-go-620",
				SourceID:     knowledgeSourceID(libraryRoot, secondLibraryPath),
				LibraryPath:  secondLibraryPath,
				MetadataPath: secondMetadataPath,
			},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Contains(t, filepath.Base(aggregate.LibraryPath), "S01E0019")
	assert.Contains(t, filepath.Base(aggregate.LibraryPath), "[同场聚合]")
	assert.NotContains(t, filepath.Base(aggregate.LibraryPath), "-S01E")
	require.FileExists(t, aggregate.LibraryPath)
	require.FileExists(t, strings.TrimSuffix(aggregate.LibraryPath, filepath.Ext(aggregate.LibraryPath))+".subtitle.json")
	require.FileExists(t, strings.TrimSuffix(aggregate.LibraryPath, filepath.Ext(aggregate.LibraryPath))+".srt")
	require.FileExists(t, strings.TrimSuffix(aggregate.LibraryPath, filepath.Ext(aggregate.LibraryPath))+".ass")
	require.FileExists(t, strings.TrimSuffix(aggregate.LibraryPath, filepath.Ext(aggregate.LibraryPath))+".nfo")
	require.FileExists(t, strings.TrimSuffix(aggregate.LibraryPath, filepath.Ext(aggregate.LibraryPath))+".jpg")

	assert.NoFileExists(t, firstLibraryPath)
	assert.NoFileExists(t, secondLibraryPath)
	assert.NotEqual(t, firstLibraryPath, aggregate.LibraryPath)
	assert.NotEqual(t, secondLibraryPath, aggregate.LibraryPath)
	seasonDir := filepath.Dir(firstLibraryPath)
	visibleMP4 := visibleMP4Files(t, seasonDir)
	assert.Equal(t, []string{filepath.Base(aggregate.LibraryPath)}, visibleMP4)

	firstMetadata, err := subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, err)
	assert.Contains(t, firstMetadata.OutputPath, ".live_session_segments")
	resolvedLibraryRoot, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	require.NoError(t, err)
	assert.NotEqual(t, resolvedLibraryRoot, hiddenRoot)
	relHiddenOutput, err := filepath.Rel(hiddenRoot, firstMetadata.OutputPath)
	require.NoError(t, err)
	assert.False(t, relHiddenOutput == ".." || strings.HasPrefix(relHiddenOutput, ".."+string(filepath.Separator)))
	require.FileExists(t, firstMetadata.OutputPath)
	assert.Equal(t, aggregate.LibraryPath, firstMetadata.RecordMeta["live_session_media_aggregate_path"])
	assert.Equal(t, firstMetadata.OutputPath, firstMetadata.RecordMeta["live_session_segment_hidden_path"])
	assert.NoDirExists(t, filepath.Join(seasonDir, liveSessionSegmentsDirName))

	secondMetadata, err := subtitle.LoadMetadata(secondMetadataPath)
	require.NoError(t, err)
	assert.Contains(t, secondMetadata.OutputPath, ".live_session_segments")
	assert.Equal(t, aggregate.LibraryPath, secondMetadata.RecordMeta["live_session_media_aggregate_path"])
	assert.Equal(t, secondMetadata.OutputPath, secondMetadata.RecordMeta["live_session_segment_hidden_path"])
	assert.Equal(t, firstVideoBefore, mustReadStageFile(t, firstMetadata.OutputPath))
	assert.Equal(t, secondVideoBefore, mustReadStageFile(t, secondMetadata.OutputPath))
	hiddenVideos, err := filepath.Glob(filepath.Join(hiddenRoot, "*", "Season 01", "*", "*.mp4"))
	require.NoError(t, err)
	assert.Len(t, hiddenVideos, 2)
	aggregateMetadata, err := subtitle.LoadMetadata(aggregate.MetadataPath)
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{firstMetadata.OutputPath, secondMetadata.OutputPath},
		liveSessionMetadataSourcePaths(t, aggregateMetadata),
	)
	for _, path := range liveSessionMetadataSourcePaths(t, aggregateMetadata) {
		require.FileExists(t, path)
	}

	manifestPath := knowledgeSessionManifestPath(libraryRoot, manifest.LiveSessionID)
	require.NoError(t, saveKnowledgeSessionManifest(manifestPath, manifest))
	reloadedManifest, err := loadKnowledgeSessionManifest(manifestPath)
	require.NoError(t, err)
	reloadedInputs, err := knowledgeSessionInputsFromManifest(reloadedManifest)
	require.NoError(t, err)
	reloadedPaths, err := liveSessionSegmentVideoPaths(reloadedInputs)
	require.NoError(t, err)
	assert.Contains(t, reloadedPaths, firstMetadata.OutputPath)
	assert.Contains(t, reloadedPaths, secondMetadata.OutputPath)
	require.Len(t, aggregate.Metadata.Segments, 2)
	assert.Equal(t, "00:02:00,000", aggregate.Metadata.Segments[1].Start)

	firstHiddenInfo, err := os.Stat(firstMetadata.OutputPath)
	require.NoError(t, err)
	secondHiddenInfo, err := os.Stat(secondMetadata.OutputPath)
	require.NoError(t, err)
	firstMetadataBeforeRepeat := mustReadStageFile(t, firstMetadataPath)
	secondMetadataBeforeRepeat := mustReadStageFile(t, secondMetadataPath)
	staleSources := aggregateMetadata.RecordMeta["live_session_media_sources"].([]any)
	staleSources[0].(map[string]any)["output_path"] = firstLibraryPath
	staleSources[1].(map[string]any)["output_path"] = secondLibraryPath
	require.NoError(t, subtitle.SaveMetadata(aggregate.MetadataPath, aggregateMetadata))

	repeatedAggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &reloadedManifest)
	require.NoError(t, err)
	require.NotNil(t, repeatedAggregate)
	assert.Equal(t, aggregate.LibraryPath, repeatedAggregate.LibraryPath)

	firstMetadataAfterRepeat, err := subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, err)
	secondMetadataAfterRepeat, err := subtitle.LoadMetadata(secondMetadataPath)
	require.NoError(t, err)
	assert.Equal(t, firstMetadata.OutputPath, firstMetadataAfterRepeat.OutputPath)
	assert.Equal(t, secondMetadata.OutputPath, secondMetadataAfterRepeat.OutputPath)
	assert.Equal(t, firstMetadataBeforeRepeat, mustReadStageFile(t, firstMetadataPath))
	assert.Equal(t, secondMetadataBeforeRepeat, mustReadStageFile(t, secondMetadataPath))
	firstHiddenInfoAfterRepeat, err := os.Stat(firstMetadataAfterRepeat.OutputPath)
	require.NoError(t, err)
	secondHiddenInfoAfterRepeat, err := os.Stat(secondMetadataAfterRepeat.OutputPath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(firstHiddenInfo, firstHiddenInfoAfterRepeat))
	assert.True(t, os.SameFile(secondHiddenInfo, secondHiddenInfoAfterRepeat))
	repeatedMetadata, err := subtitle.LoadMetadata(repeatedAggregate.MetadataPath)
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{firstMetadata.OutputPath, secondMetadata.OutputPath},
		liveSessionMetadataSourcePaths(t, repeatedMetadata),
	)
	hiddenVideosAfterRepeat, err := filepath.Glob(filepath.Join(hiddenRoot, "*", "Season 01", "*", "*.mp4"))
	require.NoError(t, err)
	assert.Len(t, hiddenVideosAfterRepeat, 2)
}

func TestPublishLiveSessionMediaAggregateKeepsOneStableVisibleAggregateWhenSessionGrows(t *testing.T) {
	oldConcat := liveSessionMediaConcat
	oldProbeDuration := liveSessionMediaProbeDuration
	liveSessionMediaConcat = func(_ context.Context, _ string, inputs []string, outputPath string) error {
		return os.WriteFile(outputPath, []byte(fmt.Sprintf("aggregate-%d", len(inputs))), 0o644)
	}
	liveSessionMediaProbeDuration = func(_ context.Context, _ string, inputPath string) (float64, error) {
		for index, episode := range []string{"S01E0019", "S01E0020", "S01E0021"} {
			if strings.Contains(inputPath, episode) {
				return float64(60 + index), nil
			}
		}
		return 0, fmt.Errorf("unexpected input path: %s", inputPath)
	}
	t.Cleanup(func() {
		liveSessionMediaConcat = oldConcat
		liveSessionMediaProbeDuration = oldProbeDuration
	})

	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 21, "第二段内容")
	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:session-grow",
		LiveSessionID: "session-grow",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-620", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	firstAggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, firstAggregate)
	assert.Contains(t, filepath.Base(firstAggregate.LibraryPath), "S01E0020")
	assert.Equal(t, []byte("aggregate-2"), mustReadStageFile(t, firstAggregate.LibraryPath))

	thirdLibraryPath, thirdMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "迟到的更早分段")
	manifest.Sources = append(manifest.Sources, knowledgeSessionManifestSource{
		TaskID:       "bililive-go-621",
		SourceID:     knowledgeSourceID(libraryRoot, thirdLibraryPath),
		LibraryPath:  thirdLibraryPath,
		MetadataPath: thirdMetadataPath,
	})

	secondAggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, secondAggregate)
	assert.Equal(t, firstAggregate.LibraryPath, secondAggregate.LibraryPath)
	assert.Contains(t, filepath.Base(secondAggregate.LibraryPath), "S01E0020")
	assert.Equal(t, []byte("aggregate-3"), mustReadStageFile(t, secondAggregate.LibraryPath))
	assert.Equal(t, []string{filepath.Base(secondAggregate.LibraryPath)}, visibleMP4Files(t, filepath.Dir(secondAggregate.LibraryPath)))

	_, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	require.NoError(t, err)
	hiddenSegments, err := filepath.Glob(filepath.Join(hiddenRoot, "*", "Season 01", "*", "*.mp4"))
	require.NoError(t, err)
	assert.Len(t, hiddenSegments, 3)
	archivedAggregates, err := filepath.Glob(filepath.Join(hiddenRoot, "*", "Season 01", "*", ".aggregate_versions", "*", "*.mp4"))
	require.NoError(t, err)
	require.Len(t, archivedAggregates, 1)
	assert.Equal(t, []byte("aggregate-2"), mustReadStageFile(t, archivedAggregates[0]))
	archivedMetadataPath := strings.TrimSuffix(archivedAggregates[0], filepath.Ext(archivedAggregates[0])) + ".subtitle.json"
	archivedMetadata, err := subtitle.LoadMetadata(archivedMetadataPath)
	require.NoError(t, err)
	assert.Equal(t, archivedAggregates[0], archivedMetadata.OutputPath)
	assert.Equal(t, "aggregate_version", archivedMetadata.RecordMeta["live_session_media_role"])
	assert.Equal(t, secondAggregate.LibraryPath, archivedMetadata.RecordMeta["live_session_media_superseded_by"])
	require.Len(t, liveSessionMetadataSourcePaths(t, archivedMetadata), 2)
	for _, path := range liveSessionMetadataSourcePaths(t, archivedMetadata) {
		require.FileExists(t, path)
		assert.Contains(t, path, liveSessionSegmentsDirName)
	}
}

func TestPublishLiveSessionMediaAggregateKeepsRelativeLibraryRootOutsideMediaLibrary(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	workDir := t.TempDir()
	libraryDir := filepath.Join(workDir, "video")
	require.NoError(t, os.MkdirAll(libraryDir, 0o755))
	t.Chdir(libraryDir)

	libraryRoot := "."
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")
	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:session-20260601-relative-root",
		LiveSessionID: "session-20260601-relative-root",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-620", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, aggregate)

	firstMetadata, err := subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(manifest.AggregatePath))
	_, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	require.NoError(t, err)
	relHiddenOutput, err := filepath.Rel(hiddenRoot, firstMetadata.OutputPath)
	require.NoError(t, err)
	assert.False(t, relHiddenOutput == ".." || strings.HasPrefix(relHiddenOutput, ".."+string(filepath.Separator)))
	assert.NoDirExists(t, filepath.Join(libraryDir, liveSessionSegmentsDirName))
	require.FileExists(t, firstMetadata.OutputPath)
}

func TestLiveSessionSegmentRootsResolveSymlinkBeforeChoosingHiddenRoot(t *testing.T) {
	parent := t.TempDir()
	realLibraryRoot := filepath.Join(parent, "real-video")
	require.NoError(t, os.Mkdir(realLibraryRoot, 0o755))
	symlinkRoot := filepath.Join(parent, "video-link")
	require.NoError(t, os.Symlink(realLibraryRoot, symlinkRoot))

	resolvedRoot, hiddenRoot, err := liveSessionSegmentRoots(symlinkRoot)
	require.NoError(t, err)
	expectedRoot, err := filepath.EvalSymlinks(realLibraryRoot)
	require.NoError(t, err)
	assert.Equal(t, expectedRoot, resolvedRoot)
	relative, err := filepath.Rel(resolvedRoot, hiddenRoot)
	require.NoError(t, err)
	assert.True(t, relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)), "隐藏根必须位于真实媒体库根外: %s", hiddenRoot)

	seasonDir := filepath.Join(symlinkRoot, "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	libraryPath := filepath.Join(seasonDir, "主播.S01E0001.2026-07-29 - 直播.mp4")
	hiddenPath, err := hiddenLiveSessionSegmentPath(
		symlinkRoot,
		&knowledgeSessionManifest{SourceID: "source", LiveSessionID: "session"},
		libraryPath,
		libraryPath,
	)
	require.NoError(t, err)
	relative, err = filepath.Rel(resolvedRoot, hiddenPath)
	require.NoError(t, err)
	assert.True(t, relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)), "隐藏分段必须位于真实媒体库根外: %s", hiddenPath)
}

func TestLiveSessionSegmentRootsRejectHiddenRootSymlinkBackIntoLibrary(t *testing.T) {
	parent := t.TempDir()
	libraryRoot := filepath.Join(parent, "video")
	inlineHiddenRoot := filepath.Join(libraryRoot, "inline-hidden")
	require.NoError(t, os.MkdirAll(inlineHiddenRoot, 0o755))
	require.NoError(t, os.Symlink(inlineHiddenRoot, filepath.Join(parent, liveSessionSegmentsDirName)))

	resolvedRoot, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	require.Error(t, err)
	assert.Empty(t, resolvedRoot)
	assert.Empty(t, hiddenRoot)
	assert.Contains(t, err.Error(), "inside library root")
}

func TestPublishLiveSessionMediaAggregateRollsBackMoveReportedAfterDirectorySyncFailure(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")
	firstBefore := mustReadStageFile(t, firstLibraryPath)
	secondBefore := mustReadStageFile(t, secondLibraryPath)

	oldSync := liveSessionSyncDirectory
	failed := false
	liveSessionSyncDirectory = func(path string) error {
		if !failed && path == filepath.Dir(firstLibraryPath) && !fileExists(firstLibraryPath) {
			failed = true
			return errors.New("injected post-unlink directory sync failure")
		}
		return syncLiveSessionDirectory(path)
	}
	t.Cleanup(func() {
		liveSessionSyncDirectory = oldSync
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:segment-sync-rollback",
		LiveSessionID: "segment-sync-rollback",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-620", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.ErrorContains(t, err, "injected post-unlink directory sync failure")
	assert.Nil(t, aggregate)
	assert.Equal(t, firstBefore, mustReadStageFile(t, firstLibraryPath))
	assert.Equal(t, secondBefore, mustReadStageFile(t, secondLibraryPath))
}

func TestPublishLiveSessionMediaAggregateRollsBackLinkedTargetWhenHiddenDirectorySyncFails(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")
	firstVideoBefore := mustReadStageFile(t, firstLibraryPath)
	firstMetadataBefore := mustReadStageFile(t, firstMetadataPath)
	secondVideoBefore := mustReadStageFile(t, secondLibraryPath)
	secondMetadataBefore := mustReadStageFile(t, secondMetadataPath)

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:hidden-target-sync-rollback",
		LiveSessionID: "hidden-target-sync-rollback",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-620", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}
	firstHiddenPath, err := hiddenLiveSessionSegmentPath(libraryRoot, &manifest, firstLibraryPath, firstLibraryPath)
	require.NoError(t, err)

	oldSync := liveSessionSyncDirectory
	failed := false
	liveSessionSyncDirectory = func(path string) error {
		if !failed && sameCleanPath(path, filepath.Dir(firstHiddenPath)) && fileExists(firstHiddenPath) {
			failed = true
			return errors.New("injected hidden target directory sync failure")
		}
		return syncLiveSessionDirectory(path)
	}
	t.Cleanup(func() {
		liveSessionSyncDirectory = oldSync
	})

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.ErrorContains(t, err, "injected hidden target directory sync failure")
	assert.Nil(t, aggregate)
	assert.Equal(t, firstVideoBefore, mustReadStageFile(t, firstLibraryPath))
	assert.Equal(t, firstMetadataBefore, mustReadStageFile(t, firstMetadataPath))
	assert.Equal(t, secondVideoBefore, mustReadStageFile(t, secondLibraryPath))
	assert.Equal(t, secondMetadataBefore, mustReadStageFile(t, secondMetadataPath))
	assert.NoFileExists(t, firstHiddenPath)

	transactionDirs, globErr := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), liveSessionSegmentsDirName, ".segment_transactions", "segments-*"))
	require.NoError(t, globErr)
	assert.Empty(t, transactionDirs)
}

func TestPublishLiveSessionMediaAggregateKeepsCommittedHiddenSegmentWhenSourceWasAlreadyRemoved(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:source-already-removed",
		LiveSessionID: "source-already-removed",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-620", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}
	firstHiddenPath, err := hiddenLiveSessionSegmentPath(libraryRoot, &manifest, firstLibraryPath, firstLibraryPath)
	require.NoError(t, err)

	oldRemove := liveSessionRemoveFile
	injected := false
	liveSessionRemoveFile = func(path string) error {
		if !injected && sameCleanPath(path, firstLibraryPath) {
			injected = true
			require.NoError(t, os.Remove(path))
			return &os.PathError{Op: "remove", Path: path, Err: os.ErrNotExist}
		}
		return os.Remove(path)
	}
	t.Cleanup(func() {
		liveSessionRemoveFile = oldRemove
	})

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.NoFileExists(t, firstLibraryPath)
	require.FileExists(t, firstHiddenPath)
	firstMetadata, loadErr := subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, loadErr)
	assert.Equal(t, firstHiddenPath, firstMetadata.OutputPath)
	assert.Equal(t, firstHiddenPath, firstMetadata.RecordMeta["live_session_segment_hidden_path"])

	transactionDirs, globErr := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), liveSessionSegmentsDirName, ".segment_transactions", "segments-*"))
	require.NoError(t, globErr)
	assert.Empty(t, transactionDirs)
}

func TestPublishLiveSessionMediaAggregateKeepsSegmentsHiddenWhenAggregateMetadataSyncFailsAfterCommit(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")
	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:aggregate-metadata-sync",
		LiveSessionID: "aggregate-metadata-sync",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-620", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	firstAggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, firstAggregate)
	firstMetadata, err := subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, err)
	firstHiddenPath := firstMetadata.OutputPath

	require.NoError(t, os.Link(firstHiddenPath, firstLibraryPath))
	firstMetadata.OutputPath = firstLibraryPath
	firstMetadata.RecordMeta["live_session_segment_hidden_path"] = firstLibraryPath
	require.NoError(t, subtitle.SaveMetadata(firstMetadataPath, firstMetadata))
	aggregateMetadata, err := subtitle.LoadMetadata(firstAggregate.MetadataPath)
	require.NoError(t, err)
	rawSources := aggregateMetadata.RecordMeta["live_session_media_sources"].([]any)
	rawSources[0].(map[string]any)["output_path"] = firstLibraryPath
	require.NoError(t, subtitle.SaveMetadata(firstAggregate.MetadataPath, aggregateMetadata))

	oldSync := liveSessionSyncDirectory
	failed := false
	seasonDir, err := filepath.EvalSymlinks(filepath.Dir(firstLibraryPath))
	require.NoError(t, err)
	liveSessionSyncDirectory = func(path string) error {
		if !failed && sameCleanPath(path, seasonDir) {
			current, loadErr := subtitle.LoadMetadata(firstAggregate.MetadataPath)
			if loadErr == nil && !slices.Contains(liveSessionMetadataSourcePaths(t, current), firstLibraryPath) {
				failed = true
				return errors.New("aggregate metadata directory sync failed after rename")
			}
		}
		return syncLiveSessionDirectory(path)
	}
	t.Cleanup(func() {
		liveSessionSyncDirectory = oldSync
	})

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "aggregate metadata committed with hidden segment paths")
	assert.Contains(t, err.Error(), "preserved transaction")
	assert.NoFileExists(t, firstLibraryPath)
	require.FileExists(t, firstHiddenPath)
	firstMetadata, err = subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, err)
	assert.Equal(t, firstHiddenPath, firstMetadata.OutputPath)
	aggregateMetadata, err = subtitle.LoadMetadata(firstAggregate.MetadataPath)
	require.NoError(t, err)
	assert.Contains(t, liveSessionMetadataSourcePaths(t, aggregateMetadata), firstHiddenPath)
	assert.NotContains(t, liveSessionMetadataSourcePaths(t, aggregateMetadata), firstLibraryPath)

	transactionDirs, globErr := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), liveSessionSegmentsDirName, ".segment_transactions", "segments-*"))
	require.NoError(t, globErr)
	require.Len(t, transactionDirs, 1)
}

func TestPublishLiveSessionMediaAggregateDoesNotOverwriteTargetCreatedAfterPreflight(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")
	firstBefore := mustReadStageFile(t, firstLibraryPath)

	oldLink := liveSessionLinkFile
	var conflictPath string
	injected := false
	liveSessionLinkFile = func(source, target string) error {
		if !injected {
			injected = true
			conflictPath = target
			require.NoError(t, os.WriteFile(target, []byte("external-target"), 0o644))
		}
		return os.Link(source, target)
	}
	t.Cleanup(func() {
		liveSessionLinkFile = oldLink
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:segment-target-race",
		LiveSessionID: "segment-target-race",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-620", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Equal(t, firstBefore, mustReadStageFile(t, firstLibraryPath))
	require.NotEmpty(t, conflictPath)
	assert.Equal(t, []byte("external-target"), mustReadStageFile(t, conflictPath))
}

func TestPublishLiveSessionMediaAggregateRecordsUniqueHiddenPathWhenTargetExists(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")
	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:segment-target-conflict",
		LiveSessionID: "segment-target-conflict",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-620", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}
	conflictPath, err := hiddenLiveSessionSegmentPath(libraryRoot, &manifest, firstLibraryPath, firstLibraryPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(conflictPath), 0o755))
	require.NoError(t, os.WriteFile(conflictPath, []byte("external-target"), 0o644))

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.Equal(t, []byte("external-target"), mustReadStageFile(t, conflictPath))

	firstMetadata, err := subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, err)
	assert.NotEqual(t, conflictPath, firstMetadata.OutputPath)
	assert.Contains(t, firstMetadata.OutputPath, ".1.mp4")
	require.FileExists(t, firstMetadata.OutputPath)
	aggregateMetadata, err := subtitle.LoadMetadata(aggregate.MetadataPath)
	require.NoError(t, err)
	assert.Contains(t, liveSessionMetadataSourcePaths(t, aggregateMetadata), firstMetadata.OutputPath)
	for _, path := range liveSessionMetadataSourcePaths(t, aggregateMetadata) {
		require.FileExists(t, path)
	}
}

func TestHideLiveSessionSegmentVideosConvergesAfterCrashWindows(t *testing.T) {
	tests := []struct {
		name            string
		promoteMetadata bool
	}{
		{name: "after-target-link"},
		{name: "after-metadata-promotion", promoteMetadata: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			libraryRoot := t.TempDir()
			libraryPath, metadataPath, metadata := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "分段")
			manifest := knowledgeSessionManifest{
				SourceID:      "live-session:crash-window",
				LiveSessionID: "crash-window",
			}
			hiddenPath, err := hiddenLiveSessionSegmentPath(libraryRoot, &manifest, libraryPath, libraryPath)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(hiddenPath), 0o755))
			require.NoError(t, os.Link(libraryPath, hiddenPath))
			require.NoError(t, syncLiveSessionDirectory(filepath.Dir(hiddenPath)))

			if test.promoteMetadata {
				metadata.OutputPath = hiddenPath
				if metadata.RecordMeta == nil {
					metadata.RecordMeta = map[string]any{}
				}
				metadata.RecordMeta["live_session_media_role"] = "segment"
				metadata.RecordMeta["live_session_media_aggregate_path"] = filepath.Join(filepath.Dir(libraryPath), "aggregate.mp4")
				metadata.RecordMeta["live_session_segment_hidden_path"] = hiddenPath
				require.NoError(t, subtitle.SaveMetadata(metadataPath, metadata))
			}
			input := knowledgeSessionPayloadInput{
				LibraryPath:  libraryPath,
				MetadataPath: metadataPath,
				Metadata:     &metadata,
			}
			aggregatePath := filepath.Join(filepath.Dir(libraryPath), "aggregate.mp4")

			transaction, err := hideLiveSessionSegmentVideos(libraryRoot, &manifest, []knowledgeSessionPayloadInput{input}, aggregatePath)
			require.NoError(t, err)
			if transaction != nil {
				transaction.commit()
			}
			assert.NoFileExists(t, libraryPath)
			assert.Equal(t, []byte("video"), mustReadStageFile(t, hiddenPath))
			reloaded, err := subtitle.LoadMetadata(metadataPath)
			require.NoError(t, err)
			assert.Equal(t, hiddenPath, reloaded.OutputPath)
			assert.Equal(t, hiddenPath, reloaded.RecordMeta["live_session_segment_hidden_path"])
			hiddenVideos, err := filepath.Glob(filepath.Join(filepath.Dir(hiddenPath), "*.mp4"))
			require.NoError(t, err)
			assert.Equal(t, []string{hiddenPath}, hiddenVideos)
		})
	}
}

func TestHideLiveSessionSegmentVideosRollbackPreservesPreexistingHiddenHardlink(t *testing.T) {
	libraryRoot := t.TempDir()
	libraryPath, metadataPath, metadata := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "分段")
	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:preexisting-hidden",
		LiveSessionID: "preexisting-hidden",
	}
	hiddenPath, err := hiddenLiveSessionSegmentPath(libraryRoot, &manifest, libraryPath, libraryPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(hiddenPath), 0o755))
	require.NoError(t, os.Link(libraryPath, hiddenPath))
	require.NoError(t, syncLiveSessionDirectory(filepath.Dir(hiddenPath)))
	input := knowledgeSessionPayloadInput{
		LibraryPath:  libraryPath,
		MetadataPath: metadataPath,
		Metadata:     &metadata,
	}

	transaction, err := hideLiveSessionSegmentVideos(
		libraryRoot,
		&manifest,
		[]knowledgeSessionPayloadInput{input},
		filepath.Join(filepath.Dir(libraryPath), "aggregate.mp4"),
	)
	require.NoError(t, err)
	require.NotNil(t, transaction)
	assert.NoFileExists(t, libraryPath)
	require.FileExists(t, hiddenPath)

	require.NoError(t, transaction.rollback())
	require.FileExists(t, libraryPath)
	require.FileExists(t, hiddenPath)
	visibleInfo, err := os.Stat(libraryPath)
	require.NoError(t, err)
	hiddenInfo, err := os.Stat(hiddenPath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(visibleInfo, hiddenInfo))
	reloaded, err := subtitle.LoadMetadata(metadataPath)
	require.NoError(t, err)
	assert.Equal(t, libraryPath, reloaded.OutputPath)
}

func TestPublishLiveSessionMediaAggregateRejectsFilesystemRootLibrary(t *testing.T) {
	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:session-20260601-filesystem-root",
		LiveSessionID: "session-20260601-filesystem-root",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", LibraryPath: "/tmp/主播/Season 01/主播.S01E0019.2026-06-01 - 第一段.mp4"},
			{TaskID: "bililive-go-620", LibraryPath: "/tmp/主播/Season 01/主播.S01E0020.2026-06-01 - 第二段.mp4"},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", string(filepath.Separator), &manifest)
	require.ErrorContains(t, err, "cannot place live session segments outside library root")
	assert.Nil(t, aggregate)
}

func TestLiveSessionAggregatePathRejectsMalformedOrCrossShowInputs(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "主播", "Season 01", "主播.S01E0001.2026-07-29 - 直播.mp4")
	tests := []struct {
		name   string
		second string
	}{
		{name: "malformed", second: filepath.Join(root, "主播", "Season 01", "not-an-episode.mp4")},
		{name: "cross-show", second: filepath.Join(root, "其他主播", "Season 01", "其他主播.S01E0002.2026-07-29 - 直播.mp4")},
		{name: "cross-season", second: filepath.Join(root, "主播", "Season 02", "主播.S02E0002.2026-07-29 - 直播.mp4")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			aggregatePath, err := liveSessionAggregatePath([]knowledgeSessionPayloadInput{
				{LibraryPath: valid},
				{LibraryPath: test.second},
			})
			require.Error(t, err)
			assert.Empty(t, aggregatePath)
		})
	}
}

func TestPublishLiveSessionMediaAggregateFailsWhenCoverCannotBeCreated(t *testing.T) {
	stubLiveSessionMedia(t, []float64{120, 90})
	stubLiveSessionCoverExtraction(t, assert.AnError)
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 19, "第一段内容")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "建筑师 linkai", 20, "第二段内容")
	require.NoError(t, os.Remove(strings.TrimSuffix(firstLibraryPath, filepath.Ext(firstLibraryPath))+".jpg"))
	require.NoError(t, os.Remove(strings.TrimSuffix(secondLibraryPath, filepath.Ext(secondLibraryPath))+".jpg"))

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:session-20260601-linkai",
		LiveSessionID: "session-20260601-linkai",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-619", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-620", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)

	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "aggregate cover")
}

func TestPublishLiveSessionMediaAggregatePreparesIdentityAndCoverBeforeReplacingVisibleEpisode(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")
	require.NoError(t, os.Remove(strings.TrimSuffix(firstLibraryPath, filepath.Ext(firstLibraryPath))+".jpg"))
	require.NoError(t, os.Remove(strings.TrimSuffix(secondLibraryPath, filepath.Ext(secondLibraryPath))+".jpg"))
	showNFOPath := filepath.Join(libraryRoot, "天津蛋哥 6点说车", "tvshow.nfo")
	require.NoError(t, os.WriteFile(showNFOPath, []byte("<tvshow>\n  <title>天津蛋哥 6点说车</title>\n  <showtitle>天津蛋哥 6点说车</showtitle>\n  <custom>保留字段</custom>\n</tvshow>\n"), 0o644))

	coverObserved := false
	oldExtractCover := liveSessionMediaExtractCover
	oldRename := liveSessionMediaRename
	liveSessionMediaExtractCover = func(_ context.Context, videoPath, coverPath string) (string, error) {
		coverObserved = true
		assert.Contains(t, filepath.Base(videoPath), ".tmp.mp4")
		assert.Equal(t, []byte("video"), mustReadStageFile(t, secondLibraryPath), "合集身份和封面完成前不得替换可见视频")
		require.NoError(t, os.WriteFile(coverPath, []byte("aggregate-cover"), 0o644))
		return coverPath, nil
	}
	liveSessionMediaRename = func(oldPath, newPath string) error {
		assert.Contains(t, filepath.Base(oldPath), ".tmp.mp4")
		assert.Contains(t, filepath.Base(newPath), "S01E0032")
		assert.Contains(t, filepath.Base(newPath), "[同场聚合]")
		assert.Equal(t, []byte("video"), mustReadStageFile(t, secondLibraryPath), "发布合集视频前必须先准备并发布身份 sidecar")
		require.FileExists(t, strings.TrimSuffix(newPath, filepath.Ext(newPath))+".nfo")
		require.FileExists(t, showNFOPath)
		require.FileExists(t, strings.TrimSuffix(newPath, filepath.Ext(newPath))+".jpg")
		require.FileExists(t, filepath.Join(libraryRoot, "天津蛋哥 6点说车", "poster.jpg"))
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		liveSessionMediaExtractCover = oldExtractCover
		liveSessionMediaRename = oldRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-identity",
		LiveSessionID: "tianjin-identity",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	assert.True(t, coverObserved)
	assert.Equal(t, []byte("aggregate-video"), mustReadStageFile(t, aggregate.LibraryPath))
	assert.Equal(t, []byte("aggregate-cover"), mustReadStageFile(t, filepath.Join(libraryRoot, "天津蛋哥 6点说车", "poster.jpg")))
	showNFO := string(mustReadStageFile(t, showNFOPath))
	assert.Contains(t, showNFO, "<custom>保留字段</custom>")
	assert.Contains(t, showNFO, `<thumb aspect="poster">poster.jpg</thumb>`)
}

func TestPublishLiveSessionMediaAggregateReplacesMismatchedShowIdentity(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")
	showNFOPath := filepath.Join(libraryRoot, "天津蛋哥 6点说车", "tvshow.nfo")
	require.NoError(t, os.WriteFile(showNFOPath, []byte("<tvshow>\n  <title>天津蛋哥   6点说车</title>\n  <showtitle>天津蛋哥   6点说车</showtitle>\n  <custom>错误身份字段</custom>\n</tvshow>\n"), 0o644))

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-identity-drift",
		LiveSessionID: "tianjin-identity-drift",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	showNFO := string(mustReadStageFile(t, showNFOPath))
	assert.Contains(t, showNFO, "<title>天津蛋哥 6点说车</title>")
	assert.Contains(t, showNFO, "<showtitle>天津蛋哥 6点说车</showtitle>")
	assert.NotContains(t, showNFO, "<custom>错误身份字段</custom>")
}

func TestPublishLiveSessionMediaAggregateRebuildsMalformedMatchingShowIdentity(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")
	showNFOPath := filepath.Join(libraryRoot, "天津蛋哥 6点说车", "tvshow.nfo")
	require.NoError(t, os.WriteFile(showNFOPath, []byte("<tvshow>\n<title>天津蛋哥 6点说车</title>\n<showtitle>天津蛋哥 6点说车</showtitle>\n<plot>未闭合\n<custom>损坏文件</custom>\n</tvshow>\n"), 0o644))

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-malformed-identity",
		LiveSessionID: "tianjin-malformed-identity",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	showNFO := string(mustReadStageFile(t, showNFOPath))
	assert.Contains(t, showNFO, "<title>天津蛋哥 6点说车</title>")
	assert.Contains(t, showNFO, `<thumb aspect="poster">poster.jpg</thumb>`)
	assert.NotContains(t, showNFO, "<custom>损坏文件</custom>")
}

func TestPublishLiveSessionMediaAggregateRestoresVisibleEpisodeWhenReplaceFails(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")
	showDir := filepath.Join(libraryRoot, "天津蛋哥 6点说车")
	posterPath := filepath.Join(showDir, "poster.jpg")
	secondNFOPath := strings.TrimSuffix(secondLibraryPath, filepath.Ext(secondLibraryPath)) + ".nfo"
	showNFOPath := filepath.Join(showDir, "tvshow.nfo")
	require.NoError(t, os.WriteFile(posterPath, []byte("curated-poster"), 0o644))
	require.NoError(t, os.WriteFile(secondNFOPath, []byte("original-episode-nfo"), 0o644))
	require.NoError(t, os.WriteFile(showNFOPath, []byte("original-show-nfo"), 0o644))

	protectedPaths := []string{
		secondLibraryPath,
		strings.TrimSuffix(secondLibraryPath, filepath.Ext(secondLibraryPath)) + ".srt",
		strings.TrimSuffix(secondLibraryPath, filepath.Ext(secondLibraryPath)) + ".ass",
		secondNFOPath,
		strings.TrimSuffix(secondLibraryPath, filepath.Ext(secondLibraryPath)) + ".jpg",
		secondMetadataPath,
		showNFOPath,
		posterPath,
	}
	before := make(map[string][]byte, len(protectedPaths))
	for _, path := range protectedPaths {
		before[path] = mustReadStageFile(t, path)
	}

	oldRename := liveSessionMediaRename
	liveSessionMediaRename = func(_, _ string) error {
		return errors.New("replace failed")
	}
	t.Cleanup(func() {
		liveSessionMediaRename = oldRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-rollback",
		LiveSessionID: "tianjin-rollback",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.ErrorContains(t, err, "replace failed")
	assert.Nil(t, aggregate)
	for path, expected := range before {
		assert.Equal(t, expected, mustReadStageFile(t, path), "失败后必须恢复 %s", path)
	}
	stagingDirs, err := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), liveSessionSegmentsDirName, ".sidecar_staging", "live-session-sidecars-*"))
	require.NoError(t, err)
	assert.Empty(t, stagingDirs)
}

func TestPublishLiveSessionMediaAggregateNeverRemovesVisibleIdentitySidecarDuringPromotion(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")
	secondNFOPath := strings.TrimSuffix(secondLibraryPath, filepath.Ext(secondLibraryPath)) + ".nfo"
	require.NoError(t, os.WriteFile(secondNFOPath, []byte("original-episode-nfo"), 0o644))

	oldRename := liveSessionSidecarRename
	liveSessionSidecarRename = func(oldPath, newPath string) error {
		if err := os.Rename(oldPath, newPath); err != nil {
			return err
		}
		if strings.Contains(newPath, string(filepath.Separator)+".backup"+string(filepath.Separator)) && !fileExists(secondNFOPath) {
			return errors.New("identity sidecar became temporarily absent")
		}
		return nil
	}
	t.Cleanup(func() {
		liveSessionSidecarRename = oldRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-sidecar-atomic",
		LiveSessionID: "tianjin-sidecar-atomic",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.NoError(t, err)
	require.NotNil(t, aggregate)
}

func TestPublishLiveSessionMediaAggregateDoesNotOverwriteConcurrentSidecarUpdateDuringRollback(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")
	var concurrentNFOPath string

	oldRename := liveSessionMediaRename
	liveSessionMediaRename = func(_, newPath string) error {
		concurrentNFOPath = strings.TrimSuffix(newPath, filepath.Ext(newPath)) + ".nfo"
		require.NoError(t, os.WriteFile(concurrentNFOPath, []byte("concurrent-sidecar-update"), 0o644))
		return errors.New("replace failed after concurrent sidecar update")
	}
	t.Cleanup(func() {
		liveSessionMediaRename = oldRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-sidecar-cas",
		LiveSessionID: "tianjin-sidecar-cas",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "recovery failed")
	assert.Contains(t, err.Error(), "changed before rollback")
	require.NotEmpty(t, concurrentNFOPath)
	assert.Equal(t, []byte("concurrent-sidecar-update"), mustReadStageFile(t, concurrentNFOPath))
}

func TestPublishLiveSessionMediaAggregateDoesNotOverwriteConcurrentVideoUpdateDuringRollback(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")
	aggregatePath, err := liveSessionAggregatePath([]knowledgeSessionPayloadInput{
		{LibraryPath: firstLibraryPath},
		{LibraryPath: secondLibraryPath},
	})
	require.NoError(t, err)

	oldRename := liveSessionSidecarRename
	liveSessionSidecarRename = func(oldPath, newPath string) error {
		if strings.HasSuffix(oldPath, ".subtitle.json") {
			require.NoError(t, os.WriteFile(aggregatePath, []byte("concurrent-video-update"), 0o644))
			return errors.New("metadata promotion failed after concurrent video update")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		liveSessionSidecarRename = oldRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-video-cas",
		LiveSessionID: "tianjin-video-cas",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstMetadataPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}
	manifest.Sources[0].LibraryPath = firstLibraryPath

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "recovery failed")
	assert.Contains(t, err.Error(), "changed before rollback")
	assert.Equal(t, []byte("concurrent-video-update"), mustReadStageFile(t, aggregatePath))
	require.FileExists(t, strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))+".nfo")
	require.FileExists(t, strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))+".srt")
}

func TestPublishLiveSessionMediaAggregateDoesNotPartiallyRollbackWhenSidecarChangesAfterVideoPromotion(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")
	aggregatePath, err := liveSessionAggregatePath([]knowledgeSessionPayloadInput{
		{LibraryPath: firstLibraryPath},
		{LibraryPath: secondLibraryPath},
	})
	require.NoError(t, err)
	aggregateNFOPath := strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath)) + ".nfo"

	oldRename := liveSessionSidecarRename
	liveSessionSidecarRename = func(oldPath, newPath string) error {
		if strings.HasSuffix(oldPath, ".subtitle.json") {
			require.NoError(t, os.WriteFile(aggregateNFOPath, []byte("concurrent-sidecar-update"), 0o644))
			return errors.New("metadata promotion failed after concurrent sidecar update")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		liveSessionSidecarRename = oldRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-group-cas",
		LiveSessionID: "tianjin-group-cas",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "recovery failed")
	assert.Contains(t, err.Error(), "changed before rollback")
	assert.Equal(t, []byte("concurrent-sidecar-update"), mustReadStageFile(t, aggregateNFOPath))
	assert.Equal(t, []byte("aggregate-video"), mustReadStageFile(t, aggregatePath))
	require.FileExists(t, strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))+".srt")
}

func TestPublishLiveSessionMediaAggregateKeepsAppliedSidecarsWhenVideoRestoreFails(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")
	aggregatePath, err := liveSessionAggregatePath([]knowledgeSessionPayloadInput{
		{LibraryPath: firstLibraryPath},
		{LibraryPath: secondLibraryPath},
	})
	require.NoError(t, err)
	aggregatePath, err = canonicalLiveSessionAggregatePath(aggregatePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(aggregatePath, []byte("old-aggregate"), 0o644))

	oldMediaRename := liveSessionMediaRename
	oldSidecarRename := liveSessionSidecarRename
	liveSessionMediaRename = func(oldPath, newPath string) error {
		if strings.Contains(oldPath, string(filepath.Separator)+".backup"+string(filepath.Separator)) {
			return errors.New("injected aggregate video restore failure")
		}
		return os.Rename(oldPath, newPath)
	}
	liveSessionSidecarRename = func(oldPath, newPath string) error {
		if strings.HasSuffix(oldPath, ".subtitle.json") {
			return errors.New("injected aggregate metadata promotion failure")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		liveSessionMediaRename = oldMediaRename
		liveSessionSidecarRename = oldSidecarRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-video-restore-failure",
		LiveSessionID: "tianjin-video-restore-failure",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "injected aggregate video restore failure")
	assert.Contains(t, err.Error(), "preserved staging")
	assert.Equal(t, []byte("aggregate-video"), mustReadStageFile(t, aggregatePath))
	require.FileExists(t, strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))+".nfo")
	require.FileExists(t, strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))+".srt")
}

func TestPublishLiveSessionMediaAggregateRestoresAllSegmentsWhenMetadataPromotionFails(t *testing.T) {
	stubLiveSessionMediaForEpisodeList(t, []float64{60, 70, 80}, []string{"S01E0031", "S01E0032", "S01E0033"})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 31, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第二段")
	thirdLibraryPath, thirdMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第三段")

	protectedPaths := []string{
		firstLibraryPath, firstMetadataPath,
		secondLibraryPath, secondMetadataPath,
		thirdLibraryPath, thirdMetadataPath,
	}
	before := make(map[string][]byte, len(protectedPaths))
	for _, path := range protectedPaths {
		before[path] = mustReadStageFile(t, path)
	}

	oldMetadataRename := liveSessionMetadataRename
	promotions := 0
	liveSessionMetadataRename = func(oldPath, newPath string) error {
		if strings.HasSuffix(oldPath, ".new.json") {
			promotions++
			if promotions == 2 {
				return errors.New("second metadata promotion failed")
			}
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		liveSessionMetadataRename = oldMetadataRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-segment-rollback",
		LiveSessionID: "tianjin-segment-rollback",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-631", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, thirdLibraryPath), LibraryPath: thirdLibraryPath, MetadataPath: thirdMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.ErrorContains(t, err, "second metadata promotion failed")
	assert.Nil(t, aggregate)
	for path, expected := range before {
		assert.Equal(t, expected, mustReadStageFile(t, path), "失败后必须恢复 %s", path)
	}
	hiddenRoot := filepath.Join(filepath.Dir(libraryRoot), liveSessionSegmentsDirName)
	sidecarStaging, globErr := filepath.Glob(filepath.Join(hiddenRoot, ".sidecar_staging", "live-session-sidecars-*"))
	require.NoError(t, globErr)
	assert.Empty(t, sidecarStaging)
	segmentTransactions, globErr := filepath.Glob(filepath.Join(hiddenRoot, ".segment_transactions", "segments-*"))
	require.NoError(t, globErr)
	assert.Empty(t, segmentTransactions)
}

func TestPublishLiveSessionMediaAggregateRefusesToOverwriteConcurrentSegmentMetadataDuringRollback(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")

	oldMetadataRename := liveSessionMetadataRename
	promotions := 0
	liveSessionMetadataRename = func(oldPath, newPath string) error {
		if strings.HasSuffix(oldPath, ".new.json") {
			promotions++
			if promotions == 2 {
				require.NoError(t, os.WriteFile(firstMetadataPath, []byte("concurrent-segment-metadata"), 0o644))
				return errors.New("second segment metadata promotion failed")
			}
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		liveSessionMetadataRename = oldMetadataRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-segment-metadata-cas",
		LiveSessionID: "tianjin-segment-metadata-cas",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "segment recovery refused")
	assert.Contains(t, err.Error(), "changed before rollback")
	assert.Equal(t, []byte("concurrent-segment-metadata"), mustReadStageFile(t, firstMetadataPath))
	assert.NoFileExists(t, firstLibraryPath)

	transactionDirs, globErr := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), liveSessionSegmentsDirName, ".segment_transactions", "segments-*"))
	require.NoError(t, globErr)
	require.Len(t, transactionDirs, 1)
}

func TestPublishLiveSessionMediaAggregatePreservesSegmentTransactionWhenRecoveryFails(t *testing.T) {
	stubLiveSessionMediaForEpisodeList(t, []float64{60, 70, 80}, []string{"S01E0031", "S01E0032", "S01E0033"})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 31, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第二段")
	thirdLibraryPath, thirdMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第三段")

	oldMetadataRename := liveSessionMetadataRename
	promotions := 0
	liveSessionMetadataRename = func(oldPath, newPath string) error {
		if strings.HasSuffix(oldPath, ".old.json") {
			return errors.New("metadata recovery failed")
		}
		if strings.HasSuffix(oldPath, ".new.json") {
			promotions++
			if promotions == 2 {
				return errors.New("second metadata promotion failed")
			}
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		liveSessionMetadataRename = oldMetadataRename
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-preserve-transaction",
		LiveSessionID: "tianjin-preserve-transaction",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-631", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, thirdLibraryPath), LibraryPath: thirdLibraryPath, MetadataPath: thirdMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "segment recovery failed")
	assert.Contains(t, err.Error(), "metadata recovery failed")
	assert.Contains(t, err.Error(), "preserved transaction")

	transactionDirs, globErr := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), liveSessionSegmentsDirName, ".segment_transactions", "segments-*"))
	require.NoError(t, globErr)
	require.Len(t, transactionDirs, 1)
	require.FileExists(t, filepath.Join(transactionDirs[0], "000.old.json"))

	firstMetadata, loadErr := subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, loadErr)
	assert.NotEqual(t, firstLibraryPath, firstMetadata.OutputPath)
	require.FileExists(t, firstMetadata.OutputPath)
	require.FileExists(t, firstLibraryPath)
}

func TestPublishLiveSessionMediaAggregateKeepsHiddenSegmentWhenRestoredSourceSyncFails(t *testing.T) {
	stubLiveSessionMediaForEpisodeList(t, []float64{60, 70, 80}, []string{"S01E0031", "S01E0032", "S01E0033"})
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 31, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第二段")
	thirdLibraryPath, thirdMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第三段")

	oldMetadataRename := liveSessionMetadataRename
	oldSyncDirectory := liveSessionSyncDirectory
	promotions := 0
	recoveryArmed := false
	liveSessionMetadataRename = func(oldPath, newPath string) error {
		if strings.HasSuffix(oldPath, ".new.json") {
			promotions++
			if promotions == 2 {
				recoveryArmed = true
				return errors.New("second metadata promotion failed")
			}
		}
		return os.Rename(oldPath, newPath)
	}
	liveSessionSyncDirectory = func(path string) error {
		if recoveryArmed && sameCleanPath(path, filepath.Dir(firstLibraryPath)) {
			return errors.New("restored source directory sync failed")
		}
		return syncLiveSessionDirectory(path)
	}
	t.Cleanup(func() {
		liveSessionMetadataRename = oldMetadataRename
		liveSessionSyncDirectory = oldSyncDirectory
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-restored-source-sync",
		LiveSessionID: "tianjin-restored-source-sync",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-631", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, thirdLibraryPath), LibraryPath: thirdLibraryPath, MetadataPath: thirdMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "restored source directory sync failed")
	assert.Contains(t, err.Error(), "preserved transaction")

	firstMetadata, loadErr := subtitle.LoadMetadata(firstMetadataPath)
	require.NoError(t, loadErr)
	assert.NotEqual(t, firstLibraryPath, firstMetadata.OutputPath)
	require.FileExists(t, firstMetadata.OutputPath)
	require.FileExists(t, firstLibraryPath)

	transactionDirs, globErr := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), liveSessionSegmentsDirName, ".segment_transactions", "segments-*"))
	require.NoError(t, globErr)
	require.Len(t, transactionDirs, 1)
}

func TestPublishLiveSessionMediaAggregatePreservesSidecarStagingWhenRollbackFails(t *testing.T) {
	stubLiveSessionMediaForEpisodes(t, []float64{120, 90}, "S01E0032", "S01E0033")
	libraryRoot := t.TempDir()
	firstLibraryPath, firstMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 32, "第一段")
	secondLibraryPath, secondMetadataPath, _ := writeCompletedKnowledgeSessionSidecar(t, libraryRoot, "天津蛋哥 6点说车", 33, "第二段")

	oldMediaRename := liveSessionMediaRename
	oldSidecarRemove := liveSessionSidecarRemove
	liveSessionMediaRename = func(_, _ string) error {
		return errors.New("replace failed")
	}
	liveSessionSidecarRemove = func(path string) error {
		if strings.HasSuffix(path, ".srt") {
			return errors.New("sidecar recovery failed")
		}
		return os.Remove(path)
	}
	t.Cleanup(func() {
		liveSessionMediaRename = oldMediaRename
		liveSessionSidecarRemove = oldSidecarRemove
	})

	manifest := knowledgeSessionManifest{
		SourceID:      "live-session:tianjin-preserve-staging",
		LiveSessionID: "tianjin-preserve-staging",
		Sources: []knowledgeSessionManifestSource{
			{TaskID: "bililive-go-632", SourceID: knowledgeSourceID(libraryRoot, firstLibraryPath), LibraryPath: firstLibraryPath, MetadataPath: firstMetadataPath},
			{TaskID: "bililive-go-633", SourceID: knowledgeSourceID(libraryRoot, secondLibraryPath), LibraryPath: secondLibraryPath, MetadataPath: secondMetadataPath},
		},
	}

	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", libraryRoot, &manifest)
	require.Error(t, err)
	assert.Nil(t, aggregate)
	assert.Contains(t, err.Error(), "recovery failed")
	assert.Contains(t, err.Error(), "sidecar recovery failed")
	assert.Contains(t, err.Error(), "preserved staging")

	stagingDirs, globErr := filepath.Glob(filepath.Join(filepath.Dir(libraryRoot), liveSessionSegmentsDirName, ".sidecar_staging", "live-session-sidecars-*"))
	require.NoError(t, globErr)
	require.Len(t, stagingDirs, 1)
}

func knowledgeSessionTestContext(taskID int64) *pipeline.PipelineContext {
	return &pipeline.PipelineContext{
		TaskID: taskID,
		RecordInfo: pipeline.RecordInfo{
			Platform:      "哔哩哔哩",
			HostName:      "建筑师 linkai",
			RoomName:      "设计师还在加班画图吗？进来看看！",
			LiveSessionID: "session-20260601-linkai",
		},
	}
}

func writeCompletedKnowledgeSessionSidecar(t *testing.T, libraryRoot, host string, episode int, text string) (string, string, subtitle.Metadata) {
	t.Helper()

	base := filepath.Join(libraryRoot, host, "Season 01", fmt.Sprintf("%s.S01E%04d.2026-06-01 - 设计师还在加班画图吗？进来看看！", host, episode))
	libraryPath := base + ".mp4"
	srtPath := base + ".srt"
	assPath := base + ".ass"
	metadataPath := base + ".subtitle.json"
	require.NoError(t, os.MkdirAll(filepath.Dir(libraryPath), 0o755))
	require.NoError(t, os.WriteFile(libraryPath, []byte("video"), 0o644))
	require.NoError(t, os.WriteFile(base+".jpg", []byte("cover"), 0o644))
	require.NoError(t, os.WriteFile(srtPath, []byte(text), 0o644))
	require.NoError(t, os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644))
	completedAt := time.Date(2026, 6, 1, 18, episode, 0, 0, time.UTC)
	metadata := subtitle.Metadata{
		Status:         subtitle.StatusCompleted,
		Language:       "zh",
		OutputPath:     libraryPath,
		SRTPath:        srtPath,
		ASSPath:        assPath,
		RendererStatus: subtitle.StatusCompleted,
		Segments: []subtitle.Segment{
			{Index: 1, Start: "00:00:00,000", End: "00:00:03,000", Text: text},
		},
		CompletedAt: &completedAt,
	}
	require.NoError(t, subtitle.SaveMetadata(metadataPath, metadata))
	return libraryPath, metadataPath, metadata
}

func visibleMP4Files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".mp4") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

func stubLiveSessionMedia(t *testing.T, durations []float64) {
	stubLiveSessionMediaForEpisodes(t, durations, "S01E0019", "S01E0020")
}

func stubLiveSessionMediaForEpisodes(t *testing.T, durations []float64, firstEpisode, secondEpisode string) {
	stubLiveSessionMediaForEpisodeList(t, durations, []string{firstEpisode, secondEpisode})
}

func stubLiveSessionMediaForEpisodeList(t *testing.T, durations []float64, episodes []string) {
	t.Helper()
	require.Len(t, durations, len(episodes))
	oldConcat := liveSessionMediaConcat
	oldProbeDuration := liveSessionMediaProbeDuration
	liveSessionMediaConcat = func(ctx context.Context, ffmpegPath string, inputs []string, outputPath string) error {
		require.Len(t, inputs, len(episodes))
		for index, episode := range episodes {
			require.Contains(t, inputs[index], episode)
		}
		return os.WriteFile(outputPath, []byte("aggregate-video"), 0o644)
	}
	liveSessionMediaProbeDuration = func(ctx context.Context, ffmpegPath string, inputPath string) (float64, error) {
		for index, episode := range episodes {
			if strings.Contains(inputPath, episode) {
				return durations[index], nil
			}
		}
		return 0, fmt.Errorf("unexpected input path: %s", inputPath)
	}
	t.Cleanup(func() {
		liveSessionMediaConcat = oldConcat
		liveSessionMediaProbeDuration = oldProbeDuration
	})
}

func stubLiveSessionCoverExtraction(t *testing.T, err error) {
	t.Helper()
	oldExtractCover := liveSessionMediaExtractCover
	liveSessionMediaExtractCover = func(ctx context.Context, videoPath, coverPath string) (string, error) {
		if err != nil {
			return "", err
		}
		if writeErr := os.WriteFile(coverPath, []byte("cover"), 0o644); writeErr != nil {
			return "", writeErr
		}
		return coverPath, nil
	}
	t.Cleanup(func() {
		liveSessionMediaExtractCover = oldExtractCover
	})
}

func requireRetryLater(t *testing.T, err error) {
	t.Helper()
	var retryLater *pipeline.RetryLaterError
	require.True(t, errors.As(err, &retryLater), "expected RetryLaterError, got %v", err)
}

func mustReadStageFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return content
}

func liveSessionMetadataSourcePaths(t *testing.T, metadata subtitle.Metadata) []string {
	t.Helper()
	rawSources, ok := metadata.RecordMeta["live_session_media_sources"].([]any)
	require.True(t, ok)
	paths := make([]string, 0, len(rawSources))
	for _, rawSource := range rawSources {
		source, ok := rawSource.(map[string]any)
		require.True(t, ok)
		outputPath, ok := source["output_path"].(string)
		require.True(t, ok)
		paths = append(paths, outputPath)
	}
	return paths
}
