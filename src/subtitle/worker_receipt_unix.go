//go:build linux || darwin

package subtitle

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func WorkerCompletionSupported() bool { return true }
func workerCompletionInode(info os.FileInfo) (uint64, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("completion_identity_unavailable")
	}
	return st.Ino, nil
}

// LockWorkerAttempt 与 Python fcntl.flock 使用同一 inode；绝不持任务锁等待 writer。
func LockWorkerAttempt(attempt string) (func(), error) {
	resolved, e := filepath.EvalSymlinks(attempt)
	if e != nil || resolved != attempt || !filepath.IsAbs(attempt) {
		return nil, errors.New("completion_attempt_invalid")
	}
	p := filepath.Join(attempt, ".worker.lock")
	fd, err := syscall.Open(p, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	var info syscall.Stat_t
	if e := syscall.Fstat(fd, &info); e != nil || info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Mode&0077 != 0 {
		syscall.Close(fd)
		return nil, errors.New("completion_lock_invalid")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		syscall.Close(fd)
		return nil, errors.New("completion_active_writer")
	}
	return func() { syscall.Flock(fd, syscall.LOCK_UN); syscall.Close(fd) }, nil
}
