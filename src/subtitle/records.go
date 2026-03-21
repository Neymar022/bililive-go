package subtitle

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Record struct {
	RelativePath      string     `json:"relative_path"`
	VideoPath         string     `json:"video_path"`
	SRTPath           string     `json:"srt_path,omitempty"`
	SourcePath        string     `json:"source_path,omitempty"`
	Status            Status     `json:"status"`
	Provider          string     `json:"provider,omitempty"`
	RenderPreset      string     `json:"render_preset,omitempty"`
	RendererStatus    Status     `json:"renderer_status,omitempty"`
	RendererError     string     `json:"renderer_error,omitempty"`
	Platform          string     `json:"platform,omitempty"`
	HostName          string     `json:"host_name,omitempty"`
	RoomName          string     `json:"room_name,omitempty"`
	KeepSource        bool       `json:"keep_source"`
	SourceExists      bool       `json:"source_exists"`
	RetentionDeadline *time.Time `json:"retention_deadline,omitempty"`
	RecordedAt        *time.Time `json:"recorded_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	Segments          []Segment  `json:"segments,omitempty"`
}

func ListRecords(libraryRoot, sourceRoot string, retentionDays int) ([]Record, error) {
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
		record.SRTPath = metadata.SRTPath
		record.SourcePath = metadata.SourcePath
		record.KeepSource = metadata.KeepSource
		record.SourceExists = metadata.SourceExists
		record.LastError = metadata.LastError
		record.Segments = metadata.Segments

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
		if metadata.CompletedAt != nil {
			deadline := metadata.CompletedAt.Add(time.Duration(retentionDays) * 24 * time.Hour)
			record.RetentionDeadline = &deadline
		}
		return record, nil
	}

	record.SourceExists = sourceExistsForVideo(videoPath, sourceRoot)
	return record, nil
}

func sourceExistsForVideo(videoPath, sourceRoot string) bool {
	sourcePath, err := ResolveSourcePath(videoPath, sourceRoot)
	if err != nil {
		return false
	}
	_, err = os.Stat(sourcePath)
	return err == nil
}
