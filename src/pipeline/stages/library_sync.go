package stages

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/bililive-go/bililive-go/src/pipeline"
)

type LibrarySyncStage struct {
	config   pipeline.StageConfig
	command  string
	commands []string
	logs     string
}

func NewLibrarySyncStage(config pipeline.StageConfig) (pipeline.Stage, error) {
	command := strings.TrimSpace(config.GetStringOption(pipeline.OptionCommand, ""))
	if command == "" {
		return nil, fmt.Errorf("library_sync stage requires 'command' option")
	}
	return &LibrarySyncStage{
		config:  config,
		command: command,
	}, nil
}

func (s *LibrarySyncStage) Name() string {
	return pipeline.StageNameLibrarySync
}

func (s *LibrarySyncStage) Execute(ctx *pipeline.PipelineContext, input []pipeline.FileInfo) ([]pipeline.FileInfo, error) {
	if len(input) == 0 {
		s.logs = "没有输入文件"
		return input, nil
	}

	s.commands = append(s.commands, s.command)
	stdout, stderr, err := executeShellCommand(ctx.Ctx, s.command)
	if stdout != "" {
		s.logs += fmt.Sprintf("stdout:\n%s\n", stdout)
	}
	if stderr != "" {
		s.logs += fmt.Sprintf("stderr:\n%s\n", stderr)
	}
	if err != nil {
		return nil, fmt.Errorf("library sync failed: %w", err)
	}

	s.logs += "展示库同步完成\n"
	return input, nil
}

func (s *LibrarySyncStage) GetCommands() []string {
	return s.commands
}

func (s *LibrarySyncStage) GetLogs() string {
	return s.logs
}

func executeShellCommand(ctx context.Context, cmdStr string) (stdout string, stderr string, err error) {
	var shell string
	var args []string

	switch runtime.GOOS {
	case "windows":
		shell = "cmd"
		args = []string{"/C", cmdStr}
	default:
		shell = "sh"
		args = []string{"-c", cmdStr}
	}

	cmd := exec.CommandContext(ctx, shell, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()
	if err != nil {
		return stdout, stderr, fmt.Errorf("%w: %s", err, stderr)
	}
	return stdout, stderr, nil
}
