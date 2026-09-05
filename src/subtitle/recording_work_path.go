package subtitle

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var recordingWorkLocks keyedPathLocks

// LockRecordingWork 防止同一任务的重复执行并发改写处理检查点。
func LockRecordingWork(path string) func() {
	return lockLibraryPath(&recordingWorkLocks, path)
}

// RecordingWorkPath 只准备库外工作路径，不创建可被媒体库扫描的占位视频或集号。
func RecordingWorkPath(libraryRoot, sessionID string, taskID int64, inputIndex int, source, host string, recordedAt time.Time) (string, error) {
	if sessionID == "" || taskID <= 0 || recordedAt.IsZero() {
		return "", fmt.Errorf("recording work path requires registered session and recording time")
	}
	meta := parseSourceFilename(source, host, recordedAt)
	identity := episodeIdentityForRecordedAt(meta.recordedAt)
	if identity <= 0 {
		return "", fmt.Errorf("invalid recording time: %s", source)
	}
	sessionHash := sha256.Sum256([]byte(sessionID))
	dir, err := recordingWorkDirectory(libraryRoot, "processing", fmt.Sprintf("%x", sessionHash[:16]), fmt.Sprintf("%d-%d", taskID, inputIndex), meta.aliasName, "Season 01")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, buildEpisodeFilename(meta.aliasName, identity, meta.recordedAt.In(mediaLibraryLocation), meta.title, ".mp4")), nil
}

// RecordingCapturePath 将新录制及其转换分段隔离于旧 organizer 的扫描源目录。
func RecordingCapturePath(libraryRoot, sessionID, producerID, name string) (string, error) {
	name = filepath.Clean(name)
	if sessionID == "" || producerID == "" || filepath.IsAbs(name) || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("recording capture requires an owned relative filename")
	}
	sessionHash, producerHash := sha256.Sum256([]byte(sessionID)), sha256.Sum256([]byte(producerID))
	dir, err := recordingWorkDirectory(libraryRoot, "recordings", fmt.Sprintf("%x", sessionHash[:16]), fmt.Sprintf("%x", producerHash[:16]))
	if err != nil {
		return "", err
	}
	dir, err = os.MkdirTemp(dir, "capture-*")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func recordingWorkDirectory(libraryRoot string, components ...string) (string, error) {
	root, err := filepath.Abs(libraryRoot)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if filepath.Dir(root) == root {
		return "", fmt.Errorf("media library cannot be filesystem root")
	}
	dir := filepath.Dir(root)
	for _, component := range append([]string{".live_session_segments"}, components...) {
		dir = filepath.Join(dir, component)
		if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
			return "", err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("recording work directory must not be a symlink: %s", dir)
		}
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", fmt.Errorf("recording work path resolves inside media library")
	}
	return dir, nil
}
