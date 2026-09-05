package configs

import "fmt"

type RecordingMode string

const (
	RecordingModeOnce       RecordingMode = "once"
	RecordingModeContinuous RecordingMode = "continuous"
)

func (m RecordingMode) Validate() error {
	switch m {
	case "", RecordingModeOnce, RecordingModeContinuous:
		return nil
	default:
		return fmt.Errorf("recording_mode 必须为 once 或 continuous，当前值: %q", m)
	}
}

// EffectiveRecordingMode 保持旧配置和旧 API 省略字段时的连续行为。
func (l LiveRoom) EffectiveRecordingMode() RecordingMode {
	if l.RecordingMode == "" {
		return RecordingModeContinuous
	}
	return l.RecordingMode
}
