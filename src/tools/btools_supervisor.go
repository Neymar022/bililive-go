package tools

import (
	"os"
	"os/exec"
	"sync"
	"time"

	blog "github.com/bililive-go/bililive-go/src/log"
)

// 只管理已有解析器进程；请求错误不能触发 FFmpeg、worker 或 app 重启。
type btoolsSupervisor struct {
	mu           sync.Mutex
	stopped      bool
	generation   uint64
	process      *os.Process
	restart      bool
	blockedSince time.Time
	lastRecovery time.Time
}

var managedBTools = &btoolsSupervisor{}

// BToolsGeneration 为请求绑定当前子进程，忽略重启前尚在途的响应。
func BToolsGeneration() uint64 { return managedBTools.snapshot() }

// ReportBToolsLiveInfo 只接收有效状态响应或已识别的全端点禁用错误。
func ReportBToolsLiveInfo(generation uint64, allEndpointsBlocked bool) {
	managedBTools.report(generation, allEndpointsBlocked, time.Now())
}

func (s *btoolsSupervisor) snapshot() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.process == nil || s.restart {
		return 0
	}
	return s.generation
}

func (s *btoolsSupervisor) report(generation uint64, blocked bool, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation == 0 || generation != s.generation || s.stopped || s.process == nil || s.restart {
		return false
	}
	if !blocked {
		s.blockedSince = time.Time{}
		return false
	}
	if s.blockedSince.IsZero() {
		s.blockedSince = now
	}
	// 先给上游默认三分钟解禁机会；持续禁用才恢复，且五分钟内最多一次。
	if now.Sub(s.blockedSince) < 3*time.Minute || (!s.lastRecovery.IsZero() && now.Sub(s.lastRecovery) < 5*time.Minute) {
		return false
	}
	if err := s.process.Kill(); err != nil {
		blog.GetLogger().WithError(err).Warn("恢复 bililive-tools 解析器失败")
		return false
	}
	s.restart = true
	s.lastRecovery = now
	currentBToolsStatus.Store(int32(BToolsStatusStarting))
	blog.GetLogger().Warn("抖音解析端点持续全部禁用，限频重启解析器；不停止录制或其他任务")
	return true
}

func (s *btoolsSupervisor) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.restart = false
	if s.process != nil {
		_ = s.process.Kill()
	}
}

func (s *btoolsSupervisor) run(command func() *exec.Cmd, onStarted func(uint64)) error {
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return nil
		}
		s.generation++
		generation := s.generation
		s.restart = false
		s.blockedSince = time.Time{}
		currentBToolsStatus.Store(int32(BToolsStatusStarting))
		s.mu.Unlock()

		cmd := command()
		// 后代可能继承日志管道；主进程退出后不能无限等待管道 EOF。
		cmd.WaitDelay = 2 * time.Second
		err := runWithKillOnCloseAndGetPID(cmd, func(pid int) {
			s.mu.Lock()
			if s.stopped {
				_ = cmd.Process.Kill()
				s.mu.Unlock()
				return
			}
			s.process = cmd.Process
			RegisterProcess("bililive-tools", pid, ProcessCategoryBTools)
			currentBToolsStatus.Store(int32(BToolsStatusReady))
			s.mu.Unlock()
			blog.GetLogger().Infof("bililive-tools process started with PID: %d, generation: %d", pid, generation)
			if onStarted != nil {
				onStarted(generation)
			}
		})

		s.mu.Lock()
		UnregisterProcess("bililive-tools")
		s.process = nil
		currentBToolsStatus.Store(int32(BToolsStatusFailed))
		restart := s.restart && !s.stopped
		s.mu.Unlock()
		if !restart {
			return err
		}
	}
}
