package danmaku

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Akegarasu/blivedm-go/client"
	"github.com/Akegarasu/blivedm-go/message"
	"github.com/sirupsen/logrus"

	"github.com/bililive-go/bililive-go/src/configs"
)

// DanmakuRecorder connects to a Bilibili live room via WebSocket,
// receives danmaku messages, and writes them to an ASS subtitle file.
type DanmakuRecorder struct {
	roomID     int
	cookies    string
	outputFile string
	cfg        configs.DanmakuConfig
	startAt    time.Time
	assWriter  *AssWriter
	client     *client.Client
	logger     *logrus.Entry
	count      int
	mu         sync.Mutex
}

// NewDanmakuRecorder creates a new danmaku recorder.
func NewDanmakuRecorder(roomID int, cookies string, outputFile string, cfg configs.DanmakuConfig, logger *logrus.Entry) *DanmakuRecorder {
	return &DanmakuRecorder{
		roomID:     roomID,
		cookies:    cookies,
		outputFile: outputFile,
		cfg:        cfg,
		logger:     logger,
	}
}

// Start begins recording danmaku.
func (d *DanmakuRecorder) Start(ctx context.Context) error {
	d.startAt = time.Now()

	assWriter, err := NewAssWriter(d.outputFile, d.startAt, d.cfg, "Bilibili Danmaku")
	if err != nil {
		return fmt.Errorf("failed to create ass writer: %w", err)
	}
	d.assWriter = assWriter

	c := client.NewClient(d.roomID)
	if d.cookies != "" {
		c.SetCookie(d.cookies)
	}
	d.client = c

	c.OnDanmaku(func(msg *message.Danmaku) {
		d.mu.Lock()
		w := d.assWriter
		d.count++
		d.mu.Unlock()
		if w != nil {
			w.AddDanmaku(
				time.Now(),
				msg.Sender.Uname,
				msg.Content,
				msg.Extra.Color,
			)
		}
	})

	if d.cfg.RecordGift != nil && *d.cfg.RecordGift {
		c.OnGift(func(msg *message.Gift) {
			if msg.Num > 0 {
				d.mu.Lock()
				w := d.assWriter
				d.mu.Unlock()
				if w != nil {
					w.AddGift(time.Now(), msg.Uname, msg.GiftName, msg.Num)
				}
			}
		})
	}

	if d.cfg.RecordGuard != nil && *d.cfg.RecordGuard {
		c.OnGuardBuy(func(msg *message.GuardBuy) {
			d.mu.Lock()
			w := d.assWriter
			d.mu.Unlock()
			if w != nil {
				w.AddGuard(time.Now(), msg.Username, msg.GiftName)
			}
		})
	}

	if d.cfg.RecordSuperChat != nil && *d.cfg.RecordSuperChat {
		c.OnSuperChat(func(msg *message.SuperChat) {
			d.mu.Lock()
			w := d.assWriter
			d.mu.Unlock()
			if w != nil {
				w.AddSuperChat(time.Now(), msg.UserInfo.Uname, msg.Message, msg.Price)
			}
		})
	}

	if err := c.Start(); err != nil {
		assWriter.Close()
		return fmt.Errorf("failed to start danmaku client: %w", err)
	}

	go func() {
		<-ctx.Done()
		d.Stop()
	}()

	return nil
}

// Stop stops the danmaku recorder.
func (d *DanmakuRecorder) Stop() {
	d.mu.Lock()
	c := d.client
	w := d.assWriter
	d.client = nil
	d.assWriter = nil
	d.mu.Unlock()
	// 在锁外关闭，避免与回调死锁
	if c != nil {
		c.Stop()
	}
	if w != nil {
		w.Close()
	}
	d.mu.Lock()
	count := d.count
	d.mu.Unlock()
	if count > 0 {
		d.logger.Infof("弹幕录制已停止，共录制 %d 条弹幕 -> %s", count, d.outputFile)
	} else {
		d.logger.Info("弹幕录制已停止，未收到弹幕")
	}
}

// OutputFile returns the path of the ASS output file.
func (d *DanmakuRecorder) OutputFile() string {
	return d.outputFile
}

// GetCount returns the number of danmaku received so far.
func (d *DanmakuRecorder) GetCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

// IsRunning returns whether the danmaku WebSocket client is active.
func (d *DanmakuRecorder) IsRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.client != nil
}

// GetStatus returns the current danmaku recording status.
func (d *DanmakuRecorder) GetStatus() map[string]interface{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return map[string]interface{}{
		"danmaku_running": d.client != nil,
		"danmaku_count":   d.count,
		"danmaku_output":  d.outputFile,
	}
}
