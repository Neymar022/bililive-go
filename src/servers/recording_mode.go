package servers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/instance"
	"github.com/bililive-go/bililive-go/src/listeners"
	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pkg/events"
	"github.com/bililive-go/bililive-go/src/recorders"
	"github.com/bililive-go/bililive-go/src/types"
)

var recordingControls sync.Map

var errRecordingControlURLChange = errors.New("更换房间 URL 与录制模式或启停状态请分开保存")

func lockRecordingControl(id types.LiveID) func() {
	value, _ := recordingControls.LoadOrStore(id, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func startListening(ctx context.Context, l live.Live) error {
	defer lockRecordingControl(l.GetLiveId())()
	inst := instance.GetInstance(ctx)
	lm := inst.ListenerManager.(listeners.Manager)
	if lm.HasListener(ctx, l.GetLiveId()) {
		return listeners.ErrListenerExist
	}
	if pm, ok := inst.PipelineManager.(*pipeline.Manager); ok {
		waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if err := events.WaitForOrderedEvents(waitCtx, inst.EventDispatcher.(events.Dispatcher), string(l.GetLiveId())); err != nil {
			return err
		}
		if rm, ok := inst.RecorderManager.(recorders.Manager); ok && rm.HasRecorder(ctx, l.GetLiveId()) {
			return errors.New("上一轮录制仍在收尾，不能重新开启")
		}
		if err := syncRoomRecordingMode(ctx, l.GetRawUrl()); err != nil {
			return err
		}
		if _, err := pm.RestartRecordingRun(string(l.GetLiveId())); err != nil {
			return err
		}
	}
	if err := lm.AddListener(ctx, l); err != nil {
		if pm, ok := inst.PipelineManager.(*pipeline.Manager); ok {
			_, stopErr := pm.StopRecordingRun(string(l.GetLiveId()))
			return errors.Join(err, stopErr)
		}
		return err
	}
	return nil
}

func stopListening(ctx context.Context, liveID types.LiveID) error {
	defer lockRecordingControl(liveID)()
	inst := instance.GetInstance(ctx)
	l, exists := inst.Lives.Get(liveID)
	if !exists {
		return listeners.ErrListenerNotExist
	}
	if pm, ok := inst.PipelineManager.(*pipeline.Manager); ok {
		if err := syncRoomRecordingMode(ctx, l.GetRawUrl()); err != nil {
			return err
		}
		if _, err := pm.StopRecordingRun(string(liveID)); err != nil {
			return err
		}
	}
	lm := inst.ListenerManager.(listeners.Manager)
	if _, err := lm.GetListener(ctx, liveID); err == nil {
		if err := lm.RemoveListener(ctx, liveID); err != nil {
			return err
		}
	}
	ed := inst.EventDispatcher.(events.Dispatcher)
	ed.DispatchEvent(events.NewOrderedEvent(listeners.UserStop, l, string(liveID)))
	waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return events.WaitForOrderedEvents(waitCtx, ed, string(liveID))
}

func requestedRecordingMode(updates map[string]any) (configs.RecordingMode, bool, error) {
	value, exists := updates["recording_mode"]
	if !exists {
		return "", false, nil
	}
	mode, ok := value.(string)
	if !ok || mode == "" {
		return "", true, errors.New("recording_mode 必须为 once 或 continuous")
	}
	return configs.RecordingMode(mode), true, configs.RecordingMode(mode).Validate()
}

func populateRecordingRunInfo(ctx context.Context, info *live.Info) {
	info.RecordingMode = configs.RecordingModeContinuous
	info.RecordingState = string(pipeline.RecordingRunStopped)
	info.RecordingPauseReason = ""
	if info.Listening {
		info.RecordingState = string(pipeline.RecordingRunWaiting)
	}
	if cfg := configs.GetCurrentConfig(); cfg != nil {
		if room, err := cfg.GetLiveRoomByUrl(info.Live.GetRawUrl()); err == nil {
			info.RecordingMode = room.EffectiveRecordingMode()
		}
	}
	if pm, ok := instance.GetInstance(ctx).PipelineManager.(*pipeline.Manager); ok {
		if run, err := pm.RecordingRun(string(info.Live.GetLiveId())); err == nil {
			info.RecordingState = string(run.State)
			info.RecordingPauseReason = run.PauseReason
			if run.Terminal() {
				info.Listening = false
			} else if run.State == pipeline.RecordingRunWaiting && !info.Listening {
				info.RecordingState = string(pipeline.RecordingRunStopped)
			}
		}
	}
}

func syncRoomRecordingMode(ctx context.Context, roomURL string) error {
	inst := instance.GetInstance(ctx)
	pm, ok := inst.PipelineManager.(*pipeline.Manager)
	if !ok {
		return errors.New("录制额度存储不可用")
	}
	room, err := configs.GetCurrentConfig().GetLiveRoomByUrl(roomURL)
	if err != nil {
		return err
	}
	if room.LiveId == "" {
		return errors.New("房间身份尚未初始化，不能应用录制模式")
	}
	_, err = pm.ConfigureRecordingRun(string(room.LiveId), room.EffectiveRecordingMode())
	return err
}

func applyRoomRecordingControls(ctx context.Context, previous, room configs.LiveRoom, version int64, hasMode bool) error {
	if !hasMode && previous.IsListening == room.IsListening {
		return nil
	}
	var err error
	if hasMode {
		err = syncRoomRecordingMode(ctx, room.Url)
	}
	if err == nil && previous.IsListening != room.IsListening {
		inst := instance.GetInstance(ctx)
		if room.IsListening {
			l, ok := inst.Lives.Get(room.LiveId)
			if !ok {
				err = errors.New("房间身份尚未初始化，不能开启录制")
			} else {
				err = startListening(inst.Ctx, l)
			}
		} else {
			err = stopListening(inst.Ctx, room.LiveId)
		}
	}
	if err == nil {
		return nil
	}
	// 仅恢复本次未能生效的控制字段；并发新配置优先，禁止整份覆盖回滚。
	_, rollbackErr := configs.UpdateCAS(version, func(c *configs.Config) error {
		target, findErr := c.GetLiveRoomByUrl(room.Url)
		if findErr != nil {
			return findErr
		}
		target.RecordingMode = previous.RecordingMode
		target.IsListening = previous.IsListening
		return nil
	})
	if rollbackErr != nil {
		return errors.Join(err, fmt.Errorf("控制字段回滚未完成: %w", rollbackErr))
	}
	if hasMode {
		// 同步失败仍返回原始错误；不会重开已停止或已完成的额度。
		_ = syncRoomRecordingMode(ctx, previous.Url)
	}
	return err
}
