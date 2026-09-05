//go:generate go run go.uber.org/mock/mockgen -package mock -destination mock/mock.go github.com/bililive-go/bililive-go/src/pkg/events Dispatcher
package events

import (
	"container/list"
	"context"
	"errors"
	"sync"

	"github.com/bililive-go/bililive-go/src/instance"
	"github.com/bililive-go/bililive-go/src/interfaces"
	bilisentry "github.com/bililive-go/bililive-go/src/pkg/sentry"
)

func NewDispatcher(ctx context.Context) Dispatcher {
	ed := &dispatcher{
		saver: make(map[EventType]*list.List),
	}
	inst := instance.GetInstance(ctx)
	if inst != nil {
		inst.EventDispatcher = ed
	}
	return ed
}

type Dispatcher interface {
	interfaces.Module
	AddEventListener(eventType EventType, listener *EventListener)
	RemoveEventListener(eventType EventType, listener *EventListener)
	RemoveAllEventListener(eventType EventType)
	DispatchEvent(event *Event)
}

type dispatcher struct {
	sync.RWMutex
	saver map[EventType]*list.List // map<EventType, List<*EventListener>>
	tails map[string]chan struct{}
}

// WaitForOrderedEvents 等待该房间已派发的生命周期完成；取消等待不会撤销已发出的停止。
// 调用者须先阻止旧 listener 派发新事件，不能在该房间的事件 handler 内等待自己。
func WaitForOrderedEvents(ctx context.Context, ed Dispatcher, key string) error {
	d, ok := ed.(*dispatcher)
	if !ok {
		return errors.New("ordered event completion unavailable")
	}
	d.RLock()
	tail := d.tails[key]
	d.RUnlock()
	if tail == nil {
		return nil
	}
	select {
	case <-tail:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *dispatcher) Start(ctx context.Context) error {
	return nil
}

func (e *dispatcher) Close(ctx context.Context) {

}

func (e *dispatcher) AddEventListener(eventType EventType, listener *EventListener) {
	e.Lock()
	defer e.Unlock()
	listeners, ok := e.saver[eventType]
	if !ok || listener == nil {
		listeners = list.New()
		e.saver[eventType] = listeners
	}
	listeners.PushBack(listener)
}

func (e *dispatcher) RemoveEventListener(eventType EventType, listener *EventListener) {
	e.Lock()
	defer e.Unlock()
	listeners, ok := e.saver[eventType]
	if !ok || listeners == nil {
		return
	}
	for e := listeners.Front(); e != nil; e = e.Next() {
		if e.Value == listener {
			listeners.Remove(e)
		}
	}
	if listeners.Len() == 0 {
		delete(e.saver, eventType)
	}
}

func (e *dispatcher) RemoveAllEventListener(eventType EventType) {
	e.Lock()
	defer e.Unlock()
	e.saver = make(map[EventType]*list.List)
}

func (e *dispatcher) DispatchEvent(event *Event) {
	if event == nil {
		return
	}
	e.Lock()
	listeners, ok := e.saver[event.Type]
	if !ok || listeners == nil {
		e.Unlock()
		return
	}
	hs := make([]*EventListener, 0)
	for e := listeners.Front(); e != nil; e = e.Next() {
		hs = append(hs, e.Value.(*EventListener))
	}
	var previous, done chan struct{}
	if event.OrderKey != "" {
		if e.tails == nil {
			e.tails = make(map[string]chan struct{})
		}
		previous = e.tails[event.OrderKey]
		done = make(chan struct{})
		e.tails[event.OrderKey] = done
	}
	e.Unlock()
	bilisentry.Go(func() {
		if done != nil {
			defer func() {
				e.Lock()
				if e.tails[event.OrderKey] == done {
					delete(e.tails, event.OrderKey)
				}
				close(done)
				e.Unlock()
			}()
		}
		if previous != nil {
			<-previous
		}
		for _, h := range hs {
			h.Handler(event)
		}
	})
}
