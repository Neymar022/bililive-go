package stages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pkg/utils"
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
	sessionRecording := strings.TrimSpace(ctx.RecordInfo.LiveSessionID) != ""
	if sessionRecording && (ctx.RecordInfo.RecordingProducerID == "" || ctx.SessionMediaReady == nil) {
		return nil, errors.New("historical live session requires verified input closure migration")
	}
	provider := s.config.GetStringOption("provider", cfg.Subtitle.DefaultProvider)
	language := s.config.GetStringOption("language", cfg.Subtitle.Language)
	preset := subtitle.ResolveRenderPreset(
		s.config.GetStringOption("preset", ""),
		"",
		cfg.Subtitle.BurnStyle,
	)

	var output []pipeline.FileInfo
	var sessionSources []pipeline.SessionMediaSource
	for index, file := range input {
		if file.Type != pipeline.FileTypeVideo {
			output = append(output, file)
			continue
		}
		if !strings.EqualFold(filepath.Ext(file.Path), ".mp4") {
			return nil, fmt.Errorf("subtitle_generate: requires mp4 input after fix_flv/convert_mp4, got %s", file.Path)
		}

		if skipped, durationSeconds, minDuration := s.shouldSkipLibraryPublish(ctx, cfg.Subtitle, file.Path, libraryRoot); !sessionRecording && skipped {
			s.cleanupShortLibraryLink(file.Path, libraryRoot)
			s.logs += fmt.Sprintf("媒体库发布已跳过（视频过短 %.2fs <= %.0fs）: %s\n", durationSeconds, minDuration.Seconds(), filepath.Base(file.Path))
			if ctx.Logger != nil {
				ctx.Logger.Infof("媒体库发布已跳过（视频过短 %.2fs <= %.0fs）: %s", durationSeconds, minDuration.Seconds(), file.Path)
			}
			logrus.WithFields(logrus.Fields{
				"source":           file.Path,
				"duration_seconds": durationSeconds,
				"minimum_seconds":  minDuration.Seconds(),
			}).Info("subtitle_generate: skipped library publish for short video")
			continue
		}

		var libraryPath string
		var err error
		if sessionRecording {
			libraryPath, err = subtitle.RecordingWorkPath(libraryRoot, ctx.RecordInfo.LiveSessionID, ctx.TaskID, index, file.Path, ctx.RecordInfo.HostName, ctx.RecordInfo.StartTime)
			if err != nil {
				return nil, err
			}
			defer subtitle.LockRecordingWork(libraryPath)()
		} else {
			libraryPath, err = subtitle.ResolveLibraryVideoPath(file.Path, libraryRoot)
		}
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
		if !sessionRecording {
			if err := subtitle.EnsureLibrarySidecars(ctx.Ctx, file.Path, libraryPath, ctx.RecordInfo.HostName, ctx.RecordInfo.StartTime, ctx.RecordInfo.Platform); err != nil {
				return nil, fmt.Errorf("subtitle_generate: failed to ensure library sidecars: %w", err)
			}
		}
		srtPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".srt"
		assPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".ass"
		metadataPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".subtitle.json"

		var completed subtitle.Metadata
		var complete bool
		if sessionRecording {
			completed, complete, err = loadRecordingCheckpoint(ctx, file.Path, libraryPath)
			if err != nil {
				return nil, err
			}
		} else {
			completed, complete = loadCompletedSubtitleMetadata(metadataPath, libraryPath, srtPath, assPath)
		}
		if metadata := completed; complete {
			if sessionRecording {
				sessionSources = append(sessionSources, pipeline.SessionMediaSource{InputPath: file.Path, LibraryPath: libraryPath, MetadataPath: metadataPath})
			} else {
				s.deleteSourceAfterCompletion(cfg.Subtitle, libraryPath, sourceRoot, metadataPath, &metadata)
				if err := s.syncKnowledge(ctx, cfg.Subtitle.KnowledgeSync, libraryRoot, libraryPath, metadataPath, &metadata); err != nil {
					return nil, err
				}
			}
			s.logs += fmt.Sprintf("字幕结果已存在，跳过转写: %s\n", subtitle.MediaDisplayTitle(libraryPath))
			output = append(output,
				pipeline.FileInfo{
					Path:       libraryPath,
					Type:       pipeline.FileTypeVideo,
					SourcePath: file.Path,
				},
				pipeline.FileInfo{
					Path:       metadata.SRTPath,
					Type:       pipeline.FileTypeOther,
					SourcePath: file.Path,
				},
				pipeline.FileInfo{
					Path:       metadata.ASSPath,
					Type:       pipeline.FileTypeOther,
					SourcePath: file.Path,
				},
			)
			continue
		}

		saveMetadata := subtitle.SaveMetadata
		if sessionRecording {
			saveMetadata, err = recordingMetadataWriter(metadataPath, completed)
			if err != nil {
				return nil, err
			}
		}
		workerVideoPath, workerSRTPath := libraryPath, srtPath
		recordMeta := map[string]any{
			"platform":   ctx.RecordInfo.Platform,
			"host_name":  ctx.RecordInfo.HostName,
			"room_name":  ctx.RecordInfo.RoomName,
			"start_time": ctx.RecordInfo.StartTime,
		}
		if sessionRecording {
			recordMeta["live_session_id"] = ctx.RecordInfo.LiveSessionID
			recordMeta["live_session_media_role"] = "segment"
			recordMeta["recording_producer_id"] = ctx.RecordInfo.RecordingProducerID
			recordMeta["pipeline_task_id"] = strconv.FormatInt(ctx.TaskID, 10)
			attemptDir, err := os.MkdirTemp(filepath.Dir(libraryPath), ".attempt-*")
			if err != nil {
				return nil, err
			}
			workerVideoPath = filepath.Join(attemptDir, filepath.Base(libraryPath))
			workerSRTPath = strings.TrimSuffix(workerVideoPath, ".mp4") + ".srt"
			recordMeta["recording_attempt_path"] = workerVideoPath
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
		if err := saveMetadata(metadataPath, metadata); err != nil {
			return nil, err
		}

		request := subtitle.ProcessRequest{
			SourcePath:      file.Path,
			OutputVideoPath: workerVideoPath,
			OutputSRTPath:   workerSRTPath,
			Provider:        provider,
			Language:        language,
			BurnStyle:       cfg.Subtitle.BurnStyle,
			RecordMeta:      recordMeta,
		}
		request.BurnStyle.Preset = preset
		maxAttempts := subtitle.DefaultProcessMaxAttempts
		if sessionRecording {
			// 网络结果不确定时保留独立 attempt，不盲目再次烧录。
			maxAttempts = 1
		}
		s.commands = append(s.commands, fmt.Sprintf("POST %s/api/v1/process (最多 %d 次尝试)", strings.TrimRight(cfg.Subtitle.GetWorkerURL(), "/"), maxAttempts))

		response, err := subtitle.ProcessFileWithRetry(cfg.Subtitle.GetWorkerURL(), request, maxAttempts)
		if err != nil {
			if subtitle.IsMacTranscriberUnavailable(err) {
				metadata.Status = subtitle.StatusQueued
				metadata.LastError = err.Error()
				metadata.RendererStatus = subtitle.StatusQueued
				metadata.RendererError = err.Error()
				metadata.SourceExists = fileExists(file.Path)
				if saveErr := saveMetadata(metadataPath, metadata); sessionRecording && saveErr != nil {
					return nil, saveErr
				}
				return nil, pipeline.NewRetryLaterError(err, subtitleRetryLaterDelay())
			}
			metadata.Status = subtitle.StatusFailed
			metadata.LastError = err.Error()
			metadata.RendererStatus = subtitle.StatusFailed
			metadata.RendererError = err.Error()
			metadata.SourceExists = fileExists(file.Path)
			// 普通 HTTP 失败也可能发生在远端完成之后；只有明确未执行的错误可自动重试。
			if sessionRecording {
				metadata.Status, metadata.RendererStatus = subtitle.StatusRunning, subtitle.StatusRunning
			}
			if saveErr := saveMetadata(metadataPath, metadata); sessionRecording && saveErr != nil {
				return nil, saveErr
			}
			return nil, err
		}

		now := time.Now().UTC()
		metadata.Status = subtitle.StatusCompleted
		metadata.LastError = ""
		metadata.RenderPreset = subtitle.ResolveRenderPreset(response.RenderPreset, preset, cfg.Subtitle.BurnStyle)
		metadata.ActualProvider = response.ActualProvider
		metadata.ActualModel = response.ActualModel
		metadata.ActualBurnProvider = response.ActualBurnProvider
		metadata.RendererStatus = subtitle.StatusCompleted
		metadata.RendererError = ""
		if response.ASSPath != "" {
			if sessionRecording {
				if response.ASSPath != strings.TrimSuffix(workerSRTPath, ".srt")+".ass" {
					return nil, errors.New("worker returned an unexpected ASS path")
				}
			} else {
				metadata.ASSPath = response.ASSPath
			}
		}
		metadata.Segments = response.Segments
		metadata.CompletedAt = &now
		metadata.SourceExists = fileExists(file.Path)
		if sessionRecording {
			staged, err := recordingAttemptMetadata(metadata)
			if err != nil {
				return nil, err
			}
			if err := subtitle.SaveMetadata(strings.TrimSuffix(workerVideoPath, ".mp4")+".subtitle.json", staged); err != nil {
				return nil, err
			}
		}
		if err := saveMetadata(metadataPath, metadata); err != nil {
			return nil, err
		}

		if sessionRecording {
			if err := promoteRecordingAttempt(metadata); err != nil {
				return nil, err
			}
			sessionSources = append(sessionSources, pipeline.SessionMediaSource{InputPath: file.Path, LibraryPath: libraryPath, MetadataPath: metadataPath})
		} else {
			s.deleteSourceAfterCompletion(cfg.Subtitle, libraryPath, sourceRoot, metadataPath, &metadata)
			if err := s.syncKnowledge(ctx, cfg.Subtitle.KnowledgeSync, libraryRoot, libraryPath, metadataPath, &metadata); err != nil {
				return nil, err
			}
		}

		s.logs += fmt.Sprintf("字幕生成完成: %s\n", subtitle.MediaDisplayTitle(libraryPath))

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

	if sessionRecording {
		return s.publishCompletedSession(ctx, cfg.Subtitle.KnowledgeSync, libraryRoot, sessionSources)
	}
	return output, nil
}

func loadCompletedSubtitleMetadata(metadataPath, libraryPath, srtPath, assPath string) (subtitle.Metadata, bool) {
	metadata, err := subtitle.LoadMetadata(metadataPath)
	if err != nil {
		return subtitle.Metadata{}, false
	}
	if metadata.Status != subtitle.StatusCompleted || len(metadata.Segments) == 0 {
		return subtitle.Metadata{}, false
	}
	if metadata.OutputPath == "" {
		metadata.OutputPath = libraryPath
	}
	if metadata.SRTPath == "" {
		metadata.SRTPath = srtPath
	}
	if metadata.ASSPath == "" {
		metadata.ASSPath = assPath
	}
	if !fileExists(metadata.OutputPath) || !fileExists(metadata.SRTPath) || !fileExists(metadata.ASSPath) {
		return subtitle.Metadata{}, false
	}
	if metadata.SourcePath != "" {
		metadata.SourceExists = fileExists(metadata.SourcePath)
	}
	return metadata, true
}

func (s *SubtitleGenerateStage) deleteSourceAfterCompletion(
	cfg configs.SubtitleConfig,
	libraryPath string,
	sourceRoot string,
	metadataPath string,
	metadata *subtitle.Metadata,
) {
	// P11: 完成后立即删除源文件（节省存储空间）
	// 触发条件：delete_source_on_completion=true + KeepSource=false + 源文件还存在。
	// 删除是非主路径：失败不让 pipeline 失败，retention_days 后台 cleanup 继续兜底。
	if !cfg.DeleteSourceOnCompletion || metadata.KeepSource || !metadata.SourceExists {
		return
	}
	sourcePath := metadata.SourcePath
	if sourcePath == "" {
		sourcePath = libraryPath
	}
	if err := subtitle.DeleteSourceFile(libraryPath, sourceRoot); err != nil {
		if errors.Is(err, subtitle.ErrSourceNotDeletable) {
			logrus.WithError(err).WithFields(logrus.Fields{
				"source":  sourcePath,
				"library": libraryPath,
			}).Info("字幕完成后跳过源文件删除：解析结果是媒体库成品或不在源目录中")
			s.logs += fmt.Sprintf("源文件删除已跳过（保留媒体库成品）: %s\n", filepath.Base(sourcePath))
			return
		}
		logrus.WithError(err).WithField("source", sourcePath).
			Warn("字幕完成后删除源文件失败（不阻塞 pipeline，retention 后台兜底）")
		s.logs += fmt.Sprintf("源文件删除失败（已记入日志）: %s\n", filepath.Base(sourcePath))
		return
	}
	if refreshed, err := subtitle.LoadMetadata(metadataPath); err == nil {
		*metadata = refreshed
	}
	s.logs += fmt.Sprintf("源文件已删除（节省存储）: %s\n", filepath.Base(sourcePath))
}

func (s *SubtitleGenerateStage) GetCommands() []string {
	return s.commands
}

func (s *SubtitleGenerateStage) GetLogs() string {
	return s.logs
}

func (s *SubtitleGenerateStage) shouldSkipLibraryPublish(ctx *pipeline.PipelineContext, cfg configs.SubtitleConfig, filePath, libraryRoot string) (bool, float64, time.Duration) {
	minDuration := cfg.GetMinLibraryVideoDuration()
	if minDuration <= 0 {
		return false, 0, minDuration
	}
	if ctx != nil && strings.TrimSpace(ctx.RecordInfo.LiveSessionID) != "" {
		return false, 0, minDuration
	}
	durationSeconds, err := s.probeVideoDuration(ctx, filePath)
	if err != nil {
		logrus.WithError(err).WithField("source", filePath).Warn("subtitle_generate: cannot evaluate video duration for library publish skip")
		return false, 0, minDuration
	}
	return durationSeconds > 0 && durationSeconds <= minDuration.Seconds(), durationSeconds, minDuration
}

func (s *SubtitleGenerateStage) cleanupShortLibraryLink(sourcePath, libraryRoot string) {
	libraryPath, err := subtitle.ResolveLibraryVideoPath(sourcePath, libraryRoot)
	if err != nil {
		return
	}
	if libraryPath == sourcePath {
		return
	}
	sourceInfo, sourceErr := os.Stat(sourcePath)
	libraryInfo, libraryErr := os.Stat(libraryPath)
	if sourceErr != nil || libraryErr != nil || !os.SameFile(sourceInfo, libraryInfo) {
		return
	}
	if err := os.Remove(libraryPath); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"source":  sourcePath,
			"library": libraryPath,
		}).Warn("subtitle_generate: failed to remove short video library hardlink")
		return
	}
	logrus.WithFields(logrus.Fields{
		"source":  sourcePath,
		"library": libraryPath,
	}).Info("subtitle_generate: removed short video library hardlink")
}

func (s *SubtitleGenerateStage) probeVideoDuration(ctx *pipeline.PipelineContext, inputFile string) (float64, error) {
	goCtx := context.Background()
	ffmpegPath := ""
	if ctx != nil {
		if ctx.Ctx != nil {
			goCtx = ctx.Ctx
		}
		ffmpegPath = ctx.FFmpegPath
	}
	if ffmpegPath == "" {
		var err error
		ffmpegPath, err = utils.GetFFmpegPath(goCtx)
		if err != nil {
			return 0, err
		}
	}
	cmd := exec.CommandContext(goCtx, ffmpegPath, "-i", inputFile, "-hide_banner")
	output, err := cmd.CombinedOutput()
	if err != nil && len(output) == 0 {
		return 0, err
	}
	return parseFFmpegDurationSeconds(string(output))
}

func parseFFmpegDurationSeconds(output string) (float64, error) {
	re := regexp.MustCompile(`Duration:\s*(\d{2}):(\d{2}):(\d{2})\.(\d{2})`)
	matches := re.FindStringSubmatch(output)
	if len(matches) < 5 {
		return 0, fmt.Errorf("duration not found")
	}
	hours, _ := strconv.Atoi(matches[1])
	minutes, _ := strconv.Atoi(matches[2])
	seconds, _ := strconv.Atoi(matches[3])
	centiseconds, _ := strconv.Atoi(matches[4])
	return float64(hours*3600+minutes*60+seconds) + float64(centiseconds)/100, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func subtitleRetryLaterDelay() time.Duration {
	raw := strings.TrimSpace(os.Getenv("SUBTITLE_RETRY_LATER_DELAY"))
	if raw == "" {
		return pipeline.DefaultRetryLaterDelay
	}
	delay, err := time.ParseDuration(raw)
	if err != nil || delay <= 0 {
		return pipeline.DefaultRetryLaterDelay
	}
	return delay
}
