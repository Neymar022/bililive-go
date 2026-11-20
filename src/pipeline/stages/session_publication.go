package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		return nil, &pipeline.RetryLaterError{Err: pipeline.NewLibrarySessionIncompleteError(session)}
	}
	_, hiddenRoot, err := liveSessionSegmentRoots(libraryRoot)
	if err != nil {
		return nil, err
	}
	knowledgeSessionManifestMu.Lock()
	heldManifest := true
	defer func() {
		if heldManifest {
			knowledgeSessionManifestMu.Unlock()
		}
	}()
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
	// append有独立同场OS工作锁；昂贵准备不占全局manifest互斥或season锁。
	appendMode := false
	if len(allSources) > 0 {
		season := subtitle.LibrarySeasonForInput(libraryRoot, allSources[0].LibraryPath, ctx.RecordInfo.HostName, ctx.RecordInfo.StartTime)
		appendMode, err = subtitle.LibraryAppendEnabled(season)
		if err != nil {
			return nil, err
		}
	}
	if appendMode {
		knowledgeSessionManifestMu.Unlock()
		heldManifest = false
	}
	aggregate, err := publishLiveSessionMediaAggregate(ctx.Ctx, ctx.FFmpegPath, libraryRoot, &manifest, ctx)
	if err != nil {
		return nil, err
	}
	if aggregate == nil {
		return nil, errors.New("sealed session produced no published media")
	}
	if appendMode {
		knowledgeSessionManifestMu.Lock()
		heldManifest = true
		// 聚合期间同场另一个调用可能已提交知识投递状态；重新合并，不能以旧manifest覆盖。
		current, err := loadOrCreateKnowledgeSessionManifest(manifestPath, session.ID)
		if err != nil {
			return nil, err
		}
		if knowledgeSessionManifestContentHash(current) != knowledgeSessionManifestContentHash(manifest) {
			return nil, errors.New("append manifest inputs changed during preparation")
		}
		current.AggregatePath = aggregate.LibraryPath
		current.PreviousAggregatePath = ""
		manifest = current
		if err := saveKnowledgeSessionManifest(manifestPath, manifest); err != nil {
			return nil, err
		}
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
	if current := configs.GetCurrentConfig(); current != nil && current.Subtitle.DeleteSourceOnCompletion && (ctx.PreserveRecoverySources == nil || !ctx.PreserveRecoverySources()) && len(aggregate.Metadata.RecordingSources) > 0 {
		if err := subtitle.DeletePublishedRecordingSources(aggregate.LibraryPath); err != nil {
			s.logs += fmt.Sprintf("原片清理已跳过或失败，公开成品保持可用: %v\n", err)
		}
	}
	return []pipeline.FileInfo{
		pipeline.NewVideoFileInfo(aggregate.LibraryPath),
		{Path: aggregate.Metadata.SRTPath, Type: pipeline.FileTypeOther},
		{Path: aggregate.Metadata.ASSPath, Type: pipeline.FileTypeOther},
	}, nil
}

// publishAppendLiveSessionMedia复用原隐藏媒体/sidecar准备，只有短公开安装持season→task。
func publishAppendLiveSessionMedia(ctx context.Context, ffmpegPath, libraryRoot, target string, manifest *knowledgeSessionManifest, inputs []knowledgeSessionPayloadInput, hooks ...*pipeline.PipelineContext) (*liveSessionMediaAggregate, error) {
	if len(hooks) == 0 || hooks[0] == nil || hooks[0].LibraryPublication == nil {
		return nil, errors.New("append_commit_guard_unavailable")
	}
	hook := hooks[0]
	if err := hook.LibraryPublication(ctx, pipeline.LibraryPublicationRequest{Phase: "append_capability", TaskID: hook.TaskID, SessionID: manifest.LiveSessionID, Path: target, LibraryRoot: libraryRoot}); err != nil {
		return nil, err
	}
	// 独立同场OS锁不按路径生成摘要；它位于既有隐藏manifest目录。
	if filepath.Base(manifest.LiveSessionID) != manifest.LiveSessionID || manifest.LiveSessionID == "." || manifest.LiveSessionID == "" {
		return nil, errors.New("invalid session publication owner")
	}
	dir := filepath.Dir(knowledgeSessionManifestPath(libraryRoot, manifest.LiveSessionID))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := subtitle.LockLibraryAssignmentSession(filepath.Join(dir, ".append-"+manifest.LiveSessionID+".lock"))
	if err != nil {
		if errors.Is(err, subtitle.ErrLibraryAssignmentWorkActive) {
			return nil, &pipeline.RetryLaterError{Err: errors.New("session_publication_active")}
		}
		return nil, err
	}
	defer lock.Close()
	season := filepath.Dir(target)
	lease, err := subtitle.LockLibraryAssignments(ctx, season)
	if err != nil {
		return nil, err
	}
	closed := false
	defer func() {
		if !closed {
			lease.Close()
		}
	}()
	ledger := lease.Ledger()
	// 媒体已经可见即不可撤销；同场与其他场均先只核既有文件、闭合ledger，不等PG或开新安装。
	for _, pending := range ledger.Assignments {
		if pending.State != "committing" {
			continue
		}
		if _, e := os.Lstat(pending.TargetPath); os.IsNotExist(e) {
			continue
		} else if e != nil {
			return nil, e
		}
		work, e := subtitle.LockAssignmentWork(pending)
		if e != nil {
			return nil, &pipeline.RetryLaterError{Err: e}
		}
		e = lease.ReconcileVisible(pending.OperationID, lease.Ledger().Revision)
		work.Close()
		if e != nil {
			return nil, e
		}
	}
	ledger = lease.Ledger()
	existing := false
	for _, e := range ledger.LegacyEntries {
		if e.Path == target {
			existing = true
		}
	}
	for _, a := range ledger.Assignments {
		if a.State == "published" && a.TargetPath == target && a.SessionID == manifest.LiveSessionID {
			existing = true
		}
	}
	finishPublished := func() (*liveSessionMediaAggregate, error) {
		meta, e := subtitle.LoadMetadata(strings.TrimSuffix(target, ".mp4") + ".subtitle.json")
		if e != nil {
			return nil, e
		}
		if e = validateCompletedCheckpoint(meta, target); e != nil {
			return nil, e
		}
		if meta.RecordMeta["live_session_id"] != manifest.LiveSessionID || meta.RecordMeta["live_session_media_role"] != "aggregate" || meta.RecordMeta["live_session_media_content_hash"] != knowledgeSessionManifestContentHash(*manifest) {
			return nil, errors.New("append existing result differs from complete members")
		}
		if e = subtitle.ValidatePublishedLibraryEpisode(target); e != nil {
			return nil, e
		}
		// 独立追踪真实已公开成品；取消只阻止后续task提交，不抹去物理发布事实。
		if e = hook.LibraryPublication(ctx, pipeline.LibraryPublicationRequest{LibraryRoot: libraryRoot, Path: target, SessionID: manifest.LiveSessionID, TaskID: hook.TaskID, Phase: "published"}); e != nil {
			return nil, e
		}
		if existing {
			current, e := subtitle.LockLibraryAssignments(ctx, season)
			if e != nil {
				return nil, e
			}
			state := current.Ledger()
			operation := state.EnabledByOperation
			for _, a := range state.Assignments {
				if a.State == "published" && a.TargetPath == target {
					operation = a.OperationID
				}
			}
			frame := &pipeline.LibraryAppendCommitRequest{SeasonPath: season, TargetPath: target, OperationID: operation, AssignmentRevision: state.Revision}
			var observation pipeline.LibraryIndexObservation
			check := pipeline.LibraryPublicationRequest{LibraryRoot: libraryRoot, Path: target, SessionID: manifest.LiveSessionID, TaskID: hook.TaskID, Phase: "append_snapshot", AppendCommit: frame, IndexObservation: &observation}
			e = hook.LibraryPublication(ctx, check)
			if e == nil {
				e = mergeAppendIndexMappings(current, observation)
				frame.AssignmentRevision = current.Ledger().Revision
			}
			if e == nil {
				observation = pipeline.LibraryIndexObservation{}
				check.Phase = "append_commit"
				check.CommitFiles = func() error {
					if current.Ledger().Revision != frame.AssignmentRevision {
						return errors.New("existing append revision changed")
					}
					return validateCompletedCheckpoint(meta, target)
				}
				e = hook.LibraryPublication(ctx, check)
				if mergeErr := mergeAppendIndexMappings(current, observation); mergeErr != nil {
					e = errors.Join(e, mergeErr)
				}
				frame.AssignmentRevision = current.Ledger().Revision
			}
			current.Close()
			if e != nil {
				return nil, e
			}
		}
		manifest.AggregatePath = target
		manifest.PreviousAggregatePath = ""
		return &liveSessionMediaAggregate{target, strings.TrimSuffix(target, ".mp4") + ".subtitle.json", meta}, nil
	}
	if existing {
		lease.Close()
		closed = true
		return finishPublished()
	}
	// 崩溃后无writer的未公开reservation可撤销；已公开则只补自有文件，不重算。
	for _, pending := range ledger.Assignments {
		if pending.State == "published" || pending.State == "aborted" || pending.SessionID == manifest.LiveSessionID {
			continue
		}
		work, e := subtitle.LockAssignmentWork(pending)
		if e != nil {
			return nil, &pipeline.RetryLaterError{Err: e}
		}
		_, visible := os.Lstat(pending.TargetPath)
		if visible == nil {
			e = lease.ReconcileVisible(pending.OperationID, lease.Ledger().Revision)
			work.Close()
			if e != nil {
				return nil, e
			}
		} else if os.IsNotExist(visible) {
			work.Close()
			if e = lease.Abort(pending.OperationID); e != nil {
				if errors.Is(e, subtitle.ErrLibraryAssignmentPartialInstall) {
					return nil, &pipeline.RetryLaterError{Err: e}
				}
				return nil, e
			}
		} else {
			work.Close()
			return nil, visible
		}
	}
	var members []int64
	memberSet := map[int64]bool{}
	for _, input := range inputs {
		id, e := strconv.ParseInt(strings.TrimPrefix(input.TaskID, "bililive-go-"), 10, 64)
		if e != nil {
			return nil, e
		}
		if !memberSet[id] {
			memberSet[id] = true
			members = append(members, id)
		}
	}
	assignment, err := lease.Reserve(manifest.LiveSessionID, target, members)
	if err != nil {
		return nil, err
	}
	work, err := subtitle.LockAssignmentWork(assignment)
	if err != nil {
		return nil, err
	}
	defer work.Close()
	expected := &pipeline.LibraryAppendCommitRequest{SeasonPath: season, TargetPath: target, OperationID: assignment.OperationID, AssignmentRevision: lease.Ledger().Revision}
	var observation pipeline.LibraryIndexObservation
	request := pipeline.LibraryPublicationRequest{LibraryRoot: libraryRoot, Path: target, SessionID: manifest.LiveSessionID, TaskID: hook.TaskID, Phase: "append_snapshot", AppendCommit: expected, IndexObservation: &observation}
	if err = hook.LibraryPublication(ctx, request); err != nil {
		work.Close()
		_ = lease.Abort(assignment.OperationID)
		var conflict *pipeline.LibraryAppendIndexConflictError
		if errors.As(err, &conflict) {
			_ = lease.Block(conflict.Reason)
		}
		return nil, err
	}
	if err = mergeAppendIndexMappings(lease, observation); err != nil {
		return nil, err
	}
	expected.AssignmentRevision = lease.Ledger().Revision
	var inputStates []subtitle.AssignmentFileIdentity
	for _, input := range inputs {
		for _, path := range []string{input.LibraryPath, input.Metadata.SRTPath, input.Metadata.ASSPath} {
			state, e := subtitle.AssignmentStat(path)
			if e != nil {
				return nil, e
			}
			inputStates = append(inputStates, state)
		}
	}
	sourceSet, err := json.Marshal(expected.ExpectedSession.Sources())
	if err != nil {
		return nil, err
	}
	if err = lease.BindInputs(assignment.OperationID, lease.Ledger().Revision, inputStates, knowledgeSessionManifestContentHash(*manifest), sourceSet); err != nil {
		work.Close()
		_ = lease.Abort(assignment.OperationID)
		return nil, err
	}
	expected.AssignmentRevision = lease.Ledger().Revision
	allowCompute := false
	permission := request
	permission.Phase = "append_compute_permission"
	permission.ComputeAllowed = &allowCompute
	if err = hook.LibraryPublication(ctx, permission); err != nil {
		return nil, err
	}
	reservedRevision := lease.Ledger().Revision
	lease.Close()
	closed = true
	manifest.AggregatePath = target
	var prepared *preparedLiveSessionAggregate
	if assignment.State != "committing" {
		prepared, err = prepareLiveSessionAggregate(ctx, ffmpegPath, libraryRoot, target, manifest, inputs, assignment.StagingRoot, !allowCompute)
		if err != nil {
			work.Close()
			retry, e := subtitle.LockLibraryAssignments(context.Background(), season)
			if e != nil {
				return nil, e
			}
			e = retry.Abort(assignment.OperationID)
			retry.Close()
			if e != nil {
				return nil, e
			}
			if errors.Is(err, subtitle.ErrLibraryComputeRequired) {
				var due time.Time
				required := request
				required.Phase = "append_compute_required"
				required.ComputeDue = &due
				if e := hook.LibraryPublication(ctx, required); e != nil {
					return nil, e
				}
				return nil, &pipeline.RetryLaterError{Err: subtitle.ErrLibraryComputeRequired, Delay: time.Until(due)}
			}
			return nil, err
		}
	}
	lease, err = subtitle.LockLibraryAssignments(ctx, season)
	if err != nil {
		return nil, err
	}
	closed = false
	if lease.Ledger().Revision != reservedRevision {
		return nil, errors.New("append preparation revision changed")
	}
	if prepared != nil {
		if hook.PreserveRecoverySources != nil && hook.PreserveRecoverySources() {
			prepared.metadata.KeepSource = true
			if err := subtitle.SaveMetadata(prepared.sidecars.metadata.staged, prepared.metadata); err != nil {
				return nil, err
			}
		}
		var files []subtitle.AssignmentInstall
		for _, file := range prepared.sidecars.before {
			if strings.HasPrefix(file.target, season+string(os.PathSeparator)) {
				files = append(files, subtitle.AssignmentInstall{Staged: file.staged, Target: file.target})
			} else {
				files = append(files, subtitle.AssignmentInstall{Staged: file.staged, Target: file.target, PreserveExisting: true})
			}
		}
		files = append(files, subtitle.AssignmentInstall{Staged: prepared.sidecars.metadata.staged, Target: prepared.sidecars.metadata.target}, subtitle.AssignmentInstall{Staged: prepared.video, Target: target})
		if err = lease.Prepare(assignment.OperationID, lease.Ledger().Revision, files); err != nil {
			return nil, err
		}
	}
	expected.AssignmentRevision = lease.Ledger().Revision
	observation = pipeline.LibraryIndexObservation{}
	request.Phase = "append_commit"
	request.CommitFiles = func() error {
		if err := lease.CheckInputs(assignment.OperationID); err != nil {
			return err
		}
		return lease.Finish(assignment.OperationID, expected.AssignmentRevision)
	}
	err = hook.LibraryPublication(ctx, request)
	if mergeErr := mergeAppendIndexMappings(lease, observation); mergeErr != nil {
		err = errors.Join(err, mergeErr)
	}
	expected.AssignmentRevision = lease.Ledger().Revision
	if err != nil {
		work.Close()
		_ = lease.Abort(assignment.OperationID)
		var conflict *pipeline.LibraryAppendIndexConflictError
		if errors.As(err, &conflict) {
			_ = lease.Block(conflict.Reason)
		}
		return nil, err
	}
	lease.Close()
	closed = true
	return finishPublished()
}

// RPC已结束且task短锁已释放，当前仍持season锁；成功观察立即成为后续保护前像。
func mergeAppendIndexMappings(lease *subtitle.AssignmentSeason, observation pipeline.LibraryIndexObservation) error {
	if observation.State != "verified" {
		return nil
	}
	var mappings []subtitle.AssignmentIndexMapping
	for _, m := range observation.Mappings {
		mappings = append(mappings, subtitle.AssignmentIndexMapping{Path: m.Path, FileID: m.FileID, EpisodeID: m.EpisodeID, ShowID: m.ShowID, SeasonID: m.SeasonID, CategoryID: m.CategoryID, LibraryID: m.LibraryID, Ordinal: m.Ordinal, SeasonNumber: m.SeasonNumber})
	}
	return lease.MergeIndex(mappings)
}
