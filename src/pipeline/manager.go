package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/instance"
	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/pkg/events"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	bilisentry "github.com/bililive-go/bililive-go/src/pkg/sentry"
	"github.com/sirupsen/logrus"
)

// Manager 管道任务管理器
// 负责管理任务队列、持久化存储、并发执行控制
type Manager struct {
	ctx                      context.Context
	cancel                   context.CancelFunc
	store                    Store
	executor                 *Executor
	config                   *ManagerConfig
	runningTasks             map[int64]context.CancelFunc
	mu                       sync.RWMutex
	wg                       sync.WaitGroup
	eventDispatch            events.Dispatcher
	ticker                   *time.Ticker
	wake                     chan struct{}
	recordingSessions        map[string]string
	initializedRecordingRuns map[string]bool
	taskLocks                sync.Map
	gateMu                   sync.Mutex
	scheduleMu               sync.Mutex
	indexObserver            LibraryIndexObserver
	libraryIndexCursor       string
	recoveryHooks            LibraryRecoveryHooks
}

// ManagerConfig 管理器配置
type ManagerConfig struct {
	MaxConcurrent int           `yaml:"max_concurrent" json:"max_concurrent"` // 最大并发数
	PollInterval  time.Duration `yaml:"poll_interval" json:"poll_interval"`   // 轮询间隔
}

// DefaultManagerConfig 返回默认配置
func DefaultManagerConfig() *ManagerConfig {
	return &ManagerConfig{
		MaxConcurrent: 3,
		PollInterval:  5 * time.Second,
	}
}

// NewManager 创建管道管理器
func NewManager(ctx context.Context, store Store, config *ManagerConfig, dispatcher events.Dispatcher) *Manager {
	if config == nil {
		config = DefaultManagerConfig()
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 3
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 5 * time.Second
	}

	managerCtx, cancel := context.WithCancel(ctx)

	m := &Manager{
		ctx:               managerCtx,
		cancel:            cancel,
		store:             store,
		executor:          NewExecutor(logrus.StandardLogger()),
		config:            config,
		runningTasks:      make(map[int64]context.CancelFunc),
		eventDispatch:     dispatcher,
		wake:              make(chan struct{}, 1),
		recordingSessions: make(map[string]string),
	}

	if path := os.Getenv("BILILIVE_LIBRARY_INDEX_SNAPSHOT"); path != "" {
		m.indexObserver = NewLibraryIndexSnapshotObserver(path)
	}
	return m
}

// RegisterStage 注册阶段工厂
func (m *Manager) RegisterStage(name string, factory StageFactory) {
	m.executor.RegisterStage(name, factory)
}

// Start 启动管理器（实现 Module 接口）
func (m *Manager) Start(ctx context.Context) error {
	if err := m.restoreWorkerSubmissions(); err != nil {
		return err
	}
	if store, err := m.recordingStore(); err == nil {
		if err := store.RecoverRecordingSessions(m.ctx); err != nil {
			return fmt.Errorf("recover recording session closure: %w", err)
		}
	}
	// 重置所有运行中的任务（处理程序非正常退出的情况）
	if err := m.store.ResetRunningTasks(m.ctx); err != nil {
		logrus.WithError(err).Warn("failed to reset running pipeline tasks")
	}

	if err := m.recoverLibraryJournals(); err != nil {
		logrus.WithError(err).Warn("library recovery remains blocked")
	}
	// 启动轮询调度
	m.ticker = time.NewTicker(m.config.PollInterval)
	m.wg.Add(1)
	bilisentry.Go(func() { m.pollLoop() })

	logrus.Info("pipeline manager started")
	return nil
}

// Close 停止管理器（实现 Module 接口）
func (m *Manager) Close(ctx context.Context) {
	m.cancel()
	if m.ticker != nil {
		m.ticker.Stop()
	}

	// 等待所有任务完成
	m.wg.Wait()
	m.store.Close()
}

// pollLoop 轮询循环
func (m *Manager) pollLoop() {
	defer m.wg.Done()

	// 首次立即检查
	m.scheduleNextTasks()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.ticker.C:
			m.scheduleNextTasks()
		case <-m.wake:
			m.scheduleNextTasks()
		}
	}
}

// scheduleNextTasks 调度下一批任务
func (m *Manager) scheduleNextTasks() {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	if err := m.ReconcileSavedCompletions(); err != nil {
		logrus.WithError(err).Error("completion journal unavailable; scheduler stopped")
		return
	}
	m.compensateLibraryFailures()
	m.mu.RLock()
	runningCount := len(m.runningTasks)
	maxConcurrent := m.config.MaxConcurrent
	m.mu.RUnlock()

	// 检查是否还有空余槽位
	availableSlots := maxConcurrent - runningCount
	if availableSlots <= 0 {
		return
	}

	// 获取待执行的任务
	tasks, err := m.store.GetPendingTasks(m.ctx, 0)
	if err != nil {
		logrus.WithError(err).Error("failed to get pending pipeline tasks")
		return
	}

	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		if a.RecordInfo.StartTime.Equal(b.RecordInfo.StartTime) {
			return a.ID < b.ID
		}
		return a.RecordInfo.StartTime.Before(b.RecordInfo.StartTime)
	})
	for _, task := range tasks {
		m.mu.RLock()
		full := len(m.runningTasks) >= maxConcurrent
		m.mu.RUnlock()
		if full {
			break
		}
		if !m.libraryTaskCanStart(task) {
			continue
		}
		m.startTask(task)
	}
}

// startTask 启动任务执行
func (m *Manager) startTask(task *PipelineTask) {
	unlock := m.lockCompletionTask(task.ID)
	defer unlock()
	m.mu.Lock()
	// 检查是否已经在运行
	if _, exists := m.runningTasks[task.ID]; exists {
		m.mu.Unlock()
		return
	}

	// 创建任务上下文
	taskCtx, cancel := context.WithCancel(m.ctx)
	m.runningTasks[task.ID] = cancel
	m.mu.Unlock()

	// 原子领取；取出 pending 后的取消不能被旧对象覆盖。
	if store, ok := m.store.(*SQLiteStore); ok {
		if err := store.ClaimTask(m.ctx, task, time.Now()); err != nil {
			cancel()
			m.mu.Lock()
			delete(m.runningTasks, task.ID)
			m.mu.Unlock()
			return
		}
	} else {
		task.MarkStarted()
		if err := m.store.UpdateTask(m.ctx, task); err != nil {
			cancel()
			m.mu.Lock()
			delete(m.runningTasks, task.ID)
			m.mu.Unlock()
			return
		}
	}

	// 广播任务状态变化
	m.broadcastTaskUpdate(task)

	// 异步执行任务
	m.wg.Add(1)
	bilisentry.Go(func() {
		defer m.wg.Done()
		m.executeTask(taskCtx, task)
	})
}

// executeTask 执行任务
func (m *Manager) executeTask(ctx context.Context, task *PipelineTask) {
	claim := time.Time{}
	if task.StartedAt != nil {
		claim = *task.StartedAt
	}
	deferReason := ""
	var deferDue time.Time
	save := func(saveCtx context.Context) error {
		unlock := m.lockCompletionTask(task.ID)
		defer unlock()
		if store, ok := m.store.(*SQLiteStore); ok && !claim.IsZero() {
			return store.saveClaimedTaskDeadline(saveCtx, task, claim, deferReason, deferDue)
		}
		return m.store.UpdateTask(saveCtx, task)
	}
	defer func() {
		m.mu.Lock()
		delete(m.runningTasks, task.ID)
		m.mu.Unlock()
		m.wakeScheduler()
	}()

	logrus.WithFields(logrus.Fields{
		"task_id":       task.ID,
		"initial_files": len(task.InitialFiles),
	}).Info("starting pipeline task execution")

	// 构建执行上下文
	pipelineCtx := &PipelineContext{
		TaskID:     task.ID,
		Ctx:        ctx,
		RecordInfo: task.RecordInfo,
		Logger: livelogger.New(livelogger.DefaultBufferSize, logrus.Fields{
			"platform": task.RecordInfo.Platform,
			"host":     task.RecordInfo.HostName,
			"room":     task.RecordInfo.RoomName,
		}),
		WorkDir:    "", // 后续可以从配置获取
		StartStage: task.CurrentStage,
	}
	m.bindCompletionContext(pipelineCtx, task)
	pipelineCtx.LibraryPublication = m.EnsureLibraryPublication
	pipelineCtx.PreserveRecoverySources = func() bool { return m.librarySourcesProtected(task.ID) }
	pipelineCtx.SubtitleRecoveryRequired = func() (bool, error) { return m.subtitleRecoveryRequired(task.ID) }
	pipelineCtx.ShouldDeferStage = func(stageIndex int, stage StageConfig) (*time.Time, bool) {
		return m.shouldDeferStage(task, stageIndex, stage)
	}
	pipelineCtx.SessionMediaReady = func(sources []SessionMediaSource) (RecordingSession, error) {
		unlock := m.lockCompletionTask(task.ID)
		defer unlock()
		current, err := m.store.GetTask(m.ctx, task.ID)
		if err != nil || !sameCompletionTask(current, task) || current.Status == PipelineStatusCancelled {
			return RecordingSession{}, errors.New("completion_task_changed")
		}
		return m.completeSessionMedia(task, sources)
	}
	pipelineCtx.OnStageCompleted = func(stageIndex int, result StageResult, output []FileInfo) {
		task.CurrentStage = stageIndex + 1
		task.CurrentFiles = append([]FileInfo(nil), output...)
		task.UpdateProgress()
		if err := save(ctx); err != nil {
			logrus.WithError(err).Warn("failed to update pipeline task current files")
			if current, e := m.store.GetTask(m.ctx, task.ID); e == nil {
				m.broadcastTaskUpdate(current)
			}
			return
		}
		m.broadcastTaskUpdate(task)
	}

	// 执行管道
	previousResults := append([]StageResult(nil), task.StageResults...)
	results, err := m.executor.Execute(
		pipelineCtx,
		task.PipelineConfig,
		task.CurrentFiles,
		func(stageIndex int, stageName string, status StageStatus) {
			// 更新任务进度
			task.CurrentStage = stageIndex
			task.UpdateProgress()
			if err := save(ctx); err != nil {
				logrus.WithError(err).Warn("failed to update pipeline task progress")
				if current, e := m.store.GetTask(m.ctx, task.ID); e == nil {
					m.broadcastTaskUpdate(current)
				}
				return
			}
			m.broadcastTaskUpdate(task)
		},
	)

	// 保存阶段结果
	task.StageResults = append(previousResults, results...)

	if err != nil {
		var deferred *DeferredExecution
		if errors.As(err, &deferred) {
			deferReason = deferred.Reason
			deferDue = deferred.NotBefore
			task.MarkDeferred(deferred.NotBefore, deferred.StageIndex, deferred.CurrentFiles)
			if deferred.Err != nil {
				task.ErrorMessage = deferred.Err.Error()
			}
			logrus.WithFields(logrus.Fields{
				"task_id":    task.ID,
				"stage":      deferred.StageName,
				"not_before": deferred.NotBefore,
			}).Info("pipeline task deferred until scheduled subtitle window")
		} else if ctx.Err() == context.Canceled {
			task.MarkCancelled()
			logrus.WithField("task_id", task.ID).Info("pipeline task cancelled")
		} else {
			task.MarkFailed(err)
			logrus.WithError(err).WithField("task_id", task.ID).Error("pipeline task failed")
		}
	} else {
		task.MarkCompleted()
		// 更新最终文件列表
		if len(results) > 0 {
			lastResult := results[len(results)-1]
			task.CurrentFiles = lastResult.OutputFiles
		}
		logrus.WithField("task_id", task.ID).Info("pipeline task completed successfully")
	}

	// 更新任务状态
	if err := save(m.ctx); err != nil {
		logrus.WithError(err).Error("failed to update pipeline task status after execution")
		if current, e := m.store.GetTask(m.ctx, task.ID); e == nil {
			m.broadcastTaskUpdate(current)
		}
		return
	}

	// 广播任务状态变化
	m.broadcastTaskUpdate(task)
}

func (m *Manager) wakeScheduler() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) shouldDeferStage(task *PipelineTask, stageIndex int, stage StageConfig) (*time.Time, bool) {
	if stage.Name != StageNameSubtitleGenerate || !stage.GetBoolOption(OptionSubtitleScheduled, false) {
		return nil, false
	}
	if record, err := m.completionRecord(task.ID); err == nil && record.AggregateCompute != nil && record.AggregateCompute.Required && record.AggregateCompute.SessionID == task.RecordInfo.LiveSessionID {
		due := record.AggregateCompute.DueAt
		if !due.IsZero() && m.recoveryNow().Before(due) {
			return &due, true
		}
		if !due.IsZero() {
			return nil, false
		}
	}
	if m.completionResultOnly(task) {
		return nil, false
	}
	cfg := configs.GetCurrentConfig()
	if cfg == nil || !cfg.Subtitle.Schedule.Enabled {
		return nil, false
	}
	notBefore, err := cfg.Subtitle.Schedule.NextRunAfter(task.CreatedAt)
	if err != nil {
		logrus.WithError(err).Warn("invalid subtitle schedule, running subtitle stage immediately")
		return nil, false
	}
	if !time.Now().Before(notBefore) {
		return nil, false
	}
	return &notBefore, true
}

// broadcastTaskUpdate 广播任务更新事件
func (m *Manager) broadcastTaskUpdate(task *PipelineTask) {
	if m.eventDispatch != nil {
		m.eventDispatch.DispatchEvent(events.NewEvent(PipelineTaskUpdateEvent, task))
	}
}

// EnqueueTask 添加任务到队列
func (m *Manager) EnqueueTask(task *PipelineTask) error {
	if err := m.store.CreateTask(m.ctx, task); err != nil {
		return fmt.Errorf("failed to create pipeline task: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"task_id":       task.ID,
		"initial_files": len(task.InitialFiles),
		"total_stages":  task.TotalStages,
	}).Info("pipeline task enqueued")

	// 广播新任务事件
	m.broadcastTaskUpdate(task)

	// 立即尝试调度
	bilisentry.Go(func() { m.scheduleNextTasks() })

	return nil
}

// EnqueueRecordingTask 创建并入队录制完成后的处理任务
// 这是 recorder.go 调用的主要入口
func (m *Manager) EnqueueRecordingTask(
	info *live.Info,
	pipelineConfig *PipelineConfig,
	outputFiles []string,
	origins ...RecordingEnqueueOrigin,
) error {
	// 构建文件信息列表
	files := make([]FileInfo, len(outputFiles))
	for i, path := range outputFiles {
		files[i] = NewVideoFileInfo(path)
	}

	// 创建任务
	recordInfo := NewRecordInfo(info)
	if len(origins) > 0 {
		recordInfo.LiveSessionID = origins[0].SessionID
		recordInfo.RecordingProducerID = origins[0].ProducerID
		recordInfo.StartTime = origins[0].StartTime
	} else if inst := instance.GetInstance(m.ctx); inst != nil {
		recordInfo.LiveSessionID = liveSessionIDFromValue(m.ctx, inst.LiveStateStore, string(info.Live.GetLiveId()))
		if recordInfo.LiveSessionID == "" {
			recordInfo.LiveSessionID = liveSessionIDFromValue(m.ctx, inst.LiveStateManager, string(info.Live.GetLiveId()))
		}
	}
	task := NewPipelineTask(recordInfo, pipelineConfig, files)

	return m.EnqueueTask(task)
}

// CancelTask 取消任务
func (m *Manager) CancelTask(taskID int64) error {
	unlock := m.lockCompletionTask(taskID)
	defer unlock()
	task, err := m.store.GetTask(m.ctx, taskID)
	if err != nil {
		return err
	}

	if task.Status == PipelineStatusCompleted || task.Status == PipelineStatusCancelled {
		return nil
	}
	if store, ok := m.store.(*SQLiteStore); ok {
		if err := store.cancelTask(m.ctx, taskID); err != nil {
			return err
		}
	} else {
		task.MarkCancelled()
		if err := m.store.UpdateTask(m.ctx, task); err != nil {
			return err
		}
	}
	m.mu.Lock()
	cancel, isRunning := m.runningTasks[taskID]
	m.mu.Unlock()
	if isRunning {
		cancel()
	}
	task.MarkCancelled()
	m.broadcastTaskUpdate(task)

	return nil
}

// RetryTask 重试失败的任务
func (m *Manager) RetryTask(taskID int64) error {
	unlock := m.lockCompletionTask(taskID)
	defer unlock()
	task, err := m.store.GetTask(m.ctx, taskID)
	if err != nil {
		return err
	}

	if !task.CanRetry {
		return fmt.Errorf("task cannot be retried")
	}

	if task.Status != PipelineStatusFailed && task.Status != PipelineStatusCancelled {
		return fmt.Errorf("only failed or cancelled tasks can be retried")
	}

	if record, e := m.completionRecord(taskID); e == nil && len(record.CompletionRefs) > 0 {
		for i := range record.CompletionRefs {
			record.CompletionRefs[i].State = "superseded"
		}
		record.ReasonCode = "explicit_retry"
		if e = m.store.(*SQLiteStore).saveLibraryRecovery(m.ctx, record); e != nil {
			return e
		}
	} else if e != nil {
		if _, ok := m.store.(*SQLiteStore); ok {
			return e
		}
	}
	// 重置任务状态
	task.Status = PipelineStatusPending
	task.StartedAt = nil
	task.CompletedAt = nil
	task.NotBefore = nil
	task.ErrorMessage = ""
	task.CurrentStage = 0
	task.CurrentFiles = task.InitialFiles
	task.StageResults = nil
	task.Progress = 0

	if err := m.store.UpdateTask(m.ctx, task); err != nil {
		return err
	}

	m.broadcastTaskUpdate(task)

	// 立即尝试调度
	bilisentry.Go(func() { m.scheduleNextTasks() })

	return nil
}

// GetTask 获取任务详情
func (m *Manager) GetTask(taskID int64) (*PipelineTask, error) {
	task, err := m.store.GetTask(m.ctx, taskID)
	if err == nil {
		task.LibraryRecovery = m.libraryTaskRecovery(taskID)
	}
	return task, err
}

// ListTasks 列出任务
func (m *Manager) ListTasks(filter TaskFilter) ([]*PipelineTask, error) {
	return m.store.ListTasks(m.ctx, filter)
}

// DeleteTask 删除任务
func (m *Manager) DeleteTask(taskID int64) error {
	unlock := m.lockCompletionTask(taskID)
	defer unlock()
	// 不能删除运行中的任务
	m.mu.RLock()
	_, isRunning := m.runningTasks[taskID]
	m.mu.RUnlock()

	if isRunning {
		return fmt.Errorf("cannot delete running task")
	}

	return m.store.DeleteTask(m.ctx, taskID)
}

// ClearCompletedTasks 清除所有已完成的任务
func (m *Manager) ClearCompletedTasks() (int, error) {
	return m.store.DeleteTasksByStatus(m.ctx, PipelineStatusCompleted)
}

// GetStats 获取队列统计信息
func (m *Manager) GetStats() (*ManagerStats, error) {
	stats := &ManagerStats{
		MaxConcurrent: m.config.MaxConcurrent,
	}

	m.mu.RLock()
	stats.RunningCount = len(m.runningTasks)
	m.mu.RUnlock()

	// 获取各状态的任务数
	for _, status := range []PipelineStatus{
		PipelineStatusPending,
		PipelineStatusCompleted,
		PipelineStatusFailed,
		PipelineStatusCancelled,
	} {
		tasks, err := m.store.ListTasks(m.ctx, TaskFilter{Status: &status})
		if err != nil {
			return nil, err
		}
		switch status {
		case PipelineStatusPending:
			stats.PendingCount = len(tasks)
		case PipelineStatusCompleted:
			stats.CompletedCount = len(tasks)
		case PipelineStatusFailed:
			stats.FailedCount = len(tasks)
		case PipelineStatusCancelled:
			stats.CancelledCount = len(tasks)
		}
	}

	return stats, nil
}

// ManagerStats 管理器统计信息
type ManagerStats struct {
	MaxConcurrent  int `json:"max_concurrent"`
	RunningCount   int `json:"running_count"`
	PendingCount   int `json:"pending_count"`
	CompletedCount int `json:"completed_count"`
	FailedCount    int `json:"failed_count"`
	CancelledCount int `json:"cancelled_count"`
}

// GetManager 从实例获取管道管理器
func GetManager(inst *instance.Instance) *Manager {
	if inst == nil || inst.PipelineManager == nil {
		return nil
	}
	return inst.PipelineManager.(*Manager)
}
