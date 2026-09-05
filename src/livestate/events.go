package livestate

import (
	"github.com/bililive-go/bililive-go/src/listeners"
	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/pkg/events"
	"github.com/bililive-go/bililive-go/src/recorders"
	"github.com/bluele/gcache"
	"github.com/sirupsen/logrus"
	"strconv"
)

// RegisterEventListeners 注册事件监听器，用于在直播状态变化时更新数据库
// 参数：
//   - ed: 事件分发器
//   - manager: LiveStateManager 实例
//   - cache: 缓存实例，用于获取直播间信息
func RegisterEventListeners(ed events.Dispatcher, manager *Manager, cache gcache.Cache, pipelines ...*pipeline.Manager) {
	if manager == nil {
		logrus.Debug("LiveStateManager 未初始化，跳过事件监听器注册")
		return
	}

	// 监听直播开始事件
	ed.AddEventListener(listeners.LiveStart, events.NewEventListener(func(event *events.Event) {
		l, ok := event.Object.(live.Live)
		if !ok {
			return
		}

		// 获取直播间信息
		liveID := string(l.GetLiveId())
		url := l.GetRawUrl()
		platform := l.GetPlatformCNName()
		hostName := ""
		roomName := ""

		// 尝试从缓存获取更多信息
		if cache != nil {
			if info, err := cache.Get(l); err == nil {
				if liveInfo, ok := info.(*live.Info); ok {
					hostName = liveInfo.HostName
					roomName = liveInfo.RoomName
				}
			}
		}

		id, err := manager.OnLiveStart(liveID, url, platform, hostName, roomName)
		if len(pipelines) > 0 && pipelines[0] != nil {
			if err == nil {
				err = pipelines[0].OpenRecordingSession(strconv.FormatInt(id, 10), liveID)
			}
			if err != nil {
				logrus.WithError(err).Error("无法登记完整录制场次，禁止自动发布")
			}
		}
	}))

	ed.AddEventListener(listeners.UserStop, events.NewEventListener(func(event *events.Event) {
		l, ok := event.Object.(live.Live)
		if !ok {
			return
		}
		liveID := string(l.GetLiveId())
		manager.OnUserStopMonitoring(liveID)
		if len(pipelines) > 0 && pipelines[0] != nil {
			if err := pipelines[0].EndRecordingSession(liveID, "user_stop"); err != nil {
				logrus.WithError(err).Warn("未确认场次封口，保留已有视频等待恢复")
			}
		}
	}))

	// 监听直播结束事件
	ed.AddEventListener(listeners.LiveEnd, events.NewEventListener(func(event *events.Event) {
		l, ok := event.Object.(live.Live)
		if !ok {
			return
		}

		liveID := string(l.GetLiveId())
		manager.OnLiveEnd(liveID)
		if len(pipelines) > 0 && pipelines[0] != nil {
			if err := pipelines[0].EndRecordingSession(liveID, "normal"); err != nil {
				logrus.WithError(err).Error("无法登记场次结束，禁止自动发布")
			}
		}
	}))

	// 监听录制开始事件
	ed.AddEventListener(recorders.RecorderStart, events.NewEventListener(func(event *events.Event) {
		l, ok := event.Object.(live.Live)
		if !ok {
			return
		}

		liveID := string(l.GetLiveId())
		manager.OnRecordingStart(liveID)
	}))

	// 监听录制结束事件
	ed.AddEventListener(recorders.RecorderStop, events.NewEventListener(func(event *events.Event) {
		l, ok := event.Object.(live.Live)
		if !ok {
			return
		}

		liveID := string(l.GetLiveId())
		manager.OnRecordingStop(liveID)
	}))

	// 监听直播间初始化完成事件（用于保存初始信息）
	ed.AddEventListener(listeners.RoomInitializingFinished, events.NewEventListener(func(event *events.Event) {
		param, ok := event.Object.(live.InitializingFinishedParam)
		if !ok {
			return
		}

		liveID := string(param.GetLiveId())
		if liveID == "" {
			return
		}

		var l live.Live
		if param.InitializingLive != nil {
			l = param.InitializingLive
		} else if param.Live != nil {
			l = param.Live
		}

		if l == nil {
			return
		}

		url := l.GetRawUrl()
		platform := l.GetPlatformCNName()
		hostName := ""
		roomName := ""

		if param.Info != nil {
			hostName = param.Info.HostName
			roomName = param.Info.RoomName
		}

		// 更新直播间信息（会自动检测名称变更）
		manager.UpdateInfo(liveID, url, platform, hostName, roomName)
	}))

	logrus.Info("直播间状态持久化事件监听器已注册")
}
