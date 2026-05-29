package pipeline

import (
	"time"
)

const DefaultRetryLaterDelay = 5 * time.Minute

// RetryLaterError 表示当前阶段只是依赖暂不可用，不应把任务标记为 failed。
type RetryLaterError struct {
	Err   error
	Delay time.Duration
}

func NewRetryLaterError(err error, delay time.Duration) *RetryLaterError {
	if delay <= 0 {
		delay = DefaultRetryLaterDelay
	}
	return &RetryLaterError{Err: err, Delay: delay}
}

func (e *RetryLaterError) Error() string {
	if e == nil || e.Err == nil {
		return "retry later"
	}
	return e.Err.Error()
}

func (e *RetryLaterError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
