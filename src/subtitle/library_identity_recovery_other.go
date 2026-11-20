//go:build !linux

package subtitle

import "context"

func CaptureLibraryFileState(path string, content bool) (LibraryFileState, error) {
	return LibraryFileState{}, recoveryRefuse("capability_unavailable_non_linux", path)
}
func libraryRecoverySingleLink(path string) error {
	return recoveryRefuse("capability_unavailable_non_linux", path)
}
func WriteLibraryRecoveryCopy(path string, data []byte, original LibraryFileState) error {
	return recoveryRefuse("capability_unavailable_non_linux", path)
}
func SyncLibraryRecoveryDirectory(path string) error {
	return recoveryRefuse("capability_unavailable_non_linux", path)
}
func LibraryRecoverySupported() bool { return false }
func ValidateLibraryRecoveryCapability(path string, observed ...LibraryRecoveryPlatform) error {
	return recoveryRefuse("capability_unavailable_non_linux", path)
}

// 普通发布仍可使用原进程内锁；自动修复明确拒绝非 Linux。
func libraryPublicationOSLock(ctx context.Context, season string) (func(), error) {
	return func() {}, nil
}
func CheckLibraryRecoverySpace(library string, needed uint64) error {
	return recoveryRefuse("capability_unavailable_non_linux", library)
}
