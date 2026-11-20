package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/subtitle"
)

// LibraryRecoveryRecord 专用恢复记录。Plan 一经插入不可修改；状态不是文件真相。
type LibraryRecoveryRecord struct {
	AggregateCompute           *LibraryAggregateComputeState `json:"aggregate_compute,omitempty"`
	CompletedSources           []SessionMediaSource          `json:"completed_sources,omitempty"`
	CompletionSchema           int                           `json:"completion_schema,omitempty"`
	CompletionRefs             []CompletionRef               `json:"completion_refs,omitempty"`
	RecoveryClass              string                        `json:"recovery_class,omitempty"`
	CompletionWorkAttempts     int                           `json:"completion_work_attempts,omitempty"`
	CompletionState            string                        `json:"completion_state,omitempty"`
	Deadline                   *CompletionDeadline           `json:"deadline,omitempty"`
	ReasonCode                 string                        `json:"reason_code,omitempty"`
	BlockingInputs             []int                         `json:"blocking_input_indices,omitempty"`
	BlockingMembers            []int64                       `json:"blocking_member_ids,omitempty"`
	LastObservedAt             time.Time                     `json:"last_observed_at,omitempty"`
	Observations               int                           `json:"observations,omitempty"`
	WorkerSubmissionsAccepting *bool                         `json:"worker_submissions_accepting,omitempty"`
	InvalidJournal             bool                          `json:"-"`

	ProtectedSources        []subtitle.LibraryFileState   `json:"protected_sources,omitempty"`
	PublishedMissingSources []string                      `json:"published_missing_sources,omitempty"`
	Fingerprint             string                        `json:"fingerprint,omitempty"`
	OperationID             string                        `json:"operation_id"`
	Kind                    string                        `json:"kind"`
	Season                  string                        `json:"season"`
	TaskID                  int64                         `json:"task_id,omitempty"`
	State                   string                        `json:"state"`
	Reason                  string                        `json:"reason,omitempty"`
	Revision                int64                         `json:"revision"`
	Attempts                int                           `json:"attempts"`
	NextAttempt             time.Time                     `json:"next_attempt_at"`
	Plan                    *subtitle.LibraryIdentityPlan `json:"plan,omitempty"`
	PlanHash                string                        `json:"plan_hash,omitempty"`
	Expected                LibraryIndexExpected          `json:"expected"`
	Observation             LibraryIndexObservation       `json:"observation"`
	BeforeCaptured          bool                          `json:"before_captured"`
	Active                  bool                          `json:"active"`
}

func (s *SQLiteStore) initLibraryRecoverySchema(dbPath string) error {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='pipeline_library_recoveries'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		var tasks int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='pipeline_tasks'`).Scan(&tasks); err != nil {
			return err
		}
		if tasks > 0 {
			backup := fmt.Sprintf("%s.before-library-recovery.%d.sqlite", dbPath, time.Now().UnixNano())
			if _, err := os.Lstat(backup); os.IsNotExist(err) {
				if _, err = s.db.Exec(`VACUUM INTO ?`, backup); err != nil {
					return fmt.Errorf("online backup before recovery migration: %w", err)
				}
			} else if err != nil {
				return err
			}
		}
	}
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS pipeline_library_recoveries (
 operation_id TEXT PRIMARY KEY, kind TEXT NOT NULL, season TEXT NOT NULL, task_id INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL, active INTEGER NOT NULL, revision INTEGER NOT NULL, record_json TEXT NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS idx_library_recovery_active_season ON pipeline_library_recoveries(season) WHERE active=1 AND kind='files';`)
	return err
}
func (s *SQLiteStore) saveLibraryRecovery(ctx context.Context, r *LibraryRecoveryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *r
	copy.Revision++
	data, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	if r.Revision == 0 {
		_, err = s.db.ExecContext(ctx, `INSERT INTO pipeline_library_recoveries(operation_id,kind,season,task_id,state,active,revision,record_json) VALUES(?,?,?,?,?,?,?,?)`, r.OperationID, r.Kind, r.Season, r.TaskID, r.State, r.Active, copy.Revision, string(data))
	} else {
		var result sql.Result
		result, err = s.db.ExecContext(ctx, `UPDATE pipeline_library_recoveries SET state=?,active=?,revision=?,record_json=? WHERE operation_id=? AND revision=?`, r.State, r.Active, copy.Revision, string(data), r.OperationID, r.Revision)
		if err == nil {
			n, _ := result.RowsAffected()
			if n != 1 {
				err = errors.New("recovery revision changed")
			}
		}
	}
	if err == nil {
		*r = copy
	}
	return err
}
func (s *SQLiteStore) libraryRecoveries(ctx context.Context) ([]*LibraryRecoveryRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id,kind,season,task_id,revision,record_json FROM pipeline_library_recoveries ORDER BY operation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LibraryRecoveryRecord
	for rows.Next() {
		var data string
		var r LibraryRecoveryRecord
		var operation, kind, season string
		var taskID, revision int64
		if err = rows.Scan(&operation, &kind, &season, &taskID, &revision, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &r); err != nil || r.OperationID != operation || r.Kind != kind || r.Season != season || r.TaskID != taskID || r.Revision != revision || (r.CompletionSchema != 0 && r.CompletionSchema != 1) {
			r = LibraryRecoveryRecord{OperationID: operation, Kind: kind, Season: season, TaskID: taskID, Revision: revision, State: "journal_invalid", ReasonCode: "journal_invalid", Active: true, InvalidJournal: true}
			out = append(out, &r)
			continue
		}
		for _, ref := range r.CompletionRefs {
			if ref.Request.Schema != 1 {
				r.InvalidJournal = true
				r.State = "journal_invalid"
				r.ReasonCode = "journal_invalid"
				r.Active = true
			}
		}
		if r.Plan != nil && r.Plan.Hash() != r.PlanHash {
			r.InvalidJournal = true
			r.State = "journal_invalid"
			r.ReasonCode = "journal_invalid"
			r.Active = true
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ClaimTask 在同一事务内领取 pending 检查点；取消/重试后旧快照不能启动。
func (s *SQLiteStore) ClaimTask(ctx context.Context, task *PipelineTask, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, _ := json.Marshal(task.CurrentFiles)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rawDeadline any
	if err = tx.QueryRowContext(ctx, `SELECT not_before FROM pipeline_tasks WHERE id=? AND status='pending' AND current_stage=? AND current_files_json=?`, task.ID, task.CurrentStage, string(files)).Scan(&rawDeadline); err != nil {
		return err
	}
	deadline, err := claimDeadline(rawDeadline)
	if err != nil {
		return err
	}
	if deadline != nil && now.Before(*deadline) {
		return errors.New("task claim not due")
	}
	now = now.UTC()
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_tasks SET status='running',started_at=?,completed_at=NULL,error_message='' WHERE id=? AND status='pending' AND current_stage=? AND current_files_json=?`, now, task.ID, task.CurrentStage, string(files))
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("task claim changed")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	task.Status = PipelineStatusRunning
	task.StartedAt = &now
	return nil
}

// saveClaimedTask 仅本次 started_at 的 running claim 能写进度或结束，不能复活 cancelled。
func (s *SQLiteStore) saveClaimedTask(ctx context.Context, task *PipelineTask, claim time.Time) error {
	return s.saveClaimedTaskDeadline(ctx, task, claim, "", time.Time{})
}
func (s *SQLiteStore) saveClaimedTaskDeadline(ctx context.Context, task *PipelineTask, claim time.Time, reason string, due time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, _ := json.Marshal(task.CurrentFiles)
	results, _ := json.Marshal(task.StageResults)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanTask(tx.QueryRowContext(ctx, taskCompletionSelect, task.ID))
	if err != nil {
		return err
	}
	if current.NotBefore != nil && (task.NotBefore == nil || current.NotBefore.After(*task.NotBefore)) {
		task.NotBefore = current.NotBefore
	}
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_tasks SET status=?,current_files_json=?,current_stage=?,stage_results_json=?,progress=?,started_at=?,completed_at=?,not_before=?,error_message=?,can_retry=? WHERE id=? AND status='running' AND started_at=?`, task.Status, string(files), task.CurrentStage, string(results), task.Progress, task.StartedAt, task.CompletedAt, task.NotBefore, task.ErrorMessage, task.CanRetry, task.ID, claim)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("task claim no longer owned")
	}
	if err = saveCompletionDeadlineTx(ctx, tx, task.ID, current.NotBefore, task.NotBefore, reason, due); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLiteStore) cancelTask(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE pipeline_tasks SET status='cancelled',completed_at=? WHERE id=? AND status IN ('pending','running','failed')`, time.Now(), id)
	return err
}

// claimDeadline 兼容 SQLite 驱动的历史时间文本，按数值时区比较绝对时间。
func claimDeadline(value any) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if t, ok := value.(time.Time); ok {
		return &t, nil
	}
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return nil, errors.New("unknown claim deadline type")
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999 -0700", "2006-01-02 15:04:05.999999999"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return &parsed, nil
		}
	}
	fields := strings.Fields(raw)
	if len(fields) >= 3 {
		if parsed, err := time.Parse("2006-01-02 15:04:05.999999999 -0700", strings.Join(fields[:3], " ")); err == nil {
			return &parsed, nil
		}
	}
	return nil, errors.New("invalid claim deadline; refusing claim")
}
