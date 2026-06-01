package danmaku

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/recorders/danmaku/douyin"
)

// DouyinDanmakuRecorder 抖音弹幕录制器
type DouyinDanmakuRecorder struct {
	roomID     string
	cookies    string
	outputFile string
	cfg        configs.DanmakuConfig
	startAt    time.Time
	assWriter  *AssWriter
	client     *douyin.DouyinClient
	logger     *logrus.Entry
	count      int
	mu         sync.Mutex
	running    bool
	done       chan struct{}
}

// NewDouyinDanmakuRecorder 创建抖音弹幕录制器
func NewDouyinDanmakuRecorder(roomID, cookies, outputFile string, cfg configs.DanmakuConfig, logger *logrus.Entry) *DouyinDanmakuRecorder {
	return &DouyinDanmakuRecorder{
		roomID:     roomID,
		cookies:    cookies,
		outputFile: outputFile,
		cfg:        cfg,
		logger:     logger,
		done:       make(chan struct{}),
	}
}

// Start 开始弹幕录制
func (r *DouyinDanmakuRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return nil
	}

	r.startAt = time.Now()

	// 创建 ASS Writer
	assWriter, err := NewAssWriter(r.outputFile, r.startAt, r.cfg, "Douyin Danmaku")
	if err != nil {
		return err
	}
	r.assWriter = assWriter

	// 创建客户端
	r.client = douyin.NewDouyinClient(r.roomID, r.cookies, r.onDanmaku, r.logger)

	// 启动客户端
	if err := r.client.Start(ctx); err != nil {
		assWriter.Close()
		return err
	}

	r.running = true
	r.logger.Info("抖音弹幕录制已启动")

	return nil
}

// Stop 停止弹幕录制
func (r *DouyinDanmakuRecorder) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.running {
		return
	}

	r.running = false
	close(r.done)

	if r.client != nil {
		r.client.Stop()
	}

	if r.assWriter != nil {
		r.assWriter.Close()
	}

	r.logger.Infof("抖音弹幕录制已停止，共录制 %d 条弹幕", r.count)
}

// OutputFile 返回输出文件路径
func (r *DouyinDanmakuRecorder) OutputFile() string {
	return r.outputFile
}

// GetCount 返回已录制弹幕数量
func (r *DouyinDanmakuRecorder) GetCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// IsRunning 返回是否正在录制
func (r *DouyinDanmakuRecorder) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// GetStatus 返回弹幕录制状态
func (r *DouyinDanmakuRecorder) GetStatus() map[string]interface{} {
	r.mu.Lock()
	defer r.mu.Unlock()

	status := map[string]interface{}{
		"danmaku_running": r.running,
		"danmaku_count":  r.count,
		"danmaku_output":  r.outputFile,
	}
	if r.running {
		status["danmaku_start_time"] = r.startAt.Format(time.RFC3339)
	}
	return status
}

// onDanmaku 弹幕回调
func (r *DouyinDanmakuRecorder) onDanmaku(username, content string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.running || r.assWriter == nil {
		return
	}

	// 抖音弹幕不含颜色信息，使用默认白色 (16777215)
	r.assWriter.AddDanmaku(time.Now(), username, content, 16777215)
	r.count++
}
