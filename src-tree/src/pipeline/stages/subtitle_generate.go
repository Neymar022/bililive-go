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
	"github.com/sirupsen/logrus"
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

	// stage 在此点之后会写若干 .subtitle.json sidecar，使 ListRecords 缓存过时。
	// 不论成功失败、中途 return err 都要让 list 能看到最新状态——defer 里统一处理。
	defer subtitle.InvalidateRecordCache()

	libraryRoot := cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath)
	sourceRoot := cfg.Subtitle.GetEffectiveSourceRoot(cfg.OutPutPath)
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
		if !strings.EqualFold(filepath.Ext(file.Path), ".mp4") {
			return nil, fmt.Errorf("subtitle_generate: requires mp4 input after fix_flv/convert_mp4, got %s", file.Path)
		}

		libraryPath, err := subtitle.ResolveLibraryVideoPath(file.Path, libraryRoot)
		if err != nil {
			// Self-sufficient mode: cron organizer hasn't run yet (race window).
			// Create the Plex-style hardlink in-process so the pipeline never fails
			// due to a missing library entry.  P17: eliminate race condition.
			libraryPath, err = subtitle.EnsureLibraryHardlink(
				ctx.Ctx, file.Path, libraryRoot,
				ctx.RecordInfo.HostName, ctx.RecordInfo.StartTime, ctx.RecordInfo.Platform,
			)
			if err != nil {
				return nil, fmt.Errorf("subtitle_generate: failed to ensure library hardlink: %w", err)
			}
			logrus.WithFields(logrus.Fields{
				"source":  file.Path,
				"library": libraryPath,
			}).Info("subtitle_generate: 已自建字幕库硬链接（cron 尚未运行）")
		}
		srtPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".srt"
		assPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".ass"
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
			ASSPath:        assPath,
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
		s.commands = append(s.commands, fmt.Sprintf("POST %s/api/v1/process (最多 %d 次重试)", strings.TrimRight(cfg.Subtitle.GetWorkerURL(), "/"), subtitle.DefaultProcessMaxAttempts))

		response, err := subtitle.ProcessFileWithRetry(cfg.Subtitle.GetWorkerURL(), request, subtitle.DefaultProcessMaxAttempts)
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
		if response.ASSPath != "" {
			metadata.ASSPath = response.ASSPath
		}
		metadata.Segments = response.Segments
		metadata.CompletedAt = &now
		metadata.SourceExists = fileExists(file.Path)
		if err := subtitle.SaveMetadata(metadataPath, metadata); err != nil {
			return nil, err
		}

		// P11: 完成后立即删除源文件（节省存储空间）
		// 触发条件：cfg.Subtitle.DeleteSourceOnCompletion=true + KeepSource=false +
		// 源文件还存在。失败不让 pipeline 失败——只 log warning，retention_days 后台
		// cleanup ticker 会兜底重试。
		if cfg.Subtitle.DeleteSourceOnCompletion && !metadata.KeepSource && metadata.SourceExists {
			if err := subtitle.DeleteSourceFile(libraryPath, sourceRoot); err != nil {
				logrus.WithError(err).WithField("source", file.Path).
					Warn("字幕完成后删除源文件失败（不阻塞 pipeline，retention 后台兜底）")
				s.logs += fmt.Sprintf("源文件删除失败（已记入日志）: %s\n", filepath.Base(file.Path))
			} else {
				s.logs += fmt.Sprintf("源文件已删除（节省存储）: %s\n", filepath.Base(file.Path))
			}
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
			pipeline.FileInfo{
				Path:       metadata.ASSPath,
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
