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
	LiveSessionID      string                    `json:"live_session_id,omitempty"`
	Host               string                    `json:"host,omitempty"`
	Title              string                    `json:"title"`
	Topic              string                    `json:"topic,omitempty"`
	SourceVideoPath    string                    `json:"source_video_path,omitempty"`
	SourceVideos       []knowledgeSourcePayload  `json:"source_videos,omitempty"`
	MediaSegments      []knowledgeSourcePayload  `json:"media_segments,omitempty"`
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
	Start           float64 `json:"start"`
	End             float64 `json:"end"`
	Text            string  `json:"text"`
	SourceIndex     int     `json:"source_index,omitempty"`
	SourceVideoPath string  `json:"source_video_path,omitempty"`
	SubtitlePath    string  `json:"subtitle_path,omitempty"`
	LocalStart      float64 `json:"local_start,omitempty"`
	LocalEnd        float64 `json:"local_end,omitempty"`
}

type knowledgeSourcePayload struct {
	TaskID          string  `json:"task_id,omitempty"`
	SourceID        string  `json:"source_id"`
	SourceVideoPath string  `json:"source_video_path"`
	SubtitlePath    string  `json:"subtitle_path,omitempty"`
	Title           string  `json:"title,omitempty"`
	Offset          float64 `json:"offset"`
}

type knowledgeSessionPayloadInput struct {
	TaskID       string
	LibraryPath  string
	MetadataPath string
	Metadata     *subtitle.Metadata
}

func (s *SubtitleGenerateStage) syncKnowledge(
	ctx *pipeline.PipelineContext,
	cfg configs.SubtitleKnowledgeSyncConfig,
	libraryRoot string,
	libraryPath string,
	metadataPath string,
	metadata *subtitle.Metadata,
) error {
	return s.syncKnowledgeAt(ctx, cfg, libraryRoot, libraryPath, metadataPath, metadata, time.Now().UTC())
}

func (s *SubtitleGenerateStage) syncKnowledgeAt(
	ctx *pipeline.PipelineContext,
	cfg configs.SubtitleKnowledgeSyncConfig,
	libraryRoot string,
	libraryPath string,
	metadataPath string,
	metadata *subtitle.Metadata,
	now time.Time,
) error {
	if !cfg.Enabled {
		return nil
	}

	taskID := knowledgeTaskID(ctx)
	sourceID := knowledgeSourceID(libraryRoot, libraryPath)
	hasLiveSession := ctx != nil && strings.TrimSpace(ctx.RecordInfo.LiveSessionID) != ""
	if hasLiveSession {
		sourceID = "live-session:" + strings.TrimSpace(ctx.RecordInfo.LiveSessionID)
	}

	if skipped, durationSeconds, minDuration := shouldSkipKnowledgeSyncForDuration(cfg, metadata, hasLiveSession); skipped {
		now := time.Now().UTC()
		metadata.KnowledgeSyncStatus = subtitle.StatusSkipped
		metadata.KnowledgeSyncTaskID = taskID
		metadata.KnowledgeSyncSourceID = sourceID
		metadata.KnowledgeSyncError = fmt.Sprintf("skipped: video duration %.2fs at or below minimum %.0fs", durationSeconds, minDuration.Seconds())
		metadata.KnowledgeSyncUpdatedAt = &now
		if err := subtitle.SaveMetadata(metadataPath, *metadata); err != nil {
			logrus.WithError(err).WithField("metadata", metadataPath).Warn("subtitle_generate: failed to save knowledge sync skipped status")
		}
		logrus.WithFields(logrus.Fields{
			"task_id":          taskID,
			"source_id":        sourceID,
			"duration_seconds": durationSeconds,
			"minimum_seconds":  minDuration.Seconds(),
		}).Info("subtitle_generate: skipped BiliNote knowledge sync for short video")
		s.logs += fmt.Sprintf("知识同步已跳过（视频过短 %.2fs <= %.0fs）: %s\n", durationSeconds, minDuration.Seconds(), filepath.Base(libraryPath))
		return nil
	}

	if hasLiveSession {
		return s.syncLiveSessionKnowledgeAt(ctx, cfg, libraryRoot, libraryPath, metadataPath, metadata, now)
	}

	s.commands = append(s.commands, "POST BiliNote /api/knowledge/ingest (non-blocking)")

	now = time.Now().UTC()
	metadata.KnowledgeSyncStatus = subtitle.StatusRunning
	metadata.KnowledgeSyncTaskID = taskID
	metadata.KnowledgeSyncSourceID = sourceID
	metadata.KnowledgeSyncError = ""
	metadata.KnowledgeSyncAttempts++
	metadata.KnowledgeSyncUpdatedAt = &now
	if err := subtitle.SaveMetadata(metadataPath, *metadata); err != nil {
		logrus.WithError(err).WithField("metadata", metadataPath).Warn("subtitle_generate: failed to save knowledge sync running status")
	}

	var payload knowledgeIngestPayload
	var err error
	payload, err = buildKnowledgeIngestPayload(ctx, cfg, libraryRoot, libraryPath, metadata)
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
		return nil
	}

	metadata.KnowledgeSyncStatus = subtitle.StatusQueued
	metadata.KnowledgeSyncError = ""
	if err := subtitle.SaveMetadata(metadataPath, *metadata); err != nil {
		logrus.WithError(err).WithField("metadata", metadataPath).Warn("subtitle_generate: failed to save knowledge sync queued status")
	}
	s.logs += fmt.Sprintf("知识同步已提交: %s\n", filepath.Base(libraryPath))
	return nil
}

func (s *SubtitleGenerateStage) syncLiveSessionKnowledgeAt(
	ctx *pipeline.PipelineContext,
	cfg configs.SubtitleKnowledgeSyncConfig,
	libraryRoot string,
	libraryPath string,
	metadataPath string,
	metadata *subtitle.Metadata,
	now time.Time,
) error {
	sessionID := strings.TrimSpace(ctx.RecordInfo.LiveSessionID)
	sourceID := "live-session:" + sessionID
	taskID := knowledgeTaskID(ctx)
	manifestPath := knowledgeSessionManifestPath(libraryRoot, sessionID)

	knowledgeSessionManifestMu.Lock()
	defer knowledgeSessionManifestMu.Unlock()

	manifest, err := loadOrCreateKnowledgeSessionManifest(manifestPath, sessionID)
	if err != nil {
		return err
	}
	changed, err := registerKnowledgeSessionSource(&manifest, libraryRoot, knowledgeSessionPayloadInput{
		TaskID:       taskID,
		LibraryPath:  libraryPath,
		MetadataPath: metadataPath,
		Metadata:     metadata,
	}, now)
	if err != nil {
		return err
	}
	if changed {
		if err := saveKnowledgeSessionManifest(manifestPath, manifest); err != nil {
			return err
		}
	}

	quietWindow := cfg.GetLiveSessionQuietWindow()
	if quietWindow > 0 {
		readyAt := manifest.UpdatedAt.Add(quietWindow)
		if now.Before(readyAt) {
			delay := readyAt.Sub(now)
			metadata.KnowledgeSyncStatus = subtitle.StatusQueued
			metadata.KnowledgeSyncTaskID = taskID
			metadata.KnowledgeSyncSourceID = sourceID
			metadata.KnowledgeSyncError = fmt.Sprintf("waiting for same live session aggregation until %s", readyAt.Format(time.RFC3339))
			metadata.KnowledgeSyncUpdatedAt = &now
			if err := subtitle.SaveMetadata(metadataPath, *metadata); err != nil {
				logrus.WithError(err).WithField("metadata", metadataPath).Warn("subtitle_generate: failed to save knowledge sync wait status")
			}
			s.logs += fmt.Sprintf("知识同步等待同场直播聚合: %s\n", filepath.Base(libraryPath))
			return pipeline.NewRetryLaterError(fmt.Errorf("waiting for same live session aggregation"), delay)
		}
	}

	inputs, err := knowledgeSessionInputsFromManifest(manifest)
	if err != nil {
		return err
	}

	goCtx := context.Background()
	ffmpegPath := ""
	if ctx != nil {
		if ctx.Ctx != nil {
			goCtx = ctx.Ctx
		}
		ffmpegPath = ctx.FFmpegPath
	}
	aggregate, err := publishLiveSessionMediaAggregate(goCtx, ffmpegPath, libraryRoot, &manifest)
	if err != nil {
		return err
	}

	contentHash := knowledgeSessionManifestContentHash(manifest)
	if manifest.PostedContentHash == contentHash && contentHash != "" {
		metadata.KnowledgeSyncStatus = subtitle.StatusQueued
		metadata.KnowledgeSyncTaskID = taskID
		metadata.KnowledgeSyncSourceID = sourceID
		metadata.KnowledgeSyncError = ""
		metadata.KnowledgeSyncUpdatedAt = &now
		if err := subtitle.SaveMetadata(metadataPath, *metadata); err != nil {
			logrus.WithError(err).WithField("metadata", metadataPath).Warn("subtitle_generate: failed to save knowledge sync queued status")
		}
		s.logs += fmt.Sprintf("知识同步已由同场直播聚合提交: %s\n", filepath.Base(libraryPath))
		return nil
	}

	var payload knowledgeIngestPayload
	if aggregate != nil {
		payload, err = buildKnowledgeLiveSessionAggregateIngestPayload(ctx, cfg, libraryRoot, aggregate)
	} else {
		payload, err = buildKnowledgeSessionIngestPayload(ctx, cfg, libraryRoot, inputs)
	}
	if err != nil {
		return err
	}

	s.commands = append(s.commands, "POST BiliNote /api/knowledge/ingest (same-live aggregation, non-blocking)")
	markKnowledgeSessionSources(manifest, subtitle.StatusRunning, sourceID, "", now, true)
	err = postKnowledgeIngest(ctx, cfg, payload)
	if err != nil {
		errorMessage := sanitizeKnowledgeSyncError(err)
		markKnowledgeSessionSources(manifest, subtitle.StatusFailed, sourceID, errorMessage, now, false)
		logrus.WithError(err).WithFields(logrus.Fields{
			"task_id":         taskID,
			"source_id":       sourceID,
			"live_session_id": sessionID,
		}).Warn("subtitle_generate: BiliNote same-live knowledge sync failed")
		s.logs += fmt.Sprintf("同场直播知识同步失败（不阻塞）: %s\n", filepath.Base(libraryPath))
		return nil
	}

	manifest.PostedContentHash = contentHash
	manifest.PostedAt = &now
	for index := range manifest.Sources {
		manifest.Sources[index].LastSubmittedAt = &now
	}
	if err := saveKnowledgeSessionManifest(manifestPath, manifest); err != nil {
		return err
	}
	markKnowledgeSessionSources(manifest, subtitle.StatusQueued, sourceID, "", now, false)
	s.logs += fmt.Sprintf("同场直播知识同步已提交: %s\n", filepath.Base(libraryPath))
	return nil
}

func markKnowledgeSessionSources(
	manifest knowledgeSessionManifest,
	status subtitle.Status,
	sourceID string,
	errorMessage string,
	now time.Time,
	incrementAttempts bool,
) {
	for _, source := range manifest.Sources {
		metadata, err := subtitle.LoadMetadata(source.MetadataPath)
		if err != nil {
			logrus.WithError(err).WithField("metadata", source.MetadataPath).Warn("subtitle_generate: failed to load session metadata for knowledge status")
			continue
		}
		metadata.KnowledgeSyncStatus = status
		metadata.KnowledgeSyncTaskID = source.TaskID
		metadata.KnowledgeSyncSourceID = sourceID
		metadata.KnowledgeSyncError = errorMessage
		metadata.KnowledgeSyncUpdatedAt = &now
		if incrementAttempts {
			metadata.KnowledgeSyncAttempts++
		}
		if err := subtitle.SaveMetadata(source.MetadataPath, metadata); err != nil {
			logrus.WithError(err).WithField("metadata", source.MetadataPath).Warn("subtitle_generate: failed to save session knowledge status")
		}
	}
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

func buildKnowledgeSessionIngestPayload(
	ctx *pipeline.PipelineContext,
	cfg configs.SubtitleKnowledgeSyncConfig,
	libraryRoot string,
	inputs []knowledgeSessionPayloadInput,
) (knowledgeIngestPayload, error) {
	if len(inputs) == 0 {
		return knowledgeIngestPayload{}, fmt.Errorf("knowledge session sync has no segment inputs")
	}
	sessionID := ""
	host := ""
	topic := ""
	if ctx != nil {
		sessionID = strings.TrimSpace(ctx.RecordInfo.LiveSessionID)
		host = ctx.RecordInfo.HostName
		topic = ctx.RecordInfo.RoomName
	}
	if sessionID == "" {
		return knowledgeIngestPayload{}, fmt.Errorf("knowledge session sync requires live_session_id")
	}

	sourceID := "live-session:" + sessionID
	title := strings.TrimSuffix(filepath.Base(inputs[0].LibraryPath), filepath.Ext(inputs[0].LibraryPath))
	payload := knowledgeIngestPayload{
		SourceID:           sourceID,
		SourceType:         "bililive-go",
		TaskID:             knowledgeTaskID(ctx),
		LiveSessionID:      sessionID,
		Host:               host,
		Title:              title,
		Topic:              topic,
		SourceVideoPath:    inputs[0].LibraryPath,
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

	var offset float64
	var allSegments []subtitle.Segment
	language := ""
	for sourceIndex, input := range inputs {
		if input.Metadata == nil {
			return knowledgeIngestPayload{}, fmt.Errorf("knowledge session input %d has no metadata", sourceIndex)
		}
		sourceID := knowledgeSourceID(libraryRoot, input.LibraryPath)
		sourcePayload := knowledgeSourcePayload{
			TaskID:          input.TaskID,
			SourceID:        sourceID,
			SourceVideoPath: input.LibraryPath,
			SubtitlePath:    input.Metadata.SRTPath,
			Title:           strings.TrimSuffix(filepath.Base(input.LibraryPath), filepath.Ext(input.LibraryPath)),
			Offset:          offset,
		}
		payload.SourceVideos = append(payload.SourceVideos, sourcePayload)
		payload.MediaSegments = append(payload.MediaSegments, sourcePayload)
		if payload.SubtitlePath == "" {
			payload.SubtitlePath = input.Metadata.SRTPath
		}
		if language == "" {
			language = input.Metadata.Language
		}

		segments, err := buildKnowledgeSegments(input.Metadata.Segments)
		if err != nil {
			return knowledgeIngestPayload{}, err
		}
		var maxEnd float64
		for _, segment := range segments {
			localStart := segment.Start
			localEnd := segment.End
			if localEnd > maxEnd {
				maxEnd = localEnd
			}
			segment.Start = offset + localStart
			segment.End = offset + localEnd
			segment.SourceIndex = sourceIndex
			segment.SourceVideoPath = input.LibraryPath
			segment.SubtitlePath = input.Metadata.SRTPath
			segment.LocalStart = localStart
			segment.LocalEnd = localEnd
			payload.Segments = append(payload.Segments, segment)
		}
		allSegments = append(allSegments, input.Metadata.Segments...)
		offset += maxEnd
	}
	if len(payload.Segments) == 0 {
		return knowledgeIngestPayload{}, fmt.Errorf("knowledge session sync has no transcript segments")
	}
	payload.Language = language
	payload.ContentHash = knowledgeContentHash(sourceID, language, allSegments)
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

func shouldSkipKnowledgeSyncForDuration(cfg configs.SubtitleKnowledgeSyncConfig, metadata *subtitle.Metadata, hasLiveSession bool) (bool, float64, time.Duration) {
	minDuration := cfg.GetMinVideoDuration()
	if minDuration <= 0 || metadata == nil {
		return false, 0, minDuration
	}
	durationSeconds, err := knowledgeTranscriptDurationSeconds(metadata.Segments)
	if err != nil {
		logrus.WithError(err).Warn("subtitle_generate: cannot evaluate transcript duration for knowledge sync skip")
		return false, 0, minDuration
	}
	return shouldSkipStandaloneKnowledgeArtifact(durationSeconds, hasLiveSession, minDuration), durationSeconds, minDuration
}

func knowledgeTranscriptDurationSeconds(segments []subtitle.Segment) (float64, error) {
	var maxEnd float64
	hasText := false
	for _, segment := range segments {
		if strings.TrimSpace(segment.Text) == "" {
			continue
		}
		end, err := parseSubtitleTimestampSeconds(segment.End)
		if err != nil {
			return 0, fmt.Errorf("invalid subtitle segment end %q: %w", segment.End, err)
		}
		if end > maxEnd {
			maxEnd = end
		}
		hasText = true
	}
	if !hasText {
		return 0, nil
	}
	return maxEnd, nil
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
