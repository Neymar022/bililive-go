package stages

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	require.Len(t, capturedPayload.SourceVideos, 2)
	assert.True(t, strings.Contains(capturedPayload.SourceVideos[0].SourceVideoPath, "S01E0019"))
	assert.True(t, strings.Contains(capturedPayload.SourceVideos[1].SourceVideoPath, "S01E0020"))
	require.Len(t, capturedPayload.Segments, 2)
	assert.Equal(t, 0, capturedPayload.Segments[0].SourceIndex)
	assert.Equal(t, 1, capturedPayload.Segments[1].SourceIndex)

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

func requireRetryLater(t *testing.T, err error) {
	t.Helper()
	var retryLater *pipeline.RetryLaterError
	require.True(t, errors.As(err, &retryLater), "expected RetryLaterError, got %v", err)
}
