package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/subtitle"
)

// ResumeTask 校验保留下来的阶段输入后续跑，不重置时间计划或已完成结果。
func (m *Manager) ResumeTask(taskID int64) error {
	return m.resumeTask(taskID, nil)
}

func (m *Manager) resumeTask(taskID int64, automaticAt *time.Time) error {
	unlock := m.lockCompletionTask(taskID)
	defer unlock()
	task, err := m.store.GetTask(m.ctx, taskID)
	if err != nil {
		return err
	}
	if err = m.validateTaskResume(task); err != nil {
		return err
	}
	if store, ok := m.store.(*SQLiteStore); ok {
		records, err := store.libraryRecoveries(m.ctx)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.Kind == "task" && record.TaskID == taskID && len(record.PublishedMissingSources) > 0 {
				if err := validateRecoverySources(record); err != nil {
					return err
				}
			}
		}
	}
	if store, ok := m.store.(*SQLiteStore); ok {
		err = store.resumeTask(m.ctx, task, automaticAt)
	} else if store, ok := m.store.(interface {
		ResumeTask(context.Context, *PipelineTask) error
	}); ok && automaticAt == nil {
		err = store.ResumeTask(m.ctx, task)
	} else {
		return errors.New("task store does not support atomic checkpoint resume")
	}
	if err != nil {
		return err
	}
	if err := m.recoveryBoundary("resume_committed"); err != nil {
		return err
	}
	updated, err := m.store.GetTask(m.ctx, taskID)
	if err == nil {
		m.broadcastTaskUpdate(updated)
	}
	if err == nil {
		if e := m.recoveryBoundary("resume_response"); e != nil {
			return e
		}
	}
	// 复用既有轮询，避免绕过 not_before 或在维护批次内直接启动 worker。
	return err
}

func (m *Manager) validateTaskResume(task *PipelineTask) error {
	if task.Status != PipelineStatusFailed || !task.CanRetry {
		return errors.New("only retryable failed tasks can resume")
	}
	if task.RecordInfo.LiveSessionID != "" && task.RecordInfo.RecordingProducerID == "" {
		return errors.New("historical live session requires verified input closure migration before resume")
	}
	if task.PipelineConfig == nil || task.CurrentStage < 0 || task.CurrentStage >= len(task.PipelineConfig.Stages) || len(task.CurrentFiles) == 0 {
		return errors.New("task has no valid stage checkpoint")
	}
	for _, input := range task.CurrentFiles {
		file, err := os.Open(input.Path)
		if err != nil {
			if os.IsNotExist(err) && m.hasPublishedRecordingInput(task, input.Path) {
				continue
			}
			return fmt.Errorf("checkpoint input unavailable: %w", err)
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("checkpoint input is not a nonempty regular file: %s", input.Path)
		}
	}
	return nil
}

func (m *Manager) hasPublishedRecordingInput(task *PipelineTask, input string) bool {
	if task.PipelineConfig.Stages[task.CurrentStage].Name != StageNameSubtitleGenerate || task.RecordInfo.RecordingProducerID == "" {
		return false
	}
	store, err := m.recordingStore()
	if err != nil {
		return false
	}
	session, err := store.RecordingSession(m.ctx, task.RecordInfo.LiveSessionID)
	if err != nil || !session.Ready() {
		return false
	}
	cfg := configs.GetCurrentConfig()
	if cfg == nil {
		return false
	}
	for _, source := range session.Sources() {
		if source.TaskID != task.ID || source.InputPath != input || source.ProducerID != task.RecordInfo.RecordingProducerID {
			continue
		}
		meta, err := subtitle.LoadMetadata(source.MetadataPath)
		stem := strings.TrimSuffix(source.LibraryPath, ".mp4")
		if err != nil || meta.Status != subtitle.StatusCompleted || len(meta.Segments) == 0 || meta.SourcePath != input || meta.OutputPath != source.LibraryPath || meta.SRTPath != stem+".srt" || meta.ASSPath != stem+".ass" || meta.RecordMeta["live_session_id"] != session.ID || meta.RecordMeta["recording_producer_id"] != source.ProducerID || meta.RecordMeta["pipeline_task_id"] != strconv.FormatInt(task.ID, 10) {
			return false
		}
		for _, path := range []string{source.LibraryPath, meta.SRTPath, meta.ASSPath} {
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				return false
			}
		}
		return subtitle.ValidateRecordingSourcePublication(cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath), meta.SourcePublicationPath, input, session.ID, source.ProducerID) == nil
	}
	return false
}

// ResumeTask 用预期检查点比较并交换状态，拒绝覆盖并发取消或重试。
func (s *SQLiteStore) ResumeTask(ctx context.Context, expected *PipelineTask) error {
	return s.resumeTask(ctx, expected, nil)
}

func (s *SQLiteStore) resumeTask(ctx context.Context, expected *PipelineTask, automaticAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentFiles, stageResults, initialFiles string
	var stage int
	var status PipelineStatus
	var retry bool
	var recordInfo, config string
	var currentNotBefore, currentStartedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT status, current_stage, current_files_json, stage_results_json, can_retry, record_info_json, pipeline_config_json, not_before,started_at,initial_files_json FROM pipeline_tasks WHERE id = ?`, expected.ID).Scan(&status, &stage, &currentFiles, &stageResults, &retry, &recordInfo, &config, &currentNotBefore, &currentStartedAt, &initialFiles)
	if err != nil {
		return err
	}
	// 自动恢复到期后才入队，事务内复核并发延长的显式等待。
	if automaticAt != nil && currentNotBefore.Valid && automaticAt.Before(currentNotBefore.Time) {
		return errors.New("recovery hold not due")
	}
	var files, initial []FileInfo
	var results []StageResult
	var record RecordInfo
	var pipelineConfig *PipelineConfig
	// 只容许并发延长暂停时间。事务不会缩短当前时间或覆盖其来源。
	deadlineChanged := currentNotBefore.Valid != (expected.NotBefore != nil) ||
		(currentNotBefore.Valid && expected.NotBefore != nil && !currentNotBefore.Time.Equal(*expected.NotBefore))
	holdExtended := currentNotBefore.Valid && (expected.NotBefore == nil || currentNotBefore.Time.After(*expected.NotBefore))
	if json.Unmarshal([]byte(currentFiles), &files) != nil || json.Unmarshal([]byte(initialFiles), &initial) != nil || json.Unmarshal([]byte(stageResults), &results) != nil || json.Unmarshal([]byte(recordInfo), &record) != nil || json.Unmarshal([]byte(config), &pipelineConfig) != nil ||
		status != PipelineStatusFailed || expected.Status != PipelineStatusFailed || !retry || retry != expected.CanRetry || !reflect.DeepEqual(initial, expected.InitialFiles) || (deadlineChanged && !holdExtended) || currentStartedAt.Valid != (expected.StartedAt != nil) || currentStartedAt.Valid && !currentStartedAt.Time.Equal(*expected.StartedAt) || stage != expected.CurrentStage || !reflect.DeepEqual(files, expected.CurrentFiles) || !reflect.DeepEqual(results, expected.StageResults) || !reflect.DeepEqual(record, expected.RecordInfo) || !reflect.DeepEqual(pipelineConfig, expected.PipelineConfig) {
		return errors.New("task checkpoint changed; resume refused")
	}
	_, err = tx.ExecContext(ctx, `UPDATE pipeline_tasks SET status = ?, started_at = NULL, completed_at = NULL, error_message = '' WHERE id = ?`, PipelineStatusPending, expected.ID)
	if err != nil {
		return err
	}
	var recoveryJSON string
	if err = tx.QueryRowContext(ctx, `SELECT record_json FROM pipeline_library_recoveries WHERE operation_id=?`, fmt.Sprintf("task-%d", expected.ID)).Scan(&recoveryJSON); err == nil {
		var recovery LibraryRecoveryRecord
		if err = json.Unmarshal([]byte(recoveryJSON), &recovery); err != nil {
			return err
		}
		if recovery.State == "resume_requested" && recovery.RecoveryClass != "worker_completion" {
			recovery.State = "resumed"
			recovery.Revision++
			data, _ := json.Marshal(recovery)
			if _, err = tx.ExecContext(ctx, `UPDATE pipeline_library_recoveries SET state='resumed',revision=?,record_json=? WHERE operation_id=?`, recovery.Revision, string(data), recovery.OperationID); err != nil {
				return err
			}
			// 保留更晚 hold；due 来自本次恢复入队时间，不使用历史 created_at。
			if !currentNotBefore.Valid || currentNotBefore.Time.Before(recovery.NextAttempt) {
				if _, err = tx.ExecContext(ctx, `UPDATE pipeline_tasks SET not_before=? WHERE id=?`, recovery.NextAttempt.UTC(), expected.ID); err != nil {
					return err
				}
			}
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	return tx.Commit()
}
