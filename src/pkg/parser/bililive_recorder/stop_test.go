package bililive_recorder

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/kira1928/remotetools/pkg/tools"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if os.Getenv("BILILIVE_TEST_RECORDER_STOP_HELPER") == "1" {
		path := os.Args[4]
		if os.WriteFile(path+".started", nil, 0o644) != nil {
			os.Exit(2)
		}
		b := make([]byte, 1)
		if _, err := io.ReadFull(os.Stdin, b); err != nil || b[0] != 'q' {
			os.Exit(3)
		}
		if os.WriteFile(path, []byte("final recording segment"), 0o644) != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStopBeforeParseDoesNotStartBililiveRecorder(t *testing.T) {
	p, err := (&builder{}).Build(nil, livelogger.New(1024, nil))
	require.NoError(t, err)
	require.NoError(t, p.Stop())
	require.NoError(t, p.Stop())
	require.NotPanics(t, func() {
		require.NoError(t, p.ParseLiveStream(context.Background(), nil, nil, ""))
	})
	require.Zero(t, p.(*Parser).GetPID())
}

func TestBililiveRecorderStopWaitsForFinalOutput(t *testing.T) {
	t.Setenv("BILILIVE_TEST_RECORDER_STOP_HELPER", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, name := range []string{DotnetToolName, ToolName} {
		previous := tools.GetDevToolOverride(name)
		tools.SetDevToolOverride(name, executable)
		t.Cleanup(func() {
			if previous == "" {
				tools.ClearDevToolOverride(name)
			} else {
				tools.SetDevToolOverride(name, previous)
			}
		})
	}
	p, err := (&builder{}).Build(nil, livelogger.New(1024, nil))
	require.NoError(t, err)
	u, err := url.Parse("https://example.com/stream.flv")
	require.NoError(t, err)
	output := filepath.Join(t.TempDir(), "recording.bin")
	done := make(chan error, 1)
	go func() { done <- p.ParseLiveStream(context.Background(), &live.StreamUrlInfo{Url: u}, nil, output) }()
	t.Cleanup(func() {
		p.(*Parser).cmdLock.Lock()
		defer p.(*Parser).cmdLock.Unlock()
		if p.(*Parser).cmd != nil && p.(*Parser).cmd.Process != nil {
			_ = p.(*Parser).cmd.Process.Kill()
		}
	})
	require.Eventually(t, func() bool { _, err := os.Stat(output + ".started"); return err == nil }, 3*time.Second, time.Millisecond)
	require.NoError(t, p.Stop())
	require.NoError(t, p.Stop())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("录播姬没有完成正常停止")
	}
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "final recording segment", string(content))
}
