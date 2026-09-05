package events

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAddAndRemoveEventListener(t *testing.T) {
	d := NewDispatcher(context.Background()).(*dispatcher)
	l := NewEventListener(func(event *Event) {})
	d.AddEventListener("test", l)
	d.AddEventListener("test2", NewEventListener(func(event *Event) {}))
	ls, ok := d.saver["test"]
	assert.True(t, ok)
	assert.Equal(t, l, ls.Front().Value)
	d.RemoveEventListener("test", l)
	_, ok = d.saver["test"]
	assert.False(t, ok)
	d.RemoveAllEventListener("test2")
	assert.Empty(t, d.saver)
}

func TestDispatchEvent(t *testing.T) {
	l := make([]int, 0)
	done := make(chan struct{})
	d := NewDispatcher(context.Background()).(*dispatcher)
	d.AddEventListener("test", NewEventListener(func(event *Event) {
		l = append(l, 0)
	}))
	d.AddEventListener("test", NewEventListener(func(event *Event) {
		l = append(l, 1)
	}))
	d.AddEventListener("test", NewEventListener(func(event *Event) {
		l = append(l, 2)
	}))
	d.AddEventListener("test", NewEventListener(func(event *Event) {
		l = append(l, 3)
		close(done)
	}))
	d.DispatchEvent(NewEvent("test", nil))
	<-done
	assert.Equal(t, []int{0, 1, 2, 3}, l)
}

func TestOrderedEventsPreserveLifecycleWithoutBlockingOtherRooms(t *testing.T) {
	d := NewDispatcher(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	ended, other := make(chan struct{}), make(chan struct{})
	d.AddEventListener("start", NewEventListener(func(*Event) { close(entered); <-release }))
	d.AddEventListener("end", NewEventListener(func(*Event) { close(ended) }))
	d.AddEventListener("other", NewEventListener(func(*Event) { close(other) }))
	d.DispatchEvent(NewOrderedEvent("start", nil, "room"))
	<-entered
	d.DispatchEvent(NewOrderedEvent("end", nil, "room"))
	d.DispatchEvent(NewOrderedEvent("other", nil, "another-room"))
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Error("一个房间的生命周期阻塞了其他房间")
	}
	select {
	case <-ended:
		t.Error("关播事件越过尚未完成登记的开播事件")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("开播登记后关播事件未继续")
	}
}

func TestWaitForOrderedEventsHonorsRoomAndCancellation(t *testing.T) {
	d := NewDispatcher(context.Background())
	release := make(chan struct{})
	d.AddEventListener("stop", NewEventListener(func(*Event) { <-release }))
	d.DispatchEvent(NewOrderedEvent("stop", nil, "room"))
	assert.NoError(t, WaitForOrderedEvents(context.Background(), d, "other"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, WaitForOrderedEvents(ctx, d, "room"), context.DeadlineExceeded)
	close(release)
	assert.NoError(t, WaitForOrderedEvents(context.Background(), d, "room"))
}
