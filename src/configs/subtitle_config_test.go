package configs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSubtitleConfigDefaults(t *testing.T) {
	cfg := NewConfig()

	assert.False(t, cfg.Subtitle.Enabled)
	assert.True(t, cfg.Subtitle.AutoGenerate)
	assert.Equal(t, "dashscope", cfg.Subtitle.DefaultProvider)
	assert.Equal(t, 7, cfg.Subtitle.RetentionDays)
	assert.Equal(t, "small", cfg.Subtitle.Local.Model)
	assert.Equal(t, "int8", cfg.Subtitle.Local.ComputeType)
	assert.Equal(t, "aliyun", cfg.Subtitle.Cloud.Vendor)
	assert.Equal(t, "qwen3-asr-flash-filetrans", cfg.Subtitle.Cloud.Model)
	assert.Equal(t, "zh", cfg.Subtitle.Language)
	assert.Zero(t, cfg.Subtitle.MinLibraryVideoDurationSeconds)
	assert.False(t, cfg.Subtitle.Schedule.Enabled)
	assert.Equal(t, "02:00", cfg.Subtitle.Schedule.RunAt)
	assert.False(t, cfg.Subtitle.KnowledgeSync.Enabled)
	assert.True(t, cfg.Subtitle.KnowledgeSync.GenerateNote)
	assert.True(t, cfg.Subtitle.KnowledgeSync.NonBlocking)
	assert.Empty(t, cfg.Subtitle.KnowledgeSync.Format)
	assert.Nil(t, cfg.Subtitle.KnowledgeSync.Link)
	assert.Nil(t, cfg.Subtitle.KnowledgeSync.Screenshot)
	assert.Nil(t, cfg.Subtitle.KnowledgeSync.VideoUnderstanding)
	assert.Zero(t, cfg.Subtitle.KnowledgeSync.VideoInterval)
	assert.Empty(t, cfg.Subtitle.KnowledgeSync.GridSize)
	assert.Equal(t, DefaultSubtitleKnowledgeSyncTimeoutSeconds, cfg.Subtitle.KnowledgeSync.TimeoutSeconds)
	assert.Equal(t, DefaultSubtitleKnowledgeSyncMinVideoDurationSeconds, cfg.Subtitle.KnowledgeSync.MinVideoDurationSeconds)
	assert.Equal(t, "vizard_classic_cn", cfg.Subtitle.BurnStyle.Preset)
	assert.Equal(t, 50, cfg.Subtitle.BurnStyle.FontSize)
	assert.Equal(t, 1018, cfg.Subtitle.BurnStyle.CardWidth)
	assert.Equal(t, 196, cfg.Subtitle.BurnStyle.CardHeight)
	assert.Equal(t, 640, cfg.Subtitle.BurnStyle.BottomOffset)
	assert.InDelta(t, 0.9, cfg.Subtitle.BurnStyle.BackgroundOpacity, 0.0001)
	assert.InDelta(t, 0.08, cfg.Subtitle.BurnStyle.BorderOpacity, 0.0001)
	assert.True(t, cfg.Subtitle.BurnStyle.SingleLine)
	assert.Equal(t, "ellipsis", cfg.Subtitle.BurnStyle.OverflowMode)
	assert.Equal(t, "vizard_classic_cn", cfg.Subtitle.BurnStyle.GetEffectivePreset())
	assert.Equal(t, cfg.OutPutPath, cfg.Subtitle.GetEffectiveSourceRoot(cfg.OutPutPath))
	assert.Equal(t, cfg.OutPutPath, cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath))
	assert.Equal(t, DefaultSubtitleWorkerURL, cfg.Subtitle.GetWorkerURL())
}

func TestSubtitleConfigWorkerURLUsesEnvironment(t *testing.T) {
	t.Setenv("SUBTITLE_WORKER_URL", "http://subtitle-worker:8091")

	cfg := NewConfig()

	assert.Equal(t, "http://subtitle-worker:8091", cfg.Subtitle.GetWorkerURL())
}

func TestSubtitleKnowledgeSyncUsesEnvironment(t *testing.T) {
	t.Setenv("BILINOTE_KNOWLEDGE_INGEST_URL", "http://bilinote-backend:8483/api/knowledge/ingest")
	t.Setenv("BILINOTE_INGEST_TOKEN", "env-token")
	t.Setenv("BILINOTE_INGEST_PROVIDER_ID", "qwen")
	t.Setenv("BILINOTE_INGEST_MODEL_NAME", "qwen3.6-plus")

	cfg := NewConfig()
	cfg.Subtitle.KnowledgeSync.Endpoint = "http://wrong.example/api/knowledge/ingest"
	cfg.Subtitle.KnowledgeSync.Token = "config-token"

	assert.Equal(t, "http://bilinote-backend:8483/api/knowledge/ingest", cfg.Subtitle.KnowledgeSync.GetEndpoint())
	assert.Equal(t, "env-token", cfg.Subtitle.KnowledgeSync.GetToken())
	assert.Equal(t, "qwen", cfg.Subtitle.KnowledgeSync.GetProviderID())
	assert.Equal(t, "qwen3.6-plus", cfg.Subtitle.KnowledgeSync.GetModelName())
}

func TestSubtitleScheduleNextRunAfter(t *testing.T) {
	schedule := SubtitleScheduleConfig{Enabled: true, RunAt: "02:00"}
	loc := time.FixedZone("test", 8*60*60)

	sameDay, err := schedule.NextRunAfter(time.Date(2026, 5, 27, 1, 30, 0, 0, loc))
	assert.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 27, 2, 0, 0, 0, loc), sameDay)

	nextDay, err := schedule.NextRunAfter(time.Date(2026, 5, 27, 15, 0, 0, 0, loc))
	assert.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 28, 2, 0, 0, 0, loc), nextDay)
}

func TestConfigVerifyRejectsInvalidSubtitleLibraryRoot(t *testing.T) {
	cfg := NewConfig()
	cfg.OutPutPath = t.TempDir()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = filepath.Join(cfg.OutPutPath, "missing")

	err := cfg.Verify()

	assert.Error(t, err)
	assert.ErrorContains(t, err, "字幕库路径")
}

func TestConfigVerifyAcceptsSubtitleRoots(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(sourceRoot, "video")
	err := os.MkdirAll(libraryRoot, 0o755)
	assert.NoError(t, err)

	cfg := NewConfig()
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.PublicURLBase = "https://bililive.example.com"

	assert.NoError(t, cfg.Verify())
}

func TestConfigVerifyRejectsInvalidKnowledgeSync(t *testing.T) {
	sourceRoot := t.TempDir()
	libraryRoot := filepath.Join(sourceRoot, "video")
	err := os.MkdirAll(libraryRoot, 0o755)
	assert.NoError(t, err)

	cfg := NewConfig()
	cfg.OutPutPath = sourceRoot
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = libraryRoot
	cfg.Subtitle.KnowledgeSync.Enabled = true

	err = cfg.Verify()
	assert.Error(t, err)
	assert.ErrorContains(t, err, "BiliNote 知识同步地址")
}

func TestSubtitleConfigMarshalRoundTrip(t *testing.T) {
	cfg := NewConfig()
	cfg.OutPutPath = t.TempDir()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = filepath.Join(cfg.OutPutPath, "video")
	cfg.Subtitle.RetentionDays = 14
	cfg.Subtitle.MinLibraryVideoDurationSeconds = 60
	cfg.Subtitle.Schedule.Enabled = true
	cfg.Subtitle.Schedule.RunAt = "02:00"
	cfg.Subtitle.BurnStyle.FontSize = 28
	cfg.Subtitle.BurnStyle.CardWidth = 960
	cfg.Subtitle.BurnStyle.CardHeight = 180
	cfg.Subtitle.BurnStyle.BottomOffset = 520
	cfg.Subtitle.BurnStyle.BackgroundOpacity = 0.76
	cfg.Subtitle.BurnStyle.BorderOpacity = 0.04
	cfg.Subtitle.BurnStyle.SingleLine = false
	cfg.Subtitle.BurnStyle.OverflowMode = "wrap"
	cfg.Subtitle.BurnStyle.MarginV = 32
	cfg.Subtitle.KnowledgeSync.Enabled = true
	cfg.Subtitle.KnowledgeSync.Endpoint = "http://bilinote-backend:8483/api/knowledge/ingest"
	cfg.Subtitle.KnowledgeSync.Token = "config-token"
	cfg.Subtitle.KnowledgeSync.ProviderID = "qwen"
	cfg.Subtitle.KnowledgeSync.ModelName = "qwen3.6-plus"
	cfg.Subtitle.KnowledgeSync.Format = []string{"toc", "link", "screenshot", "summary"}
	cfg.Subtitle.KnowledgeSync.Link = boolValue(true)
	cfg.Subtitle.KnowledgeSync.Screenshot = boolValue(true)
	cfg.Subtitle.KnowledgeSync.VideoUnderstanding = boolValue(true)
	cfg.Subtitle.KnowledgeSync.VideoInterval = 4
	cfg.Subtitle.KnowledgeSync.GridSize = []int{3, 3}
	cfg.Subtitle.KnowledgeSync.TimeoutSeconds = 45
	cfg.Subtitle.KnowledgeSync.MinVideoDurationSeconds = 6
	cfg.Subtitle.UpdatedAt = time.Unix(1_763_200_000, 0).UTC()

	blob, err := os.ReadFile("../../config.yml")
	assert.NoError(t, err)

	loaded, err := NewConfigWithBytes(blob)
	assert.NoError(t, err)
	loaded.OutPutPath = cfg.OutPutPath
	loaded.Subtitle = cfg.Subtitle

	bytes, err := loaded.ToYAMLBytes()
	assert.NoError(t, err)

	roundTripped, err := NewConfigWithBytes(bytes)
	assert.NoError(t, err)
	assert.True(t, roundTripped.Subtitle.Enabled)
	assert.Equal(t, 14, roundTripped.Subtitle.RetentionDays)
	assert.Equal(t, 60, roundTripped.Subtitle.MinLibraryVideoDurationSeconds)
	assert.True(t, roundTripped.Subtitle.Schedule.Enabled)
	assert.Equal(t, "02:00", roundTripped.Subtitle.Schedule.RunAt)
	assert.Equal(t, 28, roundTripped.Subtitle.BurnStyle.FontSize)
	assert.Equal(t, 960, roundTripped.Subtitle.BurnStyle.CardWidth)
	assert.Equal(t, 180, roundTripped.Subtitle.BurnStyle.CardHeight)
	assert.Equal(t, 520, roundTripped.Subtitle.BurnStyle.BottomOffset)
	assert.InDelta(t, 0.76, roundTripped.Subtitle.BurnStyle.BackgroundOpacity, 0.0001)
	assert.InDelta(t, 0.04, roundTripped.Subtitle.BurnStyle.BorderOpacity, 0.0001)
	assert.False(t, roundTripped.Subtitle.BurnStyle.SingleLine)
	assert.Equal(t, "wrap", roundTripped.Subtitle.BurnStyle.OverflowMode)
	assert.Equal(t, 32, roundTripped.Subtitle.BurnStyle.MarginV)
	assert.Equal(t, cfg.Subtitle.PublicURLBase, roundTripped.Subtitle.PublicURLBase)
	assert.True(t, roundTripped.Subtitle.KnowledgeSync.Enabled)
	assert.Equal(t, cfg.Subtitle.KnowledgeSync.Endpoint, roundTripped.Subtitle.KnowledgeSync.Endpoint)
	assert.Equal(t, cfg.Subtitle.KnowledgeSync.Token, roundTripped.Subtitle.KnowledgeSync.Token)
	assert.Equal(t, cfg.Subtitle.KnowledgeSync.ProviderID, roundTripped.Subtitle.KnowledgeSync.ProviderID)
	assert.Equal(t, cfg.Subtitle.KnowledgeSync.ModelName, roundTripped.Subtitle.KnowledgeSync.ModelName)
	assert.True(t, roundTripped.Subtitle.KnowledgeSync.GenerateNote)
	assert.True(t, roundTripped.Subtitle.KnowledgeSync.NonBlocking)
	assert.Equal(t, []string{"toc", "link", "screenshot", "summary"}, roundTripped.Subtitle.KnowledgeSync.Format)
	assert.NotNil(t, roundTripped.Subtitle.KnowledgeSync.Link)
	assert.True(t, *roundTripped.Subtitle.KnowledgeSync.Link)
	assert.NotNil(t, roundTripped.Subtitle.KnowledgeSync.Screenshot)
	assert.True(t, *roundTripped.Subtitle.KnowledgeSync.Screenshot)
	assert.NotNil(t, roundTripped.Subtitle.KnowledgeSync.VideoUnderstanding)
	assert.True(t, *roundTripped.Subtitle.KnowledgeSync.VideoUnderstanding)
	assert.Equal(t, 4, roundTripped.Subtitle.KnowledgeSync.VideoInterval)
	assert.Equal(t, []int{3, 3}, roundTripped.Subtitle.KnowledgeSync.GridSize)
	assert.Equal(t, 45, roundTripped.Subtitle.KnowledgeSync.TimeoutSeconds)
	assert.Equal(t, 6, roundTripped.Subtitle.KnowledgeSync.MinVideoDurationSeconds)
}

func boolValue(value bool) *bool {
	return &value
}
