package pipeline

import (
	"context"
	"errors"
	"time"
)

type recordingSessionStore interface {
	OpenRecordingSession(context.Context, string, string) error
	BeginRecordingProducer(context.Context, string) (RecordingOrigin, error)
	FinishRecordingProducer(context.Context, RecordingOrigin, string) error
	EndRecordingSession(context.Context, string, string) error
	RecordingSession(context.Context, string) (RecordingSession, error)
	CompleteRecordingTaskMedia(context.Context, string, int64, []SessionMediaSource) error
	RecoverRecordingSessions(context.Context) error
}

func (m *Manager) recordingStore() (recordingSessionStore, error) {
	store, ok := m.store.(recordingSessionStore)
	if !ok {
		return nil, errors.New("persistent recording session store unavailable")
	}
	return store, nil
}

func (m *Manager) OpenRecordingSession(id, liveID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recordingSessions, liveID)
	store, err := m.recordingStore()
	if err != nil {
		return err
	}
	if err := store.OpenRecordingSession(m.ctx, id, liveID); err != nil {
		return err
	}
	m.recordingSessions[liveID] = id
	return nil
}

func (m *Manager) BeginRecordingProducer(liveID string) (RecordingOrigin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recordingSessions[liveID] == "" {
		return RecordingOrigin{}, errors.New("current recording session was not opened successfully")
	}
	store, err := m.recordingStore()
	if err != nil {
		return RecordingOrigin{}, err
	}
	return store.BeginRecordingProducer(m.ctx, liveID)
}

func (m *Manager) FinishRecordingProducer(origin RecordingOrigin, failure string) error {
	store, err := m.recordingStore()
	if err != nil {
		return err
	}
	if err := store.FinishRecordingProducer(m.ctx, origin, failure); err != nil {
		return err
	}
	m.wakeScheduler()
	return nil
}

func (m *Manager) EndRecordingSession(liveID, reason string) error {
	store, err := m.recordingStore()
	if err != nil {
		return err
	}
	if err := store.EndRecordingSession(m.ctx, liveID, reason); err != nil {
		return err
	}
	m.wakeScheduler()
	return nil
}

func (m *Manager) completeSessionMedia(task *PipelineTask, sources []SessionMediaSource) (RecordingSession, error) {
	store, err := m.recordingStore()
	if err != nil {
		return RecordingSession{}, err
	}
	if err := store.CompleteRecordingTaskMedia(m.ctx, task.RecordInfo.LiveSessionID, task.ID, sources); err != nil {
		return RecordingSession{}, err
	}
	m.wakeScheduler()
	return store.RecordingSession(m.ctx, task.RecordInfo.LiveSessionID)
}

// RecordingEnqueueOrigin 记录真实分段起点，不把排队时间当作录制时间。
type RecordingEnqueueOrigin struct {
	RecordingOrigin
	StartTime time.Time
}
