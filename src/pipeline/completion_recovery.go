package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/subtitle"
)

// CompletionRef 只引用首次提交的原 attempt；观察不创建新 attempt。
type CompletionRef struct {
	InputIndex         int                              `json:"input_index"`
	Source             string                           `json:"source"`
	FormalPath         string                           `json:"formal_path"`
	AttemptPath        string                           `json:"attempt_path"`
	RequestID          string                           `json:"request_id"`
	RequestPath        string                           `json:"request_path"`
	ReceiptPath        string                           `json:"receipt_path"`
	Request            subtitle.WorkerCompletionRequest `json:"request"`
	RequestSnapshot    map[string]any                   `json:"request_snapshot"`
	ExpectedTaskFields *PipelineTask                    `json:"expected_task_fields"`
	State              string                           `json:"state"`
	Attempts           int                              `json:"attempts,omitempty"`
	Reason             string                           `json:"reason,omitempty"`
}
type CompletionDeadline struct {
	ComputeDue         *time.Time `json:"compute_due,omitempty"`
	RetryDue           *time.Time `json:"retry_due,omitempty"`
	ExplicitHold       *time.Time `json:"explicit_hold,omitempty"`
	AppliedNotBefore   *time.Time `json:"applied_not_before,omitempty"`
	ProvenanceRevision int64      `json:"provenance_revision"`
}
type CompletionReconcileResult struct {
	AllInputsComplete bool
	BlockingInputs    []int
	Sources           []SessionMediaSource
}
type CompletionReconciler interface {
	ReconcileSavedCompletion(*PipelineContext, []FileInfo) (CompletionReconcileResult, error)
}

const taskCompletionSelect = `SELECT id,status,record_info_json,pipeline_config_json,initial_files_json,current_files_json,current_stage,total_stages,stage_results_json,progress,created_at,started_at,completed_at,not_before,error_message,can_retry FROM pipeline_tasks WHERE id=?`

func cloneCompletionTask(t *PipelineTask) *PipelineTask {
	b, _ := json.Marshal(t)
	var copy PipelineTask
	_ = json.Unmarshal(b, &copy)
	copy.LibraryRecovery = nil
	return &copy
}
func sameCompletionTask(a, b *PipelineTask) bool {
	if a == nil || b == nil {
		return false
	}
	ac, bc := *a, *b
	ac.LibraryRecovery = nil
	bc.LibraryRecovery = nil
	return equalJSON(ac, bc)
}
func (m *Manager) lockCompletionTask(id int64) func() {
	v, _ := m.taskLocks.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
func completionTaskOwned(task *PipelineTask, ref CompletionRef) bool {
	expected := ref.ExpectedTaskFields
	if expected == nil || task.ID != expected.ID || task.RecordInfo.LiveSessionID == "" || task.Status == PipelineStatusCancelled || task.Status == PipelineStatusCompleted || !task.CanRetry {
		return false
	}
	if !equalJSON(task.RecordInfo, expected.RecordInfo) || !equalJSON(task.PipelineConfig, expected.PipelineConfig) || !equalJSON(task.InitialFiles, expected.InitialFiles) || !equalJSON(task.CurrentFiles, expected.CurrentFiles) || task.CurrentStage != expected.CurrentStage || len(task.StageResults) < len(expected.StageResults) {
		return false
	}
	if len(expected.StageResults) > 0 && !equalJSON(task.StageResults[:len(expected.StageResults)], expected.StageResults) {
		return false
	}
	if ref.State != "accepted" && task.StartedAt != nil && (expected.StartedAt == nil || !task.StartedAt.Equal(*expected.StartedAt)) {
		return false
	}
	if task.Status == PipelineStatusRunning && task.StartedAt == nil {
		return false
	}
	r := ref.Request
	return ref.State != "superseded" && ref.InputIndex >= 0 && ref.InputIndex < len(task.CurrentFiles) && task.CurrentFiles[ref.InputIndex].Type == FileTypeVideo && task.CurrentFiles[ref.InputIndex].Path == ref.Source && r.Owner.TaskID == task.ID && r.Owner.InputIndex == ref.InputIndex && r.Owner.SessionID == task.RecordInfo.LiveSessionID && r.Owner.ProducerID == task.RecordInfo.RecordingProducerID && r.SourcePath == ref.Source && r.FormalOutputPath == ref.FormalPath && r.AttemptDirectory == ref.AttemptPath && r.RequestID == ref.RequestID && ref.RequestPath == filepath.Join(ref.AttemptPath, "worker-request.json") && ref.ReceiptPath == filepath.Join(ref.AttemptPath, "worker-result.json") && equalJSON(r.RequestSnapshot, ref.RequestSnapshot)
}
func recoveryTx(ctx context.Context, tx *sql.Tx, id int64) (*LibraryRecoveryRecord, error) {
	var raw string
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT record_json,revision FROM pipeline_library_recoveries WHERE operation_id=?`, fmt.Sprintf("task-%d", id)).Scan(&raw, &revision)
	if err == sql.ErrNoRows {
		return &LibraryRecoveryRecord{OperationID: fmt.Sprintf("task-%d", id), Kind: "task", TaskID: id}, nil
	}
	if err != nil {
		return nil, err
	}
	var r LibraryRecoveryRecord
	if json.Unmarshal([]byte(raw), &r) != nil || r.Revision != revision || r.TaskID != id || r.Kind != "task" || r.OperationID != fmt.Sprintf("task-%d", id) || (r.CompletionSchema != 0 && r.CompletionSchema != 1) {
		return nil, errors.New("journal_invalid")
	}
	if r.Plan != nil && r.Plan.Hash() != r.PlanHash {
		return nil, errors.New("journal_invalid")
	}
	return &r, nil
}
func saveRecoveryTx(ctx context.Context, tx *sql.Tx, r *LibraryRecoveryRecord) error {
	next := *r
	next.Revision++
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if r.Revision == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO pipeline_library_recoveries(operation_id,kind,season,task_id,state,active,revision,record_json) VALUES(?,?,?,?,?,?,?,?)`, r.OperationID, r.Kind, r.Season, r.TaskID, r.State, r.Active, next.Revision, string(data))
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE pipeline_library_recoveries SET state=?,active=?,revision=?,record_json=? WHERE operation_id=? AND revision=?`, r.State, r.Active, next.Revision, string(data), r.OperationID, r.Revision)
		if err == nil {
			n, _ := result.RowsAffected()
			if n != 1 {
				err = errors.New("completion_journal_changed")
			}
		}
	}
	if err == nil {
		*r = next
	}
	return err
}

// completionCommit 在短事务重新比较完整任务前像；callback 仅写小文件/无覆盖链接，不得调用 store。
func (s *SQLiteStore) completionCommit(ctx context.Context, expected *PipelineTask, ref *CompletionRef, revision int64, files func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE pipeline_tasks SET id=id WHERE id=?`, expected.ID); err != nil {
		return err
	}
	current, err := scanTask(tx.QueryRowContext(ctx, taskCompletionSelect, expected.ID))
	if err != nil {
		return err
	}
	if !sameCompletionTask(current, expected) || current.Status == PipelineStatusCancelled {
		return errors.New("completion_task_changed")
	}
	r, err := recoveryTx(ctx, tx, expected.ID)
	if err != nil {
		return err
	}
	index := -1
	if ref != nil {
		if r.Revision != revision {
			return errors.New("completion_journal_changed")
		}
		for i, candidate := range r.CompletionRefs {
			if candidate.RequestID == ref.RequestID && equalJSON(candidate, *ref) {
				index = i
				break
			}
		}
		if index < 0 || !completionTaskOwned(current, *ref) {
			return errors.New("completion_owner_changed")
		}
	}
	if err = files(); err != nil {
		return err
	}
	if ref != nil {
		r.CompletionRefs[index].State = "accepted"
		r.CompletionRefs[index].Reason = ""
		r.LastObservedAt = time.Now().UTC()
		if err = saveRecoveryTx(ctx, tx, r); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (m *Manager) completionRecord(id int64) (*LibraryRecoveryRecord, error) {
	s, ok := m.store.(*SQLiteStore)
	if !ok {
		return nil, errors.New("completion_requires_sqlite")
	}
	records, err := s.libraryRecoveries(m.ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if r.TaskID == id && r.Kind == "task" {
			if r.InvalidJournal {
				return nil, errors.New("journal_invalid")
			}
			return r, nil
		}
	}
	return &LibraryRecoveryRecord{OperationID: fmt.Sprintf("task-%d", id), Kind: "task", TaskID: id}, nil
}
func (m *Manager) bindCompletionContext(pc *PipelineContext, task *PipelineTask) {
	pc.SavedCompletions = func() ([]CompletionRef, error) {
		r, err := m.completionRecord(task.ID)
		if err != nil {
			return nil, err
		}
		return r.CompletionRefs, nil
	}
	pc.RegisterCompletion = func(ref CompletionRef) error {
		unlock := m.lockCompletionTask(task.ID)
		defer unlock()
		s, ok := m.store.(*SQLiteStore)
		if !ok {
			return errors.New("completion_requires_sqlite")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		tx, err := s.db.BeginTx(m.ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(m.ctx, `UPDATE pipeline_tasks SET id=id WHERE id=?`, task.ID); err != nil {
			return err
		}
		current, err := scanTask(tx.QueryRowContext(m.ctx, taskCompletionSelect, task.ID))
		if err != nil {
			return err
		}
		if !sameCompletionTask(current, task) || task.Status != PipelineStatusRunning {
			return errors.New("completion_task_changed")
		}
		r, err := recoveryTx(m.ctx, tx, task.ID)
		if err != nil {
			return err
		}
		attempts := 1
		replace := -1
		for i, old := range r.CompletionRefs {
			if old.InputIndex == ref.InputIndex && old.State != "superseded" {
				if old.State != "queued_unsent" || old.RequestID != ref.RequestID || !equalJSON(old.Request, ref.Request) {
					return errors.New("completion_original_attempt_exists")
				}
				if old.Attempts >= 8 {
					return errors.New("retry_budget_exhausted")
				}
				attempts = old.Attempts + 1
				replace = i
			}
		}

		ref.ExpectedTaskFields = cloneCompletionTask(current)
		ref.State = "registered"
		ref.Attempts = attempts
		if !completionTaskOwned(current, ref) {
			return errors.New("completion_registration_owner_mismatch")
		}
		hasLiveRef := false
		for _, old := range r.CompletionRefs {
			if old.State != "superseded" {
				hasLiveRef = true
				break
			}
		}
		if !hasLiveRef {
			r.CompletionWorkAttempts = 0
		}
		r.CompletionSchema = 1
		r.RecoveryClass = "worker_completion"
		if replace >= 0 {
			r.CompletionRefs[replace] = ref
		} else {
			r.CompletionRefs = append(r.CompletionRefs, ref)
		}
		r.CompletionState = "waiting"
		r.ReasonCode = "completion_unknown"
		if r.State == "" {
			r.State = "completion_waiting"
		}
		r.NextAttempt = time.Now().UTC()
		if err = saveRecoveryTx(m.ctx, tx, r); err != nil {
			return err
		}
		return tx.Commit()
	}
	pc.CompletionNotSent = func(requestID string) error {
		unlock := m.lockCompletionTask(task.ID)
		defer unlock()
		r, err := m.completionRecord(task.ID)
		if err != nil {
			return err
		}
		current, err := m.store.GetTask(m.ctx, task.ID)
		if err != nil || !sameCompletionTask(current, task) {
			return errors.New("completion_task_changed")
		}
		for i := range r.CompletionRefs {
			if r.CompletionRefs[i].RequestID == requestID {
				r.CompletionRefs[i].State = "queued_unsent"
				r.CompletionRefs[i].Reason = "strict_local_not_sent"
				return m.store.(*SQLiteStore).saveLibraryRecovery(m.ctx, r)
			}
		}
		return errors.New("completion_ref_missing")
	}
	pc.WithCompletionCommit = func(ref *CompletionRef, attempt string, files func() error) error {
		if ref != nil {
			if err := m.recoveryBoundary("completion_before_commit"); err != nil {
				return err
			}
		}
		if pc.Ctx != nil && pc.Ctx.Err() != nil {
			return pc.Ctx.Err()
		}
		unlock := m.lockCompletionTask(task.ID)
		defer unlock()
		var revision int64
		if ref != nil {
			r, err := m.completionRecord(task.ID)
			if err != nil {
				return err
			}
			revision = r.Revision
			found := false
			for _, current := range r.CompletionRefs {
				if current.RequestID == ref.RequestID {
					*ref = current
					found = true
					break
				}
			}
			if !found {
				return errors.New("completion_ref_missing")
			}
			attempt = ref.AttemptPath
		}
		if attempt != "" && (ref != nil || subtitle.WorkerCompletionSupported()) {
			release, err := subtitle.LockWorkerAttempt(attempt)
			if err != nil {
				return err
			}
			defer release()
		}
		s, ok := m.store.(*SQLiteStore)
		if !ok {
			return errors.New("completion_requires_sqlite")
		}
		return s.completionCommit(m.ctx, cloneCompletionTask(task), ref, revision, func() error {
			if err := files(); err != nil {
				return err
			}
			if ref != nil {
				return m.recoveryBoundary("completion_files_committed")
			}
			return nil
		})
	}
}

// WorkerSubmissionsStatus 不把本地计数零当远端 drain 证明。
func (m *Manager) WorkerSubmissionsStatus() (map[string]any, error) {
	s, ok := m.store.(*SQLiteStore)
	if !ok {
		return nil, errors.New("gate_requires_sqlite")
	}
	records, err := s.libraryRecoveries(m.ctx)
	if err != nil {
		return nil, err
	}
	accepting := true
	controlInvalid := false
	for _, r := range records {
		if r.OperationID == "worker-submissions" {
			if r.InvalidJournal || r.WorkerSubmissionsAccepting == nil {
				// 单条损坏仅关闭新提交；保留坏JSON，录制与结果领取仍可启动。
				accepting = false
				controlInvalid = true
				break
			}
			accepting = *r.WorkerSubmissionsAccepting
		}
	}
	runtimeAccepting, inflight := subtitle.WorkerSubmissionsStatus()
	status := map[string]any{"accepting": accepting && runtimeAccepting, "configured_accepting": accepting, "inflight": inflight, "remote_drained": false, "completion_v1_supported": subtitle.WorkerCompletionSupported()}
	if controlInvalid {
		status["reason_code"] = "worker_submission_control_invalid"
	}
	return status, nil
}
func (m *Manager) SetWorkerSubmissions(accepting bool) error {
	m.gateMu.Lock()
	defer m.gateMu.Unlock()
	if !accepting {
		subtitle.ConfigureWorkerSubmissions(false)
	}
	s, ok := m.store.(*SQLiteStore)
	if !ok {
		return errors.New("gate_requires_sqlite")
	}
	records, err := s.libraryRecoveries(m.ctx)
	if err != nil {
		return err
	}
	r := &LibraryRecoveryRecord{OperationID: "worker-submissions", Kind: "control", RecoveryClass: "control", State: "configured"}
	for _, old := range records {
		if old.OperationID == r.OperationID {
			if old.InvalidJournal || old.WorkerSubmissionsAccepting == nil {
				subtitle.ConfigureWorkerSubmissions(false)
				return errors.New("worker_submission_control_invalid")
			}
			r = old
		}
	}
	// 关闭先挡新 entry，持久失败仍 fail-closed；打开必须先耐久。

	r.WorkerSubmissionsAccepting = &accepting
	if err = s.saveLibraryRecovery(m.ctx, r); err != nil {
		return err
	}
	subtitle.ConfigureWorkerSubmissions(accepting)
	return nil
}
func (m *Manager) restoreWorkerSubmissions() error {
	status, err := m.WorkerSubmissionsStatus()
	if err != nil {
		subtitle.ConfigureWorkerSubmissions(false)
		return err
	}
	subtitle.ConfigureWorkerSubmissions(status["configured_accepting"].(bool))
	return nil
}

func allCompletionRefsAccepted(task *PipelineTask, r *LibraryRecoveryRecord) bool {
	if r == nil || r.InvalidJournal {
		return false
	}
	for i, file := range task.CurrentFiles {
		if file.Type != FileTypeVideo {
			continue
		}
		found := false
		for _, ref := range r.CompletionRefs {
			if ref.InputIndex == i && ref.State == "accepted" && completionTaskOwned(task, ref) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(r.CompletionRefs) > 0
}
func (m *Manager) completionResultOnly(task *PipelineTask) bool {
	r, err := m.completionRecord(task.ID)
	if err != nil {
		return false
	}
	if allCompletionRefsAccepted(task, r) {
		return true
	}
	if task.PipelineConfig == nil || task.CurrentStage < 0 || task.CurrentStage >= len(task.PipelineConfig.Stages) {
		return false
	}
	cfg := configs.GetCurrentConfig()
	if cfg == nil || task.RecordInfo.LiveSessionID == "" {
		return false
	}
	// 新任务通常连首个工作检查点都没有，无需先构造字幕阶段。
	for i, file := range task.CurrentFiles {
		if file.Type != FileTypeVideo {
			continue
		}
		path, err := subtitle.ExistingRecordingWorkPath(cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath),
			task.RecordInfo.LiveSessionID, task.ID, i, file.Path, task.RecordInfo.HostName, task.RecordInfo.StartTime)
		if err != nil {
			return false
		}
		if _, err := os.Lstat(strings.TrimSuffix(path, ".mp4") + ".subtitle.json"); err != nil {
			return false
		}
		break
	}
	factory, ok := m.executor.getFactory(StageNameSubtitleGenerate)
	if !ok {
		return false
	}
	stage, e := factory(task.PipelineConfig.Stages[task.CurrentStage])
	if e != nil {
		return false
	}
	planner, ok := stage.(interface {
		HasCompletedInputs(*PipelineContext, []FileInfo) bool
	})
	return ok && planner.HasCompletedInputs(&PipelineContext{TaskID: task.ID, RecordInfo: task.RecordInfo, Ctx: m.ctx}, task.CurrentFiles)
}

// ReconcileSavedCompletions 仅一次有界观察，复用 Manager ticker，不调用 Execute 或未知 POST。
func (m *Manager) ReconcileSavedCompletions() error {
	if cfg := configs.GetCurrentConfig(); cfg == nil || !cfg.Subtitle.Enabled {
		return nil
	}
	s, ok := m.store.(*SQLiteStore)
	if !ok {
		return nil
	}
	records, err := s.libraryRecoveries(m.ctx)
	if err != nil {
		return err
	}
	now := m.recoveryNow()
	type candidate struct {
		record     *LibraryRecoveryRecord
		task       *PipelineTask
		reconciler CompletionReconciler
	}
	var due []candidate
	for _, r := range records {
		computeDeadline := r.RecoveryClass == "deadline" && r.Deadline != nil && r.Deadline.ComputeDue != nil
		if r.InvalidJournal || r.Kind != "task" || (len(r.CompletionRefs) == 0 && !computeDeadline && r.RecoveryClass != "completed_media") || r.NextAttempt.After(now) {
			continue
		}
		// 先作纯DB/阶段资格检查，再分配最多八项文件观察名额。
		// 不修改不可观察项的journal；running今后离开运行态仍能重新入选。
		task, err := m.store.GetTask(m.ctx, r.TaskID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("completion task lookup: %w", err)
		}
		if task.Status == PipelineStatusRunning || task.Status == PipelineStatusCancelled || task.Status == PipelineStatusCompleted {
			continue
		}
		if task.PipelineConfig == nil || task.CurrentStage < 0 || task.CurrentStage >= len(task.PipelineConfig.Stages) || task.PipelineConfig.Stages[task.CurrentStage].Name != StageNameSubtitleGenerate {
			continue
		}
		factory, ok := m.executor.getFactory(StageNameSubtitleGenerate)
		if !ok {
			continue
		}
		stage, err := factory(task.PipelineConfig.Stages[task.CurrentStage])
		if err != nil {
			continue
		}
		reconciler, ok := stage.(CompletionReconciler)
		if !ok {
			continue
		}
		due = append(due, candidate{r, task, reconciler})
	}
	sort.SliceStable(due, func(i, j int) bool {
		a, b := due[i].record, due[j].record
		if !a.NextAttempt.Equal(b.NextAttempt) {
			return a.NextAttempt.Before(b.NextAttempt)
		}
		if !a.LastObservedAt.Equal(b.LastObservedAt) {
			return a.LastObservedAt.Before(b.LastObservedAt)
		}
		return a.TaskID < b.TaskID
	})
	if len(due) > 8 {
		due = due[:8]
	}
	for _, c := range due {
		m.reconcileCompletionRecord(c.task, c.reconciler)
	}
	return nil
}
func (m *Manager) reconcileCompletionRecord(task *PipelineTask, reconciler CompletionReconciler) {
	pc := &PipelineContext{TaskID: task.ID, Ctx: m.ctx, RecordInfo: task.RecordInfo}
	m.bindCompletionContext(pc, task)
	result, workErr := reconciler.ReconcileSavedCompletion(pc, task.CurrentFiles)
	unlock := m.lockCompletionTask(task.ID)
	defer unlock()
	current, err := m.store.GetTask(m.ctx, task.ID)
	if err != nil || !sameCompletionTask(current, task) {
		return
	}
	latest, err := m.completionRecord(task.ID)
	if err != nil {
		return
	}
	latest.LastObservedAt = m.recoveryNow()
	latest.BlockingInputs = result.BlockingInputs
	latest.BlockingMembers = nil
	if len(result.BlockingInputs) > 0 {
		latest.BlockingMembers = []int64{task.ID}
	}
	if workErr != nil {
		latest.ReasonCode = "completion_blocked"
		latest.Reason = workErr.Error()
	} else {
		latest.ReasonCode = "completion_unknown"
		latest.Reason = ""
	}
	latest.CompletionState = "waiting"
	latest.Observations++
	delays := []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour}
	index := latest.Observations - 1
	if index >= len(delays) {
		index = len(delays) - 1
	}
	latest.NextAttempt = latest.LastObservedAt.Add(delays[index])
	if result.AllInputsComplete && workErr == nil {
		if err = m.completeCompletionMedia(task, result.Sources); err != nil {
			latest.ReasonCode = "completion_registry_blocked"
			latest.Reason = err.Error()
		} else {
			if len(latest.CompletionRefs) == 0 {
				latest.RecoveryClass = "completed_media"
				latest.CompletedSources = result.Sources
			}
			if task.Status == PipelineStatusFailed {
				if validationErr := m.validateTaskResume(task); validationErr != nil {
					latest.CompletionState = "blocked"
					latest.ReasonCode = "completion_resume_blocked"
					latest.Reason = validationErr.Error()
					_ = m.store.(*SQLiteStore).saveLibraryRecovery(m.ctx, latest)
					return
				}
				if latest.CompletionWorkAttempts >= 8 {
					latest.CompletionState = "blocked"
					latest.ReasonCode = "retry_budget_exhausted"
					latest.Reason = "completion resume work budget exhausted"
					_ = m.store.(*SQLiteStore).saveLibraryRecovery(m.ctx, latest)
					return
				}
				latest.CompletionWorkAttempts++
			}
			latest.CompletionState = "all_inputs_complete"
			latest.ReasonCode = "completion_accepted"
			if session, e := m.store.(*SQLiteStore).RecordingSession(m.ctx, task.RecordInfo.LiveSessionID); e == nil && !session.Ready() {
				latest.ReasonCode = "session_incomplete"
				latest.Reason = session.Blocked
				if latest.Reason == "" {
					latest.Reason = "waiting for registered session closure or members"
				}
				for key, member := range session.Tasks {
					if !member.Ready {
						if id, parseErr := strconv.ParseInt(key, 10, 64); parseErr == nil {
							latest.BlockingMembers = append(latest.BlockingMembers, id)
						}
					}
				}
				sort.Slice(latest.BlockingMembers, func(i, j int) bool { return latest.BlockingMembers[i] < latest.BlockingMembers[j] })
			}
			latest.NextAttempt = latest.LastObservedAt.Add(recoveryBackoff(latest.CompletionWorkAttempts))
			if task.Status == PipelineStatusPending {
				latest.NextAttempt = latest.LastObservedAt.Add(6 * time.Hour)
			}
			if err = m.applyCompletionDeadline(task, latest); err != nil {
				return
			}
			// Deadline 与 journal 已在同一 CAS 事务保存，随后沿原 Resume CAS，崩溃可重入。
			task, err = m.store.GetTask(m.ctx, task.ID)
			if err != nil {
				return
			}
			if task.Status == PipelineStatusFailed && task.CanRetry {
				if err = m.store.(*SQLiteStore).ResumeTask(m.ctx, task); err != nil {
					return
				}
			}
			m.wakeScheduler()
			return
		}
	}
	_ = m.store.(*SQLiteStore).saveLibraryRecovery(m.ctx, latest)
}
func (m *Manager) completeCompletionMedia(task *PipelineTask, sources []SessionMediaSource) error {
	if len(sources) == 0 {
		return errors.New("completion_no_inputs")
	}
	return m.store.(*SQLiteStore).CompleteRecordingTaskMedia(m.ctx, task.RecordInfo.LiveSessionID, task.ID, sources)
}
func (m *Manager) applyCompletionDeadline(task *PipelineTask, r *LibraryRecoveryRecord) error {
	s := m.store.(*SQLiteStore)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(m.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(m.ctx, `UPDATE pipeline_tasks SET id=id WHERE id=?`, task.ID); err != nil {
		return err
	}
	current, err := scanTask(tx.QueryRowContext(m.ctx, taskCompletionSelect, task.ID))
	if err != nil || !sameCompletionTask(current, task) {
		return errors.New("completion_task_changed")
	}
	actual, err := recoveryTx(m.ctx, tx, task.ID)
	if err != nil || actual.Revision != r.Revision {
		return errors.New("completion_journal_changed")
	}
	deadline := task.NotBefore
	if d := r.Deadline; d != nil && equalJSON(d.AppliedNotBefore, task.NotBefore) && d.ComputeDue != nil && (r.AggregateCompute == nil || !r.AggregateCompute.Required) {
		value := time.Now().UTC()
		for _, hold := range []*time.Time{d.RetryDue, d.ExplicitHold} {
			if hold != nil && hold.After(value) {
				value = *hold
			}
		}
		deadline = &value
		d.ComputeDue = nil
		d.AppliedNotBefore = deadline
		d.ProvenanceRevision++
	}
	if _, err = tx.ExecContext(m.ctx, `UPDATE pipeline_tasks SET not_before=? WHERE id=?`, deadline, task.ID); err != nil {
		return err
	}
	if err = saveRecoveryTx(m.ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit()
}

// saveCompletionDeadlineTx 与 task.not_before 同事务合并来源，保留原恢复证明。
func saveCompletionDeadlineTx(ctx context.Context, tx *sql.Tx, id int64, before, after *time.Time, reason string, due time.Time) error {
	if equalJSON(before, after) && reason == "" {
		return nil
	}
	r, err := recoveryTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if r.RecoveryClass == "" {
		r.RecoveryClass = "deadline"
	}
	if r.State == "" {
		r.State = "deadline"
	}
	d := r.Deadline
	if d == nil {
		d = &CompletionDeadline{ExplicitHold: before}
		r.Deadline = d
	} else if !equalJSON(d.AppliedNotBefore, before) {
		d.ExplicitHold = before
		d.ComputeDue = nil
		d.RetryDue = nil
	}
	switch reason {
	case "compute_due":
		d.ComputeDue = &due
	case "aggregate_compute_due":
		if r.AggregateCompute == nil || !r.AggregateCompute.Required {
			return errors.New("aggregate_compute_state_missing")
		}
		d.ComputeDue = &r.AggregateCompute.DueAt
	case "retry_due":
		d.RetryDue = &due
	default:
		d.ExplicitHold = after
		d.ComputeDue = nil
		if r.AggregateCompute != nil && r.AggregateCompute.Required {
			d.ComputeDue = &r.AggregateCompute.DueAt
		}
		d.RetryDue = nil
	}
	if r.AggregateCompute != nil && r.AggregateCompute.Required {
		d.ComputeDue = &r.AggregateCompute.DueAt
	}
	d.AppliedNotBefore = after
	d.ProvenanceRevision++
	return saveRecoveryTx(ctx, tx, r)
}

// validateAppendFrame 不读取 ledger；ledger operation/revision 的重核由已持 season 锁的 C callback 完成。
func validateAppendFrame(r *LibraryAppendCommitRequest) ([]int64, error) {
	if r == nil || r.ExpectedTask == nil || len(r.ExpectedMembers) == 0 || r.ExpectedSession.ID == "" || !r.ExpectedSession.Ready() || r.OperationID == "" || r.AssignmentRevision <= 0 || !filepath.IsAbs(r.SeasonPath) || filepath.Clean(r.SeasonPath) != r.SeasonPath || filepath.Dir(r.TargetPath) != r.SeasonPath || filepath.Clean(r.TargetPath) != r.TargetPath {
		return nil, errors.New("append_commit_frame_incomplete")
	}
	ids := make([]int64, 0, len(r.ExpectedMembers))
	seen := map[int64]bool{}
	primary := false
	if len(r.ExpectedMembers) != len(r.ExpectedSession.Tasks) {
		return nil, errors.New("append_member_set_changed")
	}
	for _, task := range r.ExpectedMembers {
		if task == nil || task.ID <= 0 || seen[task.ID] || task.PipelineConfig == nil || len(task.CurrentFiles) == 0 || task.CurrentStage < 0 || task.CurrentStage > len(task.PipelineConfig.Stages) || task.RecordInfo.LiveSessionID != r.ExpectedSession.ID || task.RecordInfo.RecordingProducerID == "" {
			return nil, errors.New("append_member_frame_invalid")
		}
		media, exists := r.ExpectedSession.Tasks[fmt.Sprint(task.ID)]
		if !exists || !media.Ready || len(media.Sources) == 0 || !r.ExpectedSession.Producers[task.RecordInfo.RecordingProducerID] {
			return nil, errors.New("append_member_owner_invalid")
		}
		inputs := map[string]bool{}
		for _, source := range media.Sources {
			if source.TaskID != task.ID || source.ProducerID != task.RecordInfo.RecordingProducerID || source.InputPath == "" || inputs[source.InputPath] {
				return nil, errors.New("append_member_owner_invalid")
			}
			inputs[source.InputPath] = true
		}
		if task.ID == r.ExpectedTask.ID {
			primary = sameCompletionTask(task, r.ExpectedTask)
		}
		seen[task.ID] = true
		ids = append(ids, task.ID)
	}
	if !primary {
		return nil, errors.New("append_primary_task_mismatch")
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// WithLibraryAppendCommit 的 caller 已持 work→season；这里只取按 ID 排序的共同 task 短锁→DB。
// callback 只允许短文件/ledger 提交，不得取 work/season/store 锁或执行 RPC/模型/聚合。
func (m *Manager) WithLibraryAppendCommit(ctx context.Context, r *LibraryAppendCommitRequest, files func() error) error {
	if files == nil {
		return errors.New("append_commit_callback_required")
	}
	return m.withLibraryTaskFront(ctx, r, func(tx *sql.Tx, records map[int64]*LibraryRecoveryRecord) error {
		now := m.recoveryNow()
		if aggregateHold(r, records, now) {
			return errors.New("append_task_hold_active")
		}
		sources, ids, err := aggregateGeneration(r.ExpectedSession, records)
		if err != nil {
			return err
		}
		state, err := requiredAggregateState(r.ExpectedSession.ID, sources, ids, records)
		if err != nil {
			return err
		}
		if state != nil && now.Before(state.DueAt) {
			return subtitle.ErrLibraryComputeRequired
		}
		if err = files(); err != nil {
			return err
		}
		// 真正公开 callback 已成功，才在同事务清本代聚合需要；不清用户 hold/真实 retry。
		if state != nil {
			for _, record := range records {
				if record.AggregateCompute == nil || !record.AggregateCompute.Required {
					continue
				}
				record.AggregateCompute.Required = false
				if d := record.Deadline; d != nil && d.ComputeDue != nil && d.ComputeDue.Equal(state.DueAt) {
					d.ComputeDue = nil
					d.ProvenanceRevision++
				}
				if err = saveRecoveryTx(ctx, tx, record); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// withLibraryTaskFront 统一三个短提交入口；任何 callback 都不得再取 store/work/season 锁。
func (m *Manager) withLibraryTaskFront(ctx context.Context, r *LibraryAppendCommitRequest, operation func(*sql.Tx, map[int64]*LibraryRecoveryRecord) error) error {
	ids, err := validateAppendFrame(r)
	if err != nil {
		return err
	}
	releases := make([]func(), 0, len(ids))
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	for _, id := range ids {
		releases = append(releases, m.lockCompletionTask(id))
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	s, ok := m.store.(*SQLiteStore)
	if !ok {
		return errors.New("append_commit_requires_sqlite")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE pipeline_tasks SET id=id WHERE id=?`, r.ExpectedTask.ID); err != nil {
		return err
	}
	records := map[int64]*LibraryRecoveryRecord{}
	for _, expected := range r.ExpectedMembers {
		current, e := scanTask(tx.QueryRowContext(ctx, taskCompletionSelect, expected.ID))
		if e != nil {
			return e
		}
		if !sameCompletionTask(current, expected) || current.Status == PipelineStatusCancelled || current.Status == PipelineStatusRunning && (current.StartedAt == nil || current.StartedAt.IsZero()) {
			return errors.New("append_task_claim_or_cancel_changed")
		}
		record, e := recoveryTx(ctx, tx, current.ID)
		if e != nil {
			return e
		}
		records[current.ID] = record
	}
	session, err := readRecordingSession(ctx, tx, r.ExpectedSession.ID)
	if err != nil {
		return err
	}
	if !session.Ready() || !equalJSON(session, r.ExpectedSession) {
		return errors.New("append_session_changed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = operation(tx, records); err != nil {
		return err
	}
	return tx.Commit()
}

func aggregateGeneration(session RecordingSession, records map[int64]*LibraryRecoveryRecord) ([]SessionMediaSource, []string, error) {
	sources := session.Sources()
	lookup := map[int64]map[string]SessionMediaSource{}
	for _, source := range sources {
		if lookup[source.TaskID] == nil {
			lookup[source.TaskID] = map[string]SessionMediaSource{}
		}
		lookup[source.TaskID][source.InputPath] = source
	}
	var ids []string
	seen := map[string]bool{}
	for taskID, record := range records {
		for _, ref := range record.CompletionRefs {
			if ref.State == "superseded" {
				continue
			}
			source, exists := lookup[taskID][ref.Source]
			request := ref.Request
			if !exists || ref.State != "accepted" || ref.FormalPath != source.LibraryPath || ref.RequestID == "" || request.RequestID != ref.RequestID || request.Owner.TaskID != taskID || request.Owner.SessionID != session.ID || request.Owner.ProducerID != source.ProducerID || seen[ref.RequestID] {
				return nil, nil, errors.New("aggregate_compute_source_generation_changed")
			}
			seen[ref.RequestID] = true
			ids = append(ids, ref.RequestID)
		}
	}
	sort.Strings(ids)
	return sources, ids, nil
}
func sameAggregateGeneration(state *LibraryAggregateComputeState, session string, sources []SessionMediaSource, ids []string) bool {
	if state == nil || state.SessionID != session || !equalJSON(state.Sources, sources) || len(state.RequestIDs) != len(ids) {
		return false
	}
	stored := append([]string(nil), state.RequestIDs...)
	sort.Strings(stored)
	for i, id := range ids {
		if id != stored[i] || i > 0 && stored[i] == stored[i-1] {
			return false
		}
	}
	return true
}
func requiredAggregateState(session string, sources []SessionMediaSource, ids []string, records map[int64]*LibraryRecoveryRecord) (*LibraryAggregateComputeState, error) {
	var state *LibraryAggregateComputeState
	for _, record := range records {
		current := record.AggregateCompute
		if current == nil || !current.Required {
			continue
		}
		if !sameAggregateGeneration(current, session, sources, ids) || current.DueAt.IsZero() || current.RequiredAt.IsZero() {
			return nil, errors.New("aggregate_compute_source_generation_changed")
		}
		if state != nil && (!state.DueAt.Equal(current.DueAt) || !state.RequiredAt.Equal(current.RequiredAt)) {
			return nil, errors.New("aggregate_compute_state_inconsistent")
		}
		state = current
	}
	return state, nil
}
func aggregateHold(frame *LibraryAppendCommitRequest, records map[int64]*LibraryRecoveryRecord, now time.Time) bool {
	for _, task := range frame.ExpectedMembers {
		if task.NotBefore != nil && now.Before(*task.NotBefore) {
			return true
		}
		if d := records[task.ID].Deadline; d != nil {
			for _, hold := range []*time.Time{d.ExplicitHold, d.RetryDue} {
				if hold != nil && now.Before(*hold) {
					return true
				}
			}
		}
	}
	return false
}

// RequireLibraryAggregateCompute 固定一次调度 due，并合并到所有当前成员的现有 journal。
func (m *Manager) RequireLibraryAggregateCompute(ctx context.Context, frame *LibraryAppendCommitRequest) (time.Time, error) {
	var due time.Time
	err := m.withLibraryTaskFront(ctx, frame, func(tx *sql.Tx, records map[int64]*LibraryRecoveryRecord) error {
		sources, ids, err := aggregateGeneration(frame.ExpectedSession, records)
		if err != nil {
			return err
		}
		existing, err := requiredAggregateState(frame.ExpectedSession.ID, sources, ids, records)
		if err != nil {
			return err
		}
		var state LibraryAggregateComputeState
		if existing != nil {
			state = *existing
		} else {
			now := m.recoveryNow()
			value := now
			cfg := configs.GetCurrentConfig()
			if cfg == nil {
				return errors.New("aggregate_compute_config_unavailable")
			}
			if cfg.Subtitle.Schedule.Enabled {
				value, err = cfg.Subtitle.Schedule.NextRunAfter(now)
				if err != nil {
					return err
				}
			}
			state = LibraryAggregateComputeState{SessionID: frame.ExpectedSession.ID, Sources: sources, RequestIDs: ids, RequiredAt: now.UTC(), DueAt: value.UTC(), Required: true}
		}
		due = state.DueAt
		for _, task := range frame.ExpectedMembers {
			record := records[task.ID]
			if record.AggregateCompute != nil && record.AggregateCompute.Required && sameAggregateGeneration(record.AggregateCompute, state.SessionID, state.Sources, state.RequestIDs) && record.Deadline != nil && record.Deadline.ComputeDue != nil && record.Deadline.ComputeDue.Equal(state.DueAt) {
				continue
			}
			copy := state
			record.AggregateCompute = &copy
			if record.State == "" {
				record.State = "aggregate_compute_waiting"
			}
			if record.RecoveryClass == "" {
				record.RecoveryClass = "completed_media"
			}
			d := record.Deadline
			if d == nil {
				d = &CompletionDeadline{ExplicitHold: task.NotBefore, AppliedNotBefore: task.NotBefore}
				record.Deadline = d
			} else if !equalJSON(d.AppliedNotBefore, task.NotBefore) {
				d.ExplicitHold = task.NotBefore
				d.AppliedNotBefore = task.NotBefore
			}
			d.ComputeDue = &copy.DueAt
			d.ProvenanceRevision++
			record.ReasonCode = "aggregate_compute_required"
			if err = saveRecoveryTx(ctx, tx, record); err != nil {
				return err
			}
		}
		return nil
	})
	return due, err
}

// AllowLibraryAggregateCompute 不按当前时间重新生成 due，不把旧代许可借给新来源。
func (m *Manager) AllowLibraryAggregateCompute(ctx context.Context, frame *LibraryAppendCommitRequest) (bool, error) {
	allowed := false
	err := m.withLibraryTaskFront(ctx, frame, func(_ *sql.Tx, records map[int64]*LibraryRecoveryRecord) error {
		sources, ids, err := aggregateGeneration(frame.ExpectedSession, records)
		if err != nil {
			return err
		}
		state, err := requiredAggregateState(frame.ExpectedSession.ID, sources, ids, records)
		if err != nil {
			return err
		}
		now := m.recoveryNow()
		allowed = state != nil && frame.ExpectedTask.Status == PipelineStatusRunning && frame.ExpectedTask.StartedAt != nil && !frame.ExpectedTask.StartedAt.IsZero() && !now.Before(state.DueAt) && !aggregateHold(frame, records, now)
		return nil
	})
	return allowed, err
}

// captureLibraryAppendSnapshot 在一个只读事务内捕获完整前像，不写文件、不拿 work/season 锁。
func (m *Manager) captureLibraryAppendSnapshot(ctx context.Context, request LibraryPublicationRequest) error {
	frame := request.AppendCommit
	if frame == nil || request.TaskID <= 0 || frame.OperationID == "" || frame.AssignmentRevision <= 0 || frame.SeasonPath == "" || frame.TargetPath != request.Path || filepath.Dir(frame.TargetPath) != frame.SeasonPath {
		return errors.New("append_snapshot_frame_incomplete")
	}
	s, ok := m.store.(*SQLiteStore)
	if !ok {
		return errors.New("append_snapshot_requires_sqlite")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	primary, err := scanTask(tx.QueryRowContext(ctx, taskCompletionSelect, request.TaskID))
	if err != nil {
		return err
	}
	if request.SessionID != primary.RecordInfo.LiveSessionID || request.SessionID == "" {
		return errors.New("append_snapshot_owner_mismatch")
	}
	session, err := readRecordingSession(ctx, tx, request.SessionID)
	if err != nil {
		return err
	}
	var members []*PipelineTask
	for key := range session.Tasks {
		id, e := strconv.ParseInt(key, 10, 64)
		if e != nil || id <= 0 || fmt.Sprint(id) != key {
			return errors.New("append_snapshot_member_invalid")
		}
		task, e := scanTask(tx.QueryRowContext(ctx, taskCompletionSelect, id))
		if e != nil {
			return e
		}
		members = append(members, task)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	if frame.ExpectedTask != nil && !sameCompletionTask(frame.ExpectedTask, primary) || len(frame.ExpectedMembers) > 0 && !equalJSON(frame.ExpectedMembers, members) || frame.ExpectedSession.ID != "" && !equalJSON(frame.ExpectedSession, session) {
		return errors.New("append_snapshot_supplied_front_changed")
	}
	snapshot := *frame
	snapshot.ExpectedTask = primary
	snapshot.ExpectedMembers = members
	snapshot.ExpectedSession = session
	if _, err = validateAppendFrame(&snapshot); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	*frame = snapshot
	return nil
}
