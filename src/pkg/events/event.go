package events

type EventType string

type EventHandler func(event *Event)

type Event struct {
	Type     EventType
	Object   any
	OrderKey string
}

func NewEvent(eventType EventType, object any) *Event {
	return &Event{Type: eventType, Object: object}
}

// NewOrderedEvent 按实体串行派发状态变更，其他实体仍可独立运行。
func NewOrderedEvent(eventType EventType, object any, key string) *Event {
	return &Event{Type: eventType, Object: object, OrderKey: key}
}

type EventListener struct {
	Handler EventHandler
}

func NewEventListener(handler EventHandler) *EventListener {
	return &EventListener{handler}
}
