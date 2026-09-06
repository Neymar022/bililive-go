package log

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
)

func TestLoggerRestartPreservesRetainedEvidence(t *testing.T) {
	if directory := os.Getenv("BGO_TEST_LOG_DIR"); directory != "" {
		cfg := configs.NewConfig()
		cfg.Log.OutPutFolder = directory
		cfg.Log.SaveEveryLog = false
		cfg.Log.SaveLastLog = true
		cfg.Log.RotateDays = 7
		configs.SetCurrentConfig(cfg)
		New(context.Background()).Info("restart-marker")
		return
	}
	for _, launcher := range []string{"", "1"} {
		t.Run("launcher="+launcher, func(t *testing.T) {
			directory := t.TempDir()
			now := time.Now()
			today := filepath.Join(directory, "bililive-go-"+now.Format("2006-01-02")+".log")
			yesterday := filepath.Join(directory, "bililive-go-"+now.AddDate(0, 0, -1).Format("2006-01-02")+".log")
			expired := filepath.Join(directory, "bililive-go-"+now.AddDate(0, 0, -9).Format("2006-01-02")+".log")
			unowned := filepath.Join(directory, "bililive-go-audit.log")
			for _, path := range []string{today, yesterday, expired, unowned} {
				if err := os.WriteFile(path, []byte("retained-evidence\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				cmd := exec.Command(os.Args[0], "-test.run=^TestLoggerRestartPreservesRetainedEvidence$")
				cmd.Env = append(os.Environ(), "BGO_TEST_LOG_DIR="+directory, "BILILIVE_LAUNCHER="+launcher)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("logger process: %v\n%s", err, output)
				}
			}
			for _, path := range []string{yesterday, unowned} {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "retained-evidence\n" {
					t.Errorf("restart lost evidence %s: %q, %v", filepath.Base(path), data, err)
				}
			}
			data, err := os.ReadFile(today)
			if err != nil || !strings.HasPrefix(string(data), "retained-evidence\n") || strings.Count(string(data), "restart-marker") != 2 {
				t.Errorf("restart did not append to today's log: %q, %v", data, err)
			}
			if _, err := os.Stat(expired); !os.IsNotExist(err) {
				t.Errorf("expired log retained: %v", err)
			}
		})
	}
}
