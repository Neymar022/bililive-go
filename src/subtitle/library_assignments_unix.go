//go:build linux || darwin

package subtitle

import (
	"errors"
	"os"
	"syscall"
)

func assignmentPlatformCheck() error { return nil }

func assignmentPlatformIdentity(path string, info os.FileInfo) (AssignmentFileIdentity, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return AssignmentFileIdentity{}, errors.New("assignment file identity unavailable")
	}
	return AssignmentFileIdentity{path, uint64(st.Dev), uint64(st.Ino), info.Size(), info.ModTime().UnixNano()}, nil
}

func assignmentOpenWorkLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLibraryAssignmentWorkActive
		}
		return nil, err
	}
	return f, nil
}

func assignmentCloseWorkLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
