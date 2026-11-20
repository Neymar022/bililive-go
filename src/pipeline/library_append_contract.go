package pipeline

import (
	"context"
	"time"
)

// LibraryAppendCommitRequest 固定公开前的任务、成员与赋号前像。
type LibraryAppendCommitRequest struct {
	ExpectedTask       *PipelineTask
	ExpectedMembers    []*PipelineTask
	ExpectedSession    RecordingSession
	SeasonPath         string
	TargetPath         string
	OperationID        string
	AssignmentRevision int64
}

// LibraryAppendCommitter 在共用任务锁与前像校验内提交短文件操作。
type LibraryAppendCommitter interface {
	WithLibraryAppendCommit(context.Context, *LibraryAppendCommitRequest, func() error) error
}

// LibraryAggregateComputeState 记录当前来源代际仍需聚合计算及原调度时间。
type LibraryAggregateComputeState struct {
	SessionID  string
	Sources    []SessionMediaSource
	RequestIDs []string
	RequiredAt time.Time
	DueAt      time.Time
	Required   bool
}

// LibraryAggregateComputeController 复用现任务调度与共同锁控制聚合计算。
type LibraryAggregateComputeController interface {
	RequireLibraryAggregateCompute(context.Context, *LibraryAppendCommitRequest) (time.Time, error)
	AllowLibraryAggregateCompute(context.Context, *LibraryAppendCommitRequest) (bool, error)
}
