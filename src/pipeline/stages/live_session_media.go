package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pkg/utils"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/bililive-go/bililive-go/src/tools"
)

const liveSessionSegmentsDirName = ".live_session_segments"

type liveSessionMediaAggregate struct {
	LibraryPath  string
	MetadataPath string
	Metadata     subtitle.Metadata
}

type liveSessionMediaConcatFunc func(ctx context.Context, ffmpegPath string, inputs []string, outputPath string) error
type liveSessionMediaProbeDurationFunc func(ctx context.Context, ffmpegPath string, inputPath string) (float64, error)

var liveSessionMediaConcat liveSessionMediaConcatFunc = concatLiveSessionMediaWithFFmpeg
var liveSessionMediaProbeDuration liveSessionMediaProbeDurationFunc = probeLiveSessionMediaDuration
var liveSessionMediaExtractCover = tools.ExtractCoverTo

var libraryMediaEpisodePattern = regexp.MustCompile(`^(.+?)\.S(\d{2})E(\d{4})(?:-S\d{2}E\d{4})?\.(\d{4}-\d{2}-\d{2}) - (.+)\.[^.]+$`)

type liveSessionMediaEpisode struct {
	Alias     string
	Season    string
	Episode   int
	Date      string
	Title     string
	SeasonDir string
}

func publishLiveSessionMediaAggregate(ctx context.Context, ffmpegPath string, libraryRoot string, manifest *knowledgeSessionManifest) (*liveSessionMediaAggregate, error) {
	if manifest == nil || len(manifest.Sources) < 2 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	inputs, err := knowledgeSessionInputsFromManifest(*manifest)
	if err != nil {
		return nil, err
	}
	if len(inputs) < 2 {
		return nil, nil
	}

	sort.SliceStable(inputs, func(i, j int) bool {
		return filepath.ToSlash(inputs[i].LibraryPath) < filepath.ToSlash(inputs[j].LibraryPath)
	})

	aggregatePath, err := liveSessionAggregatePath(inputs)
	if err != nil {
		return nil, err
	}
	aggregateStem := strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))
	aggregateMetadataPath := aggregateStem + ".subtitle.json"

	aggregateMetadata, metadataExists := loadCompletedSubtitleMetadata(
		aggregateMetadataPath,
		aggregatePath,
		aggregateStem+".srt",
		aggregateStem+".ass",
	)
	if metadataExists && aggregateMetadata.RecordMeta["live_session_media_role"] != "aggregate" {
		metadataExists = false
	}
	if !metadataExists {
		segmentPaths, err := liveSessionSegmentVideoPaths(inputs)
		if err != nil {
			return nil, err
		}
		ext := filepath.Ext(aggregatePath)
		tmpPath := strings.TrimSuffix(aggregatePath, ext) + ".tmp" + ext
		_ = os.Remove(tmpPath)
		if err := os.MkdirAll(filepath.Dir(aggregatePath), 0o755); err != nil {
			return nil, err
		}
		if err := liveSessionMediaConcat(ctx, ffmpegPath, segmentPaths, tmpPath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, err
		}
		if err := os.Rename(tmpPath, aggregatePath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, err
		}

		aggregateMetadata, err = writeLiveSessionAggregateSidecars(ctx, ffmpegPath, aggregatePath, manifest, inputs, segmentPaths)
		if err != nil {
			return nil, err
		}
	}

	if err := hideLiveSessionSegmentVideos(libraryRoot, manifest, inputs, aggregatePath); err != nil {
		return nil, err
	}

	return &liveSessionMediaAggregate{
		LibraryPath:  aggregatePath,
		MetadataPath: aggregateMetadataPath,
		Metadata:     aggregateMetadata,
	}, nil
}

func liveSessionAggregatePath(inputs []knowledgeSessionPayloadInput) (string, error) {
	if len(inputs) == 0 {
		return "", fmt.Errorf("live session media aggregate has no inputs")
	}
	target, ok := parseLiveSessionMediaEpisode(inputs[0].LibraryPath)
	if !ok {
		return "", fmt.Errorf("cannot parse library episode path: %s", inputs[0].LibraryPath)
	}
	for _, input := range inputs[1:] {
		parsed, ok := parseLiveSessionMediaEpisode(input.LibraryPath)
		if !ok {
			continue
		}
		if parsed.Episode > target.Episode {
			target = parsed
		}
	}

	episodeText := fmt.Sprintf("S%sE%04d", target.Season, target.Episode)
	name := fmt.Sprintf("%s.%s.%s - %s.mp4", target.Alias, episodeText, target.Date, target.Title)
	return filepath.Join(target.SeasonDir, name), nil
}

func parseLiveSessionMediaEpisode(path string) (liveSessionMediaEpisode, bool) {
	match := libraryMediaEpisodePattern.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return liveSessionMediaEpisode{}, false
	}
	episode, err := strconv.Atoi(match[3])
	if err != nil {
		return liveSessionMediaEpisode{}, false
	}
	return liveSessionMediaEpisode{
		Alias:     match[1],
		Season:    match[2],
		Episode:   episode,
		Date:      match[4],
		Title:     match[5],
		SeasonDir: filepath.Dir(path),
	}, true
}

func liveSessionSegmentVideoPaths(inputs []knowledgeSessionPayloadInput) ([]string, error) {
	paths := make([]string, 0, len(inputs))
	for _, input := range inputs {
		path := ""
		if input.Metadata != nil {
			path = strings.TrimSpace(input.Metadata.OutputPath)
		}
		if path == "" {
			path = input.LibraryPath
		}
		if !fileExists(path) {
			return nil, fmt.Errorf("live session segment video missing: %s", path)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func concatLiveSessionMediaWithFFmpeg(ctx context.Context, ffmpegPath string, inputs []string, outputPath string) error {
	if len(inputs) == 0 {
		return fmt.Errorf("live session media concat has no inputs")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ffmpegPath == "" {
		var err error
		ffmpegPath, err = utils.GetFFmpegPath(ctx)
		if err != nil {
			return fmt.Errorf("ffmpeg not available for live session media concat: %w", err)
		}
	}

	listFile, err := os.CreateTemp(filepath.Dir(outputPath), ".live-session-concat-*.txt")
	if err != nil {
		return err
	}
	listPath := listFile.Name()
	defer os.Remove(listPath)
	for _, input := range inputs {
		if _, err := fmt.Fprintf(listFile, "file '%s'\n", escapeFFmpegConcatPath(input)); err != nil {
			_ = listFile.Close()
			return err
		}
	}
	if err := listFile.Close(); err != nil {
		return err
	}

	args := []string{"-y", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", "-f", "mp4", outputPath}
	output, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg concat failed: %w: %s", err, trimCommandOutput(output))
	}
	return nil
}

func probeLiveSessionMediaDuration(ctx context.Context, ffmpegPath string, inputPath string) (float64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ffmpegPath == "" {
		var err error
		ffmpegPath, err = utils.GetFFmpegPath(ctx)
		if err != nil {
			return 0, err
		}
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, "-i", inputPath, "-hide_banner")
	output, err := cmd.CombinedOutput()
	if err != nil && len(output) == 0 {
		return 0, err
	}
	return parseFFmpegDurationSeconds(string(output))
}

func escapeFFmpegConcatPath(path string) string {
	return strings.ReplaceAll(path, "'", "'\\''")
}

func trimCommandOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 4096 {
		return text[len(text)-4096:]
	}
	return text
}

func writeLiveSessionAggregateSidecars(
	ctx context.Context,
	ffmpegPath string,
	aggregatePath string,
	manifest *knowledgeSessionManifest,
	inputs []knowledgeSessionPayloadInput,
	segmentPaths []string,
) (subtitle.Metadata, error) {
	stem := strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))
	segments, segmentSources, err := mergedLiveSessionSubtitleSegments(ctx, ffmpegPath, inputs, segmentPaths)
	if err != nil {
		return subtitle.Metadata{}, err
	}
	if err := os.WriteFile(stem+".srt", []byte(renderSRT(segments)), 0o644); err != nil {
		return subtitle.Metadata{}, err
	}
	if err := os.WriteFile(stem+".ass", []byte(renderASS(segments)), 0o644); err != nil {
		return subtitle.Metadata{}, err
	}
	if err := writeLiveSessionEpisodeNFO(aggregatePath, inputs); err != nil {
		return subtitle.Metadata{}, err
	}
	if err := writeLiveSessionShowNFO(aggregatePath, inputs); err != nil {
		return subtitle.Metadata{}, err
	}
	if err := ensureLiveSessionAggregateCover(ctx, aggregatePath, inputs, stem+".jpg"); err != nil {
		return subtitle.Metadata{}, err
	}

	now := time.Now().UTC()
	firstMetadata := inputs[0].Metadata
	recordMeta := copyRecordMeta(firstMetadata.RecordMeta)
	recordMeta["live_session_id"] = manifest.LiveSessionID
	recordMeta["live_session_media_role"] = "aggregate"
	recordMeta["live_session_media_sources"] = segmentSources

	metadata := subtitle.Metadata{
		Status:             subtitle.StatusCompleted,
		Provider:           firstMetadata.Provider,
		Language:           firstMetadata.Language,
		SourcePath:         firstMetadata.SourcePath,
		OutputPath:         aggregatePath,
		ASSPath:            stem + ".ass",
		SRTPath:            stem + ".srt",
		KeepSource:         true,
		SourceExists:       fileExists(firstMetadata.SourcePath),
		ActualProvider:     firstMetadata.ActualProvider,
		ActualModel:        firstMetadata.ActualModel,
		ActualBurnProvider: firstMetadata.ActualBurnProvider,
		RenderPreset:       firstMetadata.RenderPreset,
		RendererStatus:     subtitle.StatusCompleted,
		Segments:           segments,
		RecordMeta:         recordMeta,
		CompletedAt:        &now,
	}
	if err := subtitle.SaveMetadata(stem+".subtitle.json", metadata); err != nil {
		return subtitle.Metadata{}, err
	}
	return metadata, nil
}

func mergedLiveSessionSubtitleSegments(ctx context.Context, ffmpegPath string, inputs []knowledgeSessionPayloadInput, segmentPaths []string) ([]subtitle.Segment, []map[string]any, error) {
	var merged []subtitle.Segment
	var sources []map[string]any
	var offset float64
	for sourceIndex, input := range inputs {
		if input.Metadata == nil {
			return nil, nil, fmt.Errorf("live session input %d has no metadata", sourceIndex)
		}
		var maxEnd float64
		for _, segment := range input.Metadata.Segments {
			text := strings.TrimSpace(segment.Text)
			if text == "" {
				continue
			}
			start, err := parseSubtitleTimestampSeconds(segment.Start)
			if err != nil {
				return nil, nil, err
			}
			end, err := parseSubtitleTimestampSeconds(segment.End)
			if err != nil {
				return nil, nil, err
			}
			if end > maxEnd {
				maxEnd = end
			}
			merged = append(merged, subtitle.Segment{
				Index: len(merged) + 1,
				Start: formatSRTTimestamp(offset + start),
				End:   formatSRTTimestamp(offset + end),
				Text:  text,
			})
		}
		duration := maxEnd
		if sourceIndex < len(segmentPaths) {
			if probed, err := liveSessionMediaProbeDuration(ctx, ffmpegPath, segmentPaths[sourceIndex]); err == nil && probed > maxEnd {
				duration = probed
			}
		}
		sources = append(sources, map[string]any{
			"library_path":  input.LibraryPath,
			"metadata_path": input.MetadataPath,
			"output_path":   input.Metadata.OutputPath,
			"offset":        offset,
			"duration":      duration,
		})
		offset += duration
	}
	return merged, sources, nil
}

func renderSRT(segments []subtitle.Segment) string {
	var builder strings.Builder
	for index, segment := range segments {
		_, _ = fmt.Fprintf(&builder, "%d\n%s --> %s\n%s\n\n", index+1, segment.Start, segment.End, segment.Text)
	}
	return builder.String()
}

func renderASS(segments []subtitle.Segment) string {
	var builder strings.Builder
	builder.WriteString("[Script Info]\nScriptType: v4.00+\n\n")
	builder.WriteString("[V4+ Styles]\n")
	builder.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	builder.WriteString("Style: Default,Noto Sans CJK SC,48,&H00FFFFFF,&H00000000,&H99000000,0,0,0,0,100,100,0,0,1,2,0,2,40,40,40,1\n\n")
	builder.WriteString("[Events]\n")
	builder.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, segment := range segments {
		startSeconds, _ := parseSubtitleTimestampSeconds(segment.Start)
		endSeconds, _ := parseSubtitleTimestampSeconds(segment.End)
		_, _ = fmt.Fprintf(&builder, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", formatASSTimestamp(startSeconds), formatASSTimestamp(endSeconds), escapeASSText(segment.Text))
	}
	return builder.String()
}

func formatSRTTimestamp(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	totalMilliseconds := int(math.Round(seconds * 1000))
	hours := totalMilliseconds / 3_600_000
	totalMilliseconds %= 3_600_000
	minutes := totalMilliseconds / 60_000
	totalMilliseconds %= 60_000
	secs := totalMilliseconds / 1000
	milliseconds := totalMilliseconds % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, secs, milliseconds)
}

func formatASSTimestamp(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	totalCentiseconds := int(math.Round(seconds * 100))
	hours := totalCentiseconds / 360000
	totalCentiseconds %= 360000
	minutes := totalCentiseconds / 6000
	totalCentiseconds %= 6000
	secs := totalCentiseconds / 100
	centiseconds := totalCentiseconds % 100
	return fmt.Sprintf("%d:%02d:%02d.%02d", hours, minutes, secs, centiseconds)
}

func escapeASSText(text string) string {
	text = strings.ReplaceAll(text, "\n", "\\N")
	text = strings.ReplaceAll(text, "\r", "")
	return strings.ReplaceAll(text, "{", "\\{")
}

func writeLiveSessionEpisodeNFO(aggregatePath string, inputs []knowledgeSessionPayloadInput) error {
	episode, ok := parseLiveSessionMediaEpisode(aggregatePath)
	if !ok {
		return nil
	}
	recordedAt := parseEpisodeDate(episode.Date)
	platform := aggregatePlatform(inputs)
	title := fmt.Sprintf("%s - %s", episode.Date, episode.Title)
	plot := fmt.Sprintf("%s | 主播: %s | 标题: %s | 同场直播聚合成品", platform, episode.Alias, episode.Title)
	content := strings.Join([]string{
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`,
		"<episodedetails>",
		fmt.Sprintf("  <title>%s</title>", xmlEscape(title)),
		fmt.Sprintf("  <showtitle>%s</showtitle>", xmlEscape(episode.Alias)),
		fmt.Sprintf("  <sorttitle>%s</sorttitle>", xmlEscape(filepath.Base(aggregatePath))),
		"  <season>1</season>",
		fmt.Sprintf("  <episode>%d</episode>", episode.Episode),
		fmt.Sprintf("  <plot>%s</plot>", xmlEscape(plot)),
		fmt.Sprintf("  <studio>%s</studio>", xmlEscape(platform)),
		"  <genre>直播录屏</genre>",
		"  <tag>直播录屏</tag>",
		fmt.Sprintf("  <aired>%s</aired>", episode.Date),
		fmt.Sprintf("  <dateadded>%s</dateadded>", recordedAt.Format("2006-01-02 15:04:05")),
		"</episodedetails>",
		"",
	}, "\n")
	return os.WriteFile(strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))+".nfo", []byte(content), 0o644)
}

func writeLiveSessionShowNFO(aggregatePath string, inputs []knowledgeSessionPayloadInput) error {
	episode, ok := parseLiveSessionMediaEpisode(aggregatePath)
	if !ok {
		return nil
	}
	showDir := filepath.Dir(filepath.Dir(aggregatePath))
	recordedAt := parseEpisodeDate(episode.Date)
	platform := aggregatePlatform(inputs)
	plot := fmt.Sprintf("%s 的直播录屏剧集库。来源平台: %s。", episode.Alias, platform)
	content := strings.Join([]string{
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`,
		"<tvshow>",
		fmt.Sprintf("  <title>%s</title>", xmlEscape(episode.Alias)),
		fmt.Sprintf("  <showtitle>%s</showtitle>", xmlEscape(episode.Alias)),
		fmt.Sprintf("  <sorttitle>%s</sorttitle>", xmlEscape(episode.Alias)),
		fmt.Sprintf("  <year>%d</year>", recordedAt.Year()),
		fmt.Sprintf("  <plot>%s</plot>", xmlEscape(plot)),
		fmt.Sprintf("  <studio>%s</studio>", xmlEscape(platform)),
		"  <genre>直播录屏</genre>",
		"  <tag>直播录屏</tag>",
		fmt.Sprintf("  <premiered>%s</premiered>", episode.Date),
		fmt.Sprintf("  <dateadded>%s</dateadded>", recordedAt.Format("2006-01-02 15:04:05")),
		"</tvshow>",
		"",
	}, "\n")
	return os.WriteFile(filepath.Join(showDir, "tvshow.nfo"), []byte(content), 0o644)
}

func parseEpisodeDate(value string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Now()
	}
	return parsed
}

func aggregatePlatform(inputs []knowledgeSessionPayloadInput) string {
	if len(inputs) == 0 || inputs[0].Metadata == nil || inputs[0].Metadata.RecordMeta == nil {
		return "bililive-go"
	}
	if value, ok := inputs[0].Metadata.RecordMeta["platform"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return "bililive-go"
}

func ensureLiveSessionAggregateCover(ctx context.Context, aggregatePath string, inputs []knowledgeSessionPayloadInput, targetPath string) error {
	if copyFirstCover(inputs, targetPath) {
		return nil
	}
	if _, err := liveSessionMediaExtractCover(ctx, aggregatePath, targetPath); err == nil && nonEmptyFile(targetPath) {
		return nil
	}
	for _, input := range inputs {
		for _, candidate := range liveSessionCoverSourceCandidates(input) {
			if candidate == "" || candidate == aggregatePath || !fileExists(candidate) {
				continue
			}
			if _, err := liveSessionMediaExtractCover(ctx, candidate, targetPath); err == nil && nonEmptyFile(targetPath) {
				return nil
			}
		}
	}
	return fmt.Errorf("live session aggregate cover could not be created: %s", targetPath)
}

func liveSessionCoverSourceCandidates(input knowledgeSessionPayloadInput) []string {
	var candidates []string
	if input.Metadata != nil {
		candidates = append(candidates, input.Metadata.OutputPath, input.Metadata.SourcePath)
	}
	candidates = append(candidates, input.LibraryPath)
	return candidates
}

func copyFirstCover(inputs []knowledgeSessionPayloadInput, targetPath string) bool {
	for _, input := range inputs {
		stem := strings.TrimSuffix(input.LibraryPath, filepath.Ext(input.LibraryPath))
		coverPath := stem + ".jpg"
		content, err := os.ReadFile(coverPath)
		if err != nil || len(content) == 0 {
			continue
		}
		_ = os.WriteFile(targetPath, content, 0o644)
		return true
	}
	return false
}

func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func hideLiveSessionSegmentVideos(libraryRoot string, manifest *knowledgeSessionManifest, inputs []knowledgeSessionPayloadInput, aggregatePath string) error {
	if manifest == nil {
		return nil
	}
	for _, input := range inputs {
		if input.Metadata == nil {
			continue
		}
		segmentPath := input.Metadata.OutputPath
		if segmentPath == "" {
			segmentPath = input.LibraryPath
		}
		if segmentPath == "" || sameCleanPath(segmentPath, aggregatePath) || !fileExists(segmentPath) {
			continue
		}
		hiddenPath, err := hiddenLiveSessionSegmentPath(libraryRoot, manifest, input.LibraryPath, segmentPath)
		if err != nil {
			return err
		}
		if !sameCleanPath(segmentPath, hiddenPath) {
			if err := os.MkdirAll(filepath.Dir(hiddenPath), 0o755); err != nil {
				return err
			}
			targetPath := hiddenPath
			if fileExists(targetPath) {
				targetPath = uniqueLiveSessionHiddenPath(targetPath)
			}
			if err := os.Rename(segmentPath, targetPath); err != nil {
				return err
			}
			hiddenPath = targetPath
		}

		metadata := *input.Metadata
		metadata.OutputPath = hiddenPath
		if metadata.RecordMeta == nil {
			metadata.RecordMeta = map[string]any{}
		}
		metadata.RecordMeta["live_session_media_role"] = "segment"
		metadata.RecordMeta["live_session_media_aggregate_path"] = aggregatePath
		metadata.RecordMeta["live_session_segment_hidden_path"] = hiddenPath
		if err := subtitle.SaveMetadata(input.MetadataPath, metadata); err != nil {
			return err
		}
	}
	return nil
}

func hiddenLiveSessionSegmentPath(libraryRoot string, manifest *knowledgeSessionManifest, libraryPath string, segmentPath string) (string, error) {
	seasonDir := filepath.Dir(libraryPath)
	if rel, err := filepath.Rel(libraryRoot, seasonDir); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("library path is outside library root: %s", libraryPath)
	}
	hash := sha256.Sum256([]byte(manifest.SourceID + "\n" + manifest.LiveSessionID))
	return filepath.Join(seasonDir, liveSessionSegmentsDirName, hex.EncodeToString(hash[:])[:16], filepath.Base(segmentPath)), nil
}

func uniqueLiveSessionHiddenPath(path string) string {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	for index := 1; ; index++ {
		candidate := fmt.Sprintf("%s.%d%s", stem, index, ext)
		if !fileExists(candidate) {
			return candidate
		}
	}
}

func sameCleanPath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func copyRecordMeta(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+4)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func buildKnowledgeLiveSessionAggregateIngestPayload(
	ctx *pipeline.PipelineContext,
	cfg configs.SubtitleKnowledgeSyncConfig,
	libraryRoot string,
	aggregate *liveSessionMediaAggregate,
) (knowledgeIngestPayload, error) {
	if aggregate == nil {
		return knowledgeIngestPayload{}, fmt.Errorf("knowledge live session aggregate payload requires aggregate media")
	}
	segments, err := buildKnowledgeSegments(aggregate.Metadata.Segments)
	if err != nil {
		return knowledgeIngestPayload{}, err
	}
	if len(segments) == 0 {
		return knowledgeIngestPayload{}, fmt.Errorf("knowledge live session aggregate has no transcript segments")
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
		return knowledgeIngestPayload{}, fmt.Errorf("knowledge live session aggregate requires live_session_id")
	}

	sourceID := "live-session:" + sessionID
	format, link, screenshot := cfg.ResolveNoteOptions()
	return knowledgeIngestPayload{
		SourceID:           sourceID,
		SourceType:         "bililive-go",
		TaskID:             knowledgeTaskID(ctx),
		LiveSessionID:      sessionID,
		Host:               host,
		Title:              strings.TrimSuffix(filepath.Base(aggregate.LibraryPath), filepath.Ext(aggregate.LibraryPath)),
		Topic:              topic,
		SourceVideoPath:    aggregate.LibraryPath,
		SubtitlePath:       aggregate.Metadata.SRTPath,
		Language:           aggregate.Metadata.Language,
		ContentHash:        knowledgeContentHash(sourceID, aggregate.Metadata.Language, aggregate.Metadata.Segments),
		Segments:           segments,
		GenerateNote:       cfg.GenerateNote,
		NonBlocking:        cfg.NonBlocking,
		ModelName:          cfg.GetModelName(),
		ProviderID:         cfg.GetProviderID(),
		Format:             format,
		Link:               link,
		Screenshot:         screenshot,
		Style:              cfg.Style,
		Extras:             cfg.Extras,
		VideoUnderstanding: cfg.VideoUnderstanding,
		VideoInterval:      cfg.VideoInterval,
		GridSize:           append([]int(nil), cfg.GridSize...),
	}, nil
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}
