//go:build linux

package subtitle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func CaptureLibraryFileState(path string, content bool) (LibraryFileState, error) {
	var s LibraryFileState
	info, err := os.Lstat(path)
	if err != nil {
		return s, err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return s, recoveryRefuse("unsafe_file_type", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return s, err
	}
	absolute, _ := filepath.Abs(path)
	if resolved != absolute {
		return s, recoveryRefuse("symlink_ancestor", path)
	}
	st := info.Sys().(*syscall.Stat_t)
	s = LibraryFileState{Path: absolute, Device: uint64(st.Dev), Inode: st.Ino, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(), UID: st.Uid, GID: st.Gid, Mode: uint32(info.Mode().Perm()) | uint32(st.Mode&07000), Xattrs: map[string][]byte{}}
	n, err := unix.Llistxattr(path, nil)
	if err != nil {
		return s, fmt.Errorf("capability_unavailable: listxattr %s: %w", path, err)
	}
	names := make([]byte, n)
	n, err = unix.Llistxattr(path, names)
	if err != nil {
		return s, err
	}
	for _, name := range strings.Split(string(names[:n]), "\x00") {
		if name == "" {
			continue
		}
		size, err := unix.Lgetxattr(path, name, nil)
		if err != nil {
			return s, err
		}
		value := make([]byte, size)
		read, err := unix.Lgetxattr(path, name, value)
		if err != nil {
			return s, err
		}
		s.Xattrs[name] = value[:read]
	}
	if content {
		if !info.Mode().IsRegular() {
			return s, recoveryRefuse("unsafe_file_type", path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return s, err
		}
		s.SHA = LibraryRecoveryDigest(b)
	}
	return s, nil
}
func libraryRecoverySingleLink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return recoveryRefuse("shared_nfo_inode", path)
	}
	return nil
}

// WriteLibraryRecoveryCopy 不以 chmod 修复继承差异；复制后属性不等立即拒绝。
func WriteLibraryRecoveryCopy(path string, data []byte, original LibraryFileState) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(original.Mode))
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid != original.UID || st.Gid != original.GID {
		if err = file.Chown(int(original.UID), int(original.GID)); err != nil {
			return fmt.Errorf("capability_unavailable: chown: %w", err)
		}
	}
	// 完整 xattr：删除继承的多余值，再复制包括 ACL 在内的原值。
	current, err := CaptureLibraryFileState(path, false)
	if err != nil {
		return err
	}
	for name := range current.Xattrs {
		if _, ok := original.Xattrs[name]; !ok {
			if err = unix.Fremovexattr(int(file.Fd()), name); err != nil {
				return err
			}
		}
	}
	for name, value := range original.Xattrs {
		if err = unix.Fsetxattr(int(file.Fd()), name, value, 0); err != nil {
			return fmt.Errorf("capability_unavailable: setxattr %s: %w", name, err)
		}
	}
	ts := []unix.Timespec{unix.NsecToTimespec(original.MtimeNS), unix.NsecToTimespec(original.MtimeNS)}
	if err = unix.UtimesNanoAt(unix.AT_FDCWD, path, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	copied, err := CaptureLibraryFileState(path, true)
	if err != nil {
		return err
	}
	if !sameRecoveryAttributes(original, copied) || copied.MtimeNS != original.MtimeNS || copied.SHA != LibraryRecoveryDigest(data) {
		return recoveryRefuse("capability_unavailable", path)
	}
	return SyncLibraryRecoveryDirectory(filepath.Dir(path))
}
func SyncLibraryRecoveryDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func libraryPublicationOSLock(ctx context.Context, season string) (func(), error) {
	// 所有本程序 writer 都使用同一路径；锁不在媒体扫描目录中。
	absolute := canonicalLibrarySeason(season)
	var err error
	root := filepath.Join(libraryPublicationRecoveryRoot(absolute), "locks")
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(root, LibraryRecoveryDigest([]byte(absolute))+".lock")
	fd, err := unix.Open(lock, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK {
			unix.Close(fd)
			return nil, err
		}
		select {
		case <-ctx.Done():
			unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
}
func LibraryRecoverySupported() bool { return true }

// ValidateLibraryRecoveryCapability 绑定一次安装能力证明，不构成每次人工在线许可。
func ValidateLibraryRecoveryCapability(library string, observed ...LibraryRecoveryPlatform) error {
	if len(observed) != 1 {
		return recoveryRefuse("platform_unconfirmed", library)
	}
	if err := ValidateLibraryRecoveryPlatform(observed[0], time.Now()); err != nil {
		return err
	}
	current, err := CaptureLibraryFileState(library, false)
	if err != nil {
		return err
	}
	private, err := CaptureLibraryFileState(LibraryRecoveryRoot(library), false)
	if err != nil {
		return recoveryRefuse("capability_unavailable", library)
	}
	if private.Device != current.Device || private.Mode != 0700 || private.UID != uint32(os.Geteuid()) || private.GID != uint32(os.Getegid()) {
		return recoveryRefuse("capability_unavailable", private.Path)
	}
	var c struct {
		Version             int              `json:"schema_version"`
		Library             string           `json:"canonical_library_root"`
		Device              uint64           `json:"library_dev"`
		Inode               uint64           `json:"library_inode"`
		UID                 int              `json:"service_uid"`
		GID                 int              `json:"service_gid"`
		WriterContract      string           `json:"mode"`
		PlatformBuildPath   string           `json:"platform_build_path"`
		PlatformBuildSHA256 string           `json:"platform_build_sha256"`
		InstalledAt         string           `json:"installed_at"`
		ProbeEvidenceSHA256 string           `json:"probe_evidence_sha256"`
		Attributes          LibraryFileState `json:"library_attributes"`
	}
	path := filepath.Join(LibraryRecoveryRoot(library), "capability.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return recoveryRefuse("capability_unavailable", path)
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return err
	}
	installed, timeErr := time.Parse(time.RFC3339Nano, c.InstalledAt)
	if c.Version != 2 || c.Library != library || c.Device != current.Device || c.Inode != current.Inode || c.UID != os.Geteuid() || c.GID != os.Getegid() || c.WriterContract != "exclusive_application_nfo_writer" || c.PlatformBuildPath != observed[0].BuildPath || c.PlatformBuildSHA256 != observed[0].BuildSHA256 || !validRecoverySHA256(c.PlatformBuildSHA256) || timeErr != nil || installed.IsZero() || installed.After(time.Now().Add(5*time.Second)) || !validRecoverySHA256(c.ProbeEvidenceSHA256) || !reflect.DeepEqual(c.Attributes.Xattrs, current.Xattrs) || !sameRecoveryAttributes(c.Attributes, current) {
		return recoveryRefuse("capability_unavailable", path)
	}
	return nil
}

func CheckLibraryRecoverySpace(library string, needed uint64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(library, &stat); err != nil {
		return err
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < needed {
		return recoveryRefuse("insufficient_recovery_space", library)
	}
	return nil
}
