package pipeline

import (
	"context"
	"database/sql"
	"errors"

	"github.com/bililive-go/bililive-go/src/configs"
	uuid "github.com/satori/go.uuid"
)

type RecordingRunState string

const (
	RecordingRunWaiting    RecordingRunState = "waiting"
	RecordingRunRecording  RecordingRunState = "recording"
	RecordingRunFinalizing RecordingRunState = "finalizing"
	RecordingRunCompleted  RecordingRunState = "completed"
	RecordingRunPaused     RecordingRunState = "paused"
	RecordingRunStopped    RecordingRunState = "stopped"
)

// RecordingRun 是用户开启的一轮录制，不随切片或字幕任务数量重置。
type RecordingRun struct {
	LiveID      string                `json:"live_id"`
	RunID       string                `json:"run_id"`
	Mode        configs.RecordingMode `json:"recording_mode"`
	State       RecordingRunState     `json:"recording_state"`
	SessionID   string                `json:"live_session_id,omitempty"`
	PauseReason string                `json:"pause_reason,omitempty"`
}

func readRecordingRun(ctx context.Context, tx *sql.Tx, liveID string) (RecordingRun, error) {
	var run RecordingRun
	err := tx.QueryRowContext(ctx, `SELECT live_id, run_id, mode, state, session_id, pause_reason FROM recording_runs WHERE live_id = ?`, liveID).
		Scan(&run.LiveID, &run.RunID, &run.Mode, &run.State, &run.SessionID, &run.PauseReason)
	return run, err
}

func (s *SQLiteStore) GetRecordingRun(ctx context.Context, liveID string) (RecordingRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RecordingRun{}, err
	}
	defer tx.Rollback()
	return readRecordingRun(ctx, tx, liveID)
}

func saveRecordingRun(ctx context.Context, tx *sql.Tx, run RecordingRun) error {
	_, err := tx.ExecContext(ctx, `UPDATE recording_runs SET run_id = ?, mode = ?, state = ?, session_id = ?, pause_reason = ? WHERE live_id = ?`, run.RunID, run.Mode, run.State, run.SessionID, run.PauseReason, run.LiveID)
	return err
}

func (s *SQLiteStore) updateRecordingRun(ctx context.Context, liveID string, update func(*RecordingRun) error) (RecordingRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecordingRun{}, err
	}
	defer tx.Rollback()
	run, err := readRecordingRun(ctx, tx, liveID)
	if err != nil {
		return run, err
	}
	if err := update(&run); err != nil {
		return RecordingRun{}, err
	}
	if err = saveRecordingRun(ctx, tx, run); err != nil {
		return RecordingRun{}, err
	}
	return run, tx.Commit()
}

// EnsureRecordingRun 只同步配置快照，不把已完成额度当成新一轮。
func (s *SQLiteStore) EnsureRecordingRun(ctx context.Context, liveID string, mode configs.RecordingMode) (RecordingRun, error) {
	if liveID == "" {
		return RecordingRun{}, errors.New("recording run requires live identity")
	}
	if err := mode.Validate(); err != nil {
		return RecordingRun{}, err
	}
	mode = (configs.LiveRoom{RecordingMode: mode}).EffectiveRecordingMode()
	id, err := uuid.NewV4()
	if err != nil {
		return RecordingRun{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecordingRun{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO recording_runs(live_id, run_id, mode, state) VALUES(?, ?, ?, ?) ON CONFLICT(live_id) DO UPDATE SET mode = excluded.mode`, liveID, id.String(), mode, RecordingRunWaiting)
	if err != nil {
		return RecordingRun{}, err
	}
	run, err := readRecordingRun(ctx, tx, liveID)
	if err != nil {
		return RecordingRun{}, err
	}
	return run, tx.Commit()
}

func (s *SQLiteStore) StopRecordingRun(ctx context.Context, liveID string) (RecordingRun, error) {
	return s.updateRecordingRun(ctx, liveID, func(run *RecordingRun) error {
		run.State = RecordingRunStopped
		run.PauseReason = "user_stop"
		return nil
	})
}

// RestartRecordingRun 仅由用户显式重新开启调用，拒绝重置尚在录制或判定中的一轮。
func (s *SQLiteStore) RestartRecordingRun(ctx context.Context, liveID string) (RecordingRun, error) {
	return s.updateRecordingRun(ctx, liveID, func(run *RecordingRun) error {
		if run.State == RecordingRunRecording || run.State == RecordingRunFinalizing {
			return errors.New("cannot rearm an active recording run")
		}
		id, err := uuid.NewV4()
		if err != nil {
			return err
		}
		run.RunID = id.String()
		run.State = RecordingRunWaiting
		run.SessionID = ""
		run.PauseReason = ""
		return nil
	})
}

func resolveRecordingOutcome(run *RecordingRun, playable bool, failure string) {
	run.State = RecordingRunWaiting
	run.PauseReason = ""
	if playable && run.Mode == configs.RecordingModeOnce {
		run.State = RecordingRunCompleted
		run.PauseReason = "once_completed"
	} else if !playable && failure != "" && run.Mode == configs.RecordingModeOnce {
		run.State = RecordingRunPaused
		run.PauseReason = "media_verification_failed"
	}
}

// 场次封口和最终 producer 退出均在同一 SQLite 事务内推进录制额度，独立于发布成功。
func finishRecordingRunCapture(ctx context.Context, tx *sql.Tx, session RecordingSession) error {
	run, err := readRecordingRun(ctx, tx, session.LiveID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.SessionID != session.ID || (run.State != RecordingRunRecording && run.State != RecordingRunFinalizing) || session.EndReason == "" {
		return nil
	}
	if session.EndReason == "user_stop" {
		run.State = RecordingRunStopped
		run.PauseReason = "user_stop"
		return saveRecordingRun(ctx, tx, run)
	}
	run.State = RecordingRunFinalizing
	playable := false
	// 发布失败与是否留下可播放录制分开判定；完整的空视频证据允许继续等待。
	failure := ""
	if len(session.Producers) == 0 {
		failure = "capture producer was not registered"
	}
	for producer, finished := range session.Producers {
		if !finished {
			return saveRecordingRun(ctx, tx, run)
		}
		switch session.CaptureEvidence[producer] {
		case "playable":
			playable = true
		case "empty":
		default:
			failure = "capture verification is incomplete"
		}
	}
	resolveRecordingOutcome(&run, playable, failure)
	return saveRecordingRun(ctx, tx, run)
}

func (s *SQLiteStore) RecoverRecordingRun(ctx context.Context, liveID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE recording_runs
		SET state = CASE WHEN mode = 'once' THEN 'paused' ELSE 'waiting' END,
		    pause_reason = CASE WHEN mode = 'once' THEN 'unconfirmed_session_after_restart' ELSE '' END
		WHERE state IN ('recording', 'finalizing') AND live_id = ?`, liveID)
	return err
}
