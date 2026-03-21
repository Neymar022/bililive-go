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
	assert.Equal(t, "vizard_classic_cn", cfg.Subtitle.BurnStyle.Preset)
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

func TestSubtitleConfigMarshalRoundTrip(t *testing.T) {
	cfg := NewConfig()
	cfg.OutPutPath = t.TempDir()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = filepath.Join(cfg.OutPutPath, "video")
	cfg.Subtitle.RetentionDays = 14
	cfg.Subtitle.BurnStyle.FontSize = 28
	cfg.Subtitle.BurnStyle.MarginV = 32
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
	assert.Equal(t, 28, roundTripped.Subtitle.BurnStyle.FontSize)
	assert.Equal(t, 32, roundTripped.Subtitle.BurnStyle.MarginV)
	assert.Equal(t, cfg.Subtitle.PublicURLBase, roundTripped.Subtitle.PublicURLBase)
}
