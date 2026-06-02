package stages

import (
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/types"
	"github.com/stretchr/testify/assert"
)

func TestKnowledgeSessionSameLiveIDGroupsLongSegmentAndContinuation(t *testing.T) {
	start := time.Date(2026, 6, 1, 18, 0, 0, 0, time.Local)
	first := knowledgeSessionCandidate{
		RecordInfo: pipeline.RecordInfo{
			LiveID:        types.LiveID("bilibili:linkai"),
			Platform:      "哔哩哔哩",
			HostName:      "建筑师 linkai",
			RoomName:      "设计师还在加班画图吗？进来看看！",
			StartTime:     start,
			LiveSessionID: "session-20260601-linkai",
		},
		LibraryPath:     "/video/建筑师 linkai/Season 01/建筑师 linkai.S01E0019.2026-06-01 - 设计师还在加班画图吗？进来看看！.mp4",
		DurationSeconds: 68 * 60,
	}
	continuation := knowledgeSessionCandidate{
		RecordInfo: pipeline.RecordInfo{
			LiveID:        types.LiveID("bilibili:linkai"),
			Platform:      "哔哩哔哩",
			HostName:      "建筑师 linkai",
			RoomName:      "设计师还在加班画图吗？进来看看！",
			StartTime:     start.Add(69 * time.Minute),
			LiveSessionID: "session-20260601-linkai",
		},
		LibraryPath:     "/video/建筑师 linkai/Season 01/建筑师 linkai.S01E0020.2026-06-01 - 设计师还在加班画图吗？进来看看！.mp4",
		DurationSeconds: 22*60 + 53,
	}

	assert.True(t, sameKnowledgeLiveSession(first, continuation, 10*time.Minute))
	assert.Equal(t, "live-session:session-20260601-linkai", knowledgeSessionKey(first))
	assert.False(t, shouldSkipStandaloneKnowledgeArtifact(continuation.DurationSeconds, knowledgeSessionKey(continuation) != "", 600*time.Second))
}

func TestKnowledgeSessionFallbackGroupsAdjacentReconnectShardWithoutDurationRule(t *testing.T) {
	start := time.Date(2026, 5, 31, 2, 58, 0, 0, time.Local)
	first := knowledgeSessionCandidate{
		RecordInfo: pipeline.RecordInfo{
			LiveID:    types.LiveID("bilibili:tangshan"),
			Platform:  "哔哩哔哩",
			HostName:  "汤山老王",
			RoomName:  "行情好像要结束了？",
			StartTime: start,
		},
		LibraryPath:     "/video/汤山老王/Season 01/汤山老王.S01E0013.2026-05-30 - 行情好像要结束了？.mp4",
		DurationSeconds: 58,
	}
	second := knowledgeSessionCandidate{
		RecordInfo: pipeline.RecordInfo{
			LiveID:    types.LiveID("bilibili:tangshan"),
			Platform:  "哔哩哔哩",
			HostName:  "汤山老王",
			RoomName:  "行情好像要结束了？",
			StartTime: start.Add(62 * time.Second),
		},
		LibraryPath:     "/video/汤山老王/Season 01/汤山老王.S01E0014.2026-05-30 - 行情好像要结束了？.mp4",
		DurationSeconds: 47,
	}

	assert.True(t, sameKnowledgeLiveSession(first, second, 5*time.Minute))
	assert.NotEmpty(t, knowledgeSessionKey(first))
	assert.False(t, shouldSkipStandaloneKnowledgeArtifact(second.DurationSeconds, sameKnowledgeLiveSession(first, second, 5*time.Minute), 600*time.Second))
}

func TestKnowledgeSessionFallbackRejectsLargeQuietGap(t *testing.T) {
	start := time.Date(2026, 5, 31, 2, 58, 0, 0, time.Local)
	first := knowledgeSessionCandidate{
		RecordInfo: pipeline.RecordInfo{
			LiveID:    types.LiveID("bilibili:tangshan"),
			Platform:  "哔哩哔哩",
			HostName:  "汤山老王",
			RoomName:  "行情好像要结束了？",
			StartTime: start,
		},
		DurationSeconds: 60,
	}
	nextDay := knowledgeSessionCandidate{
		RecordInfo: pipeline.RecordInfo{
			LiveID:    types.LiveID("bilibili:tangshan"),
			Platform:  "哔哩哔哩",
			HostName:  "汤山老王",
			RoomName:  "行情好像要结束了？",
			StartTime: start.Add(24 * time.Hour),
		},
		DurationSeconds: 60,
	}

	assert.False(t, sameKnowledgeLiveSession(first, nextDay, 5*time.Minute))
}

func TestKnowledgeSessionSkipsOnlyStandaloneShortArtifacts(t *testing.T) {
	assert.True(t, shouldSkipStandaloneKnowledgeArtifact(2.5, false, 3*time.Second))
	assert.False(t, shouldSkipStandaloneKnowledgeArtifact(2.5, true, 3*time.Second))
	assert.False(t, shouldSkipStandaloneKnowledgeArtifact(20*60, false, 3*time.Second))
}
