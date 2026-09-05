package listeners

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bluele/gcache"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/instance"
	livepkg "github.com/bililive-go/bililive-go/src/live"
	livemock "github.com/bililive-go/bililive-go/src/live/mock"
	"github.com/bililive-go/bililive-go/src/log"
	"github.com/bililive-go/bililive-go/src/pkg/events"
	evtmock "github.com/bililive-go/bililive-go/src/pkg/events/mock"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/bililive-go/bililive-go/src/types"
	gomock "go.uber.org/mock/gomock"
)

func TestRefresh(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	ed := evtmock.NewMockDispatcher(ctrl)
	cfg := configs.NewConfig()
	cfg.VideoSplitStrategies = configs.VideoSplitStrategies{
		OnRoomNameChanged: false,
	}
	configs.SetCurrentConfig(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, instance.Key, &instance.Instance{
		EventDispatcher: ed,
	})
	log.New(ctx)
	live := livemock.NewMockLive(ctrl)
	live.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	// 创建一个测试用的 LiveLogger
	testLogger := livelogger.New(1024, logrus.Fields{"test": "listener"})
	live.EXPECT().GetLogger().Return(testLogger).AnyTimes()
	l := NewListener(ctx, live).(*listener)

	// false -> false
	live.EXPECT().GetInfo().Return(&livepkg.Info{Status: false}, nil)
	live.EXPECT().GetRawUrl().Return("").AnyTimes()                 // 添加对GetRawUrl方法的期望调用
	live.EXPECT().GetPlatformCNName().Return("platform").AnyTimes() // 添加对GetPlatformCNName方法的期望调用
	l.refresh()
	assert.False(t, l.status.roomStatus)

	// false -> true
	live.EXPECT().GetInfo().Return(&livepkg.Info{Status: true}, nil)
	live.EXPECT().SetLastStartTime(gomock.Any())
	live.EXPECT().GetPlatformCNName().Return("platform").AnyTimes()
	ed.EXPECT().DispatchEvent(events.NewOrderedEvent(LiveStart, live, "room"))
	l.refresh()
	assert.True(t, l.status.roomStatus)

	// true -> true, roomName change
	live.EXPECT().GetInfo().Return(&livepkg.Info{Status: true, RoomName: "a"}, nil)
	live.EXPECT().GetRawUrl().Return("").AnyTimes()                 // 添加对GetRawUrl方法的期望调用
	live.EXPECT().GetPlatformCNName().Return("platform").AnyTimes() // 添加对GetPlatformCNName方法的期望调用
	l.refresh()

	// true -> true, roomName change
	cfg.VideoSplitStrategies.OnRoomNameChanged = true
	live.EXPECT().GetInfo().Return(&livepkg.Info{Status: true, RoomName: "b"}, nil)
	live.EXPECT().GetRawUrl().Return("").AnyTimes()                 // 添加对GetRawUrl方法的期望调用
	live.EXPECT().GetPlatformCNName().Return("platform").AnyTimes() // 添加对GetPlatformCNName方法的期望调用
	ed.EXPECT().DispatchEvent(events.NewOrderedEvent(RoomNameChanged, live, "room"))
	l.refresh()

	// 短暂离线不结束本场，满三分钟才发出结束事件。
	live.EXPECT().GetInfo().Return(&livepkg.Info{Status: false}, nil)
	live.EXPECT().GetRawUrl().Return("").AnyTimes() // 添加对GetRawUrl方法的期望调用
	live.EXPECT().GetPlatformCNName().Return("platform").AnyTimes()
	l.refresh()
	assert.True(t, l.status.roomStatus)
	l.offlineSince = time.Now().Add(-3 * time.Minute)
	live.EXPECT().GetInfo().Return(&livepkg.Info{Status: false}, nil)
	ed.EXPECT().DispatchEvent(events.NewOrderedEvent(LiveEnd, live, "room"))
	l.refresh()
	assert.False(t, l.status.roomStatus)
}

func TestRefreshWithError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	ed := evtmock.NewMockDispatcher(ctrl)
	cache := gcache.New(4).LRU().Build()
	configs.SetCurrentConfig(configs.NewConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, instance.Key, &instance.Instance{
		EventDispatcher: ed,
		Cache:           cache,
	})
	log.New(ctx)
	live := livemock.NewMockLive(ctrl)
	// 创建一个测试用的 LiveLogger
	testLogger := livelogger.New(1024, logrus.Fields{"test": "listener"})
	live.EXPECT().GetLogger().Return(testLogger).AnyTimes()
	l := NewListener(ctx, live).(*listener)

	live.EXPECT().GetInfo().Return(nil, errors.New("this is error"))
	live.EXPECT().GetRawUrl().Return("")
	live.EXPECT().GetPlatformCNName().Return("platform").AnyTimes() // 添加对GetPlatformCNName方法的期望调用
	l.refresh()
	assert.False(t, l.status.roomStatus)
}

func TestListenerStartAndClose(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	ed := evtmock.NewMockDispatcher(ctrl)
	cache := gcache.New(4).LRU().Build()
	config := configs.NewConfig()
	config.Interval = 5
	configs.SetCurrentConfig(config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, instance.Key, &instance.Instance{
		EventDispatcher: ed,
		Cache:           cache,
	})
	log.New(ctx)
	live := livemock.NewMockLive(ctrl)
	// 创建一个测试用的 LiveLogger
	testLogger := livelogger.New(1024, logrus.Fields{"test": "listener"})
	live.EXPECT().GetLogger().Return(testLogger).AnyTimes()
	live.EXPECT().GetInfo().Return(&livepkg.Info{Status: false}, nil).AnyTimes()
	live.EXPECT().GetInfoWithInterval(gomock.Any()).DoAndReturn(func(ctx context.Context) (*livepkg.Info, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}).AnyTimes()
	live.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	live.EXPECT().GetPlatformCNName().Return("platform").AnyTimes()
	live.EXPECT().GetRawUrl().Return("").AnyTimes() // 添加对GetRawUrl方法的期望调用
	ed.EXPECT().DispatchEvent(gomock.Any()).Times(2)
	l := NewListener(ctx, live)
	assert.NoError(t, l.Start())
	assert.NoError(t, l.Start())
	l.Close()
	l.Close()
}

func TestListenerStopPreventsLateLiveStartDispatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	ed := evtmock.NewMockDispatcher(ctrl)
	configs.SetCurrentConfig(configs.NewConfig())
	ctx := context.WithValue(context.Background(), instance.Key, &instance.Instance{EventDispatcher: ed})
	log.New(ctx)
	live := livemock.NewMockLive(ctrl)
	live.EXPECT().GetLogger().Return(livelogger.New(1024, nil)).AnyTimes()
	live.EXPECT().GetLiveId().Return(types.LiveID("room")).AnyTimes()
	live.EXPECT().GetRawUrl().Return("").AnyTimes()
	live.EXPECT().GetPlatformCNName().Return("platform").AnyTimes()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	live.EXPECT().SetLastStartTime(gomock.Any()).Do(func(time.Time) { close(entered); <-release })
	ed.EXPECT().DispatchEvent(events.NewOrderedEvent(ListenStop, live, "room"))
	l := NewListener(ctx, live).(*listener)
	l.state = running
	go func() {
		defer close(done)
		l.processInfo(&livepkg.Info{Status: true})
	}()
	<-entered
	l.Close()
	close(release)
	<-done
}
