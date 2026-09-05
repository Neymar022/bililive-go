package recorders

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/pkg/utils"
)

// 必须在入队前判定，避免转换任务先删除原始输入；parser 报错也可能留下可播放视频。
func (r *recorder) recordCaptureEvidence(ctx context.Context, fileName string) {
	if r.pipelineManager == nil || r.origin.ProducerID == "" {
		return
	}
	_, err := r.pipelineManager.RecordingRun(string(r.Live.GetLiveId()))
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		r.registrationError = "recording run evidence unavailable: " + err.Error()
		return
	}
	paths := append([]string{fileName}, findBililiveRecorderOutputFiles(fileName)...)
	seen := make(map[string]bool)
	playable := false
	failure := ""
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			failure = err.Error()
			continue
		}
		ffmpeg, err := utils.GetFFmpegPathForLive(ctx, r.Live)
		if err != nil {
			failure = err.Error()
			continue
		}
		// 停止录制会取消 parser context，但最终文件仍须完成有界判定。
		valid, err := VerifyRecordedVideo(context.WithoutCancel(ctx), ffmpeg, path)
		if valid {
			playable = true
			break
		}
		if err != nil {
			failure = err.Error()
		}
	}
	if err := r.pipelineManager.RecordCaptureEvidence(r.origin, playable, failure); err != nil {
		r.registrationError = "capture evidence persistence failed: " + err.Error()
	} else if failure != "" && !playable {
		r.getLogger().WithField("reason", failure).Warn("录制视频有效性未确定，单次录制不能自动进入下一场")
	}
}

// VerifyRecordedVideo 复用 FFmpeg 探测视频流并解码一帧，不写文件或要求最低时长。
func VerifyRecordedVideo(ctx context.Context, ffmpegPath, inputPath string) (bool, error) {
	path, err := filepath.Abs(inputPath)
	if err != nil {
		return false, err
	}
	entry, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !entry.Mode().IsRegular() {
		return false, fmt.Errorf("recording is not a regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	before, err := file.Stat()
	_ = file.Close()
	if err != nil {
		return false, err
	}
	if !before.Mode().IsRegular() {
		return false, fmt.Errorf("recording is not a regular file: %s", path)
	}
	if before.Size() == 0 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, "-v", "error", "-nostdin", "-protocol_whitelist", "file,pipe", "-i", path,
		"-map", "0:v:0", "-frames:v", "1", "-an", "-sn", "-threads", "1", "-f", "framecrc", "pipe:1")
	var output, diagnostics bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &diagnostics
	decodeErr := cmd.Run()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	after, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return false, fmt.Errorf("recording changed during verification: %s", path)
	}
	if decodeErr != nil {
		var exit *exec.ExitError
		// FFmpeg 无结构化错误码区分空视频流；只接受经过真实音频文件验证的明确诊断。
		if errors.As(decodeErr, &exit) && exit.ExitCode() >= 0 && strings.HasPrefix(diagnostics.String(), "Stream map '0:v:0' matches no streams.\n") {
			return false, nil
		}
		return false, fmt.Errorf("recording decode verification failed: %w: %s", decodeErr, diagnostics.String())
	}
	frames := csv.NewReader(&output)
	frames.Comment = '#'
	frames.TrimLeadingSpace = true
	frames.FieldsPerRecord = 6
	frame, err := frames.Read()
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("invalid decoded frame evidence: %w", err)
	}
	size, err := strconv.ParseInt(frame[4], 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid decoded frame size: %w", err)
	}
	return size > 0, nil
}
