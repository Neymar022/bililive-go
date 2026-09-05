package pipeline

import (
	"context"
	"errors"

	"github.com/bililive-go/bililive-go/src/configs"
)

type recordingRunStore interface {
	EnsureRecordingRun(context.Context, string, configs.RecordingMode) (RecordingRun, error)
	GetRecordingRun(context.Context, string) (RecordingRun, error)
	RecoverRecordingRun(context.Context, string) error
	RestartRecordingRun(context.Context, string) (RecordingRun, error)
	StopRecordingRun(context.Context, string) (RecordingRun, error)
}

func (m *Manager) runStore() (recordingRunStore, error) {
	store, ok := m.store.(recordingRunStore)
	if !ok {
		return nil, errors.New("persistent recording run store unavailable")
	}
	return store, nil
}

// ConfigureRecordingRun 首次绑定真实房间身份时恢复额度；先同步当前配置，后恢复状态。
// 初始化占位 Live 和平台返回的真实 LiveID 均走此入口，避免猜测平台自定义身份。
func (m *Manager) ConfigureRecordingRun(liveID string, mode configs.RecordingMode) (RecordingRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	store, err := m.runStore()
	if err != nil {
		return RecordingRun{}, err
	}
	run, err := store.EnsureRecordingRun(m.ctx, liveID, mode)
	if err != nil {
		return RecordingRun{}, err
	}
	if !m.initializedRecordingRuns[liveID] {
		if err := store.RecoverRecordingRun(m.ctx, liveID); err != nil {
			return RecordingRun{}, err
		}
		run, err = store.GetRecordingRun(m.ctx, liveID)
		if err != nil {
			return RecordingRun{}, err
		}
		if m.initializedRecordingRuns == nil {
			m.initializedRecordingRuns = make(map[string]bool)
		}
		m.initializedRecordingRuns[liveID] = true
	}
	return run, nil
}

func (m *Manager) RestartRecordingRun(liveID string) (RecordingRun, error) {
	store, err := m.runStore()
	if err != nil {
		return RecordingRun{}, err
	}
	return store.RestartRecordingRun(m.ctx, liveID)
}

func (m *Manager) StopRecordingRun(liveID string) (RecordingRun, error) {
	store, err := m.runStore()
	if err != nil {
		return RecordingRun{}, err
	}
	return store.StopRecordingRun(m.ctx, liveID)
}

func (r RecordingRun) Terminal() bool {
	return r.State == RecordingRunCompleted || r.State == RecordingRunPaused || r.State == RecordingRunStopped
}
