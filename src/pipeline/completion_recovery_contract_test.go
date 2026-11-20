//go:build linux || darwin

package pipeline_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bililive-go/bililive-go/src/servers"
	"github.com/gorilla/mux"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pipeline/stages"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

type completionFixture struct {
	ctx                                              context.Context
	store                                            *pipeline.SQLiteStore
	manager                                          *pipeline.Manager
	task                                             *pipeline.PipelineTask
	cfg                                              *configs.Config
	root, database, source, formal, attempt, receipt string
	posts                                            *atomic.Int32
}

func newCompletionFixture(t *testing.T, inputCount int) *completionFixture {
	ctx := context.Background()
	root, resolveErr := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, resolveErr)
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = filepath.Join(root, "video")
	cfg.Subtitle.KnowledgeSync.Enabled = false
	require.NoError(t, os.Mkdir(cfg.Subtitle.LibraryRoot, 0755))
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous); subtitle.ConfigureWorkerSubmissions(true) })
	t.Setenv("SUBTITLE_COMPLETION_SHARED_ROOT", root)
	t.Setenv("SUBTITLE_COMPLETION_SHARED_ROOT_ID", "synthetic-shared-root")
	var posts atomic.Int32
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		http.Error(w, "no computation allowed", 500)
	}))
	t.Cleanup(rpc.Close)
	t.Setenv("SUBTITLE_WORKER_URL", rpc.URL)
	database := filepath.Join(root, "pipeline.db")
	store, err := pipeline.NewSQLiteStore(database)
	require.NoError(t, err)
	require.NoError(t, store.OpenRecordingSession(ctx, "completion-session", "completion-room"))
	origin, err := store.BeginRecordingProducer(ctx, "completion-room")
	require.NoError(t, err)
	source := filepath.Join(root, "source.mp4")
	require.NoError(t, os.WriteFile(source, []byte("synthetic source"), 0600))
	task := pipeline.NewPipelineTask(pipeline.RecordInfo{LiveID: "completion-room", LiveSessionID: "completion-session", RecordingProducerID: origin.ProducerID, HostName: "Host", StartTime: time.Date(2026, 11, 4, 2, 0, 0, 0, time.UTC)}, &pipeline.PipelineConfig{Stages: []pipeline.StageConfig{{Name: pipeline.StageNameSubtitleGenerate, Options: map[string]any{pipeline.OptionSubtitleScheduled: true}}}}, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)})
	for i := 1; i < inputCount; i++ {
		extra := filepath.Join(root, fmt.Sprintf("source-%d.mp4", i))
		require.NoError(t, os.WriteFile(extra, []byte("synthetic extra input"), 0600))
		task.InitialFiles = append(task.InitialFiles, pipeline.NewVideoFileInfo(extra))
		task.CurrentFiles = task.InitialFiles
	}
	claim := time.Now().UTC()
	task.StartedAt = &claim
	task.Status = pipeline.PipelineStatusFailed
	require.NoError(t, store.CreateTask(ctx, task))
	formal, err := subtitle.RecordingWorkPath(cfg.Subtitle.LibraryRoot, "completion-session", task.ID, 0, source, "Host", task.RecordInfo.StartTime)
	require.NoError(t, err)
	attempt := filepath.Join(filepath.Dir(formal), ".attempt-synthetic")
	require.NoError(t, os.Mkdir(attempt, 0700))
	video := filepath.Join(attempt, filepath.Base(formal))
	srt := strings.TrimSuffix(video, ".mp4") + ".srt"
	ass := strings.TrimSuffix(video, ".mp4") + ".ass"
	meta := map[string]any{"pipeline_task_id": strconv.FormatInt(task.ID, 10), "live_session_id": "completion-session", "recording_producer_id": origin.ProducerID, "recording_attempt_path": video}
	style := cfg.Subtitle.BurnStyle
	style.Preset = style.GetEffectivePreset()
	snapshot := map[string]any{"provider": cfg.Subtitle.DefaultProvider, "language": cfg.Subtitle.Language, "burn_style": style}
	request := map[string]any{"schema": 1, "request_id": "6b34eac0-d40e-4461-a673-ab426d074d02", "owner": map[string]any{"task_id": task.ID, "session_id": "completion-session", "producer_id": origin.ProducerID, "input_index": 0}, "source_path": source, "formal_output_path": formal, "attempt_directory": attempt, "output_video_path": video, "output_srt_path": srt, "output_ass_path": ass, "record_meta": meta, "request_snapshot": snapshot}
	write := func(path string, value any) {
		b, e := json.Marshal(value)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(path, b, 0600))
	}
	write(filepath.Join(attempt, "worker-request.json"), request)
	for _, path := range []string{video, srt, ass} {
		require.NoError(t, os.WriteFile(path, []byte("synthetic closed output"), 0600))
	}
	identity := func(path string) map[string]any {
		info, e := os.Stat(path)
		require.NoError(t, e)
		rel, e := filepath.Rel(root, path)
		require.NoError(t, e)
		return map[string]any{"type": "regular", "shared_root_id": "synthetic-shared-root", "relative_path": rel, "inode": info.Sys().(*syscall.Stat_t).Ino, "size": info.Size(), "mtime_ns": info.ModTime().UnixNano()}
	}
	executed := map[string]any{"source_path": source, "output_video_path": video, "output_srt_path": srt, "provider": cfg.Subtitle.DefaultProvider, "language": cfg.Subtitle.Language, "burn_style": style}
	response := map[string]any{"segments": []map[string]any{{"index": 1, "start": "00:00:00,000", "end": "00:00:00,010", "text": "合成字幕"}}, "ass_path": ass, "render_preset": style.Preset, "actual_provider": "fixture", "actual_model": "fixture", "actual_burn_provider": "fixture"}
	write(filepath.Join(attempt, "worker-started.json"), map[string]any{"schema": 1, "request": request, "executed_request": executed, "source_identity": identity(source), "started_at": claim})
	write(filepath.Join(attempt, "worker-result.json"), map[string]any{"schema": 1, "request": request, "executed_request": executed, "response": response, "source_identity": identity(source), "output_identities": map[string]any{"video": identity(video), "srt": identity(srt), "ass": identity(ass)}, "completed_at": time.Now().UTC(), "receipt_created_at": time.Now().UTC()})
	expected := *task
	expected.Status = pipeline.PipelineStatusRunning
	ref := map[string]any{"input_index": 0, "source": source, "formal_path": formal, "attempt_path": attempt, "request_id": request["request_id"], "request_path": filepath.Join(attempt, "worker-request.json"), "receipt_path": filepath.Join(attempt, "worker-result.json"), "request": request, "request_snapshot": snapshot, "expected_task_fields": expected, "state": "registered"}
	record := map[string]any{"operation_id": fmt.Sprintf("task-%d", task.ID), "kind": "task", "season": "", "task_id": task.ID, "state": "completion_waiting", "revision": 1, "recovery_class": "worker_completion", "completion_refs": []any{ref}}
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", database)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO pipeline_library_recoveries(operation_id,kind,season,task_id,state,active,revision,record_json) VALUES(?,?,?,?,?,0,1,?)`, fmt.Sprintf("task-%d", task.ID), "task", "", task.ID, "completion_waiting", string(raw))
	require.NoError(t, err)
	require.NoError(t, db.Close())
	manager := pipeline.NewManager(ctx, store, &pipeline.ManagerConfig{MaxConcurrent: 1, PollInterval: 20 * time.Millisecond}, nil)
	manager.RegisterStage(pipeline.StageNameSubtitleGenerate, stages.NewSubtitleGenerateStage)
	f := &completionFixture{ctx: ctx, store: store, manager: manager, task: task, cfg: cfg, root: root, database: database, source: source, formal: formal, attempt: attempt, receipt: filepath.Join(attempt, "worker-result.json"), posts: &posts}
	t.Cleanup(func() { manager.Close(ctx) })
	return f
}

func (f *completionFixture) memberReady(t *testing.T) bool {
	t.Helper()
	session, err := f.store.RecordingSession(f.ctx, "completion-session")
	require.NoError(t, err)
	return session.Tasks[strconv.FormatInt(f.task.ID, 10)].Ready
}
func (f *completionFixture) editRecord(t *testing.T, edit func(map[string]any)) {
	t.Helper()
	db, err := sql.Open("sqlite", f.database)
	require.NoError(t, err)
	defer db.Close()
	var raw string
	require.NoError(t, db.QueryRow(`SELECT record_json FROM pipeline_library_recoveries WHERE operation_id=?`, fmt.Sprintf("task-%d", f.task.ID)).Scan(&raw))
	var value map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &value))
	edit(value)
	data, err := json.Marshal(value)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE pipeline_library_recoveries SET record_json=? WHERE operation_id=?`, string(data), fmt.Sprintf("task-%d", f.task.ID))
	require.NoError(t, err)
}
func (f *completionFixture) clearForCompute(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", f.database)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM pipeline_library_recoveries WHERE operation_id=?`, fmt.Sprintf("task-%d", f.task.ID))
	require.NoError(t, err)
	require.NoError(t, db.Close())
	f.task.Status = pipeline.PipelineStatusPending
	f.task.StartedAt = nil
	require.NoError(t, f.store.UpdateTask(f.ctx, f.task))
}

func TestCompletionRecoveryContract(t *testing.T) {
	// 自有微型子进程正常退出而不跑 Close，留下本接口真实 owner 证明与无 listener 的 socket。
	if appData := os.Getenv("BILILIVE_CONTROL_CRASH_FIXTURE"); appData != "" {
		database := os.Getenv("BILILIVE_CONTROL_CRASH_DB")
		require.NotEmpty(t, database)
		store, err := pipeline.NewSQLiteStore(database)
		require.NoError(t, err)
		manager := pipeline.NewManager(context.Background(), store, nil, nil)
		_, err = servers.StartWorkerSubmissionControl(appData, manager)
		require.NoError(t, err)
		os.Exit(0)
	}

	t.Run("member_before_session_seal", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		require.NoError(t, f.manager.Start(f.ctx))
		require.Eventually(t, func() bool { return f.memberReady(t) }, time.Second, 10*time.Millisecond)
		require.Zero(t, f.posts.Load())
	})
	t.Run("cancel_before_commit", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		var once sync.Once
		f.manager.SetLibraryRecoveryHooks(pipeline.LibraryRecoveryHooks{Boundary: func(name string) error {
			if name == "completion_before_commit" {
				once.Do(func() { require.NoError(t, f.manager.CancelTask(f.task.ID)) })
			}
			return nil
		}})
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.False(t, f.memberReady(t))
		_, err := os.Lstat(f.formal)
		require.True(t, os.IsNotExist(err))
		current, err := f.store.GetTask(f.ctx, f.task.ID)
		require.NoError(t, err)
		require.Equal(t, pipeline.PipelineStatusCancelled, current.Status)
		_, err = os.Stat(f.receipt)
		require.NoError(t, err)
	})
	t.Run("files_before_db_restart_reentry", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		f.manager.SetLibraryRecoveryHooks(pipeline.LibraryRecoveryHooks{Boundary: func(name string) error {
			if name == "completion_files_committed" {
				return errors.New("synthetic DB boundary crash")
			}
			return nil
		}})
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.False(t, f.memberReady(t))
		before, err := os.Stat(f.formal)
		require.NoError(t, err)
		f.manager.Close(f.ctx)
		reopened, err := pipeline.NewSQLiteStore(f.database)
		require.NoError(t, err)
		restarted := pipeline.NewManager(f.ctx, reopened, nil, nil)
		restarted.RegisterStage(pipeline.StageNameSubtitleGenerate, stages.NewSubtitleGenerateStage)
		restarted.SetLibraryRecoveryHooks(pipeline.LibraryRecoveryHooks{Now: func() time.Time { return time.Now().Add(7 * time.Hour) }})
		defer restarted.Close(f.ctx)
		require.NoError(t, restarted.ReconcileSavedCompletions())
		session, err := reopened.RecordingSession(f.ctx, "completion-session")
		require.NoError(t, err)
		require.True(t, session.Tasks[strconv.FormatInt(f.task.ID, 10)].Ready)
		after, err := os.Stat(f.formal)
		require.NoError(t, err)
		require.True(t, os.SameFile(before, after))
		require.Zero(t, f.posts.Load())
	})
	t.Run("concurrent_receipt_claim", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = f.manager.ReconcileSavedCompletions() }()
		}
		wg.Wait()
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.True(t, f.memberReady(t))
		formal, err := os.Stat(f.formal)
		require.NoError(t, err)
		original, err := os.Stat(filepath.Join(f.attempt, filepath.Base(f.formal)))
		require.NoError(t, err)
		require.True(t, os.SameFile(formal, original))
		require.Zero(t, f.posts.Load())
	})
	t.Run("partial_inputs_not_registered_ready", func(t *testing.T) {
		f := newCompletionFixture(t, 2)
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.False(t, f.memberReady(t))
		current, err := f.manager.GetTask(f.task.ID)
		require.NoError(t, err)
		require.Equal(t, pipeline.PipelineStatusFailed, current.Status)
		require.Equal(t, []int{1}, current.LibraryRecovery.BlockingInputs)
		require.Equal(t, []int64{f.task.ID}, current.LibraryRecovery.BlockingMembers)
		require.Zero(t, f.posts.Load())
	})
	for _, computeOnly := range []bool{false, true} {
		name := "explicit_hold_preserved"
		if computeOnly {
			name = "compute_hold_released"
		}
		t.Run(name, func(t *testing.T) {
			f := newCompletionFixture(t, 1)
			future := time.Now().UTC().Add(12 * time.Hour)
			f.task.NotBefore = &future
			require.NoError(t, f.store.UpdateTask(f.ctx, f.task))
			if computeOnly {
				f.editRecord(t, func(r map[string]any) {
					r["deadline"] = map[string]any{"compute_due": future, "applied_not_before": future, "provenance_revision": 1}
				})
			}
			require.NoError(t, f.manager.ReconcileSavedCompletions())
			current, err := f.store.GetTask(f.ctx, f.task.ID)
			require.NoError(t, err)
			require.True(t, f.memberReady(t))
			require.Equal(t, pipeline.PipelineStatusPending, current.Status)
			require.NotNil(t, current.NotBefore)
			if computeOnly {
				require.True(t, current.NotBefore.Before(time.Now().Add(time.Minute)))
			} else {
				require.True(t, current.NotBefore.Equal(future))
			}
		})
	}
	t.Run("known_compute_deadline_existing_checkpoint", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		task, err := f.store.GetTask(f.ctx, f.task.ID)
		require.NoError(t, err)
		task.Status = pipeline.PipelineStatusFailed
		future := time.Now().UTC().Add(12 * time.Hour)
		task.NotBefore = &future
		require.NoError(t, f.store.UpdateTask(f.ctx, task))
		f.editRecord(t, func(r map[string]any) {
			delete(r, "completion_refs")
			r["recovery_class"] = "deadline"
			r["next_attempt_at"] = time.Now().Add(-time.Minute)
			r["deadline"] = map[string]any{"compute_due": future, "applied_not_before": future, "provenance_revision": 1}
		})
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		current, err := f.manager.GetTask(f.task.ID)
		require.NoError(t, err)
		require.Equal(t, pipeline.PipelineStatusPending, current.Status)
		require.True(t, current.NotBefore.Before(time.Now().Add(time.Minute)))
		require.Equal(t, "completed_media", current.LibraryRecovery.RecoveryClass)
		require.Zero(t, f.posts.Load())
	})
	t.Run("new_compute_still_nightly", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		f.clearForCompute(t)
		f.cfg.Subtitle.Schedule.Enabled = true
		f.cfg.Subtitle.Schedule.RunAt = "02:00"
		require.NoError(t, f.manager.Start(f.ctx))
		require.Eventually(t, func() bool {
			task, err := f.manager.GetTask(f.task.ID)
			return err == nil && task.LibraryRecovery != nil && task.LibraryRecovery.Deadline != nil && task.LibraryRecovery.Deadline.ComputeDue != nil
		}, time.Second, 10*time.Millisecond)
		require.Zero(t, f.posts.Load())
		_, err := os.Lstat(strings.TrimSuffix(f.formal, ".mp4") + ".subtitle.json")
		require.True(t, os.IsNotExist(err))
	})
	t.Run("persisted_gate_blocks_post_but_not_receipt", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		shortRoot, err := os.MkdirTemp("/tmp", "bgs-")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
		appData, err := filepath.EvalSymlinks(shortRoot)
		require.NoError(t, err)
		control, err := servers.StartWorkerSubmissionControl(appData, f.manager)
		require.NoError(t, err)
		defer control.Close(context.Background())
		socket := filepath.Join(appData, "worker-submissions", "control.sock")
		info, err := os.Stat(socket)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
		_, err = servers.StartWorkerSubmissionControl(appData, f.manager)
		require.Error(t, err, "活动控制 listener 不能被删")
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		client := &http.Client{Transport: transport, Timeout: time.Second}
		defer transport.CloseIdleConnections()
		request, err := http.NewRequest(http.MethodPut, "http://unix/api/pipeline/worker-submissions", strings.NewReader(`{"accepting":false}`))
		require.NoError(t, err)
		response, err := client.Do(request)
		require.NoError(t, err)
		require.Equal(t, 200, response.StatusCode)
		require.NoError(t, response.Body.Close())
		router := mux.NewRouter()
		servers.RegisterPipelineHandlers(router.PathPrefix("/api").Subrouter(), f.manager)
		tcp := httptest.NewServer(router)
		defer tcp.Close()
		request, err = http.NewRequest(http.MethodPut, tcp.URL+"/api/pipeline/worker-submissions", strings.NewReader(`{"accepting":true}`))
		require.NoError(t, err)
		response, err = tcp.Client().Do(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
		require.NoError(t, response.Body.Close())
		status, err := f.manager.WorkerSubmissionsStatus()
		require.NoError(t, err)
		require.Equal(t, false, status["accepting"])
		require.NoError(t, control.Close(context.Background()))
		transport.CloseIdleConnections()
		binary, err := os.Executable()
		require.NoError(t, err)
		childCtx, childCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer childCancel()
		child := exec.CommandContext(childCtx, binary, "-test.run=^TestCompletionRecoveryContract$")
		child.Env = []string{"BILILIVE_CONTROL_CRASH_FIXTURE=" + appData, "BILILIVE_CONTROL_CRASH_DB=" + f.database}
		output, err := child.CombinedOutput()
		require.NoError(t, err, string(output))
		control, err = servers.StartWorkerSubmissionControl(appData, f.manager)
		require.NoError(t, err, "只清理同owner且ECONNREFUSED的陈旧socket")
		require.NoError(t, control.Close(context.Background()))
		require.NoError(t, os.WriteFile(socket, []byte("保留普通文件"), 0600))
		_, err = servers.StartWorkerSubmissionControl(appData, f.manager)
		require.Error(t, err)
		kept, err := os.ReadFile(socket)
		require.NoError(t, err)
		require.Equal(t, "保留普通文件", string(kept))
		require.NoError(t, os.Remove(socket))
		f.manager.Close(f.ctx)
		reopened, err := pipeline.NewSQLiteStore(f.database)
		require.NoError(t, err)
		restarted := pipeline.NewManager(f.ctx, reopened, nil, nil)
		restarted.RegisterStage(pipeline.StageNameSubtitleGenerate, stages.NewSubtitleGenerateStage)
		defer restarted.Close(f.ctx)
		require.NoError(t, restarted.Start(f.ctx))
		control, err = servers.StartWorkerSubmissionControl(appData, restarted)
		require.NoError(t, err)
		defer control.Close(context.Background())
		status, err = restarted.WorkerSubmissionsStatus()
		require.NoError(t, err)
		require.Equal(t, false, status["accepting"])
		_, err = subtitle.ProcessFileWithRetry("http://invalid.example", subtitle.ProcessRequest{}, 1)
		require.ErrorIs(t, err, subtitle.ErrWorkerSubmissionsClosed)
		require.Eventually(t, func() bool {
			session, e := reopened.RecordingSession(f.ctx, "completion-session")
			return e == nil && session.Tasks[strconv.FormatInt(f.task.ID, 10)].Ready
		}, time.Second, 10*time.Millisecond)
		require.Zero(t, f.posts.Load())
	})

	t.Run("late_receipt_after_unknown", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		bytes, err := os.ReadFile(f.receipt)
		require.NoError(t, err)
		require.NoError(t, os.Remove(f.receipt))
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.False(t, f.memberReady(t))
		require.NoError(t, os.WriteFile(f.receipt, bytes, 0600))
		f.manager.SetLibraryRecoveryHooks(pipeline.LibraryRecoveryHooks{Now: func() time.Time { return time.Now().Add(7 * time.Hour) }})
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.True(t, f.memberReady(t))
		require.Zero(t, f.posts.Load())
	})
	t.Run("normal_http_lost_response_then_background", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		f.clearForCompute(t)
		f.cfg.Subtitle.Schedule.Enabled = false
		t.Setenv("BILILIVE_WORKER_COMPLETION_V1", "true")
		var calls atomic.Int32
		rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "completion_v1": map[string]any{"supported": true, "enabled": true, "schema": 1, "identity_mode": "shared-root-relative-inode"}})
				return
			}
			calls.Add(1)
			var req subtitle.ProcessRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.CompletionV1 == nil {
				http.Error(w, "missing protocol", 400)
				return
			}
			protocol := req.CompletionV1
			task, err := f.manager.GetTask(f.task.ID)
			if err != nil || task.LibraryRecovery == nil || len(task.LibraryRecovery.CompletionRefs) != 1 || task.LibraryRecovery.CompletionRefs[0].RequestID != protocol.RequestID {
				http.Error(w, "journal must precede POST", 400)
				return
			}
			if e := f.manager.SetWorkerSubmissions(false); e != nil {
				http.Error(w, "fixture gate persistence", 500)
				return
			}
			gate, e := f.manager.WorkerSubmissionsStatus()
			if e != nil || gate["inflight"] != 1 {
				http.Error(w, "inflight must include this POST", 500)
				return
			}
			write := func(path string, v any) error {
				b, e := json.Marshal(v)
				if e != nil {
					return e
				}
				return os.WriteFile(path, b, 0600)
			}
			for _, path := range []string{protocol.OutputVideoPath, protocol.OutputSRTPath, protocol.OutputASSPath} {
				if err = os.WriteFile(path, []byte("synthetic closed output"), 0600); err != nil {
					http.Error(w, "fixture IO", 500)
					return
				}
			}
			source, err := subtitle.CompletionFileIdentity(protocol.SourcePath)
			if err != nil {
				http.Error(w, "fixture identity", 500)
				return
			}
			identities := map[string]subtitle.WorkerFileIdentity{}
			for key, path := range map[string]string{"video": protocol.OutputVideoPath, "srt": protocol.OutputSRTPath, "ass": protocol.OutputASSPath} {
				value, e := subtitle.CompletionFileIdentity(path)
				if e != nil {
					http.Error(w, "fixture identity", 500)
					return
				}
				identities[key] = value
			}
			executed := map[string]any{"source_path": req.SourcePath, "output_video_path": req.OutputVideoPath, "output_srt_path": req.OutputSRTPath, "provider": req.Provider, "language": req.Language, "burn_style": req.BurnStyle}
			response := subtitle.ProcessResponse{Segments: []subtitle.Segment{{Index: 1, Start: "00:00:00,000", End: "00:00:00,010", Text: "合成字幕"}}, ASSPath: protocol.OutputASSPath, RenderPreset: req.BurnStyle.Preset, ActualProvider: "fixture", ActualModel: "fixture", ActualBurnProvider: "fixture"}
			if write(filepath.Join(protocol.AttemptDirectory, "worker-started.json"), map[string]any{"schema": 1, "request": protocol, "executed_request": executed, "source_identity": source}) != nil || write(filepath.Join(protocol.AttemptDirectory, "worker-result.json"), subtitle.WorkerCompletionReceipt{Schema: 1, Request: *protocol, ExecutedRequest: executed, Response: response, SourceIdentity: source, OutputIdentities: identities, CompletedAt: time.Now().UTC(), ReceiptCreatedAt: time.Now().UTC()}) != nil {
				http.Error(w, "fixture IO", 500)
				return
			}
			http.Error(w, "synthetic lost HTTP response after durable receipt", 500)
		}))
		defer rpc.Close()
		t.Setenv("SUBTITLE_WORKER_URL", rpc.URL)
		require.NoError(t, f.manager.Start(f.ctx))
		require.Eventually(t, func() bool { return f.memberReady(t) }, 2*time.Second, 10*time.Millisecond)
		require.Equal(t, int32(1), calls.Load())
		meta, err := subtitle.LoadMetadata(strings.TrimSuffix(f.formal, ".mp4") + ".subtitle.json")
		require.NoError(t, err)
		require.Equal(t, "00:00:00,010", meta.Segments[0].End)
	})
	t.Run("config_changed_refused", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		f.cfg.Subtitle.Language = "changed-language"
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.False(t, f.memberReady(t))
		_, err := os.Stat(f.formal)
		require.True(t, os.IsNotExist(err))
		require.Zero(t, f.posts.Load())
	})
	t.Run("gate_closes_between_precheck_and_entry", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		f.clearForCompute(t)
		f.cfg.Subtitle.Schedule.Enabled = false
		t.Setenv("BILILIVE_WORKER_COMPLETION_V1", "true")
		var calls atomic.Int32
		rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				if f.manager.SetWorkerSubmissions(false) != nil {
					http.Error(w, "fixture gate", 500)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "completion_v1": map[string]any{"supported": true, "enabled": true, "schema": 1, "identity_mode": "shared-root-relative-inode"}})
				return
			}
			calls.Add(1)
			http.Error(w, "must not submit", 500)
		}))
		defer rpc.Close()
		t.Setenv("SUBTITLE_WORKER_URL", rpc.URL)
		require.NoError(t, f.manager.Start(f.ctx))
		require.Eventually(t, func() bool {
			meta, e := subtitle.LoadMetadata(strings.TrimSuffix(f.formal, ".mp4") + ".subtitle.json")
			if e != nil || meta.Status != subtitle.StatusQueued {
				return false
			}
			task, e := f.manager.GetTask(f.task.ID)
			return e == nil && task.Status == pipeline.PipelineStatusPending && task.LibraryRecovery != nil && len(task.LibraryRecovery.CompletionRefs) == 1 && task.LibraryRecovery.CompletionRefs[0].State == "queued_unsent"
		}, time.Second, 10*time.Millisecond)
		require.Zero(t, calls.Load())
		task, err := f.manager.GetTask(f.task.ID)
		require.NoError(t, err)
		require.NotNil(t, task.LibraryRecovery)
		require.Equal(t, "queued_unsent", task.LibraryRecovery.CompletionRefs[0].State)
	})
	t.Run("append_adapter_cancel_hold_and_full_frame", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.NoError(t, f.store.FinishRecordingProducer(f.ctx, pipeline.RecordingOrigin{SessionID: "completion-session", ProducerID: f.task.RecordInfo.RecordingProducerID}, ""))
		require.NoError(t, f.store.EndRecordingSession(f.ctx, "completion-room", "user_stop"))
		season := filepath.Join(f.cfg.Subtitle.LibraryRoot, "Host", "Season 01")
		target := filepath.Join(season, "synthetic.mp4")
		snapshot := func() *pipeline.LibraryAppendCommitRequest {
			frame := &pipeline.LibraryAppendCommitRequest{SeasonPath: season, TargetPath: target, OperationID: "synthetic-operation", AssignmentRevision: 1}
			current, err := f.store.GetTask(f.ctx, f.task.ID)
			require.NoError(t, err)
			session, err := f.store.RecordingSession(f.ctx, "completion-session")
			require.NoError(t, err)
			frame.ExpectedTask = current
			frame.ExpectedMembers = []*pipeline.PipelineTask{current}
			frame.ExpectedSession = session
			return frame
		}
		frame := snapshot()
		calls := 0
		callback := func() error { calls++; return nil }
		require.NoError(t, f.manager.WithLibraryAppendCommit(f.ctx, frame, callback))
		require.Equal(t, 1, calls)
		duplicate := *frame
		duplicate.ExpectedMembers = append(append([]*pipeline.PipelineTask{}, frame.ExpectedMembers...), frame.ExpectedMembers[0])
		require.Error(t, f.manager.WithLibraryAppendCommit(f.ctx, &duplicate, callback))
		require.Equal(t, 1, calls)
		current, err := f.store.GetTask(f.ctx, f.task.ID)
		require.NoError(t, err)
		hold := time.Now().UTC().Add(time.Hour)
		current.NotBefore = &hold
		require.NoError(t, f.store.UpdateTask(f.ctx, current))
		require.Error(t, f.manager.WithLibraryAppendCommit(f.ctx, frame, callback))
		frame = snapshot()
		require.Error(t, f.manager.WithLibraryAppendCommit(f.ctx, frame, callback))
		require.Equal(t, 1, calls)
		current.NotBefore = nil
		require.NoError(t, f.store.UpdateTask(f.ctx, current))
		frame = snapshot()
		require.NoError(t, f.manager.CancelTask(f.task.ID))
		require.Error(t, f.manager.WithLibraryAppendCommit(f.ctx, frame, callback))
		require.Equal(t, 1, calls)
		require.Error(t, f.manager.WithLibraryAppendCommit(f.ctx, nil, nil))
	})
	t.Run("corrupt_journal_isolated", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		db, err := sql.Open("sqlite", f.database)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO pipeline_library_recoveries(operation_id,kind,season,task_id,state,active,revision,record_json) VALUES('task-999999','task','isolated-season',999999,'waiting',0,1,'{broken')`)
		require.NoError(t, err)
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.True(t, f.memberReady(t))
		var raw string
		require.NoError(t, db.QueryRow(`SELECT record_json FROM pipeline_library_recoveries WHERE operation_id='task-999999'`).Scan(&raw))
		require.Equal(t, "{broken", raw)
		require.NoError(t, db.Close())
	})
}

func TestCompletionAggregateComputeContract(t *testing.T) {
	t.Run("receipt_keeps_aggregate_due", func(t *testing.T) {

		f := newCompletionFixture(t, 1)
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		current, err := f.manager.GetTask(f.task.ID)
		require.NoError(t, err)
		session, err := f.store.RecordingSession(f.ctx, "completion-session")
		require.NoError(t, err)
		local := time.FixedZone("test-UTC8", 8*3600)
		base := time.Now().In(local)
		day := time.Date(base.Year(), base.Month(), base.Day(), 12, 0, 0, 0, local)
		f.cfg.Subtitle.Schedule.Enabled = true
		f.cfg.Subtitle.Schedule.RunAt = "02:00"
		due, err := f.cfg.Subtitle.Schedule.NextRunAfter(day)
		require.NoError(t, err)
		f.manager.SetLibraryRecoveryHooks(pipeline.LibraryRecoveryHooks{Now: func() time.Time { return day }})
		current.Status = pipeline.PipelineStatusFailed
		dueUTC := due.UTC()
		current.NotBefore = &dueUTC
		require.NoError(t, f.store.UpdateTask(f.ctx, current))
		state := pipeline.LibraryAggregateComputeState{SessionID: "completion-session", Sources: session.Sources(), RequestIDs: []string{current.LibraryRecovery.CompletionRefs[0].RequestID}, RequiredAt: day, DueAt: due, Required: true}
		f.editRecord(t, func(r map[string]any) {
			r["aggregate_compute"] = state
			r["next_attempt_at"] = day.Add(-time.Minute)
			r["deadline"] = pipeline.CompletionDeadline{ComputeDue: &dueUTC, AppliedNotBefore: &dueUTC, ProvenanceRevision: 1}
		})
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		after, err := f.manager.GetTask(f.task.ID)
		require.NoError(t, err)
		require.NotNil(t, after.NotBefore)
		require.True(t, after.NotBefore.Equal(due), "已有完整inputs不能清掉仍需normalize的计算窗")

	})
	t.Run("day_wait_night_normalize_and_commit", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.NoError(t, f.store.FinishRecordingProducer(f.ctx, pipeline.RecordingOrigin{SessionID: "completion-session", ProducerID: f.task.RecordInfo.RecordingProducerID}, ""))
		require.NoError(t, f.store.EndRecordingSession(f.ctx, "completion-room", "user_stop"))
		local := time.FixedZone("test-UTC8", 8*3600)
		actual := time.Now().In(local)
		clock := time.Date(actual.Year(), actual.Month(), actual.Day(), 12, 0, 0, 0, local)
		f.manager.SetLibraryRecoveryHooks(pipeline.LibraryRecoveryHooks{Now: func() time.Time { return clock }})
		f.cfg.Subtitle.Schedule.Enabled = true
		f.cfg.Subtitle.Schedule.RunAt = "02:00"
		current, err := f.store.GetTask(f.ctx, f.task.ID)
		require.NoError(t, err)
		require.NoError(t, f.store.ClaimTask(f.ctx, current, clock))
		season := filepath.Join(f.cfg.Subtitle.LibraryRoot, "Host", "Season 01")
		target := filepath.Join(season, "synthetic-normalized.mp4")
		snapshot := func() *pipeline.LibraryAppendCommitRequest {
			task, err := f.store.GetTask(f.ctx, f.task.ID)
			require.NoError(t, err)
			session, err := f.store.RecordingSession(f.ctx, "completion-session")
			require.NoError(t, err)
			return &pipeline.LibraryAppendCommitRequest{ExpectedTask: task, ExpectedMembers: []*pipeline.PipelineTask{task}, ExpectedSession: session, SeasonPath: season, TargetPath: target, OperationID: "synthetic-compute-operation", AssignmentRevision: 1}
		}
		frame := snapshot()
		due, err := f.manager.RequireLibraryAggregateCompute(f.ctx, frame)
		require.NoError(t, err)
		require.Equal(t, 2, due.In(local).Hour())
		require.True(t, due.After(clock))
		allowed, err := f.manager.AllowLibraryAggregateCompute(f.ctx, frame)
		require.NoError(t, err)
		require.False(t, allowed)
		normalizations := 0
		require.ErrorIs(t, f.manager.WithLibraryAppendCommit(f.ctx, frame, func() error { normalizations++; return nil }), subtitle.ErrLibraryComputeRequired)
		require.Zero(t, normalizations)
		clock = clock.Add(time.Hour)
		sameDue, err := f.manager.RequireLibraryAggregateCompute(f.ctx, frame)
		require.NoError(t, err)
		require.True(t, due.Equal(sameDue), "同代不能每天重新推迟")
		// C 的 typed compute_required 必须走计算来源，不落入普通 retry_due。
		executor := pipeline.NewExecutor(nil)
		executor.RegisterStage("aggregate_probe", func(pipeline.StageConfig) (pipeline.Stage, error) { return aggregateProbeStage{}, nil })
		_, err = executor.Execute(&pipeline.PipelineContext{Ctx: f.ctx}, &pipeline.PipelineConfig{Stages: []pipeline.StageConfig{{Name: "aggregate_probe"}}}, nil, nil)
		var deferred *pipeline.DeferredExecution
		require.ErrorAs(t, err, &deferred)
		require.Equal(t, "aggregate_compute_due", deferred.Reason)
		task, err := f.store.GetTask(f.ctx, f.task.ID)
		require.NoError(t, err)
		task.MarkDeferred(due, 0, task.CurrentFiles)
		require.NoError(t, f.store.UpdateTask(f.ctx, task))
		pending, err := f.store.GetTask(f.ctx, f.task.ID)
		require.NoError(t, err)
		require.Error(t, f.store.ClaimTask(f.ctx, pending, clock), "白天原claim入口不能越过due")
		clock = due.Add(time.Minute)
		pending, err = f.store.GetTask(f.ctx, f.task.ID)
		require.NoError(t, err)
		require.NoError(t, f.store.ClaimTask(f.ctx, pending, clock))
		frame = snapshot()
		allowed, err = f.manager.AllowLibraryAggregateCompute(f.ctx, frame)
		require.NoError(t, err)
		require.True(t, allowed, "到原窗口必须能进入normalize分支")
		// 真正用户 hold 仍优先；解除 hold 不会抹掉 Required/ComputeDue。
		task, err = f.store.GetTask(f.ctx, f.task.ID)
		require.NoError(t, err)
		hold := clock.Add(time.Hour).UTC()
		task.NotBefore = &hold
		require.NoError(t, f.store.UpdateTask(f.ctx, task))
		allowed, err = f.manager.AllowLibraryAggregateCompute(f.ctx, snapshot())
		require.NoError(t, err)
		require.False(t, allowed)
		task.NotBefore = nil
		require.NoError(t, f.store.UpdateTask(f.ctx, task))
		frame = snapshot()
		allowed, err = f.manager.AllowLibraryAggregateCompute(f.ctx, frame)
		require.NoError(t, err)
		require.True(t, allowed)
		prepared := filepath.Join(f.root, "prepared-normalized.mp4")
		if allowed {
			normalizations++
			require.NoError(t, os.WriteFile(prepared, []byte("synthetic normalized output"), 0600))
		}
		require.Equal(t, 1, normalizations)
		require.NoError(t, os.MkdirAll(season, 0700))
		require.NoError(t, f.manager.WithLibraryAppendCommit(f.ctx, frame, func() error { return os.Link(prepared, target) }))
		after, err := f.manager.GetTask(f.task.ID)
		require.NoError(t, err)
		require.False(t, after.LibraryRecovery.AggregateCompute.Required)
		require.Nil(t, after.LibraryRecovery.Deadline.ComputeDue)
		_, err = os.Stat(target)
		require.NoError(t, err)
		require.Zero(t, f.posts.Load())
	})
}

type aggregateProbeStage struct{}

func (aggregateProbeStage) Name() string { return "aggregate_probe" }
func (aggregateProbeStage) Execute(*pipeline.PipelineContext, []pipeline.FileInfo) ([]pipeline.FileInfo, error) {
	return nil, pipeline.NewRetryLaterError(subtitle.ErrLibraryComputeRequired, time.Minute)
}

// reviewCountingStage 仅计数公开 stage 边界，实际回执/metadata核对仍委托真实字幕阶段。
type reviewCountingStage struct {
	pipeline.Stage
	reconciler pipeline.CompletionReconciler
	calls      *atomic.Int32
}

func (s reviewCountingStage) ReconcileSavedCompletion(ctx *pipeline.PipelineContext, files []pipeline.FileInfo) (pipeline.CompletionReconcileResult, error) {
	s.calls.Add(1)
	return s.reconciler.ReconcileSavedCompletion(ctx, files)
}

type reviewIndexObserver struct{ calls atomic.Int32 }

func (o *reviewIndexObserver) VerifyPublication(context.Context, pipeline.LibraryIndexExpected) (pipeline.LibraryIndexObservation, error) {
	o.calls.Add(1)
	return pipeline.LibraryIndexObservation{State: "index_unconfirmed", Reason: "synthetic external index pending"}, nil
}

func TestCompletionReviewFixes(t *testing.T) {
	t.Run("ineligible_prefix_does_not_spend_observation_budget", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		db, err := sql.Open("sqlite", f.database)
		require.NoError(t, err)
		defer db.Close()
		insert := func(id int64, state string) {
			r := pipeline.LibraryRecoveryRecord{OperationID: fmt.Sprintf("task-%d", id), Kind: "task", TaskID: id, State: state, Revision: 1, RecoveryClass: "completed_media"}
			data, e := json.Marshal(r)
			require.NoError(t, e)
			_, e = db.Exec(`INSERT INTO pipeline_library_recoveries(operation_id,kind,season,task_id,state,active,revision,record_json) VALUES(?,?,?, ?,?,0,1,?)`, r.OperationID, r.Kind, "", id, state, string(data))
			require.NoError(t, e)
		}
		var runningID, cancelledID int64
		// 这八项都早于有效回执的due，保留原journal而不改变其排序日期。
		for i, status := range []string{"missing", "running", "cancelled", "completed", "offstage", "missing", "cancelled", "completed"} {
			id := int64(1000 + i)
			if status != "missing" {
				task := pipeline.NewPipelineTask(pipeline.RecordInfo{}, f.task.PipelineConfig, f.task.CurrentFiles)
				task.Status = pipeline.PipelineStatus(status)
				if status == "offstage" {
					task.Status = pipeline.PipelineStatusFailed
					task.PipelineConfig = &pipeline.PipelineConfig{Stages: []pipeline.StageConfig{{Name: "custom_command"}}}
				}
				require.NoError(t, f.store.CreateTask(f.ctx, task))
				id = task.ID
				if status == "running" {
					runningID = id
				}
				if status == "cancelled" {
					cancelledID = id
				}
			}
			insert(id, "completion_waiting")
		}
		f.editRecord(t, func(r map[string]any) { r["next_attempt_at"] = time.Now().UTC().Add(-time.Hour) })
		var laterIDs []int64
		for i := 0; i < 8; i++ {
			task := pipeline.NewPipelineTask(pipeline.RecordInfo{}, f.task.PipelineConfig, f.task.CurrentFiles)
			task.Status = pipeline.PipelineStatusFailed
			require.NoError(t, f.store.CreateTask(f.ctx, task))
			insert(task.ID, "completion_waiting")
			laterIDs = append(laterIDs, task.ID)
			_, err = db.Exec(`UPDATE pipeline_library_recoveries SET record_json=json_set(record_json,'$.next_attempt_at',?) WHERE task_id=?`, time.Now().UTC().Add(-30*time.Minute).Format(time.RFC3339Nano), task.ID)
			require.NoError(t, err)
		}
		var calls atomic.Int32
		f.manager.RegisterStage(pipeline.StageNameSubtitleGenerate, func(cfg pipeline.StageConfig) (pipeline.Stage, error) {
			stage, e := stages.NewSubtitleGenerateStage(cfg)
			if e != nil {
				return nil, e
			}
			return reviewCountingStage{Stage: stage, reconciler: stage.(pipeline.CompletionReconciler), calls: &calls}, nil
		})
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.True(t, f.memberReady(t), "八条当前不可观察记录不能遮蔽第九条有效回执")
		require.EqualValues(t, 8, calls.Load(), "一轮最多八次真实stage观察")
		last, err := f.manager.GetTask(laterIDs[7])
		require.NoError(t, err)
		require.Zero(t, last.LibraryRecovery.Observations)
		cancelled, err := f.manager.GetTask(cancelledID)
		require.NoError(t, err)
		require.Equal(t, pipeline.PipelineStatusCancelled, cancelled.Status)
		require.Zero(t, cancelled.LibraryRecovery.Observations)
		running, err := f.manager.GetTask(runningID)
		require.NoError(t, err)
		require.Zero(t, running.LibraryRecovery.Observations)
		running.Status = pipeline.PipelineStatusFailed
		require.NoError(t, f.store.UpdateTask(f.ctx, running))
		before := calls.Load()
		require.NoError(t, f.manager.ReconcileSavedCompletions())
		require.LessOrEqual(t, calls.Load()-before, int32(8))
		running, err = f.manager.GetTask(runningID)
		require.NoError(t, err)
		require.Equal(t, 1, running.LibraryRecovery.Observations, "running后续恢复资格时仍需被观察")
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM pipeline_library_recoveries`).Scan(&count))
		require.Equal(t, 17, count)
		// journal仍可读但任务查询坏了，不能把全局查询错误吞成missing而继续探测。
		_, err = db.Exec(`ALTER TABLE pipeline_tasks RENAME TO temporarily_unavailable_tasks`)
		require.NoError(t, err)
		require.Error(t, f.manager.ReconcileSavedCompletions())
		require.Zero(t, f.posts.Load())
	})
	for _, broken := range []string{"invalid_json", "missing_accepting"} {
		t.Run("control_"+broken+"_keeps_lifecycle", func(t *testing.T) {
			f := newCompletionFixture(t, 1)
			db, err := sql.Open("sqlite", f.database)
			require.NoError(t, err)
			defer db.Close()
			raw := `{"operation_id":"worker-submissions","kind":"control","season":"","revision":1,"state":"configured"}`
			if broken == "invalid_json" {
				raw = `{"broken"`
			}
			_, err = db.Exec(`INSERT INTO pipeline_library_recoveries(operation_id,kind,season,task_id,state,active,revision,record_json) VALUES('worker-submissions','control','',0,'configured',0,1,?)`, raw)
			require.NoError(t, err)
			shortRoot, err := os.MkdirTemp("/tmp", "b58-")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
			appData, err := filepath.EvalSymlinks(shortRoot)
			require.NoError(t, err)
			control, err := servers.StartWorkerSubmissionControl(appData, f.manager)
			require.NoError(t, err)
			defer control.Close(f.ctx)
			require.NoError(t, f.manager.Start(f.ctx), "单条坏control不能阻止正常Manager启动")
			require.Eventually(t, func() bool { return f.memberReady(t) }, time.Second, 10*time.Millisecond)
			status, err := f.manager.WorkerSubmissionsStatus()
			require.NoError(t, err)
			require.Equal(t, false, status["accepting"])
			require.Equal(t, false, status["configured_accepting"])
			require.Equal(t, "worker_submission_control_invalid", status["reason_code"])
			runtimeAccepting, _ := subtitle.WorkerSubmissionsStatus()
			require.False(t, runtimeAccepting)
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(appData, "worker-submissions", "control.sock"))
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			response, err := client.Get("http://local/api/pipeline/worker-submissions")
			require.NoError(t, err)
			var wire map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&wire))
			response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, false, wire["accepting"])
			require.Equal(t, "worker_submission_control_invalid", wire["reason_code"])
			request, err := http.NewRequest(http.MethodPut, "http://local/api/pipeline/worker-submissions", strings.NewReader(`{"accepting":true}`))
			require.NoError(t, err)
			response, err = client.Do(request)
			require.NoError(t, err)
			response.Body.Close()
			require.Equal(t, http.StatusServiceUnavailable, response.StatusCode, "损坏记录不能由普通开gate请求自动修复")
			var after string
			require.NoError(t, db.QueryRow(`SELECT record_json FROM pipeline_library_recoveries WHERE operation_id='worker-submissions'`).Scan(&after))
			require.Equal(t, raw, after)
			runtimeAccepting, _ = subtitle.WorkerSubmissionsStatus()
			require.False(t, runtimeAccepting)
			require.Zero(t, f.posts.Load())
		})
	}
	t.Run("whole_database_failure_still_rejects_start", func(t *testing.T) {
		f := newCompletionFixture(t, 1)
		require.NoError(t, f.store.Close())
		require.Error(t, f.manager.Start(f.ctx))
		accepting, _ := subtitle.WorkerSubmissionsStatus()
		require.False(t, accepting)
		require.Error(t, f.manager.ReconcileSavedCompletions())
		require.Zero(t, f.posts.Load())
	})
	for _, oldState := range []string{"published", "index_unconfirmed", "resumed"} {
		t.Run("completed_media_preserves_"+oldState, func(t *testing.T) {
			if !subtitle.LibraryRecoverySupported() {
				t.Skip("真实索引文件身份边界仅支持Linux；本轮Linux隔离二进制执行此用例")
			}
			f := newCompletionFixture(t, 1)
			require.NoError(t, f.manager.ReconcileSavedCompletions())
			require.NoError(t, f.store.FinishRecordingProducer(f.ctx, pipeline.RecordingOrigin{SessionID: "completion-session", ProducerID: f.task.RecordInfo.RecordingProducerID}, ""))
			require.NoError(t, f.store.EndRecordingSession(f.ctx, "completion-room", "user_stop"))
			session, err := f.store.RecordingSession(f.ctx, "completion-session")
			require.NoError(t, err)
			require.True(t, session.Ready())
			now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
			due := now.Add(3 * time.Hour)
			hold := now.Add(5 * time.Hour)
			f.cfg.Subtitle.Schedule.Enabled = true
			f.cfg.Subtitle.Schedule.RunAt = "02:00"
			task, err := f.store.GetTask(f.ctx, f.task.ID)
			require.NoError(t, err)
			task.Status = pipeline.PipelineStatusFailed
			task.ErrorMessage = "published NFO retry failed"
			task.NotBefore = &hold
			require.NoError(t, f.store.UpdateTask(f.ctx, task))
			state, err := subtitle.CaptureLibraryFileState(f.formal, false)
			require.NoError(t, err)
			protected, err := subtitle.CaptureLibraryFileState(f.source, false)
			require.NoError(t, err)
			f.editRecord(t, func(r map[string]any) {
				delete(r, "completion_refs")
				r["recovery_class"] = "completed_media"
				r["completed_sources"] = session.Sources()
				r["state"] = oldState
				r["next_attempt_at"] = due
				r["before_captured"] = true
				r["expected"] = pipeline.LibraryIndexExpected{OperationID: fmt.Sprintf("task-%d", task.ID), Path: f.formal, MediaRevision: fmt.Sprintf("%d:%d:%d:%d", state.Device, state.Inode, state.Size, state.MtimeNS)}
				r["protected_sources"] = []subtitle.LibraryFileState{protected}
				r["published_missing_sources"] = []string{"synthetic-preserved-proof"}
				r["aggregate_compute"] = pipeline.LibraryAggregateComputeState{SessionID: session.ID, Sources: session.Sources(), Required: true, RequiredAt: now.Add(-time.Hour), DueAt: due}
				r["deadline"] = pipeline.CompletionDeadline{ComputeDue: &due, ExplicitHold: &hold}
			})
			before, err := f.manager.GetTask(task.ID)
			require.NoError(t, err)
			var rounds atomic.Int32
			f.manager.SetLibraryRecoveryHooks(pipeline.LibraryRecoveryHooks{Now: func() time.Time { rounds.Add(1); return now }})
			observer := &reviewIndexObserver{}
			f.manager.SetLibraryIndexObserver(observer)
			require.NoError(t, f.manager.Start(f.ctx))
			require.Eventually(t, func() bool { return rounds.Load() >= 4 }, time.Second, 10*time.Millisecond)
			f.manager.Close(f.ctx)
			// Close关闭store，重开只读查看已耐久的公开任务详情。
			reopened, err := pipeline.NewSQLiteStore(f.database)
			require.NoError(t, err)
			defer reopened.Close()
			view := pipeline.NewManager(f.ctx, reopened, nil, nil)
			after, err := view.GetTask(task.ID)
			require.NoError(t, err)
			require.Equal(t, oldState, after.LibraryRecovery.State, "completed_media不能再被旧NFO改写成waiting")
			require.True(t, after.LibraryRecovery.NextAttempt.Equal(due), "旧NFO不能套入下一夜窗")
			require.Equal(t, before.LibraryRecovery.AggregateCompute, after.LibraryRecovery.AggregateCompute)
			require.Equal(t, before.LibraryRecovery.Deadline, after.LibraryRecovery.Deadline)
			require.Equal(t, before.LibraryRecovery.ProtectedSources, after.LibraryRecovery.ProtectedSources)
			require.Equal(t, before.LibraryRecovery.PublishedMissingSources, after.LibraryRecovery.PublishedMissingSources)
			require.True(t, after.NotBefore.Equal(hold))
			if oldState != "resumed" {
				require.Positive(t, observer.calls.Load(), "独立索引观察不能因退出主动NFO补偿而停掉")
			}
			require.Zero(t, f.posts.Load())
		})
	}
}

func TestDisabledSubtitlesPreserveRecoveryTasks(t *testing.T) {
	for _, mode := range []string{"failed", "queued", "disabled_after_claim"} {
		t.Run(mode, func(t *testing.T) {
			f := newCompletionFixture(t, 1)
			require.NoError(t, f.store.FinishRecordingProducer(f.ctx, pipeline.RecordingOrigin{SessionID: f.task.RecordInfo.LiveSessionID, ProducerID: f.task.RecordInfo.RecordingProducerID}, ""))
			require.NoError(t, f.store.EndRecordingSession(f.ctx, string(f.task.RecordInfo.LiveID), "normal"))
			disabled := *f.cfg
			disabled.Subtitle.Enabled = false
			disable := func() { configs.SetCurrentConfig(&disabled) }
			if mode != "failed" {
				require.NoError(t, f.manager.ReconcileSavedCompletions())
				task, err := f.manager.GetTask(f.task.ID)
				require.NoError(t, err)
				require.Equal(t, pipeline.PipelineStatusPending, task.Status)
			}
			if mode == "disabled_after_claim" {
				f.manager.RegisterStage(pipeline.StageNameSubtitleGenerate, func(cfg pipeline.StageConfig) (pipeline.Stage, error) {
					stage, err := stages.NewSubtitleGenerateStage(cfg)
					return disableOnRecoveryStage{Stage: stage, taskID: f.task.ID, disable: disable}, err
				})
			} else {
				disable()
			}

			normal := pipeline.NewPipelineTask(pipeline.RecordInfo{StartTime: f.task.RecordInfo.StartTime.Add(time.Hour)}, f.task.PipelineConfig, f.task.InitialFiles)
			past := time.Now().UTC().Add(-time.Minute)
			normal.NotBefore = &past
			require.NoError(t, f.store.CreateTask(f.ctx, normal))
			require.NoError(t, f.manager.Start(f.ctx))
			require.Eventually(t, func() bool {
				task, err := f.manager.GetTask(normal.ID)
				return err == nil && task.Status == pipeline.PipelineStatusCompleted
			}, 5*time.Second, 10*time.Millisecond, "普通禁用字幕的任务仍走既有跳过行为")

			if mode == "disabled_after_claim" {
				require.Eventually(t, func() bool {
					task, err := f.manager.GetTask(f.task.ID)
					return err == nil && len(task.StageResults) > 0 && task.Status != pipeline.PipelineStatusRunning
				}, 5*time.Second, 10*time.Millisecond, "已领取的任务须走正常阶段和落库")
			}
			got, err := f.manager.GetTask(f.task.ID)
			require.NoError(t, err)
			if mode == "failed" {
				require.Equal(t, pipeline.PipelineStatusFailed, got.Status)
				require.Equal(t, "registered", got.LibraryRecovery.CompletionRefs[0].State)
			} else {
				require.Equal(t, pipeline.PipelineStatusPending, got.Status)
				require.Nil(t, got.CompletedAt)
			}
			if mode == "disabled_after_claim" {
				require.Contains(t, got.ErrorMessage, "subtitle_recovery_disabled")
			}
			require.Zero(t, f.posts.Load())
		})
	}
}

type disableOnRecoveryStage struct {
	pipeline.Stage
	taskID  int64
	disable func()
}

func (s disableOnRecoveryStage) Execute(ctx *pipeline.PipelineContext, inputs []pipeline.FileInfo) ([]pipeline.FileInfo, error) {
	if ctx.TaskID == s.taskID {
		s.disable()
	}
	return s.Stage.Execute(ctx, inputs)
}
