package stages

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
)

func (s *SubtitleGenerateStage) publishCompletedSession(ctx *pipeline.PipelineContext, cfg configs.SubtitleKnowledgeSyncConfig, libraryRoot string, sources []pipeline.SessionMediaSource) ([]pipeline.FileInfo, error) {
	session, err := ctx.SessionMediaReady(sources)
	if err != nil {
		return nil, err
	}
	if session.Blocked != "" {
		return nil, fmt.Errorf("live session publication blocked: %s", session.Blocked)
	}
	if !session.Ready() {
		// 持久化队列根据场次 ready 放行，不用时间轮询反复占用执行槽。
		return nil, &pipeline.RetryLaterError{Err: errors.New("waiting for confirmed session end and all registered media")}
	}
	_, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	if err != nil {
		return nil, err
	}
	knowledgeSessionManifestMu.Lock()
	defer knowledgeSessionManifestMu.Unlock()
	manifestPath := knowledgeSessionManifestPath(libraryRoot, session.ID)
	manifest, err := loadOrCreateKnowledgeSessionManifest(manifestPath, session.ID)
	if err != nil {
		return nil, err
	}
	allSources := session.Sources()
	for _, source := range allSources {
		if _, err := resolveLiveSessionPathInsideRoot(hiddenRoot, source.LibraryPath); err != nil {
			return nil, fmt.Errorf("session media is not in verified work area: %w", err)
		}
		metadata, err := subtitle.LoadMetadata(source.MetadataPath)
		if err != nil {
			return nil, err
		}
		if metadata.Status != subtitle.StatusCompleted || metadata.SourcePath != source.InputPath || metadata.OutputPath != source.LibraryPath || metadata.RecordMeta["live_session_id"] != session.ID {
			return nil, errors.New("registered session media does not match completed metadata")
		}
		if err := validateCompletedCheckpoint(metadata, source.LibraryPath); err != nil {
			return nil, err
		}
		if metadata.RecordMeta["pipeline_task_id"] != strconv.FormatInt(source.TaskID, 10) || metadata.RecordMeta["recording_producer_id"] != source.ProducerID {
			return nil, errors.New("registered session media task or producer identity mismatch")
		}
		if hidden, exists := metadata.RecordMeta["live_session_segment_hidden_path"]; exists && hidden != "" && hidden != source.LibraryPath {
			return nil, errors.New("registered session media redirects to an unverified input")
		}
		if _, err := registerKnowledgeSessionSource(&manifest, libraryRoot, knowledgeSessionPayloadInput{TaskID: fmt.Sprintf("bililive-go-%d", source.TaskID), LibraryPath: source.LibraryPath, MetadataPath: source.MetadataPath, Metadata: &metadata}, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	if len(manifest.Sources) != len(allSources) || len(allSources) == 0 {
		return nil, errors.New("session manifest does not match complete registered input set")
	}
	manifest.PublicationVersion = 1
	if err := saveKnowledgeSessionManifest(manifestPath, manifest); err != nil {
		return nil, err
	}
	aggregate, err := publishLiveSessionMediaAggregate(ctx.Ctx, ctx.FFmpegPath, libraryRoot, &manifest)
	if err != nil {
		return nil, err
	}
	if aggregate == nil {
		return nil, errors.New("sealed session produced no published media")
	}
	if cfg.Enabled {
		contentHash := knowledgeSessionManifestContentHash(manifest)
		if manifest.PostedContentHash != contentHash {
			payload, err := buildKnowledgeLiveSessionAggregateIngestPayload(ctx, cfg, libraryRoot, aggregate)
			if err == nil {
				err = postKnowledgeIngest(ctx, cfg, payload)
			}
			now := time.Now().UTC()
			if err != nil {
				markKnowledgeSessionSources(manifest, subtitle.StatusFailed, manifest.SourceID, sanitizeKnowledgeSyncError(err), now, true)
				s.logs += "知识同步失败，整场媒体已发布且保持可用\n"
			} else {
				manifest.PostedContentHash = contentHash
				manifest.PostedAt = &now
				if err := saveKnowledgeSessionManifest(manifestPath, manifest); err != nil {
					return nil, err
				}
				markKnowledgeSessionSources(manifest, subtitle.StatusQueued, manifest.SourceID, "", now, true)
			}
		}
	}
	return []pipeline.FileInfo{
		pipeline.NewVideoFileInfo(aggregate.LibraryPath),
		{Path: aggregate.Metadata.SRTPath, Type: pipeline.FileTypeOther},
		{Path: aggregate.Metadata.ASSPath, Type: pipeline.FileTypeOther},
	}, nil
}
