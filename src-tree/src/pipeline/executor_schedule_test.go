package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testStage struct {
	name string
	run  func(input []FileInfo) []FileInfo
}

func (s testStage) Name() string {
	return s.name
}

func (s testStage) Execute(ctx *PipelineContext, input []FileInfo) ([]FileInfo, error) {
	if s.run == nil {
		return input, nil
	}
	return s.run(input), nil
}

func TestExecutorDefersScheduledStageAfterCompletedPrefix(t *testing.T) {
	executor := NewExecutor(nil)
	executor.RegisterStage("convert", func(config StageConfig) (Stage, error) {
		return testStage{name: "convert", run: func(input []FileInfo) []FileInfo {
			return []FileInfo{NewVideoFileInfo("/tmp/converted.mp4")}
		}}, nil
	})
	executor.RegisterStage(StageNameSubtitleGenerate, func(config StageConfig) (Stage, error) {
		t.Fatal("subtitle stage should not execute before not_before")
		return testStage{name: StageNameSubtitleGenerate}, nil
	})

	notBefore := time.Now().Add(time.Hour)
	var currentStage int
	var currentFiles []FileInfo

	_, err := executor.Execute(
		&PipelineContext{
			Ctx: context.Background(),
			ShouldDeferStage: func(stageIndex int, stage StageConfig) (*time.Time, bool) {
				if stage.Name == StageNameSubtitleGenerate && stage.GetBoolOption(OptionSubtitleScheduled, false) {
					return &notBefore, true
				}
				return nil, false
			},
			OnStageCompleted: func(stageIndex int, result StageResult, output []FileInfo) {
				currentStage = stageIndex + 1
				currentFiles = output
			},
		},
		&PipelineConfig{Stages: []StageConfig{
			{Name: "convert"},
			{Name: StageNameSubtitleGenerate, Options: map[string]any{OptionSubtitleScheduled: true}},
		}},
		[]FileInfo{NewVideoFileInfo("/tmp/source.flv")},
		nil,
	)

	var deferred *DeferredExecution
	require.True(t, errors.As(err, &deferred), "expected DeferredExecution, got %v", err)
	assert.Equal(t, 1, deferred.StageIndex)
	assert.Equal(t, StageNameSubtitleGenerate, deferred.StageName)
	assert.Equal(t, notBefore, deferred.NotBefore)
	assert.Equal(t, 1, currentStage)
	require.Len(t, currentFiles, 1)
	assert.Equal(t, "/tmp/converted.mp4", currentFiles[0].Path)
}

func TestExecutorResumesFromCurrentStage(t *testing.T) {
	executor := NewExecutor(nil)
	executor.RegisterStage("convert", func(config StageConfig) (Stage, error) {
		return testStage{name: "convert", run: func(input []FileInfo) []FileInfo {
			t.Fatal("completed prefix stage should not run again")
			return input
		}}, nil
	})
	subtitleRuns := 0
	executor.RegisterStage(StageNameSubtitleGenerate, func(config StageConfig) (Stage, error) {
		return testStage{name: StageNameSubtitleGenerate, run: func(input []FileInfo) []FileInfo {
			subtitleRuns++
			return append(input, FileInfo{Path: "/tmp/video.srt", Type: FileTypeOther})
		}}, nil
	})

	results, err := executor.Execute(
		&PipelineContext{Ctx: context.Background(), StartStage: 1},
		&PipelineConfig{Stages: []StageConfig{
			{Name: "convert"},
			{Name: StageNameSubtitleGenerate, Options: map[string]any{OptionSubtitleScheduled: true}},
		}},
		[]FileInfo{NewVideoFileInfo("/tmp/converted.mp4")},
		nil,
	)

	require.NoError(t, err)
	assert.Equal(t, 1, subtitleRuns)
	require.Len(t, results, 1)
	assert.Equal(t, StageNameSubtitleGenerate, results[0].StageName)
	require.Len(t, results[0].OutputFiles, 2)
	assert.Equal(t, "/tmp/video.srt", results[0].OutputFiles[1].Path)
}
