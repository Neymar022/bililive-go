package pipeline

import (
	"fmt"
	"time"
)

const DefaultRetryLaterDelay = 5 * time.Minute

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
	return fmt.Sprintf("retry later after %s: %s", e.Delay, e.Err)
}

func (e *RetryLaterError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
