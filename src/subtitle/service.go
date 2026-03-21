package subtitle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Status string

const (
	StatusIdle      Status = "idle"
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

type Segment struct {
	Index int    `json:"index"`
	Start string `json:"start"`
	End   string `json:"end"`
	Text  string `json:"text"`
}

type Metadata struct {
	Status          Status         `json:"status"`
	Provider        string         `json:"provider,omitempty"`
	Language        string         `json:"language,omitempty"`
	SourcePath      string         `json:"source_path,omitempty"`
	OutputPath      string         `json:"output_path,omitempty"`
	SRTPath         string         `json:"srt_path,omitempty"`
	KeepSource      bool           `json:"keep_source"`
	SourceExists    bool           `json:"source_exists"`
	LastError       string         `json:"last_error,omitempty"`
	RenderPreset    string         `json:"render_preset,omitempty"`
	RendererStatus  Status         `json:"renderer_status,omitempty"`
	RendererError   string         `json:"renderer_error,omitempty"`
	Segments        []Segment      `json:"segments,omitempty"`
	RecordMeta      map[string]any `json:"record_meta,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
	UpdatedAt       time.Time      `json:"updated_at,omitempty"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
	SourceDeletedAt *time.Time     `json:"source_deleted_at,omitempty"`
}

func sidecarPathForVideo(videoPath string) string {
	return strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".subtitle.json"
}

func SaveMetadata(path string, metadata Metadata) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = time.Now().UTC()
	}
	metadata.UpdatedAt = time.Now().UTC()
	bytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, bytes, 0o644)
}

func LoadMetadata(path string) (Metadata, error) {
	var metadata Metadata
	bytes, err := os.ReadFile(path)
	if err != nil {
		return metadata, err
	}
	if err := json.Unmarshal(bytes, &metadata); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func ResolveLibraryVideoPath(sourcePath, libraryRoot string) (string, error) {
	if rel, err := filepath.Rel(libraryRoot, sourcePath); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		if _, statErr := os.Stat(sourcePath); statErr == nil {
			return sourcePath, nil
		}
	}

	var metadataResolved string
	metadataWalkErr := filepath.WalkDir(libraryRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".json" || !strings.HasSuffix(path, ".subtitle.json") {
			return nil
		}
		metadata, err := LoadMetadata(path)
		if err != nil {
			return nil
		}
		if metadata.SourcePath != sourcePath {
			return nil
		}
		outputPath := metadata.OutputPath
		if outputPath == "" {
			outputPath = strings.TrimSuffix(path, ".subtitle.json") + filepath.Ext(sourcePath)
		}
		if _, statErr := os.Stat(outputPath); statErr == nil {
			metadataResolved = outputPath
			return errors.New("subtitle: resolved")
		}
		return nil
	})
	if metadataWalkErr != nil && metadataWalkErr.Error() != "subtitle: resolved" {
		return "", metadataWalkErr
	}
	if metadataResolved != "" {
		return metadataResolved, nil
	}

	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return "", err
	}

	var resolved string
	walkErr := filepath.WalkDir(libraryRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != filepath.Ext(sourcePath) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if os.SameFile(sourceInfo, info) {
			resolved = path
			return errors.New("subtitle: resolved")
		}
		return nil
	})
	if walkErr != nil && walkErr.Error() != "subtitle: resolved" {
		return "", walkErr
	}
	if resolved == "" {
		return "", fmt.Errorf("未在字幕库中找到源文件对应的展示视频: %s", sourcePath)
	}
	return resolved, nil
}

func ResolveSourcePath(libraryPath, sourceRoot string) (string, error) {
	metadataPath := sidecarPathForVideo(libraryPath)
	if metadata, err := LoadMetadata(metadataPath); err == nil && metadata.SourcePath != "" {
		if _, statErr := os.Stat(metadata.SourcePath); statErr == nil {
			return metadata.SourcePath, nil
		}
	}

	libraryInfo, err := os.Stat(libraryPath)
	if err != nil {
		return "", err
	}

	if rel, err := filepath.Rel(sourceRoot, libraryPath); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return libraryPath, nil
	}

	var resolved string
	walkErr := filepath.WalkDir(sourceRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if os.SameFile(libraryInfo, info) {
			resolved = path
			return errors.New("subtitle: resolved")
		}
		return nil
	})
	if walkErr != nil && walkErr.Error() != "subtitle: resolved" {
		return "", walkErr
	}
	if resolved == "" {
		return "", fmt.Errorf("未找到展示视频对应的源文件: %s", libraryPath)
	}
	return resolved, nil
}

func (m Metadata) ShouldDeleteSource(now time.Time, retentionDays int) bool {
	if m.Status != StatusCompleted {
		return false
	}
	if m.KeepSource || m.SourcePath == "" || !m.SourceExists {
		return false
	}
	if m.CompletedAt == nil || m.OutputPath == "" || m.SRTPath == "" {
		return false
	}
	if m.SourceDeletedAt != nil {
		return false
	}
	deadline := m.CompletedAt.Add(time.Duration(retentionDays) * 24 * time.Hour)
	return !deadline.After(now)
}
