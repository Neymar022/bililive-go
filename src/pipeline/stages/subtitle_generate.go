package stages

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
)

type SubtitleGenerateStage struct {
	config   pipeline.StageConfig
	commands []string
	logs     string
}

func NewSubtitleGenerateStage(config pipeline.StageConfig) (pipeline.Stage, error) {
	return &SubtitleGenerateStage{config: config}, nil
}

func (s *SubtitleGenerateStage) Name() string {
	return pipeline.StageNameSubtitleGenerate
}

func (s *SubtitleGenerateStage) Execute(ctx *pipeline.PipelineContext, input []pipeline.FileInfo) ([]pipeline.FileInfo, error) {
	if len(input) == 0 {
		s.logs = "没有输入文件"
		return input, nil
	}

	cfg := configs.GetCurrentConfig()
	if cfg == nil || !cfg.Subtitle.Enabled {
		s.logs = "字幕功能未启用，跳过处理"
		return input, nil
	}

	libraryRoot := cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath)
	provider := s.config.GetStringOption("provider", cfg.Subtitle.DefaultProvider)
	language := s.config.GetStringOption("language", cfg.Subtitle.Language)
	preset := subtitle.ResolveRenderPreset(
		s.config.GetStringOption("preset", ""),
		"",
		cfg.Subtitle.BurnStyle,
	)

	var output []pipeline.FileInfo
	for _, file := range input {
		if file.Type != pipeline.FileTypeVideo {
			output = append(output, file)
			continue
		}

		libraryPath, err := subtitle.ResolveLibraryVideoPath(file.Path, libraryRoot)
		if err != nil {
			return nil, err
		}
		srtPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".srt"
		metadataPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".subtitle.json"

		recordMeta := map[string]any{
			"platform":   ctx.RecordInfo.Platform,
			"host_name":  ctx.RecordInfo.HostName,
			"room_name":  ctx.RecordInfo.RoomName,
			"start_time": ctx.RecordInfo.StartTime,
		}

		metadata := subtitle.Metadata{
			Status:         subtitle.StatusRunning,
			Provider:       provider,
			Language:       language,
			SourcePath:     file.Path,
			OutputPath:     libraryPath,
			SRTPath:        srtPath,
			SourceExists:   fileExists(file.Path),
			RenderPreset:   preset,
			RendererStatus: subtitle.StatusRunning,
			RecordMeta:     recordMeta,
		}
		if err := subtitle.SaveMetadata(metadataPath, metadata); err != nil {
			return nil, err
		}

		request := subtitle.ProcessRequest{
			SourcePath:      file.Path,
			OutputVideoPath: libraryPath,
			OutputSRTPath:   srtPath,
			Provider:        provider,
			Language:        language,
			BurnStyle:       cfg.Subtitle.BurnStyle,
			RecordMeta:      recordMeta,
		}
		request.BurnStyle.Preset = preset
		s.commands = append(s.commands, fmt.Sprintf("POST %s/api/v1/process", strings.TrimRight(cfg.Subtitle.GetWorkerURL(), "/")))

		response, err := subtitle.ProcessFile(cfg.Subtitle.GetWorkerURL(), request)
		if err != nil {
			metadata.Status = subtitle.StatusFailed
			metadata.LastError = err.Error()
			metadata.RendererStatus = subtitle.StatusFailed
			metadata.RendererError = err.Error()
			metadata.SourceExists = fileExists(file.Path)
			_ = subtitle.SaveMetadata(metadataPath, metadata)
			return nil, err
		}

		now := time.Now().UTC()
		metadata.Status = subtitle.StatusCompleted
		metadata.LastError = ""
		metadata.RenderPreset = subtitle.ResolveRenderPreset(response.RenderPreset, preset, cfg.Subtitle.BurnStyle)
		metadata.RendererStatus = subtitle.StatusCompleted
		metadata.RendererError = ""
		metadata.Segments = response.Segments
		metadata.CompletedAt = &now
		metadata.SourceExists = fileExists(file.Path)
		if err := subtitle.SaveMetadata(metadataPath, metadata); err != nil {
			return nil, err
		}

		s.logs += fmt.Sprintf("字幕生成完成: %s\n", filepath.Base(libraryPath))

		output = append(output,
			pipeline.FileInfo{
				Path:       libraryPath,
				Type:       pipeline.FileTypeVideo,
				SourcePath: file.Path,
			},
			pipeline.FileInfo{
				Path:       srtPath,
				Type:       pipeline.FileTypeOther,
				SourcePath: file.Path,
			},
		)
	}

	return output, nil
}

func (s *SubtitleGenerateStage) GetCommands() []string {
	return s.commands
}

func (s *SubtitleGenerateStage) GetLogs() string {
	return s.logs
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
