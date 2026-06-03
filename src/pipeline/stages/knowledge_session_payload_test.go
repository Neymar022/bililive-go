package stages

import (
	"testing"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildKnowledgeSessionIngestPayloadKeepsPerSegmentSourceEvidence(t *testing.T) {
	cfg := configs.SubtitleKnowledgeSyncConfig{
		GenerateNote: true,
		NonBlocking:  true,
		ProviderID:   "qwen",
		ModelName:    "qwen3.7-plus",
		Style:        "教程",
	}
	ctx := &pipeline.PipelineContext{
		TaskID: 620,
		RecordInfo: pipeline.RecordInfo{
			HostName:      "建筑师 linkai",
			RoomName:      "设计师还在加班画图吗？进来看看！",
			LiveSessionID: "session-20260601-linkai",
		},
	}

	payload, err := buildKnowledgeSessionIngestPayload(ctx, cfg, "/video", []knowledgeSessionPayloadInput{
		{
			TaskID:      "bililive-go-619",
			LibraryPath: "/video/建筑师 linkai/Season 01/建筑师 linkai.S01E0019.2026-06-01 - 设计师还在加班画图吗？进来看看！.mp4",
			Metadata: &subtitle.Metadata{
				SRTPath:  "/video/建筑师 linkai/Season 01/建筑师 linkai.S01E0019.2026-06-01 - 设计师还在加班画图吗？进来看看！.srt",
				Language: "zh",
				Segments: []subtitle.Segment{
					{Index: 1, Start: "00:00:10,000", End: "00:00:20,000", Text: "第一段内容"},
				},
			},
		},
		{
			TaskID:      "bililive-go-620",
			LibraryPath: "/video/建筑师 linkai/Season 01/建筑师 linkai.S01E0020.2026-06-01 - 设计师还在加班画图吗？进来看看！.mp4",
			Metadata: &subtitle.Metadata{
				SRTPath:  "/video/建筑师 linkai/Season 01/建筑师 linkai.S01E0020.2026-06-01 - 设计师还在加班画图吗？进来看看！.srt",
				Language: "zh",
				Segments: []subtitle.Segment{
					{Index: 1, Start: "00:00:01,000", End: "00:00:03,000", Text: "第二段内容"},
				},
			},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "live-session:session-20260601-linkai", payload.SourceID)
	assert.Equal(t, "session-20260601-linkai", payload.LiveSessionID)
	assert.Equal(t, "qwen3.7-plus", payload.ModelName)
	assert.Equal(t, "教程", payload.Style)
	require.Len(t, payload.SourceVideos, 2)
	assert.Equal(t, "bililive-go-619", payload.SourceVideos[0].TaskID)
	assert.Equal(t, "bililive-go-620", payload.SourceVideos[1].TaskID)
	require.Len(t, payload.MediaSegments, 2)
	assert.Equal(t, payload.SourceVideos, payload.MediaSegments)
	require.Len(t, payload.Segments, 2)
	assert.Equal(t, 0, payload.Segments[0].SourceIndex)
	assert.Equal(t, 1, payload.Segments[1].SourceIndex)
	assert.Equal(t, "/video/建筑师 linkai/Season 01/建筑师 linkai.S01E0020.2026-06-01 - 设计师还在加班画图吗？进来看看！.mp4", payload.Segments[1].SourceVideoPath)
	assert.Greater(t, payload.Segments[1].Start, payload.Segments[0].End, "聚合后的全局时间线应保持前后顺序")
}
