//go:build !linux && !darwin

package servers

import (
	"context"
	"github.com/bililive-go/bililive-go/src/pipeline"
)

// WorkerSubmissionControl 在此平台不可用；旧 TCP 路径不受此可选控制接口影响。
type WorkerSubmissionControl struct{}

func StartWorkerSubmissionControl(string, *pipeline.Manager) (*WorkerSubmissionControl, error) {
	return nil, ErrWorkerSubmissionControlUnsupported
}
func (*WorkerSubmissionControl) Close(context.Context) error {
	return ErrWorkerSubmissionControlUnsupported
}
