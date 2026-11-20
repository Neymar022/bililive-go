//go:build !linux && !darwin

package subtitle

import (
	"errors"
	"os"
)

func assignmentPlatformCheck() error { return errors.New("append_assignment_platform_unsupported") }

func assignmentPlatformIdentity(string, os.FileInfo) (AssignmentFileIdentity, error) {
	return AssignmentFileIdentity{}, assignmentPlatformCheck()
}

func assignmentOpenWorkLock(string) (*os.File, error) {
	return nil, assignmentPlatformCheck()
}

func assignmentCloseWorkLock(f *os.File) { _ = f.Close() }
