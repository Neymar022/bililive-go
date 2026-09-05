package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
)

// ResumeTask 校验保留下来的阶段输入后续跑，不重置时间计划或已完成结果。
func (m *Manager) ResumeTask(taskID int64) error {
	task, err := m.store.GetTask(m.ctx, taskID)
	if err != nil {
		return err
	}
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
	store, ok := m.store.(interface {
		ResumeTask(context.Context, *PipelineTask) error
	})
	if !ok {
		return errors.New("task store does not support atomic checkpoint resume")
	}
	if err := store.ResumeTask(m.ctx, task); err != nil {
		return err
	}
	updated, err := m.store.GetTask(m.ctx, taskID)
	if err == nil {
		m.broadcastTaskUpdate(updated)
	}
	// 复用既有轮询，避免绕过 not_before 或在维护批次内直接启动 worker。
	return err
}

// ResumeTask 用预期检查点比较并交换状态，拒绝覆盖并发取消或重试。
func (s *SQLiteStore) ResumeTask(ctx context.Context, expected *PipelineTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentFiles, stageResults string
	var stage int
	var status PipelineStatus
	var retry bool
	var recordInfo, config string
	err = tx.QueryRowContext(ctx, `SELECT status, current_stage, current_files_json, stage_results_json, can_retry, record_info_json, pipeline_config_json FROM pipeline_tasks WHERE id = ?`, expected.ID).Scan(&status, &stage, &currentFiles, &stageResults, &retry, &recordInfo, &config)
	if err != nil {
		return err
	}
	var files []FileInfo
	var results []StageResult
	var record RecordInfo
	var pipelineConfig *PipelineConfig
	if json.Unmarshal([]byte(currentFiles), &files) != nil || json.Unmarshal([]byte(stageResults), &results) != nil || json.Unmarshal([]byte(recordInfo), &record) != nil || json.Unmarshal([]byte(config), &pipelineConfig) != nil ||
		status != PipelineStatusFailed || !retry || stage != expected.CurrentStage || !reflect.DeepEqual(files, expected.CurrentFiles) || !reflect.DeepEqual(results, expected.StageResults) || !reflect.DeepEqual(record, expected.RecordInfo) || !reflect.DeepEqual(pipelineConfig, expected.PipelineConfig) {
		return errors.New("task checkpoint changed; resume refused")
	}
	_, err = tx.ExecContext(ctx, `UPDATE pipeline_tasks SET status = ?, started_at = NULL, completed_at = NULL, error_message = '' WHERE id = ?`, PipelineStatusPending, expected.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
