package pipeline

import (
	"testing"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/stretchr/testify/assert"
)

func TestGetEffectivePipelineConfigAddsSubtitleStage(t *testing.T) {
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.AutoGenerate = true
	configs.SetCurrentConfig(cfg)

	pipelineConfig := GetEffectivePipelineConfig(&configs.OnRecordFinished{
		ConvertToMp4:          true,
		DeleteFlvAfterConvert: true,
	})

	stageNames := make([]string, 0, len(pipelineConfig.Stages))
	for _, stage := range pipelineConfig.Stages {
		stageNames = append(stageNames, stage.Name)
	}

	assert.Equal(t, []string{StageNameConvertMp4, StageNameSubtitleGenerate}, stageNames)
	assert.True(t, pipelineConfig.Stages[1].GetBoolOption(OptionSubtitleScheduled, false))
}

func TestGetEffectivePipelineConfigSkipsSubtitleStageWhenDisabled(t *testing.T) {
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = false
	configs.SetCurrentConfig(cfg)

	pipelineConfig := GetEffectivePipelineConfig(&configs.OnRecordFinished{
		ConvertToMp4: true,
	})

	stageNames := make([]string, 0, len(pipelineConfig.Stages))
	for _, stage := range pipelineConfig.Stages {
		stageNames = append(stageNames, stage.Name)
	}

	assert.Equal(t, []string{StageNameConvertMp4}, stageNames)
}

func TestGetEffectivePipelineConfigSkipsSubtitleStageWithoutMp4(t *testing.T) {
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.AutoGenerate = true
	configs.SetCurrentConfig(cfg)

	pipelineConfig := GetEffectivePipelineConfig(&configs.OnRecordFinished{
		ConvertToMp4: false,
	})

	assert.Empty(t, pipelineConfig.Stages)
}

func TestGetEffectivePipelineConfigPreservesSubtitleBeforeBurnSubtitles(t *testing.T) {
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.AutoGenerate = true
	configs.SetCurrentConfig(cfg)

	pipelineConfig := GetEffectivePipelineConfig(&configs.OnRecordFinished{
		ConvertToMp4:        true,
		BurnSubtitles:       true,
		BurnSubtitlesCodec:  "libx264",
		BurnSubtitlesCrf:    "23",
		BurnSubtitlesPreset: "fast",
		BurnDeleteAss:       true,
		BurnDeleteSource:    true,
		SaveCover:           true,
	})

	stageNames := make([]string, 0, len(pipelineConfig.Stages))
	for _, stage := range pipelineConfig.Stages {
		stageNames = append(stageNames, stage.Name)
	}

	assert.Equal(t, []string{
		StageNameConvertMp4,
		StageNameSubtitleGenerate,
		StageNameBurnSubtitles,
		StageNameExtractCover,
	}, stageNames)
	assert.True(t, pipelineConfig.Stages[1].GetBoolOption(OptionSubtitleScheduled, false))
	assert.Equal(t, "libx264", pipelineConfig.Stages[2].GetStringOption(OptionCodec, ""))
	assert.Equal(t, "23", pipelineConfig.Stages[2].GetStringOption(OptionCrf, ""))
	assert.Equal(t, "fast", pipelineConfig.Stages[2].GetStringOption(OptionPreset, ""))
	assert.True(t, pipelineConfig.Stages[2].GetBoolOption(OptionBurnDeleteAss, false))
	assert.True(t, pipelineConfig.Stages[2].GetBoolOption(OptionBurnDeleteSource, false))
}
