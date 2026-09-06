package tools

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBToolsRecoveryProcess(t *testing.T) {
	if os.Getenv("BGO_TEST_PARSER_CHILD") == "1" {
		time.Sleep(time.Minute)
		return
	}
	supervisor := &btoolsSupervisor{}
	started := make(chan uint64, 4)
	done := make(chan error, 1)
	go func() {
		done <- supervisor.run(func() *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBToolsRecoveryProcess$")
			cmd.Env = append(os.Environ(), "BGO_TEST_PARSER_CHILD=1")
			return cmd
		}, func(generation uint64) { started <- generation })
	}()
	t.Cleanup(func() {
		supervisor.stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("parser supervisor did not stop")
		}
	})
	waitStarted := func() uint64 {
		t.Helper()
		select {
		case generation := <-started:
			return generation
		case <-time.After(5 * time.Second):
			t.Fatal("parser did not start")
			return 0
		}
	}
	first := waitStarted()
	start := time.Now()
	if supervisor.report(first, true, start) || supervisor.report(first, true, start.Add(3*time.Minute-time.Nanosecond)) {
		t.Fatal("recovered before the normal endpoint cooldown elapsed")
	}
	// 真正退出当前子进程后才启动下一代；不调用全局进程清理。
	var recovered atomic.Int32
	var callers sync.WaitGroup
	for range 24 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if supervisor.report(first, true, start.Add(3*time.Minute)) {
				recovered.Add(1)
			}
		}()
	}
	callers.Wait()
	if recovered.Load() != 1 {
		t.Fatalf("24 simultaneous failures must recover once, got %d", recovered.Load())
	}
	second := waitStarted()
	if second <= first {
		t.Fatal("parser generation did not advance")
	}
	if supervisor.report(first, true, start.Add(time.Hour)) {
		t.Fatal("stale in-flight response restarted the replacement")
	}
	supervisor.report(second, true, start.Add(4*time.Minute))
	if supervisor.report(second, true, start.Add(7*time.Minute)) {
		t.Fatal("recovery ignored the five minute restart limit")
	}
	supervisor.report(second, false, start.Add(8*time.Minute))
	if supervisor.report(second, true, start.Add(9*time.Minute)) {
		t.Fatal("successful live-info did not reset the blocked window")
	}
	supervisor.stop()
	if supervisor.report(second, true, start.Add(time.Hour)) {
		t.Fatal("shutdown allowed a parser restart")
	}
}

func TestBToolsRecoveryBoundsInheritedPipes(t *testing.T) {
	if os.Getenv("BGO_TEST_PARSER_DESCENDANT") == "1" {
		time.Sleep(8 * time.Second)
		return
	}
	if ready := os.Getenv("BGO_TEST_PARSER_PIPE_READY"); ready != "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBToolsRecoveryBoundsInheritedPipes$")
		cmd.Env = append(os.Environ(), "BGO_TEST_PARSER_DESCENDANT=1")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		return
	}
	supervisor := &btoolsSupervisor{}
	done := make(chan error, 1)
	ready := filepath.Join(t.TempDir(), "ready")
	started := make(chan uint64, 2)
	go func() {
		done <- supervisor.run(func() *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBToolsRecoveryBoundsInheritedPipes$")
			cmd.Env = append(os.Environ(), "BGO_TEST_PARSER_PIPE_READY="+ready)
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			return cmd
		}, func(generation uint64) { started <- generation })
	}()
	t.Cleanup(func() {
		supervisor.stop()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("parser pipe cleanup did not finish")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant not started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	first := <-started
	now := time.Now()
	supervisor.report(first, true, now)
	supervisor.report(first, true, now.Add(3*time.Minute))
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("inherited pipe blocked parser recovery")
	}
}

func TestBToolsStartFailureIsNotReady(t *testing.T) {
	supervisor := &btoolsSupervisor{}
	err := supervisor.run(func() *exec.Cmd { return exec.Command("/nonexistent/bgo-parser") }, nil)
	if err == nil || IsBToolsReady() || supervisor.snapshot() != 0 {
		t.Fatal("failed child reported ready")
	}
}
