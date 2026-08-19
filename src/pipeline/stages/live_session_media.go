package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
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
var liveSessionMediaRename = os.Rename
var liveSessionMetadataRename = os.Rename
var liveSessionSidecarRename = os.Rename
var liveSessionSidecarRemove = os.Remove
var liveSessionSyncDirectory = syncLiveSessionDirectory
var liveSessionLinkFile = os.Link
var liveSessionRemoveFile = os.Remove

var libraryMediaEpisodePattern = regexp.MustCompile(`^(.+?)\.S(\d{2})E(\d+)(?:-S\d{2}E\d+)?\.(\d{4}-\d{2}-\d{2}) - (.+)\.[^.]+$`)
var normalizedLiveSessionSourcePattern = regexp.MustCompile(` - (\d{4}-\d{2}-\d{2} \d{2}-\d{2}-\d{2}) - `)
var mediaLibraryLocation = time.FixedZone("UTC+8", 8*60*60)

const (
	chronologicalEpisodeIdentityBase int64 = 8
	chronologicalEpisodeEpochUnix    int64 = 1_577_836_800 // 2020-01-01T00:00:00Z
	chronologicalEpisodeIdentityMin  int64 = 1_000_000_000_000
)

type liveSessionMediaEpisode struct {
	Alias     string
	Season    string
	Episode   int64
	Date      string
	Title     string
	SeasonDir string
}

type liveSessionStagedFile struct {
	staged           string
	target           string
	preserveNonEmpty bool
}

type liveSessionAppliedFile struct {
	target   string
	backup   string
	revision liveSessionFileRevision
}

type liveSessionFileRevision struct {
	info    os.FileInfo
	size    int64
	modTime time.Time
	digest  [sha256.Size]byte
	hasHash bool
}

type liveSessionSidecarStage struct {
	root          string
	before        []liveSessionStagedFile
	metadata      liveSessionStagedFile
	applied       []liveSessionAppliedFile
	videoRevision *liveSessionFileRevision
}

type liveSessionSegmentMove struct {
	source           string
	target           string
	metadataPath     string
	stagedMetadata   string
	backupMetadata   string
	videoRevision    liveSessionFileRevision
	metadataBefore   liveSessionFileRevision
	metadataRevision liveSessionFileRevision
	targetLinked     bool
	targetCreated    bool
	sourceRemoved    bool
	metadataPromoted bool
}

type liveSessionSegmentTransaction struct {
	root        string
	moves       []*liveSessionSegmentMove
	outputPaths map[string]string
}

type liveSessionSegmentJournalEntry struct {
	Source         string `json:"source"`
	Target         string `json:"target"`
	MetadataPath   string `json:"metadata_path"`
	StagedMetadata string `json:"staged_metadata"`
	BackupMetadata string `json:"backup_metadata"`
}

func publishLiveSessionMediaAggregate(ctx context.Context, ffmpegPath string, libraryRoot string, manifest *knowledgeSessionManifest) (*liveSessionMediaAggregate, error) {
	if manifest == nil || len(manifest.Sources) < 2 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	absoluteLibraryRoot, _, err := liveSessionSegmentRoots(libraryRoot)
	if err != nil {
		return nil, err
	}

	inputs, err := knowledgeSessionInputsFromManifest(*manifest)
	if err != nil {
		return nil, err
	}
	if len(inputs) < 2 {
		return nil, nil
	}

	if err := sortLiveSessionInputsByRecordedAt(inputs); err != nil {
		return nil, err
	}

	desiredAggregatePath, err := liveSessionAggregatePath(inputs)
	if err != nil {
		return nil, err
	}
	desiredAggregatePath, err = canonicalLiveSessionAggregatePath(desiredAggregatePath)
	if err != nil {
		return nil, err
	}
	aggregatePath := strings.TrimSpace(manifest.AggregatePath)
	oldAggregatePath := strings.TrimSpace(manifest.PreviousAggregatePath)
	if oldAggregatePath == "" {
		oldAggregatePath = aggregatePath
	}
	manifestPath := knowledgeSessionManifestPath(absoluteLibraryRoot, manifest.LiveSessionID)
	identityChanged := aggregatePath != "" && !sameCleanPath(aggregatePath, desiredAggregatePath)
	if manifest.PreviousAggregatePath != "" {
		identityChanged = true
	}
	if aggregatePath == "" || !sameCleanPath(aggregatePath, desiredAggregatePath) {
		manifest.PreviousAggregatePath = oldAggregatePath
		aggregatePath = desiredAggregatePath
		manifest.AggregatePath = aggregatePath
		if err := saveKnowledgeSessionManifest(manifestPath, *manifest); err != nil {
			manifest.AggregatePath = oldAggregatePath
			return nil, fmt.Errorf("persist live session aggregate identity: %w", err)
		}
	} else if err := validateLiveSessionAggregatePath(absoluteLibraryRoot, aggregatePath, inputs); err != nil {
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
	if manifest.PreviousAggregatePath != "" &&
		!sameCleanPath(manifest.PreviousAggregatePath, aggregatePath) &&
		fileExists(manifest.PreviousAggregatePath) {
		previousStem := strings.TrimSuffix(manifest.PreviousAggregatePath, filepath.Ext(manifest.PreviousAggregatePath))
		previousMetadata, previousMetadataExists := loadCompletedSubtitleMetadata(
			previousStem+".subtitle.json",
			manifest.PreviousAggregatePath,
			previousStem+".srt",
			previousStem+".ass",
		)
		if !previousMetadataExists || previousMetadata.RecordMeta["live_session_media_role"] != "aggregate" {
			return nil, fmt.Errorf("load previous live session aggregate metadata: %s", manifest.PreviousAggregatePath)
		}
		if err := restoreLiveSessionSegmentPathsFromAggregateMetadata(absoluteLibraryRoot, manifest, inputs, previousMetadata); err != nil {
			return nil, fmt.Errorf("restore previous live session aggregate sources: %w", err)
		}
	}
	if metadataExists {
		if err := restoreLiveSessionSegmentPathsFromAggregateMetadata(absoluteLibraryRoot, manifest, inputs, aggregateMetadata); err != nil {
			return nil, fmt.Errorf("restore live session aggregate sources: %w", err)
		}
	}
	if metadataExists &&
		aggregateMetadata.RecordMeta["live_session_media_content_hash"] != knowledgeSessionManifestContentHash(*manifest) {
		metadataExists = false
	}
	if metadataExists && manifest.PreviousAggregatePath != "" {
		if fileExists(manifest.PreviousAggregatePath) {
			if err := archiveDistinctLiveSessionAggregateVersion(absoluteLibraryRoot, manifest, manifest.PreviousAggregatePath, aggregatePath); err != nil {
				return nil, err
			}
		}
		manifest.PreviousAggregatePath = ""
		if err := saveKnowledgeSessionManifest(manifestPath, *manifest); err != nil {
			return nil, fmt.Errorf("complete live session aggregate identity recovery: %w", err)
		}
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

		var sidecarStage *liveSessionSidecarStage
		aggregateMetadata, sidecarStage, err = prepareLiveSessionAggregateSidecars(ctx, ffmpegPath, absoluteLibraryRoot, aggregatePath, tmpPath, manifest, inputs, segmentPaths)
		if err != nil {
			_ = os.Remove(tmpPath)
			return nil, err
		}
		if err := sidecarStage.promoteBeforeVideo(); err != nil {
			_ = os.Remove(tmpPath)
			return nil, sidecarStage.recover(err)
		}

		videoBackup, err := sidecarStage.backupVideo(aggregatePath)
		if err != nil {
			_ = os.Remove(tmpPath)
			return nil, sidecarStage.recover(err)
		}
		videoRevision, err := captureLiveSessionFileRevision(tmpPath, false)
		if err != nil {
			_ = os.Remove(tmpPath)
			return nil, sidecarStage.recover(err)
		}
		if err := liveSessionMediaRename(tmpPath, aggregatePath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, sidecarStage.recover(err)
		}
		sidecarStage.videoRevision = &videoRevision
		segmentTransaction, err := hideLiveSessionSegmentVideos(absoluteLibraryRoot, manifest, inputs, aggregatePath)
		if err != nil {
			return nil, sidecarStage.recoverAfterVideo(err, aggregatePath, videoBackup)
		}
		if segmentTransaction != nil {
			updateLiveSessionAggregateSourcePaths(&aggregateMetadata, segmentTransaction.outputPaths)
			if err := subtitle.SaveMetadata(sidecarStage.metadata.staged, aggregateMetadata); err != nil {
				return nil, recoverLiveSessionAggregateWithSegments(err, segmentTransaction, sidecarStage, aggregatePath, videoBackup)
			}
		}
		if err := sidecarStage.promoteMetadata(); err != nil {
			return nil, recoverLiveSessionAggregateWithSegments(err, segmentTransaction, sidecarStage, aggregatePath, videoBackup)
		}
		if err := sidecarStage.archivePreviousAggregateVersion(absoluteLibraryRoot, manifest, aggregatePath, videoBackup); err != nil {
			return nil, recoverLiveSessionAggregateWithSegments(err, segmentTransaction, sidecarStage, aggregatePath, videoBackup)
		}
		if identityChanged {
			if err := archiveDistinctLiveSessionAggregateVersion(absoluteLibraryRoot, manifest, oldAggregatePath, aggregatePath); err != nil {
				return nil, recoverLiveSessionAggregateWithSegments(err, segmentTransaction, sidecarStage, aggregatePath, videoBackup)
			}
		}
		manifest.PreviousAggregatePath = ""
		if err := saveKnowledgeSessionManifest(manifestPath, *manifest); err != nil {
			return nil, recoverLiveSessionAggregateWithSegments(fmt.Errorf("commit live session aggregate identity: %w", err), segmentTransaction, sidecarStage, aggregatePath, videoBackup)
		}
		if segmentTransaction != nil {
			segmentTransaction.commit()
		}
		sidecarStage.commit()
		return &liveSessionMediaAggregate{
			LibraryPath:  aggregatePath,
			MetadataPath: aggregateMetadataPath,
			Metadata:     aggregateMetadata,
		}, nil
	}

	segmentTransaction, err := hideLiveSessionSegmentVideos(absoluteLibraryRoot, manifest, inputs, aggregatePath)
	if err != nil {
		return nil, err
	}
	if segmentTransaction != nil {
		updateLiveSessionAggregateSourcePaths(&aggregateMetadata, segmentTransaction.outputPaths)
		committed, err := saveLiveSessionMetadataAtomically(aggregateMetadataPath, aggregateMetadata)
		if err != nil {
			if committed {
				if segmentTransaction.root != "" {
					return nil, fmt.Errorf("%w; aggregate metadata committed with hidden segment paths; preserved transaction: %s", err, segmentTransaction.root)
				}
				return nil, err
			}
			return nil, segmentTransaction.recover(err)
		}
		segmentTransaction.commit()
	}

	return &liveSessionMediaAggregate{
		LibraryPath:  aggregatePath,
		MetadataPath: aggregateMetadataPath,
		Metadata:     aggregateMetadata,
	}, nil
}

func archiveDistinctLiveSessionAggregateVersion(
	libraryRoot string,
	manifest *knowledgeSessionManifest,
	oldAggregatePath string,
	newAggregatePath string,
) error {
	if strings.TrimSpace(oldAggregatePath) == "" || sameCleanPath(oldAggregatePath, newAggregatePath) {
		return nil
	}
	if !fileExists(oldAggregatePath) {
		return nil
	}

	oldStem := strings.TrimSuffix(oldAggregatePath, filepath.Ext(oldAggregatePath))
	oldMetadataPath := oldStem + ".subtitle.json"
	metadataContent, err := os.ReadFile(oldMetadataPath)
	if err != nil {
		return fmt.Errorf("read previous aggregate metadata: %w", err)
	}
	versionHash := sha256.Sum256(metadataContent)
	hiddenPath, err := hiddenLiveSessionSegmentPath(libraryRoot, manifest, oldAggregatePath, oldAggregatePath)
	if err != nil {
		return err
	}
	archiveDir := filepath.Join(
		filepath.Dir(hiddenPath),
		".aggregate_versions",
		hex.EncodeToString(versionHash[:])[:16],
	)
	archiveParent := filepath.Dir(archiveDir)
	if err := os.MkdirAll(archiveParent, 0o700); err != nil {
		return err
	}
	if fileExists(archiveDir) {
		archivedVideoPath := filepath.Join(archiveDir, filepath.Base(oldAggregatePath))
		archivedMetadataPath := strings.TrimSuffix(archivedVideoPath, filepath.Ext(archivedVideoPath)) + ".subtitle.json"
		metadata, metadataErr := subtitle.LoadMetadata(archivedMetadataPath)
		supersededBy, _ := metadata.RecordMeta["live_session_media_superseded_by"].(string)
		if metadataErr == nil && sameCleanPath(supersededBy, newAggregatePath) {
			return nil
		}
		return fmt.Errorf("previous aggregate archive conflict: %s", archiveDir)
	}
	if err := os.Mkdir(archiveDir, 0o700); err != nil {
		return err
	}

	type aggregateMove struct {
		source string
		target string
		video  bool
	}
	moves := make([]aggregateMove, 0, 6)
	for _, extension := range []string{".srt", ".ass", ".nfo", ".jpg", ".transcript.json", ".subtitle.json", filepath.Ext(oldAggregatePath)} {
		source := oldStem + extension
		if extension == filepath.Ext(oldAggregatePath) {
			source = oldAggregatePath
		}
		if !fileExists(source) {
			continue
		}
		moves = append(moves, aggregateMove{
			source: source,
			target: filepath.Join(archiveDir, filepath.Base(source)),
			video:  sameCleanPath(source, oldAggregatePath),
		})
	}

	moved := 0
	metadataBackup := ""
	rollback := func(primary error) error {
		var recoveryErr error
		archivedMetadataPath := filepath.Join(archiveDir, filepath.Base(oldMetadataPath))
		if metadataBackup != "" && fileExists(metadataBackup) {
			if fileExists(archivedMetadataPath) {
				if err := os.Remove(archivedMetadataPath); err != nil && recoveryErr == nil {
					recoveryErr = err
				}
			}
			if recoveryErr == nil {
				if err := os.Rename(metadataBackup, archivedMetadataPath); err != nil {
					recoveryErr = err
				}
			}
		}
		for index := moved - 1; index >= 0 && recoveryErr == nil; index-- {
			move := moves[index]
			if err := os.Rename(move.target, move.source); err != nil {
				recoveryErr = err
			}
		}
		if recoveryErr == nil {
			_ = os.RemoveAll(archiveDir)
			_ = syncLiveSessionDirectory(archiveParent)
			return primary
		}
		return fmt.Errorf("%w; previous aggregate recovery failed: %v; preserved archive: %s", primary, recoveryErr, archiveDir)
	}

	for _, move := range moves {
		rename := liveSessionSidecarRename
		if move.video {
			rename = liveSessionMediaRename
		}
		if err := rename(move.source, move.target); err != nil {
			return rollback(err)
		}
		moved++
	}
	if err := syncLiveSessionDirectory(filepath.Dir(oldAggregatePath)); err != nil {
		return rollback(err)
	}

	archivedVideoPath := filepath.Join(archiveDir, filepath.Base(oldAggregatePath))
	archivedStem := strings.TrimSuffix(archivedVideoPath, filepath.Ext(archivedVideoPath))
	archivedMetadataPath := archivedStem + ".subtitle.json"
	metadata, err := subtitle.LoadMetadata(archivedMetadataPath)
	if err != nil {
		return rollback(err)
	}
	metadata.OutputPath = archivedVideoPath
	if fileExists(archivedStem + ".ass") {
		metadata.ASSPath = archivedStem + ".ass"
	}
	if fileExists(archivedStem + ".srt") {
		metadata.SRTPath = archivedStem + ".srt"
	}
	metadata.RecordMeta = copyRecordMeta(metadata.RecordMeta)
	metadata.RecordMeta["live_session_media_role"] = "aggregate_version"
	metadata.RecordMeta["live_session_media_superseded_by"] = newAggregatePath
	stagedMetadataPath := archivedMetadataPath + ".new"
	if err := subtitle.SaveMetadata(stagedMetadataPath, metadata); err != nil {
		return rollback(err)
	}
	metadataBackup = archivedMetadataPath + ".original"
	if err := os.Rename(archivedMetadataPath, metadataBackup); err != nil {
		_ = os.Remove(stagedMetadataPath)
		return rollback(err)
	}
	if err := os.Rename(stagedMetadataPath, archivedMetadataPath); err != nil {
		return rollback(err)
	}
	if err := syncLiveSessionDirectory(archiveDir); err != nil {
		return rollback(err)
	}
	if err := syncLiveSessionDirectory(archiveParent); err != nil {
		return rollback(err)
	}
	_ = os.Remove(metadataBackup)
	metadataBackup = ""
	_ = syncLiveSessionDirectory(archiveDir)
	return nil
}

func liveSessionAggregatePath(inputs []knowledgeSessionPayloadInput) (string, error) {
	if len(inputs) == 0 {
		return "", fmt.Errorf("live session media aggregate has no inputs")
	}
	if err := sortLiveSessionInputsByRecordedAt(inputs); err != nil {
		return "", err
	}
	target, ok := parseLiveSessionMediaEpisode(inputs[0].LibraryPath)
	if !ok {
		return "", fmt.Errorf("cannot parse library episode path: %s", inputs[0].LibraryPath)
	}
	recordedAt, ok := liveSessionInputRecordedAt(inputs[0])
	if !ok {
		return "", fmt.Errorf("live session aggregate has no reliable recording start time: %s", inputs[0].LibraryPath)
	}
	baseIdentity := chronologicalLiveSessionEpisodeIdentity(recordedAt)
	if baseIdentity <= 0 {
		return "", fmt.Errorf("live session aggregate recording time exceeds safe episode identity range: %s", recordedAt.Format(time.RFC3339Nano))
	}
	existingIdentity := target.Episode
	target.Episode = baseIdentity
	if existingIdentity >= baseIdentity && existingIdentity < baseIdentity+chronologicalEpisodeIdentityBase {
		target.Episode = existingIdentity
	}
	target.Date = recordedAt.Format("2006-01-02")
	identityAlias := target.Alias
	identitySeason := target.Season
	identitySeasonDir := target.SeasonDir
	for index, input := range inputs[1:] {
		parsed, ok := parseLiveSessionMediaEpisode(input.LibraryPath)
		if !ok {
			return "", fmt.Errorf("cannot parse library episode path at input %d: %s", index+1, input.LibraryPath)
		}
		if parsed.Alias != identityAlias ||
			parsed.Season != identitySeason ||
			!sameCleanPath(parsed.SeasonDir, identitySeasonDir) {
			return "", fmt.Errorf("live session input identity mismatch: %s", input.LibraryPath)
		}
	}

	episodeText := fmt.Sprintf("S%sE%04d", target.Season, target.Episode)
	name := fmt.Sprintf("%s.%s.%s - %s [同场聚合].mp4", target.Alias, episodeText, target.Date, target.Title)
	return filepath.Join(target.SeasonDir, name), nil
}

func chronologicalLiveSessionEpisodeIdentity(recordedAt time.Time) int64 {
	microseconds := recordedAt.UnixMicro() - chronologicalEpisodeEpochUnix*1_000_000
	if recordedAt.IsZero() || microseconds <= 0 || microseconds > math.MaxInt64/chronologicalEpisodeIdentityBase {
		return -1
	}
	identity := microseconds * chronologicalEpisodeIdentityBase
	if identity > 9_007_199_254_740_991 {
		return -1
	}
	return identity
}

func liveSessionInputRecordedAt(input knowledgeSessionPayloadInput) (time.Time, bool) {
	if input.Metadata != nil && input.Metadata.RecordMeta != nil {
		switch value := input.Metadata.RecordMeta["start_time"].(type) {
		case string:
			if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil && !parsed.IsZero() {
				return parsed, true
			}
		case time.Time:
			if !value.IsZero() {
				return value, true
			}
		}
	}
	if input.Metadata != nil {
		for _, path := range []string{input.Metadata.SourcePath, input.Metadata.OutputPath} {
			stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			match := normalizedLiveSessionSourcePattern.FindStringSubmatch(stem)
			if match == nil {
				continue
			}
			if parsed, err := time.ParseInLocation("2006-01-02 15-04-05", match[1], mediaLibraryLocation); err == nil {
				return parsed, true
			}
		}
	}
	episode, ok := parseLiveSessionMediaEpisode(input.LibraryPath)
	if !ok {
		return time.Time{}, false
	}
	if episode.Episode >= chronologicalEpisodeIdentityMin {
		microseconds := chronologicalEpisodeEpochUnix*1_000_000 + episode.Episode/chronologicalEpisodeIdentityBase
		return time.UnixMicro(microseconds), true
	}
	return time.Time{}, false
}

func sortLiveSessionInputsByRecordedAt(inputs []knowledgeSessionPayloadInput) error {
	recordedAt := make(map[string]time.Time, len(inputs))
	for index, input := range inputs {
		value, ok := liveSessionInputRecordedAt(input)
		if !ok {
			if input.Metadata != nil || input.MetadataPath != "" {
				return fmt.Errorf("live session input %d has no reliable recording start time: %s", index, input.LibraryPath)
			}
			episode, parsed := parseLiveSessionMediaEpisode(input.LibraryPath)
			if !parsed {
				return fmt.Errorf("cannot parse library episode path: %s", input.LibraryPath)
			}
			if episode.Episode >= chronologicalEpisodeIdentityMin {
				microseconds := chronologicalEpisodeEpochUnix*1_000_000 + episode.Episode/chronologicalEpisodeIdentityBase
				value = time.UnixMicro(microseconds)
			} else {
				// 旧四位集号只用于兼容没有 sidecar 的既有调用；它保持原集号顺序，
				// 不把路径或 mtime 伪装成真实录制时间。
				value = time.UnixMicro(episode.Episode)
			}
		}
		recordedAt[input.MetadataPath+"\x00"+input.LibraryPath] = value
	}
	sort.SliceStable(inputs, func(i, j int) bool {
		left := recordedAt[inputs[i].MetadataPath+"\x00"+inputs[i].LibraryPath]
		right := recordedAt[inputs[j].MetadataPath+"\x00"+inputs[j].LibraryPath]
		if !left.Equal(right) {
			return left.Before(right)
		}
		return filepath.ToSlash(inputs[i].LibraryPath) < filepath.ToSlash(inputs[j].LibraryPath)
	})
	return nil
}

func validateLiveSessionAggregatePath(libraryRoot, aggregatePath string, inputs []knowledgeSessionPayloadInput) error {
	if len(inputs) == 0 {
		return errors.New("live session media aggregate has no inputs")
	}
	aggregate, ok := parseLiveSessionMediaEpisode(aggregatePath)
	if !ok {
		return fmt.Errorf("cannot parse persisted live session aggregate path: %s", aggregatePath)
	}
	first, ok := parseLiveSessionMediaEpisode(inputs[0].LibraryPath)
	if !ok {
		return fmt.Errorf("cannot parse library episode path: %s", inputs[0].LibraryPath)
	}
	aggregateSeasonDir, err := filepath.EvalSymlinks(aggregate.SeasonDir)
	if err != nil {
		return err
	}
	inputSeasonDir, err := filepath.EvalSymlinks(first.SeasonDir)
	if err != nil {
		return err
	}
	if aggregate.Alias != first.Alias ||
		aggregate.Season != first.Season ||
		!sameCleanPath(aggregateSeasonDir, inputSeasonDir) {
		return fmt.Errorf("persisted live session aggregate identity mismatch: %s", aggregatePath)
	}
	resolvedAggregatePath := filepath.Join(aggregateSeasonDir, filepath.Base(aggregatePath))
	relative, err := filepath.Rel(libraryRoot, resolvedAggregatePath)
	if err != nil ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return fmt.Errorf("persisted live session aggregate is outside library root: %s", aggregatePath)
	}
	_, err = liveSessionAggregatePath(inputs)
	return err
}

func canonicalLiveSessionAggregatePath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolutePath))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolutePath)), nil
}

func parseLiveSessionMediaEpisode(path string) (liveSessionMediaEpisode, bool) {
	match := libraryMediaEpisodePattern.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return liveSessionMediaEpisode{}, false
	}
	episode, err := strconv.ParseInt(match[3], 10, 64)
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
	for index := range inputs {
		path, err := resolveLiveSessionSegmentVideoPath(inputs[index])
		if err != nil {
			return nil, err
		}
		if inputs[index].Metadata != nil {
			inputs[index].Metadata.OutputPath = path
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func resolveLiveSessionSegmentVideoPath(input knowledgeSessionPayloadInput) (string, error) {
	candidates := make([]string, 0, 3)
	if input.Metadata != nil {
		if input.Metadata.RecordMeta != nil {
			if hiddenPath, ok := input.Metadata.RecordMeta["live_session_segment_hidden_path"].(string); ok {
				candidates = append(candidates, hiddenPath)
			}
		}
		candidates = append(candidates, input.Metadata.OutputPath)
	}
	candidates = append(candidates, input.LibraryPath)

	missingPath := ""
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		cleaned := filepath.Clean(candidate)
		if _, exists := seen[cleaned]; exists {
			continue
		}
		seen[cleaned] = struct{}{}
		if missingPath == "" {
			missingPath = candidate
		}
		if fileExists(candidate) {
			return candidate, nil
		}
	}

	matches := make(map[string]struct{})
	recordedAt, hasRecordedAt := liveSessionInputRecordedAt(input)
	expectedBaseIdentity := chronologicalLiveSessionEpisodeIdentity(recordedAt)
	for candidate := range seen {
		legacy, ok := parseLiveSessionMediaEpisode(candidate)
		if !ok || legacy.Episode >= chronologicalEpisodeIdentityMin || !hasRecordedAt || expectedBaseIdentity <= 0 {
			continue
		}
		entries, err := os.ReadDir(filepath.Dir(candidate))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), filepath.Ext(candidate)) {
				continue
			}
			currentPath := filepath.Join(filepath.Dir(candidate), entry.Name())
			current, parsed := parseLiveSessionMediaEpisode(currentPath)
			if !parsed || current.Episode < expectedBaseIdentity || current.Episode >= expectedBaseIdentity+chronologicalEpisodeIdentityBase {
				continue
			}
			if current.Alias == legacy.Alias && current.Season == legacy.Season && current.Date == legacy.Date && current.Title == legacy.Title {
				matches[filepath.Clean(currentPath)] = struct{}{}
			}
		}
	}
	if len(matches) == 1 {
		for path := range matches {
			return path, nil
		}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("live session legacy segment path is ambiguous: %s", missingPath)
	}
	return "", fmt.Errorf("live session segment video missing: %s", missingPath)
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

func prepareLiveSessionAggregateSidecars(
	ctx context.Context,
	ffmpegPath string,
	libraryRoot string,
	aggregatePath string,
	aggregateVideoSourcePath string,
	manifest *knowledgeSessionManifest,
	inputs []knowledgeSessionPayloadInput,
	segmentPaths []string,
) (subtitle.Metadata, *liveSessionSidecarStage, error) {
	stage, err := newLiveSessionSidecarStage(libraryRoot)
	if err != nil {
		return subtitle.Metadata{}, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			stage.cleanup()
		}
	}()

	stagedAggregatePath := filepath.Join(stage.root, "Season 01", filepath.Base(aggregatePath))
	if err := os.MkdirAll(filepath.Dir(stagedAggregatePath), 0o755); err != nil {
		return subtitle.Metadata{}, nil, err
	}
	stagedStem := strings.TrimSuffix(stagedAggregatePath, filepath.Ext(stagedAggregatePath))
	targetStem := strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))
	segments, segmentSources, err := mergedLiveSessionSubtitleSegments(ctx, ffmpegPath, inputs, segmentPaths)
	if err != nil {
		return subtitle.Metadata{}, nil, err
	}
	if err := os.WriteFile(stagedStem+".srt", []byte(renderSRT(segments)), 0o644); err != nil {
		return subtitle.Metadata{}, nil, err
	}
	if err := os.WriteFile(stagedStem+".ass", []byte(renderASS(segments)), 0o644); err != nil {
		return subtitle.Metadata{}, nil, err
	}
	if err := writeLiveSessionEpisodeNFO(stagedAggregatePath, inputs); err != nil {
		return subtitle.Metadata{}, nil, err
	}
	if err := writeLiveSessionShowNFO(stagedAggregatePath, inputs); err != nil {
		return subtitle.Metadata{}, nil, err
	}
	if err := preserveExistingLiveSessionShowNFO(aggregatePath, filepath.Join(stage.root, "tvshow.nfo")); err != nil {
		return subtitle.Metadata{}, nil, err
	}
	if err := ensureLiveSessionAggregateCover(ctx, aggregateVideoSourcePath, inputs, stagedStem+".jpg"); err != nil {
		return subtitle.Metadata{}, nil, err
	}
	if err := subtitle.EnsureLibraryShowPoster(stagedAggregatePath); err != nil {
		return subtitle.Metadata{}, nil, err
	}

	now := time.Now().UTC()
	firstMetadata := inputs[0].Metadata
	recordMeta := copyRecordMeta(firstMetadata.RecordMeta)
	recordMeta["live_session_id"] = manifest.LiveSessionID
	recordMeta["live_session_media_role"] = "aggregate"
	recordMeta["live_session_media_content_hash"] = knowledgeSessionManifestContentHash(*manifest)
	recordMeta["live_session_media_sources"] = segmentSources

	metadata := subtitle.Metadata{
		Status:             subtitle.StatusCompleted,
		Provider:           firstMetadata.Provider,
		Language:           firstMetadata.Language,
		SourcePath:         firstMetadata.SourcePath,
		OutputPath:         aggregatePath,
		ASSPath:            targetStem + ".ass",
		SRTPath:            targetStem + ".srt",
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
	stagedMetadataPath := stagedStem + ".subtitle.json"
	if err := subtitle.SaveMetadata(stagedMetadataPath, metadata); err != nil {
		return subtitle.Metadata{}, nil, err
	}
	stage.before = []liveSessionStagedFile{
		{staged: stagedStem + ".srt", target: targetStem + ".srt"},
		{staged: stagedStem + ".ass", target: targetStem + ".ass"},
		{staged: stagedStem + ".nfo", target: targetStem + ".nfo"},
		{staged: stagedStem + ".jpg", target: targetStem + ".jpg"},
		{staged: filepath.Join(stage.root, "tvshow.nfo"), target: filepath.Join(filepath.Dir(filepath.Dir(aggregatePath)), "tvshow.nfo")},
		{staged: filepath.Join(stage.root, "poster.jpg"), target: filepath.Join(filepath.Dir(filepath.Dir(aggregatePath)), "poster.jpg"), preserveNonEmpty: true},
	}
	stage.metadata = liveSessionStagedFile{staged: stagedMetadataPath, target: targetStem + ".subtitle.json"}
	ok = true
	return metadata, stage, nil
}

func updateLiveSessionAggregateSourcePaths(metadata *subtitle.Metadata, outputPaths map[string]string) {
	if metadata == nil || len(outputPaths) == 0 {
		return
	}
	updateSource := func(source map[string]any) {
		metadataPath, _ := source["metadata_path"].(string)
		if outputPath := outputPaths[metadataPath]; outputPath != "" {
			source["output_path"] = outputPath
		}
	}
	switch sources := metadata.RecordMeta["live_session_media_sources"].(type) {
	case []map[string]any:
		for _, source := range sources {
			updateSource(source)
		}
	case []any:
		for _, rawSource := range sources {
			if source, ok := rawSource.(map[string]any); ok {
				updateSource(source)
			}
		}
	}
}

type liveSessionAggregateSourceKey struct {
	metadataPath string
	libraryPath  string
}

func restoreLiveSessionSegmentPathsFromAggregateMetadata(
	libraryRoot string,
	manifest *knowledgeSessionManifest,
	inputs []knowledgeSessionPayloadInput,
	metadata subtitle.Metadata,
) error {
	rawSources, exists := metadata.RecordMeta["live_session_media_sources"]
	if !exists {
		return nil
	}
	_, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	if err != nil {
		return err
	}
	inputByKey := make(map[liveSessionAggregateSourceKey]int, len(inputs))
	currentPaths := make(map[liveSessionAggregateSourceKey]string, len(inputs))
	currentHiddenPaths := make(map[liveSessionAggregateSourceKey]string, len(inputs))
	for index, input := range inputs {
		key := liveSessionAggregateSourceKey{
			metadataPath: filepath.Clean(strings.TrimSpace(input.MetadataPath)),
			libraryPath:  filepath.Clean(strings.TrimSpace(input.LibraryPath)),
		}
		inputByKey[key] = index
		if input.Metadata != nil && input.Metadata.RecordMeta != nil {
			hiddenPath, _ := input.Metadata.RecordMeta["live_session_segment_hidden_path"].(string)
			hiddenPath = strings.TrimSpace(hiddenPath)
			if hiddenPath != "" && fileExists(hiddenPath) {
				resolvedHiddenPath, err := resolveLiveSessionPathInsideRoot(hiddenRoot, hiddenPath)
				if err != nil {
					if !sameCleanPath(hiddenPath, input.LibraryPath) {
						return fmt.Errorf("unsafe live session segment hidden path: %s", hiddenPath)
					}
				} else if !liveSessionHiddenPathMatchesInput(libraryRoot, manifest, input, resolvedHiddenPath) {
					return fmt.Errorf("unsafe live session segment hidden path: %s", hiddenPath)
				} else {
					currentHiddenPaths[key] = resolvedHiddenPath
				}
			}
		}
		currentPath, err := resolveLiveSessionSegmentVideoPath(input)
		if err != nil {
			continue
		}
		resolvedCurrentPath, insideErr := resolveLiveSessionPathInsideRoot(hiddenRoot, currentPath)
		if insideErr == nil {
			if !liveSessionHiddenPathMatchesInput(libraryRoot, manifest, input, resolvedCurrentPath) {
				return fmt.Errorf("unsafe live session segment path: %s", currentPath)
			}
			currentPaths[key] = resolvedCurrentPath
			currentHiddenPaths[key] = resolvedCurrentPath
			continue
		}
		if !sameCleanPath(currentPath, input.LibraryPath) {
			return fmt.Errorf("unsafe live session segment path: %s", currentPath)
		}
		currentPaths[key] = filepath.Clean(currentPath)
	}

	sources := make([]map[string]any, 0)
	switch typedSources := rawSources.(type) {
	case []map[string]any:
		sources = append(sources, typedSources...)
	case []any:
		for index, rawSource := range typedSources {
			source, ok := rawSource.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid live session aggregate source mapping at index %d", index)
			}
			sources = append(sources, source)
		}
	default:
		return fmt.Errorf("invalid live session aggregate source mappings")
	}

	mappings := make(map[liveSessionAggregateSourceKey]string, len(sources))
	seenKeys := make(map[liveSessionAggregateSourceKey]struct{}, len(sources))
	resolvedOutputs := make(map[string]liveSessionAggregateSourceKey, len(sources))
	for index, source := range sources {
		outputPath, _ := source["output_path"].(string)
		metadataPath, _ := source["metadata_path"].(string)
		libraryPath, _ := source["library_path"].(string)
		outputPath = strings.TrimSpace(outputPath)
		metadataPath = strings.TrimSpace(metadataPath)
		libraryPath = strings.TrimSpace(libraryPath)
		if outputPath == "" || metadataPath == "" || libraryPath == "" {
			return fmt.Errorf("incomplete live session aggregate source mapping at index %d", index)
		}
		key := liveSessionAggregateSourceKey{
			metadataPath: filepath.Clean(metadataPath),
			libraryPath:  filepath.Clean(libraryPath),
		}
		if _, duplicate := seenKeys[key]; duplicate {
			return fmt.Errorf("duplicate live session aggregate source mapping at index %d", index)
		}
		seenKeys[key] = struct{}{}
		resolvedOutput, err := resolveLiveSessionPathInsideRoot(hiddenRoot, outputPath)
		if err != nil {
			currentPath, hasCurrentPath := currentPaths[key]
			knownLegacyPath := sameCleanPath(outputPath, libraryPath) && hasCurrentPath &&
				(sameCleanPath(currentPath, libraryPath) || currentHiddenPaths[key] != "")
			if knownLegacyPath {
				continue
			}
			return fmt.Errorf("unsafe live session aggregate source output path at index %d: %w", index, err)
		}
		if previousKey, duplicate := resolvedOutputs[resolvedOutput]; duplicate && previousKey != key {
			return fmt.Errorf("duplicate live session aggregate source output path at index %d", index)
		}
		if inputIndex, matched := inputByKey[key]; matched &&
			!liveSessionHiddenPathMatchesInput(libraryRoot, manifest, inputs[inputIndex], resolvedOutput) {
			return fmt.Errorf("unsafe live session aggregate source output identity at index %d", index)
		}
		mappings[key] = outputPath
		resolvedOutputs[resolvedOutput] = key
	}

	for index := range inputs {
		if inputs[index].Metadata == nil {
			continue
		}
		key := liveSessionAggregateSourceKey{
			metadataPath: filepath.Clean(strings.TrimSpace(inputs[index].MetadataPath)),
			libraryPath:  filepath.Clean(strings.TrimSpace(inputs[index].LibraryPath)),
		}
		outputPath, ok := mappings[key]
		if !ok {
			continue
		}
		resolvedOutputPath, err := resolveLiveSessionPathInsideRoot(hiddenRoot, outputPath)
		if err != nil {
			return err
		}
		if currentPath := currentHiddenPaths[key]; currentPath != "" {
			if !sameCleanPath(currentPath, resolvedOutputPath) {
				return fmt.Errorf("conflicting live session segment hidden path: %s", inputs[index].MetadataPath)
			}
			continue
		}
		inputs[index].Metadata.RecordMeta = copyRecordMeta(inputs[index].Metadata.RecordMeta)
		inputs[index].Metadata.RecordMeta["live_session_segment_hidden_path"] = outputPath
	}
	return nil
}

func resolveLiveSessionPathInsideRoot(root, path string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file: %s", path)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path resolves outside live session hidden root: %s", path)
	}
	return resolvedPath, nil
}

func liveSessionHiddenPathMatchesInput(
	libraryRoot string,
	manifest *knowledgeSessionManifest,
	input knowledgeSessionPayloadInput,
	resolvedPath string,
) bool {
	if manifest == nil || strings.TrimSpace(resolvedPath) == "" {
		return false
	}
	expectedPath, err := hiddenLiveSessionSegmentPath(libraryRoot, manifest, input.LibraryPath, input.LibraryPath)
	if err != nil {
		return false
	}
	expectedDir, err := filepath.EvalSymlinks(filepath.Dir(expectedPath))
	if err != nil || !sameCleanPath(filepath.Dir(resolvedPath), expectedDir) {
		return false
	}
	expectedBase := filepath.Base(expectedPath)
	resolvedBase := filepath.Base(resolvedPath)
	if resolvedBase == expectedBase {
		return true
	}
	extension := filepath.Ext(expectedBase)
	expectedStem := strings.TrimSuffix(expectedBase, extension)
	resolvedStem := strings.TrimSuffix(resolvedBase, filepath.Ext(resolvedBase))
	if filepath.Ext(resolvedBase) != extension || !strings.HasPrefix(resolvedStem, expectedStem+".") {
		return false
	}
	collisionIndex, err := strconv.Atoi(strings.TrimPrefix(resolvedStem, expectedStem+"."))
	return err == nil && collisionIndex > 0
}

func saveLiveSessionMetadataAtomically(path string, metadata subtitle.Metadata) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return false, err
	}
	defer os.Remove(tempPath)
	if err := subtitle.SaveMetadata(tempPath, metadata); err != nil {
		return false, err
	}
	file, err := os.Open(tempPath)
	if err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return false, err
	}
	if err := liveSessionSyncDirectory(filepath.Dir(path)); err != nil {
		return true, err
	}
	return true, nil
}

func recoverLiveSessionAggregateWithSegments(
	primary error,
	transaction *liveSessionSegmentTransaction,
	stage *liveSessionSidecarStage,
	aggregatePath string,
	videoBackup string,
) error {
	if transaction != nil {
		if recoveryErr := transaction.rollback(); recoveryErr != nil {
			return fmt.Errorf("%w; segment recovery failed: %v; preserved transaction: %s", primary, recoveryErr, transaction.root)
		}
	}
	return stage.recoverAfterVideo(primary, aggregatePath, videoBackup)
}

func newLiveSessionSidecarStage(libraryRoot string) (*liveSessionSidecarStage, error) {
	_, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	if err != nil {
		return nil, err
	}
	stageParent := filepath.Join(hiddenRoot, ".sidecar_staging")
	if err := os.MkdirAll(stageParent, 0o700); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(stageParent, "live-session-sidecars-*")
	if err != nil {
		return nil, err
	}
	return &liveSessionSidecarStage{root: root}, nil
}

func (stage *liveSessionSidecarStage) promoteBeforeVideo() error {
	for _, file := range stage.before {
		if err := stage.promote(file); err != nil {
			return err
		}
	}
	return nil
}

func (stage *liveSessionSidecarStage) promoteMetadata() error {
	return stage.promote(stage.metadata)
}

func (stage *liveSessionSidecarStage) promote(file liveSessionStagedFile) error {
	if file.preserveNonEmpty {
		if info, err := os.Stat(file.target); err == nil && info.Size() > 0 {
			return nil
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(file.target), 0o755); err != nil {
		return err
	}

	revision, err := captureLiveSessionFileRevision(file.staged, true)
	if err != nil {
		return err
	}

	backup := ""
	if _, err := os.Lstat(file.target); err == nil {
		backupDir := filepath.Join(stage.root, ".backup")
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return err
		}
		backup = filepath.Join(backupDir, fmt.Sprintf("%03d-%s", len(stage.applied), filepath.Base(file.target)))
		if err := os.Link(file.target, backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := liveSessionSidecarRename(file.staged, file.target); err != nil {
		if backup != "" {
			_ = os.Remove(backup)
		}
		return err
	}
	stage.applied = append(stage.applied, liveSessionAppliedFile{
		target:   file.target,
		backup:   backup,
		revision: revision,
	})
	return nil
}

func captureLiveSessionFileRevision(path string, withHash bool) (liveSessionFileRevision, error) {
	info, err := os.Stat(path)
	if err != nil {
		return liveSessionFileRevision{}, err
	}
	revision := liveSessionFileRevision{
		info:    info,
		size:    info.Size(),
		modTime: info.ModTime(),
		hasHash: withHash,
	}
	if withHash {
		content, err := os.ReadFile(path)
		if err != nil {
			return liveSessionFileRevision{}, err
		}
		revision.digest = sha256.Sum256(content)
	}
	return revision, nil
}

func liveSessionFileUnchanged(path string, revision liveSessionFileRevision) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect applied file %s: %w", path, err)
	}
	if !os.SameFile(revision.info, info) ||
		info.Size() != revision.size ||
		!info.ModTime().Equal(revision.modTime) {
		return fmt.Errorf("applied file changed before rollback: %s", path)
	}
	if revision.hasHash {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if sha256.Sum256(content) != revision.digest {
			return fmt.Errorf("applied file content changed before rollback: %s", path)
		}
	}
	return nil
}

func (stage *liveSessionSidecarStage) backupVideo(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	backupDir := filepath.Join(stage.root, ".backup")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	backup := filepath.Join(backupDir, "aggregate-video"+filepath.Ext(path))
	if err := os.Link(path, backup); err != nil {
		return "", err
	}
	return backup, nil
}

func (stage *liveSessionSidecarStage) backupForTarget(target string) string {
	for _, applied := range stage.applied {
		if sameCleanPath(applied.target, target) {
			return applied.backup
		}
	}
	return ""
}

func (stage *liveSessionSidecarStage) archivePreviousAggregateVersion(
	libraryRoot string,
	manifest *knowledgeSessionManifest,
	aggregatePath string,
	videoBackup string,
) error {
	if videoBackup == "" {
		return nil
	}

	aggregateStem := strings.TrimSuffix(aggregatePath, filepath.Ext(aggregatePath))
	metadataBackup := stage.backupForTarget(aggregateStem + ".subtitle.json")
	if metadataBackup == "" {
		return errors.New("previous aggregate metadata backup is missing")
	}
	metadataContent, err := os.ReadFile(metadataBackup)
	if err != nil {
		return err
	}
	versionHash := sha256.Sum256(metadataContent)
	hiddenPath, err := hiddenLiveSessionSegmentPath(libraryRoot, manifest, aggregatePath, aggregatePath)
	if err != nil {
		return err
	}
	archiveDir := filepath.Join(
		filepath.Dir(hiddenPath),
		".aggregate_versions",
		hex.EncodeToString(versionHash[:])[:16],
	)
	archiveParent := filepath.Dir(archiveDir)
	if err := os.MkdirAll(archiveParent, 0o700); err != nil {
		return err
	}
	if fileExists(archiveDir) {
		archivedVideoPath := filepath.Join(archiveDir, filepath.Base(aggregatePath))
		archivedMetadataPath := strings.TrimSuffix(archivedVideoPath, filepath.Ext(archivedVideoPath)) + ".subtitle.json"
		metadata, metadataErr := subtitle.LoadMetadata(archivedMetadataPath)
		supersededBy, _ := metadata.RecordMeta["live_session_media_superseded_by"].(string)
		if metadataErr == nil && sameCleanPath(supersededBy, aggregatePath) {
			return nil
		}
		return fmt.Errorf("previous aggregate archive conflict: %s", archiveDir)
	}
	if err := os.Mkdir(archiveDir, 0o700); err != nil {
		return err
	}
	cleanupArchive := true
	defer func() {
		if cleanupArchive {
			_ = os.RemoveAll(archiveDir)
			_ = syncLiveSessionDirectory(archiveParent)
		}
	}()

	archiveVideoPath := filepath.Join(archiveDir, filepath.Base(aggregatePath))
	if err := os.Link(videoBackup, archiveVideoPath); err != nil {
		return err
	}
	archiveStem := strings.TrimSuffix(archiveVideoPath, filepath.Ext(archiveVideoPath))
	for _, extension := range []string{".srt", ".ass", ".nfo", ".jpg"} {
		backup := stage.backupForTarget(aggregateStem + extension)
		if backup == "" {
			continue
		}
		if err := os.Link(backup, archiveStem+extension); err != nil {
			return err
		}
	}

	metadata, err := subtitle.LoadMetadata(metadataBackup)
	if err != nil {
		return err
	}
	metadata.OutputPath = archiveVideoPath
	if fileExists(archiveStem + ".ass") {
		metadata.ASSPath = archiveStem + ".ass"
	}
	if fileExists(archiveStem + ".srt") {
		metadata.SRTPath = archiveStem + ".srt"
	}
	metadata.RecordMeta = copyRecordMeta(metadata.RecordMeta)
	metadata.RecordMeta["live_session_media_role"] = "aggregate_version"
	metadata.RecordMeta["live_session_media_superseded_by"] = aggregatePath
	if err := subtitle.SaveMetadata(archiveStem+".subtitle.json", metadata); err != nil {
		return err
	}
	if err := syncLiveSessionDirectory(archiveDir); err != nil {
		return err
	}
	if err := syncLiveSessionDirectory(archiveParent); err != nil {
		return err
	}
	cleanupArchive = false
	return nil
}

func (stage *liveSessionSidecarStage) rollback() error {
	if err := stage.validateAppliedSidecars(); err != nil {
		return err
	}
	return stage.rollbackAppliedSidecars()
}

func (stage *liveSessionSidecarStage) validateAppliedSidecars() error {
	for _, applied := range stage.applied {
		if err := liveSessionFileUnchanged(applied.target, applied.revision); err != nil {
			return err
		}
	}
	return nil
}

func (stage *liveSessionSidecarStage) rollbackAppliedSidecars() error {
	var firstErr error
	for index := len(stage.applied) - 1; index >= 0; index-- {
		applied := stage.applied[index]
		if applied.backup != "" {
			if err := liveSessionSidecarRename(applied.backup, applied.target); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := liveSessionSidecarRemove(applied.target); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	stage.applied = nil
	return firstErr
}

func (stage *liveSessionSidecarStage) recover(primary error) error {
	recoveryErr := stage.rollback()
	if recoveryErr == nil {
		stage.cleanup()
		return primary
	}
	return fmt.Errorf("%w; recovery failed: %v; preserved staging: %s", primary, recoveryErr, stage.root)
}

func (stage *liveSessionSidecarStage) recoverAfterVideo(primary error, aggregatePath, videoBackup string) error {
	recoveryErr := stage.validateRecovery(aggregatePath)
	if recoveryErr == nil {
		recoveryErr = stage.restoreVideo(aggregatePath, videoBackup)
	}
	if recoveryErr == nil {
		recoveryErr = stage.rollbackAppliedSidecars()
	}
	if recoveryErr == nil {
		stage.cleanup()
		return primary
	}
	return fmt.Errorf("%w; recovery failed: %v; preserved staging: %s", primary, recoveryErr, stage.root)
}

func (stage *liveSessionSidecarStage) validateRecovery(aggregatePath string) error {
	if stage.videoRevision == nil {
		return errors.New("aggregate video revision is missing")
	}
	return errors.Join(
		liveSessionFileUnchanged(aggregatePath, *stage.videoRevision),
		stage.validateAppliedSidecars(),
	)
}

func (stage *liveSessionSidecarStage) commit() {
	stage.applied = nil
	stage.videoRevision = nil
	stage.cleanup()
}

func (stage *liveSessionSidecarStage) cleanup() {
	if stage == nil || stage.root == "" {
		return
	}
	_ = os.RemoveAll(stage.root)
	stage.root = ""
}

func (stage *liveSessionSidecarStage) restoreVideo(path, backup string) error {
	if backup == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return liveSessionMediaRename(backup, path)
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
	recordedAt, ok := liveSessionInputRecordedAt(inputs[0])
	if !ok {
		recordedAt = parseEpisodeDate(episode.Date)
	}
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
		`  <thumb aspect="poster">poster.jpg</thumb>`,
		fmt.Sprintf("  <premiered>%s</premiered>", episode.Date),
		fmt.Sprintf("  <dateadded>%s</dateadded>", recordedAt.Format("2006-01-02 15:04:05")),
		"</tvshow>",
		"",
	}, "\n")
	return os.WriteFile(filepath.Join(showDir, "tvshow.nfo"), []byte(content), 0o644)
}

func preserveExistingLiveSessionShowNFO(aggregatePath, stagedNFOPath string) error {
	showNFOPath := filepath.Join(filepath.Dir(filepath.Dir(aggregatePath)), "tvshow.nfo")
	content, err := os.ReadFile(showNFOPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	text := string(content)
	episode, ok := parseLiveSessionMediaEpisode(aggregatePath)
	if !ok {
		return nil
	}
	var nfo struct {
		XMLName   xml.Name `xml:"tvshow"`
		Title     string   `xml:"title"`
		ShowTitle string   `xml:"showtitle"`
	}
	if err := xml.Unmarshal(content, &nfo); err != nil ||
		nfo.XMLName.Local != "tvshow" ||
		strings.TrimSpace(nfo.Title) != episode.Alias ||
		strings.TrimSpace(nfo.ShowTitle) != episode.Alias {
		return nil
	}
	const thumb = `<thumb aspect="poster">poster.jpg</thumb>`
	if !strings.Contains(text, thumb) {
		text = strings.Replace(text, "</tvshow>", "  "+thumb+"\n</tvshow>", 1)
	}
	return os.WriteFile(stagedNFOPath, []byte(text), 0o644)
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

func syncLiveSessionDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func persistLiveSessionSegmentJournal(transactionRoot string, moves []*liveSessionSegmentMove) error {
	entries := make([]liveSessionSegmentJournalEntry, 0, len(moves))
	for _, move := range moves {
		entries = append(entries, liveSessionSegmentJournalEntry{
			Source:         move.source,
			Target:         move.target,
			MetadataPath:   move.metadataPath,
			StagedMetadata: move.stagedMetadata,
			BackupMetadata: move.backupMetadata,
		})
	}
	content, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	journalPath := filepath.Join(transactionRoot, "moves.json")
	journal, err := os.OpenFile(journalPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := journal.Write(append(content, '\n')); err != nil {
		_ = journal.Close()
		return err
	}
	if err := journal.Sync(); err != nil {
		_ = journal.Close()
		return err
	}
	if err := journal.Close(); err != nil {
		return err
	}
	if err := syncLiveSessionDirectory(transactionRoot); err != nil {
		return err
	}
	return syncLiveSessionDirectory(filepath.Dir(transactionRoot))
}

func hideLiveSessionSegmentVideos(libraryRoot string, manifest *knowledgeSessionManifest, inputs []knowledgeSessionPayloadInput, aggregatePath string) (*liveSessionSegmentTransaction, error) {
	if manifest == nil {
		return nil, nil
	}
	_, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	if err != nil {
		return nil, err
	}
	transactionParent := filepath.Join(hiddenRoot, ".segment_transactions")
	if err := os.MkdirAll(transactionParent, 0o700); err != nil {
		return nil, err
	}
	transactionRoot, err := os.MkdirTemp(transactionParent, "segments-*")
	if err != nil {
		return nil, err
	}

	var moves []*liveSessionSegmentMove
	reservedTargets := make(map[string]struct{})
	outputPaths := make(map[string]string)
	for index, input := range inputs {
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
			_ = os.RemoveAll(transactionRoot)
			return nil, err
		}
		if sameCleanPath(segmentPath, hiddenPath) {
			outputPaths[input.MetadataPath] = segmentPath
			if fileExists(input.LibraryPath) && !sameCleanPath(input.LibraryPath, segmentPath) {
				sourceInfo, sourceErr := os.Stat(input.LibraryPath)
				targetInfo, targetErr := os.Stat(segmentPath)
				if sourceErr != nil || targetErr != nil || !os.SameFile(sourceInfo, targetInfo) {
					_ = os.RemoveAll(transactionRoot)
					return nil, fmt.Errorf("visible segment differs from hidden segment: %s", input.LibraryPath)
				}
				if err := liveSessionRemoveFile(input.LibraryPath); err != nil {
					_ = os.RemoveAll(transactionRoot)
					return nil, err
				}
				if err := liveSessionSyncDirectory(filepath.Dir(input.LibraryPath)); err != nil {
					_ = os.RemoveAll(transactionRoot)
					return nil, err
				}
			}
			continue
		}
		if fileExists(hiddenPath) {
			sourceInfo, sourceErr := os.Stat(segmentPath)
			targetInfo, targetErr := os.Stat(hiddenPath)
			if sourceErr != nil || targetErr != nil || !os.SameFile(sourceInfo, targetInfo) {
				for fileExists(hiddenPath) {
					hiddenPath = uniqueLiveSessionHiddenPath(hiddenPath)
				}
			}
		}
		for {
			if _, exists := reservedTargets[filepath.Clean(hiddenPath)]; !exists {
				break
			}
			hiddenPath = uniqueLiveSessionHiddenPath(hiddenPath)
		}
		reservedTargets[filepath.Clean(hiddenPath)] = struct{}{}
		outputPaths[input.MetadataPath] = hiddenPath

		metadata := *input.Metadata
		metadata.OutputPath = hiddenPath
		if metadata.RecordMeta == nil {
			metadata.RecordMeta = map[string]any{}
		}
		metadata.RecordMeta["live_session_media_role"] = "segment"
		metadata.RecordMeta["live_session_media_aggregate_path"] = aggregatePath
		metadata.RecordMeta["live_session_segment_hidden_path"] = hiddenPath

		stagedMetadata := filepath.Join(transactionRoot, fmt.Sprintf("%03d.new.json", index))
		backupMetadata := filepath.Join(transactionRoot, fmt.Sprintf("%03d.old.json", index))
		if err := subtitle.SaveMetadata(stagedMetadata, metadata); err != nil {
			_ = os.RemoveAll(transactionRoot)
			return nil, err
		}
		if err := os.Link(input.MetadataPath, backupMetadata); err != nil {
			_ = os.RemoveAll(transactionRoot)
			return nil, err
		}
		metadataBefore, err := captureLiveSessionFileRevision(backupMetadata, true)
		if err != nil {
			_ = os.RemoveAll(transactionRoot)
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(hiddenPath), 0o755); err != nil {
			_ = os.RemoveAll(transactionRoot)
			return nil, err
		}
		moves = append(moves, &liveSessionSegmentMove{
			source:         segmentPath,
			target:         hiddenPath,
			metadataPath:   input.MetadataPath,
			stagedMetadata: stagedMetadata,
			backupMetadata: backupMetadata,
			metadataBefore: metadataBefore,
		})
	}
	if len(moves) == 0 {
		_ = os.RemoveAll(transactionRoot)
		if len(outputPaths) == 0 {
			return nil, nil
		}
		return &liveSessionSegmentTransaction{outputPaths: outputPaths}, nil
	}
	if err := persistLiveSessionSegmentJournal(transactionRoot, moves); err != nil {
		_ = os.RemoveAll(transactionRoot)
		return nil, err
	}

	for _, move := range moves {
		if !sameCleanPath(move.source, move.target) {
			videoRevision, err := captureLiveSessionFileRevision(move.source, false)
			if err != nil {
				return nil, recoverLiveSessionSegmentMoves(err, transactionRoot, moves)
			}
			sourceInfo, sourceErr := os.Stat(move.source)
			targetInfo, targetErr := os.Stat(move.target)
			if targetErr == nil && sourceErr == nil && os.SameFile(sourceInfo, targetInfo) {
				move.targetLinked = true
			} else {
				if targetErr != nil && !os.IsNotExist(targetErr) {
					return nil, recoverLiveSessionSegmentMoves(targetErr, transactionRoot, moves)
				}
				if err := liveSessionLinkFile(move.source, move.target); err != nil {
					return nil, recoverLiveSessionSegmentMoves(err, transactionRoot, moves)
				}
				move.targetLinked = true
				move.targetCreated = true
				move.videoRevision = videoRevision
				if err := liveSessionSyncDirectory(filepath.Dir(move.target)); err != nil {
					return nil, recoverLiveSessionSegmentMoves(err, transactionRoot, moves)
				}
			}
			move.videoRevision = videoRevision
		}
		if err := liveSessionFileUnchanged(move.metadataPath, move.metadataBefore); err != nil {
			return nil, recoverLiveSessionSegmentMoves(err, transactionRoot, moves)
		}
		metadataRevision, err := captureLiveSessionFileRevision(move.stagedMetadata, true)
		if err != nil {
			return nil, recoverLiveSessionSegmentMoves(err, transactionRoot, moves)
		}
		if err := liveSessionMetadataRename(move.stagedMetadata, move.metadataPath); err != nil {
			return nil, recoverLiveSessionSegmentMoves(err, transactionRoot, moves)
		}
		move.metadataRevision = metadataRevision
		move.metadataPromoted = true
		if err := liveSessionRemoveFile(move.source); err != nil && !os.IsNotExist(err) {
			return nil, recoverLiveSessionSegmentMoves(err, transactionRoot, moves)
		}
		move.sourceRemoved = true
		if err := liveSessionSyncDirectory(filepath.Dir(move.source)); err != nil {
			return nil, recoverLiveSessionSegmentMoves(err, transactionRoot, moves)
		}
	}
	return &liveSessionSegmentTransaction{
		root:        transactionRoot,
		moves:       moves,
		outputPaths: outputPaths,
	}, nil
}

func (transaction *liveSessionSegmentTransaction) commit() {
	if transaction.root != "" {
		_ = os.RemoveAll(transaction.root)
	}
}

func (transaction *liveSessionSegmentTransaction) recover(primary error) error {
	if transaction.root == "" {
		return primary
	}
	return recoverLiveSessionSegmentMoves(primary, transaction.root, transaction.moves)
}

func (transaction *liveSessionSegmentTransaction) rollback() error {
	if transaction.root == "" {
		return nil
	}
	return rollbackLiveSessionSegmentMoves(transaction.root, transaction.moves)
}

func recoverLiveSessionSegmentMoves(primary error, transactionRoot string, moves []*liveSessionSegmentMove) error {
	if recoveryErr := rollbackLiveSessionSegmentMoves(transactionRoot, moves); recoveryErr != nil {
		return fmt.Errorf("%w; %v", primary, recoveryErr)
	}
	return primary
}

func rollbackLiveSessionSegmentMoves(transactionRoot string, moves []*liveSessionSegmentMove) error {
	for _, move := range moves {
		if move.metadataPromoted {
			if err := liveSessionFileUnchanged(move.metadataPath, move.metadataRevision); err != nil {
				return fmt.Errorf("segment recovery refused: %v; preserved transaction: %s", err, transactionRoot)
			}
		}
		if move.targetLinked {
			if err := liveSessionFileUnchanged(move.target, move.videoRevision); err != nil {
				return fmt.Errorf("segment recovery refused: %v; preserved transaction: %s", err, transactionRoot)
			}
		}
	}

	for index := len(moves) - 1; index >= 0; index-- {
		move := moves[index]
		if move.sourceRemoved {
			if err := liveSessionLinkFile(move.target, move.source); err != nil {
				return fmt.Errorf("segment recovery failed: restore segment video %s: %v; preserved transaction: %s", move.source, err, transactionRoot)
			} else if err := liveSessionSyncDirectory(filepath.Dir(move.source)); err != nil {
				return fmt.Errorf("segment recovery failed: sync restored segment video %s: %v; preserved transaction: %s", move.source, err, transactionRoot)
			}
		}
		if move.metadataPromoted {
			if err := liveSessionMetadataRename(move.backupMetadata, move.metadataPath); err != nil {
				return fmt.Errorf("segment recovery failed: restore segment metadata %s: %v; preserved transaction: %s", move.metadataPath, err, transactionRoot)
			}
		}
		if move.targetCreated {
			if err := liveSessionRemoveFile(move.target); err != nil {
				return fmt.Errorf("segment recovery failed: remove hidden segment video %s: %v; preserved transaction: %s", move.target, err, transactionRoot)
			} else if err := liveSessionSyncDirectory(filepath.Dir(move.target)); err != nil {
				return fmt.Errorf("segment recovery failed: sync removed hidden segment video %s: %v; preserved transaction: %s", move.target, err, transactionRoot)
			}
		}
	}
	_ = os.RemoveAll(transactionRoot)
	return nil
}

func hiddenLiveSessionSegmentPath(libraryRoot string, manifest *knowledgeSessionManifest, libraryPath string, segmentPath string) (string, error) {
	absoluteLibraryRoot, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	if err != nil {
		return "", err
	}
	seasonDir, err := filepath.Abs(filepath.Dir(libraryPath))
	if err != nil {
		return "", err
	}
	seasonDir, err = filepath.EvalSymlinks(seasonDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absoluteLibraryRoot, seasonDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("library path is outside library root: %s", libraryPath)
	}
	hash := sha256.Sum256([]byte(manifest.SourceID + "\n" + manifest.LiveSessionID))
	return filepath.Join(hiddenRoot, rel, hex.EncodeToString(hash[:])[:16], filepath.Base(segmentPath)), nil
}

func liveSessionSegmentRoots(libraryRoot string) (string, string, error) {
	absoluteLibraryRoot, err := filepath.Abs(libraryRoot)
	if err != nil {
		return "", "", err
	}
	absoluteLibraryRoot, err = filepath.EvalSymlinks(absoluteLibraryRoot)
	if err != nil {
		return "", "", err
	}
	parentDir := filepath.Dir(absoluteLibraryRoot)
	if sameCleanPath(parentDir, absoluteLibraryRoot) {
		return "", "", fmt.Errorf("cannot place live session segments outside library root: %s", libraryRoot)
	}
	hiddenRoot := filepath.Join(parentDir, liveSessionSegmentsDirName)
	resolvedHiddenRoot, err := filepath.EvalSymlinks(hiddenRoot)
	if err == nil {
		hiddenRoot = resolvedHiddenRoot
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	relative, err := filepath.Rel(absoluteLibraryRoot, hiddenRoot)
	if err != nil {
		return "", "", err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)) {
		return "", "", fmt.Errorf("live session hidden root resolves inside library root: %s", hiddenRoot)
	}
	return absoluteLibraryRoot, hiddenRoot, nil
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
		Title:              subtitle.MediaDisplayTitle(aggregate.LibraryPath),
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
