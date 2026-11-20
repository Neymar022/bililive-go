//go:build !linux && !darwin

package subtitle

import "os"

func WorkerCompletionSupported() bool                   { return false }
func workerCompletionInode(os.FileInfo) (uint64, error) { return 0, ErrWorkerCompletionUnsupported }

// LockWorkerAttempt 在不支持的平台拒绝 v1，不提供无锁替身。
func LockWorkerAttempt(string) (func(), error) { return nil, ErrWorkerCompletionUnsupported }
