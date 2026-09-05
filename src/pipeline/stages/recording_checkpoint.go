package stages

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

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
