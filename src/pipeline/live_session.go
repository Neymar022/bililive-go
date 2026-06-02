package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

func liveSessionIDFromValue(ctx context.Context, source any, liveID string) string {
	if source == nil || strings.TrimSpace(liveID) == "" {
		return ""
	}

	value := reflect.ValueOf(source)
	method := value.MethodByName("GetSessionsByLiveID")
	if !method.IsValid() {
		return ""
	}
	methodType := method.Type()
	if methodType.NumIn() != 3 || methodType.NumOut() != 2 {
		return ""
	}
	if !reflect.TypeOf(ctx).AssignableTo(methodType.In(0)) ||
		!reflect.TypeOf(liveID).AssignableTo(methodType.In(1)) ||
		!reflect.TypeOf(1).AssignableTo(methodType.In(2)) {
		return ""
	}

	results := method.Call([]reflect.Value{
		reflect.ValueOf(ctx),
		reflect.ValueOf(liveID),
		reflect.ValueOf(1),
	})
	if len(results) != 2 || !results[1].IsNil() {
		return ""
	}
	sessions := results[0]
	if sessions.Kind() != reflect.Slice || sessions.Len() == 0 {
		return ""
	}

	first := sessions.Index(0)
	if first.Kind() == reflect.Pointer {
		if first.IsNil() {
			return ""
		}
		first = first.Elem()
	}
	if first.Kind() != reflect.Struct {
		return ""
	}
	id := first.FieldByName("ID")
	if !id.IsValid() {
		return ""
	}
	switch id.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if id.Int() <= 0 {
			return ""
		}
		return fmt.Sprintf("%d", id.Int())
	case reflect.String:
		return strings.TrimSpace(id.String())
	default:
		return ""
	}
}
