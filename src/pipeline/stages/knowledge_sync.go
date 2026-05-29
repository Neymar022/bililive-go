package stages

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/sirupsen/logrus"
)

type knowledgeIngestPayload struct {
	SourceID           string                    `json:"source_id"`
	SourceType         string                    `json:"source_type"`
	TaskID             string                    `json:"task_id,omitempty"`
	Host               string                    `json:"host,omitempty"`
	Title              string                    `json:"title"`
	Topic              string                    `json:"topic,omitempty"`
	SourceVideoPath    string                    `json:"source_video_path,omitempty"`
	SourceURL          string                    `json:"source_url,omitempty"`
	SubtitlePath       string                    `json:"subtitle_path,omitempty"`
	Language           string                    `json:"language,omitempty"`
	ContentHash        string                    `json:"content_hash,omitempty"`
	Segments           []knowledgeSegmentPayload `json:"segments"`
	GenerateNote       bool                      `json:"generate_note"`
	NonBlocking        bool                      `json:"non_blocking"`
	ModelName          string                    `json:"model_name,omitempty"`
	ProviderID         string                    `json:"provider_id,omitempty"`
	Format             []string                  `json:"format,omitempty"`
	Link               *bool                     `json:"link,omitempty"`
	Screenshot         *bool                     `json:"screenshot,omitempty"`
	Style              string                    `json:"style,omitempty"`
	Extras             string                    `json:"extras,omitempty"`
	VideoUnderstanding *bool                     `json:"video_understanding,omitempty"`
	VideoInterval      int                       `json:"video_interval,omitempty"`
	GridSize           []int                     `json:"grid_size,omitempty"`
}

type knowledgeSegmentPayload struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

func (s *SubtitleGenerateStage) syncKnowledge(
	ctx *pipeline.PipelineContext,
	cfg configs.SubtitleKnowledgeSyncConfig,
	libraryRoot string,
	libraryPath string,
	metadataPath string,
	metadata *subtitle.Metadata,
) {
	if !cfg.Enabled {
		return
	}
	s.commands = append(s.commands, "POST BiliNote /api/knowledge/ingest (non-blocking)")

	taskID := knowledgeTaskID(ctx)
	sourceID := knowledgeSourceID(libraryRoot, libraryPath)
	now := time.Now().UTC()
	metadata.KnowledgeSyncStatus = subtitle.StatusRunning
	metadata.KnowledgeSyncTaskID = taskID
	metadata.KnowledgeSyncSourceID = sourceID
	metadata.KnowledgeSyncError = ""
	metadata.KnowledgeSyncAttempts++
	metadata.KnowledgeSyncUpdatedAt = &now
	if err := subtitle.SaveMetadata(metadataPath, *metadata); err != nil {
		logrus.WithError(err).WithField("metadata", metadataPath).Warn("subtitle_generate: failed to save knowledge sync running status")
	}

	payload, err := buildKnowledgeIngestPayload(ctx, cfg, libraryRoot, libraryPath, metadata)
	if err == nil {
		err = postKnowledgeIngest(ctx, cfg, payload)
	}

	now = time.Now().UTC()
	metadata.KnowledgeSyncUpdatedAt = &now
	if err != nil {
		metadata.KnowledgeSyncStatus = subtitle.StatusFailed
		metadata.KnowledgeSyncError = sanitizeKnowledgeSyncError(err)
		if saveErr := subtitle.SaveMetadata(metadataPath, *metadata); saveErr != nil {
			logrus.WithError(saveErr).WithField("metadata", metadataPath).Warn("subtitle_generate: failed to save knowledge sync failure status")
		}
		logrus.WithError(err).WithFields(logrus.Fields{
			"task_id":   taskID,
			"source_id": sourceID,
		}).Warn("subtitle_generate: BiliNote knowledge sync failed")
		s.logs += fmt.Sprintf("知识同步失败（不阻塞）: %s\n", filepath.Base(libraryPath))
		return
	}

	metadata.KnowledgeSyncStatus = subtitle.StatusQueued
	metadata.KnowledgeSyncError = ""
	if err := subtitle.SaveMetadata(metadataPath, *metadata); err != nil {
		logrus.WithError(err).WithField("metadata", metadataPath).Warn("subtitle_generate: failed to save knowledge sync queued status")
	}
	s.logs += fmt.Sprintf("知识同步已提交: %s\n", filepath.Base(libraryPath))
}

func buildKnowledgeIngestPayload(
	ctx *pipeline.PipelineContext,
	cfg configs.SubtitleKnowledgeSyncConfig,
	libraryRoot string,
	libraryPath string,
	metadata *subtitle.Metadata,
) (knowledgeIngestPayload, error) {
	segments, err := buildKnowledgeSegments(metadata.Segments)
	if err != nil {
		return knowledgeIngestPayload{}, err
	}
	if len(segments) == 0 {
		return knowledgeIngestPayload{}, fmt.Errorf("knowledge sync has no transcript segments")
	}

	sourceID := knowledgeSourceID(libraryRoot, libraryPath)
	title := strings.TrimSuffix(filepath.Base(libraryPath), filepath.Ext(libraryPath))
	host := ""
	topic := ""
	if ctx != nil {
		host = ctx.RecordInfo.HostName
		topic = ctx.RecordInfo.RoomName
	}

	payload := knowledgeIngestPayload{
		SourceID:           sourceID,
		SourceType:         "bililive-go",
		TaskID:             knowledgeTaskID(ctx),
		Host:               host,
		Title:              title,
		Topic:              topic,
		SourceVideoPath:    libraryPath,
		SubtitlePath:       metadata.SRTPath,
		Language:           metadata.Language,
		ContentHash:        knowledgeContentHash(sourceID, metadata.Language, metadata.Segments),
		Segments:           segments,
		GenerateNote:       cfg.GenerateNote,
		NonBlocking:        cfg.NonBlocking,
		ModelName:          cfg.GetModelName(),
		ProviderID:         cfg.GetProviderID(),
		Format:             append([]string(nil), cfg.Format...),
		Link:               cfg.Link,
		Screenshot:         cfg.Screenshot,
		Style:              cfg.Style,
		Extras:             cfg.Extras,
		VideoUnderstanding: cfg.VideoUnderstanding,
		VideoInterval:      cfg.VideoInterval,
		GridSize:           append([]int(nil), cfg.GridSize...),
	}
	return payload, nil
}

func postKnowledgeIngest(ctx *pipeline.PipelineContext, cfg configs.SubtitleKnowledgeSyncConfig, payload knowledgeIngestPayload) error {
	endpoint := cfg.GetEndpoint()
	if endpoint == "" {
		return fmt.Errorf("BiliNote knowledge ingest endpoint is empty")
	}
	token := cfg.GetToken()
	if token == "" {
		return fmt.Errorf("BiliNote knowledge ingest token is empty")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	baseCtx := context.Background()
	if ctx != nil && ctx.Ctx != nil {
		baseCtx = ctx.Ctx
	}
	timeout := cfg.GetTimeout()
	requestCtx, cancel := context.WithTimeout(baseCtx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(responseBody))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("BiliNote knowledge ingest returned HTTP %d: %s", resp.StatusCode, message)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func buildKnowledgeSegments(segments []subtitle.Segment) ([]knowledgeSegmentPayload, error) {
	result := make([]knowledgeSegmentPayload, 0, len(segments))
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		start, err := parseSubtitleTimestampSeconds(segment.Start)
		if err != nil {
			return nil, fmt.Errorf("invalid subtitle segment start %q: %w", segment.Start, err)
		}
		end, err := parseSubtitleTimestampSeconds(segment.End)
		if err != nil {
			return nil, fmt.Errorf("invalid subtitle segment end %q: %w", segment.End, err)
		}
		result = append(result, knowledgeSegmentPayload{Start: start, End: end, Text: text})
	}
	return result, nil
}

func parseSubtitleTimestampSeconds(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty timestamp")
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return seconds, nil
	}

	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("unsupported timestamp format")
	}
	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	seconds, err := strconv.ParseFloat(strings.ReplaceAll(parts[2], ",", "."), 64)
	if err != nil {
		return 0, err
	}
	return float64(hours*3600+minutes*60) + seconds, nil
}

func knowledgeTaskID(ctx *pipeline.PipelineContext) string {
	if ctx == nil || ctx.TaskID <= 0 {
		return ""
	}
	return fmt.Sprintf("bililive-go-%d", ctx.TaskID)
}

func knowledgeSourceID(libraryRoot string, libraryPath string) string {
	if rel, err := filepath.Rel(libraryRoot, libraryPath); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(libraryPath)
}

func knowledgeContentHash(sourceID string, language string, segments []subtitle.Segment) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\n%s\n", sourceID, language)
	for _, segment := range segments {
		_, _ = fmt.Fprintf(hash, "%d|%s|%s|%s\n", segment.Index, segment.Start, segment.End, segment.Text)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sanitizeKnowledgeSyncError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}
