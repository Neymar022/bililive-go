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
