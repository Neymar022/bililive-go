package stages

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
)

func readableCheckpointFile(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if allowMissing && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("checkpoint must be a nonempty regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func validateCompletedCheckpoint(metadata subtitle.Metadata, path string) error {
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	if metadata.Status != subtitle.StatusCompleted || len(metadata.Segments) == 0 || metadata.OutputPath != path || metadata.SRTPath != stem+".srt" || metadata.ASSPath != stem+".ass" {
		return fmt.Errorf("completed checkpoint references do not match expected output: %s", path)
	}
	for _, file := range []string{path, metadata.SRTPath, metadata.ASSPath} {
		if err := readableCheckpointFile(file, false); err != nil {
			return err
		}
	}
	return nil
}

func loadRecordingCheckpoint(ctx *pipeline.PipelineContext, source, path string) (subtitle.Metadata, bool, error) {
	if ctx.WithCompletionCommit == nil {
		return loadRecordingCheckpointUnlocked(ctx, source, path)
	}
	var metadata subtitle.Metadata
	var complete bool
	attempt := ""
	if previous, err := subtitle.LoadMetadata(strings.TrimSuffix(path, ".mp4") + ".subtitle.json"); err == nil && previous.RecordMeta["recording_attempt_path"] != nil {
		p, e := validatedRecordingAttemptPath(previous)
		if e != nil {
			return previous, false, e
		}
		attempt = filepath.Dir(p)
	}
	err := ctx.WithCompletionCommit(nil, attempt, func() error {
		var err error
		metadata, complete, err = loadRecordingCheckpointUnlocked(ctx, source, path)
		return err
	})
	return metadata, complete, err
}
func loadRecordingCheckpointUnlocked(ctx *pipeline.PipelineContext, source, path string) (subtitle.Metadata, bool, error) {
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	metadataPath := stem + ".subtitle.json"
	_, err := os.Lstat(metadataPath)
	if os.IsNotExist(err) {
		for _, file := range []string{path, stem + ".srt", stem + ".ass", stem + ".nfo", stem + ".jpg"} {
			if _, err := os.Lstat(file); !os.IsNotExist(err) {
				return subtitle.Metadata{}, false, fmt.Errorf("unowned work artifact requires recovery: %s", file)
			}
		}
		return subtitle.Metadata{}, false, nil
	}
	if err := readableCheckpointFile(metadataPath, false); err != nil {
		return subtitle.Metadata{}, false, err
	}
	metadata, err := subtitle.LoadMetadata(metadataPath)
	if err != nil {
		return metadata, false, err
	}
	if metadata.SourcePath != source || metadata.OutputPath != path || metadata.SRTPath != stem+".srt" || metadata.ASSPath != stem+".ass" || metadata.RecordMeta["live_session_id"] != ctx.RecordInfo.LiveSessionID || metadata.RecordMeta["recording_producer_id"] != ctx.RecordInfo.RecordingProducerID || metadata.RecordMeta["pipeline_task_id"] != strconv.FormatInt(ctx.TaskID, 10) {
		return metadata, false, fmt.Errorf("work checkpoint ownership mismatch: %s", path)
	}
	if metadata.Status != subtitle.StatusCompleted && metadata.RecordMeta["recording_attempt_path"] != nil {
		confirmed, exists, err := confirmedRecordingAttempt(metadata)
		if err != nil {
			return metadata, false, err
		}
		if exists {
			write, err := recordingMetadataWriter(metadataPath, metadata)
			if err != nil {
				return metadata, false, err
			}
			if err := write(metadataPath, confirmed); err != nil {
				return metadata, false, err
			}
			metadata = confirmed
		}
	}
	if metadata.Status == subtitle.StatusCompleted {
		err := validateCompletedCheckpoint(metadata, path)
		if metadata.RecordMeta["recording_attempt_path"] != nil {
			err = promoteRecordingAttempt(metadata)
		}
		return metadata, err == nil, err
	}
	if metadata.Status != subtitle.StatusFailed && metadata.Status != subtitle.StatusQueued {
		return metadata, false, fmt.Errorf("worker completion is unconfirmed; checkpoint recovery required: %s", path)
	}
	if attempt, exists := metadata.RecordMeta["recording_attempt_path"]; exists {
		path, err := validatedRecordingAttemptPath(metadata)
		if err != nil {
			return metadata, false, err
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return metadata, false, fmt.Errorf("worker left a video without confirmed completion; recovery required: %v", attempt)
		}
	}
	// 已存在视频但未提交完成状态时，不能猜测需要再次烧录。
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return metadata, false, fmt.Errorf("unfinished video checkpoint requires recovery: %s", path)
	}
	for _, file := range []string{metadata.SRTPath, metadata.ASSPath} {
		if err := readableCheckpointFile(file, true); err != nil {
			return metadata, false, err
		}
	}
	return metadata, false, nil
}

func confirmedRecordingAttempt(expected subtitle.Metadata) (subtitle.Metadata, bool, error) {
	path, err := validatedRecordingAttemptPath(expected)
	if err != nil {
		return expected, false, err
	}
	responsePath := strings.TrimSuffix(path, ".mp4") + ".subtitle.json"
	if _, err := os.Lstat(responsePath); os.IsNotExist(err) {
		return expected, false, nil
	}
	if err := readableCheckpointFile(responsePath, false); err != nil {
		return expected, false, err
	}
	confirmed, err := subtitle.LoadMetadata(responsePath)
	if err != nil {
		return expected, false, err
	}
	if confirmed.SourcePath != expected.SourcePath || !reflect.DeepEqual(confirmed.RecordMeta, expected.RecordMeta) {
		return expected, false, fmt.Errorf("completed attempt ownership mismatch: %s", responsePath)
	}
	if err := validateCompletedCheckpoint(confirmed, path); err != nil {
		return expected, false, err
	}
	confirmed.OutputPath, confirmed.SRTPath, confirmed.ASSPath = expected.OutputPath, expected.SRTPath, expected.ASSPath
	return confirmed, true, nil
}

func recordingMetadataWriter(path string, expected subtitle.Metadata) (func(string, subtitle.Metadata) error, error) {
	var revision *liveSessionFileRevision
	if expected.Status != "" {
		current, err := subtitle.LoadMetadata(path)
		if err != nil || !reflect.DeepEqual(current, expected) {
			return nil, fmt.Errorf("recording checkpoint changed before writing: %s", path)
		}
		value, err := captureLiveSessionFileRevision(path, true)
		if err != nil {
			return nil, err
		}
		revision = &value
	}
	return func(target string, metadata subtitle.Metadata) error {
		if target != path {
			return fmt.Errorf("unexpected recording metadata target: %s", target)
		}
		file, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*.json")
		if err != nil {
			return err
		}
		staged := file.Name()
		_ = file.Close()
		defer os.Remove(staged)
		if err := subtitle.SaveMetadata(staged, metadata); err != nil {
			return err
		}
		file, err = os.Open(staged)
		if err != nil {
			return err
		}
		err = file.Sync()
		_ = file.Close()
		if err != nil {
			return err
		}
		if revision == nil {
			err = os.Link(staged, path)
		} else {
			if err := liveSessionFileUnchanged(path, *revision); err != nil {
				return err
			}
			err = os.Rename(staged, path)
		}
		if err != nil {
			return err
		}
		value, err := captureLiveSessionFileRevision(path, true)
		if err != nil {
			return err
		}
		revision = &value
		return syncLiveSessionDirectory(filepath.Dir(path))
	}, nil
}

func validatedRecordingAttemptPath(metadata subtitle.Metadata) (string, error) {
	path, ok := metadata.RecordMeta["recording_attempt_path"].(string)
	dir := filepath.Dir(path)
	if !ok || path == "" || filepath.Base(path) != filepath.Base(metadata.OutputPath) || filepath.Dir(dir) != filepath.Dir(metadata.OutputPath) || !strings.HasPrefix(filepath.Base(dir), ".attempt-") {
		return "", fmt.Errorf("unverified recording attempt path: %s", path)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("unverified recording attempt directory: %s", dir)
	}
	return path, nil
}

func recordingAttemptMetadata(metadata subtitle.Metadata) (subtitle.Metadata, error) {
	path, err := validatedRecordingAttemptPath(metadata)
	if err != nil {
		return metadata, err
	}
	metadata.OutputPath = path
	metadata.SRTPath = strings.TrimSuffix(path, ".mp4") + ".srt"
	metadata.ASSPath = strings.TrimSuffix(path, ".mp4") + ".ass"
	return metadata, validateCompletedCheckpoint(metadata, path)
}

func promoteRecordingAttempt(metadata subtitle.Metadata) error {
	staged, err := recordingAttemptMetadata(metadata)
	if err != nil {
		return err
	}
	// 保留暂存硬链接供崩溃后继续提交；绝不覆盖 worker 运行期间出现的目标。
	for _, pair := range [][2]string{{staged.SRTPath, metadata.SRTPath}, {staged.ASSPath, metadata.ASSPath}, {staged.OutputPath, metadata.OutputPath}} {
		if err := os.Link(pair[0], pair[1]); err != nil {
			source, sourceErr := os.Lstat(pair[0])
			target, targetErr := os.Lstat(pair[1])
			if sourceErr != nil || targetErr != nil || !os.SameFile(source, target) {
				return fmt.Errorf("recording output conflict; completed attempt retained: %s: %w", pair[1], err)
			}
		}
	}
	if err := syncLiveSessionDirectory(filepath.Dir(metadata.OutputPath)); err != nil {
		return err
	}
	return validateCompletedCheckpoint(metadata, metadata.OutputPath)
}

// acceptSavedCompletion 只能在 work→task→attempt→DB 守卫内写文件，正常 HTTP 和后台复用。
func (s *SubtitleGenerateStage) acceptSavedCompletion(ctx *pipeline.PipelineContext, ref pipeline.CompletionRef) error {
	if ctx.WithCompletionCommit == nil {
		return fmt.Errorf("completion commit guard unavailable")
	}
	return ctx.WithCompletionCommit(&ref, ref.AttemptPath, func() error {
		cfg := configs.GetCurrentConfig()
		if cfg == nil {
			return fmt.Errorf("completion config unavailable")
		}
		style := cfg.Subtitle.BurnStyle
		style.Preset = subtitle.ResolveRenderPreset(s.config.GetStringOption("preset", ""), "", style)
		snapshot := map[string]any{"provider": s.config.GetStringOption("provider", cfg.Subtitle.DefaultProvider), "language": s.config.GetStringOption("language", cfg.Subtitle.Language), "burn_style": style}
		if !subtitle.CompletionJSONEqual(snapshot, ref.RequestSnapshot) {
			return fmt.Errorf("completion request config changed")
		}
		receipt, err := subtitle.ReadWorkerCompletion(ref.Request)
		if err != nil {
			return err
		}
		path := strings.TrimSuffix(ref.FormalPath, ".mp4") + ".subtitle.json"
		metadata, err := subtitle.LoadMetadata(path)
		missing := os.IsNotExist(err)
		if os.IsNotExist(err) {
			metadata = subtitle.Metadata{SourcePath: ref.Source, OutputPath: ref.FormalPath, SRTPath: strings.TrimSuffix(ref.FormalPath, ".mp4") + ".srt", ASSPath: strings.TrimSuffix(ref.FormalPath, ".mp4") + ".ass", RecordMeta: ref.Request.RecordMeta}
			err = nil
		}
		if err != nil {
			return err
		}
		if metadata.SourcePath != ref.Source || metadata.OutputPath != ref.FormalPath || !subtitle.CompletionJSONEqual(metadata.RecordMeta, ref.Request.RecordMeta) {
			return fmt.Errorf("completion checkpoint owner changed")
		}
		if metadata.Status == subtitle.StatusCompleted {
			if !subtitle.CompletionJSONEqual(metadata.Segments, receipt.Response.Segments) || metadata.ActualProvider != receipt.Response.ActualProvider || metadata.ActualModel != receipt.Response.ActualModel || metadata.ActualBurnProvider != receipt.Response.ActualBurnProvider || metadata.RenderPreset != receipt.Response.RenderPreset || metadata.CompletedAt == nil || !metadata.CompletedAt.Equal(receipt.CompletedAt) {
				return fmt.Errorf("completion completed checkpoint differs from receipt")
			}
			return promoteRecordingAttempt(metadata)
		}
		expected := metadata
		if missing {
			expected = subtitle.Metadata{}
		}
		writer, err := recordingMetadataWriter(path, expected)
		if err != nil {
			return err
		}
		response := receipt.Response
		metadata.Status = subtitle.StatusCompleted
		metadata.RendererStatus = subtitle.StatusCompleted
		metadata.LastError = ""
		metadata.RendererError = ""
		metadata.SourceExists = true
		metadata.Provider, _ = ref.Request.RequestSnapshot["provider"].(string)
		metadata.Language, _ = ref.Request.RequestSnapshot["language"].(string)
		metadata.RenderPreset = response.RenderPreset
		metadata.ActualProvider = response.ActualProvider
		metadata.ActualModel = response.ActualModel
		metadata.ActualBurnProvider = response.ActualBurnProvider
		metadata.Segments = response.Segments
		metadata.CompletedAt = &receipt.CompletedAt
		metadata.UpdatedAt = receipt.CompletedAt
		if metadata.CreatedAt.IsZero() {
			metadata.CreatedAt = receipt.CompletedAt
		}
		staged, err := recordingAttemptMetadata(metadata)
		if err != nil {
			return err
		}
		if err = subtitle.DurableCompletionJSON(strings.TrimSuffix(ref.Request.OutputVideoPath, ".mp4")+".subtitle.json", staged); err != nil {
			return err
		}
		if err = writer(path, metadata); err != nil {
			return err
		}
		return promoteRecordingAttempt(metadata)
	})
}

func (s *SubtitleGenerateStage) ReconcileSavedCompletion(ctx *pipeline.PipelineContext, input []pipeline.FileInfo) (pipeline.CompletionReconcileResult, error) {
	result := pipeline.CompletionReconcileResult{AllInputsComplete: true}
	if ctx.SavedCompletions == nil {
		return result, fmt.Errorf("completion refs unavailable")
	}
	refs, err := ctx.SavedCompletions()
	if err != nil {
		return result, err
	}
	var first error
	for index, file := range input {
		if file.Type != pipeline.FileTypeVideo {
			continue
		}
		if !strings.EqualFold(filepath.Ext(file.Path), ".mp4") {
			result.AllInputsComplete = false
			result.BlockingInputs = append(result.BlockingInputs, index)
			if first == nil {
				first = fmt.Errorf("completion input requires mp4")
			}
			continue
		}
		var ref *pipeline.CompletionRef
		for i := range refs {
			if refs[i].InputIndex == index && refs[i].State != "superseded" {
				ref = &refs[i]
				break
			}
		}
		if ref == nil {
			cfg := configs.GetCurrentConfig()
			if cfg == nil {
				return result, fmt.Errorf("completion config unavailable")
			}
			path, e := subtitle.ExistingRecordingWorkPath(cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath), ctx.RecordInfo.LiveSessionID, ctx.TaskID, index, file.Path, ctx.RecordInfo.HostName, ctx.RecordInfo.StartTime)
			if e == nil {
				release, locked := subtitle.TryLockRecordingWork(path)
				if locked {
					metadata, complete, loadErr := loadRecordingCheckpoint(ctx, file.Path, path)
					release()
					if loadErr == nil && complete {
						result.Sources = append(result.Sources, pipeline.SessionMediaSource{InputPath: file.Path, LibraryPath: path, MetadataPath: strings.TrimSuffix(metadata.OutputPath, ".mp4") + ".subtitle.json"})
						continue
					}
				}
			}
			result.AllInputsComplete = false
			result.BlockingInputs = append(result.BlockingInputs, index)
			continue
		}
		if ref.State == "queued_unsent" {
			result.AllInputsComplete = false
			result.BlockingInputs = append(result.BlockingInputs, index)
			continue
		}
		cfg := configs.GetCurrentConfig()
		if cfg == nil {
			return result, fmt.Errorf("completion config unavailable")
		}
		style := cfg.Subtitle.BurnStyle
		style.Preset = subtitle.ResolveRenderPreset(s.config.GetStringOption("preset", ""), "", style)
		snapshot := map[string]any{"provider": s.config.GetStringOption("provider", cfg.Subtitle.DefaultProvider), "language": s.config.GetStringOption("language", cfg.Subtitle.Language), "burn_style": style}
		if !subtitle.CompletionJSONEqual(snapshot, ref.RequestSnapshot) {
			result.AllInputsComplete = false
			result.BlockingInputs = append(result.BlockingInputs, index)
			if first == nil {
				first = fmt.Errorf("completion request config changed")
			}
			continue
		}
		release, locked := subtitle.TryLockRecordingWork(ref.FormalPath)
		if !locked {
			result.AllInputsComplete = false
			result.BlockingInputs = append(result.BlockingInputs, index)
			continue
		}
		err = s.acceptSavedCompletion(ctx, *ref)
		release()
		if err != nil {
			if first == nil {
				first = err
			}
			result.AllInputsComplete = false
			result.BlockingInputs = append(result.BlockingInputs, index)
			continue
		}
		result.Sources = append(result.Sources, pipeline.SessionMediaSource{InputPath: file.Path, LibraryPath: ref.FormalPath, MetadataPath: strings.TrimSuffix(ref.FormalPath, ".mp4") + ".subtitle.json"})
	}
	return result, first
}

// HasCompletedInputs 只读取已完成前像，不写 metadata、不 promotion、不访问 worker。
func (s *SubtitleGenerateStage) HasCompletedInputs(ctx *pipeline.PipelineContext, input []pipeline.FileInfo) bool {
	cfg := configs.GetCurrentConfig()
	if cfg == nil || ctx.RecordInfo.LiveSessionID == "" {
		return false
	}
	found := false
	for index, file := range input {
		if file.Type != pipeline.FileTypeVideo {
			continue
		}
		found = true
		path, err := subtitle.ExistingRecordingWorkPath(cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath), ctx.RecordInfo.LiveSessionID, ctx.TaskID, index, file.Path, ctx.RecordInfo.HostName, ctx.RecordInfo.StartTime)
		if err != nil {
			return false
		}
		metadata, err := subtitle.LoadMetadata(strings.TrimSuffix(path, ".mp4") + ".subtitle.json")
		if err != nil || metadata.SourcePath != file.Path || metadata.RecordMeta["live_session_id"] != ctx.RecordInfo.LiveSessionID || metadata.RecordMeta["recording_producer_id"] != ctx.RecordInfo.RecordingProducerID || metadata.RecordMeta["pipeline_task_id"] != strconv.FormatInt(ctx.TaskID, 10) || validateCompletedCheckpoint(metadata, path) != nil {
			return false
		}
	}
	return found
}
