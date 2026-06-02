package stages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/subtitle"
)

const knowledgeSessionManifestDir = ".knowledge_sessions"

var knowledgeSessionManifestMu sync.Mutex

type knowledgeSessionManifest struct {
	SourceID          string                           `json:"source_id"`
	LiveSessionID     string                           `json:"live_session_id"`
	UpdatedAt         time.Time                        `json:"updated_at"`
	PostedContentHash string                           `json:"posted_content_hash,omitempty"`
	PostedAt          *time.Time                       `json:"posted_at,omitempty"`
	Sources           []knowledgeSessionManifestSource `json:"sources"`
}

type knowledgeSessionManifestSource struct {
	TaskID          string     `json:"task_id,omitempty"`
	SourceID        string     `json:"source_id"`
	LibraryPath     string     `json:"library_path"`
	MetadataPath    string     `json:"metadata_path"`
	ContentHash     string     `json:"content_hash"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	RegisteredAt    time.Time  `json:"registered_at"`
	LastSubmittedAt *time.Time `json:"last_submitted_at,omitempty"`
}

func knowledgeSessionManifestPath(libraryRoot, sessionID string) string {
	sum := sha256.Sum256([]byte("live-session:" + strings.TrimSpace(sessionID)))
	return filepath.Join(libraryRoot, knowledgeSessionManifestDir, hex.EncodeToString(sum[:])+".json")
}

func loadKnowledgeSessionManifest(path string) (knowledgeSessionManifest, error) {
	var manifest knowledgeSessionManifest
	bytes, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(bytes, &manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func loadOrCreateKnowledgeSessionManifest(path, sessionID string) (knowledgeSessionManifest, error) {
	manifest, err := loadKnowledgeSessionManifest(path)
	if err == nil {
		return manifest, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return manifest, err
	}
	sourceID := "live-session:" + strings.TrimSpace(sessionID)
	return knowledgeSessionManifest{
		SourceID:      sourceID,
		LiveSessionID: strings.TrimSpace(sessionID),
		Sources:       []knowledgeSessionManifestSource{},
	}, nil
}

func saveKnowledgeSessionManifest(path string, manifest knowledgeSessionManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	bytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, bytes, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func registerKnowledgeSessionSource(
	manifest *knowledgeSessionManifest,
	libraryRoot string,
	input knowledgeSessionPayloadInput,
	now time.Time,
) (bool, error) {
	if manifest == nil {
		return false, fmt.Errorf("knowledge session manifest is nil")
	}
	if input.Metadata == nil {
		return false, fmt.Errorf("knowledge session source %s has no metadata", input.LibraryPath)
	}
	sourceID := knowledgeSourceID(libraryRoot, input.LibraryPath)
	sourceHash := knowledgeContentHash(sourceID, input.Metadata.Language, input.Metadata.Segments)
	source := knowledgeSessionManifestSource{
		TaskID:       input.TaskID,
		SourceID:     sourceID,
		LibraryPath:  input.LibraryPath,
		MetadataPath: input.MetadataPath,
		ContentHash:  sourceHash,
		CompletedAt:  input.Metadata.CompletedAt,
		RegisteredAt: now,
	}

	for index, existing := range manifest.Sources {
		if existing.LibraryPath != input.LibraryPath && existing.MetadataPath != input.MetadataPath {
			continue
		}
		if existing.TaskID == source.TaskID &&
			existing.SourceID == source.SourceID &&
			existing.ContentHash == source.ContentHash &&
			existing.MetadataPath == source.MetadataPath {
			return false, nil
		}
		source.LastSubmittedAt = existing.LastSubmittedAt
		manifest.Sources[index] = source
		sortKnowledgeSessionSources(manifest.Sources)
		manifest.UpdatedAt = now
		manifest.PostedContentHash = ""
		manifest.PostedAt = nil
		return true, nil
	}

	manifest.Sources = append(manifest.Sources, source)
	sortKnowledgeSessionSources(manifest.Sources)
	manifest.UpdatedAt = now
	manifest.PostedContentHash = ""
	manifest.PostedAt = nil
	return true, nil
}

func sortKnowledgeSessionSources(sources []knowledgeSessionManifestSource) {
	sort.SliceStable(sources, func(i, j int) bool {
		return filepath.ToSlash(sources[i].LibraryPath) < filepath.ToSlash(sources[j].LibraryPath)
	})
}

func knowledgeSessionManifestContentHash(manifest knowledgeSessionManifest) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\n%s\n", manifest.SourceID, manifest.LiveSessionID)
	for _, source := range manifest.Sources {
		_, _ = fmt.Fprintf(hash, "%s|%s|%s\n", source.SourceID, filepath.ToSlash(source.LibraryPath), source.ContentHash)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func knowledgeSessionInputsFromManifest(manifest knowledgeSessionManifest) ([]knowledgeSessionPayloadInput, error) {
	inputs := make([]knowledgeSessionPayloadInput, 0, len(manifest.Sources))
	for _, source := range manifest.Sources {
		metadata, err := subtitle.LoadMetadata(source.MetadataPath)
		if err != nil {
			return nil, fmt.Errorf("load session metadata %s: %w", source.MetadataPath, err)
		}
		if metadata.Status != subtitle.StatusCompleted {
			continue
		}
		inputs = append(inputs, knowledgeSessionPayloadInput{
			TaskID:       source.TaskID,
			LibraryPath:  source.LibraryPath,
			MetadataPath: source.MetadataPath,
			Metadata:     &metadata,
		})
	}
	return inputs, nil
}
