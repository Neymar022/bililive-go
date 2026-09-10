package subtitle

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecordingSource 只授权清理已公开场次的专属原片，不包括已烧分段。
type RecordingSource struct {
	Path         string `json:"path"`
	ProducerID   string `json:"producer_id"`
	Size         int64  `json:"size"`
	ModifiedAt   int64  `json:"modified_at"`
	MetadataPath string `json:"metadata_path"`
}

func recordingSourcePath(libraryRoot, sessionID, producerID, path string) error {
	if sessionID == "" || producerID == "" || filepath.Ext(path) != ".mp4" {
		return ErrSourceNotDeletable
	}
	libraryRoot, err := filepath.EvalSymlinks(libraryRoot)
	if err != nil {
		return err
	}
	sessionHash, producerHash := sha256.Sum256([]byte(sessionID)), sha256.Sum256([]byte(producerID))
	root := filepath.Join(filepath.Dir(libraryRoot), ".live_session_segments", "recordings", fmt.Sprintf("%x", sessionHash[:16]), fmt.Sprintf("%x", producerHash[:16]))
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return ErrSourceNotDeletable
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "capture-") || parts[0] == "capture-" {
		return ErrSourceNotDeletable
	}
	// 拒绝任何目录链接；源已清理时仍需验证其父路径。
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || parent != filepath.Clean(filepath.Dir(path)) {
		return ErrSourceNotDeletable
	}
	return nil
}

// CaptureRecordingSource 在公开前固定原片大小和修改时间；旧路径不扩大授权。
func CaptureRecordingSource(libraryRoot, sessionID string, metadata Metadata) (RecordingSource, error) {
	producer, _ := metadata.RecordMeta["recording_producer_id"].(string)
	if err := recordingSourcePath(libraryRoot, sessionID, producer, metadata.SourcePath); err != nil {
		return RecordingSource{}, err
	}
	info, err := os.Lstat(metadata.SourcePath)
	if err != nil {
		return RecordingSource{}, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return RecordingSource{}, ErrSourceNotDeletable
	}
	return RecordingSource{Path: metadata.SourcePath, ProducerID: producer, Size: info.Size(), ModifiedAt: info.ModTime().UnixNano(), MetadataPath: sidecarPathForVideo(metadata.OutputPath)}, nil
}

// DeletePublishedRecordingSources 与 retention 共用，先全量校验再删除，不重建历史清单。
func DeletePublishedRecordingSources(videoPath string) error {
	unlock := LockLibraryPublication(videoPath)
	defer unlock()
	defer InvalidateRecordCache()
	info, err := os.Lstat(videoPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrSourceNotDeletable
	}
	videoPath, err = filepath.EvalSymlinks(videoPath)
	if err != nil {
		return err
	}
	metadataPath := sidecarPathForVideo(videoPath)
	metadata, err := LoadMetadata(metadataPath)
	if err != nil {
		return err
	}
	if metadata.KeepSource || len(metadata.RecordingSources) == 0 {
		return ErrSourceNotDeletable
	}
	if !metadata.SourceExists && metadata.SourceDeletedAt != nil {
		for _, source := range metadata.RecordingSources {
			if _, err := os.Lstat(source.Path); !os.IsNotExist(err) {
				return ErrSourceNotDeletable
			}
		}
		return nil
	}
	if metadata.Status != StatusCompleted || metadata.CompletedAt == nil || metadata.OutputPath != videoPath || metadata.SRTPath != strings.TrimSuffix(videoPath, ".mp4")+".srt" || metadata.ASSPath != strings.TrimSuffix(videoPath, ".mp4")+".ass" || metadata.RecordMeta["live_session_media_role"] != "aggregate" {
		return ErrSourceNotDeletable
	}
	if err := ValidatePublishedLibraryEpisode(videoPath); err != nil {
		return err
	}
	var products []os.FileInfo
	for _, path := range []string{videoPath, metadata.SRTPath, metadata.ASSPath} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return ErrSourceNotDeletable
		}
		products = append(products, info)
	}
	libraryRoot := filepath.Dir(filepath.Dir(filepath.Dir(videoPath)))
	sessionID, _ := metadata.RecordMeta["live_session_id"].(string)
	files := make(map[string]os.FileInfo)
	checkpoints := make(map[string]Metadata)
	for _, source := range metadata.RecordingSources {
		if err := recordingSourcePath(libraryRoot, sessionID, source.ProducerID, source.Path); err != nil {
			return err
		}
		workRoot := filepath.Join(filepath.Dir(libraryRoot), ".live_session_segments", "processing")
		inside, err := pathWithinRoot(source.MetadataPath, workRoot)
		if err != nil || !inside {
			return ErrSourceNotDeletable
		}
		resolved, err := filepath.EvalSymlinks(source.MetadataPath)
		if err != nil || resolved != filepath.Clean(source.MetadataPath) {
			return ErrSourceNotDeletable
		}
		checkpoint, err := LoadMetadata(source.MetadataPath)
		if err != nil {
			return err
		}
		if checkpoint.Status != StatusCompleted || checkpoint.KeepSource || checkpoint.SourcePath != source.Path || checkpoint.RecordMeta["live_session_id"] != sessionID || checkpoint.RecordMeta["recording_producer_id"] != source.ProducerID {
			return ErrSourceNotDeletable
		}
		for _, path := range []string{checkpoint.OutputPath, checkpoint.SRTPath, checkpoint.ASSPath} {
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				return ErrSourceNotDeletable
			}
		}
		checkpoint.SourcePublicationPath = videoPath
		checkpoints[source.MetadataPath] = checkpoint
		info, err := os.Lstat(source.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != source.Size || info.ModTime().UnixNano() != source.ModifiedAt {
			return fmt.Errorf("recording source changed; retained: %s", source.Path)
		}
		for _, product := range products {
			if os.SameFile(info, product) {
				return ErrSourceNotDeletable
			}
		}
		files[source.Path] = info
	}
	// 先持久化成品关联，保证删源后其他同场失败任务仍可验证检查点并续跑。
	for path, checkpoint := range checkpoints {
		if err := saveRecordingSourceMetadata(path, checkpoint); err != nil {
			return err
		}
	}
	for path, expected := range files {
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(current, expected) || current.Size() != expected.Size() || !current.ModTime().Equal(expected.ModTime()) {
			return fmt.Errorf("recording source changed before cleanup: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := syncRecordingSourceDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	metadata.SourceExists = false
	now := time.Now().UTC()
	metadata.SourceDeletedAt = &now
	return saveRecordingSourceMetadata(metadataPath, metadata)
}

// ValidateRecordingSourcePublication 只确认新版清理清单中的原片已被该成品覆盖。
func ValidateRecordingSourcePublication(libraryRoot, videoPath, sourcePath, sessionID, producerID string) error {
	libraryRoot, err := filepath.EvalSymlinks(libraryRoot)
	if err != nil {
		return err
	}
	inside, err := pathWithinRoot(videoPath, libraryRoot)
	if err != nil || !inside {
		return ErrSourceNotDeletable
	}
	if err := ValidatePublishedLibraryEpisode(videoPath); err != nil {
		return err
	}
	meta, err := LoadMetadata(sidecarPathForVideo(videoPath))
	if err != nil {
		return err
	}
	stem := strings.TrimSuffix(videoPath, ".mp4")
	if meta.Status != StatusCompleted || meta.CompletedAt == nil || meta.OutputPath != videoPath || meta.SRTPath != stem+".srt" || meta.ASSPath != stem+".ass" || meta.RecordMeta["live_session_id"] != sessionID || meta.RecordMeta["live_session_media_role"] != "aggregate" {
		return ErrSourceNotDeletable
	}
	for _, path := range []string{videoPath, meta.SRTPath, meta.ASSPath} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return ErrSourceNotDeletable
		}
	}
	for _, source := range meta.RecordingSources {
		if source.Path == sourcePath && source.ProducerID == producerID {
			return recordingSourcePath(libraryRoot, sessionID, producerID, sourcePath)
		}
	}
	return ErrSourceNotDeletable
}

func saveRecordingSourceMetadata(path string, metadata Metadata) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".source-cleanup-*.json")
	if err != nil {
		return err
	}
	staged := file.Name()
	_ = file.Close()
	defer os.Remove(staged)
	if err := SaveMetadata(staged, metadata); err != nil {
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
	if err := os.Rename(staged, path); err != nil {
		return err
	}
	return syncRecordingSourceDirectory(filepath.Dir(path))
}

func syncRecordingSourceDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
