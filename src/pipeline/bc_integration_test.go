//go:build linux || darwin

package pipeline_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pipeline/stages"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

type bcFixture struct {
	*completionFixture
	clock                             time.Time
	season, oldPath, ffmpeg, commands string
	oldNFO                            []byte
	mapping                           subtitle.AssignmentIndexMapping
}

func TestBCIntegratedContract(t *testing.T) {
	t.Run("same_generation_day_receipt_restart_due_real_normalize", func(t *testing.T) {
		f := newBCFixture(t)
		task := f.claim(t, f.task.ID)
		_, err := f.execute(task, nil)
		var deferred *pipeline.DeferredExecution
		require.ErrorAs(t, err, &deferred)
		require.ErrorIs(t, deferred.Err, subtitle.ErrLibraryComputeRequired)
		require.Equal(t, "aggregate_compute_due", deferred.Reason)
		before, e := f.manager.GetTask(task.ID)
		require.NoError(t, e)
		require.NotNil(t, before.LibraryRecovery.AggregateCompute)
		require.Len(t, before.LibraryRecovery.CompletionRefs, 2)
		require.Len(t, before.LibraryRecovery.AggregateCompute.RequestIDs, 2)
		due := before.LibraryRecovery.AggregateCompute.DueAt
		require.Equal(t, 2, due.In(time.FixedZone("UTC8", 8*3600)).Hour())
		require.True(t, due.After(f.clock))
		require.True(t, due.Before(time.Now()), "C真实time.Now路径与原due必须相容")
		require.True(t, before.LibraryRecovery.AggregateCompute.Required)
		ledger, e := subtitle.LoadLibraryAssignments(f.season)
		require.NoError(t, e)
		require.Len(t, ledger.Assignments, 1)
		require.Equal(t, "aborted", ledger.Assignments[0].State)
		require.NoFileExists(t, ledger.Assignments[0].TargetPath)
		work, e := subtitle.LockAssignmentWork(ledger.Assignments[0])
		require.NoError(t, e)
		work.Close()
		// 原正式defer合并API保留not_before，不清空时间保护来使测试前进。
		task.MarkDeferred(due, 0, task.CurrentFiles)
		require.NoError(t, f.store.UpdateTask(f.ctx, task))
		calls, e := os.ReadFile(f.commands)
		if !os.IsNotExist(e) {
			require.NoError(t, e)
			require.NotContains(t, string(calls), "-filter_complex")
		}
		// 同季另一个完整场实际走C copy-only发布，不直接安装成功ledger或媒体。
		other := f.otherReady(t)
		other = f.claim(t, other.ID)
		_, e = f.execute(other, nil)
		require.NoError(t, e)
		ledger, e = subtitle.LoadLibraryAssignments(f.season)
		require.NoError(t, e)
		require.EqualValues(t, 2, ledger.Assignments[len(ledger.Assignments)-1].Ordinal)
		require.Equal(t, "published", ledger.Assignments[len(ledger.Assignments)-1].State)
		// 原receipt重领后，再关闭并重新打开真实Manager/SQLite；保持同代due。
		f.clock = f.clock.Add(30 * time.Minute)
		observations := before.LibraryRecovery.Observations
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		current, e := f.manager.GetTask(task.ID)
		require.NoError(t, e)
		require.Greater(t, current.LibraryRecovery.Observations, observations)
		f.checkDue(t, current, due)
		f.manager.Close(f.ctx)
		store, e := pipeline.NewSQLiteStore(f.database)
		require.NoError(t, e)
		f.store = store
		manager := pipeline.NewManager(f.ctx, store, nil, nil)
		manager.RegisterStage(pipeline.StageNameSubtitleGenerate, stages.NewSubtitleGenerateStage)
		f.manager = manager
		t.Cleanup(func() { manager.Close(f.ctx) })
		f.clock = f.clock.Add(7 * time.Hour)
		f.installManagerHooks()
		require.NoError(t, manager.ReconcileSavedCompletions())
		current, e = manager.GetTask(task.ID)
		require.NoError(t, e)
		f.checkDue(t, current, due)
		// ClaimTask在原due之前拒绝；到原due以真实CAS取得新的running claim。
		require.Error(t, store.ClaimTask(f.ctx, current, f.clock))
		f.clock = due.Add(time.Minute)
		task = f.claim(t, task.ID)
		output, e := f.execute(task, nil)
		require.NoError(t, e)
		require.NotEmpty(t, output)
		after, e := manager.GetTask(task.ID)
		require.NoError(t, e)
		require.False(t, after.LibraryRecovery.AggregateCompute.Required)
		require.Nil(t, after.LibraryRecovery.Deadline.ComputeDue)
		calls, e = os.ReadFile(f.commands)
		require.NoError(t, e)
		require.Equal(t, 1, strings.Count(string(calls), "-filter_complex"))
		for _, line := range strings.Split(string(calls), "\n") {
			if strings.Contains(line, "-filter_complex") {
				t.Logf("actual_normalize_argv=%s", line)
			}
		}
		t.Logf("fixed_due=%s original_day=%s request_ids=%v required_after_commit=%v", due.Format(time.RFC3339), before.LibraryRecovery.AggregateCompute.RequiredAt.Format(time.RFC3339), after.LibraryRecovery.AggregateCompute.RequestIDs, after.LibraryRecovery.AggregateCompute.Required)
		ledger, e = subtitle.LoadLibraryAssignments(f.season)
		require.NoError(t, e)
		published := 0
		primaryPublished := 0
		for _, a := range ledger.Assignments {
			if a.State == "published" && a.SessionID == task.RecordInfo.LiveSessionID {
				primaryPublished++
			}
			if a.State == "published" {
				published++
			}
		}
		require.Equal(t, 2, published)
		require.Equal(t, 1, primaryPublished)
		last := ledger.Assignments[len(ledger.Assignments)-1]
		require.EqualValues(t, 3, last.Ordinal)
		require.Equal(t, "published", last.State)
		require.Equal(t, task.RecordInfo.LiveSessionID, last.SessionID)
		f.checkPreserved(t)
	})
	for _, guard := range []string{"cancel", "hold"} {
		t.Run("snapshot_to_commit_"+guard, func(t *testing.T) {
			f := newBCFixture(t)
			task := f.claim(t, f.task.ID)
			_, err := f.execute(task, nil)
			var deferred *pipeline.DeferredExecution
			require.ErrorAs(t, err, &deferred)
			current, e := f.manager.GetTask(task.ID)
			require.NoError(t, e)
			due := current.LibraryRecovery.AggregateCompute.DueAt
			task.MarkDeferred(due, 0, task.CurrentFiles)
			require.NoError(t, f.store.UpdateTask(f.ctx, task))
			f.clock = due.Add(time.Minute)
			task = f.claim(t, task.ID)
			reached := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, e := f.execute(task, func(ctx context.Context, request pipeline.LibraryPublicationRequest) error {
					if request.Phase == "append_commit" {
						close(reached)
						<-release
					}
					return f.manager.EnsureLibraryPublication(ctx, request)
				})
				done <- e
			}()
			select {
			case <-reached:
			case e := <-done:
				t.Fatalf("未到真实提交屏障: %v", e)
			case <-time.After(10 * time.Second):
				t.Fatal("commit barrier timed out")
			}
			// prepare已实际结束且未取task短锁；真实Cancel/hold更新必须能完成。
			var hold time.Time
			if guard == "cancel" {
				require.NoError(t, f.manager.CancelTask(task.ID))
			} else {
				current, e = f.store.GetTask(f.ctx, task.ID)
				require.NoError(t, e)
				hold = time.Now().Add(time.Hour).UTC()
				current.NotBefore = &hold
				require.NoError(t, f.store.UpdateTask(f.ctx, current))
			}
			close(release)
			select {
			case err = <-done:
				require.Error(t, err)
				require.Contains(t, err.Error(), "append_task_claim_or_cancel_changed")
			case <-time.After(10 * time.Second):
				t.Fatal("guard return timed out")
			}
			current, e = f.manager.GetTask(task.ID)
			require.NoError(t, e)
			if guard == "cancel" {
				require.Equal(t, pipeline.PipelineStatusCancelled, current.Status)
			} else {
				require.True(t, current.NotBefore.Equal(hold))
				require.NotEqual(t, pipeline.PipelineStatusCompleted, current.Status)
			}
			ledger, e := subtitle.LoadLibraryAssignments(f.season)
			require.NoError(t, e)
			for _, a := range ledger.Assignments {
				require.NotEqual(t, "published", a.State)
				require.NoFileExists(t, a.TargetPath)
			}
			require.True(t, current.LibraryRecovery.AggregateCompute.Required)
			require.True(t, current.LibraryRecovery.Deadline.ComputeDue.Equal(due))
			f.checkPreserved(t)
		})
	}
	t.Run("invalid_journal_blocks_append_capability_before_reservation", func(t *testing.T) {
		f := newBCFixture(t)
		task := f.claim(t, f.task.ID)
		db, e := openBCDB(f.database)
		require.NoError(t, e)
		_, e = db.Exec(`UPDATE pipeline_library_recoveries SET record_json='{' WHERE task_id=?`, task.ID)
		require.NoError(t, e)
		require.NoError(t, db.Close())
		_, err := f.execute(task, nil)
		require.ErrorContains(t, err, "journal_invalid")
		ledger, e := subtitle.LoadLibraryAssignments(f.season)
		require.NoError(t, e)
		require.Empty(t, ledger.Assignments)
		f.checkPreserved(t)
	})
}

func newBCFixture(t *testing.T) *bcFixture {
	t.Helper()
	base := newCompletionFixture(t, 2)
	f := &bcFixture{completionFixture: base}
	// 使用过去服务日的真实02:00，后续C time.Now不需清空/伪造hold。
	local := time.FixedZone("UTC8", 8*3600)
	past := time.Now().In(local).AddDate(0, 0, -2)
	f.clock = time.Date(past.Year(), past.Month(), past.Day(), 12, 0, 0, 0, local)
	f.cfg.Subtitle.Schedule.Enabled = true
	f.cfg.Subtitle.Schedule.RunAt = "02:00"
	f.cfg.Subtitle.DeleteSourceOnCompletion = false
	subtitle.ConfigureWorkerSubmissions(false)
	f.completeSecondReceipt(t)
	f.installManagerHooks()
	require.NoError(t, f.manager.ReconcileSavedCompletions())
	ready, e := f.store.RecordingSession(f.ctx, f.task.RecordInfo.LiveSessionID)
	require.NoError(t, e)
	current, lookupErr := f.manager.GetTask(f.task.ID)
	require.NoError(t, lookupErr)
	require.True(t, ready.Tasks[fmt.Sprint(f.task.ID)].Ready, "reason=%s/%s, blocking=%v", current.LibraryRecovery.ReasonCode, current.LibraryRecovery.Reason, current.LibraryRecovery.BlockingInputs)
	require.NoError(t, f.store.RecordCaptureEvidence(f.ctx, pipeline.RecordingOrigin{SessionID: ready.ID, ProducerID: f.task.RecordInfo.RecordingProducerID}, true, ""))
	require.NoError(t, f.store.FinishRecordingProducer(f.ctx, pipeline.RecordingOrigin{SessionID: ready.ID, ProducerID: f.task.RecordInfo.RecordingProducerID}, ""))
	require.NoError(t, f.store.EndRecordingSession(f.ctx, "completion-room", "normal"))
	f.season = filepath.Join(f.cfg.Subtitle.LibraryRoot, "Host", "Season 01")
	require.NoError(t, os.MkdirAll(f.season, 0755))
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	identity := (at.UnixMicro() - 1577836800*1000000) * 8
	f.oldPath = filepath.Join(f.season, fmt.Sprintf("Host.S01E%d.2026-09-01 - preserved.mp4", identity))
	nfo, e := subtitle.BuildLibraryEpisodeNFO(f.oldPath, at, "fixture")
	require.NoError(t, e)
	f.oldNFO = []byte(nfo)
	require.NoError(t, os.WriteFile(f.oldPath, []byte("old-media"), 0600))
	require.NoError(t, os.WriteFile(strings.TrimSuffix(f.oldPath, ".mp4")+".nfo", f.oldNFO, 0600))
	f.mapping = subtitle.AssignmentIndexMapping{Path: f.oldPath, FileID: "101", EpisodeID: "201", ShowID: "301", CategoryID: "category", LibraryID: "4", Ordinal: 1, SeasonNumber: 1}
	plan, e := subtitle.PlanLibraryAssignments(f.ctx, f.season, "bc-enable", []subtitle.AssignmentIndexMapping{f.mapping}, nil)
	require.NoError(t, e)
	require.NoError(t, subtitle.ApplyLibraryAssignments(f.ctx, plan))
	f.ffmpeg, f.commands = bcTools(t, f.root)
	f.cfg.FfmpegPath = f.ffmpeg
	f.installManagerHooks()
	return f
}
func (f *bcFixture) installManagerHooks() {
	f.manager.SetLibraryRecoveryHooks(pipeline.LibraryRecoveryHooks{Now: func() time.Time { return f.clock }})
	if f.season != "" {
		f.manager.SetLibraryIndexObserver(pipeline.NewLibraryIndexSnapshotObserver(filepath.Join(f.root, "snapshot.json")))
		f.writeSnapshot()
	}
}
func (f *bcFixture) claim(t *testing.T, id int64) *pipeline.PipelineTask {
	t.Helper()
	task, e := f.store.GetTask(f.ctx, id)
	require.NoError(t, e)
	require.NoError(t, f.store.ClaimTask(f.ctx, task, f.clock))
	return task
}
func (f *bcFixture) checkDue(t *testing.T, task *pipeline.PipelineTask, due time.Time) {
	t.Helper()
	require.True(t, task.LibraryRecovery.AggregateCompute.Required)
	require.True(t, task.LibraryRecovery.AggregateCompute.DueAt.Equal(due))
	require.True(t, task.LibraryRecovery.Deadline.ComputeDue.Equal(due))
	require.NotNil(t, task.NotBefore)
	require.True(t, task.NotBefore.Equal(due))
}
func (f *bcFixture) execute(task *pipeline.PipelineTask, publication func(context.Context, pipeline.LibraryPublicationRequest) error) ([]pipeline.StageResult, error) {
	if publication == nil {
		publication = f.manager.EnsureLibraryPublication
	}
	pc := &pipeline.PipelineContext{Ctx: f.ctx, TaskID: task.ID, RecordInfo: task.RecordInfo, StartStage: task.CurrentStage, FFmpegPath: f.ffmpeg, LibraryPublication: publication, PreserveRecoverySources: func() bool { return true }, SessionMediaReady: func(sources []pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
		if err := f.store.CompleteRecordingTaskMedia(f.ctx, task.RecordInfo.LiveSessionID, task.ID, sources); err != nil {
			return pipeline.RecordingSession{}, err
		}
		return f.store.RecordingSession(f.ctx, task.RecordInfo.LiveSessionID)
	}}
	executor := pipeline.NewExecutor(nil)
	executor.RegisterStage(pipeline.StageNameSubtitleGenerate, stages.NewSubtitleGenerateStage)
	return executor.Execute(pc, task.PipelineConfig, task.CurrentFiles, nil)
}
func (f *bcFixture) checkPreserved(t *testing.T) {
	t.Helper()
	raw, e := os.ReadFile(strings.TrimSuffix(f.oldPath, ".mp4") + ".nfo")
	require.NoError(t, e)
	require.Equal(t, f.oldNFO, raw)
	require.FileExists(t, f.oldPath)
	oldBytes, readErr := os.ReadFile(f.oldPath)
	require.NoError(t, readErr)
	require.Equal(t, "old-media", string(oldBytes))
	owner, lookupErr := f.manager.GetTask(f.task.ID)
	require.NoError(t, lookupErr)
	if owner.LibraryRecovery != nil {
		for _, ref := range owner.LibraryRecovery.CompletionRefs {
			for _, path := range []string{ref.RequestPath, ref.ReceiptPath, ref.Request.OutputVideoPath, ref.Request.OutputSRTPath, ref.Request.OutputASSPath} {
				require.FileExists(t, path)
			}
		}
	}
	require.Zero(t, f.posts.Load())
	for _, file := range f.task.InitialFiles {
		require.FileExists(t, file.Path)
	}
	ledger, e := subtitle.LoadLibraryAssignments(f.season)
	require.NoError(t, e)
	require.Equal(t, f.mapping, *ledger.LegacyEntries[0].KnownIndexMapping)
}

func (f *bcFixture) completeSecondReceipt(t *testing.T) {
	t.Helper()
	request := bcJSON(t, filepath.Join(f.attempt, "worker-request.json"))
	result := bcJSON(t, f.receipt)
	video := request["output_video_path"].(string)
	require.NoError(t, os.WriteFile(video, []byte("bc-input-a"), 0600))
	outputs := result["output_identities"].(map[string]any)
	outputs["video"] = f.identity(t, video)
	result["completed_at"] = time.Now().UTC()
	result["receipt_created_at"] = time.Now().UTC()
	bcWrite(t, f.receipt, result)
	second := f.task.CurrentFiles[1].Path
	formal, e := subtitle.RecordingWorkPath(f.cfg.Subtitle.LibraryRoot, "completion-session", f.task.ID, 1, second, "Host", f.task.RecordInfo.StartTime)
	require.NoError(t, e)
	attempt := filepath.Join(filepath.Dir(formal), ".attempt-bc-second")
	require.NoError(t, os.MkdirAll(attempt, 0700))
	path := filepath.Join(attempt, filepath.Base(formal))
	srt := strings.TrimSuffix(path, ".mp4") + ".srt"
	ass := strings.TrimSuffix(path, ".mp4") + ".ass"
	request = bcClone(request)
	request["request_id"] = "7b34eac0-d40e-4461-a673-ab426d074d02"
	request["owner"].(map[string]any)["input_index"] = 1
	request["source_path"] = second
	request["formal_output_path"] = formal
	request["attempt_directory"] = attempt
	request["output_video_path"] = path
	request["output_srt_path"] = srt
	request["output_ass_path"] = ass
	request["record_meta"].(map[string]any)["recording_attempt_path"] = path
	bcWrite(t, filepath.Join(attempt, "worker-request.json"), request)
	for _, p := range []string{path, srt, ass} {
		content := "synthetic second sidecar"
		if p == path {
			content = "bc-input-b"
		}
		require.NoError(t, os.WriteFile(p, []byte(content), 0600))
	}
	executed := bcClone(result["executed_request"].(map[string]any))
	executed["source_path"] = second
	executed["output_video_path"] = path
	executed["output_srt_path"] = srt
	response := bcClone(result["response"].(map[string]any))
	response["ass_path"] = ass
	now := time.Now().UTC()
	bcWrite(t, filepath.Join(attempt, "worker-started.json"), map[string]any{"schema": 1, "request": request, "executed_request": executed, "source_identity": f.identity(t, second), "started_at": f.task.StartedAt})
	bcWrite(t, filepath.Join(attempt, "worker-result.json"), map[string]any{"schema": 1, "request": request, "executed_request": executed, "response": response, "source_identity": f.identity(t, second), "output_identities": map[string]any{"video": f.identity(t, path), "srt": f.identity(t, srt), "ass": f.identity(t, ass)}, "completed_at": now, "receipt_created_at": now})
	f.editRecord(t, func(record map[string]any) {
		refs := record["completion_refs"].([]any)
		ref := bcClone(refs[0].(map[string]any))
		ref["input_index"] = 1
		ref["source"] = second
		ref["formal_path"] = formal
		ref["attempt_path"] = attempt
		ref["request_id"] = request["request_id"]
		ref["request_path"] = filepath.Join(attempt, "worker-request.json")
		ref["receipt_path"] = filepath.Join(attempt, "worker-result.json")
		ref["request"] = request
		record["completion_refs"] = append(refs, ref)
	})
}
func (f *bcFixture) identity(t *testing.T, path string) map[string]any {
	t.Helper()
	info, e := os.Stat(path)
	require.NoError(t, e)
	rel, e := filepath.Rel(f.root, path)
	require.NoError(t, e)
	return map[string]any{"type": "regular", "shared_root_id": "synthetic-shared-root", "relative_path": rel, "inode": info.Sys().(*syscall.Stat_t).Ino, "size": info.Size(), "mtime_ns": info.ModTime().UnixNano()}
}
func (f *bcFixture) otherReady(t *testing.T) *pipeline.PipelineTask {
	t.Helper()
	require.NoError(t, f.store.OpenRecordingSession(f.ctx, "bc-other-session", "bc-other-room"))
	origin, e := f.store.BeginRecordingProducer(f.ctx, "bc-other-room")
	require.NoError(t, e)
	source := filepath.Join(f.root, "copy-source.mp4")
	require.NoError(t, os.WriteFile(source, []byte("bc-copy"), 0600))
	at := f.task.RecordInfo.StartTime.Add(24 * time.Hour)
	task := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "bc-other-room", LiveSessionID: origin.SessionID, RecordingProducerID: origin.ProducerID, HostName: "Host", StartTime: at}, f.task.PipelineConfig, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
	require.NoError(t, f.store.CreateTask(f.ctx, task))
	formal, e := subtitle.RecordingWorkPath(f.cfg.Subtitle.LibraryRoot, origin.SessionID, task.ID, 0, source, "Host", at)
	require.NoError(t, e)
	stem := strings.TrimSuffix(formal, ".mp4")
	for _, p := range []string{formal, stem + ".srt", stem + ".ass"} {
		content := "synthetic sidecar"
		if p == formal {
			content = "bc-copy"
		}
		require.NoError(t, os.WriteFile(p, []byte(content), 0600))
	}
	now := time.Now().UTC()
	metadata := subtitle.Metadata{Status: subtitle.StatusCompleted, RendererStatus: subtitle.StatusCompleted, SourcePath: source, OutputPath: formal, SRTPath: stem + ".srt", ASSPath: stem + ".ass", KeepSource: true, CompletedAt: &now, Language: "zh", Segments: []subtitle.Segment{{Index: 1, Start: "00:00:00,000", End: "00:00:00,010", Text: "synthetic"}}, RecordMeta: map[string]any{"pipeline_task_id": strconv.FormatInt(task.ID, 10), "live_session_id": origin.SessionID, "recording_producer_id": origin.ProducerID, "start_time": at.Format(time.RFC3339Nano)}}
	require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", metadata))
	require.NoError(t, f.store.CompleteRecordingTaskMedia(f.ctx, origin.SessionID, task.ID, []pipeline.SessionMediaSource{{InputPath: source, LibraryPath: formal, MetadataPath: stem + ".subtitle.json"}}))
	require.NoError(t, f.store.RecordCaptureEvidence(f.ctx, origin, true, ""))
	require.NoError(t, f.store.FinishRecordingProducer(f.ctx, origin, ""))
	require.NoError(t, f.store.EndRecordingSession(f.ctx, "bc-other-room", "normal"))
	return task
}
func (f *bcFixture) writeSnapshot() {
	now := time.Now().UTC()
	snapshot := map[string]any{"schema_version": 2, "adapter_version": "ugreen-fixed-select-v2", "generated_at": now, "library_root": f.cfg.Subtitle.LibraryRoot, "platform": map[string]any{"state": "confirmed", "pid": 123, "uid": 0, "start_ticks": 1, "started_at": now.Add(-time.Hour), "observed_at": now, "startup_path": subtitle.LibraryPlatformStartupPath, "build_path": subtitle.LibraryPlatformBuildPath, "build_sha256": strings.Repeat("b", 64)}, "libraries": []map[string]any{{"id": 4, "roots": []string{f.cfg.Subtitle.LibraryRoot}, "read_local_info_first": true, "nfo_info_save_to_local": false}}, "files": []map[string]any{{"path": f.oldPath, "file_id": 101, "file_size": len("old-media"), "category_id": "category", "library_id": 4, "season": 1, "episode": 1, "ordinal_file_count": 1, "audio_count": 1, "video_count": 1, "episodes": []map[string]any{{"id": 201, "category_id": "category", "library_id": 4, "season": 1, "episode": 1, "paths": []string{f.oldPath}}}, "shows": []map[string]any{{"id": 301, "name": "Host", "category_id": "category", "library_id": 4}}}}}
	raw, e := json.Marshal(snapshot)
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile(filepath.Join(f.root, "snapshot.json"), raw, 0600); e != nil {
		panic(e)
	}
}
func bcJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, e := os.ReadFile(path)
	require.NoError(t, e)
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&value))
	return value
}
func bcWrite(t *testing.T, path string, value any) {
	t.Helper()
	raw, e := json.Marshal(value)
	require.NoError(t, e)
	require.NoError(t, os.WriteFile(path, raw, 0600))
}
func bcClone(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	var clone map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&clone)
	return clone
}
func openBCDB(path string) (*sql.DB, error) { return sql.Open("sqlite", path) }
func bcTools(t *testing.T, root string) (string, string) {
	t.Helper()
	dir := filepath.Join(root, "tools")
	require.NoError(t, os.Mkdir(dir, 0700))
	log := filepath.Join(root, "ffmpeg-commands.log")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nfor last do :; done\ncase \"$*\" in *'-f null'*) printf 'frame=60\\n'; exit 0;; *'-filter_complex'*) printf 'bc-normalized' > \"$last\";; *) printf 'bc-cover' > \"$last\";; esac\n", log)
	require.NoError(t, os.WriteFile(ffmpeg, []byte(script), 0700))
	probe := fmt.Sprintf("#!/bin/sh\nfor last do :; done\ncase \"$*\" in *'-show_streams'*) value=$(cat \"$last\"); case \"$value\" in bc-normalized) printf '%%s\\n' '%s';; bc-input-b) printf '%%s\\n' '%s';; *) printf '%%s\\n' '%s';; esac;; *) printf '1\\n';; esac\n", bcProbe(1080, 2), bcProbe(720, 1), bcProbe(1080, 1))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ffprobe"), []byte(probe), 0700))
	return ffmpeg, log
}
func bcProbe(width, seconds int) string {
	video := map[string]any{"codec_type": "video", "codec_name": "h264", "codec_tag_string": "avc1", "profile": "High", "level": 40, "time_base": "1/90000", "extradata_hash": "SHA256:" + strings.Repeat("a", 64), "extradata_size": 32, "width": width, "height": 1920, "coded_width": width, "coded_height": 1920, "pix_fmt": "yuv420p", "sample_aspect_ratio": "1:1", "r_frame_rate": "30/1", "avg_frame_rate": "30/1", "field_order": "progressive", "color_range": "tv", "color_space": "bt709", "color_transfer": "bt709", "color_primaries": "bt709", "start_time": "0", "duration": fmt.Sprint(seconds), "nb_frames": fmt.Sprint(seconds * 30)}
	audio := map[string]any{"codec_type": "audio", "codec_name": "aac", "codec_tag_string": "mp4a", "profile": "LC", "time_base": "1/48000", "extradata_hash": "SHA256:" + strings.Repeat("b", 64), "extradata_size": 5, "sample_fmt": "fltp", "sample_rate": "48000", "channels": 2, "channel_layout": "stereo", "start_time": "0", "duration": fmt.Sprint(seconds), "nb_frames": fmt.Sprint(seconds * 48)}
	raw, _ := json.Marshal(map[string]any{"streams": []map[string]any{video, audio}, "format": map[string]any{"duration": fmt.Sprint(seconds), "start_time": "0"}})
	return string(raw)
}
