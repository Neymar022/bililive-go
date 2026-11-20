package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

// 此组仅配合证据目录内c53_stage_fixture overlay运行；它不是B共同锁/CAS证明。
func TestStableAppendReviewFixes(t *testing.T) {
	if _, ok := any((*pipeline.Manager)(nil)).(pipeline.LibraryAppendCommitter); !ok {
		t.Skip("独立C基点无B adapter；使用证据内c53_stage_fixture，最终B/C接合需另验")
	}
	for _, mode := range []string{"recover", "cancelled", "foreign_sidecar"} {
		t.Run("F1_partial_install_"+mode, func(t *testing.T) {
			f := c53StageFixture(t, false)
			original := f.hook.LibraryPublication
			fault := true
			f.hook.LibraryPublication = func(ctx context.Context, req pipeline.LibraryPublicationRequest) error {
				if req.Phase != "append_commit" || !fault {
					return original(ctx, req)
				}
				fault = false
				missing := f.assignment.Files[1].Staged
				parked := missing + ".parked"
				require.NoError(t, os.Rename(missing, parked))
				err := original(ctx, req)
				require.NoError(t, os.Rename(parked, missing))
				return err
			}
			_, err := f.run()
			require.Error(t, err)
			require.FileExists(t, f.assignment.Files[0].Target)
			require.NoFileExists(t, f.assignment.TargetPath)
			ledger, err := subtitle.LoadLibraryAssignments(f.season)
			require.NoError(t, err)
			require.Equal(t, "committing", ledger.Assignments[0].State)
			switch mode {
			case "cancelled":
				f.task.Status = pipeline.PipelineStatusCancelled
				require.NoError(t, f.store.UpdateTask(context.Background(), f.task))
			case "foreign_sidecar":
				target := f.assignment.Files[0].Target
				require.NoError(t, os.Remove(target))
				require.NoError(t, os.WriteFile(target, []byte("external owner"), 0600))
			}
			files, err := f.run()
			if mode == "recover" {
				require.NoError(t, err)
				require.NotEmpty(t, files)
			} else {
				require.Error(t, err)
				require.NoFileExists(t, f.assignment.TargetPath)
			}
			ledger, err = subtitle.LoadLibraryAssignments(f.season)
			require.NoError(t, err)
			require.Len(t, ledger.Assignments, 1)
			require.Equal(t, f.assignment.OperationID, ledger.Assignments[0].OperationID)
			require.FileExists(t, f.source)
			if mode == "recover" {
				require.Equal(t, "published", ledger.Assignments[0].State)
			}
			if mode == "cancelled" {
				current, e := f.store.GetTask(context.Background(), f.task.ID)
				require.NoError(t, e)
				require.Equal(t, pipeline.PipelineStatusCancelled, current.Status)
			}
			if mode == "foreign_sidecar" {
				require.NotEmpty(t, ledger.BlockedReason)
				raw, e := os.ReadFile(f.assignment.Files[0].Target)
				require.NoError(t, e)
				require.Equal(t, "external owner", string(raw))
			}
		})
	}
	for _, mode := range []string{"running", "cancelled"} {
		t.Run("F2_visible_committing_"+mode, func(t *testing.T) {
			f := c53StageFixture(t, false)
			for _, file := range f.assignment.Files {
				require.NoError(t, os.Link(file.Staged, file.Target))
			}
			if mode == "cancelled" {
				f.task.Status = pipeline.PipelineStatusCancelled
				require.NoError(t, f.store.UpdateTask(context.Background(), f.task))
			}
			commits := 0
			observed := 0
			original := f.hook.LibraryPublication
			f.hook.LibraryPublication = func(ctx context.Context, req pipeline.LibraryPublicationRequest) error {
				if req.Phase == "append_commit" {
					commits++
				}
				if req.Phase == "published" {
					observed++
				}
				return original(ctx, req)
			}
			files, err := f.run()
			ledger, e := subtitle.LoadLibraryAssignments(f.season)
			require.NoError(t, e)
			require.Equal(t, "published", ledger.Assignments[0].State)
			require.Equal(t, f.assignment.OperationID, ledger.Assignments[0].OperationID)
			require.NotNil(t, ledger.Assignments[0].PublishedAt)
			if mode == "running" {
				require.NoError(t, err)
				require.NotEmpty(t, files)
			} else {
				require.Error(t, err)
				require.Equal(t, 0, commits)
				current, e := f.store.GetTask(context.Background(), f.task.ID)
				require.NoError(t, e)
				require.Equal(t, pipeline.PipelineStatusCancelled, current.Status)
			}
			require.Equal(t, 1, observed)
			for _, file := range f.assignment.Files {
				current, e := subtitle.AssignmentStat(file.Target)
				require.NoError(t, e)
				require.Equal(t, file.Identity.Device, current.Device)
				require.Equal(t, file.Identity.Inode, current.Inode)
				require.Equal(t, file.Identity.Size, current.Size)
			}
			require.FileExists(t, f.source)
		})
	}
	for _, mode := range []string{"changed", "missing"} {
		t.Run("F3_observed_mapping_"+mode, func(t *testing.T) {
			f := c53StageFixture(t, true)
			ledger, e := subtitle.LoadLibraryAssignments(f.season)
			require.NoError(t, e)
			var legacy string
			for _, entry := range ledger.LegacyEntries {
				if entry.Ordinal == 36 {
					legacy = entry.Path
					require.Nil(t, entry.KnownIndexMapping)
				}
			}
			require.NotEmpty(t, legacy)
			x := stableIndexRow(legacy, 36, 3036, 4036)
			rows := append(append([]map[string]any{}, f.rows...), x)
			stableWriteSnapshot(t, f.season, rows)
			original := f.hook.LibraryPublication
			changed := false
			f.hook.LibraryPublication = func(ctx context.Context, req pipeline.LibraryPublicationRequest) error {
				if req.Phase == "append_commit" && !changed {
					changed = true
					next := append([]map[string]any{}, f.rows...)
					if mode == "changed" {
						next = append(next, stableIndexRow(legacy, 36, 5036, 6036))
					}
					stableWriteSnapshot(t, f.season, next)
				}
				return original(ctx, req)
			}
			_, err := f.run()
			require.ErrorContains(t, err, "external_index_conflict")
			ledger, e = subtitle.LoadLibraryAssignments(f.season)
			require.NoError(t, e)
			for _, entry := range ledger.LegacyEntries {
				if entry.Path == legacy {
					require.NotNil(t, entry.KnownIndexMapping)
					require.Equal(t, "3036", entry.KnownIndexMapping.FileID)
					require.Equal(t, "4036", entry.KnownIndexMapping.EpisodeID)
				}
			}
			require.NotEmpty(t, ledger.BlockedReason)
			require.NoFileExists(t, f.assignment.TargetPath)
			require.FileExists(t, f.source)
		})
	}
}

type c53Fixture struct {
	season, root, source string
	assignment           subtitle.LibraryAssignment
	store                *pipeline.SQLiteStore
	task                 *pipeline.PipelineTask
	hook                 *pipeline.PipelineContext
	rows                 []map[string]any
}

func (f c53Fixture) run() ([]pipeline.FileInfo, error) {
	return (&SubtitleGenerateStage{}).publishCompletedSession(f.hook, configs.SubtitleKnowledgeSyncConfig{}, f.root, nil)
}
func c53StageFixture(t *testing.T, unknownLegacy bool) c53Fixture {
	t.Helper()
	season, old, at := stableBootstrap(t)
	var rows []map[string]any
	if unknownLegacy {
		season, at, rows = stableIndexFixture(t)
	} else {
		row := stableIndexRow(old, 1, 1, 2)
		stat, e := os.Stat(old)
		require.NoError(t, e)
		row["file_size"] = stat.Size()
		rows = []map[string]any{row}
	}
	root := filepath.Dir(filepath.Dir(season))
	_, hidden, e := liveSessionSegmentRoots(root)
	require.NoError(t, e)
	input, metadataPath, metadata := writeCompletedKnowledgeSessionSidecarAt(t, filepath.Join(hidden, "c53"), "Host", chronologicalLiveSessionEpisodeIdentity(at.Add(time.Hour)), at.Add(time.Hour), "synthetic")
	source := filepath.Join(filepath.Dir(root), "source.mp4")
	require.NoError(t, os.WriteFile(source, []byte("retained source"), 0600))
	store, e := pipeline.NewSQLiteStore(filepath.Join(filepath.Dir(root), "c53.db"))
	require.NoError(t, e)
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	require.NoError(t, store.OpenRecordingSession(ctx, "c53-session", "c53-room"))
	origin, e := store.BeginRecordingProducer(ctx, "c53-room")
	require.NoError(t, e)
	task := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "c53-room", LiveSessionID: origin.SessionID, RecordingProducerID: origin.ProducerID, HostName: "Host", StartTime: at.Add(time.Hour)}, &pipeline.PipelineConfig{Stages: []pipeline.StageConfig{{Name: pipeline.StageNameSubtitleGenerate}}}, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
	now := time.Now().UTC()
	task.Status = pipeline.PipelineStatusRunning
	task.StartedAt = &now
	require.NoError(t, store.CreateTask(ctx, task))
	metadata.SourcePath = source
	metadata.KeepSource = true
	metadata.RecordMeta["live_session_id"] = origin.SessionID
	metadata.RecordMeta["pipeline_task_id"] = fmt.Sprint(task.ID)
	metadata.RecordMeta["recording_producer_id"] = origin.ProducerID
	require.NoError(t, subtitle.SaveMetadata(metadataPath, metadata))
	require.NoError(t, store.CompleteRecordingTaskMedia(ctx, origin.SessionID, task.ID, []pipeline.SessionMediaSource{{InputPath: source, LibraryPath: input, MetadataPath: metadataPath}}))
	require.NoError(t, store.RecordCaptureEvidence(ctx, origin, true, ""))
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	require.NoError(t, store.EndRecordingSession(ctx, "c53-room", "normal"))
	manifest := knowledgeSessionManifest{PublicationVersion: 1, SourceID: "live-session:" + origin.SessionID, LiveSessionID: origin.SessionID}
	payload := knowledgeSessionPayloadInput{TaskID: fmt.Sprintf("bililive-go-%d", task.ID), LibraryPath: input, MetadataPath: metadataPath, Metadata: &metadata}
	_, e = registerKnowledgeSessionSource(&manifest, root, payload, now)
	require.NoError(t, e)
	require.NoError(t, saveKnowledgeSessionManifest(knowledgeSessionManifestPath(root, origin.SessionID), manifest))
	target, e := liveSessionAggregatePath([]knowledgeSessionPayloadInput{payload})
	require.NoError(t, e)
	target = filepath.Join(season, filepath.Base(target))
	a, _, e := subtitle.ReserveLibraryAssignment(ctx, season, origin.SessionID, target, []int64{task.ID})
	require.NoError(t, e)
	require.NoError(t, os.MkdirAll(a.StagingRoot, 0700))
	nfo, e := subtitle.BuildLibraryEpisodeNFO(target, at.Add(time.Hour), "fixture")
	require.NoError(t, e)
	aggregate := metadata
	aggregate.OutputPath = target
	aggregate.SRTPath = strings.TrimSuffix(target, ".mp4") + ".srt"
	aggregate.ASSPath = strings.TrimSuffix(target, ".mp4") + ".ass"
	aggregate.RecordMeta = copyRecordMeta(metadata.RecordMeta)
	aggregate.RecordMeta["live_session_media_role"] = "aggregate"
	aggregate.RecordMeta["live_session_media_content_hash"] = knowledgeSessionManifestContentHash(manifest)
	raw, e := json.Marshal(aggregate)
	require.NoError(t, e)
	var files []subtitle.AssignmentInstall
	for _, ext := range []string{".srt", ".ass", ".nfo", ".subtitle.json", ".jpg", ".mp4"} {
		content := []byte("synthetic prepared")
		if ext == ".nfo" {
			content = []byte(nfo)
		}
		if ext == ".subtitle.json" {
			content = raw
		}
		path := filepath.Join(a.StagingRoot, "prepared"+ext)
		require.NoError(t, os.WriteFile(path, content, 0600))
		files = append(files, subtitle.AssignmentInstall{Staged: path, Target: strings.TrimSuffix(target, ".mp4") + ext})
	}
	lease, e := subtitle.LockLibraryAssignments(ctx, season)
	require.NoError(t, e)
	require.NoError(t, lease.Prepare(a.OperationID, lease.Ledger().Revision, files))
	a = lease.Ledger().Assignments[0]
	lease.Close()
	path := stableWriteSnapshot(t, season, rows)
	manager := pipeline.NewManager(ctx, store, nil, nil)
	manager.SetLibraryIndexObserver(pipeline.NewLibraryIndexSnapshotObserver(path))
	hook := &pipeline.PipelineContext{Ctx: ctx, FFmpegPath: filepath.Join(root, "forbidden-no-prepare"), TaskID: task.ID, RecordInfo: task.RecordInfo, LibraryPublication: manager.EnsureLibraryPublication, SessionMediaReady: func([]pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
		return store.RecordingSession(ctx, origin.SessionID)
	}, PreserveRecoverySources: func() bool { return true }}
	// 任何误入媒体准备均直接失败，预置committing归属必须只收尾。
	prior := liveSessionMediaConcat
	liveSessionMediaConcat = func(context.Context, string, []string, string) error { return errors.New("unexpected concat") }
	t.Cleanup(func() { liveSessionMediaConcat = prior })
	return c53Fixture{season: season, root: root, source: source, assignment: a, store: store, task: task, hook: hook, rows: rows}
}

func stableAppendFixture(t *testing.T) (string, string, time.Time) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	season := filepath.Join(root, "video", "Host", "Season 01")
	require.NoError(t, os.MkdirAll(season, 0755))
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(season, fmt.Sprintf("Host.S01E%d.2026-09-01 - original.mp4", chronologicalLiveSessionEpisodeIdentity(at)))
	nfo, err := subtitle.BuildLibraryEpisodeNFO(path, at, "fixture")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("existing media"), 0600))
	require.NoError(t, os.WriteFile(path[:len(path)-4]+".nfo", []byte(nfo), 0600))
	return season, path, at
}

func TestStableAppendContract(t *testing.T) {
	t.Run("late_older_session_gets_next_ordinal", func(t *testing.T) {
		season, old, at := stableAppendFixture(t)
		before, err := os.ReadFile(old[:len(old)-4] + ".nfo")
		require.NoError(t, err)
		mapping := subtitle.AssignmentIndexMapping{Path: old, FileID: "1", EpisodeID: "2", ShowID: "3", CategoryID: "category", LibraryID: "4", SeasonNumber: 1, Ordinal: 1}
		p, err := subtitle.PlanLibraryAssignments(context.Background(), season, "enable-fixture", []subtitle.AssignmentIndexMapping{mapping}, nil)
		require.NoError(t, err)
		require.NoError(t, subtitle.ApplyLibraryAssignments(context.Background(), p))
		late := at.AddDate(0, -1, 0)
		target := filepath.Join(season, fmt.Sprintf("Host.S01E%d.%s - late.mp4", chronologicalLiveSessionEpisodeIdentity(late), late.Format("2006-01-02")))
		a, _, err := subtitle.ReserveLibraryAssignment(context.Background(), season, "late-session", target, []int64{12, 13})
		require.NoError(t, err)
		require.EqualValues(t, 2, a.Ordinal)
		nfo, err := subtitle.BuildLibraryEpisodeNFO(target, late, "fixture")
		require.NoError(t, err)
		require.Contains(t, nfo, "<episode>2</episode>")
		after, err := os.ReadFile(old[:len(old)-4] + ".nfo")
		require.NoError(t, err)
		require.Equal(t, before, after)
	})
	t.Run("concurrent_reservation_and_crash_boundaries", func(t *testing.T) {
		season, _, at := stableBootstrap(t)
		type answer struct {
			a   subtitle.LibraryAssignment
			err error
		}
		results := make(chan answer, 2)
		for i := 0; i < 2; i++ {
			go func(i int) {
				a, _, e := subtitle.ReserveLibraryAssignment(context.Background(), season, fmt.Sprint(i), stableTarget(season, at.Add(time.Duration(i+1)*time.Hour), fmt.Sprint(i)), []int64{int64(100 + i)})
				results <- answer{a, e}
			}(i)
		}
		first, second := <-results, <-results
		if first.err != nil {
			first, second = second, first
		}
		require.NoError(t, first.err)
		require.Error(t, second.err)
		require.EqualValues(t, 2, first.a.Ordinal)
		same, _, err := subtitle.ReserveLibraryAssignment(context.Background(), season, first.a.SessionID, first.a.TargetPath, first.a.MemberTaskIDs)
		require.NoError(t, err)
		require.Equal(t, first.a.OperationID, same.OperationID)
		files, revision := stablePrepare(t, season, first.a, at.Add(time.Duration(first.a.MemberTaskIDs[0]-99)*time.Hour))
		// 模拟公开媒体已安装而ledger终态尚未提交；不再次聚合，只补同inode文件。
		for _, f := range files {
			require.NoError(t, os.Link(f.Staged, f.Target))
		}
		lease, err := subtitle.LockLibraryAssignments(context.Background(), season)
		require.NoError(t, err)
		require.Error(t, lease.Abort(first.a.OperationID))
		require.NoError(t, lease.Finish(first.a.OperationID, revision))
		lease.Close()
		next, _, err := subtitle.ReserveLibraryAssignment(context.Background(), season, "after", stableTarget(season, at.Add(4*time.Hour), "after"), []int64{200})
		require.NoError(t, err)
		require.EqualValues(t, 3, next.Ordinal)
	})
	t.Run("cancel_before_media_releases_only_owned_reservation", func(t *testing.T) {
		season, old, at := stableBootstrap(t)
		a, _, err := subtitle.ReserveLibraryAssignment(context.Background(), season, "cancelled", stableTarget(season, at.Add(time.Hour), "cancel"), []int64{7})
		require.NoError(t, err)
		work, err := subtitle.LockAssignmentWork(a)
		require.NoError(t, err)
		lease, err := subtitle.LockLibraryAssignments(context.Background(), season)
		require.NoError(t, err)
		require.Error(t, lease.Abort(a.OperationID))
		lease.Close()
		work.Close()
		lease, err = subtitle.LockLibraryAssignments(context.Background(), season)
		require.NoError(t, err)
		require.NoError(t, lease.Abort(a.OperationID))
		lease.Close()
		next, _, err := subtitle.ReserveLibraryAssignment(context.Background(), season, "complete", stableTarget(season, at.Add(2*time.Hour), "next"), []int64{8})
		require.NoError(t, err)
		require.EqualValues(t, 2, next.Ordinal)
		require.FileExists(t, old)
	})
	t.Run("normal_writer_cannot_bypass_allocator_or_orphan_conflict", func(t *testing.T) {
		season, _, at := stableBootstrap(t)
		outside := stableTarget(season, at.Add(time.Hour), "unreserved")
		_, err := subtitle.BuildLibraryEpisodeNFO(outside, at.Add(time.Hour), "fixture")
		require.Error(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(season, "unrelated.nfo"), []byte("orphan"), 0600))
		a, _, err := subtitle.ReserveLibraryAssignment(context.Background(), season, "ok", outside, []int64{9})
		require.NoError(t, err)
		require.EqualValues(t, 2, a.Ordinal)
		lease, err := subtitle.LockLibraryAssignments(context.Background(), season)
		require.NoError(t, err)
		require.NoError(t, lease.Abort(a.OperationID))
		lease.Close()
		require.NoError(t, os.WriteFile(strings.TrimSuffix(outside, ".mp4")+".nfo", []byte("exact conflict"), 0600))
		_, _, err = subtitle.ReserveLibraryAssignment(context.Background(), season, "other", outside, []int64{10})
		require.Error(t, err)
	})

	t.Run("unknown_earlier_session_does_not_block_complete_candidate", func(t *testing.T) {
		season, _, at := stableBootstrap(t)
		ctx := context.Background()
		store, err := pipeline.NewSQLiteStore(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(season))), "pipeline.db"))
		require.NoError(t, err)
		defer store.Close()
		require.NoError(t, store.OpenRecordingSession(ctx, "ready-session", "room"))
		origin, err := store.BeginRecordingProducer(ctx, "room")
		require.NoError(t, err)
		source := filepath.Join(filepath.Dir(season), "source.mp4")
		require.NoError(t, os.WriteFile(source, []byte("completed source"), 0600))
		task := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "room", LiveSessionID: origin.SessionID, RecordingProducerID: origin.ProducerID, HostName: "Host", StartTime: at.Add(time.Hour)}, &pipeline.PipelineConfig{Stages: []pipeline.StageConfig{{Name: pipeline.StageNameSubtitleGenerate}}}, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
		require.NoError(t, store.CreateTask(ctx, task))
		require.NoError(t, store.CompleteRecordingTaskMedia(ctx, origin.SessionID, task.ID, []pipeline.SessionMediaSource{{InputPath: source, LibraryPath: source, MetadataPath: source + ".json"}}))
		require.NoError(t, store.RecordCaptureEvidence(ctx, origin, true, ""))
		require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
		require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
		require.NoError(t, store.OpenRecordingSession(ctx, "old-unknown", "earlier"))
		oldOrigin, err := store.BeginRecordingProducer(ctx, "earlier")
		require.NoError(t, err)
		earlier := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "earlier", LiveSessionID: oldOrigin.SessionID, RecordingProducerID: oldOrigin.ProducerID, HostName: "Host", StartTime: at.Add(-24 * time.Hour)}, task.PipelineConfig, task.CurrentFiles)
		earlier.Status = pipeline.PipelineStatusFailed
		require.NoError(t, store.CreateTask(ctx, earlier))
		target := stableTarget(season, task.RecordInfo.StartTime, "complete")
		_, _, err = subtitle.ReserveLibraryAssignment(ctx, season, origin.SessionID, target, []int64{task.ID})
		require.NoError(t, err)
		manager := pipeline.NewManager(ctx, store, nil, nil)
		require.NoError(t, manager.EnsureLibraryPublication(ctx, pipeline.LibraryPublicationRequest{LibraryRoot: filepath.Dir(filepath.Dir(season)), Path: target, SessionID: origin.SessionID, TaskID: task.ID, RecordedAt: task.RecordInfo.StartTime, Phase: "before"}))
	})
	t.Run("incomplete_session_lists_members_before_any_season_reservation", func(t *testing.T) {
		root := t.TempDir()
		session := pipeline.RecordingSession{ID: "incomplete", EndReason: "normal", Producers: map[string]bool{"p": true}}
		raw := []byte(`{"id":"incomplete","end_reason":"normal","producers":{"p":true},"tasks":{"41":{"ready":true},"42":{"ready":false}}}`)
		require.NoError(t, json.Unmarshal(raw, &session))
		stage := &SubtitleGenerateStage{}
		_, err := stage.publishCompletedSession(&pipeline.PipelineContext{Ctx: context.Background(), SessionMediaReady: func([]pipeline.SessionMediaSource) (pipeline.RecordingSession, error) { return session, nil }}, configs.SubtitleKnowledgeSyncConfig{}, root, nil)
		require.ErrorContains(t, err, "42")
		var incomplete *pipeline.LibrarySessionIncompleteError
		require.ErrorAs(t, err, &incomplete)
		require.Equal(t, []int64{42}, incomplete.BlockingMemberIDs)
		require.NoFileExists(t, filepath.Join(root, subtitle.LibraryAssignmentsFilename))
	})

	t.Run("index_delay_legacy_36_82_and_season_conflict_isolation", func(t *testing.T) {
		season, at, rows := stableIndexFixture(t)
		target := stableTarget(season, at, "new")
		a, _, err := subtitle.ReserveLibraryAssignment(context.Background(), season, "new-complete", target, []int64{300})
		require.NoError(t, err)
		require.EqualValues(t, 83, a.Ordinal)
		stablePublish(t, season, a, at)
		stat, err := os.Stat(target)
		require.NoError(t, err)
		expected := pipeline.LibraryIndexExpected{Scope: "append_by_admission", LibraryRoot: filepath.Dir(filepath.Dir(season)), Season: season, Show: "Host", Path: target, Identity: fmt.Sprint(a.Identity), Ordinal: 83, MediaSize: stat.Size()}
		path := stableWriteSnapshot(t, season, rows)
		observer := pipeline.NewLibraryIndexSnapshotObserver(path)
		pending, err := observer.VerifyPublication(context.Background(), expected)
		require.NoError(t, err)
		require.Equal(t, "index_unconfirmed", pending.State)
		require.Len(t, pending.LegacyUnconfirmed, 2)
		require.Len(t, pending.OwnedIndexPending, 1)
		next, _, err := subtitle.ReserveLibraryAssignment(context.Background(), season, "next-complete", stableTarget(season, at.Add(time.Hour), "next"), []int64{301})
		require.NoError(t, err)
		require.EqualValues(t, 84, next.Ordinal)
		row := stableIndexRow(target, 83, 1083, 2083)
		row["file_size"] = stat.Size()
		rows = append(rows, row)
		stableWriteSnapshot(t, season, rows)
		verified, err := observer.VerifyPublication(context.Background(), expected)
		require.NoError(t, err)
		require.Equal(t, "target_verified", verified.State)
		require.Len(t, verified.LegacyUnconfirmed, 2)
		// 已知mapping丢失是真冲突，不能以unknown豁免；另一个season仍可分配。
		rows = rows[1:]
		stableWriteSnapshot(t, season, rows)
		conflict, err := observer.VerifyPublication(context.Background(), expected)
		require.NoError(t, err)
		require.Equal(t, "external_index_conflict", conflict.State)
		other, _, otherAt := stableBootstrap(t)
		otherA, _, err := subtitle.ReserveLibraryAssignment(context.Background(), other, "independent", stableTarget(other, otherAt.Add(time.Hour), "independent"), []int64{400})
		require.NoError(t, err)
		require.EqualValues(t, 2, otherA.Ordinal)
	})

	t.Run("copy_only_blocks_compute_and_explicit_permission_enters_normalize", func(t *testing.T) {
		root := t.TempDir()
		ffmpeg := filepath.Join(root, "ffmpeg")
		ffprobe := filepath.Join(root, "ffprobe")
		log := filepath.Join(root, "commands.log")
		script := fmt.Sprintf("#!/bin/sh\nfor last do :; done\ncase \"$*\" in *'-f null'*) printf 'frame=60\\n'; exit 0;; esac\nprintf '%%s\\n' \"$*\" >> %q\nprintf 'normalized synthetic' > \"$last\"\n", log)
		require.NoError(t, os.WriteFile(ffmpeg, []byte(script), 0700))
		probe := fmt.Sprintf("#!/bin/sh\nfor last do :; done\ncase \"$last\" in *second.mp4) printf '%%s\\n' '%s';; *output.mp4) printf '%%s\\n' '%s';; *) printf '%%s\\n' '%s';; esac\n", stableProbeJSON(720, "1"), stableProbeJSON(1080, "2"), stableProbeJSON(1080, "1"))
		require.NoError(t, os.WriteFile(ffprobe, []byte(probe), 0700))
		paths := []string{filepath.Join(root, "first.mp4"), filepath.Join(root, "second.mp4")}
		for _, path := range paths {
			require.NoError(t, os.WriteFile(path, []byte("source"), 0600))
		}
		output := filepath.Join(root, "output.mp4")
		err := concatLiveSessionMediaResultOnly(context.Background(), ffmpeg, paths, output)
		require.ErrorIs(t, err, subtitle.ErrLibraryComputeRequired)
		require.NoFileExists(t, log)
		require.NoFileExists(t, output)
		require.NoError(t, concatLiveSessionMediaMode(context.Background(), ffmpeg, paths, output, false))
		command, err := os.ReadFile(log)
		require.NoError(t, err)
		require.Contains(t, string(command), "-filter_complex")
		require.FileExists(t, output)
	})
	t.Run("prepublication_index_gate_preserves_pending_without_early_upgrade", func(t *testing.T) {
		season, at, rows := stableIndexFixture(t)
		a, _, err := subtitle.ReserveLibraryAssignment(context.Background(), season, "published-owned", stableTarget(season, at, "owned"), []int64{500})
		require.NoError(t, err)
		// 快照含同路径行但生成于本次公开前，不能据此升级保护。
		ownedRow := stableIndexRow(a.TargetPath, 83, 1083, 2083)
		ownedRow["file_size"] = len("synthetic completed result")
		rows = append(rows, ownedRow)
		path := stableWriteSnapshot(t, season, rows)
		stablePublish(t, season, a, at)
		observer := pipeline.NewLibraryIndexSnapshotObserver(path)
		baseline := pipeline.LibraryIndexExpected{Scope: "append_by_admission", LibraryRoot: filepath.Dir(filepath.Dir(season)), Season: season, Show: "Host"}
		observed, err := observer.VerifyPublication(context.Background(), baseline)
		require.NoError(t, err)
		require.Equal(t, "verified", observed.State)
		require.Len(t, observed.OwnedIndexPending, 1)
		for _, mapping := range observed.Mappings {
			require.NotEqual(t, a.TargetPath, mapping.Path)
		}
		// 精确索引冲突在下一媒体提交前即可识别，普通缺新target索引不阻后场。
		rows = append(rows, stableIndexRow(filepath.Join(season, "external-missing.mp4"), 84, 1084, 2084))
		stableWriteSnapshot(t, season, rows)
		conflict, err := observer.VerifyPublication(context.Background(), baseline)
		require.NoError(t, err)
		require.Equal(t, "external_index_conflict", conflict.State)
	})

}

func stableBootstrap(t *testing.T) (string, string, time.Time) {
	t.Helper()
	season, path, at := stableAppendFixture(t)
	mapping := subtitle.AssignmentIndexMapping{Path: path, FileID: "1", EpisodeID: "2", ShowID: "3", CategoryID: "category", LibraryID: "4", SeasonNumber: 1, Ordinal: 1}
	p, err := subtitle.PlanLibraryAssignments(context.Background(), season, "enable-synthetic", []subtitle.AssignmentIndexMapping{mapping}, nil)
	require.NoError(t, err)
	require.NoError(t, subtitle.ApplyLibraryAssignments(context.Background(), p))
	return season, path, at
}
func stableTarget(season string, at time.Time, title string) string {
	return filepath.Join(season, fmt.Sprintf("Host.S01E%d.%s - %s.mp4", chronologicalLiveSessionEpisodeIdentity(at), at.Format("2006-01-02"), title))
}
func stablePrepare(t *testing.T, season string, a subtitle.LibraryAssignment, at time.Time) ([]subtitle.AssignmentInstall, int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(a.StagingRoot, 0700))
	nfo, err := subtitle.BuildLibraryEpisodeNFO(a.TargetPath, at, "fixture")
	require.NoError(t, err)
	var files []subtitle.AssignmentInstall
	for _, suffix := range []string{".srt", ".ass", ".nfo", ".subtitle.json", ".jpg", ".mp4"} {
		staged := filepath.Join(a.StagingRoot, "prepared"+suffix)
		content := []byte("synthetic completed result")
		if suffix == ".nfo" {
			content = []byte(nfo)
		}
		require.NoError(t, os.WriteFile(staged, content, 0600))
		files = append(files, subtitle.AssignmentInstall{Staged: staged, Target: strings.TrimSuffix(a.TargetPath, ".mp4") + suffix})
	}
	lease, err := subtitle.LockLibraryAssignments(context.Background(), season)
	require.NoError(t, err)
	defer lease.Close()
	require.NoError(t, lease.Prepare(a.OperationID, lease.Ledger().Revision, files))
	return files, lease.Ledger().Revision
}
func stablePublish(t *testing.T, season string, a subtitle.LibraryAssignment, at time.Time) {
	t.Helper()
	_, revision := stablePrepare(t, season, a, at)
	lease, err := subtitle.LockLibraryAssignments(context.Background(), season)
	require.NoError(t, err)
	defer lease.Close()
	require.NoError(t, lease.Finish(a.OperationID, revision))
}

func stableIndexFixture(t *testing.T) (string, time.Time, []map[string]any) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	season := filepath.Join(root, "video", "Host", "Season 01")
	require.NoError(t, os.MkdirAll(season, 0755))
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	var known []subtitle.AssignmentIndexMapping
	var missing []string
	var rows []map[string]any
	for i := 1; i <= 82; i++ {
		at := start.AddDate(0, 0, i)
		path := stableTarget(season, at, fmt.Sprint(i))
		nfo, err := subtitle.BuildLibraryEpisodeNFO(path, at, "fixture")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte("media"), 0600))
		require.NoError(t, os.WriteFile(strings.TrimSuffix(path, ".mp4")+".nfo", []byte(nfo), 0600))
		if i == 36 || i == 82 {
			missing = append(missing, path)
			continue
		}
		mapping := subtitle.AssignmentIndexMapping{Path: path, FileID: fmt.Sprint(1000 + i), EpisodeID: fmt.Sprint(2000 + i), ShowID: "3", CategoryID: "category", LibraryID: "4", Ordinal: int64(i), SeasonNumber: 1}
		known = append(known, mapping)
		rows = append(rows, stableIndexRow(path, int64(i), int64(1000+i), int64(2000+i)))
	}
	require.NoError(t, os.WriteFile(filepath.Join(season, "Host.S01E999999999999999.2026-01-01 - orphan.nfo"), []byte("orphan no media"), 0600))
	ctx := context.Background()
	_, err = subtitle.PlanLibraryAssignments(ctx, season, "unapproved", known, missing)
	require.ErrorContains(t, err, "unapproved legacy mapping missing")
	admission := &subtitle.LibraryLegacyAdmission{
		ApprovalPath: filepath.Join(root, "approval.json"),
		Approval: subtitle.LibraryLegacyApproval{
			Schema: 1, Season: season, ApprovedBy: "fixture", EvidenceRef: "synthetic",
			ApprovedAt: time.Now().UTC(),
		},
	}
	expected := pipeline.LibraryIndexExpected{Scope: "append_bootstrap", LibraryRoot: filepath.Dir(filepath.Dir(season)), Season: season, Show: "Host"}
	for _, ordinal := range []int64{36, 82} {
		at := start.AddDate(0, 0, int(ordinal))
		path := stableTarget(season, at, fmt.Sprint(ordinal))
		identity := fmt.Sprint((at.UnixMicro() - 1577836800000000) * 8)
		media, err := subtitle.AssignmentStat(path)
		require.NoError(t, err)
		admission.Approval.Entries = append(admission.Approval.Entries, subtitle.LibraryLegacyApprovalEntry{Path: path, Identity: identity, Ordinal: ordinal, Media: subtitle.LibraryLegacyMedia{Exists: true, Regular: true, Size: media.Size, Device: media.Device, Inode: media.Inode, MtimeNS: media.MtimeNS}, Reason: "existing_media_index_absent"})
		expected.BaselineLegacyUnconfirmed = append(expected.BaselineLegacyUnconfirmed, pipeline.LibraryIndexUnconfirmed{Path: path, Identity: identity, Ordinal: ordinal})
	}
	for _, m := range known {
		expected.Protected = append(expected.Protected, pipeline.LibraryIndexMapping{Path: m.Path, FileID: m.FileID, EpisodeID: m.EpisodeID, ShowID: m.ShowID, CategoryID: m.CategoryID, LibraryID: m.LibraryID, Ordinal: m.Ordinal, SeasonNumber: m.SeasonNumber})
	}
	observer := pipeline.NewLibraryIndexSnapshotObserver(stableWriteSnapshot(t, season, rows))
	observed, err := observer.VerifyPublication(ctx, expected)
	require.NoError(t, err)
	require.Equal(t, "index_unconfirmed", observed.State)
	require.Contains(t, observed.Reason, "unapproved bootstrap unknown")
	plan, err := subtitle.PlanLibraryAssignments(ctx, season, "enable-with-two-unknown", known, missing, admission)
	require.NoError(t, err)
	unapproved := *plan
	unapproved.LegacyAdmission = nil
	require.ErrorContains(t, subtitle.ApplyLibraryAssignments(ctx, &unapproved), "unapproved legacy mapping missing")
	require.NoError(t, subtitle.ApplyLibraryAssignments(ctx, plan))

	// 合成曾经以旧策略创建的持久账本，后续测试只检验历史条目重开。
	ledger, err := subtitle.LoadLibraryAssignments(season)
	require.NoError(t, err)
	ledger.LegacyAdmission = nil
	raw, err := json.Marshal(ledger)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(season, subtitle.LibraryAssignmentsFilename), raw, 0600))
	return season, start.AddDate(0, 0, 83), rows
}
func stableIndexRow(path string, ordinal, fileID, episodeID int64) map[string]any {
	return map[string]any{"path": path, "file_id": fileID, "file_size": int64(5), "category_id": "category", "library_id": 4, "season": 1, "episode": ordinal, "ordinal_file_count": 1, "audio_count": 1, "video_count": 1, "episodes": []map[string]any{{"id": episodeID, "category_id": "category", "library_id": 4, "season": 1, "episode": ordinal, "paths": []string{path}}}, "shows": []map[string]any{{"id": 3, "name": "Host", "category_id": "category", "library_id": 4}}}
}
func stableWriteSnapshot(t *testing.T, season string, rows []map[string]any) string {
	t.Helper()
	now := time.Now().UTC()
	snapshot := map[string]any{"schema_version": 2, "adapter_version": "ugreen-fixed-select-v2", "generated_at": now, "library_root": filepath.Dir(filepath.Dir(season)), "platform": map[string]any{"state": "confirmed", "pid": 123, "uid": 0, "start_ticks": 1, "started_at": now.Add(-time.Hour), "observed_at": now, "startup_path": subtitle.LibraryPlatformStartupPath, "build_path": subtitle.LibraryPlatformBuildPath, "build_sha256": strings.Repeat("b", 64)}, "libraries": []map[string]any{{"id": 4, "roots": []string{filepath.Dir(filepath.Dir(season))}, "read_local_info_first": true, "nfo_info_save_to_local": false}}, "files": rows}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	path := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(season))), "snapshot.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	return path
}

func stableProbeJSON(width int, duration string) string {
	video := map[string]any{"codec_type": "video", "codec_name": "h264", "codec_tag_string": "avc1", "profile": "High", "level": 40, "time_base": "1/90000", "extradata_hash": "SHA256:" + strings.Repeat("a", 64), "extradata_size": 32, "width": width, "height": 1920, "coded_width": width, "coded_height": 1920, "pix_fmt": "yuv420p", "sample_aspect_ratio": "1:1", "r_frame_rate": "30/1", "avg_frame_rate": "30/1", "field_order": "progressive", "color_range": "tv", "color_space": "bt709", "color_transfer": "bt709", "color_primaries": "bt709", "start_time": "0", "duration": duration, "nb_frames": "30"}
	audio := map[string]any{"codec_type": "audio", "codec_name": "aac", "codec_tag_string": "mp4a", "profile": "LC", "time_base": "1/48000", "extradata_hash": "SHA256:" + strings.Repeat("b", 64), "extradata_size": 5, "sample_fmt": "fltp", "sample_rate": "48000", "channels": 2, "channel_layout": "stereo", "start_time": "0", "duration": duration, "nb_frames": "48"}
	raw, _ := json.Marshal(map[string]any{"streams": []map[string]any{video, audio}, "format": map[string]any{"duration": duration, "start_time": "0"}})
	return string(raw)
}
