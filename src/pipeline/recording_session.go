package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	uuid "github.com/satori/go.uuid"
)

// RecordingOrigin 将录制线程固定到开播时的场次，不能在末段入队时猜测最新场次。
type RecordingOrigin struct {
	SessionID  string
	ProducerID string
}

// SessionMediaSource 是已完成必要处理的分段，不代表整场已经公开。
type SessionMediaSource struct {
	TaskID       int64  `json:"task_id"`
	ProducerID   string `json:"producer_id"`
	InputPath    string `json:"input_path"`
	LibraryPath  string `json:"library_path"`
	MetadataPath string `json:"metadata_path"`
}

type recordingTaskMedia struct {
	Ready   bool                 `json:"ready"`
	Sources []SessionMediaSource `json:"sources"`
}

// RecordingSession 将封口事实与完整任务集合持久化在现有 pipeline 数据库。
type RecordingSession struct {
	ID        string                        `json:"id"`
	LiveID    string                        `json:"live_id"`
	EndReason string                        `json:"end_reason,omitempty"`
	Blocked   string                        `json:"blocked,omitempty"`
	Producers map[string]bool               `json:"producers"`
	Tasks     map[string]recordingTaskMedia `json:"tasks"`
}

func (s RecordingSession) Sealed() bool {
	if s.EndReason == "" || s.Blocked != "" || len(s.Producers) == 0 {
		return false
	}
	for _, finished := range s.Producers {
		if !finished {
			return false
		}
	}
	return true
}

func (s RecordingSession) Ready() bool {
	if !s.Sealed() || len(s.Tasks) == 0 {
		return false
	}
	for _, task := range s.Tasks {
		if !task.Ready {
			return false
		}
	}
	return true
}

func (s RecordingSession) Sources() []SessionMediaSource {
	var sources []SessionMediaSource
	for _, task := range s.Tasks {
		sources = append(sources, task.Sources...)
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].TaskID != sources[j].TaskID {
			return sources[i].TaskID < sources[j].TaskID
		}
		return sources[i].LibraryPath < sources[j].LibraryPath
	})
	return sources
}

func readRecordingSession(ctx context.Context, tx *sql.Tx, id string) (RecordingSession, error) {
	var content string
	var session RecordingSession
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM pipeline_recording_sessions WHERE id = ?`, id).Scan(&content); err != nil {
		return session, err
	}
	if err := json.Unmarshal([]byte(content), &session); err != nil {
		return session, err
	}
	if session.ID != id || session.Producers == nil || session.Tasks == nil {
		return session, errors.New("invalid recording session state")
	}
	return session, nil
}

func saveRecordingSession(ctx context.Context, tx *sql.Tx, session RecordingSession) error {
	content, err := json.Marshal(struct {
		RecordingSession
		Ready bool `json:"ready"`
	}{RecordingSession: session, Ready: session.Ready()})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE pipeline_recording_sessions SET state_json = ? WHERE id = ?`, string(content), session.ID)
	return err
}

func (s *SQLiteStore) updateRecordingSession(ctx context.Context, id string, update func(*RecordingSession, *sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, err := readRecordingSession(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := update(&session, tx); err != nil {
		return err
	}
	if err := saveRecordingSession(ctx, tx, session); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) OpenRecordingSession(ctx context.Context, id, liveID string) error {
	if id == "" || liveID == "" {
		return errors.New("recording session identity required")
	}
	session := RecordingSession{ID: id, LiveID: liveID, Producers: map[string]bool{}, Tasks: map[string]recordingTaskMedia{}}
	content, err := json.Marshal(session)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `INSERT INTO pipeline_recording_sessions(id, live_id, state_json) VALUES (?, ?, ?)`, id, liveID, string(content))
	return err
}

func (s *SQLiteStore) latestRecordingSessionID(ctx context.Context, liveID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM pipeline_recording_sessions WHERE live_id = ? ORDER BY rowid DESC LIMIT 1`, liveID).Scan(&id)
	return id, err
}

func (s *SQLiteStore) BeginRecordingProducer(ctx context.Context, liveID string) (RecordingOrigin, error) {
	id, err := s.latestRecordingSessionID(ctx, liveID)
	if err != nil {
		return RecordingOrigin{}, err
	}
	producerID, err := uuid.NewV4()
	if err != nil {
		return RecordingOrigin{}, err
	}
	origin := RecordingOrigin{SessionID: id, ProducerID: producerID.String()}
	err = s.updateRecordingSession(ctx, id, func(session *RecordingSession, _ *sql.Tx) error {
		if session.EndReason != "" || session.Blocked != "" {
			return errors.New("recording session is closed or requires confirmation")
		}
		session.Producers[origin.ProducerID] = false
		return nil
	})
	return origin, err
}

func (s *SQLiteStore) FinishRecordingProducer(ctx context.Context, origin RecordingOrigin, failure string) error {
	return s.updateRecordingSession(ctx, origin.SessionID, func(session *RecordingSession, _ *sql.Tx) error {
		if _, ok := session.Producers[origin.ProducerID]; !ok {
			return errors.New("unregistered recording producer")
		}
		session.Producers[origin.ProducerID] = true
		if failure != "" {
			session.Blocked = failure
		}
		return nil
	})
}

func (s *SQLiteStore) EndRecordingSession(ctx context.Context, liveID, reason string) error {
	if reason != "normal" && reason != "user_stop" {
		return errors.New("recording session end requires confirmed offline or user stop")
	}
	id, err := s.latestRecordingSessionID(ctx, liveID)
	if err != nil {
		return err
	}
	return s.updateRecordingSession(ctx, id, func(session *RecordingSession, _ *sql.Tx) error {
		session.EndReason = reason
		return nil
	})
}

func (s *SQLiteStore) RecordingSession(ctx context.Context, id string) (RecordingSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RecordingSession{}, err
	}
	defer tx.Rollback()
	session, err := readRecordingSession(ctx, tx, id)
	if err != nil {
		return session, err
	}
	for taskID, media := range session.Tasks {
		if media.Ready {
			continue
		}
		var status PipelineStatus
		if err := tx.QueryRowContext(ctx, `SELECT status FROM pipeline_tasks WHERE id = ?`, taskID).Scan(&status); err != nil {
			return session, fmt.Errorf("registered session task unavailable: %s: %w", taskID, err)
		}
		if status == PipelineStatusFailed || status == PipelineStatusCancelled {
			session.Blocked = fmt.Sprintf("registered task %s is %s; resume its checkpoint first", taskID, status)
		}
	}
	return session, nil
}

func registerRecordingTask(ctx context.Context, tx *sql.Tx, task *PipelineTask) error {
	if task.RecordInfo.RecordingProducerID == "" {
		return nil
	}
	session, err := readRecordingSession(ctx, tx, task.RecordInfo.LiveSessionID)
	if err != nil {
		return err
	}
	finished, exists := session.Producers[task.RecordInfo.RecordingProducerID]
	if !exists || finished || session.Blocked != "" {
		return errors.New("cannot enqueue after recording producer was sealed")
	}
	session.Tasks[strconv.FormatInt(task.ID, 10)] = recordingTaskMedia{}
	return saveRecordingSession(ctx, tx, session)
}

func (s *SQLiteStore) CompleteRecordingTaskMedia(ctx context.Context, sessionID string, taskID int64, sources []SessionMediaSource) error {
	return s.updateRecordingSession(ctx, sessionID, func(session *RecordingSession, tx *sql.Tx) error {
		key := strconv.FormatInt(taskID, 10)
		previous, exists := session.Tasks[key]
		if !exists {
			return fmt.Errorf("unregistered session task: %d", taskID)
		}
		var filesJSON, recordJSON string
		if err := tx.QueryRowContext(ctx, `SELECT current_files_json, record_info_json FROM pipeline_tasks WHERE id = ?`, taskID).Scan(&filesJSON, &recordJSON); err != nil {
			return err
		}
		var record RecordInfo
		if err := json.Unmarshal([]byte(recordJSON), &record); err != nil {
			return err
		}
		if _, exists := session.Producers[record.RecordingProducerID]; !exists || record.LiveSessionID != sessionID {
			return errors.New("session task recording origin mismatch")
		}
		var files []FileInfo
		if err := json.Unmarshal([]byte(filesJSON), &files); err != nil {
			return err
		}
		inputs := make(map[string]bool)
		for _, file := range files {
			if file.Type == FileTypeVideo {
				inputs[file.Path] = false
			}
		}
		if !previous.Ready && (len(inputs) == 0 || len(inputs) != len(sources)) {
			return errors.New("processed media does not cover the complete stage input set")
		}
		for i := range sources {
			if sources[i].InputPath == "" || sources[i].LibraryPath == "" || sources[i].MetadataPath == "" {
				return errors.New("session media reference is empty")
			}
			if !previous.Ready {
				seen, exists := inputs[sources[i].InputPath]
				if !exists || seen {
					return errors.New("processed media has an unknown or repeated stage input")
				}
				inputs[sources[i].InputPath] = true
			}
			sources[i].TaskID = taskID
			sources[i].ProducerID = record.RecordingProducerID
		}
		if previous.Ready && !reflect.DeepEqual(previous.Sources, sources) {
			return errors.New("completed session inputs changed; maintenance required")
		}
		session.Tasks[key] = recordingTaskMedia{Ready: true, Sources: sources}
		return nil
	})
}

func (s *SQLiteStore) RecoverRecordingSessions(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE pipeline_recording_sessions SET state_json = json_set(state_json, '$.ready', json('false'), '$.blocked', 'recording interrupted; input closure requires confirmation') WHERE json_extract(state_json, '$.end_reason') IS NULL OR EXISTS (SELECT 1 FROM json_each(state_json, '$.producers') WHERE value = 0)`)
	return err
}
