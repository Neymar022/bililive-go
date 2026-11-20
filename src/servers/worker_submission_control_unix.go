//go:build linux || darwin

package servers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bililive-go/bililive-go/src/pipeline"
)

// WorkerSubmissionControl 属于当前进程生命周期，只监听私有 Unix socket。
type WorkerSubmissionControl struct {
	server   *http.Server
	listener *net.UnixListener
	lock     *os.File
	path     string
	identity os.FileInfo
}
type workerControlOwner struct {
	Schema int    `json:"schema"`
	Kind   string `json:"kind"`
	UID    uint32 `json:"uid"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func controlPrivatePath(path string, private bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(absolute, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if e != nil {
			return e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || (st.Uid != uint32(os.Geteuid()) && st.Uid != 0) {
			return errors.New("worker control directory ownership invalid")
		}
		if info.Mode().Perm()&0022 != 0 && (info.Mode()&os.ModeSticky == 0 || st.Uid != 0) {
			return errors.New("worker control directory is writable by another account")
		}
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if private && (info.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid())) {
		return errors.New("worker control directory must be service-owned 0700")
	}
	return nil
}
func controlFileOwner(info os.FileInfo) workerControlOwner {
	st := info.Sys().(*syscall.Stat_t)
	return workerControlOwner{1, "worker_submissions", st.Uid, uint64(st.Dev), st.Ino}
}
func readControlOwner(path string) (workerControlOwner, error) {
	var owner workerControlOwner
	info, err := os.Lstat(path)
	if err != nil {
		return owner, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) {
		return owner, errors.New("worker control marker is not private")
	}
	f, err := os.Open(path)
	if err != nil {
		return owner, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&owner); err != nil {
		return owner, err
	}
	if decoder.Decode(new(any)) != io.EOF || owner.Schema != 1 || owner.Kind != "worker_submissions" || owner.UID != uint32(os.Geteuid()) {
		return owner, errors.New("worker control marker invalid")
	}
	return owner, nil
}
func writeControlOwner(path string, owner workerControlOwner) error {
	if _, err := os.Lstat(path); err == nil {
		if _, err = readControlOwner(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".control-owner-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncControlDirectory(filepath.Dir(path))
}
func syncControlDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// StartWorkerSubmissionControl 不改变普通 TCP router 的鉴权/路由体系。
func StartWorkerSubmissionControl(appData string, manager *pipeline.Manager) (*WorkerSubmissionControl, error) {
	if manager == nil || appData == "" {
		return nil, errors.New("worker control requires appdata and manager")
	}
	root, err := filepath.Abs(appData)
	if err != nil {
		return nil, err
	}
	if err = controlPrivatePath(root, false); err != nil {
		return nil, err
	}
	directory := filepath.Join(root, "worker-submissions")
	if err = os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if err = controlPrivatePath(directory, true); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(directory, ".control.lock")
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	success := false
	defer func() {
		if !success {
			_ = lock.Close()
		}
	}()
	info, err := lock.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("worker control lock invalid")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.New("worker control already active")
	}
	path := filepath.Join(directory, "control.sock")
	marker := filepath.Join(directory, ".socket-owner.json")
	if before, e := os.Lstat(path); e == nil {
		if before.Mode()&os.ModeSocket == 0 || before.Mode().Perm() != 0600 {
			return nil, errors.New("worker control path is not a socket")
		}
		owner, e := readControlOwner(marker)
		if e != nil || owner != controlFileOwner(before) {
			return nil, errors.New("worker control stale socket ownership unproven")
		}
		connection, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, errors.New("worker control listener is active")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, errors.New("worker control listener state unknown")
		}
		current, e := os.Lstat(path)
		if e != nil || !os.SameFile(before, current) {
			return nil, errors.New("worker control socket changed")
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	created, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	cleanup := func() {
		_ = listener.Close()
		if current, e := os.Lstat(path); e == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(created, current) {
			_ = os.Remove(path)
		}
	}
	if err = os.Chmod(path, 0600); err != nil {
		cleanup()
		return nil, err
	}
	identity, err := os.Lstat(path)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err = writeControlOwner(marker, controlFileOwner(identity)); err != nil {
		cleanup()
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/pipeline/worker-submissions", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			makeWorkerSubmissionsStatusHandler(manager)(w, r)
		case http.MethodPut:
			makeWorkerSubmissionsUpdateHandler(manager)(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	control := &WorkerSubmissionControl{server: server, listener: listener, lock: lock, path: path, identity: identity}
	success = true
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = manager.SetWorkerSubmissions(false)
		}
	}()
	return control, nil
}
func (c *WorkerSubmissionControl) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	err := c.server.Shutdown(ctx)
	if err != nil {
		_ = c.server.Close()
	}
	defer c.lock.Close()
	current, e := os.Lstat(c.path)
	if os.IsNotExist(e) {
		return err
	}
	if e != nil {
		return e
	}
	if current.Mode()&os.ModeSocket == 0 || !os.SameFile(c.identity, current) {
		return errors.New("worker control path replaced; retained")
	}
	marker := filepath.Join(filepath.Dir(c.path), ".socket-owner.json")
	owner, e := readControlOwner(marker)
	if e != nil || owner != controlFileOwner(current) {
		return errors.New("worker control marker changed; retained")
	}
	if e = os.Remove(c.path); e != nil {
		return e
	}
	if e = os.Remove(marker); e != nil {
		return e
	}
	if e = syncControlDirectory(filepath.Dir(c.path)); e != nil {
		return e
	}
	return err
}
