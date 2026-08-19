package subtitle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Record struct {
	RelativePath           string     `json:"relative_path"`
	DisplayTitle           string     `json:"display_title"`
	VideoPath              string     `json:"video_path"`
	ASSPath                string     `json:"ass_path,omitempty"`
	SRTPath                string     `json:"srt_path,omitempty"`
	SourcePath             string     `json:"source_path,omitempty"`
	Status                 Status     `json:"status"`
	Provider               string     `json:"provider,omitempty"`
	RenderPreset           string     `json:"render_preset,omitempty"`
	RendererStatus         Status     `json:"renderer_status,omitempty"`
	RendererError          string     `json:"renderer_error,omitempty"`
	Platform               string     `json:"platform,omitempty"`
	HostName               string     `json:"host_name,omitempty"`
	RoomName               string     `json:"room_name,omitempty"`
	KeepSource             bool       `json:"keep_source"`
	SourceExists           bool       `json:"source_exists"`
	RetentionDeadline      *time.Time `json:"retention_deadline,omitempty"`
	RecordedAt             *time.Time `json:"recorded_at,omitempty"`
	LastError              string     `json:"last_error,omitempty"`
	ActualProvider         string     `json:"actual_provider,omitempty"`
	ActualModel            string     `json:"actual_model,omitempty"`
	ActualBurnProvider     string     `json:"actual_burn_provider,omitempty"`
	Segments               []Segment  `json:"segments,omitempty"`
	KnowledgeSyncStatus    Status     `json:"knowledge_sync_status,omitempty"`
	KnowledgeSyncTaskID    string     `json:"knowledge_sync_task_id,omitempty"`
	KnowledgeSyncSourceID  string     `json:"knowledge_sync_source_id,omitempty"`
	KnowledgeSyncError     string     `json:"knowledge_sync_error,omitempty"`
	KnowledgeSyncAttempts  int        `json:"knowledge_sync_attempts,omitempty"`
	KnowledgeSyncUpdatedAt *time.Time `json:"knowledge_sync_updated_at,omitempty"`
}

var ErrSourceNotDeletable = errors.New("subtitle: resolved source is not a deletable source file")

// ListRecords 返回字幕库中所有 mp4 录制的状态列表。
//
// 实现走 recordCache 进程内索引：缓存命中是 O(N) 内存拷贝；未命中或被
// invalidate 后触发一次 walk 重建。写入路径（pipeline 完成、rerun、删源、改配置）
// 应调用 InvalidateRecordCache 让下次读看到最新状态。
func ListRecords(libraryRoot, sourceRoot string, retentionDays int) ([]Record, error) {
	return globalRecordCache.listFromCacheOrRebuild(libraryRoot, sourceRoot, retentionDays)
}

// walkRecords 是不走缓存的全量磁盘扫描，作为 cache 的填充函数。
// 单独抽出便于测试以及在 cache miss 时复用。
func walkRecords(libraryRoot, sourceRoot string, retentionDays int) ([]Record, error) {
	records := make([]Record, 0)
	err := filepath.WalkDir(libraryRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".mp4" {
			return nil
		}
		record, err := buildRecord(path, libraryRoot, sourceRoot, retentionDays)
		if err != nil {
			return err
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(records, func(i, j int) bool {
		left := records[i].RelativePath
		right := records[j].RelativePath
		return left < right
	})

	return records, nil
}

func GetRecord(libraryRoot, sourceRoot, relativePath string, retentionDays int) (Record, error) {
	videoPath := filepath.Join(libraryRoot, filepath.FromSlash(relativePath))
	return buildRecord(videoPath, libraryRoot, sourceRoot, retentionDays)
}

func SetKeepSource(videoPath string, keep bool) error {
	metadataPath := sidecarPathForVideo(videoPath)
	metadata, err := LoadMetadata(metadataPath)
	if err != nil {
		return err
	}
	metadata.KeepSource = keep
	return SaveMetadata(metadataPath, metadata)
}

func DeleteSourceFile(videoPath, sourceRoot string) error {
	sourcePath, err := ResolveSourcePath(videoPath, sourceRoot)
	if err != nil {
		return err
	}
	if !isDeletableSourcePath(videoPath, sourceRoot, sourcePath) {
		return fmt.Errorf("%w: %s", ErrSourceNotDeletable, sourcePath)
	}
	if err := os.Remove(sourcePath); err != nil && !os.IsNotExist(err) {
		return err
	}

	metadataPath := sidecarPathForVideo(videoPath)
	metadata, err := LoadMetadata(metadataPath)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	metadata.SourceExists = false
	metadata.SourceDeletedAt = &now
	return SaveMetadata(metadataPath, metadata)
}

func CleanupExpiredSources(libraryRoot, sourceRoot string, retentionDays int, now time.Time) (int, error) {
	records, err := ListRecords(libraryRoot, sourceRoot, retentionDays)
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, record := range records {
		metadata, err := LoadMetadata(sidecarPathForVideo(record.VideoPath))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return deleted, err
		}
		if !metadata.ShouldDeleteSource(now, retentionDays) {
			continue
		}
		if err := DeleteSourceFile(record.VideoPath, sourceRoot); err != nil {
			if errors.Is(err, ErrSourceNotDeletable) {
				continue
			}
			return deleted, err
		}
		deleted++
	}

	return deleted, nil
}

func buildRecord(videoPath, libraryRoot, sourceRoot string, retentionDays int) (Record, error) {
	relativePath, err := filepath.Rel(libraryRoot, videoPath)
	if err != nil {
		return Record{}, err
	}

	record := Record{
		RelativePath: filepath.ToSlash(relativePath),
		DisplayTitle: MediaDisplayTitle(videoPath),
		VideoPath:    videoPath,
		Status:       StatusIdle,
	}

	metadataPath := sidecarPathForVideo(videoPath)
	metadata, err := LoadMetadata(metadataPath)
	if err == nil {
		record.Status = metadata.Status
		record.Provider = metadata.Provider
		record.RenderPreset = metadata.RenderPreset
		record.RendererStatus = metadata.RendererStatus
		record.RendererError = metadata.RendererError
		record.ASSPath = metadata.ASSPath
		record.SRTPath = metadata.SRTPath
		record.SourcePath = metadata.SourcePath
		record.KeepSource = metadata.KeepSource
		record.SourceExists = metadata.SourceExists
		record.LastError = metadata.LastError
		record.ActualProvider = metadata.ActualProvider
		record.ActualModel = metadata.ActualModel
		record.ActualBurnProvider = metadata.ActualBurnProvider
		record.Segments = metadata.Segments
		record.KnowledgeSyncStatus = metadata.KnowledgeSyncStatus
		record.KnowledgeSyncTaskID = metadata.KnowledgeSyncTaskID
		record.KnowledgeSyncSourceID = metadata.KnowledgeSyncSourceID
		record.KnowledgeSyncError = metadata.KnowledgeSyncError
		record.KnowledgeSyncAttempts = metadata.KnowledgeSyncAttempts
		record.KnowledgeSyncUpdatedAt = metadata.KnowledgeSyncUpdatedAt

		if platform, ok := metadata.RecordMeta["platform"].(string); ok {
			record.Platform = platform
		}
		if hostName, ok := metadata.RecordMeta["host_name"].(string); ok {
			record.HostName = hostName
		}
		if roomName, ok := metadata.RecordMeta["room_name"].(string); ok {
			record.RoomName = roomName
		}
		if startTime, ok := metadata.RecordMeta["start_time"].(string); ok {
			if parsed, parseErr := time.Parse(time.RFC3339, startTime); parseErr == nil {
				record.RecordedAt = &parsed
			}
		}
		recordedAt := time.Time{}
		if record.RecordedAt != nil {
			recordedAt = *record.RecordedAt
		}
		record.DisplayTitle = RecordDisplayTitle(videoPath, record.RoomName, recordedAt)
		if metadata.CompletedAt != nil {
			deadline := metadata.CompletedAt.Add(time.Duration(retentionDays) * 24 * time.Hour)
			record.RetentionDeadline = &deadline
		}
		return record, nil
	}

	record.SourceExists = sourceExistsForVideo(videoPath, sourceRoot)
	return record, nil
}

func isDeletableSourcePath(videoPath, sourceRoot, sourcePath string) bool {
	if strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(sourceRoot) == "" {
		return false
	}
	if same, err := sameCleanPath(sourcePath, videoPath); err == nil && same {
		return false
	}
	inside, err := pathWithinRoot(sourcePath, sourceRoot)
	return err == nil && inside
}

func sameCleanPath(left, right string) (bool, error) {
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return false, err
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return false, err
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs), nil
}

func pathWithinRoot(path, root string) (bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(pathAbs))
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel), nil
}

func sourceExistsForVideo(videoPath, sourceRoot string) bool {
	sourcePath, err := ResolveSourcePath(videoPath, sourceRoot)
	if err != nil {
		return false
	}
	_, err = os.Stat(sourcePath)
	return err == nil
}
